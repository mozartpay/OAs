package trading

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
)

// Bot produces desired resting-order intents for a strategy.
type Bot interface {
	GenerateOrders(ctx context.Context) ([]models.OrderIntent, *PriceQuote, error)
	Interval() time.Duration
}

// NewBot constructs a strategy implementation.
func (s *Service) NewBot(strategy *models.TradingStrategy) (Bot, error) {
	switch strategy.Type {
	case models.StrategyBuySell:
		return newBuySellBot(s, strategy)
	case models.StrategySell:
		return newSellBot(s, strategy)
	default:
		return nil, fmt.Errorf("strategy type %s is signal-based; use --dry-run for compatibility", strategy.Type)
	}
}

type botBase struct {
	service  *Service
	strategy *models.TradingStrategy
	feedSpec string
	interval time.Duration
	base     models.AssetRef
	quote    models.AssetRef
}

func newBotBase(s *Service, strategy *models.TradingStrategy, defaultFeed string) (*botBase, error) {
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		return nil, fmt.Errorf("base asset: %w", err)
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		return nil, fmt.Errorf("quote asset: %w", err)
	}
	feed := paramString(strategy.Parameters, "price_feed", "")
	if feed == "" {
		feed = defaultFeed
	}
	interval := time.Duration(paramFloat(strategy.Parameters, "interval_seconds", 30)) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	return &botBase{service: s, strategy: strategy, feedSpec: feed, interval: interval, base: base, quote: quote}, nil
}

func (b *botBase) referencePrice(ctx context.Context) (*PriceQuote, error) {
	feed, err := NewPriceFeed(b.feedSpec, b.strategy.Network, b.service.market, b.service.pools)
	if err != nil {
		return nil, err
	}
	quote, err := feed.Price(ctx)
	if err != nil {
		return nil, err
	}
	if quote.Price <= 0 || math.IsNaN(quote.Price) || math.IsInf(quote.Price, 0) {
		return nil, fmt.Errorf("feed %s returned invalid price", feed.Name())
	}
	return quote, nil
}

func (b *botBase) intent(side models.OrderSide, level int, price, amount float64, passive bool, reason string) models.OrderIntent {
	return models.OrderIntent{
		IntentID:   fmt.Sprintf("%s-%d", side, level),
		StrategyID: b.strategy.ID,
		Side:       side,
		BaseAsset:  b.base,
		QuoteAsset: b.quote,
		Price:      round7(price),
		Amount:     round7(amount),
		Passive:    passive,
		Reason:     reason,
	}
}

// BuySellBot maintains bid and ask levels around a reference price.
type BuySellBot struct {
	*botBase
	spreadPct       float64
	levelSpacingPct float64
	levels          int
	amount          float64
	rateOffsetPct   float64
	makerOnly       bool
}

func newBuySellBot(s *Service, strategy *models.TradingStrategy) (*BuySellBot, error) {
	base, err := newBotBase(s, strategy, fmt.Sprintf("sdex:%s/%s/mid", strategy.BaseAsset, strategy.QuoteAsset))
	if err != nil {
		return nil, err
	}
	bot := &BuySellBot{
		botBase:         base,
		spreadPct:       firstParam(strategy.Parameters, []string{"spread_pct", "spread"}, 1.0),
		levelSpacingPct: paramFloat(strategy.Parameters, "level_spacing_pct", 0.5),
		levels:          paramInt(strategy.Parameters, "levels", 1),
		amount:          firstParam(strategy.Parameters, []string{"amount_per_level", "amount"}, 0),
		rateOffsetPct:   paramFloat(strategy.Parameters, "rate_offset_pct", 0),
		makerOnly:       paramBool(strategy.Parameters, "maker_only", false),
	}
	if bot.levels < 1 || bot.levels > 20 {
		return nil, fmt.Errorf("levels must be between 1 and 20")
	}
	if bot.spreadPct <= 0 || bot.spreadPct >= 100 {
		return nil, fmt.Errorf("spread_pct must be between 0 and 100")
	}
	if bot.levelSpacingPct < 0 || bot.levelSpacingPct >= 50 {
		return nil, fmt.Errorf("level_spacing_pct must be between 0 and 50")
	}
	if bot.amount <= 0 {
		return nil, fmt.Errorf("amount_per_level must be positive")
	}
	return bot, nil
}

func (b *BuySellBot) Interval() time.Duration { return b.interval }

func (b *BuySellBot) GenerateOrders(ctx context.Context) ([]models.OrderIntent, *PriceQuote, error) {
	quote, err := b.referencePrice(ctx)
	if err != nil {
		return nil, nil, err
	}
	center := quote.Price * (1 + b.rateOffsetPct/100)
	halfSpread := b.spreadPct / 200
	intents := make([]models.OrderIntent, 0, b.levels*2)
	for level := 0; level < b.levels; level++ {
		offset := halfSpread + float64(level)*b.levelSpacingPct/100
		bid := center * (1 - offset)
		ask := center * (1 + offset)
		if bid <= 0 || ask <= 0 {
			return nil, quote, fmt.Errorf("calculated non-positive order price")
		}
		intents = append(intents,
			b.intent(models.OrderSideBuy, level, bid, b.amount, false, fmt.Sprintf("bid level %d around %.7f", level, center)),
			b.intent(models.OrderSideSell, level, ask, b.amount, b.makerOnly, fmt.Sprintf("ask level %d around %.7f", level, center)),
		)
	}
	return intents, quote, nil
}

// SellBot maintains a one-sided sell ladder.
type SellBot struct {
	*botBase
	levels          int
	amount          float64
	startOffsetPct  float64
	levelSpacingPct float64
	minPrice        float64
	maxPrice        float64
	makerOnly       bool
}

func newSellBot(s *Service, strategy *models.TradingStrategy) (*SellBot, error) {
	base, err := newBotBase(s, strategy, fmt.Sprintf("sdex:%s/%s/ask", strategy.BaseAsset, strategy.QuoteAsset))
	if err != nil {
		return nil, err
	}
	bot := &SellBot{
		botBase:         base,
		levels:          paramInt(strategy.Parameters, "levels", 1),
		amount:          firstParam(strategy.Parameters, []string{"amount_per_level", "amount"}, 0),
		startOffsetPct:  firstParam(strategy.Parameters, []string{"start_offset_pct", "spread_pct"}, 0.5),
		levelSpacingPct: paramFloat(strategy.Parameters, "level_spacing_pct", 0.5),
		minPrice:        firstParam(strategy.Parameters, []string{"min_price", "price_floor"}, 0),
		maxPrice:        firstParam(strategy.Parameters, []string{"max_price", "price_limit"}, 0),
		makerOnly:       paramBool(strategy.Parameters, "maker_only", false),
	}
	if bot.levels < 1 || bot.levels > 20 {
		return nil, fmt.Errorf("levels must be between 1 and 20")
	}
	if bot.amount <= 0 {
		return nil, fmt.Errorf("amount_per_level must be positive")
	}
	if bot.startOffsetPct < 0 || bot.levelSpacingPct < 0 {
		return nil, fmt.Errorf("price offsets cannot be negative")
	}
	if bot.minPrice > 0 && bot.maxPrice > 0 && bot.minPrice > bot.maxPrice {
		return nil, fmt.Errorf("min_price cannot exceed max_price")
	}
	return bot, nil
}

func (b *SellBot) Interval() time.Duration { return b.interval }

func (b *SellBot) GenerateOrders(ctx context.Context) ([]models.OrderIntent, *PriceQuote, error) {
	quote, err := b.referencePrice(ctx)
	if err != nil {
		return nil, nil, err
	}
	intents := make([]models.OrderIntent, 0, b.levels)
	for level := 0; level < b.levels; level++ {
		price := quote.Price * (1 + (b.startOffsetPct+float64(level)*b.levelSpacingPct)/100)
		if b.minPrice > 0 && price < b.minPrice {
			price = b.minPrice
		}
		if b.maxPrice > 0 && price > b.maxPrice {
			continue
		}
		if price <= 0 {
			return nil, quote, fmt.Errorf("calculated non-positive sell price")
		}
		intents = append(intents, b.intent(models.OrderSideSell, level, price, b.amount, b.makerOnly, fmt.Sprintf("sell level %d around %.7f", level, quote.Price)))
	}
	return intents, quote, nil
}

func paramFloat(params map[string]interface{}, key string, fallback float64) float64 {
	if params == nil {
		return fallback
	}
	if value, ok := params[key]; ok {
		switch v := value.(type) {
		case float64:
			return v
		case float32:
			return float64(v)
		case int:
			return float64(v)
		case int64:
			return float64(v)
		case string:
			if parsed, err := strconvParseFloat(v); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func firstParam(params map[string]interface{}, keys []string, fallback float64) float64 {
	for _, key := range keys {
		if _, ok := params[key]; ok {
			return paramFloat(params, key, fallback)
		}
	}
	return fallback
}

func paramInt(params map[string]interface{}, key string, fallback int) int {
	return int(paramFloat(params, key, float64(fallback)))
}

func paramString(params map[string]interface{}, key, fallback string) string {
	if params == nil {
		return fallback
	}
	if value, ok := params[key]; ok {
		if s, ok := value.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return fallback
}

func paramBool(params map[string]interface{}, key string, fallback bool) bool {
	if params == nil {
		return fallback
	}
	if value, ok := params[key]; ok {
		switch v := value.(type) {
		case bool:
			return v
		case string:
			return strings.EqualFold(v, "true") || v == "1" || strings.EqualFold(v, "yes")
		case float64:
			return v != 0
		}
	}
	return fallback
}

func strconvParseFloat(value string) (float64, error) {
	var parsed float64
	_, err := fmt.Sscanf(value, "%f", &parsed)
	return parsed, err
}

func round7(value float64) float64 {
	return math.Round(value*1e7) / 1e7
}
