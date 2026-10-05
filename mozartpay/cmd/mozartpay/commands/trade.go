package commands

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/dashboard"
	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/trading"
	"github.com/ogtechnologies/mozartpay/internal/ui"
)

func newTradeCmd(cfg *config.Config) *Command {
	cmd := &Command{
		Name:  "trade",
		Short: "Automated trading strategies",
		Long:  "Create, manage, and execute persistent automated trading strategies on Stellar SDEX.",
		cfg:   cfg,
	}
	cmd.addSub(newTradeStrategyCmd(cfg))
	cmd.addSub(newTradeListCmd(cfg))
	cmd.addSub(newTradeStartCmd(cfg))
	cmd.addSub(newTradePauseCmd(cfg))
	cmd.addSub(newTradeStopCmd(cfg))
	cmd.addSub(newTradeRunCmd(cfg))
	cmd.addSub(newTradeOrderCmd(cfg))
	cmd.addSub(newTradeOffersCmd(cfg))
	cmd.addSub(newTradeCancelCmd(cfg))
	cmd.addSub(newTradeKillCmd(cfg))
	cmd.addSub(newTradeBacktestCmd(cfg))
	cmd.addSub(newTradeStatusCmd(cfg))
	cmd.addSub(newTradePerformanceCmd(cfg))
	cmd.addSub(newTradeHistoryCmd(cfg))
	cmd.addSub(newTradeFillsCmd(cfg))
	cmd.addSub(newTradeDashboardCmd(cfg))
	cmd.Run = func(c *Command, args []string) error {
		c.printHelp()
		return nil
	}
	return cmd
}

func tradeService(cfg *config.Config, network string) *trading.Service {
	if network == "" {
		network = cfg.Network
	}
	if network == "" {
		network = string(models.NetworkStellarMainnet)
	}
	return trading.NewService(models.Network(network))
}

func strategyTypeFromString(value string) (models.StrategyType, error) {
	switch models.StrategyType(value) {
	case models.StrategyArbitrage, models.StrategyMeanReversion, models.StrategyMomentum,
		models.StrategyGridTrading, models.StrategyDCA, models.StrategyBreakout,
		models.StrategyScalping, models.StrategyBuySell, models.StrategySell:
		return models.StrategyType(value), nil
	default:
		return "", fmt.Errorf("invalid strategy type: %s", value)
	}
}

// ─── trade strategy ─────────────────────────────

func newTradeStrategyCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("strategy", flag.ContinueOnError)
	strategyType := fs.String("type", "", "Strategy type: buysell, sell, arbitrage, mean_reversion, momentum, grid_trading, dca, breakout, scalping")
	name := fs.String("name", "", "Strategy name")
	baseAsset := fs.String("base", "XLM", "Base asset (XLM, CODE, or CODE:ISSUER)")
	quoteAsset := fs.String("quote", "USDC", "Quote asset (XLM, CODE, or CODE:ISSUER)")
	network := fs.String("network", cfg.Network, "Network: stellar-testnet | stellar-mainnet")
	maxPosition := fs.Float64("max-position", 100, "Maximum position size")
	maxDailyLoss := fs.Float64("max-daily-loss", 50, "Maximum daily loss")
	maxDrawdown := fs.Float64("max-drawdown", 20, "Maximum drawdown percentage")
	maxOpenOffers := fs.Int("max-open-offers", 10, "Maximum desired open offers")
	stopLoss := fs.Float64("stop-loss", 2.0, "Stop loss percentage")
	takeProfit := fs.Float64("take-profit", 3.0, "Take profit percentage")
	params := fs.String("params", "", "Strategy parameters as key=value,key=value")

	return &Command{
		Name:  "strategy",
		Short: "Create a new trading strategy",
		Long: "Create and configure an automated trading strategy. Examples:\n\n" +
			"Buy/sell market making:\n" +
			"  mozartpay trade strategy --type buysell --name \"XLM Market\" --base XLM --quote USDC --params \"price_feed=sdex:XLM/USDC/mid,spread_pct=1,levels=2,amount_per_level=1\"\n\n" +
			"Sell ladder:\n" +
			"  mozartpay trade strategy --type sell --name \"Asset Distribution\" --base CODE:ISSUER --quote USDC --params \"price_feed=fixed:1,levels=3,amount_per_level=10,start_offset_pct=0.5\"\n\n" +
			"Grid Trading:\n" +
			"  mozartpay trade strategy --type grid_trading --name \"XLM Grid\" --params \"upper_price=0.12,lower_price=0.10,num_grids=10\"\n\n" +
			"DCA:\n" +
			"  mozartpay trade strategy --type dca --name \"Daily DCA\" --params \"amount_per_order=10,interval_hours=24\"",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ui.Header("Create Trading Strategy")
			if *strategyType == "" || *name == "" {
				return fmt.Errorf("--type and --name are required")
			}
			strategyTypeVal, err := strategyTypeFromString(*strategyType)
			if err != nil {
				return err
			}
			paramsMap := make(map[string]interface{})
			if *params != "" {
				for _, pair := range strings.Split(*params, ",") {
					kv := strings.SplitN(pair, "=", 2)
					if len(kv) != 2 {
						return fmt.Errorf("invalid parameter %q", pair)
					}
					key, val := strings.TrimSpace(kv[0]), strings.TrimSpace(kv[1])
					if f, err := strconv.ParseFloat(val, 64); err == nil {
						paramsMap[key] = f
					} else {
						paramsMap[key] = val
					}
				}
			}
			riskLimits := models.RiskLimits{
				MaxPositionSize:   *maxPosition,
				MaxDailyLoss:      *maxDailyLoss,
				MaxDrawdown:       *maxDrawdown,
				StopLossPercent:   *stopLoss,
				TakeProfitPercent: *takeProfit,
				MaxOpenTrades:     *maxOpenOffers,
			}
			svc := tradeService(cfg, *network)
			defer svc.Close()
			spin := ui.NewSpinner("Creating strategy...")
			spin.Start()
			strategy, err := svc.CreateStrategy(*name, strategyTypeVal, *baseAsset, *quoteAsset, paramsMap, riskLimits)
			if err != nil {
				spin.Stop(false, err.Error())
				return err
			}
			spin.Stop(true, "Strategy created")
			printStrategySummary(strategy)
			fmt.Println()
			ui.Info(fmt.Sprintf("Start it with: mozartpay trade start %s", strategy.ID))
			ui.Info(fmt.Sprintf("Run dry-run once with: mozartpay trade run --once %s", strategy.ID))
			return nil
		},
	}
}

func printStrategySummary(strategy *models.TradingStrategy) {
	ui.SectionLabel("Strategy Details")
	ui.KV("ID", strategy.ID)
	ui.KV("Name", strategy.Name)
	ui.KV("Type", string(strategy.Type))
	ui.KV("Pair", fmt.Sprintf("%s/%s", strategy.BaseAsset, strategy.QuoteAsset))
	ui.KV("Network", string(strategy.Network))
	ui.KV("Status", string(strategy.Status))
	ui.SectionLabel("Risk Limits")
	ui.KV("Max Position", fmt.Sprintf("%.2f", strategy.RiskLimits.MaxPositionSize))
	ui.KV("Max Daily Loss", fmt.Sprintf("%.2f", strategy.RiskLimits.MaxDailyLoss))
	ui.KV("Max Drawdown", fmt.Sprintf("%.1f%%", strategy.RiskLimits.MaxDrawdown))
	ui.KV("Max Open Offers", fmt.Sprintf("%d", strategy.RiskLimits.MaxOpenTrades))
}

// ─── trade list ───────────────────────────────

func newTradeListCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	showAll := fs.Bool("all", false, "Show all strategies including stopped")
	return &Command{
		Name:  "list",
		Short: "List trading strategies",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ui.Header("Trading Strategies")
			svc := tradeService(cfg, "")
			defer svc.Close()
			strategies := svc.GetAllStrategies()
			if len(strategies) == 0 {
				ui.Info("No strategies configured")
				ui.Info("Create one with: mozartpay trade strategy --type <type> --name <name>")
				return nil
			}
			t := ui.NewTable("ID", "Name", "Type", "Pair", "Network", "Status", "Trades", "Profit")
			for _, s := range strategies {
				if !*showAll && s.Status == models.StrategyStopped {
					continue
				}
				shortID := s.ID
				if len(shortID) > 12 {
					shortID = shortID[:12] + "..."
				}
				statusColor := ui.Reset
				switch s.Status {
				case models.StrategyActive:
					statusColor = ui.BrightGreen
				case models.StrategyPaused:
					statusColor = ui.BrightYellow
				case models.StrategyError:
					statusColor = ui.Red
				}
				t.AddRow(shortID, s.Name, string(s.Type), fmt.Sprintf("%s/%s", s.BaseAsset, s.QuoteAsset), string(s.Network), statusColor+string(s.Status)+ui.Reset, fmt.Sprintf("%d", s.TotalTrades), fmt.Sprintf("%.4f", s.TotalProfit))
			}
			t.Print()
			fmt.Println()
			ui.Info("Use 'mozartpay trade status <id>' for detailed strategy info")
			return nil
		},
	}
}

// ─── lifecycle commands ─────────────────────────

func newTradeStartCmd(cfg *config.Config) *Command {
	return &Command{Name: "start", Short: "Start a trading strategy", Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		ui.Header("Start Trading Strategy")
		if err := svc.StartStrategy(args[0]); err != nil {
			ui.Error(err.Error())
			return err
		}
		ui.Success(fmt.Sprintf("Strategy '%s' activated", strategy.Name))
		ui.Info(fmt.Sprintf("Run it with: mozartpay trade run %s", strategy.ID))
		return nil
	}}
}

func newTradePauseCmd(cfg *config.Config) *Command {
	return &Command{Name: "pause", Short: "Pause a trading strategy", Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		if err := svc.PauseStrategy(args[0]); err != nil {
			return err
		}
		ui.Success(fmt.Sprintf("Strategy '%s' paused", strategy.Name))
		return nil
	}}
}

func newTradeStopCmd(cfg *config.Config) *Command {
	return &Command{Name: "stop", Short: "Stop a trading strategy", Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		if err := svc.StopStrategy(args[0]); err != nil {
			ui.Error(err.Error())
			return err
		}
		ui.Success(fmt.Sprintf("Strategy '%s' stopped", strategy.Name))
		ui.Info(fmt.Sprintf("Total trades: %d | Total profit: %.4f", strategy.TotalTrades, strategy.TotalProfit))
		return nil
	}}
}

// ─── trade run ─────────────────────────────────

func newTradeRunCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	live := fs.Bool("live", false, "Submit real SDEX offers (default is dry-run)")
	once := fs.Bool("once", false, "Run one reconciliation iteration and exit")
	return &Command{
		Name:  "run",
		Short: "Run an active strategy in the foreground",
		Long:  "Run a strategy reconciliation loop. Dry-run is the default; pass --live to submit real offers.",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			svc := tradeService(cfg, "")
			defer svc.Close()
			if len(args) == 0 {
				ui.Header("Run Active Trading Strategies")
				ui.Info(fmt.Sprintf("Mode: %s", runMode(*live)))
				return svc.RunActive(ctx, *live)
			}
			strategy, err := svc.GetStrategy(args[0])
			if err != nil {
				return err
			}
			ui.Header(fmt.Sprintf("Run Strategy: %s", strategy.Name))
			ui.KV("Pair", fmt.Sprintf("%s/%s", strategy.BaseAsset, strategy.QuoteAsset))
			ui.KV("Mode", runMode(*live))
			if *once {
				report, err := svc.RunOnce(ctx, strategy, *live)
				if err != nil {
					return err
				}
				printReconcileReport(report)
				return nil
			}
			return svc.RunStrategy(ctx, strategy.ID, *live)
		},
	}
}

func runMode(live bool) string {
	if live {
		return "live"
	}
	return "dry-run"
}

func displayAsset(asset models.AssetRef) string {
	if asset.Canonical() == "native" {
		return "XLM"
	}
	return asset.Canonical()
}

func printReconcileReport(report *trading.ReconcileReport) {
	ui.SectionLabel("Reconciliation")
	ui.KV("Reference", fmt.Sprintf("%.7f (%s)", report.ReferencePrice, report.Feed))
	ui.KV("Desired", fmt.Sprintf("%d", report.Desired))
	ui.KV("Created", fmt.Sprintf("%d", report.Created))
	ui.KV("Updated", fmt.Sprintf("%d", report.Updated))
	ui.KV("Canceled", fmt.Sprintf("%d", report.Canceled))
	ui.KV("Unchanged", fmt.Sprintf("%d", report.Unchanged))
	ui.KV("Orphaned", fmt.Sprintf("%d", report.Orphaned))
	if len(report.TxHashes) > 0 {
		ui.KV("Transaction", strings.Join(report.TxHashes, ","))
	}
	for _, action := range report.Actions {
		ui.Info(fmt.Sprintf("%s %s %.7f @ %.7f offer=%d", strings.ToUpper(action.Kind), action.Side, action.Amount, action.Price, action.OfferID))
	}
	for _, errText := range report.Errors {
		ui.Warn(errText)
	}
}

// ─── offer commands ─────────────────────────────

func newTradeOrderCmd(cfg *config.Config) *Command {
	cmd := &Command{Name: "order", Short: "Create or cancel a manual SDEX order", cfg: cfg}
	cmd.addSub(newTradeOrderCreateCmd(cfg))
	cmd.addSub(newTradeOrderCancelCmd(cfg))
	cmd.Run = func(c *Command, args []string) error {
		c.printHelp()
		return nil
	}
	return cmd
}

func newTradeOrderCreateCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	side := fs.String("side", "", "Order side: buy or sell")
	baseAsset := fs.String("base", "", "Base asset")
	quoteAsset := fs.String("quote", "", "Quote asset")
	amount := fs.Float64("amount", 0, "Base-asset amount")
	price := fs.Float64("price", 0, "Quote-per-base price")
	passive := fs.Bool("passive", false, "Create a passive sell offer")
	offerID := fs.Int64("offer-id", 0, "Existing offer ID to update")
	live := fs.Bool("live", false, "Submit the order (default is dry-run)")
	networkFlag := fs.String("network", cfg.Network, "Network")
	return &Command{Name: "create", Short: "Create one manual SDEX order", Flags: fs, Run: func(c *Command, args []string) error {
		if *side != "buy" && *side != "sell" {
			return fmt.Errorf("--side must be buy or sell")
		}
		if *amount <= 0 || *price <= 0 {
			return fmt.Errorf("--amount and --price must be positive")
		}
		net := models.Network(*networkFlag)
		base, err := market.ParseAsset(*baseAsset, net)
		if err != nil {
			return fmt.Errorf("base asset: %w", err)
		}
		quote, err := market.ParseAsset(*quoteAsset, net)
		if err != nil {
			return fmt.Errorf("quote asset: %w", err)
		}
		spec := market.OfferSpec{OfferID: *offerID, Side: models.OrderSide(*side), Base: base, Quote: quote, Amount: *amount, Price: *price, Passive: *passive}
		ui.Header("Create SDEX Order")
		ui.KV("Side", *side)
		ui.KV("Pair", fmt.Sprintf("%s/%s", displayAsset(base), displayAsset(quote)))
		ui.KV("Amount", fmt.Sprintf("%.7f", spec.Amount))
		ui.KV("Price", fmt.Sprintf("%.7f", spec.Price))
		if spec.OfferID > 0 {
			ui.KV("Offer ID", strconv.FormatInt(spec.OfferID, 10))
		}
		if !*live {
			ui.Warn("Dry-run only; pass --live to submit")
			return nil
		}
		svc := tradeService(cfg, *networkFlag)
		defer svc.Close()
		result, err := svc.SubmitOffer(context.Background(), spec)
		if err != nil {
			return err
		}
		ui.Success(fmt.Sprintf("Order submitted in transaction %s", result.Hash))
		return nil
	}}
}

func newTradeOrderCancelCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	live := fs.Bool("live", false, "Submit cancellation (default is dry-run)")
	networkFlag := fs.String("network", cfg.Network, "Network")
	return &Command{Name: "cancel", Short: "Cancel one live SDEX offer by ID", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("offer ID required")
		}
		offerID, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil || offerID <= 0 {
			return fmt.Errorf("invalid offer ID %q", args[0])
		}
		ui.Header("Cancel SDEX Order")
		ui.KV("Offer ID", strconv.FormatInt(offerID, 10))
		if !*live {
			ui.Warn("Dry-run only; pass --live to submit cancellation")
			return nil
		}
		svc := tradeService(cfg, *networkFlag)
		defer svc.Close()
		result, err := svc.CancelOfferByID(context.Background(), offerID)
		if err != nil {
			return err
		}
		ui.Success(fmt.Sprintf("Cancellation submitted in transaction %s", result.Hash))
		return nil
	}}
}

func newTradeOffersCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("offers", flag.ContinueOnError)
	live := fs.Bool("live", false, "Query live open offers from Horizon")
	return &Command{Name: "offers", Short: "Show strategy-managed or live offers", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		ui.Header(fmt.Sprintf("Offers: %s", strategy.Name))
		if *live {
			offers, err := svc.LiveOffers(context.Background(), strategy)
			if err != nil {
				return err
			}
			if len(offers) == 0 {
				ui.Info("No live offers for this pair")
				return nil
			}
			t := ui.NewTable("Offer ID", "Side", "Base", "Quote", "Amount", "Price")
			for _, offer := range offers {
				t.AddRow(strconv.FormatInt(offer.ID, 10), string(offer.Side), offer.Base.Canonical(), offer.Quote.Canonical(), fmt.Sprintf("%.7f", offer.Amount), fmt.Sprintf("%.7f", offer.Price))
			}
			t.Print()
			return nil
		}
		offers, err := svc.ManagedOffers(strategy.ID)
		if err != nil {
			return err
		}
		if len(offers) == 0 {
			ui.Info("No managed offers recorded")
			return nil
		}
		t := ui.NewTable("Intent", "Offer ID", "Side", "Amount", "Price", "Status", "Updated")
		for _, offer := range offers {
			offerID := "-"
			if offer.OfferID > 0 {
				offerID = strconv.FormatInt(offer.OfferID, 10)
			}
			t.AddRow(offer.IntentID, offerID, string(offer.Side), fmt.Sprintf("%.7f", offer.Amount), fmt.Sprintf("%.7f", offer.Price), offer.Status, offer.UpdatedAt.Format("15:04:05"))
		}
		t.Print()
		return nil
	}}
}

func newTradeCancelCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("cancel", flag.ContinueOnError)
	allPair := fs.Bool("all-pair", false, "Cancel all open offers for the strategy pair, including unmanaged offers")
	return &Command{Name: "cancel", Short: "Cancel strategy offers", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		ui.Header("Cancel Strategy Offers")
		result, err := svc.CancelStrategyOffers(context.Background(), args[0], *allPair)
		if err != nil {
			return err
		}
		if result == nil {
			ui.Info("No matching live offers to cancel")
			return nil
		}
		ui.Success(fmt.Sprintf("Submitted cancellation transaction %s", result.Hash))
		return nil
	}}
}

func newTradeKillCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("kill", flag.ContinueOnError)
	cancel := fs.Bool("cancel", true, "Cancel all managed offers after enabling the kill switch")
	clear := fs.Bool("clear", false, "Clear the kill switch instead of enabling it")
	return &Command{Name: "kill", Short: "Enable or clear a strategy kill switch", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		if *clear {
			if err := svc.ClearKillSwitch(args[0]); err != nil {
				return err
			}
			ui.Success("Kill switch cleared")
			return nil
		}
		ui.Header("Kill Switch")
		if err := svc.KillSwitch(args[0]); err != nil {
			return err
		}
		ui.Warn("Strategy paused and kill switch enabled")
		if *cancel {
			result, err := svc.CancelStrategyOffers(context.Background(), args[0], false)
			if err != nil {
				return err
			}
			if result != nil {
				ui.Success(fmt.Sprintf("Canceled offers in transaction %s", result.Hash))
			} else {
				ui.Info("No managed live offers to cancel")
			}
		}
		return nil
	}}
}

// ─── trade backtest ─────────────────────────────

func newTradeBacktestCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	startFlag := fs.String("start", "", "Start time in RFC3339 format")
	endFlag := fs.String("end", "", "End time in RFC3339 format")
	days := fs.Int("days", 7, "Lookback days when --start is omitted")
	resolution := fs.Duration("resolution", time.Hour, "Candle resolution (for example 5m, 1h, 1d)")
	initialBase := fs.Float64("initial-base", 100, "Initial base-asset inventory")
	initialQuote := fs.Float64("initial-quote", 100, "Initial quote-asset inventory")
	return &Command{Name: "backtest", Short: "Replay a strategy against historical SDEX candles", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		end := time.Now().UTC()
		if *endFlag != "" {
			parsed, err := time.Parse(time.RFC3339, *endFlag)
			if err != nil {
				return fmt.Errorf("invalid --end: %w", err)
			}
			end = parsed
		}
		start := end.Add(-time.Duration(*days) * 24 * time.Hour)
		if *startFlag != "" {
			parsed, err := time.Parse(time.RFC3339, *startFlag)
			if err != nil {
				return fmt.Errorf("invalid --start: %w", err)
			}
			start = parsed
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		ui.Header(fmt.Sprintf("Backtest: %s", strategy.Name))
		ui.Info(fmt.Sprintf("Fetching %s/%s candles from %s to %s", strategy.BaseAsset, strategy.QuoteAsset, start.Format(time.RFC3339), end.Format(time.RFC3339)))
		result, err := svc.Backtest(context.Background(), strategy.ID, start, end, *resolution, *initialBase, *initialQuote)
		if err != nil {
			return err
		}
		ui.SectionLabel("Result")
		ui.KV("Candles", fmt.Sprintf("%d", result.Iterations))
		ui.KV("Desired Orders", fmt.Sprintf("%d", result.DesiredOrders))
		ui.KV("Fills", fmt.Sprintf("%d buys / %d sells", result.Buys, result.Sells))
		ui.KV("Ending Value", fmt.Sprintf("%.7f %s", result.EndingValue, strategy.QuoteAsset))
		pnlColor := ui.BrightGreen
		if result.RealizedPnL < 0 {
			pnlColor = ui.Red
		}
		ui.KVColor("P&L", fmt.Sprintf("%.7f %s", result.RealizedPnL, strategy.QuoteAsset), pnlColor)
		return nil
	}}
}

// ─── status/performance/history ─────────────────

func newTradeStatusCmd(cfg *config.Config) *Command {
	return &Command{Name: "status", Short: "Show detailed strategy status", Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		ui.Header(fmt.Sprintf("Strategy: %s", strategy.Name))
		ui.SectionLabel("General")
		ui.KV("ID", strategy.ID)
		ui.KV("Type", string(strategy.Type))
		ui.KV("Pair", fmt.Sprintf("%s/%s", strategy.BaseAsset, strategy.QuoteAsset))
		ui.KV("Network", string(strategy.Network))
		statusColor := ui.Reset
		switch strategy.Status {
		case models.StrategyActive:
			statusColor = ui.BrightGreen
		case models.StrategyPaused:
			statusColor = ui.BrightYellow
		case models.StrategyError:
			statusColor = ui.Red
		}
		ui.KVColor("Status", string(strategy.Status), statusColor)
		if strategy.LastRunAt != nil {
			ui.KV("Last Run", strategy.LastRunAt.Format("2006-01-02 15:04:05"))
		}
		ui.SectionLabel("Performance")
		ui.KV("Total Trades", fmt.Sprintf("%d", strategy.TotalTrades))
		ui.KV("Total Profit", fmt.Sprintf("%.4f", strategy.TotalProfit))
		if perf, err := svc.GetPerformance(strategy.ID); err == nil && perf.TotalTrades > 0 {
			ui.KV("Win Rate", fmt.Sprintf("%.1f%%", perf.WinRate))
			ui.KV("Profit Factor", fmt.Sprintf("%.2f", perf.ProfitFactor))
		}
		ui.SectionLabel("Risk Limits")
		ui.KV("Max Position", fmt.Sprintf("%.2f", strategy.RiskLimits.MaxPositionSize))
		ui.KV("Max Daily Loss", fmt.Sprintf("%.2f", strategy.RiskLimits.MaxDailyLoss))
		ui.KV("Max Drawdown", fmt.Sprintf("%.1f%%", strategy.RiskLimits.MaxDrawdown))
		ui.KV("Stop Loss", fmt.Sprintf("%.1f%%", strategy.RiskLimits.StopLossPercent))
		ui.KV("Take Profit", fmt.Sprintf("%.1f%%", strategy.RiskLimits.TakeProfitPercent))
		return nil
	}}
}

func newTradePerformanceCmd(cfg *config.Config) *Command {
	return &Command{Name: "performance", Short: "Show strategy performance metrics", Run: func(c *Command, args []string) error {
		svc := tradeService(cfg, "")
		defer svc.Close()
		if len(args) < 1 {
			ui.Header("Trading Performance Summary")
			strategies := svc.GetAllStrategies()
			if len(strategies) == 0 {
				ui.Info("No strategies to show performance for")
				return nil
			}
			t := ui.NewTable("Name", "Type", "Trades", "Wins", "Losses", "Win Rate", "Total Return")
			for _, s := range strategies {
				perf, err := svc.GetPerformance(s.ID)
				if err != nil {
					continue
				}
				t.AddRow(s.Name, string(s.Type), fmt.Sprintf("%d", perf.TotalTrades), fmt.Sprintf("%d", perf.WinningTrades), fmt.Sprintf("%d", perf.LosingTrades), fmt.Sprintf("%.1f%%", perf.WinRate), fmt.Sprintf("%.4f", perf.TotalReturn))
			}
			t.Print()
			return nil
		}
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		perf, err := svc.GetPerformance(strategy.ID)
		if err != nil {
			return err
		}
		ui.Header(fmt.Sprintf("Performance: %s", strategy.Name))
		ui.SectionLabel("Trade Statistics")
		ui.KV("Total Trades", fmt.Sprintf("%d", perf.TotalTrades))
		ui.KV("Winning Trades", fmt.Sprintf("%d", perf.WinningTrades))
		ui.KV("Losing Trades", fmt.Sprintf("%d", perf.LosingTrades))
		ui.KVColor("Win Rate", fmt.Sprintf("%.1f%%", perf.WinRate), ui.BrightGreen)
		ui.SectionLabel("Returns")
		ui.KV("Average Profit", fmt.Sprintf("%.4f", perf.AvgProfit))
		ui.KV("Average Loss", fmt.Sprintf("%.4f", perf.AvgLoss))
		ui.KV("Profit Factor", fmt.Sprintf("%.2f", perf.ProfitFactor))
		ui.KVColor("Total Return", fmt.Sprintf("%.4f", perf.TotalReturn), ui.BrightGreen)
		ui.SectionLabel("Risk Metrics")
		ui.KV("Sharpe Ratio", fmt.Sprintf("%.2f", perf.SharpeRatio))
		ui.KV("Max Drawdown", fmt.Sprintf("%.2f%%", perf.MaxDrawdown))
		return nil
	}}
}

func newTradeHistoryCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "Number of executions to show")
	return &Command{Name: "history", Short: "Show strategy execution history", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		executions := svc.GetExecutions(strategy.ID, *limit)
		ui.Header(fmt.Sprintf("Execution History: %s", strategy.Name))
		if len(executions) == 0 {
			ui.Info("No executions yet")
			return nil
		}
		t := ui.NewTable("Time", "Action", "Amount", "Price", "Value", "P&L", "Status")
		for _, e := range executions {
			plStr, plColor := "-", ui.Reset
			if e.ProfitLoss != 0 {
				plStr = fmt.Sprintf("%.4f", e.ProfitLoss)
				if e.ProfitLoss > 0 {
					plColor = ui.BrightGreen
				} else {
					plColor = ui.Red
				}
			}
			t.AddRow(e.Timestamp.Format("2006-01-02 15:04"), string(e.Action), fmt.Sprintf("%.2f", e.Amount), fmt.Sprintf("%.6f", e.Price), fmt.Sprintf("%.4f", e.Value), plColor+plStr+ui.Reset, string(e.Status))
		}
		t.Print()
		return nil
	}}
}

func newTradeFillsCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("fills", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "Number of fills to show")
	return &Command{Name: "fills", Short: "Show observed SDEX fills", Flags: fs, Run: func(c *Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("strategy ID required")
		}
		svc := tradeService(cfg, "")
		defer svc.Close()
		strategy, err := svc.GetStrategy(args[0])
		if err != nil {
			return err
		}
		fills := svc.GetFills(strategy.ID, *limit)
		ui.Header(fmt.Sprintf("Fills: %s", strategy.Name))
		if len(fills) == 0 {
			ui.Info("No fills recorded")
			return nil
		}
		t := ui.NewTable("Time", "Intent", "Offer", "Trade", "Amount", "Price", "Counter")
		for _, fill := range fills {
			offerID := "-"
			if fill.OfferID > 0 {
				offerID = strconv.FormatInt(fill.OfferID, 10)
			}
			tradeID := fill.TradeID
			if len(tradeID) > 12 {
				tradeID = tradeID[:12]
			}
			t.AddRow(fill.ExecutedAt.Format("2006-01-02 15:04"), fill.IntentID, offerID, tradeID,
				fmt.Sprintf("%.7f", fill.Amount), fmt.Sprintf("%.7f", fill.Price), fmt.Sprintf("%.7f", fill.Counter))
		}
		t.Print()
		return nil
	}}
}

func newTradeDashboardCmd(cfg *config.Config) *Command {
	fs := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "Dashboard listen host (use 127.0.0.1 for local access)")
	port := fs.Int("port", 8080, "Dashboard listen port")
	noOpen := fs.Bool("no-open", false, "Do not open the dashboard in a browser")
	token := fs.String("token", os.Getenv("MOZARTPAY_DASHBOARD_TOKEN"), "Fixed dashboard auth token reused across restarts (env MOZARTPAY_DASHBOARD_TOKEN)")
	return &Command{
		Name:  "dashboard",
		Short: "Open the HTML trading dashboard",
		Long:  "Serve a local HTML dashboard for trading strategies, offers, fills, runtime state, and operational controls.",
		Flags: fs,
		Run: func(c *Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument: %s", args[0])
			}
			server, err := dashboard.NewServerWithToken(cfg, *token)
			if err != nil {
				return err
			}
			listener, err := server.Listen(*host, *port)
			if err != nil {
				return fmt.Errorf("listen on %s:%d: %w", *host, *port, err)
			}
			url := server.BrowserURL(listener.Addr().String())

			ui.Header("Trading Dashboard")
			ui.KV("Listen", listener.Addr().String())
			if server.FixedToken() {
				ui.KV("Mode", "fixed token (reused across restarts)")
			} else {
				ui.KV("Mode", "local token-protected session")
			}
			ui.Link(url, "Open dashboard")
			if *host != "127.0.0.1" && *host != "localhost" && *host != "::1" {
				ui.Warn("Dashboard is bound to a non-loopback interface")
			}
			if !*noOpen {
				if err := dashboard.OpenBrowser(url); err != nil {
					ui.Warn(fmt.Sprintf("Could not open browser: %v", err))
				}
			}
			ui.Info("Press Ctrl+C to stop the dashboard")

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return server.Serve(ctx, listener)
		},
	}
}
