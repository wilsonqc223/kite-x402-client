package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Chesten223/kite-x402-client/internal/kite"
)

// twoChainChallenge advertises a non-Kite EVM chain first and Kite second,
// so auto-select and an explicit 0 are distinguishable.
func twoChainChallengeJSON(price string) string {
	challenge := map[string]any{
		"x402Version": 2,
		"resource":    map[string]any{"url": "/v1/x", "description": "d", "mimeType": "application/json"},
		"accepts": []any{
			map[string]any{"scheme": "exact", "network": "eip155:8453", "amount": "1",
				"asset": "0x4200000000000000000000000000000000000006",
				"payTo": "0x00000000000000000000000000000000000000ff", "maxTimeoutSeconds": 60},
			map[string]any{"scheme": "exact", "network": kite.Testnet.Network, "amount": price,
				"asset": kite.Testnet.AssetAddress,
				"payTo": "0x00000000000000000000000000000000000000ff", "maxTimeoutSeconds": 60},
		},
	}
	raw, _ := json.Marshal(challenge)
	return base64.StdEncoding.EncodeToString(raw)
}

// paidMock answers 402 until a PAYMENT-SIGNATURE arrives, then 200 with a
// receipt, counting the payments it accepted.
func paidMock(t *testing.T, challengeB64 string) (*httptest.Server, *int32) {
	t.Helper()
	var accepted int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PAYMENT-SIGNATURE") != "" {
			atomic.AddInt32(&accepted, 1)
			receipt := base64.StdEncoding.EncodeToString([]byte(`{"success":true,"transaction":"0xt","network":"` + kite.Testnet.Network + `"}`))
			w.Header().Set("PAYMENT-RESPONSE", receipt)
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		w.Header().Set("PAYMENT-REQUIRED", challengeB64)
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	t.Cleanup(server.Close)
	return server, &accepted
}

// testKeyHex is a throwaway key used only by tests; never fund it.
const testKeyHex = "0x8f2a1c4b0e5d3a7c6b9e2d4f1a8c7b5e3d6f9a2c5b8e1d4f7a2c5b8e3d6f9a1c"

func callPay(t *testing.T, url string, option any) toolCallResult {
	t.Helper()
	arguments, _ := json.Marshal(map[string]any{"url": url, "option": option})
	params, _ := json.Marshal(map[string]any{"name": "kx402_pay", "arguments": json.RawMessage(arguments)})
	raw, rpcErr := callTool(params, "")
	if rpcErr != nil {
		t.Fatalf("tools/call: %v", rpcErr)
	}
	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPayHonorsAnExplicitOptionZero(t *testing.T) {
	t.Setenv("KITE_PRIVATE_KEY", testKeyHex)
	server, accepted := paidMock(t, twoChainChallengeJSON("1000000000000000"))

	// Auto-select steps over the non-Kite option and pays the Kite one.
	if result := callPay(t, server.URL+"/v1/x", nil); result.IsError {
		t.Fatalf("auto-select failed: %s", result.Content[0].Text)
	}
	// An explicit 0 names the first option itself — the non-Kite one, which
	// this toolkit cannot sign for. It must be reported as such, not
	// silently swapped for the auto-selected option.
	result := callPay(t, server.URL+"/v1/x", 0)
	if !result.IsError || !strings.Contains(result.Content[0].Text, "cannot pay on eip155:8453") {
		t.Errorf("option 0 must select the first option; got %+v", result)
	}
	if got := atomic.LoadInt32(accepted); got != 1 {
		t.Errorf("accepted %d payments, want 1 (the refused call must not be signed)", got)
	}
}

func TestPayRefusesPricesAboveTheOperatorCap(t *testing.T) {
	t.Setenv("KITE_PRIVATE_KEY", testKeyHex)
	t.Setenv("KITE_MAX_PAYMENT_USD", "0.01")
	// 5 pieUSD against a $0.01 cap.
	server, accepted := paidMock(t, twoChainChallengeJSON("5000000000000000000"))

	result := callPay(t, server.URL+"/v1/x", 1)
	if !result.IsError || !strings.Contains(result.Content[0].Text, "per-payment cap") {
		t.Errorf("want a cap refusal, got %+v", result)
	}
	if got := atomic.LoadInt32(accepted); got != 0 {
		t.Errorf("accepted %d payments, want 0", got)
	}
}
