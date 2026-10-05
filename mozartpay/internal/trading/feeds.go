package trading

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/config"
	"github.com/ogtechnologies/mozartpay/internal/market"
	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/pool"
)

// PriceQuote is a normalized price-feed response.
type PriceQuote struct {
	Source    string          `json:"source"`
	Price     float64         `json:"price"`
	Base      models.AssetRef `json:"base,omitempty"`
	Quote     models.AssetRef `json:"quote,omitempty"`
	FetchedAt time.Time       `json:"fetchedAt"`
}

// PriceFeed resolves a reference price.
type PriceFeed interface {
	Name() string
	Price(ctx context.Context) (*PriceQuote, error)
}

type feedFactory struct {
	network models.Network
	market  *market.Client
	pools   *pool.Service
	http    *http.Client
}

// NewPriceFeed builds a feed from a URI-like spec:
// fixed:0.1
// sdex:XLM/USDC/mid
// sdex:USDC:issuer/XLM/last
// pool:<pool-id>/a-to-b
// coingecko:stellar/usd
// frankfurter:EUR/USD
// rest:https://example.com/price/path.to.value
// function:max(feedA,feedB), function:invert(feed), function:weighted(feedA:0.7,feedB:0.3)
func NewPriceFeed(spec string, network models.Network, marketClient *market.Client, poolSvc *pool.Service) (PriceFeed, error) {
	factory := &feedFactory{
		network: network,
		market:  marketClient,
		pools:   poolSvc,
		http:    config.NewHTTPClient(),
	}
	return factory.build(strings.TrimSpace(spec), 0)
}

func (f *feedFactory) build(spec string, depth int) (PriceFeed, error) {
	if depth > 8 {
		return nil, fmt.Errorf("price feed nesting too deep")
	}
	if spec == "" {
		return nil, fmt.Errorf("price feed cannot be empty")
	}
	if f.market == nil {
		f.market = market.NewClient(f.network)
	}
	if f.pools == nil {
		f.pools = pool.NewService(f.network)
	}

	switch {
	case strings.HasPrefix(spec, "fixed:"):
		value, err := strconv.ParseFloat(strings.TrimPrefix(spec, "fixed:"), 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("invalid fixed price %q", spec)
		}
		return &fixedFeed{spec: spec, value: value}, nil

	case strings.HasPrefix(spec, "sdex:"):
		parts := strings.Split(strings.TrimPrefix(spec, "sdex:"), "/")
		mode := ""
		if len(parts) == 3 {
			mode = parts[2]
		} else if len(parts) == 2 {
			idx := strings.LastIndex(parts[1], ":")
			if idx <= 0 {
				return nil, fmt.Errorf("invalid SDEX feed %q", spec)
			}
			mode = parts[1][idx+1:]
			parts[1] = parts[1][:idx]
		} else {
			return nil, fmt.Errorf("invalid SDEX feed %q; expected sdex:BASE/QUOTE/mid|bid|ask|last", spec)
		}
		baseAsset, err := market.ParseAsset(parts[0], f.network)
		if err != nil {
			return nil, err
		}
		quoteAsset, err := market.ParseAsset(parts[1], f.network)
		if err != nil {
			return nil, err
		}
		mode = strings.ToLower(mode)
		if mode != "mid" && mode != "bid" && mode != "ask" && mode != "last" {
			return nil, fmt.Errorf("invalid SDEX mode %q", mode)
		}
		return &sdexFeed{spec: spec, market: f.market, base: baseAsset, quote: quoteAsset, mode: mode}, nil

	case strings.HasPrefix(spec, "pool:"):
		parts := strings.Split(strings.TrimPrefix(spec, "pool:"), "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid pool feed %q; expected pool:<id>/a-to-b|b-to-a", spec)
		}
		direction := strings.ToLower(parts[1])
		if direction != "a-to-b" && direction != "b-to-a" {
			return nil, fmt.Errorf("invalid pool direction %q", direction)
		}
		return &poolFeed{spec: spec, pools: f.pools, poolID: parts[0], direction: direction}, nil

	case strings.HasPrefix(spec, "coingecko:"):
		parts := strings.Split(strings.TrimPrefix(spec, "coingecko:"), "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid CoinGecko feed %q; expected coingecko:<asset>/<currency>", spec)
		}
		return &coinGeckoFeed{spec: spec, http: f.http, asset: coinGeckoID(parts[0]), currency: strings.ToLower(parts[1])}, nil

	case strings.HasPrefix(spec, "frankfurter:"):
		parts := strings.Split(strings.TrimPrefix(spec, "frankfurter:"), "/")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid Frankfurter feed %q; expected frankfurter:<base>/<quote>", spec)
		}
		return &frankfurterFeed{spec: spec, http: f.http, base: strings.ToUpper(parts[0]), quote: strings.ToUpper(parts[1])}, nil

	case strings.HasPrefix(spec, "rest:"):
		body := strings.TrimPrefix(spec, "rest:")
		idx := strings.LastIndex(body, "/")
		if idx <= 0 || idx == len(body)-1 {
			return nil, fmt.Errorf("invalid REST feed %q; expected rest:<url>/<json.path>", spec)
		}
		rawURL, path := body[:idx], body[idx+1:]
		if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
			return nil, fmt.Errorf("REST feed URL must use http or https")
		}
		return &restFeed{spec: spec, http: f.http, rawURL: rawURL, path: path}, nil

	case strings.HasPrefix(spec, "function:"):
		return f.buildFunction(strings.TrimPrefix(spec, "function:"), depth)
	}

	return nil, fmt.Errorf("unknown price feed %q", spec)
}

func (f *feedFactory) buildFunction(spec string, depth int) (PriceFeed, error) {
	open := strings.Index(spec, "(")
	if open <= 0 || !strings.HasSuffix(spec, ")") {
		return nil, fmt.Errorf("invalid feed function %q", spec)
	}
	name := strings.ToLower(strings.TrimSpace(spec[:open]))
	args := splitTopLevel(spec[open+1 : len(spec)-1])
	if len(args) == 0 {
		return nil, fmt.Errorf("feed function %s requires arguments", name)
	}

	switch name {
	case "invert":
		if len(args) != 1 {
			return nil, fmt.Errorf("invert requires one feed")
		}
		feed, err := f.build(args[0], depth+1)
		if err != nil {
			return nil, err
		}
		return &invertFeed{spec: "function:invert(" + args[0] + ")", feed: feed}, nil
	case "max":
		feeds := make([]PriceFeed, 0, len(args))
		for _, arg := range args {
			feed, err := f.build(arg, depth+1)
			if err != nil {
				return nil, err
			}
			feeds = append(feeds, feed)
		}
		return &maxFeed{spec: "function:max(" + strings.Join(args, ",") + ")", feeds: feeds}, nil
	case "weighted":
		inputs := make([]weightedInput, 0, len(args))
		weightSum := 0.0
		for _, arg := range args {
			idx := strings.LastIndex(arg, ":")
			if idx <= 0 {
				return nil, fmt.Errorf("weighted argument %q requires :weight", arg)
			}
			weight, err := strconv.ParseFloat(arg[idx+1:], 64)
			if err != nil || weight <= 0 {
				return nil, fmt.Errorf("invalid weighted feed weight %q", arg)
			}
			feed, err := f.build(arg[:idx], depth+1)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, weightedInput{feed: feed, weight: weight})
			weightSum += weight
		}
		if weightSum <= 0 {
			return nil, fmt.Errorf("weighted feed weights must be positive")
		}
		return &weightedFeed{spec: "function:weighted(" + strings.Join(args, ",") + ")", inputs: inputs, weightSum: weightSum}, nil
	default:
		return nil, fmt.Errorf("unsupported feed function %q", name)
	}
}

func splitTopLevel(input string) []string {
	var out []string
	depth := 0
	start := 0
	for i, r := range input {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(input[start:i]))
				start = i + 1
			}
		}
	}
	if tail := strings.TrimSpace(input[start:]); tail != "" {
		out = append(out, tail)
	}
	return out
}

type fixedFeed struct {
	spec  string
	value float64
}

func (f *fixedFeed) Name() string { return f.spec }
func (f *fixedFeed) Price(ctx context.Context) (*PriceQuote, error) {
	return &PriceQuote{Source: f.spec, Price: f.value, FetchedAt: time.Now().UTC()}, nil
}

type sdexFeed struct {
	spec   string
	market *market.Client
	base   models.AssetRef
	quote  models.AssetRef
	mode   string
}

func (f *sdexFeed) Name() string { return f.spec }
func (f *sdexFeed) Price(ctx context.Context) (*PriceQuote, error) {
	var value float64
	if f.mode == "last" {
		var err error
		value, err = f.market.LatestTradePrice(ctx, f.base, f.quote)
		if err != nil {
			return nil, err
		}
	} else {
		book, err := f.market.OrderBook(ctx, f.base, f.quote, 20)
		if err != nil {
			return nil, err
		}
		switch f.mode {
		case "bid":
			if len(book.Bids) == 0 {
				return nil, fmt.Errorf("no bids for %s/%s", market.DisplayCode(f.base), market.DisplayCode(f.quote))
			}
			value = book.Bids[0].Price
		case "ask":
			if len(book.Asks) == 0 {
				return nil, fmt.Errorf("no asks for %s/%s", market.DisplayCode(f.base), market.DisplayCode(f.quote))
			}
			value = book.Asks[0].Price
		default:
			mid, ok := book.Mid()
			if !ok {
				return nil, fmt.Errorf("cannot calculate midpoint for %s/%s", market.DisplayCode(f.base), market.DisplayCode(f.quote))
			}
			value = mid
		}
	}
	return &PriceQuote{Source: f.spec, Price: value, Base: f.base, Quote: f.quote, FetchedAt: time.Now().UTC()}, nil
}

type poolFeed struct {
	spec      string
	pools     *pool.Service
	poolID    string
	direction string
}

func (f *poolFeed) Name() string { return f.spec }
func (f *poolFeed) Price(ctx context.Context) (*PriceQuote, error) {
	lp, err := f.pools.GetPool(f.poolID)
	if err != nil {
		return nil, err
	}
	price := f.pools.CalculatePrice(lp)
	if price == nil {
		return nil, fmt.Errorf("pool %s has invalid reserves", f.poolID)
	}
	value := price.PriceAtoB
	if f.direction == "b-to-a" {
		value = price.PriceBtoA
	}
	return &PriceQuote{Source: f.spec, Price: value, FetchedAt: time.Now().UTC()}, nil
}

type coinGeckoFeed struct {
	spec     string
	http     *http.Client
	asset    string
	currency string
}

func (f *coinGeckoFeed) Name() string { return f.spec }
func (f *coinGeckoFeed) Price(ctx context.Context) (*PriceQuote, error) {
	endpoint := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=%s", url.QueryEscape(f.asset), url.QueryEscape(f.currency))
	var response map[string]map[string]float64
	if err := fetchJSON(ctx, f.http, endpoint, &response); err != nil {
		return nil, err
	}
	value, ok := response[f.asset][f.currency]
	if !ok || value <= 0 {
		return nil, fmt.Errorf("CoinGecko returned no price for %s/%s", f.asset, f.currency)
	}
	return &PriceQuote{Source: f.spec, Price: value, FetchedAt: time.Now().UTC()}, nil
}

type frankfurterFeed struct {
	spec  string
	http  *http.Client
	base  string
	quote string
}

func (f *frankfurterFeed) Name() string { return f.spec }
func (f *frankfurterFeed) Price(ctx context.Context) (*PriceQuote, error) {
	endpoint := fmt.Sprintf("https://api.frankfurter.app/latest?from=%s&to=%s", url.QueryEscape(f.base), url.QueryEscape(f.quote))
	var response struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := fetchJSON(ctx, f.http, endpoint, &response); err != nil {
		return nil, err
	}
	value, ok := response.Rates[f.quote]
	if !ok || value <= 0 {
		return nil, fmt.Errorf("Frankfurter returned no rate for %s/%s", f.base, f.quote)
	}
	return &PriceQuote{Source: f.spec, Price: value, FetchedAt: time.Now().UTC()}, nil
}

type restFeed struct {
	spec   string
	http   *http.Client
	rawURL string
	path   string
}

func (f *restFeed) Name() string { return f.spec }
func (f *restFeed) Price(ctx context.Context) (*PriceQuote, error) {
	var payload interface{}
	if err := fetchJSON(ctx, f.http, f.rawURL, &payload); err != nil {
		return nil, err
	}
	value, err := jsonPathFloat(payload, f.path)
	if err != nil {
		return nil, err
	}
	return &PriceQuote{Source: f.spec, Price: value, FetchedAt: time.Now().UTC()}, nil
}

type invertFeed struct {
	spec string
	feed PriceFeed
}

func (f *invertFeed) Name() string { return f.spec }
func (f *invertFeed) Price(ctx context.Context) (*PriceQuote, error) {
	quote, err := f.feed.Price(ctx)
	if err != nil {
		return nil, err
	}
	if quote.Price == 0 {
		return nil, fmt.Errorf("cannot invert zero price")
	}
	quote.Source = f.spec
	quote.Price = 1 / quote.Price
	return quote, nil
}

type maxFeed struct {
	spec  string
	feeds []PriceFeed
}

func (f *maxFeed) Name() string { return f.spec }
func (f *maxFeed) Price(ctx context.Context) (*PriceQuote, error) {
	var best *PriceQuote
	for _, feed := range f.feeds {
		quote, err := feed.Price(ctx)
		if err != nil {
			return nil, err
		}
		if best == nil || quote.Price > best.Price {
			best = quote
		}
	}
	if best == nil {
		return nil, fmt.Errorf("max feed has no inputs")
	}
	best.Source = f.spec
	return best, nil
}

type weightedInput struct {
	feed   PriceFeed
	weight float64
}

type weightedFeed struct {
	spec      string
	inputs    []weightedInput
	weightSum float64
}

func (f *weightedFeed) Name() string { return f.spec }
func (f *weightedFeed) Price(ctx context.Context) (*PriceQuote, error) {
	total := 0.0
	for _, input := range f.inputs {
		quote, err := input.feed.Price(ctx)
		if err != nil {
			return nil, err
		}
		total += quote.Price * input.weight
	}
	return &PriceQuote{Source: f.spec, Price: total / f.weightSum, FetchedAt: time.Now().UTC()}, nil
}

func coinGeckoID(asset string) string {
	asset = strings.ToLower(strings.TrimSpace(asset))
	switch asset {
	case "xlm":
		return "stellar"
	case "usdc":
		return "usd-coin"
	case "eurc":
		return "eurc"
	default:
		return asset
	}
}

func fetchJSON(ctx context.Context, client *http.Client, endpoint string, out interface{}) error {
	if client == nil {
		client = config.NewHTTPClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s returned HTTP %d", endpoint, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func jsonPathFloat(payload interface{}, path string) (float64, error) {
	current := payload
	for _, part := range strings.Split(path, ".") {
		switch value := current.(type) {
		case map[string]interface{}:
			current = value[part]
		case []interface{}:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(value) {
				return 0, fmt.Errorf("invalid array index %q in JSON path", part)
			}
			current = value[idx]
		default:
			return 0, fmt.Errorf("JSON path %q not found", path)
		}
	}
	switch value := current.(type) {
	case float64:
		return value, nil
	case int:
		return float64(value), nil
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, err
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("JSON path %q is not numeric", path)
	}
}
