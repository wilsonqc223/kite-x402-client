package x402

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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
	// Paid reports whether the request went out with a payment header and
	// was accepted (final status < 400).
	Paid bool
	// Option is the index of the payment option that was paid, when a
	// payment was attempted.
	Option int
}

// Pay performs the full x402 client flow against rawURL:
//
//  1. issue the request unpaid; a non-402 (or headerless) answer is passed
//     through untouched;
//  2. decode the 402 challenge, pick a payment option, sign an EIP-3009
//     authorization with signer;
//  3. reissue the request with the PAYMENT-SIGNATURE header and collect the
//     settled response (and receipt) — or the server's rejection.
//
// optionIndex selects an entry of the challenge's accepts list; a negative
// value auto-selects the first option on a network this toolkit knows.
func Pay(
	httpClient *http.Client,
	rawURL, method string,
	body []byte,
	headers [][2]string,
	signer *Signer,
	optionIndex int,
) (PayResult, error) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	req, err := newRequest(method, rawURL, body, headers)
	if err != nil {
		return PayResult{}, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return PayResult{}, fmt.Errorf("requesting %s: %w", rawURL, err)
	}
	challenge, err := readChallenge(resp)
	if err != nil {
		return PayResult{}, err
	}
	if challenge == nil {
		// Not an x402 endpoint (or nothing to pay): pass through.
		return readFinal(resp, PayResult{Option: -1})
	}

	option, index, err := selectOption(*challenge, optionIndex)
	if err != nil {
		return PayResult{}, err
	}

	payload, err := signer.BuildAndSign(option, challenge.Resource, time.Now())
	if err != nil {
		return PayResult{}, err
	}
	encoded, err := EncodePayload(payload)
	if err != nil {
		return PayResult{}, err
	}

	paidHeaders := append(append([][2]string{}, headers...), [2]string{HeaderPaymentSignature, encoded})
	req, err = newRequest(method, rawURL, body, paidHeaders)
	if err != nil {
		return PayResult{}, err
	}
	resp, err = httpClient.Do(req)
	if err != nil {
		return PayResult{}, fmt.Errorf("paying %s: %w", rawURL, err)
	}
	final, err := readChallenge(resp)
	if err != nil {
		return PayResult{}, err
	}
	result, err := readFinal(resp, PayResult{Option: index, Paid: final == nil})
	if err != nil {
		return result, err
	}
	result.Challenge = final
	return result, nil
}

// selectOption resolves the caller's choice into one advertised option.
// A negative index auto-selects the first option on a known Kite network,
// falling back to accepts[0] when none match.
func selectOption(challenge PaymentRequired, index int) (PaymentRequirements, int, error) {
	if len(challenge.Accepts) == 0 {
		return PaymentRequirements{}, 0, fmt.Errorf("the 402 challenge advertises no payment options")
	}
	if index < 0 {
		for i, option := range challenge.Accepts {
			if _, err := networkChainID(option.Network); err == nil {
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

// readChallenge drains resp and returns the decoded 402 challenge, or nil
// when the response is not a payment challenge.
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

// networkChainID parses a CAIP-2 "eip155:<id>" network identifier.
func networkChainID(network string) (int64, error) {
	const prefix = "eip155:"
	if !strings.HasPrefix(network, prefix) {
		return 0, fmt.Errorf("network %q is not an EVM chain", network)
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(network, prefix), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("network %q has no numeric chain id", network)
	}
	return id, nil
}
