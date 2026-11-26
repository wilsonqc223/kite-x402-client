// Command kx402 is a client toolkit for x402 paid HTTP APIs on the Kite AI
// chains: probe 402 challenges, pay with an EIP-3009 signature, and decode
// settlement receipts.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wilsonqc223/kite-x402-client/internal/cli"
	"github.com/wilsonqc223/kite-x402-client/internal/kite"
	"github.com/wilsonqc223/kite-x402-client/internal/x402"
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
	case "version":
		fmt.Println(cli.VersionString())
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
  kx402 pay [flags] <url>    Pay the 402 challenge and fetch the content
  kx402 receipt <base64|->   Decode a PAYMENT-RESPONSE settlement receipt
  kx402 balance [addr]       Native KITE + stablecoin balance on one network
  kx402 wallet               The configured wallet's address and balances
  kx402 version              Print the version this binary was built at

  kx402 pay flags:
    -X, -method GET          HTTP method for the paid request
    -d, -data @file|-|TEXT   Request body: a file, stdin, or a literal string
    -H, -header K:V          Extra header, repeatable
    -o, -option N            Pay accepts[N] instead of auto-selecting
    -network NAME            Pay the option on a Kite network: mainnet,
                              testnet or a CAIP-2 id. Not together with -o.
    -max-usd USD             Refuse a payment above this price (default $1;
                              0 disables the cap)
    -dry-run                 Report what would be paid; sign and send nothing
    -max-body-bytes N        Read at most N bytes of the response (8 MiB)
    -key-file PATH           Private key file (else $KITE_PRIVATE_KEY)
    -proxy URL               Egress proxy, e.g. socks5://127.0.0.1:10800

  probe and balance take -proxy URL as well.

Environment:
  KITE_PRIVATE_KEY      Private key for 'pay': 64 hex characters, with or
                        without 0x. Never a CLI argument, so it cannot leak
                        into shell history.
  KITE_MAX_PAYMENT_USD  Default for -max-usd, and the only cap the MCP
                        server kx402-mcp knows.
  KITE_NETWORK          Network for 'balance' (default testnet).
  KITE_PROXY            Egress proxy for kx402-mcp.
`)
}

// cmdPay performs the full x402 payment flow for one URL, or with
// -dry-run only reports what it would pay.
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
	proxy := fs.String("proxy", "", "egress proxy URL, e.g. socks5://127.0.0.1:10800")
	maxUSD := fs.String("max-usd", cli.EnvOrDefault("KITE_MAX_PAYMENT_USD", x402.DefaultMaxPaymentUSD),
		"refuse any single payment above this USD price; 0 disables the cap")
	dryRun := fs.Bool("dry-run", false,
		"report what would be paid — option, price, cap verdict — without signing or sending (no wallet needed); exit 3 if pay would refuse")
	maxBody := fs.Int64("max-body-bytes", x402.DefaultMaxBodyBytes,
		"read at most this many bytes of the response; a longer body is cut off and reported on stderr")
	network := fs.String("network", "",
		"pay the option on this Kite network (mainnet, testnet or a CAIP-2 id such as eip155:2366); cannot be combined with -o")
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

	body, err := readBody(*data)
	if err != nil {
		return err
	}
	httpClient, err := cli.HTTPClientFor(*proxy, 60*time.Second)
	if err != nil {
		return err
	}
	if *network != "" && *option >= 0 {
		return fmt.Errorf("-network and -o cannot be combined; pass one or the other")
	}
	payOpts := []x402.PayOption{x402.WithMaxAmountUSD(*maxUSD), x402.WithMaxBodyBytes(*maxBody)}
	if *network != "" {
		payOpts = append(payOpts, x402.WithNetwork(*network))
	}

	if *dryRun {
		// Nothing is signed on this path, so no key is required and a missing
		// one must not block an audit.
		wouldPay, err := runDryRun(httpClient, fs.Arg(0), *method, body, headers, *option, payOpts, os.Stdout)
		if err != nil {
			return err
		}
		if !wouldPay {
			os.Exit(3)
		}
		return nil
	}

	key, err := cli.LoadKey(*keyFile)
	if err != nil {
		return err
	}
	signer, err := x402.NewSignerFromHex(key)
	if err != nil {
		return err
	}
	result, err := x402.Pay(httpClient, fs.Arg(0), *method, body, headers, signer, *option, payOpts...)
	if err != nil {
		return withCapHint(err, " (raise it with -max-usd)")
	}

	if result.Paid {
		fmt.Fprintf(os.Stderr, "paid option %d as %s\n", result.Option, signer.Address())
		if result.Receipt != nil {
			chain, known := cli.ChainByNetwork(result.Receipt.Network)
			fmt.Fprintf(os.Stderr, "settled: tx %s on %s", result.Receipt.Transaction, result.Receipt.Network)
			if result.Receipt.Amount != nil {
				fmt.Fprintf(os.Stderr, " (%s %s)", cli.FormatUnits(*result.Receipt.Amount, chain, known), chain.AssetSymbol)
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
	if result.BodyTruncated {
		// On purpose not on stdout: what was printed is the beginning of the
		// response, and a pipe should not take it for the whole thing.
		fmt.Fprintf(os.Stderr, "kx402: response body cut off after %d bytes; pass -max-body-bytes to read more\n", *maxBody)
	}
	if result.Status >= 400 {
		os.Exit(2)
	}
	return nil
}

// runDryRun inspects url and writes what `kx402 pay` would do to out,
// reporting whether it would go ahead and sign. It sends one unpaid request
// and never signs, so no key is required; the address of the wallet in
// KITE_PRIVATE_KEY is shown when it is set.
func runDryRun(
	httpClient *http.Client,
	rawURL, method string,
	body []byte,
	headers [][2]string,
	optionIndex int,
	payOpts []x402.PayOption,
	out io.Writer,
) (bool, error) {
	got, err := x402.Inspect(httpClient, rawURL, method, body, headers, optionIndex, payOpts...)
	if err != nil {
		return false, err
	}
	wouldPay := cli.WriteInspection(out, rawURL, got, cli.WalletAddressFromEnv())
	// Raising the cap cures only a price above it. A malformed price or an
	// unusable cap is reported above, and this advice would be wrong there.
	if got.CapExceeded {
		fmt.Fprintln(out, "to raise the cap, pass -max-usd (0 disables it)")
	}
	return wouldPay, nil
}

// withCapHint appends how to raise the cap on this surface to a price-cap
// error, leaving every other error untouched.
func withCapHint(err error, hint string) error {
	var capErr *x402.PriceCapError
	if errors.As(err, &capErr) {
		return fmt.Errorf("%w%s", err, hint)
	}
	return err
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
	proxy := fs.String("proxy", "", "egress proxy URL, e.g. socks5://127.0.0.1:10800")
	asJSON := fs.Bool("json", false, "print the decoded challenge as JSON")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 probe [-json] [-proxy URL] <url>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	httpClient, err := cli.HTTPClientFor(*proxy, 30*time.Second)
	if err != nil {
		return err
	}
	if *asJSON {
		return cli.ProbeJSON(httpClient, fs.Arg(0), os.Stdout)
	}
	return cli.Probe(httpClient, fs.Arg(0), os.Stdout)
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
		chain, known := cli.ChainByNetwork(receipt.Network)
		fmt.Printf("amount:      %s %s\n",
			cli.FormatUnits(*receipt.Amount, chain, known), chain.AssetSymbol)
	}
	if receipt.ErrorReason != nil {
		fmt.Printf("error:       %s\n", *receipt.ErrorReason)
	}
	if receipt.ErrorMessage != nil {
		fmt.Printf("message:     %s\n", *receipt.ErrorMessage)
	}
	return nil
}

// cmdBalance shows native and stablecoin balances of an address on one Kite
// network. Without an argument (and with a key available) it uses the
// wallet's own address.
func cmdBalance(args []string) error {
	fs := flag.NewFlagSet("balance", flag.ExitOnError)
	network := fs.String("net", "", "testnet (default) or mainnet")
	keyFile := fs.String("key-file", "", "private key file (to default the address)")
	proxyFlag := fs.String("proxy", "", "egress proxy URL, e.g. socks5://127.0.0.1:10800")
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
	} else if key := strings.TrimSpace(cli.EnvOrDefault("KITE_PRIVATE_KEY", "")); key != "" || *keyFile != "" {
		loaded, err := cli.LoadKey(*keyFile)
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

	httpClient, err := cli.HTTPClientFor(*proxyFlag, 15*time.Second)
	if err != nil {
		return err
	}
	return cli.BalanceOut(chain, chain.RPCURL, address, os.Stdout, httpClient)
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
	key, err := cli.LoadKey(*keyFile)
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
		httpClient, err := cli.HTTPClientFor("", 15*time.Second)
		if err != nil {
			return err
		}
		if err := cli.BalanceOut(chain, chain.RPCURL, signer.Address(), os.Stdout, httpClient); err != nil {
			fmt.Fprintf(os.Stderr, "kx402: %s: %v\n", chain.Network, err)
		}
	}
	return nil
}
