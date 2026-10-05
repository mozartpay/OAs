//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ogtechnologies/mozartpay/internal/assets"
	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/terminal"
	"github.com/ogtechnologies/mozartpay/internal/trading"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
)

// TestE2E_Testnet_SDEXTradingLifecycle exercises the persistent SDEX trading
// path against Stellar testnet using a fresh friendbot-funded account and an
// isolated HOME directory. It intentionally creates a far-from-market passive
// sell offer so the test does not rely on order-book liquidity.
func TestE2E_Testnet_SDEXTradingLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live testnet e2e test in short mode")
	}

	ctx := context.Background()
	cfg := setupTradingE2EHome(t)
	account := createTradingE2EWallet(t)
	marketClient := market.NewClient(models.NetworkStellarTestnet)

	waitForTradingAccount(t, ctx, marketClient, account.Address)

	quote, err := market.ParseAsset("USDC", models.NetworkStellarTestnet)
	if err != nil {
		t.Fatalf("parse testnet USDC: %v", err)
	}
	trustTx, err := assets.NewService().CreateTrustline(account.Address, quote.Code, quote.Issuer, "")
	if err != nil {
		t.Fatalf("create USDC trustline: %v", err)
	}
	t.Logf("USDC trustline tx: %s", trustTx)
	waitForTradingTrustline(t, ctx, marketClient, account.Address, quote)

	svc := trading.NewService(models.NetworkStellarTestnet)
	strategy, err := svc.CreateStrategy(
		"E2E Passive Sell",
		models.StrategySell,
		"XLM",
		"USDC",
		map[string]interface{}{
			"price_feed":          "fixed:1000000",
			"amount_per_level":    0.0000001,
			"levels":              1,
			"start_offset_pct":    0,
			"level_spacing_pct":   0,
			"maker_only":          true,
			"min_xlm_reserve":     2,
			"interval_seconds":    5,
			"price_tolerance_pct": 0.1,
		},
		models.RiskLimits{
			MaxPositionSize: 1,
			MaxDailyLoss:    10,
			MaxDrawdown:     50,
			MaxOpenTrades:   4,
		},
	)
	if err != nil {
		t.Fatalf("create strategy: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = svc.CancelStrategyOffers(cleanupCtx, strategy.ID, true)
		_ = svc.StopStrategy(strategy.ID)
		_ = svc.Close()
	})
	t.Logf("Strategy ID: %s", strategy.ID)

	// Reopen the store through a second service instance to prove persistence.
	persistedSvc := trading.NewService(models.NetworkStellarTestnet)
	persisted, err := persistedSvc.GetStrategy(strategy.ID)
	if err != nil {
		_ = persistedSvc.Close()
		t.Fatalf("reload persisted strategy: %v", err)
	}
	if persisted.Name != strategy.Name || persisted.Type != models.StrategySell {
		_ = persistedSvc.Close()
		t.Fatalf("persisted strategy mismatch: %#v", persisted)
	}
	_ = persistedSvc.Close()

	dryRun, err := svc.RunOnce(ctx, strategy, false)
	if err != nil {
		t.Fatalf("dry-run reconciliation: %v", err)
	}
	if !dryRun.DryRun || dryRun.Desired != 1 || dryRun.Created != 1 || len(dryRun.TxHashes) != 0 {
		t.Fatalf("unexpected dry-run report: %+v", dryRun)
	}
	assertTerminalShowsStrategy(t, cfg, strategy.ID, strategy.Name)

	if err := svc.StartStrategy(strategy.ID); err != nil {
		t.Fatalf("start strategy: %v", err)
	}
	strategy, err = svc.GetStrategy(strategy.ID)
	if err != nil {
		t.Fatalf("reload started strategy: %v", err)
	}
	live, err := svc.RunOnce(ctx, strategy, true)
	if err != nil {
		t.Fatalf("live reconciliation: %v", err)
	}
	if live.DryRun || live.Created != 1 || len(live.TxHashes) != 1 {
		t.Fatalf("unexpected live report: %+v", live)
	}
	t.Logf("Offer transaction: %s", live.TxHashes[0])
	waitForTradingOfferCount(t, ctx, marketClient, account.Address, strategy, 1)

	managed, err := svc.ManagedOffers(strategy.ID)
	if err != nil {
		t.Fatalf("load managed offers: %v", err)
	}
	if len(managed) != 1 || managed[0].OfferID == 0 || managed[0].Status != "open" {
		t.Fatalf("unexpected managed offers: %+v", managed)
	}

	secondRun, err := svc.RunOnce(ctx, strategy, true)
	if err != nil {
		t.Fatalf("second live reconciliation: %v", err)
	}
	if secondRun.Created != 0 || secondRun.Updated != 0 || secondRun.Canceled != 0 || secondRun.Unchanged != 1 {
		t.Fatalf("expected unchanged second reconciliation, got %+v", secondRun)
	}

	executions := svc.GetExecutions(strategy.ID, 20)
	var sawDryRun, sawExecuted bool
	for _, execution := range executions {
		sawDryRun = sawDryRun || execution.Status == models.ExecutionSkipped
		sawExecuted = sawExecuted || execution.Status == models.ExecutionExecuted
	}
	if !sawDryRun || !sawExecuted {
		t.Fatalf("missing persisted dry-run/executed history: %+v", executions)
	}
	if _, err := svc.GetPerformance(strategy.ID); err != nil {
		t.Fatalf("load persisted performance: %v", err)
	}

	cancelResult, err := svc.CancelStrategyOffers(ctx, strategy.ID, true)
	if err != nil {
		t.Fatalf("cancel strategy offers: %v", err)
	}
	if cancelResult == nil || cancelResult.Hash == "" {
		t.Fatal("cancel result did not include a transaction hash")
	}
	t.Logf("Cancel transaction: %s", cancelResult.Hash)
	waitForTradingOfferCount(t, ctx, marketClient, account.Address, strategy, 0)

	if err := svc.KillSwitch(strategy.ID); err != nil {
		t.Fatalf("enable kill switch: %v", err)
	}
	runtimeState, err := svc.RuntimeState(strategy.ID)
	if err != nil {
		t.Fatalf("load runtime state: %v", err)
	}
	if !runtimeState.KillSwitch {
		t.Fatal("kill switch was not persisted")
	}
}

func setupTradingE2EHome(t *testing.T) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Network = string(models.NetworkStellarTestnet)
	cfg.WalletType = string(models.WalletStellar)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save isolated config: %v", err)
	}
	return cfg
}

func createTradingE2EWallet(t *testing.T) *models.Account {
	t.Helper()
	walletSvc := wallet.NewService()
	account, _, err := walletSvc.ConnectStellarWallet(models.NetworkStellarTestnet)
	if err != nil {
		t.Fatalf("create Stellar wallet: %v", err)
	}
	funded, err := walletSvc.FundTestnetAccount(account)
	if err != nil {
		t.Fatalf("fund wallet via friendbot: %v", err)
	}
	if err := walletSvc.AddWalletToRegistry(funded, true); err != nil {
		t.Fatalf("register isolated wallet: %v", err)
	}
	t.Logf("Trading wallet: %s", funded.Address)
	return funded
}

func waitForTradingAccount(t *testing.T, ctx context.Context, client *market.Client, address string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := client.Account(ctx, address); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("account %s not visible to Horizon", address)
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForTradingTrustline(t *testing.T, ctx context.Context, client *market.Client, address string, asset models.AssetRef) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		account, err := client.Account(ctx, address)
		if err == nil && market.HasTrustline(account, asset) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("trustline %s not visible for %s", asset.Canonical(), address)
		}
		time.Sleep(2 * time.Second)
	}
}

func waitForTradingOfferCount(t *testing.T, ctx context.Context, client *market.Client, address string, strategy *models.TradingStrategy, want int) {
	t.Helper()
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		t.Fatalf("parse strategy base asset: %v", err)
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		t.Fatalf("parse strategy quote asset: %v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	var offers []market.OpenOffer
	for {
		offers, err = client.OpenOffersForPair(ctx, address, base, quote)
		if err == nil && len(offers) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("offer count = %d, want %d; offers=%+v lastErr=%v", len(offers), want, offers, err)
		}
		time.Sleep(2 * time.Second)
	}
}

func assertTerminalShowsStrategy(t *testing.T, cfg *config.Config, strategyID, strategyName string) {
	t.Helper()
	model := terminal.NewModel(cfg)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 45})
	model, ok := updated.(terminal.Model)
	if !ok {
		t.Fatalf("unexpected terminal model type %T", updated)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	model, ok = updated.(terminal.Model)
	if !ok {
		t.Fatalf("unexpected terminal model type %T", updated)
	}
	view := model.View()
	if !strings.Contains(view, "BOTS") {
		t.Fatalf("terminal view does not include Bots panel:\n%s", view)
	}
	if !strings.Contains(view, strategyName) && !strings.Contains(view, strategyID) {
		t.Fatalf("terminal Bots panel does not include strategy %s:\n%s", strategyID, view)
	}
}
