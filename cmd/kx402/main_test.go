package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if err := probe(server.URL+"/v1/forecast", &out); err != nil {
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
	if err := probe(server.URL, &out); err == nil {
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
		chain, known := chainByNetwork(tc.network)
		if got := formatUnits(tc.amount, chain, known); got != tc.want {
			t.Errorf("formatUnits(%q, %s) = %q, want %q", tc.amount, tc.network, got, tc.want)
		}
	}
}

func TestChainByNetworkUnknown(t *testing.T) {
	if _, known := chainByNetwork("eip155:1"); known {
		t.Error("chainByNetwork(eip155:1) known, want unknown")
	}
	if chain, known := chainByNetwork("eip155:2368"); !known || chain != kite.Testnet {
		t.Errorf("chainByNetwork(eip155:2368) = %v, %v", chain, known)
	}
}
