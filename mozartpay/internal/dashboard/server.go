package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/trading"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
)

//go:embed static/*
var staticAssets embed.FS

// Server exposes the trading dashboard on a localhost HTTP listener.
type Server struct {
	cfg        *config.Config
	token      string
	fixedToken bool
	handler    http.Handler

	mu      sync.Mutex
	cfgMu   sync.RWMutex
	runners map[string]*strategyRunner

	balanceMu      sync.Mutex
	balanceCache   map[string]liveBalance
	balanceCacheAt time.Time
}

type liveBalance struct {
	balance string
	funded  bool
}

const balanceCacheTTL = 30 * time.Second

type strategyRunner struct {
	cancel context.CancelFunc
	live   bool
}

// WalletSummary is the non-secret active-wallet data shown by the dashboard.
type WalletSummary struct {
	Address string `json:"address,omitempty"`
	Network string `json:"network,omitempty"`
	Balance string `json:"balance,omitempty"`
	Funded  bool   `json:"funded"`
}

// BotSummary combines persisted strategy, runtime, offer, and activity state.
type BotSummary struct {
	Strategy         *models.TradingStrategy     `json:"strategy"`
	Runtime          *models.StrategyRuntime     `json:"runtime,omitempty"`
	Performance      *models.StrategyPerformance `json:"performance,omitempty"`
	Offers           []*models.ManagedOffer      `json:"offers"`
	OpenOffers       int                         `json:"openOffers"`
	SpreadPct        float64                     `json:"spreadPct"`
	RecentExecutions []models.StrategyExecution  `json:"recentExecutions"`
	RecentFills      []models.Fill               `json:"recentFills"`
}

// WalletOption is a selectable wallet entry shown in the dashboard picker.
type WalletOption struct {
	Address string `json:"address"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Network string `json:"network"`
	Balance string `json:"balance"`
	Funded  bool   `json:"funded"`
	Active  bool   `json:"active"`
}

// Overview is the dashboard state snapshot streamed to the browser.
type Overview struct {
	Network     string         `json:"network"`
	Wallet      WalletSummary  `json:"wallet"`
	Wallets     []WalletOption `json:"wallets"`
	GeneratedAt time.Time      `json:"generatedAt"`
	Bots        []BotSummary   `json:"bots"`
}

// NewServer creates a dashboard server with a random local session token.
func NewServer(cfg *config.Config) (*Server, error) {
	return NewServerWithToken(cfg, "")
}

// NewServerWithToken creates a dashboard server. When token is empty a random
// session token is generated; a non-empty token is reused for every session.
func NewServerWithToken(cfg *config.Config, token string) (*Server, error) {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	fixed := token != ""
	if !fixed {
		var err error
		token, err = randomToken()
		if err != nil {
			return nil, err
		}
	}
	s := &Server{cfg: cfg, token: token, fixedToken: fixed, runners: make(map[string]*strategyRunner)}
	s.handler = s.routes()
	return s, nil
}

// Token returns the local session token required by API endpoints.
func (s *Server) Token() string { return s.token }

// FixedToken reports whether the session token was configured rather than generated.
func (s *Server) FixedToken() bool { return s.fixedToken }

// Handler returns the HTTP handler for tests or embedding.
func (s *Server) Handler() http.Handler { return s.handler }

// Listen binds the dashboard listener without starting it.
func (s *Server) Listen(host string, port int) (net.Listener, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	return net.Listen("tcp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
}

// BrowserURL returns a one-time local login URL that establishes the dashboard cookie.
func (s *Server) BrowserURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Sprintf("http://%s/auth?token=%s", addr, s.token)
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%s/auth?token=%s", host, port, s.token)
}

// Serve runs the dashboard until ctx is canceled or the server fails.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if err == http.ErrServerClosed {
			err = nil
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		s.stopRunners()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-errCh
		return nil
	case err := <-errCh:
		s.stopRunners()
		return err
	}
}

// OpenBrowser opens URL in the user's default browser.
func OpenBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd, args = "open", []string{url}
	case "linux":
		cmd, args = "xdg-open", []string{url}
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
	return exec.Command(cmd, args...).Start()
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, err := fs.Sub(staticAssets, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/auth", s.handleAuth)
	mux.HandleFunc("/api/overview", s.withAuth(s.handleOverview))
	mux.HandleFunc("/api/events", s.withAuth(s.handleEvents))
	mux.HandleFunc("/api/network", s.withAuth(s.handleNetwork))
	mux.HandleFunc("/api/wallets/active", s.withAuth(s.handleActiveWallet))
	mux.HandleFunc("/api/strategies", s.withAuth(s.handleStrategies))
	mux.HandleFunc("/api/strategies/", s.withAuth(s.handleStrategy))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'self'")
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !s.validToken(r.URL.Query().Get("token")) {
		writeError(w, http.StatusUnauthorized, "invalid dashboard token")
		return
	}
	cookie := &http.Cookie{
		Name:     "mozartpay_dashboard",
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
	if s.fixedToken {
		// A configured token stays valid across restarts, so persist the cookie.
		cookie.MaxAge = int((30 * 24 * time.Hour).Seconds())
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "time": time.Now().UTC()})
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	overview, err := s.overview()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fmt.Fprint(w, "retry: 2000\n\n")
	flusher.Flush()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		overview, err := s.overview()
		if err == nil {
			data, _ := json.Marshal(overview)
			fmt.Fprintf(w, "event: state\ndata: %s\n\n", data)
		} else {
			data, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		}
		flusher.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

// handleNetwork switches the configured Stellar network (testnet/mainnet).
func (s *Server) handleNetwork(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Network string `json:"network"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := s.setNetwork(req.Network); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "network": s.network()})
}

func (s *Server) setNetwork(network string) error {
	switch models.Network(network) {
	case models.NetworkStellarTestnet, models.NetworkStellarMainnet:
	default:
		return fmt.Errorf("unsupported network %q (want stellar-testnet or stellar-mainnet)", network)
	}
	s.cfgMu.Lock()
	if s.cfg.Network == network {
		s.cfgMu.Unlock()
		return nil
	}
	s.cfg.Network = network
	if network == string(models.NetworkStellarMainnet) {
		s.cfg.Integrations.StellarHorizonURL = "https://horizon.stellar.org"
	} else {
		s.cfg.Integrations.StellarHorizonURL = "https://horizon-testnet.stellar.org"
	}
	cfg := *s.cfg
	s.cfgMu.Unlock()
	// Dashboard runners are bound to the previous network; stop them.
	s.stopRunners()
	if err := config.Save(&cfg); err != nil {
		return err
	}
	s.selectWalletForNetwork(network)
	return nil
}

// selectWalletForNetwork activates the first wallet on the given network when
// the current active wallet belongs to a different network.
func (s *Server) selectWalletForNetwork(network string) {
	walletSvc := wallet.NewService()
	if account, err := walletSvc.GetActiveWallet(); err == nil && string(account.Network) == network {
		return
	}
	entries, _, err := walletSvc.ListWallets()
	if err != nil {
		return
	}
	for _, entry := range entries {
		if string(entry.Network) == network {
			_ = walletSvc.SetActiveWallet(entry.Address)
			return
		}
	}
}

// handleActiveWallet selects which registered wallet is active.
func (s *Server) handleActiveWallet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Address) == "" {
		writeError(w, http.StatusBadRequest, "wallet address is required")
		return
	}
	if err := wallet.NewService().SetActiveWallet(req.Address); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "address": req.Address})
}

func (s *Server) handleStrategies(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/strategies" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Name        string                 `json:"name"`
		Type        models.StrategyType    `json:"type"`
		BaseAsset   string                 `json:"baseAsset"`
		QuoteAsset  string                 `json:"quoteAsset"`
		Parameters  map[string]interface{} `json:"parameters"`
		RiskLimits  models.RiskLimits      `json:"riskLimits"`
		Description string                 `json:"description"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Type != models.StrategyBuySell && req.Type != models.StrategySell {
		writeError(w, http.StatusBadRequest, "dashboard supports buysell and sell strategies")
		return
	}
	if req.Parameters == nil {
		req.Parameters = map[string]interface{}{}
	}
	if req.RiskLimits == (models.RiskLimits{}) {
		req.RiskLimits = models.DefaultRiskLimits()
	}
	svc := trading.NewService(models.Network(s.network()))
	defer svc.Close()
	strategy, err := svc.CreateStrategy(req.Name, req.Type, req.BaseAsset, req.QuoteAsset, req.Parameters, req.RiskLimits)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	strategy.Description = req.Description
	_ = svc.Store().SaveStrategy(strategy)
	writeJSON(w, http.StatusCreated, strategy)
}

func (s *Server) handleStrategy(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/strategies/"), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] != "action" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Action  string `json:"action"`
		AllPair bool   `json:"allPair"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	svc := trading.NewService(models.Network(s.network()))
	defer svc.Close()
	strategyID := parts[0]
	var result interface{}
	var err error
	switch req.Action {
	case "start", "resume":
		err = svc.StartStrategy(strategyID)
	case "pause":
		err = svc.PauseStrategy(strategyID)
		s.stopRunner(strategyID)
	case "stop":
		err = svc.StopStrategy(strategyID)
		s.stopRunner(strategyID)
	case "kill":
		err = svc.KillSwitch(strategyID)
		s.stopRunner(strategyID)
	case "clear_kill":
		err = svc.ClearKillSwitch(strategyID)
	case "cancel":
		result, err = svc.CancelStrategyOffers(r.Context(), strategyID, req.AllPair)
	case "dry_run":
		var strategy *models.TradingStrategy
		strategy, err = svc.GetStrategy(strategyID)
		if err == nil {
			result, err = svc.RunOnce(r.Context(), strategy, false)
		}
	case "run_dry":
		result, err = s.startRunner(strategyID, false)
	case "run_live":
		result, err = s.startRunner(strategyID, true)
	case "delete":
		err = svc.DeleteStrategy(strategyID)
		s.stopRunner(strategyID)
	default:
		writeError(w, http.StatusBadRequest, "unsupported action")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "result": result})
}

func (s *Server) startRunner(strategyID string, live bool) (map[string]interface{}, error) {
	s.mu.Lock()
	if _, exists := s.runners[strategyID]; exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("strategy %s already has a dashboard runner", strategyID)
	}
	s.mu.Unlock()

	svc := trading.NewService(models.Network(s.network()))
	strategy, err := svc.GetStrategy(strategyID)
	if err == nil && strategy.Status != models.StrategyActive {
		err = fmt.Errorf("strategy %s is not active", strategyID)
	}
	if err == nil {
		if runtimeState, runtimeErr := svc.RuntimeState(strategyID); runtimeErr == nil && runtimeState.KillSwitch {
			err = fmt.Errorf("kill switch is enabled for strategy %s", strategyID)
		}
	}
	if err == nil {
		if runtimeState, runtimeErr := svc.RuntimeState(strategyID); runtimeErr == nil && runtimeState.LastHeartbeat != nil {
			if time.Since(*runtimeState.LastHeartbeat) < 2*time.Minute && runtimeState.PID != os.Getpid() {
				err = fmt.Errorf("strategy %s already has a running process (pid %d)", strategyID, runtimeState.PID)
			}
		}
	}
	_ = svc.Close()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	runner := &strategyRunner{cancel: cancel, live: live}
	s.mu.Lock()
	s.runners[strategyID] = runner
	s.mu.Unlock()

	runnerSvc := trading.NewService(models.Network(s.network()))
	go func() {
		runErr := runnerSvc.RunStrategy(ctx, strategyID, live)
		_ = runnerSvc.Close()
		s.mu.Lock()
		if s.runners[strategyID] == runner {
			delete(s.runners, strategyID)
		}
		s.mu.Unlock()
		if runErr != nil && runErr != context.Canceled {
			fmt.Fprintf(os.Stderr, "dashboard runner %s stopped: %v\n", strategyID, runErr)
		}
	}()

	return map[string]interface{}{
		"strategyId": strategyID,
		"live":       live,
		"pid":        os.Getpid(),
	}, nil
}

func (s *Server) stopRunner(strategyID string) {
	s.mu.Lock()
	runner := s.runners[strategyID]
	delete(s.runners, strategyID)
	s.mu.Unlock()
	if runner != nil {
		runner.cancel()
	}
}

func (s *Server) stopRunners() {
	s.mu.Lock()
	runners := s.runners
	s.runners = make(map[string]*strategyRunner)
	s.mu.Unlock()
	for _, runner := range runners {
		runner.cancel()
	}
}

// applyLiveBalances refreshes wallet balances from Horizon, cached for
// balanceCacheTTL since SSE rebuilds the overview every few seconds.
func (s *Server) applyLiveBalances(overview *Overview, walletSvc *wallet.Service, network string) {
	s.balanceMu.Lock()
	stale := s.balanceCacheAt.IsZero() || time.Since(s.balanceCacheAt) > balanceCacheTTL
	if stale {
		s.balanceCacheAt = time.Now()
	}
	cache := s.balanceCache
	s.balanceMu.Unlock()

	if stale {
		addresses := make([]string, 0, len(overview.Wallets)+1)
		if overview.Wallet.Address != "" {
			addresses = append(addresses, overview.Wallet.Address)
		}
		for _, opt := range overview.Wallets {
			addresses = append(addresses, opt.Address)
		}
		fresh := make(map[string]liveBalance, len(addresses))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, address := range addresses {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				if bal, funded, err := walletSvc.FetchBalanceFromNetwork(addr, models.Network(network)); err == nil {
					mu.Lock()
					fresh[addr] = liveBalance{bal, funded}
					mu.Unlock()
				}
			}(address)
		}
		wg.Wait()
		s.balanceMu.Lock()
		if s.balanceCache == nil {
			s.balanceCache = make(map[string]liveBalance)
		}
		for address, b := range fresh {
			s.balanceCache[address] = b
		}
		cache = s.balanceCache
		s.balanceMu.Unlock()
	}

	for i, opt := range overview.Wallets {
		if b, ok := cache[opt.Address]; ok {
			overview.Wallets[i].Balance = b.balance
			overview.Wallets[i].Funded = b.funded
		}
	}
	if b, ok := cache[overview.Wallet.Address]; ok {
		overview.Wallet.Balance = b.balance
		overview.Wallet.Funded = b.funded
	}
}

// network returns the configured network under a read lock.
func (s *Server) network() string {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Network
}

func (s *Server) overview() (*Overview, error) {
	network := s.network()
	overview := &Overview{
		Network:     network,
		GeneratedAt: time.Now().UTC(),
		Wallets:     []WalletOption{},
		Bots:        []BotSummary{},
	}
	walletSvc := wallet.NewService()
	if entries, active, err := walletSvc.ListWallets(); err == nil {
		for _, entry := range entries {
			if string(entry.Network) != network {
				continue
			}
			overview.Wallets = append(overview.Wallets, WalletOption{
				Address: entry.Address,
				Name:    entry.Name,
				Type:    string(entry.Type),
				Network: string(entry.Network),
				Balance: entry.Balance,
				Funded:  entry.Funded,
				Active:  entry.Address == active,
			})
		}
	}
	if account, err := walletSvc.GetActiveWallet(); err == nil && account != nil {
		overview.Wallet = WalletSummary{
			Address: account.Address,
			Network: string(account.Network),
			Balance: account.Balance,
			Funded:  account.Funded,
		}
	}

	s.applyLiveBalances(overview, walletSvc, network)

	svc := trading.NewService(models.Network(network))
	defer svc.Close()
	activeAddress := overview.Wallet.Address
	strategies := svc.GetAllStrategies()
	for _, strategy := range strategies {
		if string(strategy.Network) != network {
			continue
		}
		if strategy.Wallet != "" && strategy.Wallet != activeAddress {
			continue
		}
		bot := BotSummary{Strategy: strategy, Offers: []*models.ManagedOffer{}}
		if runtimeState, err := svc.RuntimeState(strategy.ID); err == nil {
			bot.Runtime = runtimeState
		}
		if performance, err := svc.GetPerformance(strategy.ID); err == nil {
			bot.Performance = performance
		}
		if offers, err := svc.ManagedOffers(strategy.ID); err == nil {
			bot.Offers = offers
			minAsk, maxBid := 0.0, 0.0
			for _, offer := range offers {
				if offer.Status != "open" && offer.Status != "submitted" && offer.Status != "desired" {
					continue
				}
				bot.OpenOffers++
				if offer.Side == models.OrderSideSell && (minAsk == 0 || offer.Price < minAsk) {
					minAsk = offer.Price
				}
				if offer.Side == models.OrderSideBuy && offer.Price > maxBid {
					maxBid = offer.Price
				}
			}
			if minAsk > 0 && maxBid > 0 {
				mid := (minAsk + maxBid) / 2
				if mid > 0 {
					bot.SpreadPct = (minAsk - maxBid) / mid * 100
				}
			}
		}
		bot.RecentExecutions = svc.GetExecutions(strategy.ID, 8)
		bot.RecentFills = svc.GetFills(strategy.ID, 8)
		overview.Bots = append(overview.Bots, bot)
	}
	return overview, nil
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if token == "" {
			token = r.Header.Get("X-Mozart-Dashboard-Token")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			if cookie, err := r.Cookie("mozartpay_dashboard"); err == nil {
				token = cookie.Value
			}
		}
		if !s.validToken(token) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r)
	}
}

func (s *Server) validToken(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(s.token)) == 1
}

func randomToken() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", fmt.Errorf("generate dashboard token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data[:]), nil
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{"error": message})
}
