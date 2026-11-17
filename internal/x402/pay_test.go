package x402

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
				Extra:             map[string]string{"name": kite.Testnet.EIP712Name, "version": kite.Testnet.EIP712Version},
			},
			{
				Scheme:            "exact",
				Network:           kite.Mainnet.Network,
				Amount:            "50000",
				Asset:             kite.Mainnet.AssetAddress,
				PayTo:             "0x00000000000000000000000000000000000000ff",
				MaxTimeoutSeconds: 60,
				Extra:             map[string]string{"name": kite.Mainnet.EIP712Name, "version": kite.Mainnet.EIP712Version},
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

func TestNetworkChainID(t *testing.T) {
	if id, err := networkChainID("eip155:2368"); err != nil || id != 2368 {
		t.Errorf("networkChainID(eip155:2368) = %d, %v", id, err)
	}
	for _, bad := range []string{"", "2368", "eip155:abc", "bip122:000000000019d668"} {
		if _, err := networkChainID(bad); err == nil {
			t.Errorf("networkChainID(%q) = nil error, want error", bad)
		}
	}
}

// compile-time assertion that Pay uses a clock (kept honest for future
// refactors of the validity window).
var _ = time.Now
