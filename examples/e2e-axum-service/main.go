// Command e2e-axum-service exercises kx402 against the real kite-x402-axum
// service binary: it starts a mock facilitator and mock upstream, launches
// the service (as a deployment would, purely via env vars), then runs the
// full client flow — probe the 402 challenge, pay it with an EIP-3009
// signature, and verify the settlement receipt.
//
// The facilitator here is a mock that always verifies and settles, so the
// run needs no chain funds. The signature format is the same one the real
// facilitator at facilitator.pieverse.io/v2 already accepted (see
// examples/facilitator-livecheck).
//
// Usage:
//
//	go run ./examples/e2e-axum-service -bin ../kite-x402-axum/target/debug/kite-x402-service
//
// Exits non-zero on any mismatch.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

const (
	payTo   = "0x00000000000000000000000000000000000000ff"
	testKey = "0x8f2a1c4b0e5d3a7c6b9e2d4f1a8c7b5e3d6f9a2c5b8e1d4f7a2c5b8e3d6f9a1c"
)

func main() {
	bin := flag.String("bin", os.Getenv("KITE_X402_AXUM_BIN"), "path to the kite-x402-service binary")
	flag.Parse()
	if *bin == "" {
		fmt.Fprintln(os.Stderr, "pass -bin <path to kite-x402-service> or set KITE_X402_AXUM_BIN")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*bin)
	fatalIf(err)
	if _, err := os.Stat(abs); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: service binary not found: %v\n", err)
		os.Exit(2)
	}

	facilitator := startMockFacilitator()
	defer facilitator.Close()
	upstream := startMockUpstream()
	defer upstream.Close()

	port := freePort()
	service := startService(abs, facilitator.URL, upstream.URL, port)
	defer func() {
		_ = service.Process.Kill()
		_, _ = service.Process.Wait()
	}()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitHealthy(base + "/healthz")

	// 1. Probe the challenge.
	challenge := mustProbe(base + "/v1/forecast")
	fmt.Printf("challenge: %d option(s), first %s units of %s on %s\n",
		len(challenge.Accepts), challenge.Accepts[0].Amount, challenge.Accepts[0].Asset, challenge.Accepts[0].Network)

	// 2. Pay it with a real EIP-3009 signature.
	signer, err := x402.NewSignerFromHex(testKey)
	fatalIf(err)
	result, err := x402.Pay(http.DefaultClient, base+"/v1/forecast", http.MethodGet, nil, nil, signer, 0)
	fatalIf(err)
	if !result.Paid || result.Status != http.StatusOK {
		fatalf("pay: paid=%v status=%d body=%s", result.Paid, result.Status, result.Body)
	}
	if result.Receipt == nil || !result.Receipt.Success || result.Receipt.Transaction != "0xe2e-settled" {
		fatalf("receipt: %+v", result.Receipt)
	}
	fmt.Printf("paid: status %d, body %s\n", result.Status, result.Body)
	fmt.Printf("receipt: tx %s on %s\n", result.Receipt.Transaction, result.Receipt.Network)

	// 3. The paid content must come from the mock upstream behind the gate.
	if string(result.Body) != `{"upstream":"e2e"}` {
		fatalf("upstream body mismatch: %s", result.Body)
	}
	fmt.Println("PASS: kx402 paid the kite-x402-axum service end to end")
}

func startMockFacilitator() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/verify":
			reply(w, map[string]any{"isValid": true, "payer": "0xe2e"})
		case "/settle":
			reply(w, map[string]any{
				"success":     true,
				"transaction": "0xe2e-settled",
				"network":     kite.Testnet.Network,
				"amount":      "1000000000000000",
			})
		case "/supported":
			reply(w, map[string]any{"kinds": []any{
				map[string]any{"x402Version": 2, "scheme": "exact", "network": kite.Testnet.Network},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func startMockUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"upstream":"e2e"}`))
	}))
}

func startService(bin, facilitatorURL, upstreamURL string, port int) *exec.Cmd {
	cmd := exec.Command(bin)
	// The service must reach the local mocks directly; strip inherited
	// proxy settings or reqwest would route loopback traffic through an
	// egress proxy.
	filtered := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name := entry
		if i := strings.IndexByte(entry, '='); i >= 0 {
			name = entry[:i]
		}
		switch name {
		case "ALL_PROXY", "all_proxy", "HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy":
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = append(filtered,
		fmt.Sprintf("PORT=%d", port),
		"PAY_TO="+payTo,
		"UPSTREAM_URL="+upstreamURL,
		"FACILITATOR_URL="+facilitatorURL,
		"KITE_NETWORK=testnet",
		"PRICE_USD=0.001",
		"RUST_LOG=warn",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fatalf("start service: %v", err)
	}
	return cmd
}
func mustProbe(url string) x402.PaymentRequired {
	resp, err := http.Get(url)
	fatalIf(err)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		fatalf("expected 402 from %s, got %d", url, resp.StatusCode)
	}
	challenge, err := x402.DecodeChallenge(resp.Header.Get(x402.HeaderPaymentRequired))
	fatalIf(err)
	if len(challenge.Accepts) == 0 {
		fatal("challenge advertises no options")
	}
	if challenge.Accepts[0].PayTo != payTo {
		fatalf("challenge payTo %s, want %s", challenge.Accepts[0].PayTo, payTo)
	}
	return challenge
}

func waitHealthy(url string) {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	fatalf("service did not become healthy at %s", url)
}

func reply(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func freePort() int {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	fatalIf(err)
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func fatalIf(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatal(msg string) { fatalf("%s", msg) }

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "e2e: "+format+"\n", args...)
	os.Exit(1)
}
