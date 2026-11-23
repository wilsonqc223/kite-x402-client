// Package x402 implements the client side of the x402 payment protocol on
// the Kite AI chains: decoding 402 challenges, matching advertised payment
// options, building signed payment payloads, and reading settlement
// receipts.
package x402

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// PaymentRequirements is one way to pay for a resource: accepts[i] in a 402
// challenge and accepted in the client's payment payload. Field names match
// the JSON the TypeScript and Go SDKs put on the wire exactly.
type PaymentRequirements struct {
	Scheme            string `json:"scheme"`
	Network           string `json:"network"`
	Amount            string `json:"amount"`
	Asset             string `json:"asset"`
	PayTo             string `json:"payTo"`
	MaxTimeoutSeconds uint64 `json:"maxTimeoutSeconds"`
	// Extra is server-defined metadata. The x402 schema allows string and
	// number values (e.g. some assets carry a numeric chainId), so it is kept
	// loosely typed — a single non-string value must not fail the whole
	// challenge decode.
	Extra map[string]any `json:"extra,omitempty"`
}

// ResourceInfo describes the protected resource in a challenge.
type ResourceInfo struct {
	URL         string  `json:"url"`
	Description *string `json:"description,omitempty"`
	MimeType    *string `json:"mimeType,omitempty"`
}

// PaymentRequired is the 402 body, base64-encoded into the PAYMENT-REQUIRED
// response header.
type PaymentRequired struct {
	X402Version uint32                `json:"x402Version"`
	Error       *string               `json:"error,omitempty"`
	Resource    ResourceInfo          `json:"resource"`
	Accepts     []PaymentRequirements `json:"accepts"`
}

// Authorization is the EIP-3009 transferWithAuthorization the payer signs.
// All numeric fields are decimal strings on the wire.
type Authorization struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Value       string `json:"value"`
	ValidAfter  string `json:"validAfter"`
	ValidBefore string `json:"validBefore"`
	Nonce       string `json:"nonce"`
}

// PaymentPayload is what the client sends, base64-encoded into the
// PAYMENT-SIGNATURE (or legacy X-PAYMENT) request header.
type PaymentPayload struct {
	X402Version uint32              `json:"x402Version"`
	Resource    *ResourceInfo       `json:"resource,omitempty"`
	Accepted    PaymentRequirements `json:"accepted"`
	Payload     struct {
		Signature     string        `json:"signature"`
		Authorization Authorization `json:"authorization"`
	} `json:"payload"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

// SettleResponse is the facilitator's /settle result, base64-encoded into
// the PAYMENT-RESPONSE response header on both success and failure.
type SettleResponse struct {
	Success      bool    `json:"success"`
	ErrorReason  *string `json:"errorReason,omitempty"`
	ErrorMessage *string `json:"errorMessage,omitempty"`
	Payer        *string `json:"payer,omitempty"`
	Transaction  string  `json:"transaction"`
	Network      string  `json:"network"`
	Amount       *string `json:"amount,omitempty"`
}

// Header names used by x402 v2 on the wire.
const (
	// HeaderPaymentRequired carries the base64 challenge on a 402 response.
	HeaderPaymentRequired = "PAYMENT-REQUIRED"
	// HeaderPaymentSignature carries the base64 payment on a request.
	HeaderPaymentSignature = "PAYMENT-SIGNATURE"
	// HeaderPaymentLegacy is the pre-rename name of HeaderPaymentSignature.
	HeaderPaymentLegacy = "X-PAYMENT"
	// HeaderPaymentResponse carries the base64 settlement receipt.
	HeaderPaymentResponse = "PAYMENT-RESPONSE"
)

// DecodeChallenge base64-decodes and parses a PAYMENT-REQUIRED header value.
func DecodeChallenge(header string) (PaymentRequired, error) {
	var challenge PaymentRequired
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return challenge, fmt.Errorf("challenge is not valid base64: %w", err)
	}
	if err := json.Unmarshal(raw, &challenge); err != nil {
		return challenge, fmt.Errorf("challenge is not valid JSON: %w", err)
	}
	return challenge, nil
}

// EncodePayload serializes and base64-encodes a payment payload for the
// PAYMENT-SIGNATURE header.
func EncodePayload(payload PaymentPayload) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encoding payment payload: %w", err)
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeReceipt base64-decodes and parses a PAYMENT-RESPONSE header value.
func DecodeReceipt(header string) (SettleResponse, error) {
	var receipt SettleResponse
	raw, err := base64.StdEncoding.DecodeString(header)
	if err != nil {
		return receipt, fmt.Errorf("receipt is not valid base64: %w", err)
	}
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return receipt, fmt.Errorf("receipt is not valid JSON: %w", err)
	}
	return receipt, nil
}

// ErrNoMatch is returned when no advertised option can be paid with the
// given network/asset pair.
var ErrNoMatch = errors.New("no advertised payment option matches")

// FindMatchingRequirements picks the advertised options the client can pay
// with the given network and asset, mirroring the server-side
// findMatchingRequirements matching in the official SDKs: scheme, network,
// asset, payTo and amount decide; maxTimeoutSeconds and extra are advisory.
func FindMatchingRequirements(accepts []PaymentRequirements, network, asset string) []PaymentRequirements {
	var matches []PaymentRequirements
	for _, option := range accepts {
		if option.Scheme == "exact" && option.Network == network && option.Asset == asset {
			matches = append(matches, option)
		}
	}
	return matches
}
