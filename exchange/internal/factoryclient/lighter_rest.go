package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

const (
	lighterVenue = exchange.VenueLighter
	lighterSpot  = "spot"
	lighterPerp  = "perp"
)

type lighterMarketMeta struct {
	instrument exchange.Instrument
	marketID   int
	marketType string
	priceScale decimal.Decimal
	sizeScale  decimal.Decimal
	quoteScale decimal.Decimal
}

func (client *lighterSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	metas, err := client.lighterMetas(ctx, "Instruments", exchange.ProductSpot, lighterSpot)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}

func (client *lighterSpotClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	meta, err := client.lighterMeta(ctx, "OrderBook", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	return lighterOrderBook(ctx, client.sdk, exchange.ProductSpot, "OrderBook", meta, req.Limit)
}

func (client *lighterSpotClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	meta, err := client.lighterMeta(ctx, "Candles", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	return lighterCandles(ctx, client.sdk, exchange.ProductSpot, meta, req)
}

func (client *lighterSpotClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := req.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(exchange.ProductSpot, "PlaceOrder", "invalid normalized order request")
	}
	meta, err := client.lighterMeta(ctx, "PlaceOrder", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return client.lighterPlace(ctx, exchange.ProductSpot, meta, req)
}

func (client *lighterSpotClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if _, err := lighterValidateCancel(req); err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(exchange.ProductSpot, "CancelOrder", err.Error())
	}
	meta, err := client.lighterMeta(ctx, "CancelOrder", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return client.lighterCancel(ctx, exchange.ProductSpot, meta, req)
}

func (client *lighterSpotClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	meta, err := client.lighterMeta(ctx, "OpenOrders", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return lighterOpenOrders(ctx, client.sdk, exchange.ProductSpot, meta, req)
}

func (client *lighterSpotClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	meta, err := client.lighterMeta(ctx, "Fills", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.FillPage{}, err
	}
	return lighterFills(ctx, client.sdk, exchange.ProductSpot, lighterSpot, meta, req)
}

func (client *lighterSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := lighterAccount(ctx, client.sdk, exchange.ProductSpot, "Balances")
	if err != nil {
		return nil, err
	}
	return lighterSpotBalances(account.Assets, exchange.ProductSpot, "Balances")
}

func (client *lighterSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.Balances(ctx)
	if err != nil {
		return exchange.SpotAccount{}, withExchangeOperation(err, "SpotAccount")
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *lighterPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	metas, err := client.lighterMetas(ctx, "Instruments", exchange.ProductPerp, lighterPerp)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(metas))
	for _, meta := range metas {
		out = append(out, meta.instrument)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Symbol < out[j].Symbol })
	return out, nil
}

func (client *lighterPerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	meta, err := client.lighterMeta(ctx, "OrderBook", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	return lighterOrderBook(ctx, client.sdk, exchange.ProductPerp, "OrderBook", meta, req.Limit)
}

func (client *lighterPerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	meta, err := client.lighterMeta(ctx, "Candles", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	return lighterCandles(ctx, client.sdk, exchange.ProductPerp, meta, req)
}

func (client *lighterPerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := req.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(exchange.ProductPerp, "PlaceOrder", "invalid normalized order request")
	}
	meta, err := client.lighterMeta(ctx, "PlaceOrder", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return client.lighterPlace(ctx, exchange.ProductPerp, meta, req)
}

func (client *lighterPerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if _, err := lighterValidateCancel(req); err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(exchange.ProductPerp, "CancelOrder", err.Error())
	}
	meta, err := client.lighterMeta(ctx, "CancelOrder", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return client.lighterCancel(ctx, exchange.ProductPerp, meta, req)
}

func (client *lighterPerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	meta, err := client.lighterMeta(ctx, "OpenOrders", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return lighterOpenOrders(ctx, client.sdk, exchange.ProductPerp, meta, req)
}

func (client *lighterPerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	meta, err := client.lighterMeta(ctx, "Fills", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.FillPage{}, err
	}
	return lighterFills(ctx, client.sdk, exchange.ProductPerp, lighterPerp, meta, req)
}

func (client *lighterPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *lighterPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	account, err := lighterAccount(ctx, client.sdk, exchange.ProductPerp, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	balances, err := lighterSpotBalances(account.Assets, exchange.ProductPerp, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	equity, err := lighterOptional(account.Collateral)
	if err != nil {
		return exchange.PerpAccount{}, lighterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	available, err := lighterOptional(account.AvailableBalance)
	if err != nil {
		return exchange.PerpAccount{}, lighterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	margin, err := lighterOptional(account.CrossInitialMarginReq)
	if err != nil {
		return exchange.PerpAccount{}, lighterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	return exchange.PerpAccount{Balances: balances, Equity: equity, Available: available, MarginUsed: margin}, nil
}

func (client *lighterPerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	metas, err := client.lighterMetas(ctx, "Positions", exchange.ProductPerp, lighterPerp)
	if err != nil {
		return nil, err
	}
	var requested lighterMarketMeta
	if req.Instrument != "" {
		var ok bool
		requested, ok = metas[req.Instrument]
		if !ok {
			return nil, lighterInvalid(exchange.ProductPerp, "Positions", "instrument is not present in Lighter perp metadata")
		}
	}
	account, err := lighterAccount(ctx, client.sdk, exchange.ProductPerp, "Positions")
	if err != nil {
		return nil, err
	}
	out := make([]exchange.Position, 0, len(account.Positions))
	for _, row := range account.Positions {
		meta, ok := client.state.byID[row.MarketId]
		if !ok || meta.marketType != lighterPerp {
			return nil, lighterMalformed(exchange.ProductPerp, "Positions", "unknown or mixed position market")
		}
		pos, err := lighterPosition(row, meta)
		if err != nil {
			return nil, lighterMalformed(exchange.ProductPerp, "Positions", err.Error())
		}
		if req.Instrument != "" && row.MarketId != requested.marketID {
			continue
		}
		if pos.Quantity.IsZero() {
			continue
		}
		out = append(out, pos)
	}
	return out, nil
}

func (client *lighterSpotClient) lighterMetas(ctx context.Context, operation string, product exchange.Product, marketType string) (map[string]lighterMarketMeta, error) {
	return lighterMetas(ctx, client.sdk, client.state, operation, product, marketType)
}

func (client *lighterPerpClient) lighterMetas(ctx context.Context, operation string, product exchange.Product, marketType string) (map[string]lighterMarketMeta, error) {
	return lighterMetas(ctx, client.sdk, client.state, operation, product, marketType)
}

func (client *lighterSpotClient) lighterMeta(ctx context.Context, operation string, product exchange.Product, marketType, instrument string) (lighterMarketMeta, error) {
	return lighterMeta(ctx, client.sdk, client.state, operation, product, marketType, instrument)
}

func (client *lighterPerpClient) lighterMeta(ctx context.Context, operation string, product exchange.Product, marketType, instrument string) (lighterMarketMeta, error) {
	return lighterMeta(ctx, client.sdk, client.state, operation, product, marketType, instrument)
}

func lighterMetas(ctx context.Context, sdk *lighter.Client, state *lighterRESTState, operation string, product exchange.Product, marketType string) (map[string]lighterMarketMeta, error) {
	if err := lighterReady(ctx, product, operation, sdk); err != nil {
		return nil, err
	}
	for {
		state.cacheMu.Lock()
		if state.metas != nil {
			cached := state.metas
			state.cacheMu.Unlock()
			return cached, nil
		}
		if state.loading == nil {
			state.loading = make(chan struct{})
			loading := state.loading
			state.cacheMu.Unlock()
			return lighterLoadMetas(ctx, sdk, state, operation, product, marketType, loading)
		}
		loading := state.loading
		state.cacheMu.Unlock()
		select {
		case <-loading:
		case <-ctx.Done():
			return nil, lighterContextErr(product, operation, ctx.Err())
		}
	}
}

func lighterLoadMetas(ctx context.Context, sdk *lighter.Client, state *lighterRESTState, operation string, product exchange.Product, marketType string, loading chan struct{}) (map[string]lighterMarketMeta, error) {
	res, err := sdk.GetOrderBookDetails(ctx, nil, nil)
	if err != nil {
		lighterClearLoading(state, loading)
		return nil, lighterNormalizeErr(product, operation, err)
	}
	metas, byID, err := lighterBuildMetas(res, product, marketType)
	if err != nil {
		lighterClearLoading(state, loading)
		return nil, lighterMalformed(product, operation, err.Error())
	}
	state.cacheMu.Lock()
	state.metas = metas
	state.byID = byID
	if state.loading == loading {
		state.loading = nil
		close(loading)
	}
	state.cacheMu.Unlock()
	return metas, nil
}

func lighterClearLoading(state *lighterRESTState, loading chan struct{}) {
	state.cacheMu.Lock()
	if state.loading == loading {
		state.loading = nil
		close(loading)
	}
	state.cacheMu.Unlock()
}

func lighterMeta(ctx context.Context, sdk *lighter.Client, state *lighterRESTState, operation string, product exchange.Product, marketType, instrument string) (lighterMarketMeta, error) {
	if strings.TrimSpace(instrument) == "" || strings.TrimSpace(instrument) != instrument {
		return lighterMarketMeta{}, lighterInvalid(product, operation, "instrument is required and must not have surrounding whitespace")
	}
	metas, err := lighterMetas(ctx, sdk, state, operation, product, marketType)
	if err != nil {
		return lighterMarketMeta{}, err
	}
	meta, ok := metas[instrument]
	if !ok {
		return lighterMarketMeta{}, lighterInvalid(product, operation, "instrument is not present in Lighter metadata")
	}
	return meta, nil
}

func lighterBuildMetas(res *lighter.OrderBookDetailsResponse, product exchange.Product, marketType string) (map[string]lighterMarketMeta, map[int]lighterMarketMeta, error) {
	if res == nil || res.Code != 200 {
		return nil, nil, errors.New("metadata response code is not 200")
	}
	rows := res.SpotOrderBookDetails
	if marketType == lighterPerp {
		rows = res.OrderBookDetails
	}
	metas := make(map[string]lighterMarketMeta, len(rows))
	byID := make(map[int]lighterMarketMeta, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, nil, errors.New("nil market detail")
		}
		if row.MarketType != marketType {
			return nil, nil, errors.New("mixed market_type in metadata slice")
		}
		if strings.TrimSpace(row.Symbol) == "" || row.MarketId < 0 {
			return nil, nil, errors.New("invalid market identity")
		}
		priceDecimals, err := lighterMetaDecimals(row.PriceDecimals, row.SupportedPriceDecimals)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid price decimals: %w", err)
		}
		sizeDecimals, err := lighterMetaDecimals(row.SizeDecimals, row.SupportedSizeDecimals)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid size decimals: %w", err)
		}
		quoteDecimals, err := lighterMetaDecimals(row.SupportedQuoteDecimals, 0)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid quote decimals: %w", err)
		}
		priceInc := decimal.New(1, -int32(priceDecimals))
		qtyInc := decimal.New(1, -int32(sizeDecimals))
		minQty, err := lighterNonNegativeDecimal(row.MinBaseAmount)
		if err != nil {
			return nil, nil, errors.New("invalid min base amount")
		}
		minNotional := exchange.OptionalDecimal{}
		if strings.TrimSpace(row.MinQuoteAmount) != "" {
			value, err := lighterNonNegativeDecimal(row.MinQuoteAmount)
			if err != nil {
				return nil, nil, errors.New("invalid min quote amount")
			}
			if value.IsPositive() {
				minNotional = exchange.OptionalDecimal{Value: value, Valid: true}
			}
		}
		base, quote := lighterAssets(row.Symbol, product)
		inst := exchange.Instrument{
			Symbol:            row.Symbol,
			BaseAsset:         base,
			QuoteAsset:        quote,
			Product:           product,
			PriceIncrement:    priceInc,
			QuantityIncrement: qtyInc,
			MinQuantity:       minQty,
			MinNotional:       minNotional,
		}
		if product == exchange.ProductPerp {
			inst.SettleAsset = quote
		}
		meta := lighterMarketMeta{instrument: inst, marketID: row.MarketId, marketType: marketType, priceScale: decimal.New(1, int32(priceDecimals)), sizeScale: decimal.New(1, int32(sizeDecimals)), quoteScale: decimal.New(1, int32(quoteDecimals))}
		if _, exists := metas[row.Symbol]; exists {
			return nil, nil, errors.New("duplicate market symbol")
		}
		if _, exists := byID[row.MarketId]; exists {
			return nil, nil, errors.New("duplicate market id")
		}
		metas[row.Symbol] = meta
		byID[row.MarketId] = meta
	}
	return metas, byID, nil
}

func lighterMetaDecimals(primary, fallback uint8) (uint8, error) {
	value := primary
	if value == 0 {
		value = fallback
	}
	if value > 18 {
		return 0, errors.New("decimals exceed safe bound")
	}
	return value, nil
}

func lighterOrderBook(ctx context.Context, sdk *lighter.Client, product exchange.Product, operation string, meta lighterMarketMeta, limit int) (exchange.OrderBook, error) {
	if limit < 0 {
		return exchange.OrderBook{}, lighterInvalid(product, operation, "limit must be non-negative")
	}
	res, err := sdk.GetOrderBookOrders(ctx, meta.marketID, int64(limit))
	if err != nil {
		return exchange.OrderBook{}, lighterNormalizeErr(product, operation, err)
	}
	if res.Code != 200 {
		return exchange.OrderBook{}, lighterMalformed(product, operation, "order book response code is not 200")
	}
	bids, err := lighterBidLevels(res.Bids)
	if err != nil {
		return exchange.OrderBook{}, lighterMalformed(product, operation, err.Error())
	}
	asks, err := lighterAskLevels(res.Asks)
	if err != nil {
		return exchange.OrderBook{}, lighterMalformed(product, operation, err.Error())
	}
	return exchange.OrderBook{Instrument: meta.instrument.Symbol, Bids: bids, Asks: asks, Page: exchange.PageInfo{Limit: limit}}, nil
}

func lighterCandles(ctx context.Context, sdk *lighter.Client, product exchange.Product, meta lighterMarketMeta, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if req.Interval == "" {
		return exchange.CandlePage{}, lighterInvalid(product, "Candles", "interval is required")
	}
	if req.Limit < 0 {
		return exchange.CandlePage{}, lighterInvalid(product, "Candles", "limit must be non-negative")
	}
	interval, err := lighterInterval(req.Interval)
	if err != nil {
		return exchange.CandlePage{}, lighterInvalid(product, "Candles", err.Error())
	}
	end := req.End
	if end.IsZero() {
		end = time.Now().UTC()
	}
	start := req.Start
	if start.IsZero() {
		windowSize := req.Limit
		if windowSize == 0 {
			windowSize = 100
		}
		start = end.Add(-time.Duration(windowSize) * interval)
	}
	if !start.Before(end) {
		return exchange.CandlePage{}, lighterInvalid(product, "Candles", "start must be before end")
	}
	res, err := sdk.GetCandlesticks(ctx, meta.marketID, req.Interval, start.Unix(), end.Unix(), int64(req.Limit))
	if err != nil {
		return exchange.CandlePage{}, lighterNormalizeErr(product, "Candles", err)
	}
	if res.Code != 200 {
		return exchange.CandlePage{}, lighterMalformed(product, "Candles", "candles response code is not 200")
	}
	if res.Resolution != "" && res.Resolution != req.Interval {
		return exchange.CandlePage{}, lighterMalformed(product, "Candles", "candles response resolution mismatch")
	}
	out := make([]exchange.Candle, 0, len(res.Candlesticks))
	var last time.Time
	for _, row := range res.Candlesticks {
		if row.Timestamp <= 0 || !lighterFinite(row.Open, row.High, row.Low, row.Close, row.Volume) {
			return exchange.CandlePage{}, lighterMalformed(product, "Candles", "invalid candle numeric field")
		}
		if row.Open <= 0 || row.High <= 0 || row.Low <= 0 || row.Close <= 0 || row.Volume < 0 ||
			row.Low > row.Open || row.Low > row.Close || row.High < row.Open || row.High < row.Close || row.Low > row.High {
			return exchange.CandlePage{}, lighterMalformed(product, "Candles", "invalid candle OHLCV")
		}
		open := decimal.NewFromFloat(row.Open)
		high := decimal.NewFromFloat(row.High)
		low := decimal.NewFromFloat(row.Low)
		closeValue := decimal.NewFromFloat(row.Close)
		volume := decimal.NewFromFloat(row.Volume)
		ts := time.Unix(row.Timestamp, 0).UTC()
		if !last.IsZero() && !ts.After(last) {
			return exchange.CandlePage{}, lighterMalformed(product, "Candles", "candles must be strictly ascending")
		}
		last = ts
		closeTime := ts.Add(interval)
		out = append(out, exchange.Candle{OpenTime: ts, CloseTime: closeTime, Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: !closeTime.After(time.Now())})
	}
	return exchange.CandlePage{Candles: out, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: start, WindowEnd: end}}, nil
}

func (client *lighterSpotClient) lighterPlace(ctx context.Context, product exchange.Product, meta lighterMarketMeta, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return lighterPlace(ctx, client.sdk, client.state, product, meta, req)
}

func (client *lighterPerpClient) lighterPlace(ctx context.Context, product exchange.Product, meta lighterMarketMeta, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return lighterPlace(ctx, client.sdk, client.state, product, meta, req)
}

func lighterPlace(ctx context.Context, sdk *lighter.Client, state *lighterRESTState, product exchange.Product, meta lighterMarketMeta, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	price, qty, clientID, err := lighterValidatePlace(product, meta, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(product, "PlaceOrder", err.Error())
	}
	if req.Type == exchange.OrderTypeMarket {
		price, err = lighterMarketProtectionPrice(ctx, sdk, product, meta, req.Side)
		if err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
	}
	if err := lighterEnterCommand(ctx, product, "PlaceOrder", state); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	defer lighterLeaveCommand(state)
	trackedCtx, tracker := lighterWithSendTracker(ctx)
	isAsk := uint32(0)
	if req.Side == exchange.SideSell {
		isAsk = 1
	}
	native := lighterPlaceRequest(meta, req, price, qty, clientID, isAsk)
	resp, err := sdk.PlaceOrder(trackedCtx, native)
	if err != nil {
		return lighterCommandErr(product, exchange.OrderOperationPlace, meta.instrument.Symbol, "", req.ClientOrderID, err, sdk, tracker)
	}
	ack, err := lighterCommandAck(product, "PlaceOrder", exchange.OrderOperationPlace, meta.instrument.Symbol, "", req.ClientOrderID, resp.Code, resp.Message, resp.TxHash, sdk, tracker)
	ack.OrderType = req.Type
	if err != nil {
		return ack, err
	}
	return ack, ack.Validate()
}

func (client *lighterSpotClient) lighterCancel(ctx context.Context, product exchange.Product, meta lighterMarketMeta, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return lighterCancel(ctx, client.sdk, client.state, product, meta, req)
}

func (client *lighterPerpClient) lighterCancel(ctx context.Context, product exchange.Product, meta lighterMarketMeta, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return lighterCancel(ctx, client.sdk, client.state, product, meta, req)
}

func lighterCancel(ctx context.Context, sdk *lighter.Client, state *lighterRESTState, product exchange.Product, meta lighterMarketMeta, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	orderID, err := lighterValidateCancel(req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, lighterInvalid(product, "CancelOrder", err.Error())
	}
	if err := lighterEnterCommand(ctx, product, "CancelOrder", state); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	defer lighterLeaveCommand(state)
	trackedCtx, tracker := lighterWithSendTracker(ctx)
	resp, err := sdk.CancelOrder(trackedCtx, lighter.CancelOrderRequest{MarketId: meta.marketID, OrderId: orderID})
	if err != nil {
		return lighterCommandErr(product, exchange.OrderOperationCancel, meta.instrument.Symbol, req.OrderID, "", err, sdk, tracker)
	}
	return lighterCommandAck(product, "CancelOrder", exchange.OrderOperationCancel, meta.instrument.Symbol, req.OrderID, "", resp.Code, resp.Message, resp.TxHash, sdk, tracker)
}

func lighterEnterCommand(ctx context.Context, product exchange.Product, operation string, state *lighterRESTState) error {
	if ctx == nil {
		return lighterInvalid(product, operation, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return lighterContextErr(product, operation, err)
	}
	select {
	case state.cmdGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			lighterLeaveCommand(state)
			return lighterContextErr(product, operation, err)
		}
		return nil
	case <-ctx.Done():
		return lighterContextErr(product, operation, ctx.Err())
	}
}

func lighterLeaveCommand(state *lighterRESTState) {
	<-state.cmdGate
}

func lighterOpenOrders(ctx context.Context, sdk *lighter.Client, product exchange.Product, meta lighterMarketMeta, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if req.Cursor != "" {
		return exchange.OrderPage{}, lighterInvalid(product, "OpenOrders", "Lighter active orders does not support cursor")
	}
	if req.Limit < 0 {
		return exchange.OrderPage{}, lighterInvalid(product, "OpenOrders", "limit must be non-negative")
	}
	res, err := sdk.GetAccountActiveOrders(ctx, meta.marketID)
	if err != nil {
		return exchange.OrderPage{}, lighterNormalizeErr(product, "OpenOrders", err)
	}
	if res.Code != 200 {
		return exchange.OrderPage{}, lighterMalformed(product, "OpenOrders", "active orders response code is not 200")
	}
	orders := make([]exchange.Order, 0, len(res.Orders))
	for _, row := range res.Orders {
		if row.MarketIndex != meta.marketID {
			return exchange.OrderPage{}, lighterMalformed(product, "OpenOrders", "mixed order market")
		}
		if lighterOrderOutsideExchangeSubset(row) {
			continue
		}
		order, err := lighterOrder(row, meta)
		if err != nil {
			return exchange.OrderPage{}, lighterMalformed(product, "OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	return boundedOrderPage(orders, req.Limit, res.NextCursor), nil
}

func lighterFills(ctx context.Context, sdk *lighter.Client, product exchange.Product, marketType string, meta lighterMarketMeta, req exchange.FillsRequest) (exchange.FillPage, error) {
	if !req.End.IsZero() {
		return exchange.FillPage{}, lighterInvalid(product, "Fills", "Lighter fills does not support end time")
	}
	if req.Limit < 0 {
		return exchange.FillPage{}, lighterInvalid(product, "Fills", "limit must be non-negative")
	}
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	from := req.Start.UnixMilli()
	if req.Start.IsZero() {
		from = 0
	}
	var orderIndex *int64
	if req.OrderID != "" {
		id, err := strconv.ParseInt(req.OrderID, 10, 64)
		if err != nil || id <= 0 {
			return exchange.FillPage{}, lighterInvalid(product, "Fills", "order id must be a positive numeric native id")
		}
		orderIndex = &id
	}
	res, err := sdk.GetTrades(ctx, lighter.TradesRequest{MarketID: &meta.marketID, MarketType: marketType, OrderIndex: orderIndex, Cursor: req.Cursor, From: from, Limit: int64(limit)})
	if err != nil {
		return exchange.FillPage{}, lighterNormalizeErr(product, "Fills", err)
	}
	if res.Code != 200 {
		return exchange.FillPage{}, lighterMalformed(product, "Fills", "trades response code is not 200")
	}
	fills := make([]exchange.Fill, 0, len(res.Trades))
	for _, row := range res.Trades {
		if row.MarketId != meta.marketID {
			return exchange.FillPage{}, lighterMalformed(product, "Fills", "mixed fill market")
		}
		fill, err := lighterFill(row, meta, sdk.AccountIndex)
		if err != nil {
			return exchange.FillPage{}, lighterMalformed(product, "Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Cursor: req.Cursor, Limit: limit, WindowStart: req.Start}}, nil
}

func lighterAccount(ctx context.Context, sdk *lighter.Client, product exchange.Product, operation string) (*lighter.Account, error) {
	if err := lighterReady(ctx, product, operation, sdk); err != nil {
		return nil, err
	}
	trackedCtx, tracker := lighterWithSendTracker(ctx)
	res, err := sdk.GetAccount(trackedCtx)
	if err != nil {
		return nil, lighterNormalizeErr(product, operation, err)
	}
	if res.Code != 200 || len(res.Accounts) != 1 || res.Accounts[0] == nil {
		return nil, lighterMalformed(product, operation, "account response must contain exactly one account")
	}
	account := res.Accounts[0]
	identity := tracker.accountIdentitySnapshot()
	if !identity.observed || (!identity.index.present && !identity.accountIndex.present) {
		return nil, lighterMalformed(product, operation, "account response missing configured account index")
	}
	if identity.index.present && (!identity.index.valid || identity.index.value != sdk.AccountIndex) {
		return nil, lighterMalformed(product, operation, "account index mismatch")
	}
	if identity.accountIndex.present && (!identity.accountIndex.valid || identity.accountIndex.value != sdk.AccountIndex) {
		return nil, lighterMalformed(product, operation, "account index mismatch")
	}
	return account, nil
}

func lighterSpotBalances(rows []*lighter.SpotAsset, product exchange.Product, operation string) ([]exchange.Balance, error) {
	out := make([]exchange.Balance, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Symbol == "" {
			return nil, lighterMalformed(product, operation, "invalid asset row")
		}
		available, err := lighterNonNegativeDecimal(row.Balance)
		if err != nil {
			return nil, lighterMalformed(product, operation, "invalid balance")
		}
		locked, err := lighterNonNegativeDecimal(row.LockedBalance)
		if err != nil {
			return nil, lighterMalformed(product, operation, "invalid locked balance")
		}
		total := available.Add(locked)
		out = append(out, exchange.Balance{Asset: row.Symbol, Available: available, Locked: locked, Total: total})
	}
	return out, nil
}

func lighterValidatePlace(product exchange.Product, meta lighterMarketMeta, req exchange.PlaceOrderRequest) (uint32, int64, int64, error) {
	if err := req.Validate(product); err != nil {
		return 0, 0, 0, errors.New("invalid normalized order request")
	}
	clientID, err := strconv.ParseInt(req.ClientOrderID, 10, 64)
	if err != nil || clientID <= 0 || clientID > lighterMaxClientOrderIndex || strconv.FormatInt(clientID, 10) != req.ClientOrderID {
		return 0, 0, 0, errors.New("client order id must be a positive uint48 decimal string")
	}
	price := uint32(0)
	if req.Type == exchange.OrderTypeLimit {
		price, err = lighterScaleUint32(req.LimitPrice, meta.priceScale)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("price must align to Lighter price tick: %w", err)
		}
	}
	qty, err := lighterScaleInt64(req.Quantity, meta.sizeScale)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("quantity must align to Lighter size tick: %w", err)
	}
	if product == exchange.ProductSpot && req.Quantity.LessThan(meta.instrument.MinQuantity) {
		return 0, 0, 0, errors.New("quantity is below minimum")
	}
	if product == exchange.ProductPerp && req.Quantity.LessThan(meta.instrument.MinQuantity) {
		return 0, 0, 0, errors.New("quantity is below minimum")
	}
	if req.Type == exchange.OrderTypeLimit && meta.instrument.MinNotional.Valid && req.Quantity.Mul(req.LimitPrice).LessThan(meta.instrument.MinNotional.Value) {
		return 0, 0, 0, errors.New("notional is below minimum")
	}
	return price, qty, clientID, nil
}

const lighterMaxClientOrderIndex int64 = 1<<48 - 1

func lighterValidateCancel(req exchange.CancelOrderRequest) (int64, error) {
	id, err := strconv.ParseInt(req.OrderID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != req.OrderID {
		return 0, errors.New("order id must be a positive numeric native id")
	}
	return id, nil
}

func lighterCommandAck(product exchange.Product, operation string, op exchange.OrderOperation, instrument, orderID, clientOrderID string, code int32, msg, txHash string, sdk *lighter.Client, tracker *lighterSendTracker) (exchange.OrderAcknowledgement, error) {
	txHash = strings.TrimSpace(txHash)
	ack := exchange.OrderAcknowledgement{Venue: lighterVenue, Product: product, Operation: op, Instrument: instrument, OrderID: orderID, ClientOrderID: clientOrderID, VenueCode: strconv.FormatInt(int64(code), 10), TransactionHash: txHash}
	if code == 200 && txHash != "" {
		ack.State = exchange.AckAcceptedPending
		return ack, ack.Validate()
	}
	if code >= 500 {
		ack.State = exchange.AckAmbiguous
		if err := ack.Validate(); err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "order command outcome is unknown after possible send"})
	}
	if code <= 0 {
		return exchange.OrderAcknowledgement{}, lighterMalformed(product, operation, "Lighter command response code is invalid")
	}
	if code == 200 {
		return exchange.OrderAcknowledgement{}, lighterMalformed(product, operation, "accepted Lighter command response missing transaction hash")
	}
	ack.State = exchange.AckRejected
	ack.VenueMessage = "Lighter rejected order command"
	if sdk != nil {
		sdk.InvalidateNonce()
	}
	if err := ack.Validate(); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "Lighter rejected order command"})
}

func lighterCommandErr(product exchange.Product, op exchange.OrderOperation, instrument, orderID, clientOrderID string, err error, sdk *lighter.Client, tracker *lighterSendTracker) (exchange.OrderAcknowledgement, error) {
	ack := exchange.OrderAcknowledgement{Venue: lighterVenue, Product: product, Operation: op, State: exchange.AckAmbiguous, Instrument: instrument, OrderID: orderID, ClientOrderID: clientOrderID}
	operation := lighterCommandOperation(op)
	began, status := false, 0
	if tracker != nil {
		began, status = tracker.snapshot()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		if began {
			return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
		}
		return exchange.OrderAcknowledgement{}, lighterContextErr(product, operation, err)
	}
	if status == http.StatusTooManyRequests {
		if began && sdk != nil {
			sdk.InvalidateNonce()
		}
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(status), SafeMessage: "Lighter rate limit"})
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if began && sdk != nil {
			sdk.InvalidateNonce()
		}
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(status), SafeMessage: "Lighter authentication failed"})
	}
	if status == http.StatusNotFound {
		if began && sdk != nil {
			sdk.InvalidateNonce()
		}
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(status), SafeMessage: "Lighter resource not found"})
	}
	if status >= http.StatusInternalServerError {
		if began {
			return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(status), SafeMessage: "order command outcome is unknown after possible send"})
		}
		return exchange.OrderAcknowledgement{}, lighterNormalizeErr(product, operation, err)
	}
	if status >= http.StatusBadRequest {
		if !began {
			return exchange.OrderAcknowledgement{}, lighterNormalizeErr(product, operation, err)
		}
		ack.State = exchange.AckRejected
		ack.VenueCode = strconv.Itoa(status)
		ack.VenueMessage = "Lighter rejected order command"
		if sdk != nil {
			sdk.InvalidateNonce()
		}
		if err := ack.Validate(); err != nil {
			return exchange.OrderAcknowledgement{}, err
		}
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "Lighter rejected order command"})
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "Lighter rate limit"})
	}
	var apiErr *lighter.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code <= 0 {
			return exchange.OrderAcknowledgement{}, lighterMalformed(product, operation, "Lighter API error code is invalid")
		}
		if apiErr.Code == http.StatusTooManyRequests {
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Lighter rate limit"})
		}
		if apiErr.Code == 401 || apiErr.Code == 403 {
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Lighter authentication failed"})
		}
		if apiErr.Code >= 500 {
			return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "order command outcome is unknown after possible send"})
		}
		ack.State = exchange.AckRejected
		ack.VenueCode = strconv.Itoa(apiErr.Code)
		ack.VenueMessage = "Lighter rejected order command"
		if sdk != nil {
			sdk.InvalidateNonce()
		}
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "Lighter rejected order command"})
	}
	if !began {
		return exchange.OrderAcknowledgement{}, lighterNormalizeErr(product, operation, err)
	}
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
}

func lighterCommandOperation(operation exchange.OrderOperation) string {
	switch operation {
	case exchange.OrderOperationPlace:
		return "PlaceOrder"
	case exchange.OrderOperationCancel:
		return "CancelOrder"
	default:
		return string(operation)
	}
}

func lighterOrder(row *lighter.Order, meta lighterMarketMeta) (exchange.Order, error) {
	if row == nil {
		return exchange.Order{}, errors.New("nil order row")
	}
	qty, err := lighterNonNegativeDecimal(row.InitialBaseAmount)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := lighterNonNegativeDecimal(row.FilledBaseAmount)
	if err != nil {
		return exchange.Order{}, err
	}
	if filled.GreaterThan(qty) {
		return exchange.Order{}, errors.New("filled amount exceeds quantity")
	}
	price, err := lighterNonNegativeDecimal(row.Price)
	if err != nil {
		return exchange.Order{}, err
	}
	if row.OrderIndex <= 0 {
		return exchange.Order{}, errors.New("order id must be positive")
	}
	orderType := exchange.OrderTypeLimit
	limitPolicy := exchange.LimitPolicyResting
	switch row.OrderType {
	case lighter.OrderTypeRespMarket:
		orderType = exchange.OrderTypeMarket
		limitPolicy = ""
	case lighter.OrderTypeRespLimit, "":
		switch strings.ToLower(row.TimeInForce) {
		case "immediate-or-cancel", "ioc":
			limitPolicy = exchange.LimitPolicyIOC
		case "post-only", "post_only":
			limitPolicy = exchange.LimitPolicyPostOnly
		default:
			if row.TimeInForce != "" && !lighterIsGTCCompatible(row.TimeInForce) {
				return exchange.Order{}, errors.New("unsupported time in force")
			}
		}
	default:
		return exchange.Order{}, errors.New("unsupported order type")
	}
	if lighterOrderOutsideExchangeSubset(row) {
		return exchange.Order{}, errors.New("trigger active order is outside exchange subset")
	}
	created, err := lighterUnixMillisStrict(row.CreatedAt)
	if err != nil {
		return exchange.Order{}, err
	}
	updated, err := lighterUnixMillisStrict(row.UpdatedAt)
	if err != nil {
		return exchange.Order{}, err
	}
	side := exchange.SideBuy
	if row.IsAsk {
		side = exchange.SideSell
	}
	return exchange.Order{Instrument: meta.instrument.Symbol, OrderID: strconv.FormatInt(row.OrderIndex, 10), ClientOrderID: lighterOrderClientID(row.ClientOrderIndex), Side: side, Type: orderType, Quantity: qty, LimitPrice: price, LimitPolicy: limitPolicy, ReduceOnly: row.ReduceOnly, Filled: filled, Status: string(row.Status), CreatedAt: created, UpdatedAt: updated}, nil
}

func lighterOrderOutsideExchangeSubset(row *lighter.Order) bool {
	if row == nil {
		return false
	}
	raw := strings.TrimSpace(row.TriggerPrice)
	if raw == "" {
		return false
	}
	trigger, err := decimal.NewFromString(raw)
	return err != nil || !trigger.IsZero()
}

func lighterFill(row lighter.Trade, meta lighterMarketMeta, accountID int64) (exchange.Fill, error) {
	if row.TradeId <= 0 || row.Timestamp <= 0 {
		return exchange.Fill{}, errors.New("fill id and time must be positive")
	}
	if row.AskAccountId == accountID && row.BidAccountId == accountID {
		return exchange.Fill{}, errors.New("configured account appears on both sides")
	}
	qty, err := lighterNonNegativeDecimal(row.Size)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := lighterNonNegativeDecimal(row.Price)
	if err != nil {
		return exchange.Fill{}, err
	}
	side := exchange.SideBuy
	orderID := row.BidId
	clientID := row.BidClientId
	fee := lighterFee(row.TakerFee, meta)
	liq := exchange.LiquidityTaker
	if row.AskAccountId == accountID {
		side = exchange.SideSell
		orderID = row.AskId
		clientID = row.AskClientId
		fee = lighterFee(row.MakerFee, meta)
		liq = exchange.LiquidityMaker
		if !row.IsMakerAsk {
			liq = exchange.LiquidityTaker
			fee = lighterFee(row.TakerFee, meta)
		}
	} else if row.BidAccountId == accountID {
		if !row.IsMakerAsk {
			liq = exchange.LiquidityMaker
			fee = lighterFee(row.MakerFee, meta)
		}
	} else {
		return exchange.Fill{}, errors.New("fill does not belong to configured account")
	}
	if orderID <= 0 {
		return exchange.Fill{}, errors.New("fill order id must be positive")
	}
	return exchange.Fill{Instrument: meta.instrument.Symbol, OrderID: strconv.FormatInt(orderID, 10), ClientOrderID: lighterOrderClientID(clientID), FillID: strconv.FormatInt(row.TradeId, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: "USDC", Liquidity: liq, Time: lighterUnixMillis(row.Timestamp)}, nil
}

func lighterPosition(row *lighter.Position, meta lighterMarketMeta) (exchange.Position, error) {
	if row.Sign < -1 || row.Sign > 1 {
		return exchange.Position{}, errors.New("position sign must be -1, 0, or 1")
	}
	qty, err := lighterNonNegativeDecimal(row.Position)
	if err != nil {
		return exchange.Position{}, err
	}
	side := exchange.SideBuy
	if row.Sign < 0 {
		side = exchange.SideSell
		qty = qty.Neg()
	}
	entry, err := lighterNonNegativeDecimal(row.AvgEntryPrice)
	if err != nil {
		return exchange.Position{}, err
	}
	pnl, err := lighterDecimal(row.UnrealizedPnl)
	if err != nil {
		return exchange.Position{}, err
	}
	liq, err := lighterOptional(row.LiquidationPrice)
	if err != nil {
		return exchange.Position{}, err
	}
	margin, err := lighterOptional(row.AllocatedMargin)
	if err != nil {
		return exchange.Position{}, err
	}
	value, err := lighterNonNegativeDecimal(row.PositionValue)
	if err != nil {
		return exchange.Position{}, err
	}
	mark := decimal.Zero
	if !qty.IsZero() {
		mark = value.Div(qty.Abs())
	}
	return exchange.Position{Instrument: meta.instrument.Symbol, Side: side, Quantity: qty, EntryPrice: entry, MarkPrice: mark, UnrealizedPnL: pnl, LiquidationPrice: liq, MarginUsed: margin}, nil
}

func lighterBidLevels(rows []lighter.Bid) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, err := lighterPositiveDecimal(row.Price)
		if err != nil {
			return nil, err
		}
		qty, err := lighterPositiveDecimal(row.RemainingBaseAmount)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

func lighterAskLevels(rows []lighter.Ask) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, err := lighterPositiveDecimal(row.Price)
		if err != nil {
			return nil, err
		}
		qty, err := lighterPositiveDecimal(row.RemainingBaseAmount)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

const lighterMarketProtectionBPS int64 = 50

func lighterMarketProtectionPrice(
	ctx context.Context,
	sdk *lighter.Client,
	product exchange.Product,
	meta lighterMarketMeta,
	side exchange.Side,
) (uint32, error) {
	res, err := sdk.GetOrderBookOrders(ctx, meta.marketID, 1)
	if err != nil {
		return 0, lighterNormalizeErr(product, "PlaceOrder", err)
	}
	if res.Code != 200 {
		return 0, lighterMalformed(product, "PlaceOrder", "order book response code is not 200")
	}

	var raw string
	switch side {
	case exchange.SideBuy:
		if len(res.Asks) == 0 {
			return 0, lighterMalformed(product, "PlaceOrder", "market buy requires a best ask")
		}
		raw = res.Asks[0].Price
	case exchange.SideSell:
		if len(res.Bids) == 0 {
			return 0, lighterMalformed(product, "PlaceOrder", "market sell requires a best bid")
		}
		raw = res.Bids[0].Price
	default:
		return 0, lighterInvalid(product, "PlaceOrder", "side must be buy or sell")
	}

	reference, err := lighterPositiveDecimal(raw)
	if err != nil {
		return 0, lighterMalformed(product, "PlaceOrder", "invalid market protection reference price")
	}
	bps := decimal.NewFromInt(10_000)
	factor := bps.Sub(decimal.NewFromInt(lighterMarketProtectionBPS))
	if side == exchange.SideBuy {
		factor = bps.Add(decimal.NewFromInt(lighterMarketProtectionBPS))
	}
	scaled := reference.Mul(factor).Div(bps).Mul(meta.priceScale)
	if side == exchange.SideBuy {
		scaled = scaled.Ceil()
	} else {
		scaled = scaled.Floor()
	}
	if !scaled.IsPositive() || scaled.GreaterThan(decimal.NewFromInt(math.MaxUint32)) {
		return 0, lighterMalformed(product, "PlaceOrder", "invalid market protection price")
	}
	return uint32(scaled.IntPart()), nil
}

func lighterScaleUint32(value, scale decimal.Decimal) (uint32, error) {
	scaled := value.Mul(scale)
	if !scaled.Equal(scaled.Truncate(0)) || scaled.IsNegative() || scaled.GreaterThan(decimal.NewFromInt(math.MaxUint32)) {
		return 0, errors.New("not an exact uint32 tick")
	}
	return uint32(scaled.IntPart()), nil
}

func lighterScaleInt64(value, scale decimal.Decimal) (int64, error) {
	scaled := value.Mul(scale)
	if !scaled.Equal(scaled.Truncate(0)) || !scaled.IsPositive() || scaled.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, errors.New("not an exact positive int64 tick")
	}
	return scaled.IntPart(), nil
}

func lighterPositiveDecimal(raw string) (decimal.Decimal, error) {
	value, err := lighterNonNegativeDecimal(raw)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if !value.IsPositive() {
		return decimal.Decimal{}, errors.New("decimal must be positive")
	}
	return value, nil
}

func lighterNonNegativeDecimal(raw string) (decimal.Decimal, error) {
	value, err := lighterDecimal(raw)
	if err != nil {
		return decimal.Decimal{}, err
	}
	if value.IsNegative() {
		return decimal.Decimal{}, errors.New("decimal must be non-negative")
	}
	return value, nil
}

func lighterDecimal(raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(raw)
}

func lighterOptional(raw string) (exchange.OptionalDecimal, error) {
	if strings.TrimSpace(raw) == "" {
		return exchange.OptionalDecimal{}, nil
	}
	value, err := lighterDecimal(raw)
	if err != nil {
		return exchange.OptionalDecimal{}, err
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}, nil
}

func lighterOrderClientID(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func lighterUnixMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	if ms > 1_000_000_000_000_000 {
		return time.UnixMicro(ms).UTC()
	}
	return time.UnixMilli(ms).UTC()
}

func lighterUnixMillisStrict(ms int64) (time.Time, error) {
	if ms <= 0 {
		return time.Time{}, errors.New("timestamp must be positive")
	}
	return lighterUnixMillis(ms), nil
}

func lighterAssets(symbol string, product exchange.Product) (string, string) {
	if product == exchange.ProductSpot {
		for _, sep := range []string{"-", "/"} {
			parts := strings.Split(symbol, sep)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				return parts[0], parts[1]
			}
		}
	}
	return symbol, "USDC"
}

func lighterFee(ticks int32, meta lighterMarketMeta) decimal.Decimal {
	return decimal.NewFromInt(int64(ticks)).Div(meta.quoteScale)
}

func lighterIsGTCCompatible(raw string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(raw), "_", "-"), " ", "-"))
	switch normalized {
	case "gtc", "gtt", "good-till-time", "good-till-cancel", "good-til-cancel", "good-till-canceled", "good-til-canceled":
		return true
	default:
		return false
	}
}

func lighterFinite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func lighterInterval(interval string) (time.Duration, error) {
	switch interval {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	default:
		return 0, errors.New("unsupported interval")
	}
}

func lighterReady(ctx context.Context, product exchange.Product, operation string, sdk *lighter.Client) error {
	if ctx == nil {
		return lighterInvalid(product, operation, "context is required")
	}
	if sdk == nil {
		return lighterInvalid(product, operation, "client is not initialized")
	}
	select {
	case <-ctx.Done():
		return lighterContextErr(product, operation, ctx.Err())
	default:
		return nil
	}
}

func lighterNormalizeErr(product exchange.Product, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return lighterContextErr(product, operation, err)
	}
	if errors.Is(err, lighter.ErrMalformedResponse) {
		return lighterMalformed(product, operation, "Lighter response body is malformed")
	}
	var statusErr *lighterHTTPStatusError
	if errors.As(err, &statusErr) {
		return lighterNormalizeStatus(product, operation, statusErr.status)
	}
	var apiErr *lighter.APIError
	if errors.As(err, &apiErr) {
		return lighterNormalizeStatus(product, operation, apiErr.Code)
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &syntaxErr) || errors.As(err, &typeErr) {
		return lighterMalformed(product, operation, "Lighter response body is malformed")
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "Lighter rate limit"})
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "http error 429") || strings.Contains(msg, "rate limit"):
		return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "Lighter rate limit"})
	case strings.Contains(msg, "http error 401") || strings.Contains(msg, "http error 403") || strings.Contains(msg, "credentials required") || strings.Contains(msg, "authentication") || strings.Contains(msg, "authorization"):
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "Lighter authentication failed"})
	case strings.Contains(msg, "http error 404") || strings.Contains(msg, "not found"):
		return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "Lighter resource not found"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: "Lighter transport error"})
}

func lighterNormalizeStatus(product exchange.Product, operation string, status int) error {
	details := exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, Code: strconv.Itoa(status)}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		details.SafeMessage = "Lighter authentication failed"
		return exchange.NewError(exchange.KindAuthentication, details)
	case http.StatusNotFound:
		details.SafeMessage = "Lighter resource not found"
		return exchange.NewError(exchange.KindNotFound, details)
	case http.StatusTooManyRequests:
		details.SafeMessage = "Lighter rate limit"
		return exchange.NewError(exchange.KindRateLimit, details)
	default:
		switch {
		case status >= http.StatusInternalServerError:
			details.SafeMessage = "Lighter transport error"
			return exchange.NewError(exchange.KindTransport, details)
		case status >= http.StatusBadRequest:
			details.SafeMessage = "Lighter request was rejected"
			return exchange.NewError(exchange.KindInvalidRequest, details)
		default:
			return lighterMalformed(product, operation, "Lighter response code is malformed")
		}
	}
}

func lighterInvalid(product exchange.Product, operation, msg string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: msg})
}

func lighterMalformed(product exchange.Product, operation, msg string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: msg})
}

func lighterContextErr(product exchange.Product, operation string, err error) error {
	kind := exchange.KindCanceled
	message := "context canceled"
	if errors.Is(err, context.DeadlineExceeded) {
		kind = exchange.KindDeadlineExceeded
		message = "context deadline exceeded"
	}
	return exchange.NewError(kind, exchange.ErrorDetails{Venue: lighterVenue, Product: product, Operation: operation, SafeMessage: message})
}
