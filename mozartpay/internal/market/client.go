package market

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/ogtechnologies/mozartpay/internal/models"
	"github.com/ogtechnologies/mozartpay/internal/wallet"
	"github.com/stellar/go/clients/horizonclient"
	"github.com/stellar/go/network"
	"github.com/stellar/go/price"
	hProtocol "github.com/stellar/go/protocols/horizon"
	"github.com/stellar/go/txnbuild"
	"github.com/stellar/go/xdr"
)

const maxOperationsPerTransaction = 100

// Client is a Stellar DEX adapter backed by Horizon.
type Client struct {
	horizon    *horizonclient.Client
	network    models.Network
	passphrase string
	http       *horizonclient.Client
}

// OrderLevel is a normalized order-book level.
type OrderLevel struct {
	Price  float64 `json:"price"`
	Amount float64 `json:"amount"`
}

// OrderBook is a normalized base/quote book.
type OrderBook struct {
	Base      models.AssetRef `json:"base"`
	Quote     models.AssetRef `json:"quote"`
	Bids      []OrderLevel    `json:"bids"`
	Asks      []OrderLevel    `json:"asks"`
	FetchedAt time.Time       `json:"fetchedAt"`
}

// Mid returns the midpoint price when both sides exist.
func (b OrderBook) Mid() (float64, bool) {
	if len(b.Bids) == 0 || len(b.Asks) == 0 || b.Bids[0].Price <= 0 || b.Asks[0].Price <= 0 {
		return 0, false
	}
	return (b.Bids[0].Price + b.Asks[0].Price) / 2, true
}

// OfferSpec describes a desired or existing offer operation.
type OfferSpec struct {
	OfferID int64            `json:"offerId,omitempty"`
	Side    models.OrderSide `json:"side"`
	Base    models.AssetRef  `json:"base"`
	Quote   models.AssetRef  `json:"quote"`
	Price   float64          `json:"price"`
	Amount  float64          `json:"amount"`
	Passive bool             `json:"passive"`
}

// OpenOffer is a normalized live offer.
type OpenOffer struct {
	ID                 int64            `json:"id"`
	Seller             string           `json:"seller"`
	Side               models.OrderSide `json:"side"`
	Base               models.AssetRef  `json:"base"`
	Quote              models.AssetRef  `json:"quote"`
	Price              float64          `json:"price"`
	Amount             float64          `json:"amount"`
	LastModifiedLedger int32            `json:"lastModifiedLedger"`
	LastModifiedTime   *time.Time       `json:"lastModifiedTime,omitempty"`
}

// SubmittedOperation records the offer result corresponding to an operation index.
type SubmittedOperation struct {
	Index   int   `json:"index"`
	OfferID int64 `json:"offerId,omitempty"`
}

// SubmitResult is the result of a market transaction.
type SubmitResult struct {
	Hash       string               `json:"hash"`
	Ledger     int64                `json:"ledger"`
	Operations []SubmittedOperation `json:"operations"`
	ResultXDR  string               `json:"resultXdr,omitempty"`
}

// NewClient creates a network-specific market adapter.
func NewClient(net models.Network) *Client {
	c := &Client{network: net}
	if net == models.NetworkStellarMainnet {
		c.horizon = horizonclient.DefaultPublicNetClient
		c.passphrase = network.PublicNetworkPassphrase
	} else {
		c.horizon = horizonclient.DefaultTestNetClient
		c.passphrase = network.TestNetworkPassphrase
	}
	return c
}

// NewClientWithHorizon creates an adapter against a custom Horizon endpoint.
func NewClientWithHorizon(net models.Network, horizonURL string) *Client {
	c := NewClient(net)
	if horizonURL != "" {
		c.horizon = &horizonclient.Client{HorizonURL: horizonURL}
	}
	return c
}

// Account returns account details.
func (c *Client) Account(ctx context.Context, address string) (hProtocol.Account, error) {
	return c.horizon.AccountDetail(horizonclient.AccountRequest{AccountID: address})
}

// OrderBook returns the live order book for base/quote.
func (c *Client) OrderBook(ctx context.Context, baseAsset, quoteAsset models.AssetRef, limit uint) (*OrderBook, error) {
	if limit == 0 || limit > 200 {
		limit = 20
	}
	sellingType, sellingCode, sellingIssuer := HorizonAsset(baseAsset)
	buyingType, buyingCode, buyingIssuer := HorizonAsset(quoteAsset)

	page, err := c.horizon.OrderBook(horizonclient.OrderBookRequest{
		SellingAssetType:   sellingType,
		SellingAssetCode:   sellingCode,
		SellingAssetIssuer: sellingIssuer,
		BuyingAssetType:    buyingType,
		BuyingAssetCode:    buyingCode,
		BuyingAssetIssuer:  buyingIssuer,
		Limit:              limit,
	})
	if err != nil {
		return nil, fmt.Errorf("order book %s/%s: %w", DisplayCode(baseAsset), DisplayCode(quoteAsset), err)
	}

	book := &OrderBook{
		Base:      baseAsset,
		Quote:     quoteAsset,
		FetchedAt: time.Now().UTC(),
	}
	for _, level := range page.Bids {
		book.Bids = append(book.Bids, parseLevel(level))
	}
	for _, level := range page.Asks {
		book.Asks = append(book.Asks, parseLevel(level))
	}
	return book, nil
}

// OpenOffers returns all open offers for an account.
func (c *Client) OpenOffers(ctx context.Context, account string) ([]OpenOffer, error) {
	page, err := c.horizon.Offers(horizonclient.OfferRequest{
		ForAccount: account,
		Limit:      200,
		Order:      horizonclient.OrderAsc,
	})
	if err != nil {
		return nil, fmt.Errorf("account offers: %w", err)
	}

	offers := make([]OpenOffer, 0, len(page.Embedded.Records))
	for _, record := range page.Embedded.Records {
		offers = append(offers, normalizeOffer(record))
	}
	return offers, nil
}

// OpenOffersForPair filters account offers to a base/quote pair.
func (c *Client) OpenOffersForPair(ctx context.Context, account string, baseAsset, quoteAsset models.AssetRef) ([]OpenOffer, error) {
	page, err := c.horizon.Offers(horizonclient.OfferRequest{
		ForAccount: account,
		Limit:      200,
		Order:      horizonclient.OrderAsc,
	})
	if err != nil {
		return nil, fmt.Errorf("account offers: %w", err)
	}
	filtered := make([]OpenOffer, 0)
	for _, record := range page.Embedded.Records {
		if offer, ok := normalizeOfferForPair(record, baseAsset, quoteAsset); ok {
			filtered = append(filtered, offer)
		}
	}
	return filtered, nil
}

// LatestTrades returns latest trades for a pair.
func (c *Client) LatestTrades(ctx context.Context, baseAsset, quoteAsset models.AssetRef, limit uint) ([]hProtocol.Trade, error) {
	if limit == 0 || limit > 200 {
		limit = 20
	}
	baseType, baseCode, baseIssuer := HorizonAsset(baseAsset)
	quoteType, quoteCode, quoteIssuer := HorizonAsset(quoteAsset)
	page, err := c.horizon.Trades(horizonclient.TradeRequest{
		BaseAssetType:      baseType,
		BaseAssetCode:      baseCode,
		BaseAssetIssuer:    baseIssuer,
		CounterAssetType:   quoteType,
		CounterAssetCode:   quoteCode,
		CounterAssetIssuer: quoteIssuer,
		Order:              horizonclient.OrderDesc,
		Limit:              limit,
	})
	if err != nil {
		return nil, fmt.Errorf("trades %s/%s: %w", DisplayCode(baseAsset), DisplayCode(quoteAsset), err)
	}
	return page.Embedded.Records, nil
}

// Trades returns account trades filtered to a base/quote pair.
func (c *Client) Trades(ctx context.Context, account string, baseType horizonclient.AssetType, baseCode, baseIssuer string, quoteType horizonclient.AssetType, quoteCode, quoteIssuer string, limit uint) ([]hProtocol.Trade, error) {
	if limit == 0 || limit > 200 {
		limit = 200
	}
	page, err := c.horizon.Trades(horizonclient.TradeRequest{
		ForAccount:         account,
		BaseAssetType:      baseType,
		BaseAssetCode:      baseCode,
		BaseAssetIssuer:    baseIssuer,
		CounterAssetType:   quoteType,
		CounterAssetCode:   quoteCode,
		CounterAssetIssuer: quoteIssuer,
		Order:              horizonclient.OrderDesc,
		Limit:              limit,
	})
	if err != nil {
		return nil, fmt.Errorf("account trades: %w", err)
	}
	return page.Embedded.Records, nil
}

// TradeAggregations returns historical OHLC candles for a pair.
func (c *Client) TradeAggregations(ctx context.Context, baseAsset, quoteAsset models.AssetRef, start, end time.Time, resolution time.Duration, limit uint) ([]models.Candle, error) {
	baseType, baseCode, baseIssuer := HorizonAsset(baseAsset)
	quoteType, quoteCode, quoteIssuer := HorizonAsset(quoteAsset)
	if limit == 0 || limit > 200 {
		limit = 200
	}
	page, err := c.horizon.TradeAggregations(horizonclient.TradeAggregationRequest{
		StartTime:          start,
		EndTime:            end,
		Resolution:         resolution,
		BaseAssetType:      baseType,
		BaseAssetCode:      baseCode,
		BaseAssetIssuer:    baseIssuer,
		CounterAssetType:   quoteType,
		CounterAssetCode:   quoteCode,
		CounterAssetIssuer: quoteIssuer,
		Order:              horizonclient.OrderAsc,
		Limit:              limit,
	})
	if err != nil {
		return nil, fmt.Errorf("trade aggregations: %w", err)
	}
	candles := make([]models.Candle, 0, len(page.Embedded.Records))
	for pages := 0; ; pages++ {
		for _, record := range page.Embedded.Records {
			open, _ := strconv.ParseFloat(record.Open, 64)
			high, _ := strconv.ParseFloat(record.High, 64)
			low, _ := strconv.ParseFloat(record.Low, 64)
			closePrice, _ := strconv.ParseFloat(record.Close, 64)
			volume, _ := strconv.ParseFloat(record.BaseVolume, 64)
			candles = append(candles, models.Candle{
				Timestamp: time.UnixMilli(record.Timestamp).UTC(),
				Open:      open,
				High:      high,
				Low:       low,
				Close:     closePrice,
				Volume:    volume,
			})
		}
		if page.Links.Next.Href == "" || len(page.Embedded.Records) == 0 || pages >= 20 {
			break
		}
		page, err = c.horizon.NextTradeAggregationsPage(page)
		if err != nil {
			return candles, fmt.Errorf("next trade aggregations page: %w", err)
		}
	}
	return candles, nil
}

// LatestTradePrice returns the last trade price in quote per base.
func (c *Client) LatestTradePrice(ctx context.Context, baseAsset, quoteAsset models.AssetRef) (float64, error) {
	trades, err := c.LatestTrades(ctx, baseAsset, quoteAsset, 1)
	if err != nil {
		return 0, err
	}
	if len(trades) == 0 {
		return 0, fmt.Errorf("no trades for %s/%s", DisplayCode(baseAsset), DisplayCode(quoteAsset))
	}
	return TradePrice(trades[0]), nil
}

// TradePrice normalizes a Horizon trade price to quote per base.
func TradePrice(trade hProtocol.Trade) float64 {
	if trade.Price.D != 0 && trade.Price.N != 0 {
		return float64(trade.Price.N) / float64(trade.Price.D)
	}
	baseAmount, _ := strconv.ParseFloat(trade.BaseAmount, 64)
	counterAmount, _ := strconv.ParseFloat(trade.CounterAmount, 64)
	if baseAmount == 0 {
		return 0
	}
	return counterAmount / baseAmount
}

// FeeStats returns current Horizon fee statistics.
func (c *Client) FeeStats(ctx context.Context) (hProtocol.FeeStats, error) {
	return c.horizon.FeeStats()
}

// SuggestedBaseFee returns a per-operation fee based on current fee stats.
func (c *Client) SuggestedBaseFee(ctx context.Context) int64 {
	stats, err := c.FeeStats(ctx)
	if err != nil {
		return txnbuild.MinBaseFee
	}
	fee := stats.FeeCharged.P90
	if stats.MaxFee.P50 > fee {
		fee = stats.MaxFee.P50
	}
	if stats.LastLedgerBaseFee > fee {
		fee = stats.LastLedgerBaseFee
	}
	if fee < txnbuild.MinBaseFee {
		fee = txnbuild.MinBaseFee
	}
	return fee
}

// SubmitOffers submits offer operations in one transaction.
func (c *Client) SubmitOffers(ctx context.Context, specs []OfferSpec) (*SubmitResult, error) {
	if len(specs) == 0 {
		return &SubmitResult{}, nil
	}
	if len(specs) > maxOperationsPerTransaction {
		return nil, fmt.Errorf("too many operations: %d > %d", len(specs), maxOperationsPerTransaction)
	}

	kp, err := wallet.LoadStellarKeypairForSwap()
	if err != nil {
		return nil, fmt.Errorf("load active Stellar wallet: %w", err)
	}
	account, err := c.Account(ctx, kp.Address())
	if err != nil {
		return nil, fmt.Errorf("load account %s: %w", kp.Address(), err)
	}

	ops := make([]txnbuild.Operation, 0, len(specs))
	for _, spec := range specs {
		op, err := OfferOperation(spec)
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}

	tx, err := txnbuild.NewTransaction(txnbuild.TransactionParams{
		SourceAccount:        &account,
		IncrementSequenceNum: true,
		BaseFee:              c.SuggestedBaseFee(ctx),
		Preconditions:        txnbuild.Preconditions{TimeBounds: txnbuild.NewTimeout(60)},
		Operations:           ops,
	})
	if err != nil {
		return nil, fmt.Errorf("build offer transaction: %w", err)
	}
	tx, err = tx.Sign(c.passphrase, kp)
	if err != nil {
		return nil, fmt.Errorf("sign offer transaction: %w", err)
	}
	xdr64, err := tx.Base64()
	if err != nil {
		return nil, fmt.Errorf("serialize offer transaction: %w", err)
	}

	resp, err := c.horizon.SubmitTransactionXDR(xdr64)
	if err != nil {
		return nil, formatHorizonError(err)
	}

	result := &SubmitResult{
		Hash:      resp.Hash,
		Ledger:    int64(resp.Ledger),
		ResultXDR: resp.ResultXdr,
	}
	result.Operations = ExtractOfferIDs(resp.ResultXdr)
	return result, nil
}

// CancelOffer cancels a single live offer.
func (c *Client) CancelOffer(ctx context.Context, offer OpenOffer) (*SubmitResult, error) {
	return c.SubmitOffers(ctx, []OfferSpec{{
		OfferID: offer.ID,
		Side:    offer.Side,
		Base:    offer.Base,
		Quote:   offer.Quote,
		Price:   offer.Price,
		Amount:  0,
	}})
}

// CancelAll cancels all offers for an account in batches.
func (c *Client) CancelAll(ctx context.Context, account string) ([]SubmitResult, error) {
	offers, err := c.OpenOffers(ctx, account)
	if err != nil {
		return nil, err
	}
	results := make([]SubmitResult, 0)
	for start := 0; start < len(offers); start += maxOperationsPerTransaction {
		end := start + maxOperationsPerTransaction
		if end > len(offers) {
			end = len(offers)
		}
		specs := make([]OfferSpec, 0, end-start)
		for _, offer := range offers[start:end] {
			specs = append(specs, OfferSpec{
				OfferID: offer.ID,
				Side:    offer.Side,
				Base:    offer.Base,
				Quote:   offer.Quote,
				Price:   offer.Price,
				Amount:  0,
			})
		}
		res, err := c.SubmitOffers(ctx, specs)
		if err != nil {
			return results, err
		}
		results = append(results, *res)
	}
	return results, nil
}

// HasTrustline reports whether account has a trustline for a credit asset.
func HasTrustline(account hProtocol.Account, asset models.AssetRef) bool {
	if canonical(asset) == "native" {
		return true
	}
	for _, balance := range account.Balances {
		if balance.Asset.Code == asset.Code && balance.Asset.Issuer == asset.Issuer {
			return true
		}
	}
	return false
}

// Spendable returns the currently spendable balance for an asset.
func Spendable(account hProtocol.Account, asset models.AssetRef, minXLMReserve float64) float64 {
	for _, balance := range account.Balances {
		if canonical(asset) == "native" {
			if balance.Asset.Type != "native" {
				continue
			}
			bal, _ := strconv.ParseFloat(balance.Balance, 64)
			selling, _ := strconv.ParseFloat(balance.SellingLiabilities, 64)
			reserve := float64(2+account.SubentryCount) * 0.5
			if minXLMReserve > 0 {
				reserve += minXLMReserve
			}
			return math.Max(0, bal-reserve-selling)
		}
		if balance.Asset.Code == asset.Code && balance.Asset.Issuer == asset.Issuer {
			bal, _ := strconv.ParseFloat(balance.Balance, 64)
			selling, _ := strconv.ParseFloat(balance.SellingLiabilities, 64)
			return math.Max(0, bal-selling)
		}
	}
	return 0
}

// OfferOperation builds a transaction operation for an offer spec.
func OfferOperation(spec OfferSpec) (txnbuild.Operation, error) {
	baseAsset, err := TxnAsset(spec.Base)
	if err != nil {
		return nil, err
	}
	quoteAsset, err := TxnAsset(spec.Quote)
	if err != nil {
		return nil, err
	}
	priceValue, err := price.Parse(fmt.Sprintf("%.7f", spec.Price))
	if err != nil {
		return nil, fmt.Errorf("invalid price %.7f: %w", spec.Price, err)
	}
	if spec.Amount < 0 {
		return nil, fmt.Errorf("amount cannot be negative")
	}
	amount := fmt.Sprintf("%.7f", spec.Amount)

	if spec.Side == models.OrderSideBuy {
		return &txnbuild.ManageBuyOffer{
			Selling: quoteAsset,
			Buying:  baseAsset,
			Amount:  amount,
			Price:   priceValue,
			OfferID: spec.OfferID,
		}, nil
	}
	if spec.OfferID == 0 && spec.Passive && spec.Amount > 0 {
		return &txnbuild.CreatePassiveSellOffer{
			Selling: baseAsset,
			Buying:  quoteAsset,
			Amount:  amount,
			Price:   priceValue,
		}, nil
	}
	return &txnbuild.ManageSellOffer{
		Selling: baseAsset,
		Buying:  quoteAsset,
		Amount:  amount,
		Price:   priceValue,
		OfferID: spec.OfferID,
	}, nil
}

// ExtractOfferIDs decodes operation results and returns created/updated offer IDs.
func ExtractOfferIDs(resultXDR string) []SubmittedOperation {
	var result xdr.TransactionResult
	if err := xdr.SafeUnmarshalBase64(resultXDR, &result); err != nil || result.Result.Results == nil {
		return nil
	}
	out := make([]SubmittedOperation, 0, len(*result.Result.Results))
	for i, opResult := range *result.Result.Results {
		entry := SubmittedOperation{Index: i}
		tr, ok := opResult.GetTr()
		if !ok {
			out = append(out, entry)
			continue
		}
		if sell, ok := tr.GetManageSellOfferResult(); ok {
			if success, ok := sell.GetSuccess(); ok {
				if offer, ok := success.Offer.GetOffer(); ok {
					entry.OfferID = int64(offer.OfferId)
				}
			}
		} else if passive, ok := tr.GetCreatePassiveSellOfferResult(); ok {
			if success, ok := passive.GetSuccess(); ok {
				if offer, ok := success.Offer.GetOffer(); ok {
					entry.OfferID = int64(offer.OfferId)
				}
			}
		} else if buy, ok := tr.GetManageBuyOfferResult(); ok {
			if success, ok := buy.GetSuccess(); ok {
				if offer, ok := success.Offer.GetOffer(); ok {
					entry.OfferID = int64(offer.OfferId)
				}
			}
		}
		out = append(out, entry)
	}
	return out
}

func parseLevel(level hProtocol.PriceLevel) OrderLevel {
	priceValue, _ := strconv.ParseFloat(level.Price, 64)
	amount, _ := strconv.ParseFloat(level.Amount, 64)
	return OrderLevel{Price: priceValue, Amount: amount}
}

func normalizeOffer(record hProtocol.Offer) OpenOffer {
	selling := FromHorizonAsset(record.Selling)
	buying := FromHorizonAsset(record.Buying)
	amount, _ := strconv.ParseFloat(record.Amount, 64)
	priceValue, _ := strconv.ParseFloat(record.Price, 64)

	return OpenOffer{
		ID:                 record.ID,
		Seller:             record.Seller,
		Side:               models.OrderSideSell,
		Base:               selling,
		Quote:              buying,
		Price:              priceValue,
		Amount:             amount,
		LastModifiedLedger: record.LastModifiedLedger,
		LastModifiedTime:   record.LastModifiedTime,
	}
}

func normalizeOfferForPair(record hProtocol.Offer, baseAsset, quoteAsset models.AssetRef) (OpenOffer, bool) {
	selling := FromHorizonAsset(record.Selling)
	buying := FromHorizonAsset(record.Buying)
	amount, _ := strconv.ParseFloat(record.Amount, 64)
	priceValue, _ := strconv.ParseFloat(record.Price, 64)

	offer := OpenOffer{
		ID:                 record.ID,
		Seller:             record.Seller,
		Price:              priceValue,
		Amount:             amount,
		LastModifiedLedger: record.LastModifiedLedger,
		LastModifiedTime:   record.LastModifiedTime,
	}
	if Equal(selling, baseAsset) && Equal(buying, quoteAsset) {
		offer.Side = models.OrderSideSell
		offer.Base = baseAsset
		offer.Quote = quoteAsset
		return offer, true
	}
	if Equal(selling, quoteAsset) && Equal(buying, baseAsset) {
		offer.Side = models.OrderSideBuy
		offer.Base = baseAsset
		offer.Quote = quoteAsset
		if priceValue > 0 {
			offer.Price = 1 / priceValue
			offer.Amount = amount * priceValue
		}
		return offer, true
	}
	return offer, false
}

func formatHorizonError(err error) error {
	if herr, ok := err.(*horizonclient.Error); ok {
		if resultCodes, rcErr := herr.ResultCodes(); rcErr == nil && resultCodes != nil {
			return fmt.Errorf("transaction failed: %s, operations: %v", resultCodes.TransactionCode, resultCodes.OperationCodes)
		}
		return fmt.Errorf("horizon error: %s", herr.Problem.Title)
	}
	return err
}
