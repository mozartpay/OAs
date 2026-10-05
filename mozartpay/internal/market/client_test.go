package market

import (
	"testing"

	"github.com/ogtechnologies/mozartpay/internal/models"
	hProtocol "github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/txnbuild"
)

func TestOfferOperation(t *testing.T) {
	base := models.AssetRef{Code: "XLM"}
	quote := models.AssetRef{Code: "USDC", Issuer: "GISSUER"}

	sell, err := OfferOperation(OfferSpec{Side: models.OrderSideSell, Base: base, Quote: quote, Price: 1.25, Amount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sell.(*txnbuild.ManageSellOffer); !ok {
		t.Fatalf("expected ManageSellOffer, got %T", sell)
	}

	buy, err := OfferOperation(OfferSpec{Side: models.OrderSideBuy, Base: base, Quote: quote, Price: 1.25, Amount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := buy.(*txnbuild.ManageBuyOffer); !ok {
		t.Fatalf("expected ManageBuyOffer, got %T", buy)
	}

	passive, err := OfferOperation(OfferSpec{Side: models.OrderSideSell, Base: base, Quote: quote, Price: 1.25, Amount: 2, Passive: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := passive.(*txnbuild.CreatePassiveSellOffer); !ok {
		t.Fatalf("expected CreatePassiveSellOffer, got %T", passive)
	}

	cancel, err := OfferOperation(OfferSpec{OfferID: 42, Side: models.OrderSideSell, Base: base, Quote: quote, Price: 1.25, Amount: 0})
	if err != nil {
		t.Fatal(err)
	}
	op, ok := cancel.(*txnbuild.ManageSellOffer)
	if !ok || op.OfferID != 42 || op.Amount != "0.0000000" {
		t.Fatalf("unexpected cancel operation: %#v", cancel)
	}
}

func TestNormalizePairBuyOffer(t *testing.T) {
	base := models.AssetRef{Code: "XLM"}
	quote := models.AssetRef{Code: "USDC", Issuer: "GISSUER"}
	record := hProtocol.Offer{
		ID:      42,
		Selling: hProtocol.Asset{Type: "credit_alphanum4", Code: "USDC", Issuer: "GISSUER"},
		Buying:  hProtocol.Asset{Type: "native"},
		Amount:  "7.0000000",
		Price:   "2.0000000",
	}

	offer, ok := normalizeOfferForPair(record, base, quote)
	if !ok || offer.Side != models.OrderSideBuy {
		t.Fatalf("expected normalized buy offer, got %+v", offer)
	}
	if offer.Price != 0.5 || offer.Amount != 14 {
		t.Fatalf("unexpected normalized price/amount: %+v", offer)
	}
}

func TestNormalizeOfferKeepsLedgerOrientation(t *testing.T) {
	record := hProtocol.Offer{
		ID:      42,
		Selling: hProtocol.Asset{Type: "native"},
		Buying:  hProtocol.Asset{Type: "credit_alphanum4", Code: "USDC", Issuer: "GISSUER"},
		Amount:  "1.0000000",
		Price:   "0.1250000",
	}

	offer := normalizeOffer(record)
	if offer.Side != models.OrderSideSell || offer.Base.Code != "XLM" || offer.Quote.Code != "USDC" {
		t.Fatalf("unexpected ledger orientation: %+v", offer)
	}
	if offer.Price != 0.125 || offer.Amount != 1 {
		t.Fatalf("unexpected normalized price/amount: %+v", offer)
	}
}
