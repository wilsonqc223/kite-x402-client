package rpc

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRecordingRPC serves one canned result per method and records requests.
func newRecordingRPC(t *testing.T, results map[string]string) (*Client, *[]rpcRequest) {
	t.Helper()
	var requests []rpcRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		requests = append(requests, req)
		result, ok := results[req.Method]
		if !ok {
			result = "0x0"
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		})
	}))
	t.Cleanup(server.Close)
	return New(server.URL), &requests
}

func TestChainID(t *testing.T) {
	client, requests := newRecordingRPC(t, map[string]string{"eth_chainId": "0x940"})
	id, err := client.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	if id != 2368 {
		t.Errorf("ChainID = %d, want 2368", id)
	}
	if len(*requests) != 1 || (*requests)[0].Method != "eth_chainId" {
		t.Errorf("requests = %+v", *requests)
	}
}

func TestNativeBalance(t *testing.T) {
	client, requests := newRecordingRPC(t, map[string]string{"eth_getBalance": "0xde0b6b3a7640000"})
	balance, err := client.NativeBalance(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("NativeBalance: %v", err)
	}
	if balance.Cmp(big.NewInt(1000000000000000000)) != 0 {
		t.Errorf("balance = %s, want 1e18", balance)
	}
	req := (*requests)[0]
	if req.Method != "eth_getBalance" || len(req.Params) != 2 || req.Params[1] != "latest" {
		t.Errorf("request = %+v", req)
	}
}

func TestERC20BalanceOfEncodesTheRightCallData(t *testing.T) {
	client, requests := newRecordingRPC(t, map[string]string{"eth_call": "0x1"})
	if _, err := client.ERC20BalanceOf(context.Background(), "0xTOKEN", "0x00000000000000000000000000000000000000ff"); err != nil {
		t.Fatalf("ERC20BalanceOf: %v", err)
	}
	req := (*requests)[0]
	if req.Method != "eth_call" {
		t.Fatalf("method = %s", req.Method)
	}
	call := req.Params[0].(map[string]interface{})
	want := "0x70a08231" + strings.Repeat("0", 62) + "ff"
	if call["data"] != want {
		t.Errorf("data = %s, want %s", call["data"], want)
	}
	if call["to"] != "0xTOKEN" {
		t.Errorf("to = %s", call["to"])
	}
}

func TestRPCErrorSurfacesCodeAndMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]interface{}{"code": -32000, "message": "header not found"},
		})
	}))
	defer server.Close()
	client := New(server.URL)
	_, err := client.ChainID(context.Background())
	if err == nil || !strings.Contains(err.Error(), "-32000") || !strings.Contains(err.Error(), "header not found") {
		t.Errorf("err = %v, want rpc error with code and message", err)
	}
}

func TestParseHex(t *testing.T) {
	if got, _ := parseHexUint("0x940"); got != 2368 {
		t.Errorf("parseHexUint = %d", got)
	}
	if got, _ := parseHexBig("0x10"); got.Int64() != 16 {
		t.Errorf("parseHexBig = %s", got)
	}
	for _, bad := range []string{"", "2368", "0xZZ"} {
		if _, err := parseHexUint(bad); err == nil {
			t.Errorf("parseHexUint(%q) = nil error", bad)
		}
	}
	for _, bad := range []string{"", "4096", "0xGG"} {
		if _, err := parseHexBig(bad); err == nil {
			t.Errorf("parseHexBig(%q) = nil error", bad)
		}
	}
}

func TestPaddedAddress(t *testing.T) {
	if got := paddedAddress("0xAbC"); len(got) != 64 || !strings.HasSuffix(got, "abc") {
		t.Errorf("paddedAddress = %q", got)
	}
	// A full 40-hex address still encodes as a 64-digit uint160.
	want := strings.Repeat("0", 24) + strings.Repeat("ab", 20)
	if got := paddedAddress("0x" + strings.Repeat("ab", 20)); got != want {
		t.Errorf("full-length address changed: %q", got)
	}
}
