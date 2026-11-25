package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/wilsonqc223/kite-x402-client/internal/kite"
	"github.com/wilsonqc223/kite-x402-client/internal/x402"
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

// newTestKeyHex returns a fresh, never-funded private key. Keys are made
// at runtime rather than committed: no private-key material belongs in the
// repository, not even as a test fixture.
func newTestKeyHex(t *testing.T) string {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generating a test key: %v", err)
	}
	return "0x" + hex.EncodeToString(crypto.FromECDSA(key))
}

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
	t.Setenv("KITE_PRIVATE_KEY", newTestKeyHex(t))
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
	t.Setenv("KITE_PRIVATE_KEY", newTestKeyHex(t))
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

func callInspect(t *testing.T, url string, option any) toolCallResult {
	t.Helper()
	arguments, _ := json.Marshal(map[string]any{"url": url, "option": option})
	params, _ := json.Marshal(map[string]any{"name": "kx402_inspect", "arguments": json.RawMessage(arguments)})
	raw, rpcErr := callTool(params, "")
	if rpcErr != nil {
		t.Fatalf("tools/call kx402_inspect: %v", rpcErr)
	}
	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestToolsListAdvertisesInspect(t *testing.T) {
	var out strings.Builder
	serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out, "")
	var reply struct {
		Result struct {
			Tools []toolDef `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &reply); err != nil {
		t.Fatal(err)
	}
	var inspect *toolDef
	for i, tool := range reply.Result.Tools {
		if tool.Name == "kx402_inspect" {
			inspect = &reply.Result.Tools[i]
		}
	}
	if inspect == nil {
		t.Fatal("tools/list does not advertise kx402_inspect")
	}
	if inspect.InputSchema["type"] != "object" || inspect.Description == "" {
		t.Errorf("kx402_inspect definition incomplete: %+v", inspect)
	}
	for _, prop := range []string{"url", "option"} {
		if _, ok := inspect.InputSchema["properties"].(map[string]any)[prop]; !ok {
			t.Errorf("kx402_inspect schema lacks property %q", prop)
		}
	}
}

func TestInspectChecksThePriceWithoutAWalletOrASignedRequest(t *testing.T) {
	// No KITE_PRIVATE_KEY anywhere: inspecting a price needs no wallet.
	t.Setenv("KITE_PRIVATE_KEY", "")
	server, accepted := paidMock(t, twoChainChallengeJSON("1000000000000000"))

	result := callInspect(t, server.URL+"/v1/x", 1)
	if result.IsError {
		t.Fatalf("kx402_inspect failed: %s", result.Content[0].Text)
	}
	report := result.Content[0].Text
	for _, want := range []string{
		"nothing was signed or sent",
		"pay would sign option 1",
		"0.001 pieUSD  (1000000000000000 units)",
		"within cap",
		"call kx402_pay with the same url to proceed",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("inspect report missing %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "wallet:") {
		t.Errorf("no wallet is configured, yet the report mentions one:\n%s", report)
	}
	if got := atomic.LoadInt32(accepted); got != 0 {
		t.Errorf("server received %d signed payments, want 0", got)
	}
}

func TestInspectReportsARefusalAsTheAnswerNotAnError(t *testing.T) {
	t.Setenv("KITE_PRIVATE_KEY", newTestKeyHex(t))
	t.Setenv("KITE_MAX_PAYMENT_USD", "0.01")
	// 5 pieUSD against a $0.01 cap.
	server, accepted := paidMock(t, twoChainChallengeJSON("5000000000000000000"))

	result := callInspect(t, server.URL+"/v1/x", 1)
	if result.IsError {
		t.Errorf("an unaffordable price is the answer, not a tool error: %+v", result)
	}
	report := result.Content[0].Text
	for _, want := range []string{"pay would REFUSE", "per-payment cap", "kx402_pay would refuse this payment"} {
		if !strings.Contains(report, want) {
			t.Errorf("inspect report missing %q:\n%s", want, report)
		}
	}
	if got := atomic.LoadInt32(accepted); got != 0 {
		t.Errorf("server received %d signed payments, want 0", got)
	}
}

func TestInspectSelectsTheSameOptionAsPay(t *testing.T) {
	// Option 0 is a chain this toolkit cannot sign for. Inspecting it must
	// name that option rather than quietly reporting the auto-selected one,
	// so the agent sees exactly what kx402_pay would do.
	t.Setenv("KITE_PRIVATE_KEY", "")
	server, _ := paidMock(t, twoChainChallengeJSON("1000000000000000"))

	auto := callInspect(t, server.URL+"/v1/x", nil)
	if auto.IsError || !strings.Contains(auto.Content[0].Text, "pay would sign option 1") {
		t.Errorf("auto-select should land on the Kite option 1: %+v", auto)
	}
	first := callInspect(t, server.URL+"/v1/x", 0)
	if first.IsError || !strings.Contains(first.Content[0].Text, "pay would sign option 0") ||
		!strings.Contains(first.Content[0].Text, "eip155:8453") {
		t.Errorf("an explicit option 0 must be reported as is: %+v", first)
	}
}

func TestInspectRequiresAURL(t *testing.T) {
	raw, rpcErr := callTool(json.RawMessage(`{"name":"kx402_inspect","arguments":{}}`), "")
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	var result toolCallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "url is required") {
		t.Errorf("missing url must be a tool error, got %+v", result)
	}
}

func TestCapRefusalsPointAtTheOperatorsKnobNotACommandLineFlag(t *testing.T) {
	t.Setenv("KITE_PRIVATE_KEY", newTestKeyHex(t))
	t.Setenv("KITE_MAX_PAYMENT_USD", "0.01")
	server, _ := paidMock(t, twoChainChallengeJSON("5000000000000000000"))

	reports := map[string]string{}
	inspect := callInspect(t, server.URL+"/v1/x", 1)
	reports["kx402_inspect"] = inspect.Content[0].Text
	pay := callPay(t, server.URL+"/v1/x", 1)
	if !pay.IsError {
		t.Fatalf("kx402_pay must refuse above the cap: %+v", pay)
	}
	reports["kx402_pay"] = pay.Content[0].Text

	for tool, report := range reports {
		if !strings.Contains(report, "KITE_MAX_PAYMENT_USD") {
			t.Errorf("%s: the refusal must tell the agent the cap is the operator's setting:\n%s", tool, report)
		}
		// There is no such flag on an MCP server; naming it would send the
		// agent looking for a knob that does not exist.
		if strings.Contains(report, "-max-usd") {
			t.Errorf("%s: the refusal names a CLI flag the agent cannot use:\n%s", tool, report)
		}
	}
}

func TestOnlyCapErrorsGetTheOperatorHint(t *testing.T) {
	plain := errors.New("something else went wrong")
	if got := withOperatorCapHint(plain); got != plain {
		t.Errorf("a non-cap error must pass through unchanged, got %v", got)
	}
	capErr := &x402.PriceCapError{Amount: "1", Network: "eip155:2368", CapUSD: "1"}
	if got := withOperatorCapHint(capErr); !strings.Contains(got.Error(), "KITE_MAX_PAYMENT_USD") {
		t.Errorf("a cap error must gain the operator hint, got %v", got)
	}
}
