// Command kx402 is a client toolkit for x402 paid HTTP APIs on the Kite AI
// chains: probe 402 challenges, pay with an EIP-3009 signature, and decode
// settlement receipts.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/rpc"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "probe":
		err = cmdProbe(os.Args[2:])
	case "balance":
		err = cmdBalance(os.Args[2:])
	case "pay":
		err = cmdPay(os.Args[2:])
	case "wallet":
		err = cmdWallet(os.Args[2:])
	case "receipt":
		err = cmdReceipt(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "kx402: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kx402: %v\n", err)
		os.Exit(1)
	}
}
func usage() {
	fmt.Fprint(os.Stderr, `kx402 — x402 client toolkit for the Kite AI chains

  kx402 probe <url>          Fetch a URL and decode its 402 payment challenge
  kx402 receipt <base64>     Decode a PAYMENT-RESPONSE settlement receipt
  kx402 balance [addr]       Native KITE + stablecoin balance on one chain

  kx402 pay <url>            Pay the 402 challenge and fetch the content
    -X, -method GET          HTTP method for the paid request
    -H, -header K:V          Extra header, repeatable
    -o, -option N            Pay accepts[N] instead of auto-selecting
    -key-file PATH           Private key file (else $KITE_PRIVATE_KEY)

Environment:
  KITE_PRIVATE_KEY  0x-prefixed private key used by 'pay' (never a CLI
                    argument, so it cannot leak into shell history)
  KITE_NETWORK      network for 'balance' (default testnet) and asset labels
`)
}

// cmdPay performs the full x402 payment flow for one URL.
func cmdPay(args []string) error {
	fs := flag.NewFlagSet("pay", flag.ExitOnError)
	method := fs.String("X", "GET", "HTTP method")
	fs.StringVar(method, "method", "GET", "HTTP method")
	data := fs.String("d", "", "request body: @file, - for stdin, or a literal string")
	fs.StringVar(data, "data", "", "request body: @file, - for stdin, or a literal string")
	var headers headerList
	fs.Var(&headers, "H", "extra request header K:V (repeatable)")
	fs.Var(&headers, "header", "extra request header K:V (repeatable)")
	option := fs.Int("o", -1, "index of the payment option to pay (default: auto)")
	fs.IntVar(option, "option", -1, "index of the payment option to pay (default: auto)")
	keyFile := fs.String("key-file", "", "private key file (default: $KITE_PRIVATE_KEY)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 pay [flags] <url>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}

	key, err := loadKey(*keyFile)
	if err != nil {
		return err
	}
	signer, err := x402.NewSignerFromHex(key)
	if err != nil {
		return err
	}
	body, err := readBody(*data)
	if err != nil {
		return err
	}

	result, err := x402.Pay(http.DefaultClient, fs.Arg(0), *method, body, headers, signer, *option)
	if err != nil {
		return err
	}

	if result.Paid {
		fmt.Fprintf(os.Stderr, "paid option %d as %s\n", result.Option, signer.Address())
		if result.Receipt != nil {
			chain, known := chainByNetwork(result.Receipt.Network)
			fmt.Fprintf(os.Stderr, "settled: tx %s on %s", result.Receipt.Transaction, result.Receipt.Network)
			if result.Receipt.Amount != nil {
				fmt.Fprintf(os.Stderr, " (%s %s)", formatUnits(*result.Receipt.Amount, chain, known), chain.AssetSymbol)
			}
			fmt.Fprintln(os.Stderr)
		}
	} else if result.Challenge != nil {
		if result.Challenge.Error != nil {
			fmt.Fprintf(os.Stderr, "payment rejected: %s\n", *result.Challenge.Error)
		} else {
			fmt.Fprintln(os.Stderr, "payment rejected")
		}
	} else {
		fmt.Fprintf(os.Stderr, "endpoint answered without a 402 challenge (status %d)\n", result.Status)
	}

	fmt.Printf("%s\n", result.Body)
	if result.Status >= 400 {
		os.Exit(2)
	}
	return nil
}

// headerList collects repeatable -H K:V flags.
type headerList [][2]string

func (h *headerList) String() string { return fmt.Sprint([]([2]string)(*h)) }

func (h *headerList) Set(value string) error {
	key, val, ok := strings.Cut(value, ":")
	if !ok {
		return fmt.Errorf("header %q must be K:V", value)
	}
	*h = append(*h, [2]string{strings.TrimSpace(key), strings.TrimSpace(val)})
	return nil
}

// loadKey reads the wallet key from a file or the environment.
func loadKey(path string) (string, error) {
	if path != "" {
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

// readBody resolves the -d flag: @file reads a file, - reads stdin, and
// anything else is a literal body string.
func readBody(spec string) ([]byte, error) {
	switch {
	case spec == "":
		return nil, nil
	case spec == "-":
		return io.ReadAll(os.Stdin)
	case strings.HasPrefix(spec, "@"):
		return os.ReadFile(strings.TrimPrefix(spec, "@"))
	default:
		return []byte(spec), nil
	}
}

// cmdProbe fetches a URL unpaid and pretty-prints the 402 challenge's
// advertised payment options.
func cmdProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 probe <url>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	return probe(fs.Arg(0), os.Stdout)
}

func probe(rawURL string, out io.Writer) error {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
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
		chain, known := chainByNetwork(option.Network)
		fmt.Fprintf(out, "\n  [%d] %s\n", i, option.Network)
		fmt.Fprintf(out, "      price:  %s %s\n",
			formatUnits(option.Amount, chain, known), chain.AssetSymbol)
		fmt.Fprintf(out, "      asset:  %s\n", option.Asset)
		fmt.Fprintf(out, "      payTo:  %s\n", option.PayTo)
		fmt.Fprintf(out, "      scheme: %s (max timeout %ds)\n",
			option.Scheme, option.MaxTimeoutSeconds)
		if name, ok := option.Extra["name"]; ok {
			fmt.Fprintf(out, "      domain: %s v%s\n", name, option.Extra["version"])
		}
	}
	return nil
}

// cmdReceipt decodes a base64 PAYMENT-RESPONSE header value.
func cmdReceipt(args []string) error {
	fs := flag.NewFlagSet("receipt", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "print the decoded receipt as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 receipt [--json] <base64 | ->")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	raw := fs.Arg(0)
	if raw == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		raw = strings.TrimSpace(string(data))
	}
	receipt, err := x402.DecodeReceipt(raw)
	if err != nil {
		return err
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(receipt, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
		return nil
	}
	status := "FAILED"
	if receipt.Success {
		status = "settled"
	}
	fmt.Printf("%s on %s\n", status, receipt.Network)
	fmt.Printf("transaction: %s\n", receipt.Transaction)
	if receipt.Payer != nil {
		fmt.Printf("payer:       %s\n", *receipt.Payer)
	}
	if receipt.Amount != nil {
		chain, known := chainByNetwork(receipt.Network)
		fmt.Printf("amount:      %s %s\n",
			formatUnits(*receipt.Amount, chain, known), chain.AssetSymbol)
	}
	if receipt.ErrorReason != nil {
		fmt.Printf("error:       %s\n", *receipt.ErrorReason)
	}
	if receipt.ErrorMessage != nil {
		fmt.Printf("message:     %s\n", *receipt.ErrorMessage)
	}
	return nil
}

// chainByNetwork resolves a CAIP-2 id to a Kite chain; unknown networks get
// the zero chain and known=false so callers can fall back to raw units.
func chainByNetwork(network string) (kite.Chain, bool) {
	chain, err := kite.ByNetwork(network)
	if err != nil {
		return kite.Chain{}, false
	}
	return chain, true
}

// formatUnits renders an integer smallest-unit amount as a decimal USD-style
// string when the chain is known, falling back to the raw integer.
func formatUnits(amount string, chain kite.Chain, known bool) string {
	if !known {
		return fmt.Sprintf("%s (raw units)", amount)
	}
	return unitsToDecimal(amount, int(chain.AssetDecimals))
}

// unitsToDecimal renders an integer smallest-unit amount with the given
// number of fractional digits (integer string math, no floats).
func unitsToDecimal(amount string, decimals int) string {
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

// cmdBalance shows native and stablecoin balances of an address on one Kite
// network. Without an argument (and with a key available) it uses the
// wallet's own address.
func cmdBalance(args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	network := fs.String("net", "", "testnet (default) or mainnet")
	keyFile := fs.String("key-file", "", "private key file (to default the address)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 balance [-net testnet|mainnet] [address]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		fs.Usage()
		os.Exit(2)
	}

	name := strings.TrimSpace(*network)
	if name == "" {
		name = strings.TrimSpace(os.Getenv("KITE_NETWORK"))
	}
	if name == "" {
		name = "testnet"
	}
	chain, err := kite.ByNetwork(name)
	if err != nil {
		return err
	}

	address := ""
	if fs.NArg() == 1 {
		address = fs.Arg(0)
	} else if key := strings.TrimSpace(envOrDefault("KITE_PRIVATE_KEY", "")); key != "" || *keyFile != "" {
		loaded, err := loadKey(*keyFile)
		if err != nil {
			return err
		}
		signer, err := x402.NewSignerFromHex(loaded)
		if err != nil {
			return err
		}
		address = signer.Address()
	} else {
		fs.Usage()
		return fmt.Errorf("pass an address, or set KITE_PRIVATE_KEY / -key-file to use the wallet's")
	}

	return balanceOut(chain, chain.RPCURL, address, os.Stdout)
}

// cmdWallet prints the wallet's address (from the key) and, unless
// -address-only is set, its balances on both Kite networks — the natural
// first command when setting up: fund this address, then pay.
func cmdWallet(args []string) error {
	fs := flag.NewFlagSet("wallet", flag.ExitOnError)
	keyFile := fs.String("key-file", "", "private key file (default: $KITE_PRIVATE_KEY)")
	addressOnly := fs.Bool("address-only", false, "print just the address")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 wallet [-key-file PATH] [-address-only]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := loadKey(*keyFile)
	if err != nil {
		return err
	}
	signer, err := x402.NewSignerFromHex(key)
	if err != nil {
		return err
	}
	if *addressOnly {
		fmt.Println(signer.Address())
		return nil
	}
	fmt.Printf("wallet: %s\n", signer.Address())
	fmt.Println("fund it (testnet faucet drops 0.5 KITE): https://faucet.gokite.ai")
	for _, chain := range []kite.Chain{kite.Testnet, kite.Mainnet} {
		fmt.Println()
		if err := balanceOut(chain, chain.RPCURL, signer.Address(), os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "kx402: %s: %v\n", chain.Network, err)
		}
	}
	return nil
}

// balanceOut queries one chain and prints the wallet picture.
func balanceOut(chain kite.Chain, rpcURL, address string, out io.Writer) error {
	client := rpc.New(rpcURL).WithHTTP(&http.Client{Timeout: 15 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("asking %s for the chain id: %w", chain.RPCURL, err)
	}
	if chainID != chain.ChainID {
		return fmt.Errorf("%s reports chain id %d, expected %d — is -net right?",
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
	fmt.Fprintf(out, "%s  %s KITE\n", address, unitsToDecimal(native.String(), 18))
	fmt.Fprintf(out, "%s  %s %s\n",
		strings.Repeat(" ", len(address)), unitsToDecimal(stable.String(), int(chain.AssetDecimals)), chain.AssetSymbol)
	return nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
