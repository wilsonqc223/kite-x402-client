package x402

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// A realistic 402 challenge in the exact wire shape the Kite x402 wrapper
// templates emit (base64 of camelCase JSON).
const challengeJSON = `{
  "x402Version": 2,
  "resource": {
    "url": "https://api.example.com/v1/forecast",
    "description": "Test wrapper",
    "mimeType": "application/json"
  },
  "accepts": [
    {
      "scheme": "exact",
      "network": "eip155:2368",
      "amount": "1000000000000000",
      "asset": "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
      "payTo": "0xC0FFEE0000000000000000000000000000C0FFEE",
      "maxTimeoutSeconds": 60,
      "extra": {"name": "pieUSD", "version": "1"}
    },
    {
      "scheme": "exact",
      "network": "eip155:2366",
      "amount": "50000",
      "asset": "0x7aB6f3ed87C42eF0aDb67Ed95090f8bF5240149e",
      "payTo": "0xC0FFEE0000000000000000000000000000C0FFEE",
      "maxTimeoutSeconds": 60,
      "extra": {"name": "Bridged USDC (Kite AI)", "version": "2"}
    }
  ]
}`

func TestDecodeChallenge(t *testing.T) {
	header := base64.StdEncoding.EncodeToString([]byte(challengeJSON))
	challenge, err := DecodeChallenge(header)
	if err != nil {
		t.Fatalf("DecodeChallenge: %v", err)
	}
	if challenge.X402Version != 2 {
		t.Errorf("x402Version = %d, want 2", challenge.X402Version)
	}
	if len(challenge.Accepts) != 2 {
		t.Fatalf("accepts len = %d, want 2", len(challenge.Accepts))
	}
	first := challenge.Accepts[0]
	if first.Scheme != "exact" || first.Network != "eip155:2368" {
		t.Errorf("first option = %+v", first)
	}
	if name, _ := first.Extra["name"].(string); name != "pieUSD" {
		t.Errorf("extra name = %q, want pieUSD", first.Extra["name"])
	}
	if challenge.Resource.URL != "https://api.example.com/v1/forecast" {
		t.Errorf("resource url = %q", challenge.Resource.URL)
	}
}

func TestDecodeChallengeRejectsGarbage(t *testing.T) {
	for _, header := range []string{"", "not base64!!", "aGVsbG8="} {
		if _, err := DecodeChallenge(header); err == nil {
			t.Errorf("DecodeChallenge(%q) = nil error, want error", header)
		}
	}
}

func TestEncodePayloadRoundTrip(t *testing.T) {
	var payload PaymentPayload
	payload.X402Version = 2
	payload.Accepted = PaymentRequirements{
		Scheme:            "exact",
		Network:           "eip155:2368",
		Amount:            "1000000000000000",
		Asset:             "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
		PayTo:             "0xC0FFEE0000000000000000000000000000C0FFEE",
		MaxTimeoutSeconds: 60,
	}
	payload.Payload.Signature = "0xsig"
	payload.Payload.Authorization = Authorization{
		From:        "0xPayer",
		To:          "0xC0FFEE0000000000000000000000000000C0FFEE",
		Value:       "1000000000000000",
		ValidAfter:  "0",
		ValidBefore: "9999999999",
		Nonce:       "0xnonce",
	}

	encoded, err := EncodePayload(payload)
	if err != nil {
		t.Fatalf("EncodePayload: %v", err)
	}

	// The wire must be camelCase JSON inside base64.
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("encoded value is not base64: %v", err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatalf("encoded value is not JSON: %v", err)
	}
	if _, ok := probe["x402Version"]; !ok {
		t.Error("missing x402Version key (camelCase)")
	}
	if _, ok := probe["accepted"]; !ok {
		t.Error("missing accepted key")
	}
	inner := probe["payload"].(map[string]any)
	if _, ok := inner["authorization"].(map[string]any)["validBefore"]; !ok {
		t.Error("missing authorization.validBefore key (camelCase)")
	}
}

func TestDecodeReceipt(t *testing.T) {
	body := `{"success":true,"transaction":"0xabc","network":"eip155:2368","amount":"1000000000000000"}`
	receipt, err := DecodeReceipt(base64.StdEncoding.EncodeToString([]byte(body)))
	if err != nil {
		t.Fatalf("DecodeReceipt: %v", err)
	}
	if !receipt.Success || receipt.Transaction != "0xabc" {
		t.Errorf("receipt = %+v", receipt)
	}
}

func TestFindMatchingRequirements(t *testing.T) {
	header := base64.StdEncoding.EncodeToString([]byte(challengeJSON))
	challenge, err := DecodeChallenge(header)
	if err != nil {
		t.Fatalf("DecodeChallenge: %v", err)
	}

	testnetMatches := FindMatchingRequirements(
		challenge.Accepts, "eip155:2368", "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A")
	if len(testnetMatches) != 1 || testnetMatches[0].Amount != "1000000000000000" {
		t.Errorf("testnet matches = %+v", testnetMatches)
	}

	mainnetMatches := FindMatchingRequirements(
		challenge.Accepts, "eip155:2366", "0x7aB6f3ed87C42eF0aDb67Ed95090f8bF5240149e")
	if len(mainnetMatches) != 1 || mainnetMatches[0].Amount != "50000" {
		t.Errorf("mainnet matches = %+v", mainnetMatches)
	}

	none := FindMatchingRequirements(challenge.Accepts, "eip155:1", "0xother")
	if len(none) != 0 {
		t.Errorf("unknown network matched %d options", len(none))
	}

	// A non-exact scheme must never match even with the right network/asset.
	var odd []PaymentRequirements
	for _, option := range challenge.Accepts {
		option.Scheme = "upto"
		odd = append(odd, option)
	}
	if got := FindMatchingRequirements(odd, "eip155:2368", odd[0].Asset); len(got) != 0 {
		t.Errorf("non-exact scheme matched %d options", len(got))
	}
}
