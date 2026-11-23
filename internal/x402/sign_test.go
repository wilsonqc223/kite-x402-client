package x402

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Chesten223/kite-x402-client/internal/kite"
)

// A fixed throwaway key (never fund this address) so digest tests are
// deterministic. Generated with crypto.GenerateKey for this test suite.
const testPrivateKey = "0x8f2a1c4b0e5d3a7c6b9e2d4f1a8c7b5e3d6f9a2c5b8e1d4f7a2c5b8e3d6f9a1c"

func mustSigner(t *testing.T) *Signer {
	t.Helper()
	signer, err := NewSignerFromHex(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSignerFromHex: %v", err)
	}
	return signer
}

func testOption() PaymentRequirements {
	return PaymentRequirements{
		Scheme:            "exact",
		Network:           "eip155:2368",
		Amount:            "1000000000000000",
		Asset:             "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
		PayTo:             "0xC0FFEE0000000000000000000000000000C0FFEE",
		MaxTimeoutSeconds: 60,
		Extra:             map[string]any{"name": "pieUSD", "version": "1"},
	}
}

func TestAuthorizationDigestIsDeterministic(t *testing.T) {
	auth := Authorization{
		From:        "0xPayer",
		To:          "0xC0FFEE0000000000000000000000000000C0FFEE",
		Value:       "1000000000000000",
		ValidAfter:  "1700000000",
		ValidBefore: "1700000060",
		Nonce:       "0x" + strings.Repeat("ab", 32),
	}
	a, err := AuthorizationDigest(auth, kite.Testnet)
	if err != nil {
		t.Fatalf("AuthorizationDigest: %v", err)
	}
	b, err := AuthorizationDigest(auth, kite.Testnet)
	if err != nil {
		t.Fatalf("AuthorizationDigest: %v", err)
	}
	if a != b {
		t.Errorf("digest not deterministic: %s vs %s", a.Hex(), b.Hex())
	}

	// The same authorization on mainnet (different chain id, asset, and
	// EIP-712 domain) must hash differently.
	c, err := AuthorizationDigest(auth, kite.Mainnet)
	if err != nil {
		t.Fatalf("AuthorizationDigest: %v", err)
	}
	if a == c {
		t.Error("digest identical across chains, domain separation broken")
	}
}

func TestAuthorizationDigestRejectsMalformedInput(t *testing.T) {
	base := Authorization{
		From:        "0xabc",
		To:          "0xdef",
		Value:       "1",
		ValidAfter:  "0",
		ValidBefore: "60",
		Nonce:       "0x" + strings.Repeat("00", 32),
	}
	for _, mutate := range []func(*Authorization){
		func(a *Authorization) { a.Value = "not-a-number" },
		func(a *Authorization) { a.ValidAfter = "" },
		func(a *Authorization) { a.ValidBefore = "1.5" },
		func(a *Authorization) { a.Nonce = "0x1234" },
	} {
		bad := base
		mutate(&bad)
		if _, err := AuthorizationDigest(bad, kite.Testnet); err == nil {
			t.Errorf("AuthorizationDigest accepted malformed input: %+v", bad)
		}
	}
}

func TestBuildAndSignProducesVerifiableSignature(t *testing.T) {
	signer := mustSigner(t)
	option := testOption()
	now := time.Unix(1700000000, 0)

	payload, err := signer.BuildAndSign(option, ResourceInfo{URL: "/v1/forecast"}, now)
	if err != nil {
		t.Fatalf("BuildAndSign: %v", err)
	}

	// Wire shape.
	if payload.X402Version != 2 {
		t.Errorf("x402Version = %d, want 2", payload.X402Version)
	}
	if payload.Resource == nil || payload.Resource.URL != "/v1/forecast" {
		t.Errorf("resource = %+v, want the echoed challenge resource", payload.Resource)
	}
	if payload.Accepted.Amount != option.Amount || payload.Accepted.Network != option.Network {
		t.Errorf("accepted = %+v, want the offered option", payload.Accepted)
	}
	auth := payload.Payload.Authorization
	if auth.From != signer.Address() {
		t.Errorf("authorization.from = %s, want signer %s", auth.From, signer.Address())
	}
	if auth.To != payload.Accepted.PayTo || auth.Value != option.Amount {
		t.Errorf("authorization pays %s to %s, want %s to the checksummed payTo",
			auth.Value, auth.To, option.Amount)
	}
	if payload.Accepted.PayTo != "0xC0FfEE0000000000000000000000000000c0fFEe" {
		t.Errorf("payTo = %s, want EIP-55 checksummed form", payload.Accepted.PayTo)
	}
	if auth.ValidAfter != "0" { // the official v2 client pins validAfter to 0
		t.Errorf("validAfter = %s, want 0", auth.ValidAfter)
	}
	if want := "1700000060"; auth.ValidBefore != want { // now + 60s
		t.Errorf("validBefore = %s, want %s", auth.ValidBefore, want)
	}
	if len(strings.TrimPrefix(auth.Nonce, "0x")) != 64 {
		t.Errorf("nonce = %s, want 32 bytes", auth.Nonce)
	}

	// The signature must recover to the signer's address over the digest of
	// exactly the authorization in the payload — proving digest and signature
	// agree with the on-chain EIP-3009 verification the facilitator runs.
	digest, err := AuthorizationDigest(auth, kite.Testnet)
	if err != nil {
		t.Fatalf("AuthorizationDigest: %v", err)
	}
	payer, err := recoverPayer(digest, payload.Payload.Signature)
	if err != nil {
		t.Fatalf("recoverPayer: %v", err)
	}
	if payer.Hex() != signer.Address() {
		t.Errorf("signature recovers to %s, want %s", payer.Hex(), signer.Address())
	}

	// v must be in {27, 28}.
	raw, err := hex.DecodeString(strings.TrimPrefix(payload.Payload.Signature, "0x"))
	if err != nil || len(raw) != 65 {
		t.Fatalf("signature must be 65 bytes: %v", err)
	}
	if raw[64] != 27 && raw[64] != 28 {
		t.Errorf("signature v = %d, want 27 or 28", raw[64])
	}
}

func TestBuildAndSignUsesFreshNonces(t *testing.T) {
	signer := mustSigner(t)
	option := testOption()
	now := time.Unix(1700000000, 0)

	first, err := signer.BuildAndSign(option, ResourceInfo{URL: "/v1/forecast"}, now)
	if err != nil {
		t.Fatalf("BuildAndSign: %v", err)
	}
	second, err := signer.BuildAndSign(option, ResourceInfo{URL: "/v1/forecast"}, now)
	if err != nil {
		t.Fatalf("BuildAndSign: %v", err)
	}
	if first.Payload.Authorization.Nonce == second.Payload.Authorization.Nonce {
		t.Error("two payments used the same nonce — replayable")
	}
	if first.Payload.Signature == second.Payload.Signature {
		t.Error("two payments produced identical signatures")
	}
}

func TestBuildAndSignRejectsUnknownNetwork(t *testing.T) {
	signer := mustSigner(t)
	option := testOption()
	option.Network = "eip155:1"
	option.Asset = "0xother"
	if _, err := signer.BuildAndSign(option, ResourceInfo{URL: "/v1/forecast"}, time.Now()); err == nil {
		t.Error("BuildAndSign on an unknown network = nil error, want error")
	}
}

func TestNewSignerFromHexValidation(t *testing.T) {
	for _, bad := range []string{"", "0x1234", "zz" + strings.Repeat("0", 62), strings.Repeat("0", 63)} {
		if _, err := NewSignerFromHex(bad); err == nil {
			t.Errorf("NewSignerFromHex(%d chars) = nil error, want error", len(bad))
		}
	}

	signer := mustSigner(t)
	key, err := crypto.HexToECDSA(strings.TrimPrefix(testPrivateKey, "0x"))
	if err != nil {
		t.Fatalf("re-reading test key: %v", err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey).Hex()
	if signer.Address() != want {
		t.Errorf("Address() = %s, want %s", signer.Address(), want)
	}
	if _, ok := new(big.Int).SetString("1", 10); !ok {
		t.Fatal("sanity")
	}
}

func TestBuildAndSignRefusesMalformedServerValues(t *testing.T) {
	// payTo/asset come from the server. HexToAddress would silently pad or
	// truncate a wrong-length string into a different valid-looking address
	// and that rewritten address is the one that would get signed — so every
	// malformed value must be refused instead of normalized.
	signer := mustSigner(t)
	for name, mutate := range map[string]func(*PaymentRequirements){
		"short payTo":     func(o *PaymentRequirements) { o.PayTo = "0x1234" },
		"long payTo":      func(o *PaymentRequirements) { o.PayTo = "0x" + strings.Repeat("ab", 21) },
		"non-hex payTo":   func(o *PaymentRequirements) { o.PayTo = "0xzz00000000000000000000000000000000000000" },
		"short asset":     func(o *PaymentRequirements) { o.Asset = "0xabc" },
		"empty amount":    func(o *PaymentRequirements) { o.Amount = "" },
		"negative amount": func(o *PaymentRequirements) { o.Amount = "-5" },
		"text amount":     func(o *PaymentRequirements) { o.Amount = "lots" },
	} {
		option := testOption()
		mutate(&option)
		payload, err := signer.BuildAndSign(option, ResourceInfo{URL: "/v1/x"}, time.Unix(1700000000, 0))
		if err == nil {
			t.Errorf("%s: want a refusal, got a signed payload paying %s", name, payload.Payload.Authorization.To)
		}
	}
}
