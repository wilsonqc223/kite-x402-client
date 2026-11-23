package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Chesten223/kite-x402-client/internal/cli"
	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

const challengeJSON = `{
  "x402Version": 2,
  "resource": {"url": "/v1/forecast", "description": "Test wrapper", "mimeType": "application/json"},
  "accepts": [
    {
      "scheme": "exact",
      "network": "eip155:2368",
      "amount": "1000000000000000",
      "asset": "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
      "payTo": "0xC0FFEE0000000000000000000000000000C0FFEE",
      "maxTimeoutSeconds": 60,
      "extra": {"name": "pieUSD", "version": "1"}
    }
  ]
}`

func newPaidEndpoint(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(
			x402.HeaderPaymentRequired,
			base64.StdEncoding.EncodeToString([]byte(challengeJSON)))
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte("{}"))
	}))
}

func TestProbeDecodesChallenge(t *testing.T) {
	server := newPaidEndpoint(t)
	defer server.Close()

	var out strings.Builder
	if err := cli.Probe(http.DefaultClient, server.URL+"/v1/forecast", &out); err != nil {
		t.Fatalf("probe: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"x402 v2 challenge",
		"eip155:2368",
		"0.001 pieUSD", // 1000000000000000 wei = 0.001 with 18 decimals
		"payTo:  0xC0FFEE0000000000000000000000000000C0FFEE",
		"domain: pieUSD v1",
		"1 payment option",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("probe output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestProbeRejectsOrdinaryEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	var out strings.Builder
	if err := cli.Probe(http.DefaultClient, server.URL, &out); err == nil {
		t.Error("probe on a free endpoint = nil error, want error")
	}
}

func TestFormatUnits(t *testing.T) {
	cases := []struct {
		amount  string
		network string
		want    string
	}{
		{"1000000000000000", "eip155:2368", "0.001"},
		{"50000", "eip155:2366", "0.05"},
		{"123", "eip155:2366", "0.000123"},
		{"1", "eip155:2368", "0.000000000000000001"},
		{"7", "eip155:9999", "7 (raw units)"},
	}
	for _, tc := range cases {
		chain, known := cli.ChainByNetwork(tc.network)
		if got := cli.FormatUnits(tc.amount, chain, known); got != tc.want {
			t.Errorf("cli.FormatUnits(%q, %s) = %q, want %q", tc.amount, tc.network, got, tc.want)
		}
	}
}

func TestBalanceOut(t *testing.T) {
	var mu sync.Mutex
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		calls++
		mu.Unlock()
		result := map[string]string{
			"eth_chainId":     "0x940",
			"eth_blockNumber": "0x64",
			"eth_getBalance":  "0xde0b6b3a7640000", // 1 KITE
			"eth_call":        "0x38d7ea4c68000",   // 0.001 * 10^18
		}[req.Method]
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "result": result,
		})
	}))
	defer server.Close()

	var out strings.Builder
	if err := cli.BalanceOut(kite.Testnet, server.URL, "0xAbC", &out, http.DefaultClient); err != nil {
		t.Fatalf("balanceOut: %v", err)
	}
	got := out.String()
	for _, want := range []string{"eip155:2368 (chain 2368, block 100)", "1 KITE", "0.001 pieUSD"} {
		if !strings.Contains(got, want) {
			t.Errorf("balance output missing %q\n%s", want, got)
		}
	}
	if calls != 4 {
		t.Errorf("made %d RPC calls, want 4", calls)
	}
}

func TestBalanceOutRejectsWrongChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0", "id": 1, "result": "0x1", // chain id 1
		})
	}))
	defer server.Close()
	if err := cli.BalanceOut(kite.Testnet, server.URL, "0xAbC", &strings.Builder{}, http.DefaultClient); err == nil {
		t.Error("balanceOut with mismatched chain id = nil error, want error")
	}
}

func TestChainByNetworkUnknown(t *testing.T) {
	if _, known := cli.ChainByNetwork("eip155:1"); known {
		t.Error("cli.ChainByNetwork(eip155:1) known, want unknown")
	}
	if chain, known := cli.ChainByNetwork("eip155:2368"); !known || chain != kite.Testnet {
		t.Errorf("cli.ChainByNetwork(eip155:2368) = %v, %v", chain, known)
	}
}
