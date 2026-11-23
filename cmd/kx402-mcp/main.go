// Command kx402-mcp exposes the kx402 toolkit as Model Context Protocol
// tools over stdio, so any MCP-capable AI agent can probe paid endpoints,
// check balances, and pay x402 invoices without leaving its chat.
//
// Protocol: MCP over stdio (JSON-RPC 2.0). Implements the 2024-11-05
// handshake (initialize / notifications/initialized), tools/list,
// tools/call, and ping.
//
// Tools:
//
//	kx402_probe    {url}                      — decode a 402 challenge
//	kx402_balance  {address?, network?}       — wallet picture on a chain
//	kx402_pay      {url, option?}             — pay (needs $KITE_PRIVATE_KEY)
//
// Usage:
//
//	kx402-mcp            # run under an MCP client (claude desktop, etc.)
//
// All egress honors KITE_PROXY if set (e.g. socks5://127.0.0.1:10800).
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/cli"
	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

const protocolVersion = "2024-11-05"

// ---- wire types (only what we use) ----

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

var tools = []toolDef{
	{
		Name: "kx402_probe",
		Description: "Fetch an x402-paid HTTP API endpoint unpaid and decode its 402 Payment Required " +
			"challenge: every advertised payment option with network, human-readable price, asset and payee.",
		InputSchema: objectSchema(map[string]any{
			"url": map[string]any{"type": "string", "description": "The endpoint URL to probe."},
		}, []string{"url"}),
	},
	{
		Name: "kx402_balance",
		Description: "Show an address's native KITE and stablecoin (pieUSD/USDC.e) balance on a Kite " +
			"network. Defaults: testnet, and the wallet address when KITE_PRIVATE_KEY is set.",
		InputSchema: objectSchema(map[string]any{
			"address": map[string]any{"type": "string", "description": "0x address to query; defaults to the configured wallet."},
			"network": map[string]any{"type": "string", "enum": []string{"testnet", "mainnet"}, "description": "Kite network (default testnet)."},
		}, nil),
	},
	{
		Name: "kx402_pay",
		Description: "Pay an x402-paid endpoint's 402 challenge with an EIP-3009 signature and return " +
			"the paid body plus the on-chain settlement receipt. Requires KITE_PRIVATE_KEY in the " +
			"server's environment; the key is never logged or returned.",
		InputSchema: objectSchema(map[string]any{
			"url":    map[string]any{"type": "string", "description": "The endpoint URL to pay."},
			"option": map[string]any{"type": "integer", "description": "Index of the challenge option to pay; default auto-selects."},
		}, []string{"url"}),
	},
}

func objectSchema(props map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func main() {
	proxy := os.Getenv("KITE_PROXY")
	serve(os.Stdin, os.Stdout, proxy)
}

// serve reads JSON-RPC frames line by line and writes one response per
// request; notifications get no reply. Split from main so tests can drive
// the whole protocol with pipes.
func serve(in io.Reader, out io.Writer, proxy string) {
	reader := bufio.NewReader(in)
	encoder := json.NewEncoder(out)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue // not a valid JSON-RPC frame; MCP transports are lossy-tolerant on notifications
		}
		if len(req.ID) == 0 {
			continue // notification (notifications/initialized etc.) — no response
		}
		result, rpcErr := dispatch(req, proxy)
		if rpcErr != nil {
			_ = encoder.Encode(response{JSONRPC: "2.0", ID: req.ID, Error: rpcErr})
			continue
		}
		_ = encoder.Encode(response{JSONRPC: "2.0", ID: req.ID, Result: result})
	}
}

func dispatch(req request, proxy string) (json.RawMessage, *rpcError) {
	switch req.Method {
	case "initialize":
		return mustJSON(map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "kx402", "version": cli.VersionString()},
		}), nil
	case "tools/list":
		return mustJSON(map[string]any{"tools": tools}), nil
	case "tools/call":
		return callTool(req.Params, proxy)
	case "ping":
		return mustJSON(map[string]any{}), nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
}

func callTool(params json.RawMessage, proxy string) (json.RawMessage, *rpcError) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments,omitempty"`
	}
	if err := json.Unmarshal(params, &call); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid tools/call params"}
	}

	var out toolCallResult
	var err error
	switch call.Name {
	case "kx402_probe":
		out, err = toolProbe(call.Arguments, proxy)
	case "kx402_balance":
		out, err = toolBalance(call.Arguments, proxy)
	case "kx402_pay":
		out, err = toolPay(call.Arguments, proxy)
	default:
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + call.Name}
	}
	if err != nil {
		out = toolCallResult{Content: []toolContent{{Type: "text", Text: err.Error()}}, IsError: true}
	}
	return mustJSON(out), nil
}

func toolProbe(args json.RawMessage, proxy string) (toolCallResult, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.URL == "" {
		return toolCallResult{}, fmt.Errorf("url is required")
	}
	client, err := httpClient(proxy, 30*time.Second)
	if err != nil {
		return toolCallResult{}, err
	}
	var buf strings.Builder
	if err := cli.Probe(client, in.URL, &buf); err != nil {
		return toolCallResult{}, err
	}
	return text(buf.String()), nil
}

func toolBalance(args json.RawMessage, proxy string) (toolCallResult, error) {
	var in struct {
		Address string `json:"address"`
		Network string `json:"network"`
	}
	_ = json.Unmarshal(args, &in)
	network := in.Network
	if network == "" {
		network = "testnet"
	}
	chain, err := kite.ByNetwork(network)
	if err != nil {
		return toolCallResult{}, err
	}
	address := strings.TrimSpace(in.Address)
	if address == "" {
		key, err := cli.LoadKey("")
		if err != nil {
			return toolCallResult{}, fmt.Errorf("address is required when no wallet key is configured: %w", err)
		}
		signer, err := x402.NewSignerFromHex(key)
		if err != nil {
			return toolCallResult{}, err
		}
		address = signer.Address()
	}
	client, err := httpClient(proxy, 15*time.Second)
	if err != nil {
		return toolCallResult{}, err
	}
	var buf strings.Builder
	if err := cli.BalanceOut(chain, chain.RPCURL, address, &buf, client); err != nil {
		return toolCallResult{}, err
	}
	return text(buf.String()), nil
}

func toolPay(args json.RawMessage, proxy string) (toolCallResult, error) {
	var in struct {
		URL    string `json:"url"`
		Option int    `json:"option"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.URL == "" {
		return toolCallResult{}, fmt.Errorf("url is required")
	}
	key, err := cli.LoadKey("")
	if err != nil {
		return toolCallResult{}, fmt.Errorf("pay needs a wallet: %w", err)
	}
	signer, err := x402.NewSignerFromHex(key)
	if err != nil {
		return toolCallResult{}, err
	}
	client, err := httpClient(proxy, 60*time.Second)
	if err != nil {
		return toolCallResult{}, err
	}
	option := in.Option
	if option == 0 {
		option = -1 // absent means auto-select
	}
	result, err := x402.Pay(client, in.URL, "GET", nil, nil, signer, option)
	if err != nil {
		return toolCallResult{}, err
	}
	var buf strings.Builder
	fmt.Fprintf(&buf, "status: %d paid: %v option: %d\n", result.Status, result.Paid, result.Option)
	if result.Receipt != nil {
		fmt.Fprintf(&buf, "receipt: tx %s on %s success=%v\n",
			result.Receipt.Transaction, result.Receipt.Network, result.Receipt.Success)
	}
	if result.Challenge != nil && result.Challenge.Error != nil {
		fmt.Fprintf(&buf, "server said: %s\n", *result.Challenge.Error)
	}
	fmt.Fprintf(&buf, "body: %s\n", result.Body)
	return text(buf.String()), nil
}

func text(s string) toolCallResult {
	return toolCallResult{Content: []toolContent{{Type: "text", Text: s}}}
}

func httpClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	return cli.HTTPClientFor(proxyURL, timeout)
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}
