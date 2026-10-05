package trading

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// Early-return failures must leave a failed execution row; previously they
// returned without recording anything, so dashboard history looked clean even
// though cycles were dying.
func TestReconcileRecordsFailureOnEarlyReturn(t *testing.T) {
	store, err := NewStoreAt(filepath.Join(t.TempDir(), "trading.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	svc := NewServiceWithDependencies(models.NetworkStellarTestnet, store, nil, nil)
	strategy := &models.TradingStrategy{
		ID:         "strategy-mismatch",
		Name:       "Mismatch",
		Type:       models.StrategyBuySell,
		Status:     models.StrategyActive,
		Network:    models.NetworkStellarMainnet,
		BaseAsset:  "XLM",
		QuoteAsset: "USDC",
		Parameters: map[string]interface{}{},
		RiskLimits: models.DefaultRiskLimits(),
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := store.SaveStrategy(strategy); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.ReconcileStrategy(context.Background(), strategy, false); err == nil {
		t.Fatal("expected network mismatch error")
	}
	executions, err := store.ListExecutions(strategy.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 {
		t.Fatalf("expected one execution record, got %d", len(executions))
	}
	if executions[0].Status != models.ExecutionFailed {
		t.Fatalf("expected failed execution, got %s", executions[0].Status)
	}
	if !strings.Contains(executions[0].Error, "does not match") {
		t.Fatalf("expected mismatch error text, got %q", executions[0].Error)
	}
}
