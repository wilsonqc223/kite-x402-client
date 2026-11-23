package x402

import (
	"bytes"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/Chesten223/kite-x402-client/internal/kite"
)

// DefaultMaxPaymentUSD caps what a single payment may cost unless the
// caller raises it explicitly. x402 prices are typically fractions of a
// cent; a dollar is far above any legitimate per-call price and low enough
// that a malicious or misconfigured endpoint — or a prompt-injected agent
// driving the MCP server — cannot drain a wallet in one request.
const DefaultMaxPaymentUSD = "1"

// PayResult is the outcome of one x402-paid request.
type PayResult struct {
	// HTTP status of the final response.
	Status int
	// Body of the final response.
	Body []byte
	// Receipt is the decoded PAYMENT-RESPONSE header of a settled payment,
	// or nil when the response carried none.
	Receipt *SettleResponse
	// Challenge is the final 402 challenge when the payment was rejected,
	// or nil.
	Challenge *PaymentRequired
	// Paid reports that the request went out with a payment and the server
	// accepted it (final status below 400). It is false for a free endpoint,
	// for a rejected payment, and for a paid request whose handler failed —
	// the server does not settle those.
	Paid bool
	// Option is the index of the payment option that was paid, or -1 when
	// nothing was paid.
	Option int
	// Amount is the atomic-unit price of the option that was signed, empty
	// when nothing was signed.
	Amount string
}

// payConfig holds the tunables of Pay.
type payConfig struct {
	maxAmountUSD string
}

// PayOption customises Pay.
type PayOption func(*payConfig)

// WithMaxAmountUSD caps the per-payment price, as a USD decimal string
// ("0.01", "$5"). An option priced above the cap is refused before
// anything is signed. "0" disables the cap.
func WithMaxAmountUSD(usd string) PayOption {
	return func(c *payConfig) { c.maxAmountUSD = usd }
}

// Pay performs the full x402 client flow against rawURL:
//
//  1. issue the request unpaid; a non-402 (or headerless) answer is passed
//     through untouched;
//  2. decode the 402 challenge, pick a payment option, check it against the
//     price cap, and sign an EIP-3009 authorization with signer;
//  3. reissue the request with the PAYMENT-SIGNATURE header and collect the
//     settled response (and receipt) — or the server's rejection.
//
// optionIndex selects an entry of the challenge's accepts list; a negative
// value auto-selects the first option on a Kite network this toolkit can
// pay on, falling back to accepts[0] when none match.
//
// A single payment never costs more than DefaultMaxPaymentUSD unless
// WithMaxAmountUSD says otherwise.
func Pay(
	httpClient *http.Client,
	rawURL, method string,
	body []byte,
	headers [][2]string,
	signer *Signer,
	optionIndex int,
	opts ...PayOption,
) (PayResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	config := payConfig{maxAmountUSD: DefaultMaxPaymentUSD}
	for _, opt := range opts {
		opt(&config)
	}

	req, err := newRequest(method, rawURL, body, headers)
	if err != nil {
		return PayResult{Option: -1}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return PayResult{Option: -1}, fmt.Errorf("requesting %s: %w", rawURL, err)
	}
	challenge, err := readChallenge(resp)
	if err != nil {
		resp.Body.Close()
		return PayResult{Option: -1}, err
	}
	if challenge == nil {
		// Not an x402 endpoint (or nothing to pay): pass through.
		return readFinal(resp, PayResult{Option: -1})
	}
	// The challenge is fully decoded; the unpaid response body is no longer
	// needed and must not linger on the connection pool.
	resp.Body.Close()

	option, index, err := selectOption(*challenge, optionIndex)
	if err != nil {
		return PayResult{Option: -1, Challenge: challenge}, err
	}
	if err := checkPriceCap(option, config.maxAmountUSD); err != nil {
		return PayResult{Option: index, Challenge: challenge}, err
	}

	payload, err := signer.BuildAndSign(option, challenge.Resource, time.Now())
	if err != nil {
		return PayResult{Option: index, Challenge: challenge}, err
	}
	encoded, err := EncodePayload(payload)
	if err != nil {
		return PayResult{Option: index, Challenge: challenge}, err
	}

	paidHeaders := append(append([][2]string{}, headers...), [2]string{HeaderPaymentSignature, encoded})
	req, err = newRequest(method, rawURL, body, paidHeaders)
	if err != nil {
		return PayResult{Option: index, Challenge: challenge}, err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return PayResult{Option: index, Challenge: challenge}, fmt.Errorf("paying %s: %w", rawURL, err)
	}
	final, err := readChallenge(resp)
	if err != nil {
		resp.Body.Close()
		return PayResult{Option: index, Challenge: challenge}, err
	}
	result, err := readFinal(resp, PayResult{
		Option: index,
		Amount: option.Amount,
		Paid:   final == nil && resp.StatusCode < 400,
	})
	if err != nil {
		return result, err
	}
	result.Challenge = final
	return result, nil
}

// selectOption resolves the caller's choice into one advertised option.
// A negative index auto-selects the first option on a Kite network this
// toolkit can sign for, falling back to accepts[0] when none match.
func selectOption(challenge PaymentRequired, index int) (PaymentRequirements, int, error) {
	if len(challenge.Accepts) == 0 {
		return PaymentRequirements{}, 0, fmt.Errorf("the 402 challenge advertises no payment options")
	}
	if index < 0 {
		for i, option := range challenge.Accepts {
			if _, err := kite.ByNetwork(option.Network); err == nil {
				return option, i, nil
			}
		}
		return challenge.Accepts[0], 0, nil
	}
	if index >= len(challenge.Accepts) {
		return PaymentRequirements{}, 0, fmt.Errorf(
			"payment option %d requested but the challenge advertises %d", index, len(challenge.Accepts))
	}
	return challenge.Accepts[index], index, nil
}

// checkPriceCap refuses an option priced above the configured USD cap. The
// comparison is done in atomic units with the chain's own integer price
// parser, so there is no float rounding at the boundary.
func checkPriceCap(option PaymentRequirements, maxAmountUSD string) error {
	if maxAmountUSD == "0" {
		return nil
	}
	chain, err := kite.ByNetwork(option.Network)
	if err != nil {
		return nil // BuildAndSign reports unsupported networks with its own message
	}
	cap, err := chain.ParsePrice(maxAmountUSD)
	if err != nil {
		return fmt.Errorf("invalid payment cap %q: %w", maxAmountUSD, err)
	}
	price, ok := new(big.Int).SetString(option.Amount, 10)
	if !ok || price.Sign() < 0 {
		return fmt.Errorf("the challenge advertises a non-numeric price %q", option.Amount)
	}
	capUnits, ok := new(big.Int).SetString(cap.Amount, 10)
	if !ok {
		return fmt.Errorf("invalid payment cap %q", maxAmountUSD)
	}
	if price.Cmp(capUnits) > 0 {
		return fmt.Errorf(
			"refusing to pay %s atomic units on %s: it exceeds the per-payment cap of $%s USD (raise it with -max-usd)",
			option.Amount, option.Network, maxAmountUSD)
	}
	return nil
}

func newRequest(method, rawURL string, body []byte, headers [][2]string) (*http.Request, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	for _, header := range headers {
		req.Header.Add(header[0], header[1])
	}
	return req, nil
}

// readChallenge decodes a 402 response's PAYMENT-REQUIRED header and
// returns nil for anything else. It does not read or close the body: the
// caller owns the response and either hands it to readFinal or closes it.
func readChallenge(resp *http.Response) (*PaymentRequired, error) {
	if resp.StatusCode != http.StatusPaymentRequired {
		return nil, nil
	}
	header := resp.Header.Get(HeaderPaymentRequired)
	if header == "" {
		return nil, nil
	}
	challenge, err := DecodeChallenge(header)
	if err != nil {
		return nil, fmt.Errorf("decoding challenge: %w", err)
	}
	return &challenge, nil
}

// readFinal drains resp into the result, attaching the settlement receipt
// when present. It leaves resp's body consumed and closed.
func readFinal(resp *http.Response, result PayResult) (PayResult, error) {
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return result, fmt.Errorf("reading response body: %w", err)
	}
	result.Status = resp.StatusCode
	result.Body = body
	if header := resp.Header.Get(HeaderPaymentResponse); header != "" {
		receipt, err := DecodeReceipt(header)
		if err != nil {
			return result, fmt.Errorf("decoding receipt: %w", err)
		}
		result.Receipt = &receipt
	}
	return result, nil
}
