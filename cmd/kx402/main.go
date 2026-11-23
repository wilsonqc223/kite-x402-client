// Command kx402 is a client toolkit for x402 paid HTTP APIs on the Kite AI
// chains: probe 402 challenges, pay with an EIP-3009 signature, and decode
// settlement receipts.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/cli"
	"github.com/Chesten223/kite-x402-client/internal/kite"
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
	proxy := fs.String("proxy", "", "egress proxy URL, e.g. socks5://127.0.0.1:10800")
	maxUSD := fs.String("max-usd", cli.EnvOrDefault("KITE_MAX_PAYMENT_USD", x402.DefaultMaxPaymentUSD),
		"refuse any single payment above this USD price; 0 disables the cap")
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

	key, err := cli.LoadKey(*keyFile)
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

	httpClient, err := cli.HTTPClientFor(*proxy, 60*time.Second)
	if err != nil {
		return err
	}
	result, err := x402.Pay(httpClient, fs.Arg(0), *method, body, headers, signer, *option,
		x402.WithMaxAmountUSD(*maxUSD))
	if err != nil {
		return err
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
