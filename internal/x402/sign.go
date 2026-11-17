package x402

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"

	"github.com/Chesten223/kite-x402-client/internal/kite"
)

// Signer signs x402 payments with an EIP-3009 transferWithAuthorization
// for one Kite wallet.
type Signer struct {
	key  *ecdsa.PrivateKey
	from string
}

// NewSignerFromHex builds a Signer from a 0x-prefixed (or bare) 64-hex
// private key. The key is read from the environment by the CLI and never
// accepted as a loggable argument.
func NewSignerFromHex(privateKey string) (*Signer, error) {
	hexKey := strings.TrimPrefix(strings.TrimSpace(privateKey), "0x")
	if len(hexKey) != 64 {
		return nil, errors.New("private key must be 64 hex characters")
	}
	key, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return nil, fmt.Errorf("parsing private key: %w", err)
	}
	return &Signer{
		key:  key,
		from: crypto.PubkeyToAddress(key.PublicKey).Hex(),
	}, nil
}

// Address returns the checksummed payer address.
func (s *Signer) Address() string { return s.from }

// authorizationTypes mirrors the x402 TypeScript SDK's EIP-3009 types
// field for field (eip3009.ts); changing the field order or types changes
// the digest and breaks verification.
var authorizationTypes = apitypes.Types{
	"EIP712Domain": {
		{Name: "name", Type: "string"},
		{Name: "version", Type: "string"},
		{Name: "chainId", Type: "uint256"},
		{Name: "verifyingContract", Type: "address"},
	},
	"TransferWithAuthorization": {
		{Name: "from", Type: "address"},
		{Name: "to", Type: "address"},
		{Name: "value", Type: "uint256"},
		{Name: "validAfter", Type: "uint256"},
		{Name: "validBefore", Type: "uint256"},
		{Name: "nonce", Type: "bytes32"},
	},
}

// AuthorizationDigest computes the EIP-712 digest the payer signs for an
// EIP-3009 transferWithAuthorization on the given chain. Exported so tests
// (and curious users) can reproduce the exact bytes that get signed.
func AuthorizationDigest(auth Authorization, chain kite.Chain) (common.Hash, error) {
	if _, ok := new(big.Int).SetString(auth.Value, 10); !ok {
		return common.Hash{}, fmt.Errorf("value %q is not a decimal integer", auth.Value)
	}
	if _, ok := new(big.Int).SetString(auth.ValidAfter, 10); !ok {
		return common.Hash{}, fmt.Errorf("validAfter %q is not a decimal integer", auth.ValidAfter)
	}
	if _, ok := new(big.Int).SetString(auth.ValidBefore, 10); !ok {
		return common.Hash{}, fmt.Errorf("validBefore %q is not a decimal integer", auth.ValidBefore)
	}
	if len(strings.TrimPrefix(auth.Nonce, "0x")) != 64 {
		return common.Hash{}, errors.New("nonce must be a 32-byte hex string")
	}

	typed := apitypes.TypedData{
		Types:       authorizationTypes,
		PrimaryType: "TransferWithAuthorization",
		Domain: apitypes.TypedDataDomain{
			Name:              chain.EIP712Name,
			Version:           chain.EIP712Version,
			ChainId:           (*math.HexOrDecimal256)(big.NewInt(chain.ChainID)),
			VerifyingContract: chain.AssetAddress,
		},
		Message: apitypes.TypedDataMessage{
			"from":        common.HexToAddress(auth.From).Hex(),
			"to":          common.HexToAddress(auth.To).Hex(),
			"value":       auth.Value,
			"validAfter":  auth.ValidAfter,
			"validBefore": auth.ValidBefore,
			"nonce":       auth.Nonce,
		},
	}
	digest, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		return common.Hash{}, fmt.Errorf("hashing EIP-712 payload: %w", err)
	}
	return common.BytesToHash(digest), nil
}

// signDigest signs a 32-byte digest with the wallet key, returning the
// 0x-prefixed 65-byte r||s||v with v in {27, 28} — the format viem's
// signTypedData produces and the Kite facilitator expects.
func (s *Signer) signDigest(digest common.Hash) (string, error) {
	sig, err := crypto.Sign(digest[:], s.key)
	if err != nil {
		return "", fmt.Errorf("signing: %w", err)
	}
	if len(sig) != 65 {
		return "", fmt.Errorf("signature has %d bytes, want 65", len(sig))
	}
	if sig[64] < 27 {
		sig[64] += 27
	}
	return "0x" + hex.EncodeToString(sig), nil
}

// BuildAndSign turns one advertised payment option into a signed x402 v2
// payment payload: a fresh random nonce, a validity window matching the
// official v2 client (validAfter 0 .. now+maxTimeoutSeconds), and an
// EIP-3009 signature over it. The challenge's resource object is echoed
// into the payload — the facilitator requires it.
func (s *Signer) BuildAndSign(
	option PaymentRequirements,
	resource ResourceInfo,
	now time.Time,
) (PaymentPayload, error) {
	chain, err := kite.ByNetwork(option.Network)
	if err != nil {
		return PaymentPayload{}, fmt.Errorf("cannot pay on %s with this toolkit: %w", option.Network, err)
	}

	// The facilitator validates EVM addresses with a strict (EIP-55
	// checksum) parser, so echo every address checksummed.
	option.PayTo = common.HexToAddress(option.PayTo).Hex()
	option.Asset = common.HexToAddress(option.Asset).Hex()

	nonceBytes := make([]byte, 32)
	if _, err := rand.Read(nonceBytes); err != nil {
		return PaymentPayload{}, fmt.Errorf("generating nonce: %w", err)
	}
	nonce := "0x" + hex.EncodeToString(nonceBytes)

	auth := Authorization{
		From:        s.from,
		To:          option.PayTo,
		Value:       option.Amount,
		ValidAfter:  "0",
		ValidBefore: big.NewInt(now.Add(time.Duration(option.MaxTimeoutSeconds) * time.Second).Unix()).String(),
		Nonce:       nonce,
	}

	digest, err := AuthorizationDigest(auth, chain)
	if err != nil {
		return PaymentPayload{}, err
	}
	signature, err := s.signDigest(digest)
	if err != nil {
		return PaymentPayload{}, err
	}

	var payload PaymentPayload
	payload.X402Version = 2
	payload.Resource = &resource
	// The facilitator's schema requires description and mimeType as strings;
	// a challenge that omitted them gets empty values echoed back.
	if payload.Resource.Description == nil {
		empty := ""
		payload.Resource.Description = &empty
	}
	if payload.Resource.MimeType == nil {
		empty := ""
		payload.Resource.MimeType = &empty
	}
	payload.Accepted = option
	payload.Payload.Signature = signature
	payload.Payload.Authorization = auth
	return payload, nil
}

// recoverPayer extracts the signing address from a signature over a digest.
// Used in tests to prove the signature actually verifies.
func recoverPayer(digest common.Hash, signatureHex string) (common.Address, error) {
	sig, err := hex.DecodeString(strings.TrimPrefix(signatureHex, "0x"))
	if err != nil || len(sig) != 65 {
		return common.Address{}, errors.New("signature must be 65 bytes of hex")
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	pubkey, err := crypto.SigToPub(digest[:], sig)
	if err != nil {
		return common.Address{}, err
	}
	return crypto.PubkeyToAddress(*pubkey), nil
}
