package dashboard

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/trading"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
)

func TestDashboardRequiresToken(t *testing.T) {
	server := newTestDashboardServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated overview status = %d, want %d", res.Code, http.StatusUnauthorized)
	}
}

func TestDashboardOverviewAndActions(t *testing.T) {
	server := newTestDashboardServer(t)
	svc := trading.NewService(models.NetworkStellarTestnet)
	defer svc.Close()
	strategy, err := svc.CreateStrategy("Dashboard Test", models.StrategySell, "XLM", "USDC", map[string]interface{}{
		"price_feed":       "fixed:1.25",
		"amount_per_level": 0.0000001,
		"levels":           1,
	}, models.DefaultRiskLimits())
	if err != nil {
		t.Fatalf("create strategy: %v", err)
	}

	overview := getOverview(t, server)
	if len(overview.Bots) != 1 || overview.Bots[0].Strategy.ID != strategy.ID {
		t.Fatalf("overview did not include strategy: %+v", overview)
	}

	for _, step := range []struct {
		action string
		status models.StrategyStatus
	}{
		{action: "start", status: models.StrategyActive},
		{action: "pause", status: models.StrategyPaused},
		{action: "start", status: models.StrategyActive},
		{action: "stop", status: models.StrategyStopped},
	} {
		postAction(t, server, strategy.ID, step.action, http.StatusOK)
		persisted, err := svc.GetStrategy(strategy.ID)
		if err != nil {
			t.Fatalf("reload strategy after %s: %v", step.action, err)
		}
		if persisted.Status != step.status {
			t.Fatalf("status after %s = %s, want %s", step.action, persisted.Status, step.status)
		}
	}

	postAction(t, server, strategy.ID, "kill", http.StatusOK)
	runtimeState, err := svc.RuntimeState(strategy.ID)
	if err != nil || !runtimeState.KillSwitch {
		t.Fatalf("kill switch not persisted: %+v, err=%v", runtimeState, err)
	}
	postAction(t, server, strategy.ID, "clear_kill", http.StatusOK)
	runtimeState, err = svc.RuntimeState(strategy.ID)
	if err != nil || runtimeState.KillSwitch {
		t.Fatalf("kill switch not cleared: %+v, err=%v", runtimeState, err)
	}
	postAction(t, server, strategy.ID, "dry_run", http.StatusOK)
	postAction(t, server, strategy.ID, "unsupported", http.StatusBadRequest)
}

func TestDashboardRunnerLifecycle(t *testing.T) {
	server := newTestDashboardServer(t)
	t.Cleanup(server.stopRunners)

	svc := trading.NewService(models.NetworkStellarTestnet)
	defer svc.Close()
	strategy, err := svc.CreateStrategy("Dashboard Runner", models.StrategyBuySell, "XLM", "USDC", map[string]interface{}{
		"price_feed":        "fixed:1.25",
		"amount_per_level":  0.0000001,
		"levels":            1,
		"spread_pct":        1,
		"level_spacing_pct": 0.1,
		"interval_seconds":  1,
	}, models.DefaultRiskLimits())
	if err != nil {
		t.Fatalf("create strategy: %v", err)
	}
	postAction(t, server, strategy.ID, "start", http.StatusOK)
	postAction(t, server, strategy.ID, "run_dry", http.StatusOK)
	postAction(t, server, strategy.ID, "run_dry", http.StatusBadRequest)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtimeState, stateErr := svc.RuntimeState(strategy.ID)
		if stateErr == nil && runtimeState.Cycles > 0 && runtimeState.DryRun {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	runtimeState, err := svc.RuntimeState(strategy.ID)
	if err != nil || runtimeState.Cycles == 0 || !runtimeState.DryRun {
		t.Fatalf("runner did not update runtime state: %+v, err=%v", runtimeState, err)
	}

	postAction(t, server, strategy.ID, "stop", http.StatusOK)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		server.mu.Lock()
		running := len(server.runners)
		server.mu.Unlock()
		if running == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dashboard runner did not stop")
}

func TestDashboardCreatesStrategy(t *testing.T) {
	server := newTestDashboardServer(t)
	payload := map[string]interface{}{
		"name":       "Created Through API",
		"type":       "buysell",
		"baseAsset":  "XLM",
		"quoteAsset": "USDC",
		"parameters": map[string]interface{}{
			"price_feed":       "fixed:1.0",
			"amount_per_level": 0.0000001,
			"levels":           1,
			"spread_pct":       1,
		},
		"riskLimits": map[string]interface{}{"maxOpenTrades": 2, "maxPositionSize": 1},
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/strategies", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", res.Code, res.Body.String())
	}
	var strategy models.TradingStrategy
	if err := json.Unmarshal(res.Body.Bytes(), &strategy); err != nil {
		t.Fatalf("decode created strategy: %v", err)
	}
	if strategy.ID == "" || strategy.Type != models.StrategyBuySell {
		t.Fatalf("unexpected strategy: %+v", strategy)
	}
	overview := getOverview(t, server)
	if len(overview.Bots) != 1 || overview.Bots[0].Strategy.Name != "Created Through API" {
		t.Fatalf("overview did not include created strategy: %+v", overview)
	}
}

func TestDashboardAuthCookie(t *testing.T) {
	server := newTestDashboardServer(t)
	req := httptest.NewRequest(http.MethodGet, "/auth?token="+server.Token(), nil)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusSeeOther {
		t.Fatalf("auth status = %d, want %d", res.Code, http.StatusSeeOther)
	}
	cookies := res.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "mozartpay_dashboard" || !cookies[0].HttpOnly {
		t.Fatalf("unexpected auth cookies: %+v", cookies)
	}

	overviewReq := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	overviewReq.AddCookie(cookies[0])
	overviewRes := httptest.NewRecorder()
	server.Handler().ServeHTTP(overviewRes, overviewReq)
	if overviewRes.Code != http.StatusOK {
		t.Fatalf("cookie-authenticated overview status = %d", overviewRes.Code)
	}
}

func TestDashboardStaticAndSSE(t *testing.T) {
	server := newTestDashboardServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	body := res.Body.String()
	if res.Code != http.StatusOK || !strings.Contains(body, "Trading Operations") || !strings.Contains(body, "Strategy guide") || !strings.Contains(body, "Frequently asked questions") || !strings.Contains(body, "field-help") || !strings.Contains(body, "Suggested minimum") {
		t.Fatalf("unexpected index response status=%d", res.Code)
	}
	if res.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("missing security headers")
	}

	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	eventsReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/api/events?token="+server.Token(), nil)
	if err != nil {
		t.Fatalf("create events request: %v", err)
	}
	resp, err := httpServer.Client().Do(eventsReq)
	if err != nil {
		t.Fatalf("connect events: %v", err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var event strings.Builder
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read SSE: %v", err)
		}
		event.WriteString(line)
		if strings.Contains(event.String(), "event: state") {
			return
		}
	}
	t.Fatalf("SSE did not emit state event: %q", event.String())
}

func TestDashboardFixedToken(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server, err := NewServerWithToken(config.DefaultConfig(), "fixed-token-123")
	if err != nil {
		t.Fatalf("new dashboard server: %v", err)
	}
	if server.Token() != "fixed-token-123" || !server.FixedToken() {
		t.Fatalf("fixed token not honored: token=%q fixed=%v", server.Token(), server.FixedToken())
	}

	req := httptest.NewRequest(http.MethodGet, "/auth?token=fixed-token-123", nil)
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	cookies := res.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge <= 0 {
		t.Fatalf("fixed token should set a persistent cookie: %+v", cookies)
	}
}

func TestDashboardNetworkSwitch(t *testing.T) {
	server := newTestDashboardServer(t)

	post := func(body string, want int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/network", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+server.Token())
		res := httptest.NewRecorder()
		server.Handler().ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("network switch status = %d, want %d; body=%s", res.Code, want, res.Body.String())
		}
	}

	post(`{"network":"stellar-mainnet"}`, http.StatusOK)
	if got := getOverview(t, server).Network; got != string(models.NetworkStellarMainnet) {
		t.Fatalf("overview network = %s, want stellar-mainnet", got)
	}
	persisted, err := config.Load()
	if err != nil || persisted.Network != string(models.NetworkStellarMainnet) {
		t.Fatalf("config not persisted: %+v err=%v", persisted, err)
	}
	post(`{"network":"evm-sepolia"}`, http.StatusBadRequest)
	post(`{"network":"stellar-testnet"}`, http.StatusOK)
}

func TestDashboardWalletSelect(t *testing.T) {
	server := newTestDashboardServer(t)

	svc := wallet.NewService()
	first := &models.Account{Address: "GFIRST", Network: models.NetworkStellarTestnet, Type: models.WalletStellar, Balance: "10", Funded: true}
	second := &models.Account{Address: "GSECOND", Network: models.NetworkStellarTestnet, Type: models.WalletStellar, Balance: "20", Funded: true}
	if err := svc.AddWalletToRegistry(first, true); err != nil {
		t.Fatalf("add first wallet: %v", err)
	}
	if err := svc.AddWalletToRegistry(second, false); err != nil {
		t.Fatalf("add second wallet: %v", err)
	}

	overview := getOverview(t, server)
	if len(overview.Wallets) != 2 || !overview.Wallets[0].Active {
		t.Fatalf("unexpected wallets in overview: %+v", overview.Wallets)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/wallets/active", strings.NewReader(`{"address":"GSECOND"}`))
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("select wallet status = %d, body=%s", res.Code, res.Body.String())
	}

	overview = getOverview(t, server)
	if !overview.Wallets[1].Active || overview.Wallet.Address != "GSECOND" {
		t.Fatalf("active wallet not updated: %+v / %+v", overview.Wallets, overview.Wallet)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/wallets/active", strings.NewReader(`{"address":"GMISSING"}`))
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res = httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing wallet status = %d, want %d", res.Code, http.StatusBadRequest)
	}
}

func TestDashboardFiltersByNetwork(t *testing.T) {
	server := newTestDashboardServer(t)

	params := map[string]interface{}{"price_feed": "fixed:1.0", "amount_per_level": 1.0}
	testnetSvc := trading.NewService(models.NetworkStellarTestnet)
	defer testnetSvc.Close()
	if _, err := testnetSvc.CreateStrategy("Testnet Bot", models.StrategySell, "XLM", "USDC", params, models.DefaultRiskLimits()); err != nil {
		t.Fatalf("create testnet strategy: %v", err)
	}
	mainnetSvc := trading.NewService(models.NetworkStellarMainnet)
	defer mainnetSvc.Close()
	if _, err := mainnetSvc.CreateStrategy("Mainnet Bot", models.StrategySell, "XLM", "USDC", params, models.DefaultRiskLimits()); err != nil {
		t.Fatalf("create mainnet strategy: %v", err)
	}

	walletSvc := wallet.NewService()
	if err := walletSvc.AddWalletToRegistry(&models.Account{Address: "GTEST", Network: models.NetworkStellarTestnet, Type: models.WalletStellar}, true); err != nil {
		t.Fatalf("add testnet wallet: %v", err)
	}
	if err := walletSvc.AddWalletToRegistry(&models.Account{Address: "GMAIN", Network: models.NetworkStellarMainnet, Type: models.WalletStellar}, false); err != nil {
		t.Fatalf("add mainnet wallet: %v", err)
	}

	overview := getOverview(t, server)
	if len(overview.Wallets) != 1 || overview.Wallets[0].Address != "GTEST" {
		t.Fatalf("testnet wallets not filtered: %+v", overview.Wallets)
	}
	if len(overview.Bots) != 1 || overview.Bots[0].Strategy.Name != "Testnet Bot" {
		t.Fatalf("testnet bots not filtered: %+v", overview.Bots)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/network", strings.NewReader(`{"network":"stellar-mainnet"}`))
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("network switch status = %d, body=%s", res.Code, res.Body.String())
	}

	overview = getOverview(t, server)
	if len(overview.Wallets) != 1 || overview.Wallets[0].Address != "GMAIN" || !overview.Wallets[0].Active {
		t.Fatalf("mainnet wallets not filtered/active: %+v", overview.Wallets)
	}
	if overview.Wallet.Address != "GMAIN" {
		t.Fatalf("active wallet not switched to mainnet: %+v", overview.Wallet)
	}
	if len(overview.Bots) != 1 || overview.Bots[0].Strategy.Name != "Mainnet Bot" {
		t.Fatalf("mainnet bots not filtered: %+v", overview.Bots)
	}
}

func newTestDashboardServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	cfg := config.DefaultConfig()
	cfg.Network = string(models.NetworkStellarTestnet)
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save test config: %v", err)
	}
	server, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("new dashboard server: %v", err)
	}
	return server
}

func getOverview(t *testing.T, server *Server) *Overview {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/overview", nil)
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("overview status = %d, body=%s", res.Code, res.Body.String())
	}
	var overview Overview
	if err := json.Unmarshal(res.Body.Bytes(), &overview); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	return &overview
}

func postAction(t *testing.T, server *Server, strategyID, action string, wantStatus int) {
	t.Helper()
	body := strings.NewReader(`{"action":"` + action + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/strategies/"+strategyID+"/action", body)
	req.Header.Set("Authorization", "Bearer "+server.Token())
	res := httptest.NewRecorder()
	server.Handler().ServeHTTP(res, req)
	if res.Code != wantStatus {
		t.Fatalf("action %s status = %d, want %d; body=%s", action, res.Code, wantStatus, res.Body.String())
	}
}
