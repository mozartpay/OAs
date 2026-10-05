package trading

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
	hProtocol "github.com/stellar/go/protocols/horizon"
)

// OfferAction is a planned reconciliation action.
type OfferAction struct {
	Kind     string               `json:"kind"` // create, update, cancel
	IntentID string               `json:"intentId,omitempty"`
	Side     models.OrderSide     `json:"side"`
	Price    float64              `json:"price"`
	Amount   float64              `json:"amount"`
	OfferID  int64                `json:"offerId,omitempty"`
	Spec     market.OfferSpec     `json:"-"`
	Managed  *models.ManagedOffer `json:"-"`
}

// ReconcileReport summarizes a strategy iteration.
type ReconcileReport struct {
	StrategyID     string        `json:"strategyId"`
	DryRun         bool          `json:"dryRun"`
	ReferencePrice float64       `json:"referencePrice,omitempty"`
	Feed           string        `json:"feed,omitempty"`
	Desired        int           `json:"desired"`
	Created        int           `json:"created"`
	Updated        int           `json:"updated"`
	Canceled       int           `json:"canceled"`
	Unchanged      int           `json:"unchanged"`
	Orphaned       int           `json:"orphaned"`
	OpenOffers     int           `json:"openOffers"`
	TxHashes       []string      `json:"txHashes,omitempty"`
	Actions        []OfferAction `json:"actions,omitempty"`
	Errors         []string      `json:"errors,omitempty"`
	ExecutedAt     time.Time     `json:"executedAt"`
}

// ReconcileStrategy generates desired offers and reconciles them with SDEX.
func (s *Service) ReconcileStrategy(ctx context.Context, strategy *models.TradingStrategy, dryRun bool) (*ReconcileReport, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	report := &ReconcileReport{StrategyID: strategy.ID, DryRun: dryRun, ExecutedAt: time.Now().UTC()}
	if strategy.Network != s.network {
		return report, fmt.Errorf("strategy network %s does not match service network %s", strategy.Network, s.network)
	}

	bot, err := s.NewBot(strategy)
	if err != nil {
		return report, err
	}
	desired, quote, err := bot.GenerateOrders(ctx)
	if err != nil {
		return report, err
	}
	report.Desired = len(desired)
	if quote != nil {
		report.ReferencePrice = quote.Price
		report.Feed = quote.Source
	}

	account, accountErr := s.activeAccount(ctx)
	if accountErr != nil {
		if !dryRun {
			return report, accountErr
		}
		report.Errors = append(report.Errors, fmt.Sprintf("wallet checks skipped in dry-run: %v", accountErr))
	}
	if account != nil {
		if err := s.checkRisk(ctx, strategy, account, desired); err != nil {
			return report, err
		}
	}

	var pairBase, pairQuote models.AssetRef
	if len(desired) > 0 {
		pairBase, pairQuote = desired[0].BaseAsset, desired[0].QuoteAsset
	} else {
		pairBase, err = market.ParseAsset(strategy.BaseAsset, strategy.Network)
		if err != nil {
			return report, err
		}
		pairQuote, err = market.ParseAsset(strategy.QuoteAsset, strategy.Network)
		if err != nil {
			return report, err
		}
	}
	var liveOffers []market.OpenOffer
	if account != nil {
		liveOffers, err = s.market.OpenOffersForPair(ctx, account.AccountID, pairBase, pairQuote)
		if err != nil {
			return report, err
		}
	}
	managed, err := s.store.ListManagedOffers(strategy.ID)
	if err != nil {
		return report, err
	}

	priceTolerance := paramFloat(strategy.Parameters, "price_tolerance_pct", 0.10) / 100
	amountTolerance := paramFloat(strategy.Parameters, "amount_tolerance_pct", 1.0) / 100
	cancelOrphans := paramBool(strategy.Parameters, "cancel_orphans", false)

	liveByID := make(map[int64]market.OpenOffer, len(liveOffers))
	for _, offer := range liveOffers {
		liveByID[offer.ID] = offer
	}
	managedByIntent := make(map[string]*models.ManagedOffer, len(managed))
	managedByOfferID := make(map[int64]*models.ManagedOffer, len(managed))
	for _, offer := range managed {
		managedByIntent[offer.IntentID] = offer
		if offer.OfferID > 0 {
			managedByOfferID[offer.OfferID] = offer
		}
	}

	desiredKeys := make(map[string]models.OrderIntent, len(desired))
	var actions []OfferAction
	for _, intent := range desired {
		desiredKeys[intent.IntentID] = intent
		record := managedByIntent[intent.IntentID]
		previousStatus := ""
		var previousUpdatedAt time.Time
		if record == nil {
			now := time.Now().UTC()
			record = &models.ManagedOffer{
				IntentID:   intent.IntentID,
				StrategyID: strategy.ID,
				Status:     "desired",
				CreatedAt:  now,
			}
		} else {
			previousStatus = record.Status
			previousUpdatedAt = record.UpdatedAt
		}
		record.Side = intent.Side
		record.BaseAsset = intent.BaseAsset
		record.QuoteAsset = intent.QuoteAsset
		record.Price = intent.Price
		record.Amount = intent.Amount
		record.Passive = intent.Passive
		record.UpdatedAt = time.Now().UTC()
		record.LastError = ""
		if err := s.store.SaveManagedOffer(record); err != nil {
			return report, err
		}

		if record.OfferID == 0 {
			for _, candidate := range liveOffers {
				if offersEquivalent(candidate, intent, priceTolerance, amountTolerance) {
					record.OfferID = candidate.ID
					managedByOfferID[record.OfferID] = record
					break
				}
			}
		}
		live, liveOK := liveByID[record.OfferID]
		if liveOK && offersEquivalent(live, intent, priceTolerance, amountTolerance) {
			record.Status = "open"
			record.Amount = live.Amount
			record.Price = live.Price
			_ = s.store.SaveManagedOffer(record)
			report.Unchanged++
			continue
		}
		if record.OfferID == 0 && previousStatus == "submitted" && time.Since(previousUpdatedAt) < 2*time.Minute {
			record.Status = previousStatus
			record.UpdatedAt = previousUpdatedAt
			_ = s.store.SaveManagedOffer(record)
			report.Unchanged++
			continue
		}

		if liveOK && !intent.Passive {
			spec := specFromIntent(intent)
			spec.OfferID = record.OfferID
			actions = append(actions, OfferAction{Kind: "update", IntentID: intent.IntentID, Side: intent.Side, Price: intent.Price, Amount: intent.Amount, OfferID: record.OfferID, Spec: spec, Managed: record})
			continue
		}
		if liveOK && intent.Passive {
			cancelSpec := specFromIntent(intent)
			cancelSpec.OfferID = record.OfferID
			cancelSpec.Amount = 0
			actions = append(actions, OfferAction{Kind: "cancel", IntentID: intent.IntentID, Side: intent.Side, OfferID: record.OfferID, Spec: cancelSpec, Managed: record})
		}
		spec := specFromIntent(intent)
		actions = append(actions, OfferAction{Kind: "create", IntentID: intent.IntentID, Side: intent.Side, Price: intent.Price, Amount: intent.Amount, Spec: spec, Managed: record})
	}

	for _, record := range managed {
		if _, wanted := desiredKeys[record.IntentID]; wanted || record.OfferID == 0 {
			continue
		}
		live, liveOK := liveByID[record.OfferID]
		if !liveOK {
			record.Status = "closed"
			record.UpdatedAt = time.Now().UTC()
			_ = s.store.SaveManagedOffer(record)
			continue
		}
		spec := market.OfferSpec{OfferID: live.ID, Side: live.Side, Base: live.Base, Quote: live.Quote, Price: live.Price, Amount: 0}
		actions = append(actions, OfferAction{Kind: "cancel", IntentID: record.IntentID, Side: live.Side, OfferID: live.ID, Spec: spec, Managed: record})
	}

	for _, live := range liveOffers {
		if _, owned := managedByOfferID[live.ID]; owned {
			continue
		}
		report.Orphaned++
		if cancelOrphans {
			spec := market.OfferSpec{OfferID: live.ID, Side: live.Side, Base: live.Base, Quote: live.Quote, Price: live.Price, Amount: 0}
			actions = append(actions, OfferAction{Kind: "cancel", Side: live.Side, Price: live.Price, Amount: live.Amount, OfferID: live.ID, Spec: spec})
		}
	}

	report.Actions = actions
	for _, action := range actions {
		switch action.Kind {
		case "create":
			report.Created++
		case "update":
			report.Updated++
		case "cancel":
			report.Canceled++
		}
	}

	if dryRun {
		return report, s.recordReconcileExecution(strategy, report, models.ExecutionSkipped, "dry run")
	}

	if len(actions) > 0 {
		specs := make([]market.OfferSpec, 0, len(actions))
		for _, action := range actions {
			specs = append(specs, action.Spec)
		}
		result, err := s.market.SubmitOffers(ctx, specs)
		if err != nil {
			for _, action := range actions {
				if action.Managed != nil {
					action.Managed.Status = "error"
					action.Managed.LastError = err.Error()
					action.Managed.UpdatedAt = time.Now().UTC()
					_ = s.store.SaveManagedOffer(action.Managed)
				}
			}
			_ = s.recordReconcileExecution(strategy, report, models.ExecutionFailed, err.Error())
			return report, err
		}
		report.TxHashes = append(report.TxHashes, result.Hash)
		s.applySubmissionResult(strategy.ID, actions, result)
		if err := s.syncManagedOffers(ctx, strategy, account.AccountID); err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
	}

	if err := s.syncFills(ctx, strategy, account.AccountID); err != nil {
		report.Errors = append(report.Errors, err.Error())
	}
	if managed, err := s.store.ListManagedOffers(strategy.ID); err == nil {
		for _, offer := range managed {
			if offer.Status == "open" || offer.Status == "submitted" {
				report.OpenOffers++
			}
		}
	}
	return report, s.recordReconcileExecution(strategy, report, models.ExecutionExecuted, "")
}

func (s *Service) activeAccount(ctx context.Context) (*hProtocol.Account, error) {
	kp, err := wallet.LoadStellarKeypairForSwap()
	if err != nil {
		return nil, err
	}
	active, err := wallet.NewService().GetActiveWallet()
	if err != nil {
		return nil, fmt.Errorf("active wallet: %w", err)
	}
	if active.Address != kp.Address() {
		return nil, fmt.Errorf("active wallet mismatch: %s != %s", active.Address, kp.Address())
	}
	if active.Network != s.network {
		return nil, fmt.Errorf("active wallet network %s does not match service network %s", active.Network, s.network)
	}
	account, err := s.market.Account(ctx, kp.Address())
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func (s *Service) checkRisk(ctx context.Context, strategy *models.TradingStrategy, account *hProtocol.Account, desired []models.OrderIntent) error {
	if len(desired) == 0 {
		return nil
	}
	active, err := wallet.NewService().GetActiveWallet()
	if err != nil {
		return fmt.Errorf("active wallet: %w", err)
	}
	if active.Address != account.AccountID {
		return fmt.Errorf("active wallet mismatch: %s != %s", active.Address, account.AccountID)
	}
	if active.Network != strategy.Network {
		return fmt.Errorf("wallet network %s does not match strategy network %s", active.Network, strategy.Network)
	}
	if runtimeState, err := s.store.GetRuntime(strategy.ID); err == nil && runtimeState.KillSwitch {
		return fmt.Errorf("kill switch is enabled for strategy %s", strategy.ID)
	}
	if strategy.RiskLimits.MaxOpenTrades > 0 && len(desired) > strategy.RiskLimits.MaxOpenTrades {
		return fmt.Errorf("desired orders %d exceed max_open_trades %d", len(desired), strategy.RiskLimits.MaxOpenTrades)
	}

	minReserve := paramFloat(strategy.Parameters, "min_xlm_reserve", 1.0)
	sellBase := 0.0
	buyBase := 0.0
	buyQuote := 0.0
	for _, intent := range desired {
		if !market.HasTrustline(*account, intent.BaseAsset) {
			return fmt.Errorf("missing trustline for %s", intent.BaseAsset.Canonical())
		}
		if !market.HasTrustline(*account, intent.QuoteAsset) {
			return fmt.Errorf("missing trustline for %s", intent.QuoteAsset.Canonical())
		}
		if intent.Amount <= 0 || intent.Price <= 0 {
			return fmt.Errorf("invalid intent %s amount/price", intent.IntentID)
		}
		if intent.Side == models.OrderSideSell {
			sellBase += intent.Amount
		} else {
			buyBase += intent.Amount
			buyQuote += intent.Amount * intent.Price
		}
	}
	if strategy.RiskLimits.MaxPositionSize > 0 {
		if sellBase > strategy.RiskLimits.MaxPositionSize {
			return fmt.Errorf("sell exposure %.7f exceeds max_position_size %.7f", sellBase, strategy.RiskLimits.MaxPositionSize)
		}
		if buyBase > strategy.RiskLimits.MaxPositionSize {
			return fmt.Errorf("buy exposure %.7f exceeds max_position_size %.7f", buyBase, strategy.RiskLimits.MaxPositionSize)
		}
	}
	if sellBase > market.Spendable(*account, desired[0].BaseAsset, minReserve)+1e-7 {
		return fmt.Errorf("insufficient spendable %s: need %.7f", desired[0].BaseAsset.Code, sellBase)
	}
	if buyQuote > market.Spendable(*account, desired[0].QuoteAsset, minReserve)+1e-7 {
		return fmt.Errorf("insufficient spendable %s: need %.7f", desired[0].QuoteAsset.Code, buyQuote)
	}
	if strategy.RiskLimits.MaxDailyLoss > 0 {
		executions, err := s.store.ListExecutions(strategy.ID, 500)
		if err == nil {
			dayAgo := time.Now().UTC().Add(-24 * time.Hour)
			dailyLoss := 0.0
			for _, execution := range executions {
				if execution.Timestamp.After(dayAgo) && execution.ProfitLoss < 0 {
					dailyLoss += math.Abs(execution.ProfitLoss)
				}
			}
			if dailyLoss >= strategy.RiskLimits.MaxDailyLoss {
				return fmt.Errorf("daily loss limit reached: %.7f >= %.7f", dailyLoss, strategy.RiskLimits.MaxDailyLoss)
			}
		}
	}
	if strategy.RiskLimits.MaxDrawdown > 0 {
		if perf, err := s.store.GetPerformance(strategy.ID); err == nil && perf.MaxDrawdown >= strategy.RiskLimits.MaxDrawdown {
			return fmt.Errorf("drawdown limit reached: %.2f%% >= %.2f%%", perf.MaxDrawdown, strategy.RiskLimits.MaxDrawdown)
		}
	}
	return nil
}

func specFromIntent(intent models.OrderIntent) market.OfferSpec {
	return market.OfferSpec{
		Side:    intent.Side,
		Base:    intent.BaseAsset,
		Quote:   intent.QuoteAsset,
		Price:   intent.Price,
		Amount:  intent.Amount,
		Passive: intent.Passive,
	}
}

func offersEquivalent(live market.OpenOffer, intent models.OrderIntent, priceTolerance, amountTolerance float64) bool {
	if live.Side != intent.Side || !market.Equal(live.Base, intent.BaseAsset) || !market.Equal(live.Quote, intent.QuoteAsset) {
		return false
	}
	return withinTolerance(live.Price, intent.Price, priceTolerance) && withinTolerance(live.Amount, intent.Amount, amountTolerance)
}

func withinTolerance(actual, desired, tolerance float64) bool {
	if desired == 0 {
		return actual == 0
	}
	return math.Abs(actual-desired)/desired <= tolerance
}

func (s *Service) applySubmissionResult(strategyID string, actions []OfferAction, result *market.SubmitResult) {
	now := time.Now().UTC()
	for i, action := range actions {
		offerID := int64(0)
		if i < len(result.Operations) {
			offerID = result.Operations[i].OfferID
		}
		if offerID == 0 {
			offerID = action.Spec.OfferID
		}
		if action.Managed == nil {
			continue
		}
		action.Managed.UpdatedAt = now
		action.Managed.SubmittedAt = &now
		action.Managed.TxHash = result.Hash
		if action.Kind == "cancel" {
			action.Managed.Status = "canceled"
		} else {
			action.Managed.Status = "submitted"
			if offerID > 0 {
				action.Managed.OfferID = offerID
			}
		}
		_ = s.store.SaveManagedOffer(action.Managed)
	}
}

func (s *Service) syncManagedOffers(ctx context.Context, strategy *models.TradingStrategy, account string) error {
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		return err
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		return err
	}
	liveOffers, err := s.market.OpenOffersForPair(ctx, account, base, quote)
	if err != nil {
		return err
	}
	managed, err := s.store.ListManagedOffers(strategy.ID)
	if err != nil {
		return err
	}
	liveByID := make(map[int64]market.OpenOffer, len(liveOffers))
	for _, offer := range liveOffers {
		liveByID[offer.ID] = offer
	}
	for _, record := range managed {
		if record.OfferID > 0 {
			if live, ok := liveByID[record.OfferID]; ok {
				record.Status = "open"
				record.Price = live.Price
				record.Amount = live.Amount
			} else if record.Status == "submitted" || record.Status == "open" {
				record.Status = "closed"
			}
			record.UpdatedAt = time.Now().UTC()
			_ = s.store.SaveManagedOffer(record)
			continue
		}
		for _, live := range liveOffers {
			if live.Side == record.Side && withinTolerance(live.Price, record.Price, 0.0001) && withinTolerance(live.Amount, record.Amount, 0.001) {
				record.OfferID = live.ID
				record.Status = "open"
				record.UpdatedAt = time.Now().UTC()
				_ = s.store.SaveManagedOffer(record)
				break
			}
		}
	}
	return nil
}

func (s *Service) syncFills(ctx context.Context, strategy *models.TradingStrategy, account string) error {
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		return err
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		return err
	}
	managed, err := s.store.ListManagedOffers(strategy.ID)
	if err != nil {
		return err
	}
	offerToIntent := make(map[string]*models.ManagedOffer)
	for _, offer := range managed {
		if offer.OfferID > 0 {
			offerToIntent[strconv.FormatInt(offer.OfferID, 10)] = offer
		}
	}
	if len(offerToIntent) == 0 {
		return nil
	}
	baseType, baseCode, baseIssuer := market.HorizonAsset(base)
	quoteType, quoteCode, quoteIssuer := market.HorizonAsset(quote)
	trades, err := s.market.Trades(ctx, account, baseType, baseCode, baseIssuer, quoteType, quoteCode, quoteIssuer, 200)
	if err != nil {
		return err
	}
	newFills := 0
	for _, trade := range trades {
		offerID := ""
		if _, ok := offerToIntent[trade.BaseOfferID]; ok {
			offerID = trade.BaseOfferID
		} else if _, ok := offerToIntent[trade.CounterOfferID]; ok {
			offerID = trade.CounterOfferID
		}
		if offerID == "" {
			continue
		}
		intent := offerToIntent[offerID]
		baseAmount, _ := strconv.ParseFloat(trade.BaseAmount, 64)
		counterAmount, _ := strconv.ParseFloat(trade.CounterAmount, 64)
		amount := baseAmount
		counter := counterAmount
		fill := &models.Fill{
			ID:         fmt.Sprintf("fill-%s", trade.ID),
			StrategyID: strategy.ID,
			IntentID:   intent.IntentID,
			TradeID:    trade.ID,
			Price:      market.TradePrice(trade),
			Amount:     amount,
			Counter:    counter,
			ExecutedAt: trade.LedgerCloseTime,
		}
		if offerID != "" {
			if parsed, err := strconv.ParseInt(offerID, 10, 64); err == nil {
				fill.OfferID = parsed
			}
		}
		exists, err := s.store.HasFill(fill.ID)
		if err != nil || exists {
			continue
		}
		if err := s.store.SaveFill(fill); err != nil {
			continue
		}
		action := models.TradeSell
		if intent.Side == models.OrderSideBuy {
			action = models.TradeBuy
		}
		execution := &models.StrategyExecution{
			ID:           "exec-" + fill.ID,
			StrategyID:   strategy.ID,
			StrategyType: strategy.Type,
			Timestamp:    fill.ExecutedAt,
			Action:       action,
			BaseAsset:    strategy.BaseAsset,
			QuoteAsset:   strategy.QuoteAsset,
			Amount:       fill.Amount,
			Price:        fill.Price,
			Value:        fill.Amount * fill.Price,
			Status:       models.ExecutionExecuted,
			Metadata: map[string]interface{}{
				"trade_id": trade.ID,
				"offer_id": offerID,
			},
		}
		_ = s.store.SaveExecution(execution)
		strategy.TotalTrades++
		newFills++
	}
	if newFills > 0 {
		strategy.UpdatedAt = time.Now().UTC()
		_ = s.store.SaveStrategy(strategy)
		s.updatePerformance(strategy.ID)
	}
	return nil
}

// ManagedOffers returns persisted offers for a strategy.
func (s *Service) ManagedOffers(strategyID string) ([]*models.ManagedOffer, error) {
	if err := s.storeReady(); err != nil {
		return nil, err
	}
	return s.store.ListManagedOffers(strategyID)
}

// LiveOffers returns open SDEX offers for a strategy pair.
func (s *Service) LiveOffers(ctx context.Context, strategy *models.TradingStrategy) ([]market.OpenOffer, error) {
	account, err := s.activeAccount(ctx)
	if err != nil {
		return nil, err
	}
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		return nil, err
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		return nil, err
	}
	return s.market.OpenOffersForPair(ctx, account.AccountID, base, quote)
}

// CancelStrategyOffers cancels all live offers for a strategy pair. allPair also
// cancels open pair offers that are not currently tracked as managed records.
func (s *Service) CancelStrategyOffers(ctx context.Context, strategyID string, allPair bool) (*SubmitResult, error) {
	strategy, err := s.GetStrategy(strategyID)
	if err != nil {
		return nil, err
	}
	if strategy.Network != s.network {
		return nil, fmt.Errorf("strategy network %s does not match service network %s", strategy.Network, s.network)
	}
	live, err := s.LiveOffers(ctx, strategy)
	if err != nil {
		return nil, err
	}
	managed, err := s.store.ListManagedOffers(strategy.ID)
	if err != nil {
		return nil, err
	}
	managedIDs := make(map[int64]*models.ManagedOffer, len(managed))
	for _, offer := range managed {
		if offer.OfferID > 0 {
			managedIDs[offer.OfferID] = offer
		}
	}
	var specs []market.OfferSpec
	for _, offer := range live {
		if _, ok := managedIDs[offer.ID]; !ok && !allPair {
			continue
		}
		specs = append(specs, market.OfferSpec{OfferID: offer.ID, Side: offer.Side, Base: offer.Base, Quote: offer.Quote, Price: offer.Price, Amount: 0})
	}
	if len(specs) == 0 {
		return nil, nil
	}
	result, err := s.market.SubmitOffers(ctx, specs)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, offer := range live {
		if record, ok := managedIDs[offer.ID]; ok {
			record.Status = "canceled"
			record.TxHash = result.Hash
			record.SubmittedAt = &now
			record.UpdatedAt = now
			_ = s.store.SaveManagedOffer(record)
		}
	}
	return result, nil
}

// SubmitOffer submits a single manually requested offer.
func (s *Service) SubmitOffer(ctx context.Context, spec market.OfferSpec) (*market.SubmitResult, error) {
	if spec.Amount <= 0 || spec.Price <= 0 {
		return nil, fmt.Errorf("amount and price must be positive")
	}
	account, err := s.activeAccount(ctx)
	if err != nil {
		return nil, err
	}
	if !market.HasTrustline(*account, spec.Base) {
		return nil, fmt.Errorf("missing trustline for %s", spec.Base.Canonical())
	}
	if !market.HasTrustline(*account, spec.Quote) {
		return nil, fmt.Errorf("missing trustline for %s", spec.Quote.Canonical())
	}
	if spec.Side == models.OrderSideSell {
		if market.Spendable(*account, spec.Base, 1.0) < spec.Amount {
			return nil, fmt.Errorf("insufficient spendable %s", spec.Base.Code)
		}
	} else if market.Spendable(*account, spec.Quote, 1.0) < spec.Amount*spec.Price {
		return nil, fmt.Errorf("insufficient spendable %s", spec.Quote.Code)
	}
	return s.market.SubmitOffers(ctx, []market.OfferSpec{spec})
}

// CancelOfferByID cancels a live offer belonging to the active account.
func (s *Service) CancelOfferByID(ctx context.Context, offerID int64) (*market.SubmitResult, error) {
	account, err := s.activeAccount(ctx)
	if err != nil {
		return nil, err
	}
	offers, err := s.market.OpenOffers(ctx, account.AccountID)
	if err != nil {
		return nil, err
	}
	for _, offer := range offers {
		if offer.ID == offerID {
			return s.market.CancelOffer(ctx, offer)
		}
	}
	return nil, fmt.Errorf("open offer %d not found for active account", offerID)
}

// SubmitResult aliases market submission results for command output.
type SubmitResult = market.SubmitResult

func (s *Service) recordReconcileExecution(strategy *models.TradingStrategy, report *ReconcileReport, status models.ExecutionStatus, errText string) error {
	execution := &models.StrategyExecution{
		ID:           generateExecutionID(),
		StrategyID:   strategy.ID,
		StrategyType: strategy.Type,
		Timestamp:    time.Now().UTC(),
		Action:       models.TradeEnter,
		BaseAsset:    strategy.BaseAsset,
		QuoteAsset:   strategy.QuoteAsset,
		Price:        report.ReferencePrice,
		Status:       status,
		Error:        errText,
		Metadata: map[string]interface{}{
			"dry_run":   report.DryRun,
			"feed":      report.Feed,
			"created":   report.Created,
			"updated":   report.Updated,
			"canceled":  report.Canceled,
			"unchanged": report.Unchanged,
			"orphaned":  report.Orphaned,
			"tx_hashes": report.TxHashes,
		},
	}
	if len(report.TxHashes) > 0 {
		execution.TxHash = report.TxHashes[0]
	}
	return s.store.SaveExecution(execution)
}
