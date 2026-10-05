package trading

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/models"
)

func TestStoreRoundTrip(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	strategy := &models.TradingStrategy{
		ID:         "strategy-test",
		Name:       "Test",
		Type:       models.StrategyBuySell,
		Status:     models.StrategyStopped,
		Network:    models.NetworkStellarTestnet,
		BaseAsset:  "XLM",
		QuoteAsset: "USDC",
		Parameters: map[string]interface{}{"levels": float64(1)},
		RiskLimits: models.DefaultRiskLimits(),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := store.SaveStrategy(strategy); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetStrategy(strategy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != strategy.Name || loaded.Type != strategy.Type || loaded.Parameters["levels"] != float64(1) {
		t.Fatalf("unexpected strategy: %+v", loaded)
	}

	offer := &models.ManagedOffer{
		IntentID:   "buy-0",
		StrategyID: strategy.ID,
		OfferID:    42,
		Side:       models.OrderSideBuy,
		BaseAsset:  models.AssetRef{Code: "XLM"},
		QuoteAsset: models.AssetRef{Code: "USDC", Issuer: "GISSUER"},
		Price:      0.1,
		Amount:     1,
		Status:     "open",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := store.SaveManagedOffer(offer); err != nil {
		t.Fatal(err)
	}
	offers, err := store.ListManagedOffers(strategy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers) != 1 || offers[0].OfferID != 42 {
		t.Fatalf("unexpected offers: %+v", offers)
	}

	execution := &models.StrategyExecution{
		ID:           "exec-test",
		StrategyID:   strategy.ID,
		StrategyType: strategy.Type,
		Timestamp:    time.Now().UTC(),
		Action:       models.TradeEnter,
		BaseAsset:    strategy.BaseAsset,
		QuoteAsset:   strategy.QuoteAsset,
		Status:       models.ExecutionSkipped,
	}
	if err := store.SaveExecution(execution); err != nil {
		t.Fatal(err)
	}
	executions, err := store.ListExecutions(strategy.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || executions[0].ID != execution.ID {
		t.Fatalf("unexpected executions: %+v", executions)
	}
}
