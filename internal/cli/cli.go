// Package cli holds the plumbing shared by the kx402 CLI and the kx402-mcp
// server: version stamping, egress-proxy client construction, key loading,
// and the probe/balance output runners.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/rpc"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

// Version is set at build time via -ldflags "-X …"; dev builds report dev.
var Version = "dev"

// VersionString is what `kx402 version` and the MCP serverInfo report.
func VersionString() string {
	return fmt.Sprintf("kx402 %s (github.com/wilsonqc223/kite-x402-client)", Version)
}

// HTTPClientFor builds an HTTP client. An empty proxy keeps Go's defaults;
// otherwise all traffic (including DNS, via remote-resolving socks) flows
// through the given URL, e.g. socks5://127.0.0.1:10800.
func HTTPClientFor(proxyURL string, timeout time.Duration) (*http.Client, error) {
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}, nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parsing proxy %q: %w", proxyURL, err)
	}
	if parsed.Scheme == "" {
		return nil, fmt.Errorf("parsing proxy %q: missing scheme (use socks5:// or http://)", proxyURL)
	}
	// Start from the default transport so proxying keeps everything it
	// configures — HTTP/2, connection pooling, dial and TLS handshake
	// timeouts — instead of silently dropping them.
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default transport %T", http.DefaultTransport)
	}
	cloned := transport.Clone()
	cloned.Proxy = http.ProxyURL(parsed)
	return &http.Client{Timeout: timeout, Transport: cloned}, nil
}

// LoadKey reads the wallet key from a file or KITE_PRIVATE_KEY. The key is
// only ever held in memory and returned — callers must not log it.
//
// A key file that other users on the machine can read is refused: a wallet
// key is only as safe as the file holding it. The check is skipped on
// Windows, where POSIX permission bits do not apply.
func LoadKey(path string) (string, error) {
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("reading key file: %w", err)
		}
		if runtime.GOOS != "windows" && info.Mode().IsRegular() && info.Mode().Perm()&0o077 != 0 {
			return "", fmt.Errorf(
				"refusing key file %s: mode %s lets other users read it (run chmod 600 %s)",
				path, info.Mode().Perm(), path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("reading key file: %w", err)
		}
		key := strings.TrimSpace(string(raw))
		if key == "" {
			return "", fmt.Errorf("key file %s is empty", path)
		}
		return key, nil
	}
	key := strings.TrimSpace(os.Getenv("KITE_PRIVATE_KEY"))
	if key == "" {
		return "", fmt.Errorf("no wallet: set KITE_PRIVATE_KEY or pass -key-file")
	}
	return key, nil
}

// EnvOrDefault returns the trimmed env value or the fallback when unset/blank.
func EnvOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// ChainByNetwork resolves a CAIP-2 id to a Kite chain; unknown networks get
// the zero chain and known=false so callers can fall back to raw units.
func ChainByNetwork(network string) (kite.Chain, bool) {
	chain, err := kite.ByNetwork(network)
	if err != nil {
		return kite.Chain{}, false
	}
	return chain, true
}

// Probe fetches a URL unpaid and pretty-prints the 402 challenge's
// advertised payment options into out.
func Probe(client *http.Client, rawURL string, out io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	header := resp.Header.Get(x402.HeaderPaymentRequired)
	if resp.StatusCode != http.StatusPaymentRequired || header == "" {
		return fmt.Errorf(
			"%s answered %s with no PAYMENT-REQUIRED header — not an x402 paid endpoint",
			rawURL, resp.Status)
	}

	challenge, err := x402.DecodeChallenge(header)
	if err != nil {
		return fmt.Errorf("decoding challenge: %w", err)
	}

	fmt.Fprintf(out, "x402 v%d challenge from %s\n", challenge.X402Version, rawURL)
	if challenge.Resource.Description != nil {
		fmt.Fprintf(out, "resource: %s\n", *challenge.Resource.Description)
	}
	if challenge.Error != nil {
		fmt.Fprintf(out, "error:    %s\n", *challenge.Error)
	}
	fmt.Fprintf(out, "%d payment option(s):\n", len(challenge.Accepts))
	for i, option := range challenge.Accepts {
		chain, known := ChainByNetwork(option.Network)
		fmt.Fprintf(out, "\n  [%d] %s\n", i, option.Network)
		fmt.Fprintf(out, "      price:  %s %s\n",
			FormatUnits(option.Amount, chain, known), chain.AssetSymbol)
		fmt.Fprintf(out, "      asset:  %s\n", option.Asset)
		fmt.Fprintf(out, "      payTo:  %s\n", option.PayTo)
		fmt.Fprintf(out, "      scheme: %s (max timeout %ds)\n",
			option.Scheme, option.MaxTimeoutSeconds)
		if name, ok := option.Extra["name"].(string); ok {
			version, _ := option.Extra["version"].(string)
			fmt.Fprintf(out, "      domain: %s v%s\n", name, version)
		}
	}
	return nil
}

// ProbeJSON fetches a URL unpaid and writes the decoded 402 challenge as
// indented JSON — the machine-readable counterpart of Probe for scripts
// and agents.
func ProbeJSON(client *http.Client, rawURL string, out io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	header := resp.Header.Get(x402.HeaderPaymentRequired)
	if resp.StatusCode != http.StatusPaymentRequired || header == "" {
		return fmt.Errorf(
			"%s answered %s with no PAYMENT-REQUIRED header — not an x402 paid endpoint",
			rawURL, resp.Status)
	}
	challenge, err := x402.DecodeChallenge(header)
	if err != nil {
		return fmt.Errorf("decoding challenge: %w", err)
	}
	encoded, err := json.MarshalIndent(challenge, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

// BalanceOut queries one chain and prints the wallet picture into out.
func BalanceOut(chain kite.Chain, rpcURL, address string, out io.Writer, httpClient *http.Client) error {
	client := rpc.New(rpcURL).WithHTTP(httpClient)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("asking %s for the chain id: %w", chain.RPCURL, err)
	}
	if chainID != chain.ChainID {
		return fmt.Errorf("%s reports chain id %d, expected %d — is the network right?",
			chain.RPCURL, chainID, chain.ChainID)
	}
	block, err := client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("fetching block number: %w", err)
	}
	native, err := client.NativeBalance(ctx, address)
	if err != nil {
		return fmt.Errorf("fetching native balance: %w", err)
	}
	stable, err := client.ERC20BalanceOf(ctx, chain.AssetAddress, address)
	if err != nil {
		return fmt.Errorf("fetching %s balance: %w", chain.AssetSymbol, err)
	}

	fmt.Fprintf(out, "%s (chain %d, block %d)\n", chain.Network, chainID, block)
	fmt.Fprintf(out, "%s  %s KITE\n", address, UnitsToDecimal(native.String(), 18))
	fmt.Fprintf(out, "%s  %s %s\n",
		strings.Repeat(" ", len(address)), UnitsToDecimal(stable.String(), int(chain.AssetDecimals)), chain.AssetSymbol)
	return nil
}

// FormatUnits renders an integer smallest-unit amount as a decimal string
// when the chain is known, falling back to the raw integer.
func FormatUnits(amount string, chain kite.Chain, known bool) string {
	if !known {
		return fmt.Sprintf("%s (raw units)", amount)
	}
	return UnitsToDecimal(amount, int(chain.AssetDecimals))
}

// UnitsToDecimal renders an integer smallest-unit amount with the given
// number of fractional digits (integer string math, no floats).
func UnitsToDecimal(amount string, decimals int) string {
	units, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return amount
	}
	if decimals == 0 {
		return units.String()
	}
	s := units.String()
	for len(s) <= decimals {
		s = "0" + s
	}
	s = s[:len(s)-decimals] + "." + s[len(s)-decimals:]
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
