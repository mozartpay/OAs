package trading

import (
	"context"
	"testing"

	"github.com/ogtechnologies/mozartpay/internal/models"
)

func TestFixedAndCompositeFeeds(t *testing.T) {
	feed, err := NewPriceFeed("fixed:1.25", models.NetworkStellarTestnet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quote, err := feed.Price(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != 1.25 {
		t.Fatalf("unexpected price %f", quote.Price)
	}

	inverted, err := NewPriceFeed("function:invert(fixed:2)", models.NetworkStellarTestnet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quote, err = inverted.Price(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != 0.5 {
		t.Fatalf("unexpected inverted price %f", quote.Price)
	}

	maxFeed, err := NewPriceFeed("function:max(fixed:1,fixed:3,fixed:2)", models.NetworkStellarTestnet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quote, err = maxFeed.Price(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != 3 {
		t.Fatalf("unexpected max price %f", quote.Price)
	}

	sdex, err := NewPriceFeed("sdex:XLM/USDC:mid", models.NetworkStellarTestnet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sdex.Name() != "sdex:XLM/USDC:mid" {
		t.Fatalf("unexpected SDEX feed name %s", sdex.Name())
	}

	weighted, err := NewPriceFeed("function:weighted(fixed:1:0.25,fixed:3:0.75)", models.NetworkStellarTestnet, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	quote, err = weighted.Price(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if quote.Price != 2.5 {
		t.Fatalf("unexpected weighted price %f", quote.Price)
	}
}

func TestHistoricalIntents(t *testing.T) {
	strategy := &models.TradingStrategy{
		ID:   "test",
		Type: models.StrategyBuySell,
		Parameters: map[string]interface{}{
			"amount_per_level":  2.0,
			"levels":            2.0,
			"spread_pct":        2.0,
			"level_spacing_pct": 1.0,
		},
	}
	intents := historicalIntents(strategy, 100)
	if len(intents) != 4 {
		t.Fatalf("expected 4 intents, got %d", len(intents))
	}
	if intents[0].Side != models.OrderSideBuy || intents[0].Price != 99 {
		t.Fatalf("unexpected first bid: %+v", intents[0])
	}
	if intents[1].Side != models.OrderSideSell || intents[1].Price != 101 {
		t.Fatalf("unexpected first ask: %+v", intents[1])
	}
}
