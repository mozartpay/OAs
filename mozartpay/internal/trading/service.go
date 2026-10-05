package trading

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/pool"
	"github.com/ogtechnologies/mozartpay/internal/swap"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
)

// Service handles persistent trading strategy lifecycle and execution.
type Service struct {
	network  models.Network
	swapSvc  *swap.Service
	market   *market.Client
	pools    *pool.Service
	store    *Store
	storeErr error
	mu       sync.RWMutex
	stopCh   chan struct{}
}

// NewService creates a new trading service.
func NewService(network models.Network) *Service {
	store, err := NewStore()
	return &Service{
		network:  network,
		swapSvc:  swap.NewService(network),
		market:   market.NewClient(network),
		pools:    pool.NewService(network),
		store:    store,
		storeErr: err,
		stopCh:   make(chan struct{}),
	}
}

// NewServiceWithDependencies creates a service with explicit dependencies.
func NewServiceWithDependencies(network models.Network, store *Store, marketClient *market.Client, poolSvc *pool.Service) *Service {
	if marketClient == nil {
		marketClient = market.NewClient(network)
	}
	if poolSvc == nil {
		poolSvc = pool.NewService(network)
	}
	return &Service{
		network: network,
		swapSvc: swap.NewService(network),
		market:  marketClient,
		pools:   poolSvc,
		store:   store,
		stopCh:  make(chan struct{}),
	}
}

// Store returns the persistent store.
func (s *Service) Store() *Store { return s.store }

// Close releases service resources.
func (s *Service) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

func (s *Service) storeReady() error {
	if s.storeErr != nil {
		return fmt.Errorf("trading store unavailable: %w", s.storeErr)
	}
	if s.store == nil {
		return fmt.Errorf("trading store unavailable")
	}
	return nil
}

// CreateStrategy creates and persists a new trading strategy.
func (s *Service) CreateStrategy(name string, strategyType models.StrategyType, baseAsset, quoteAsset string, params map[string]interface{}, riskLimits models.RiskLimits) (*models.TradingStrategy, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	strategy := &models.TradingStrategy{
		ID:         generateStrategyID(),
		Name:       name,
		Type:       strategyType,
		Network:    s.network,
		BaseAsset:  baseAsset,
		QuoteAsset: quoteAsset,
		Parameters: params,
		RiskLimits: riskLimits,
		Status:     models.StrategyStopped,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if active, err := wallet.NewService().GetActiveWallet(); err == nil && active != nil {
		strategy.Wallet = active.Address
	}
	if err := s.validateStrategyParams(strategyType, params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if _, err := market.ParseAsset(baseAsset, s.network); err != nil {
		return nil, fmt.Errorf("base asset: %w", err)
	}
	if _, err := market.ParseAsset(quoteAsset, s.network); err != nil {
		return nil, fmt.Errorf("quote asset: %w", err)
	}
	if err := s.store.SaveStrategy(strategy); err != nil {
		return nil, err
	}
	_ = s.store.SavePerformance(&models.StrategyPerformance{StrategyID: strategy.ID, LastUpdated: now})
	return strategy, nil
}

// StartStrategy marks a strategy active. Use RunStrategy/RunActive for execution.
func (s *Service) StartStrategy(strategyID string) error {
	if err := s.storeReady(); err != nil {
		return err
	}
	strategy, err := s.store.GetStrategy(strategyID)
	if err != nil {
		return fmt.Errorf("strategy %s not found", strategyID)
	}
	if strategy.Status == models.StrategyActive {
		return fmt.Errorf("strategy already active")
	}
	if strategy.Network != s.network {
		return fmt.Errorf("strategy network %s does not match configured network %s", strategy.Network, s.network)
	}
	now := time.Now().UTC()
	strategy.Status = models.StrategyActive
	strategy.ActiveSince = &now
	strategy.UpdatedAt = now
	return s.store.SaveStrategy(strategy)
}

// StopStrategy stops a strategy and clears runtime state.
func (s *Service) StopStrategy(strategyID string) error {
	if err := s.storeReady(); err != nil {
		return err
	}
	strategy, err := s.store.GetStrategy(strategyID)
	if err != nil {
		return fmt.Errorf("strategy %s not found", strategyID)
	}
	strategy.Status = models.StrategyStopped
	strategy.UpdatedAt = time.Now().UTC()
	strategy.ActiveSince = nil
	if err := s.store.SaveStrategy(strategy); err != nil {
		return err
	}
	return s.store.ClearRuntime(strategyID)
}

// PauseStrategy temporarily pauses a strategy.
func (s *Service) PauseStrategy(strategyID string) error {
	if err := s.storeReady(); err != nil {
		return err
	}
	strategy, err := s.store.GetStrategy(strategyID)
	if err != nil {
		return fmt.Errorf("strategy %s not found", strategyID)
	}
	if strategy.Status != models.StrategyActive {
		return fmt.Errorf("strategy not active")
	}
	strategy.Status = models.StrategyPaused
	strategy.UpdatedAt = time.Now().UTC()
	return s.store.SaveStrategy(strategy)
}

// GetStrategy retrieves a strategy by ID.
func (s *Service) GetStrategy(strategyID string) (*models.TradingStrategy, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	strategy, err := s.store.GetStrategy(strategyID)
	if err != nil {
		return nil, fmt.Errorf("strategy %s not found", strategyID)
	}
	return strategy, nil
}

// GetAllStrategies returns all configured strategies.
func (s *Service) GetAllStrategies() []*models.TradingStrategy {
	if s.store == nil {
		return nil
	}
	strategies, err := s.store.ListStrategies()
	if err != nil {
		return nil
	}
	return strategies
}

// GetActiveStrategies returns currently active strategies.
func (s *Service) GetActiveStrategies() []*models.TradingStrategy {
	strategies := s.GetAllStrategies()
	active := make([]*models.TradingStrategy, 0)
	for _, strategy := range strategies {
		if strategy.Status == models.StrategyActive {
			active = append(active, strategy)
		}
	}
	return active
}

// RuntimeState returns persisted runtime state for a strategy.
func (s *Service) RuntimeState(strategyID string) (*models.StrategyRuntime, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	return s.store.GetRuntime(strategyID)
}

// GetPerformance returns performance metrics for a strategy.
func (s *Service) GetPerformance(strategyID string) (*models.StrategyPerformance, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	perf, err := s.store.GetPerformance(strategyID)
	if err != nil {
		return nil, fmt.Errorf("no performance data for strategy %s", strategyID)
	}
	return perf, nil
}

// GetExecutions returns execution history for a strategy.
func (s *Service) GetExecutions(strategyID string, limit int) []models.StrategyExecution {
	if s.store == nil {
		return nil
	}
	executions, err := s.store.ListExecutions(strategyID, limit)
	if err != nil {
		return nil
	}
	return executions
}

// GetFills returns observed SDEX fills for a strategy.
func (s *Service) GetFills(strategyID string, limit int) []models.Fill {
	if s.store == nil {
		return nil
	}
	fills, err := s.store.ListFills(strategyID, limit)
	if err != nil {
		return nil
	}
	return fills
}

// DeleteStrategy removes a stopped strategy.
func (s *Service) DeleteStrategy(strategyID string) error {
	if err := s.storeReady(); err != nil {
		return err
	}
	return s.store.DeleteStrategy(strategyID)
}

// RunOnce executes one strategy iteration. Real resting-order strategies require
// live=false for dry-run output or live=true for signed transaction submission.
func (s *Service) RunOnce(ctx context.Context, strategy *models.TradingStrategy, live bool) (*ReconcileReport, error) {
	if strategy.Status != models.StrategyActive && live {
		return nil, fmt.Errorf("strategy is not active")
	}
	if strategy.Type == models.StrategyBuySell || strategy.Type == models.StrategySell {
		report, err := s.ReconcileStrategy(ctx, strategy, !live)
		now := time.Now().UTC()
		strategy.LastRunAt = &now
		strategy.UpdatedAt = now
		_ = s.store.SaveStrategy(strategy)
		return report, err
	}
	if live {
		return nil, fmt.Errorf("live execution is currently supported only for buysell and sell strategies")
	}
	s.executeStrategyIteration(strategy)
	return &ReconcileReport{StrategyID: strategy.ID, DryRun: true, ExecutedAt: time.Now().UTC()}, nil
}

// RunStrategy runs one strategy in a foreground loop until ctx is canceled.
func (s *Service) RunStrategy(ctx context.Context, strategyID string, live bool) error {
	strategy, err := s.GetStrategy(strategyID)
	if err != nil {
		return err
	}
	if strategy.Network != s.network {
		return fmt.Errorf("strategy network %s does not match configured network %s", strategy.Network, s.network)
	}
	if strategy.Status != models.StrategyActive {
		return fmt.Errorf("strategy %s is not active", strategyID)
	}
	return s.runLoop(ctx, strategy, live)
}

// RunActive runs all active strategies for the service network.
func (s *Service) RunActive(ctx context.Context, live bool) error {
	var claimed []string
	defer func() {
		for _, id := range claimed {
			_ = s.store.ClearRuntime(id)
		}
	}()
	for {
		strategies := s.GetActiveStrategies()
		ran := false
		for _, strategy := range strategies {
			if strategy.Network != s.network {
				continue
			}
			fresh, err := s.GetStrategy(strategy.ID)
			if err != nil || fresh.Status != models.StrategyActive {
				continue
			}
			alreadyClaimed := false
			for _, id := range claimed {
				if id == fresh.ID {
					alreadyClaimed = true
					break
				}
			}
			if !alreadyClaimed {
				if err := s.acquireRuntime(fresh.ID, live); err != nil {
					s.markRuntimeError(fresh.ID, err)
					continue
				}
				claimed = append(claimed, fresh.ID)
			}
			bot, err := s.NewBot(fresh)
			if err != nil {
				return err
			}
			if live && !s.runtimeDue(fresh.ID, bot.Interval()) {
				continue
			}
			report, err := s.RunOnce(ctx, fresh, live)
			if err != nil {
				s.markRuntimeError(fresh.ID, err)
				continue
			}
			s.markRuntimeSuccess(fresh.ID, !live)
			_ = report
			ran = true
		}
		if !ran && len(strategies) == 0 {
			return fmt.Errorf("no active strategies for %s", s.network)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func (s *Service) runLoop(ctx context.Context, strategy *models.TradingStrategy, live bool) error {
	if err := s.acquireRuntime(strategy.ID, live); err != nil {
		return err
	}
	defer s.store.ClearRuntime(strategy.ID)
	for {
		fresh, err := s.GetStrategy(strategy.ID)
		if err != nil {
			return err
		}
		if fresh.Status != models.StrategyActive {
			return nil
		}
		interval := 30 * time.Second
		if bot, err := s.NewBot(fresh); err == nil {
			interval = bot.Interval()
		}
		report, err := s.RunOnce(ctx, fresh, live)
		if err != nil {
			s.markRuntimeError(strategy.ID, err)
			if s.maxErrorsReached(fresh) {
				fresh.Status = models.StrategyError
				fresh.UpdatedAt = time.Now().UTC()
				_ = s.store.SaveStrategy(fresh)
				return err
			}
		} else {
			_ = report
			s.markRuntimeSuccess(strategy.ID, !live)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

func (s *Service) acquireRuntime(strategyID string, live bool) error {
	state, err := s.store.GetRuntime(strategyID)
	if err == nil && state.LastHeartbeat != nil && state.PID != 0 {
		if time.Since(*state.LastHeartbeat) < 2*time.Minute {
			return fmt.Errorf("strategy %s already has a running process (pid %d)", strategyID, state.PID)
		}
	}
	now := time.Now().UTC()
	return s.store.SaveRuntime(&models.StrategyRuntime{
		StrategyID:    strategyID,
		PID:           processID(),
		StartedAt:     &now,
		LastHeartbeat: &now,
		DryRun:        !live,
	})
}

func (s *Service) markRuntimeSuccess(strategyID string, dryRun bool) {
	state, err := s.store.GetRuntime(strategyID)
	if err != nil {
		state = &models.StrategyRuntime{StrategyID: strategyID, PID: processID()}
	}
	now := time.Now().UTC()
	state.LastHeartbeat = &now
	state.LastError = ""
	state.ConsecutiveErr = 0
	state.Cycles++
	state.DryRun = dryRun
	_ = s.store.SaveRuntime(state)
}

func (s *Service) markRuntimeError(strategyID string, runErr error) {
	state, err := s.store.GetRuntime(strategyID)
	if err != nil {
		state = &models.StrategyRuntime{StrategyID: strategyID, PID: processID()}
	}
	now := time.Now().UTC()
	state.LastHeartbeat = &now
	state.LastError = runErr.Error()
	state.ConsecutiveErr++
	state.Cycles++
	_ = s.store.SaveRuntime(state)
}

func (s *Service) maxErrorsReached(strategy *models.TradingStrategy) bool {
	maxErrors := paramInt(strategy.Parameters, "max_errors", 5)
	state, err := s.store.GetRuntime(strategy.ID)
	return err == nil && state.ConsecutiveErr >= maxErrors
}

func (s *Service) runtimeDue(strategyID string, interval time.Duration) bool {
	state, err := s.store.GetRuntime(strategyID)
	if err != nil || state.LastHeartbeat == nil || state.Cycles == 0 {
		return true
	}
	return time.Since(*state.LastHeartbeat) >= interval
}

// KillSwitch enables the strategy kill switch and marks it paused.
func (s *Service) KillSwitch(strategyID string) error {
	strategy, err := s.GetStrategy(strategyID)
	if err != nil {
		return err
	}
	state, _ := s.store.GetRuntime(strategyID)
	if state == nil {
		state = &models.StrategyRuntime{StrategyID: strategyID}
	}
	state.KillSwitch = true
	now := time.Now().UTC()
	state.LastHeartbeat = &now
	if err := s.store.SaveRuntime(state); err != nil {
		return err
	}
	strategy.Status = models.StrategyPaused
	strategy.UpdatedAt = now
	return s.store.SaveStrategy(strategy)
}

// ClearKillSwitch disables the kill switch.
func (s *Service) ClearKillSwitch(strategyID string) error {
	state, err := s.store.GetRuntime(strategyID)
	if err != nil {
		state = &models.StrategyRuntime{StrategyID: strategyID}
	}
	state.KillSwitch = false
	return s.store.SaveRuntime(state)
}

// executeStrategyIteration runs a signal-only compatibility iteration.
func (s *Service) executeStrategyIteration(strategy *models.TradingStrategy) {
	defer func() {
		if r := recover(); r != nil {
			strategy.Status = models.StrategyError
			strategy.UpdatedAt = time.Now().UTC()
			_ = s.store.SaveStrategy(strategy)
		}
	}()

	var signal *models.StrategySignal
	switch strategy.Type {
	case models.StrategyArbitrage:
		signal = s.executeArbitrageStrategy(strategy)
	case models.StrategyMeanReversion:
		signal = s.executeMeanReversionStrategy(strategy)
	case models.StrategyMomentum:
		signal = s.executeMomentumStrategy(strategy)
	case models.StrategyGridTrading:
		signal = s.executeGridStrategy(strategy)
	case models.StrategyDCA:
		signal = s.executeDCAStrategy(strategy)
	case models.StrategyBreakout:
		signal = s.executeBreakoutStrategy(strategy)
	case models.StrategyScalping:
		signal = s.executeScalpingStrategy(strategy)
	}

	if signal != nil && signal.Action != models.TradeHold {
		s.executeSignal(strategy, signal)
	}
	now := time.Now().UTC()
	strategy.LastRunAt = &now
	strategy.UpdatedAt = now
	_ = s.store.SaveStrategy(strategy)
}

// executeSignal records a dry-run signal execution.
func (s *Service) executeSignal(strategy *models.TradingStrategy, signal *models.StrategySignal) {
	execution := models.StrategyExecution{
		ID:           generateExecutionID(),
		StrategyID:   strategy.ID,
		StrategyType: strategy.Type,
		Timestamp:    time.Now().UTC(),
		Action:       signal.Action,
		BaseAsset:    strategy.BaseAsset,
		QuoteAsset:   strategy.QuoteAsset,
		Amount:       signal.Amount,
		Price:        signal.Price,
		Value:        signal.Amount * signal.Price,
		Status:       models.ExecutionSkipped,
		TxHash:       "dry-run",
		Metadata:     signal.Metadata,
	}
	if signal.Action == models.TradeSell || signal.Action == models.TradeExit {
		execution.ProfitLoss = s.calculateProfitLoss(strategy, signal)
		execution.ProfitPct = (execution.ProfitLoss / math.Max(execution.Value, 1e-7)) * 100
	}
	_ = s.store.SaveExecution(&execution)
	strategy.TotalTrades++
	strategy.TotalProfit += execution.ProfitLoss
	strategy.UpdatedAt = time.Now().UTC()
	_ = s.store.SaveStrategy(strategy)
	s.updatePerformance(strategy.ID)
}

func (s *Service) calculateProfitLoss(strategy *models.TradingStrategy, signal *models.StrategySignal) float64 {
	var totalBuyValue, totalBuyAmount float64
	executions := s.GetExecutions(strategy.ID, 500)
	for _, exec := range executions {
		if exec.Action == models.TradeBuy {
			totalBuyValue += exec.Value
			totalBuyAmount += exec.Amount
		}
	}
	if totalBuyAmount == 0 {
		return 0
	}
	avgBuyPrice := totalBuyValue / totalBuyAmount
	return signal.Amount*signal.Price - signal.Amount*avgBuyPrice
}

func (s *Service) updatePerformance(strategyID string) {
	strategy, err := s.store.GetStrategy(strategyID)
	if err != nil {
		return
	}
	executions, _ := s.store.ListExecutions(strategyID, 1000)
	perf := &models.StrategyPerformance{StrategyID: strategyID, LastUpdated: time.Now().UTC()}
	var profits, losses float64
	for _, execution := range executions {
		if execution.Status != models.ExecutionExecuted && execution.Status != models.ExecutionSkipped {
			continue
		}
		perf.TotalTrades++
		if execution.ProfitLoss > 0 {
			perf.WinningTrades++
			profits += execution.ProfitLoss
		} else if execution.ProfitLoss < 0 {
			perf.LosingTrades++
			losses += math.Abs(execution.ProfitLoss)
		}
	}
	if perf.TotalTrades > 0 {
		perf.WinRate = float64(perf.WinningTrades) / float64(perf.TotalTrades) * 100
	}
	if perf.WinningTrades > 0 {
		perf.AvgProfit = profits / float64(perf.WinningTrades)
	}
	if perf.LosingTrades > 0 {
		perf.AvgLoss = losses / float64(perf.LosingTrades)
	}
	if losses > 0 {
		perf.ProfitFactor = profits / losses
	}
	perf.TotalReturn = strategy.TotalProfit
	_ = s.store.SavePerformance(perf)
}

// validateStrategyParams validates parameters for each strategy type.
func (s *Service) validateStrategyParams(strategyType models.StrategyType, params map[string]interface{}) error {
	switch strategyType {
	case models.StrategyBuySell:
		if paramFloat(params, "amount_per_level", paramFloat(params, "amount", 0)) <= 0 {
			return fmt.Errorf("buysell requires amount_per_level")
		}
	case models.StrategySell:
		if paramFloat(params, "amount_per_level", paramFloat(params, "amount", 0)) <= 0 {
			return fmt.Errorf("sell requires amount_per_level")
		}
	case models.StrategyGridTrading:
		if _, ok := params["upper_price"]; !ok {
			return fmt.Errorf("grid trading requires upper_price parameter")
		}
		if _, ok := params["lower_price"]; !ok {
			return fmt.Errorf("grid trading requires lower_price parameter")
		}
		if _, ok := params["num_grids"]; !ok {
			return fmt.Errorf("grid trading requires num_grids parameter")
		}
	case models.StrategyDCA:
		if _, ok := params["amount_per_order"]; !ok {
			return fmt.Errorf("DCA requires amount_per_order parameter")
		}
		if _, ok := params["interval_hours"]; !ok {
			return fmt.Errorf("DCA requires interval_hours parameter")
		}
	case models.StrategyMeanReversion:
		if _, ok := params["lookback_periods"]; !ok {
			return fmt.Errorf("mean reversion requires lookback_periods parameter")
		}
		if _, ok := params["std_dev_threshold"]; !ok {
			return fmt.Errorf("mean reversion requires std_dev_threshold parameter")
		}
	case models.StrategyMomentum:
		if _, ok := params["short_ma_periods"]; !ok {
			return fmt.Errorf("momentum requires short_ma_periods parameter")
		}
		if _, ok := params["long_ma_periods"]; !ok {
			return fmt.Errorf("momentum requires long_ma_periods parameter")
		}
	case models.StrategyScalping:
		if kPeriod, ok := params["stochastic_k_period"]; ok {
			if kp, ok := kPeriod.(float64); !ok || kp < 5 || kp > 50 {
				return fmt.Errorf("stochastic_k_period must be a number between 5 and 50")
			}
		}
		if dPeriod, ok := params["stochastic_d_period"]; ok {
			if dp, ok := dPeriod.(float64); !ok || dp < 1 || dp > 10 {
				return fmt.Errorf("stochastic_d_period must be a number between 1 and 10")
			}
		}
	}
	return nil
}

func generateStrategyID() string {
	return fmt.Sprintf("strategy_%d", time.Now().UnixNano())
}

func generateExecutionID() string {
	return fmt.Sprintf("exec_%d", time.Now().UnixNano())
}

func processID() int {
	return os.Getpid()
}

// bollingerBands calculates Bollinger Bands for mean reversion.
func bollingerBands(prices []float64, periods int, stdDevMultiplier float64) (upper, middle, lower float64) {
	if len(prices) < periods {
		return 0, 0, 0
	}
	sum := 0.0
	for i := len(prices) - periods; i < len(prices); i++ {
		sum += prices[i]
	}
	middle = sum / float64(periods)
	varianceSum := 0.0
	for i := len(prices) - periods; i < len(prices); i++ {
		diff := prices[i] - middle
		varianceSum += diff * diff
	}
	stdDev := math.Sqrt(varianceSum / float64(periods))
	return middle + stdDev*stdDevMultiplier, middle, middle - stdDev*stdDevMultiplier
}

func simpleMovingAverage(prices []float64, periods int) float64 {
	if len(prices) < periods {
		return 0
	}
	sum := 0.0
	for i := len(prices) - periods; i < len(prices); i++ {
		sum += prices[i]
	}
	return sum / float64(periods)
}

func rsi(prices []float64, periods int) float64 {
	if len(prices) < periods+1 {
		return 50
	}
	gains, losses := 0.0, 0.0
	for i := len(prices) - periods; i < len(prices); i++ {
		change := prices[i] - prices[i-1]
		if change > 0 {
			gains += change
		} else {
			losses -= change
		}
	}
	avgGain := gains / float64(periods)
	avgLoss := losses / float64(periods)
	if avgLoss == 0 {
		return 100
	}
	return 100 - (100 / (1 + avgGain/avgLoss))
}

func stochastic(prices []float64, kPeriod, dPeriod int) (k, d float64) {
	if len(prices) < kPeriod+dPeriod {
		return 50, 50
	}
	kValues := make([]float64, dPeriod)
	for i := 0; i < dPeriod; i++ {
		startIdx := len(prices) - kPeriod - dPeriod + i
		endIdx := len(prices) - dPeriod + i
		lowest, highest := prices[startIdx], prices[startIdx]
		currentClose := prices[endIdx]
		for j := startIdx; j <= endIdx; j++ {
			if prices[j] < lowest {
				lowest = prices[j]
			}
			if prices[j] > highest {
				highest = prices[j]
			}
		}
		rangeValue := highest - lowest
		if rangeValue == 0 {
			kValues[i] = 50
		} else {
			kValues[i] = ((currentClose - lowest) / rangeValue) * 100
		}
	}
	k = kValues[len(kValues)-1]
	for _, val := range kValues {
		d += val
	}
	return k, d / float64(dPeriod)
}
