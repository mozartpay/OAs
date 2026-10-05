package market

import (
	"testing"

	"github.com/ogtechnologies/mozartpay/internal/models"
)

func TestParseAsset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		network models.Network
		code    string
		issuer  string
		wantErr bool
	}{
		{name: "xlm", input: "XLM", network: models.NetworkStellarTestnet, code: "XLM"},
		{name: "native", input: "native", network: models.NetworkStellarTestnet, code: "XLM"},
		{name: "known testnet", input: "usdc", network: models.NetworkStellarTestnet, code: "USDC", issuer: "GBBD47IF6LWK7P7MDEVSCWR7DPUWV3NY3DTQEVFL4NAT4AQH3ZLLFLA5"},
		{name: "explicit", input: "ABC:GABC", network: models.NetworkStellarTestnet, code: "ABC", issuer: "GABC"},
		{name: "unknown requires issuer", input: "NOPE", network: models.NetworkStellarTestnet, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			asset, err := ParseAsset(tt.input, tt.network)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if asset.Code != tt.code || asset.Issuer != tt.issuer {
				t.Fatalf("unexpected asset: %+v", asset)
			}
		})
	}
}

func TestOrderBookMid(t *testing.T) {
	book := OrderBook{
		Bids: []OrderLevel{{Price: 0.10, Amount: 1}},
		Asks: []OrderLevel{{Price: 0.12, Amount: 1}},
	}
	mid, ok := book.Mid()
	if !ok || mid != 0.11 {
		t.Fatalf("unexpected midpoint %.7f", mid)
	}
	if _, ok := (OrderBook{}).Mid(); ok {
		t.Fatal("empty book should not have midpoint")
	}
}
