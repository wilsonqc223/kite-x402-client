package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/wilsonqc223/kite-x402-client/internal/x402"
)

// WriteInspection renders an x402.Inspect result — what Pay would do for an
// endpoint — and reports whether Pay would go ahead and sign. It is the one
// rendering shared by `kx402 pay -dry-run` and the MCP server's inspect
// tool, so a CLI user and an agent are told exactly the same thing.
//
// walletAddress, when non-empty, is the payer that would be charged. How the
// option was chosen comes from the inspection itself, so the report cannot
// disagree with the library about it.
func WriteInspection(out io.Writer, rawURL string, got x402.Inspection, walletAddress string) bool {
	fmt.Fprintln(out, "dry run — nothing was signed or sent")
	if !got.PaidEndpoint {
		fmt.Fprintf(out, "%s answered %d without a 402 challenge — nothing to pay\n", rawURL, got.Status)
		writeBodyPreview(out, got)
		return true
	}

	chain, known := ChainByNetwork(got.Option.Network)
	fmt.Fprintf(out, "%s answered 402 with %d payment option(s); pay would sign option %d:\n",
		rawURL, len(got.Challenge.Accepts), got.OptionIndex)
	switch got.Selection {
	case x402.AutoSelected:
		fmt.Fprintln(out, "  (chosen automatically: the first option on a Kite network)")
	case x402.SelectedByNetwork:
		fmt.Fprintf(out, "  (chosen by network: the option on %s)\n", got.Option.Network)
	}
	fmt.Fprintf(out, "  network:  %s\n", got.Option.Network)
	fmt.Fprintf(out, "  price:    %s %s  (%s units)\n",
		FormatUnits(got.Option.Amount, chain, known), chain.AssetSymbol, got.Option.Amount)
	fmt.Fprintf(out, "  asset:    %s\n", got.Option.Asset)
	fmt.Fprintf(out, "  payTo:    %s\n", got.Option.PayTo)
	fmt.Fprintf(out, "  validity: %ds\n", got.Option.MaxTimeoutSeconds)
	if name, ok := got.Option.Extra["name"].(string); ok {
		fmt.Fprintf(out, "  token:    %s\n", name)
	}

	switch {
	case got.CapUSD == "0":
		fmt.Fprintln(out, "price cap: disabled")
	case got.WithinCap:
		fmt.Fprintf(out, "price cap: $%s — within cap\n", got.CapUSD)
	default:
		fmt.Fprintf(out, "price cap: $%s — pay would REFUSE: %s\n", got.CapUSD, got.Refusal)
	}
	if walletAddress != "" {
		fmt.Fprintf(out, "wallet:    %s (balance not checked)\n", walletAddress)
	}
	return got.WithinCap
}

// inspectPreviewBytes is how much of a free endpoint's body the report
// shows. A dry run answers a question about the price; the body is context,
// and it comes from an endpoint that has neither been paid for nor vetted.
const inspectPreviewBytes = 2048

// writeBodyPreview shows the beginning of the unpaid response body and says
// how much of it is left out, so neither a terminal nor an agent's context
// is flooded by an endpoint that answers big.
func writeBodyPreview(out io.Writer, got x402.Inspection) {
	body := got.Body
	if len(body) <= inspectPreviewBytes {
		fmt.Fprintf(out, "%s\n", body)
		return
	}
	fmt.Fprintf(out, "%s\n(%d more bytes of the body not shown",
		body[:inspectPreviewBytes], len(body)-inspectPreviewBytes)
	if got.BodyTruncated {
		fmt.Fprint(out, ", and the body itself was cut off at the reading limit")
	}
	fmt.Fprintln(out, ")")
}

// WalletAddressFromEnv returns the address of the wallet configured through
// KITE_PRIVATE_KEY, or "" when no usable key is set. It derives the address
// locally and never prints or returns the key itself.
func WalletAddressFromEnv() string {
	key, err := LoadKey("")
	if err != nil {
		return ""
	}
	signer, err := x402.NewSignerFromHex(key)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(signer.Address())
}
