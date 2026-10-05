package trading

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
)

// Backtest runs a deterministic candle replay for resting-order strategies.
func (s *Service) Backtest(ctx context.Context, strategyID string, start, end time.Time, resolution time.Duration, initialBase, initialQuote float64) (*models.BacktestResult, error) {
	strategy, err := s.GetStrategy(strategyID)
	if err != nil {
		return nil, err
	}
	if strategy.Type != models.StrategyBuySell && strategy.Type != models.StrategySell {
		return nil, fmt.Errorf("backtests currently support buysell and sell strategies")
	}
	if end.Before(start) {
		return nil, fmt.Errorf("end time must be after start time")
	}
	if resolution < time.Minute {
		return nil, fmt.Errorf("resolution must be at least one minute")
	}
	base, err := market.ParseAsset(strategy.BaseAsset, strategy.Network)
	if err != nil {
		return nil, err
	}
	quote, err := market.ParseAsset(strategy.QuoteAsset, strategy.Network)
	if err != nil {
		return nil, err
	}
	candles, err := s.market.TradeAggregations(ctx, base, quote, start, end, resolution, 200)
	if err != nil {
		return nil, err
	}
	if len(candles) == 0 {
		return nil, fmt.Errorf("no historical trades found for %s/%s", strategy.BaseAsset, strategy.QuoteAsset)
	}

	result := &models.BacktestResult{
		StrategyID:   strategy.ID,
		StrategyName: strategy.Name,
		BaseAsset:    strategy.BaseAsset,
		QuoteAsset:   strategy.QuoteAsset,
		Network:      strategy.Network,
		StartedAt:    candles[0].Timestamp,
		EndedAt:      candles[len(candles)-1].Timestamp,
		CreatedAt:    time.Now().UTC(),
	}

	cash := initialQuote
	inventory := initialBase
	initialValue := initialQuote + initialBase*candles[0].Open
	for _, candle := range candles {
		result.Iterations++
		intents := historicalIntents(strategy, candle.Close)
		result.DesiredOrders += len(intents)
		for _, intent := range intents {
			switch intent.Side {
			case models.OrderSideBuy:
				if candle.Low <= intent.Price {
					cost := intent.Amount * intent.Price
					if cash+1e-7 >= cost {
						cash -= cost
						inventory += intent.Amount
						result.Buys++
						result.Trades++
					}
				}
			case models.OrderSideSell:
				if candle.High >= intent.Price && inventory+1e-7 >= intent.Amount {
					inventory -= intent.Amount
					cash += intent.Amount * intent.Price
					result.Sells++
					result.Trades++
				}
			}
		}
	}
	result.EndingValue = cash + inventory*candles[len(candles)-1].Close
	result.RealizedPnL = result.EndingValue - initialValue
	if math.IsNaN(result.RealizedPnL) || math.IsInf(result.RealizedPnL, 0) {
		return nil, fmt.Errorf("invalid backtest result")
	}
	if s.store != nil {
		_ = s.store.SaveBacktest(result)
	}
	return result, nil
}

func historicalIntents(strategy *models.TradingStrategy, center float64) []models.OrderIntent {
	amount := firstParam(strategy.Parameters, []string{"amount_per_level", "amount"}, 0)
	levels := paramInt(strategy.Parameters, "levels", 1)
	if amount <= 0 || levels <= 0 || center <= 0 {
		return nil
	}
	out := make([]models.OrderIntent, 0, levels*2)
	if strategy.Type == models.StrategySell {
		startOffset := firstParam(strategy.Parameters, []string{"start_offset_pct", "spread_pct"}, 0.5) / 100
		spacing := paramFloat(strategy.Parameters, "level_spacing_pct", 0.5) / 100
		for level := 0; level < levels; level++ {
			price := center * (1 + startOffset + float64(level)*spacing)
			minPrice := firstParam(strategy.Parameters, []string{"min_price", "price_floor"}, 0)
			maxPrice := firstParam(strategy.Parameters, []string{"max_price", "price_limit"}, 0)
			if minPrice > 0 && price < minPrice {
				price = minPrice
			}
			if maxPrice > 0 && price > maxPrice {
				continue
			}
			out = append(out, models.OrderIntent{IntentID: fmt.Sprintf("sell-%d", level), Side: models.OrderSideSell, Price: price, Amount: amount})
		}
		return out
	}

	spread := firstParam(strategy.Parameters, []string{"spread_pct", "spread"}, 1.0) / 200
	spacing := paramFloat(strategy.Parameters, "level_spacing_pct", 0.5) / 100
	offset := paramFloat(strategy.Parameters, "rate_offset_pct", 0) / 100
	center *= 1 + offset
	for level := 0; level < levels; level++ {
		delta := spread + float64(level)*spacing
		bid := center * (1 - delta)
		ask := center * (1 + delta)
		if bid > 0 {
			out = append(out, models.OrderIntent{IntentID: fmt.Sprintf("buy-%d", level), Side: models.OrderSideBuy, Price: bid, Amount: amount})
		}
		if ask > 0 {
			out = append(out, models.OrderIntent{IntentID: fmt.Sprintf("sell-%d", level), Side: models.OrderSideSell, Price: ask, Amount: amount})
		}
	}
	return out
}
