// Command kx402 is a client toolkit for x402 paid HTTP APIs on the Kite AI
// chains: probe 402 challenges, pay with an EIP-3009 signature, and decode
// settlement receipts.
package main

import (
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"

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

Usage:
  kx402 probe <url>          Fetch a URL and decode its 402 payment challenge
  kx402 receipt <base64>     Decode a PAYMENT-RESPONSE settlement receipt

Environment:
  KITE_NETWORK   mainnet (default) or testnet, used to label assets
`)
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
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: kx402 receipt <base64>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	receipt, err := x402.DecodeReceipt(fs.Arg(0))
	if err != nil {
		return err
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
	units, ok := new(big.Int).SetString(amount, 10)
	if !ok {
		return amount
	}
	if !known {
		return fmt.Sprintf("%s (raw units)", amount)
	}
	decimals := int(chain.AssetDecimals)
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
