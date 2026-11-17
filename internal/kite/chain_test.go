package kite

import "testing"

func TestParsePriceTestnet(t *testing.T) {
	got, err := Testnet.ParsePrice("0.001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Amount != "1000000000000000" {
		t.Errorf("amount = %q, want 1000000000000000", got.Amount)
	}
	if got.Asset != Testnet.AssetAddress {
		t.Errorf("asset = %q, want %q", got.Asset, Testnet.AssetAddress)
	}
	if got.Extra["name"] != "pieUSD" || got.Extra["version"] != "1" {
		t.Errorf("extra = %v, want pieUSD/1", got.Extra)
	}
}

func TestParsePriceMainnetWithDollar(t *testing.T) {
	got, err := Mainnet.ParsePrice("$0.05")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Amount != "50000" {
		t.Errorf("amount = %q, want 50000", got.Amount)
	}
	if got.Extra["name"] != "Bridged USDC (Kite AI)" {
		t.Errorf("extra name = %q", got.Extra["name"])
	}
}

func TestParsePriceTestnetAcceptsSubCentAmounts(t *testing.T) {
	// 18 decimals on pieUSD: 0.0000001 = 10^-7 * 10^18 = 10^11 units — valid.
	got, err := Testnet.ParsePrice("0.0000001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Amount != "100000000000" {
		t.Errorf("amount = %q, want 100000000000", got.Amount)
	}
}

func TestParsePriceRejectsBadInput(t *testing.T) {
	bad := []string{"", "abc", "-1", "0", "1e3", "$", "1.", ".5", "1.2.3", "+1"}
	for _, price := range bad {
		if _, err := Testnet.ParsePrice(price); err == nil {
			t.Errorf("ParsePrice(%q) = nil error, want rejection", price)
		}
	}
}

func TestParsePriceRejectsTooManyDecimals(t *testing.T) {
	// Mainnet USDC.e has 6 decimals; 7 fractional digits must be rejected.
	if _, err := Mainnet.ParsePrice("0.0000001"); err == nil {
		t.Error("mainnet 7-decimal price accepted, want rejection")
	}
	// The same price is fine on testnet's 18-decimal pieUSD (covered above).
}

func TestParsePriceIsDeterministic(t *testing.T) {
	a, _ := Testnet.ParsePrice("0.25")
	b, _ := Testnet.ParsePrice("0.25")
	if a.Asset != b.Asset || a.Amount != b.Amount {
		t.Errorf("ParsePrice not deterministic: %v vs %v", a, b)
	}
}

func TestByNetwork(t *testing.T) {
	for _, tc := range []struct {
		in      string
		want    Chain
		wantErr bool
	}{
		{"mainnet", Mainnet, false},
		{"testnet", Testnet, false},
		{"eip155:2366", Mainnet, false},
		{"eip155:2368", Testnet, false},
		{"  TESTNET ", Testnet, false},
		{"devnet", Chain{}, true},
		{"eip155:1", Chain{}, true},
	} {
		got, err := ByNetwork(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ByNetwork(%q) = nil error, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ByNetwork(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ByNetwork(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestChainIDMatchesNetwork(t *testing.T) {
	if got := Mainnet.Network; got != "eip155:2366" || Mainnet.ChainID != 2366 {
		t.Errorf("mainnet mismatch: %s/%d", got, Mainnet.ChainID)
	}
	if got := Testnet.Network; got != "eip155:2368" || Testnet.ChainID != 2368 {
		t.Errorf("testnet mismatch: %s/%d", got, Testnet.ChainID)
	}
}
