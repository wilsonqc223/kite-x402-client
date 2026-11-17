// Package kite holds the Kite AI network constants and the $-style price
// parser used on the x402 wire.
//
// The payer signs an EIP-3009 transferWithAuthorization, so the asset must
// implement EIP-3009 and the EIP-712 domain (name/version) must match the
// token contract exactly or the facilitator rejects the signature. The
// values mirror kite.ts / kite.go / kite.rs from the official x402 wrapper
// templates field for field — keep them as is.
package kite

import (
	"fmt"
	"strings"
)

// Chain describes one Kite network the client can pay on.
type Chain struct {
	// Network is the CAIP-2 identifier, e.g. "eip155:2368".
	Network string
	// ChainID is the EIP-155 chain id matching Network.
	ChainID int64
	// RPCURL is the JSON-RPC endpoint of the chain.
	RPCURL string
	// AssetAddress is the stablecoin contract address.
	AssetAddress string
	// AssetSymbol is the ticker symbol of the stablecoin.
	AssetSymbol string
	// AssetDecimals is the number of decimals the stablecoin uses.
	AssetDecimals uint8
	// EIP712Name is the EIP-712 domain name of the asset contract.
	EIP712Name string
	// EIP712Version is the EIP-712 domain version of the asset contract.
	EIP712Version string
}

// Mainnet is Kite mainnet: Bridged USDC (USDC.e), 6 decimals.
var Mainnet = Chain{
	Network:       "eip155:2366",
	ChainID:       2366,
	RPCURL:        "https://rpc.gokite.ai",
	AssetAddress:  "0x7aB6f3ed87C42eF0aDb67Ed95090f8bF5240149e",
	AssetSymbol:   "USDC.e",
	AssetDecimals: 6,
	EIP712Name:    "Bridged USDC (Kite AI)",
	EIP712Version: "2",
}

// Testnet is Kite testnet: pieUSD test stablecoin, 18 decimals. This is what
// a Kite Passport agent in sandbox mode pays with.
var Testnet = Chain{
	Network:       "eip155:2368",
	ChainID:       2368,
	RPCURL:        "https://rpc-testnet.gokite.ai",
	AssetAddress:  "0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
	AssetSymbol:   "pieUSD",
	AssetDecimals: 18,
	EIP712Name:    "pieUSD",
	EIP712Version: "1",
}

// FacilitatorURL verifies and settles x402 payments on both Kite networks.
// The x402 SDKs append /verify, /settle and /supported, so the /v2 prefix
// must stay in the base URL.
const FacilitatorURL = "https://facilitator.pieverse.io/v2"

// ByNetwork resolves a CAIP-2 network id ("eip155:2366"/"eip155:2368") or a
// shorthand ("mainnet"/"testnet") to a Chain.
func ByNetwork(name string) (Chain, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "mainnet", "eip155:2366":
		return Mainnet, nil
	case "testnet", "eip155:2368":
		return Testnet, nil
	default:
		return Chain{}, fmt.Errorf("unknown Kite network %q (want mainnet or testnet)", name)
	}
}

// AssetAmount is an amount ready to go on the x402 wire: an integer
// token-unit string plus the extra metadata (EIP-712 name/version) the
// facilitator needs to validate the signature.
type AssetAmount struct {
	// Asset is the asset contract address.
	Asset string
	// Amount is the amount in the asset's smallest unit, as an integer string.
	Amount string
	// Extra carries the EIP-712 domain name/version for the facilitator.
	Extra map[string]string
}

// ParsePrice parses a "0.001"-or-"$0.001"-style USD price into integer token
// units for the chain's stablecoin.
//
// It does integer string math (no floats) to avoid rounding, exactly like
// kiteMoneyParser in the official templates: at most AssetDecimals
// fractional digits, no scientific notation, no sign, and the result must be
// at least one smallest unit of the asset.
func (c Chain) ParsePrice(priceUSD string) (AssetAmount, error) {
	trimmed := strings.TrimSpace(priceUSD)
	text := strings.TrimPrefix(trimmed, "$")

	if !isPlainPositiveDecimal(text) {
		return AssetAmount{}, fmt.Errorf("price must be a positive decimal, got %q", priceUSD)
	}

	whole, frac, _ := strings.Cut(text, ".")
	if len(frac) > int(c.AssetDecimals) {
		return AssetAmount{}, fmt.Errorf(
			"price %s has more than %d decimals (%s)", text, c.AssetDecimals, c.AssetSymbol)
	}

	digits := whole + frac + strings.Repeat("0", int(c.AssetDecimals)-len(frac))
	units := strings.TrimLeft(digits, "0")
	if units == "" {
		return AssetAmount{}, fmt.Errorf(
			"price %s is below one unit of %s", priceUSD, c.AssetSymbol)
	}

	return AssetAmount{
		Asset:  c.AssetAddress,
		Amount: units,
		Extra:  map[string]string{"name": c.EIP712Name, "version": c.EIP712Version},
	}, nil
}

// isPlainPositiveDecimal accepts ^\d+(\.\d+)?$: digits, at most one ".", no
// leading ".", no trailing ".", no sign, no scientific notation.
func isPlainPositiveDecimal(s string) bool {
	if s == "" {
		return false
	}
	whole, frac, hasDot := strings.Cut(s, ".")
	if hasDot && frac == "" {
		return false
	}
	return whole != "" && allDigits(whole) && allDigits(frac)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
