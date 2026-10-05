package trading

import (
	"testing"

	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
)

func TestOffersEquivalent(t *testing.T) {
	base := models.AssetRef{Code: "XLM"}
	quote := models.AssetRef{Code: "USDC", Issuer: "GISSUER"}
	live := market.OpenOffer{Side: models.OrderSideSell, Base: base, Quote: quote, Price: 1.001, Amount: 10.05}
	intent := models.OrderIntent{Side: models.OrderSideSell, BaseAsset: base, QuoteAsset: quote, Price: 1.0, Amount: 10}
	if !offersEquivalent(live, intent, 0.002, 0.01) {
		t.Fatal("expected equivalent offer within tolerance")
	}
	if offersEquivalent(live, intent, 0.0001, 0.0001) {
		t.Fatal("expected offer outside tolerance")
	}
	live.Side = models.OrderSideBuy
	if offersEquivalent(live, intent, 1, 1) {
		t.Fatal("side mismatch should not be equivalent")
	}
}
