// Package rpc is a minimal Ethereum JSON-RPC client for the Kite chains:
// just enough for chain-id checks, native balances, and ERC-20 balances —
// no node software, no abigen, plain HTTP.
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync/atomic"
)

// Client talks to one JSON-RPC endpoint.
type Client struct {
	url    string
	http   *http.Client
	nextID atomic.Int64
}

// New builds a client for an RPC endpoint.
func New(url string) *Client {
	return &Client{url: url, http: http.DefaultClient}
}

// WithHTTP swaps the underlying HTTP client (timeouts, proxies).
func (c *Client) WithHTTP(httpClient *http.Client) *Client {
	c.http = httpClient
	return c
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if len(e.Data) > 0 {
		return fmt.Sprintf("rpc %d: %s (%s)", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("rpc %d: %s", e.Code, e.Message)
}

// Call performs one JSON-RPC call and unmarshals result into out.
func (c *Client) Call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	request := rpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID.Add(1),
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshaling %s request: %w", method, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	defer resp.Body.Close()

	var decoded rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decoding %s response: %w", method, err)
	}
	if decoded.Error != nil {
		return fmt.Errorf("%s: %w", method, decoded.Error)
	}
	if err := json.Unmarshal(decoded.Result, out); err != nil {
		return fmt.Errorf("unmarshaling %s result %s: %w", method, string(decoded.Result), err)
	}
	return nil
}

// ChainID returns the chain id as reported by the endpoint.
func (c *Client) ChainID(ctx context.Context) (int64, error) {
	var hex string
	if err := c.Call(ctx, "eth_chainId", nil, &hex); err != nil {
		return 0, err
	}
	id, err := parseHexUint(hex)
	return int64(id), err
}

// NativeBalance returns the account's native (gas-token) balance in wei.
func (c *Client) NativeBalance(ctx context.Context, address string) (*big.Int, error) {
	var hex string
	if err := c.Call(ctx, "eth_getBalance", []interface{}{address, "latest"}, &hex); err != nil {
		return nil, err
	}
	return parseHexBig(hex)
}

// BlockNumber returns the latest block height.
func (c *Client) BlockNumber(ctx context.Context) (uint64, error) {
	var hex string
	if err := c.Call(ctx, "eth_blockNumber", nil, &hex); err != nil {
		return 0, err
	}
	return parseHexUint(hex)
}

// ERC20BalanceOf calls `balanceOf(address)` (0x70a08231) on an ERC-20
// contract and returns the holder's token balance in smallest units.
func (c *Client) ERC20BalanceOf(ctx context.Context, token, holder string) (*big.Int, error) {
	data := "0x70a08231" + paddedAddress(holder)
	var hex string
	if err := c.Call(ctx, "eth_call", []interface{}{
		map[string]string{"to": token, "data": data},
		"latest",
	}, &hex); err != nil {
		return nil, err
	}
	return parseHexBig(hex)
}

// paddedAddress renders a 0x address as 64 hex characters (ABI-encoded
// uint160, the argument shape balanceOf expects).
func paddedAddress(address string) string {
	clean := strings.TrimPrefix(strings.ToLower(address), "0x")
	for len(clean) < 64 {
		clean = "0" + clean
	}
	return clean
}

// parseHexUint parses a 0x-prefixed quantity into an uint64.
func parseHexUint(value string) (uint64, error) {
	if !strings.HasPrefix(value, "0x") {
		return 0, fmt.Errorf("quantity %q is not 0x-prefixed", value)
	}
	var out uint64
	for _, digit := range value[2:] {
		out <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			out |= uint64(digit - '0')
		case digit >= 'a' && digit <= 'f':
			out |= uint64(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			out |= uint64(digit-'A') + 10
		default:
			return 0, fmt.Errorf("quantity %q has a non-hex digit %q", value, digit)
		}
	}
	return out, nil
}

// parseHexBig parses a 0x-prefixed quantity into a big.Int.
func parseHexBig(value string) (*big.Int, error) {
	if !strings.HasPrefix(value, "0x") {
		return nil, fmt.Errorf("quantity %q is not 0x-prefixed", value)
	}
	out, ok := new(big.Int).SetString(strings.TrimPrefix(value, "0x"), 16)
	if !ok {
		return nil, fmt.Errorf("quantity %q is not hexadecimal", value)
	}
	return out, nil
}
