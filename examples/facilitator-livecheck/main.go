// Command facilitator-livecheck signs a testnet payment with a freshly
// generated throwaway key and asks the real Kite facilitator /verify
// whether the EIP-712 signature is well-formed.
//
// Expected result: HTTP 200 with isValid=false and
// invalidReason="insufficient_funds" — the throwaway wallet cannot pay,
// but the facilitator recovers the correct payer address from the
// signature, proving the EIP-3009 wire format end to end.
//
//	go run ./examples/facilitator-livecheck
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/crypto"

	"github.com/Chesten223/kite-x402-client/internal/kite"
	"github.com/Chesten223/kite-x402-client/internal/x402"
)

func main() {
	key, err := crypto.GenerateKey()
	if err != nil {
		panic(err)
	}
	keyHex := fmt.Sprintf("%x", crypto.FromECDSA(key))

	signer, err := x402.NewSignerFromHex(keyHex)
	if err != nil {
		panic(err)
	}
	fmt.Fprintln(os.Stderr, "throwaway payer:", signer.Address())

	option := x402.PaymentRequirements{
		Scheme:            "exact",
		Network:           kite.Testnet.Network,
		Amount:            "1000000000000000",
		Asset:             kite.Testnet.AssetAddress,
		PayTo:             "0x000000000000000000000000000000000000c0ff",
		MaxTimeoutSeconds: 60,
		Extra:             map[string]string{"name": kite.Testnet.EIP712Name, "version": kite.Testnet.EIP712Version},
	}

	payload, err := signer.BuildAndSign(option, x402.ResourceInfo{
		URL:         "https://api.example.com/v1/forecast",
		Description: nil,
	}, time.Now())
	if err != nil {
		panic(err)
	}

	body := map[string]any{
		"x402Version":         2,
		"paymentPayload":      payload,
		"paymentRequirements": option,
	}
	raw, _ := json.Marshal(body)
	resp, err := http.Post(
		kite.FacilitatorURL+"/verify", "application/json", bytes.NewReader(raw))
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	var out bytes.Buffer
	out.ReadFrom(resp.Body)
	fmt.Println("HTTP", resp.Status)
	var pretty any
	if json.Unmarshal(out.Bytes(), &pretty) == nil {
		indented, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(indented))
	} else {
		fmt.Println(out.String())
	}
}
