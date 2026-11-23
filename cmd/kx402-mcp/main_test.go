package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func runMCP(t *testing.T, frames ...string) []response {
	t.Helper()
	var out bytes.Buffer
	var in strings.Builder
	for _, f := range frames {
		in.WriteString(f + "\n")
	}
	serve(strings.NewReader(in.String()), &out, "")
	var replies []response
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line == "" {
			continue
		}
		var r response
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unparseable reply %q: %v", line, err)
		}
		replies = append(replies, r)
	}
	return replies
}

func TestInitializeHandshake(t *testing.T) {
	replies := runMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
	)
	if len(replies) != 1 {
		t.Fatalf("notifications must not produce replies; got %d", len(replies))
	}
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name string `json:"name"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(replies[0].Result, &result); err != nil {
		t.Fatalf("unparseable initialize result: %v", err)
	}
	if result.ProtocolVersion != protocolVersion || result.ServerInfo.Name != "kx402" {
		t.Errorf("initialize = %+v", result)
	}
}

func TestToolsList(t *testing.T) {
	replies := runMCP(t, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var result struct {
		Tools []toolDef `json:"tools"`
	}
	if err := json.Unmarshal(replies[0].Result, &result); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range result.Tools {
		names[tool.Name] = true
		if tool.InputSchema["type"] != "object" {
			t.Errorf("%s input schema is not an object", tool.Name)
		}
	}
	for _, want := range []string{"kx402_probe", "kx402_balance", "kx402_pay"} {
		if !names[want] {
			t.Errorf("tools/list missing %s", want)
		}
	}
}

func TestPingAndUnknownMethod(t *testing.T) {
	replies := runMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":2,"method":"resources/list"}`,
	)
	if string(replies[0].Result) != "{}" {
		t.Errorf("ping result = %s", replies[0].Result)
	}
	if replies[1].Error == nil || replies[1].Error.Code != -32601 {
		t.Errorf("unknown method should be -32601, got %+v", replies[1].Error)
	}
}

func TestToolsCallValidation(t *testing.T) {
	replies := runMCP(t,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"kx402_probe","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`,
	)
	var first toolCallResult
	if err := json.Unmarshal(replies[0].Result, &first); err != nil {
		t.Fatal(err)
	}
	if !first.IsError || !strings.Contains(first.Content[0].Text, "url is required") {
		t.Errorf("missing url should be a tool error, got %+v", first)
	}
	if replies[1].Error == nil || replies[1].Error.Code != -32602 {
		t.Errorf("unknown tool should be -32602, got %+v", replies[1].Error)
	}
}

func TestGarbageLinesAreIgnored(t *testing.T) {
	replies := runMCP(t,
		`not json at all`,
		``,
		`{"jsonrpc":"2.0","id":9,"method":"ping"}`,
	)
	if len(replies) != 1 {
		t.Fatalf("garbage frames must be dropped; got %d replies", len(replies))
	}
}
