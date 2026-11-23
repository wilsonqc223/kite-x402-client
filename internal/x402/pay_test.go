package x402

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Chesten223/kite-x402-client/internal/kite"
)

// paidEndpoint is a mock x402 resource server: it challenges unpaid
// requests, accepts any payment whose accepted option matches one it
// advertised, and answers with a settlement receipt. It records the
// payment payloads it accepted.
type paidEndpoint struct {
	mu        sync.Mutex
	challenge PaymentRequired
	accepted  []PaymentPayload
	free      bool // answer 200 without a challenge
	reject    bool // keep answering 402 even when paid
	failAfter int  // non-zero: answer with this status once a valid payment arrives
}

func (e *paidEndpoint) handler(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if header := r.Header.Get(HeaderPaymentSignature); header != "" && !e.reject {
		raw, err := base64.StdEncoding.DecodeString(header)
		if err != nil {
			http.Error(w, "bad payment encoding", http.StatusBadRequest)
			return
		}
		var payload PaymentPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			http.Error(w, "bad payment json", http.StatusBadRequest)
			return
		}
		for _, option := range e.challenge.Accepts {
			if payload.Accepted.Amount == option.Amount &&
				payload.Accepted.Network == option.Network &&
				payload.Accepted.PayTo == option.PayTo {
				e.accepted = append(e.accepted, payload)
				if e.failAfter != 0 {
					// Payment verified, but the protected handler failed: a
					// conforming server returns that status and never settles.
					w.WriteHeader(e.failAfter)
					return
				}
				receipt := SettleResponse{
					Success:     true,
					Transaction: "0xsettled",
					Network:     option.Network,
				}
				encoded, _ := json.Marshal(receipt)
				w.Header().Set(HeaderPaymentResponse, base64.StdEncoding.EncodeToString(encoded))
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"data":"paid content"}`))
				return
			}
		}
	}

	if e.free {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"free content"}`))
		return
	}

	encoded, _ := json.Marshal(e.challenge)
	w.Header().Set(HeaderPaymentRequired, base64.StdEncoding.EncodeToString(encoded))
	w.WriteHeader(http.StatusPaymentRequired)
	_, _ = w.Write([]byte("{}"))
}

func (e *paidEndpoint) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(e.handler))
	t.Cleanup(server.Close)
	return server
}

func twoOptionChallenge() PaymentRequired {
	description := "Test wrapper"
	mimeType := "application/json"
	return PaymentRequired{
		X402Version: 2,
		Resource: ResourceInfo{
			URL:         "/v1/forecast",
			Description: &description,
			MimeType:    &mimeType,
		},
		Accepts: []PaymentRequirements{
			{
				Scheme:            "exact",
				Network:           kite.Testnet.Network,
				Amount:            "1000000000000000",
				Asset:             kite.Testnet.AssetAddress,
				PayTo:             "0x00000000000000000000000000000000000000ff",
				MaxTimeoutSeconds: 60,
				Extra:             map[string]any{"name": kite.Testnet.EIP712Name, "version": kite.Testnet.EIP712Version},
			},
			{
				Scheme:            "exact",
				Network:           kite.Mainnet.Network,
				Amount:            "50000",
				Asset:             kite.Mainnet.AssetAddress,
				PayTo:             "0x00000000000000000000000000000000000000ff",
				MaxTimeoutSeconds: 60,
				Extra:             map[string]any{"name": kite.Mainnet.EIP712Name, "version": kite.Mainnet.EIP712Version},
			},
		},
	}
}

func TestPayHappyPathPaysTheAutoSelectedOption(t *testing.T) {
	endpoint := &paidEndpoint{challenge: twoOptionChallenge()}
	server := endpoint.start(t)
	signer, err := NewSignerFromHex(testPrivateKey)
	if err != nil {
		t.Fatalf("NewSignerFromHex: %v", err)
	}

	result, err := Pay(http.DefaultClient, server.URL+"/v1/forecast", http.MethodGet, nil, nil, signer, -1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if !result.Paid || result.Status != http.StatusOK {
		t.Fatalf("result = paid:%v status:%d, want paid 200", result.Paid, result.Status)
	}
	if !strings.Contains(string(result.Body), "paid content") {
		t.Errorf("body = %s", result.Body)
	}
	if result.Option != 0 {
		t.Errorf("auto-select picked option %d, want 0 (first known network)", result.Option)
	}
	if result.Receipt == nil || !result.Receipt.Success || result.Receipt.Transaction != "0xsettled" {
		t.Errorf("receipt = %+v", result.Receipt)
	}

	if len(endpoint.accepted) != 1 {
		t.Fatalf("server accepted %d payments, want 1", len(endpoint.accepted))
	}
	payload := endpoint.accepted[0]
	if payload.X402Version != 2 || payload.Payload.Signature == "" {
		t.Errorf("payload = %+v", payload)
	}
	if payload.Resource == nil || payload.Resource.URL != "/v1/forecast" {
		t.Errorf("resource = %+v, want the challenge resource echoed", payload.Resource)
	}
	if payload.Payload.Authorization.To != payload.Accepted.PayTo {
		t.Errorf("authorization.to = %s, want the option payTo", payload.Payload.Authorization.To)
	}
}

func TestPayExplicitOptionIndexPicksThatOption(t *testing.T) {
	endpoint := &paidEndpoint{challenge: twoOptionChallenge()}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL+"/v1/x", http.MethodGet, nil, nil, signer, 1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if !result.Paid || result.Option != 1 {
		t.Fatalf("result = paid:%v option:%d, want paid option 1", result.Paid, result.Option)
	}
	if got := endpoint.accepted[0].Accepted.Network; got != kite.Mainnet.Network {
		t.Errorf("paid network = %s, want mainnet", got)
	}
}

func TestPayOutOfRangeOptionIsAnError(t *testing.T) {
	endpoint := &paidEndpoint{challenge: twoOptionChallenge()}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	if _, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, 5); err == nil {
		t.Error("Pay with option 5 of 2 = nil error, want error")
	}
}

func TestPayPassesThroughFreeEndpoints(t *testing.T) {
	endpoint := &paidEndpoint{free: true}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if result.Paid || result.Status != http.StatusOK || !strings.Contains(string(result.Body), "free content") {
		t.Errorf("result = paid:%v status:%d body:%s", result.Paid, result.Status, result.Body)
	}
}

func TestPaySurfacesTheRejectionChallenge(t *testing.T) {
	endpoint := &paidEndpoint{challenge: twoOptionChallenge(), reject: true}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if result.Paid || result.Status != http.StatusPaymentRequired {
		t.Fatalf("result = paid:%v status:%d, want rejected 402", result.Paid, result.Status)
	}
	if result.Challenge == nil || len(result.Challenge.Accepts) != 2 {
		t.Errorf("challenge = %+v, want the re-advertised options", result.Challenge)
	}
}

func TestPayPreservesMethodBodyAndHeaders(t *testing.T) {
	var gotMethod, gotHeader string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if r.Header.Get(HeaderPaymentSignature) != "" {
			gotMethod, gotHeader, gotBody = r.Method, r.Header.Get("X-Trace"), body
			w.WriteHeader(http.StatusOK)
			return
		}
		encoded, _ := json.Marshal(twoOptionChallenge())
		w.Header().Set(HeaderPaymentRequired, base64.StdEncoding.EncodeToString(encoded))
		w.WriteHeader(http.StatusPaymentRequired)
	}))
	defer server.Close()
	signer, _ := NewSignerFromHex(testPrivateKey)

	if _, err := Pay(http.DefaultClient, server.URL, http.MethodPost, []byte(`{"q":1}`),
		[][2]string{{"X-Trace", "abc"}}, signer, -1); err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if gotMethod != http.MethodPost || gotHeader != "abc" || string(gotBody) != `{"q":1}` {
		t.Errorf("paid request: method=%s trace=%s body=%s", gotMethod, gotHeader, gotBody)
	}
}

// ---- hardening: price cap, auto-select, Paid semantics, loose extra ----

func pricedChallenge(network, amount string) PaymentRequired {
	description := "priced"
	mimeType := "application/json"
	return PaymentRequired{
		X402Version: 2,
		Resource:    ResourceInfo{URL: "/v1/x", Description: &description, MimeType: &mimeType},
		Accepts: []PaymentRequirements{{
			Scheme: "exact", Network: network, Amount: amount,
			Asset: kite.Testnet.AssetAddress,
			PayTo: "0x00000000000000000000000000000000000000ff",
		}},
	}
}

func TestPayRefusesAPriceAboveTheDefaultCapBeforeSigning(t *testing.T) {
	// 5 pieUSD = 5 * 10^18 units, far above the $1 default.
	endpoint := &paidEndpoint{challenge: pricedChallenge(kite.Testnet.Network, "5000000000000000000")}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1)
	if err == nil || !strings.Contains(err.Error(), "per-payment cap") {
		t.Fatalf("want a cap refusal, got err=%v", err)
	}
	if result.Paid || result.Receipt != nil {
		t.Errorf("nothing must be paid or settled: %v", result)
	}
	if len(endpoint.accepted) != 0 {
		t.Errorf("server received %d payments, want 0 — the refusal must precede signing", len(endpoint.accepted))
	}
}

func TestPayCapCanBeRaisedAndDisabled(t *testing.T) {
	// 0.002 pieUSD is above a 0.001 cap, fine under a raised cap, fine with no cap.
	for _, tc := range []struct {
		cap  string
		want bool
	}{
		{"$0.001", false},
		{"0.01", true},
		{"0", true},
	} {
		endpoint := &paidEndpoint{challenge: pricedChallenge(kite.Testnet.Network, "2000000000000000")}
		server := endpoint.start(t)
		signer, _ := NewSignerFromHex(testPrivateKey)

		result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1,
			WithMaxAmountUSD(tc.cap))
		if tc.want {
			if err != nil || !result.Paid {
				t.Errorf("cap %q: want the payment to go through, got paid=%v err=%v", tc.cap, result.Paid, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "per-payment cap") {
			t.Errorf("cap %q: want a cap refusal, got err=%v", tc.cap, err)
		}
	}
}

func TestPayAutoSelectSkipsNetworksThisToolkitCannotPayOn(t *testing.T) {
	// A multi-chain endpoint lists a non-Kite EVM chain first. Auto-select
	// must step over it to the Kite option instead of failing on it.
	description := "multi"
	mimeType := "application/json"
	challenge := PaymentRequired{
		X402Version: 2,
		Resource:    ResourceInfo{URL: "/v1/x", Description: &description, MimeType: &mimeType},
		Accepts: []PaymentRequirements{
			{Scheme: "exact", Network: "eip155:8453", Amount: "1000000", Asset: "0x4200000000000000000000000000000000000006", PayTo: "0x00000000000000000000000000000000000000ff"},
			{Scheme: "exact", Network: kite.Testnet.Network, Amount: "1000000000000000", Asset: kite.Testnet.AssetAddress, PayTo: "0x00000000000000000000000000000000000000ff"},
		},
	}
	endpoint := &paidEndpoint{challenge: challenge}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if result.Option != 1 {
		t.Errorf("auto-select picked option %d, want 1 (the Kite option)", result.Option)
	}
	if len(endpoint.accepted) != 1 || endpoint.accepted[0].Accepted.Network != kite.Testnet.Network {
		t.Errorf("paid the wrong option: %+v", endpoint.accepted)
	}
}

func TestPaidIsFalseWhenTheProtectedHandlerFails(t *testing.T) {
	// The signature verified, but a 500 from the handler means nothing was
	// settled — the request was not paid for.
	endpoint := &paidEndpoint{challenge: twoOptionChallenge(), failAfter: http.StatusInternalServerError}
	server := endpoint.start(t)
	signer, _ := NewSignerFromHex(testPrivateKey)

	result, err := Pay(http.DefaultClient, server.URL, http.MethodGet, nil, nil, signer, -1)
	if err != nil {
		t.Fatalf("Pay: %v", err)
	}
	if result.Paid {
		t.Errorf("a failed handler must not report Paid: %+v", result)
	}
	if result.Status != http.StatusInternalServerError || result.Receipt != nil {
		t.Errorf("result = status %d receipt %v, want 500 and no receipt", result.Status, result.Receipt)
	}
}

func TestChallengeExtraMayCarryNonStringValues(t *testing.T) {
	// The x402 schema allows string and number values in extra; a number in
	// any option must not fail the whole challenge decode.
	raw := `{"x402Version":2,"resource":{"url":"/v1/x"},
	 "accepts":[{"scheme":"exact","network":"eip155:2368","amount":"1",
	  "asset":"0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A",
	  "payTo":"0x00000000000000000000000000000000000000ff","maxTimeoutSeconds":60,
	  "extra":{"name":"pieUSD","version":"1","chainId":2368}}]}`
	challenge, err := DecodeChallenge(base64.StdEncoding.EncodeToString([]byte(raw)))
	if err != nil {
		t.Fatalf("DecodeChallenge: %v", err)
	}
	if chainID, ok := challenge.Accepts[0].Extra["chainId"].(float64); !ok || chainID != 2368 {
		t.Errorf("numeric extra lost: %#v", challenge.Accepts[0].Extra)
	}
}
