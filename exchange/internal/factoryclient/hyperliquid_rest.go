package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
	"github.com/shopspring/decimal"
)

const (
	hyperliquidVenue            = exchange.VenueHyperliquid
	hyperliquidOutcomeAssetBase = int64(100_000_000)
	hyperliquidSpotAssetOffset  = int64(10_000)
)

var (
	hyperliquidMinNotional = decimal.NewFromInt(10)
)

type hyperliquidMarketMeta struct {
	instrument    exchange.Instrument
	nativeCoin    string
	assetID       int
	sizeDecimals  int
	priceDecimals int
	markPrice     decimal.Decimal
}

func (client *hyperliquidSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "Instruments"); err != nil {
		return nil, err
	}
	metas, err := client.spotMetas(ctx, "Instruments")
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

func (client *hyperliquidSpotClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	meta, err := client.spotMeta(ctx, "OrderBook", req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, hlInvalid(exchange.ProductSpot, "OrderBook", "limit must be non-negative")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	row, err := client.sdk.L2Book(requestCtx, meta.nativeCoin)
	if err != nil {
		return exchange.OrderBook{}, hlNormalizeQueryErr(exchange.ProductSpot, "OrderBook", err, tracker)
	}
	return hlBook(exchange.ProductSpot, "OrderBook", meta, req.Limit, row.Coin, row.Levels, row.Time)
}

func (client *hyperliquidSpotClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	meta, err := client.spotMeta(ctx, "Candles", req.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	start, end, err := hlCandleWindow(req)
	if err != nil {
		return exchange.CandlePage{}, hlInvalid(exchange.ProductSpot, "Candles", err.Error())
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.CandleSnapshot(requestCtx, meta.nativeCoin, req.Interval, start, end)
	if err != nil {
		return exchange.CandlePage{}, hlNormalizeQueryErr(exchange.ProductSpot, "Candles", err, tracker)
	}
	return hlSpotCandles(meta, req, rows)
}

func (client *hyperliquidSpotClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, hlInvalid(exchange.ProductSpot, "PlaceOrder", "invalid normalized order request")
	}
	meta, err := client.spotMeta(ctx, "PlaceOrder", req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	req, err = hlNormalizePlace(exchange.ProductSpot, req, meta)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		return exchange.OrderAcknowledgement{}, hlContextErr(exchange.ProductSpot, "PlaceOrder", err)
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	nativeClientID := hlNativeClientOrderID(req.ClientOrderID)
	var status *hyperliquidspot.OrderStatus
	if req.Type == exchange.OrderTypeMarket {
		status, err = client.sdk.PlaceMarketOrder(requestCtx, hyperliquidspot.MarketOrderRequest{
			Coin:          meta.nativeCoin,
			IsBuy:         req.Side == exchange.SideBuy,
			Size:          hlMustFloat(req.Quantity),
			ClientOrderID: hlOptionalString(nativeClientID),
		})
	} else {
		status, err = client.sdk.PlaceOrder(requestCtx, hyperliquidspot.PlaceOrderRequest{
			AssetID:       meta.assetID,
			IsBuy:         req.Side == exchange.SideBuy,
			Price:         hlMustFloat(req.LimitPrice),
			Size:          hlMustFloat(req.Quantity),
			OrderType:     hyperliquidspot.OrderType{Limit: &hyperliquidspot.OrderTypeLimit{Tif: hlLimitTIF(req.LimitPolicy)}},
			ClientOrderID: hlOptionalString(nativeClientID),
		})
	}
	if err != nil {
		return hlMutationErr(exchange.ProductSpot, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err, tracker)
	}
	ack, err := hlSpotPlaceAck(meta.instrument.Symbol, nativeClientID, status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack.ClientOrderID = req.ClientOrderID
	ack.OrderType = req.Type
	return ack, ack.Validate()
}

func (client *hyperliquidSpotClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	meta, oid, err := client.spotCancelMeta(ctx, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		return exchange.OrderAcknowledgement{}, hlContextErr(exchange.ProductSpot, "CancelOrder", err)
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	status, err := client.sdk.CancelOrder(requestCtx, hyperliquidspot.CancelOrderRequest{AssetID: meta.assetID, OrderID: oid})
	if err != nil {
		return hlMutationErr(exchange.ProductSpot, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err, tracker)
	}
	return hlCancelAck(exchange.ProductSpot, req.Instrument, req.OrderID, status)
}

func (client *hyperliquidSpotClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	if req.Limit < 0 || req.Cursor != "" {
		return exchange.OrderPage{}, hlInvalid(exchange.ProductSpot, "OpenOrders", "unsupported page request")
	}
	metas, requested, err := client.spotMetasForOptional(ctx, "OpenOrders", req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.UserOpenOrders(requestCtx, client.sdk.AccountAddr)
	if err != nil {
		return exchange.OrderPage{}, hlNormalizeQueryErr(exchange.ProductSpot, "OpenOrders", err, tracker)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		meta, ok := metas[row.Coin]
		if !ok || hlRejectCoin(row.Coin) {
			return exchange.OrderPage{}, hlMalformed(exchange.ProductSpot, "OpenOrders", "mixed or unknown product row")
		}
		if requested != nil && row.Coin != requested.nativeCoin {
			continue
		}
		order, err := hlOrder(meta, row.Coin, row.Side, row.LimitPx, row.Sz, row.OrigSz, row.Oid, row.Cliod, row.Timestamp, row.Timestamp, row.OrderType, row.Tif, row.ReduceOnly, row.IsTrigger)
		if err != nil {
			return exchange.OrderPage{}, hlMalformed(exchange.ProductSpot, "OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	if req.Limit > 0 && len(orders) > req.Limit {
		orders = orders[:req.Limit]
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *hyperliquidSpotClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	if err := hlValidateFillsRequest(exchange.ProductSpot, req); err != nil {
		return exchange.FillPage{}, err
	}
	if err := hlValidateOptionalOrderID(exchange.ProductSpot, "Fills", req.OrderID); err != nil {
		return exchange.FillPage{}, err
	}
	metas, requested, err := client.spotMetasForOptional(ctx, "Fills", req.Instrument)
	if err != nil {
		return exchange.FillPage{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.UserFills(requestCtx, client.sdk.AccountAddr)
	if err != nil {
		return exchange.FillPage{}, hlNormalizeQueryErr(exchange.ProductSpot, "Fills", err, tracker)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if requested != nil && row.Coin != requested.nativeCoin {
			continue
		}
		meta, ok := metas[row.Coin]
		if !ok || hlRejectCoin(row.Coin) {
			return exchange.FillPage{}, hlMalformed(exchange.ProductSpot, "Fills", "mixed or unknown product row")
		}
		if req.OrderID != "" && strconv.FormatInt(row.Oid, 10) != req.OrderID {
			continue
		}
		fill, err := hlFill(meta, row.Coin, row.Side, row.Px, row.Sz, row.Fee, row.FeeToken, row.Oid, row.Tid, row.Hash, row.Crossed, row.Time)
		if err != nil {
			return exchange.FillPage{}, hlMalformed(exchange.ProductSpot, "Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	if req.Limit > 0 && len(fills) > req.Limit {
		fills = fills[:req.Limit]
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *hyperliquidSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "Balances"); err != nil {
		return nil, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	state, err := client.sdk.GetSpotClearinghouseState(requestCtx, client.sdk.AccountAddr)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductSpot, "Balances", err, tracker)
	}
	return hlSpotBalances("Balances", state)
}

func (client *hyperliquidSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	if err := hlRequireCtx(ctx, exchange.ProductSpot, "SpotAccount"); err != nil {
		return exchange.SpotAccount{}, err
	}
	balances, err := client.Balances(ctx)
	if err != nil {
		return exchange.SpotAccount{}, withExchangeOperation(err, "SpotAccount")
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *hyperliquidPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "Instruments"); err != nil {
		return nil, err
	}
	metas, err := client.perpMetas(ctx, "Instruments")
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

func (client *hyperliquidPerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	meta, err := client.perpMeta(ctx, "OrderBook", req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, hlInvalid(exchange.ProductPerp, "OrderBook", "limit must be non-negative")
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	row, err := client.sdk.L2Book(requestCtx, meta.nativeCoin)
	if err != nil {
		return exchange.OrderBook{}, hlNormalizeQueryErr(exchange.ProductPerp, "OrderBook", err, tracker)
	}
	return hlBook(exchange.ProductPerp, "OrderBook", meta, req.Limit, row.Coin, row.Levels, row.Time)
}

func (client *hyperliquidPerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	meta, err := client.perpMeta(ctx, "Candles", req.Instrument)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	start, end, err := hlCandleWindow(req)
	if err != nil {
		return exchange.CandlePage{}, hlInvalid(exchange.ProductPerp, "Candles", err.Error())
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.CandleSnapshot(requestCtx, meta.nativeCoin, req.Interval, start, end)
	if err != nil {
		return exchange.CandlePage{}, hlNormalizeQueryErr(exchange.ProductPerp, "Candles", err, tracker)
	}
	return hlPerpCandles(meta, req, rows)
}

func (client *hyperliquidPerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, hlInvalid(exchange.ProductPerp, "PlaceOrder", "invalid normalized order request")
	}
	meta, err := client.perpMeta(ctx, "PlaceOrder", req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	req, err = hlNormalizePlace(exchange.ProductPerp, req, meta)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		return exchange.OrderAcknowledgement{}, hlContextErr(exchange.ProductPerp, "PlaceOrder", err)
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	nativeClientID := hlNativeClientOrderID(req.ClientOrderID)
	var status *hyperliquidperp.OrderStatus
	if req.Type == exchange.OrderTypeMarket {
		status, err = client.sdk.PlaceMarketOrder(requestCtx, hyperliquidperp.MarketOrderRequest{
			Coin:          meta.nativeCoin,
			IsBuy:         req.Side == exchange.SideBuy,
			Size:          hlMustFloat(req.Quantity),
			ReduceOnly:    req.ReduceOnly,
			ClientOrderID: hlOptionalString(nativeClientID),
		})
	} else {
		status, err = client.sdk.PlaceOrder(requestCtx, hyperliquidperp.PlaceOrderRequest{
			AssetID:       meta.assetID,
			IsBuy:         req.Side == exchange.SideBuy,
			Price:         hlMustFloat(req.LimitPrice),
			Size:          hlMustFloat(req.Quantity),
			ReduceOnly:    req.ReduceOnly,
			OrderType:     hyperliquidperp.OrderType{Limit: &hyperliquidperp.OrderTypeLimit{Tif: hlLimitTIF(req.LimitPolicy)}},
			ClientOrderID: hlOptionalString(nativeClientID),
		})
	}
	if err != nil {
		return hlMutationErr(exchange.ProductPerp, exchange.OrderOperationPlace, req.Instrument, "", req.ClientOrderID, err, tracker)
	}
	ack, err := hlPerpPlaceAck(meta.instrument.Symbol, nativeClientID, status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack.ClientOrderID = req.ClientOrderID
	ack.OrderType = req.Type
	return ack, ack.Validate()
}

func (client *hyperliquidPerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	meta, oid, err := client.perpCancelMeta(ctx, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := ctx.Err(); err != nil {
		return exchange.OrderAcknowledgement{}, hlContextErr(exchange.ProductPerp, "CancelOrder", err)
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	status, err := client.sdk.CancelOrder(requestCtx, hyperliquidperp.CancelOrderRequest{AssetID: meta.assetID, OrderID: oid})
	if err != nil {
		return hlMutationErr(exchange.ProductPerp, exchange.OrderOperationCancel, req.Instrument, req.OrderID, "", err, tracker)
	}
	return hlCancelAck(exchange.ProductPerp, req.Instrument, req.OrderID, status)
}

func (client *hyperliquidPerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	if req.Limit < 0 || req.Cursor != "" {
		return exchange.OrderPage{}, hlInvalid(exchange.ProductPerp, "OpenOrders", "unsupported page request")
	}
	metas, requested, err := client.perpMetasForOptional(ctx, "OpenOrders", req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.UserOpenOrders(requestCtx, client.sdk.AccountAddr)
	if err != nil {
		return exchange.OrderPage{}, hlNormalizeQueryErr(exchange.ProductPerp, "OpenOrders", err, tracker)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		meta, ok := metas[row.Coin]
		if !ok || hlRejectCoin(row.Coin) {
			return exchange.OrderPage{}, hlMalformed(exchange.ProductPerp, "OpenOrders", "mixed or unknown product row")
		}
		if requested != nil && row.Coin != requested.nativeCoin {
			continue
		}
		order, err := hlOrder(meta, row.Coin, row.Side, row.LimitPx, row.Sz, row.OrigSz, row.Oid, row.Cliod, row.Timestamp, row.Timestamp, row.OrderType, row.Tif, row.ReduceOnly, row.IsTrigger)
		if err != nil {
			return exchange.OrderPage{}, hlMalformed(exchange.ProductPerp, "OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	if req.Limit > 0 && len(orders) > req.Limit {
		orders = orders[:req.Limit]
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *hyperliquidPerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	if err := hlValidateFillsRequest(exchange.ProductPerp, req); err != nil {
		return exchange.FillPage{}, err
	}
	if err := hlValidateOptionalOrderID(exchange.ProductPerp, "Fills", req.OrderID); err != nil {
		return exchange.FillPage{}, err
	}
	metas, requested, err := client.perpMetasForOptional(ctx, "Fills", req.Instrument)
	if err != nil {
		return exchange.FillPage{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	rows, err := client.sdk.UserFills(requestCtx, client.sdk.AccountAddr)
	if err != nil {
		return exchange.FillPage{}, hlNormalizeQueryErr(exchange.ProductPerp, "Fills", err, tracker)
	}
	fills := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if requested != nil && row.Coin != requested.nativeCoin {
			continue
		}
		meta, ok := metas[row.Coin]
		if !ok || hlRejectCoin(row.Coin) {
			return exchange.FillPage{}, hlMalformed(exchange.ProductPerp, "Fills", "mixed or unknown product row")
		}
		if req.OrderID != "" && strconv.FormatInt(row.Oid, 10) != req.OrderID {
			continue
		}
		fill, err := hlFill(meta, row.Coin, row.Side, row.Px, row.Sz, row.Fee, row.FeeToken, row.Oid, row.Tid, row.Hash, row.Crossed, row.Time)
		if err != nil {
			return exchange.FillPage{}, hlMalformed(exchange.ProductPerp, "Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	if req.Limit > 0 && len(fills) > req.Limit {
		fills = fills[:req.Limit]
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *hyperliquidPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "Balances"); err != nil {
		return nil, err
	}
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *hyperliquidPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	state, err := client.sdk.GetBalance(requestCtx)
	if err != nil {
		return exchange.PerpAccount{}, hlNormalizeQueryErr(exchange.ProductPerp, "PerpAccount", err, tracker)
	}
	account := exchange.PerpAccount{Balances: []exchange.Balance{}}
	if strings.TrimSpace(state.MarginSummary.AccountValue) != "" {
		value, err := hlDecimal(state.MarginSummary.AccountValue)
		if err != nil {
			return exchange.PerpAccount{}, hlMalformed(exchange.ProductPerp, "PerpAccount", "invalid account value")
		}
		account.Equity = exchange.OptionalDecimal{Value: value, Valid: true}
	}
	var available decimal.Decimal
	if strings.TrimSpace(state.Withdrawable) != "" {
		value, err := hlDecimal(state.Withdrawable)
		if err != nil {
			return exchange.PerpAccount{}, hlMalformed(exchange.ProductPerp, "PerpAccount", "invalid withdrawable")
		}
		available = value
		account.Available = exchange.OptionalDecimal{Value: value, Valid: true}
	}
	if strings.TrimSpace(state.MarginSummary.TotalMarginUsed) != "" {
		value, err := hlDecimal(state.MarginSummary.TotalMarginUsed)
		if err != nil {
			return exchange.PerpAccount{}, hlMalformed(exchange.ProductPerp, "PerpAccount", "invalid margin used")
		}
		account.MarginUsed = exchange.OptionalDecimal{Value: value, Valid: true}
	}
	if account.Equity.Valid && account.Available.Valid {
		locked := account.Equity.Value.Sub(available)
		if locked.IsNegative() {
			return exchange.PerpAccount{}, hlMalformed(exchange.ProductPerp, "PerpAccount", "withdrawable exceeds account value")
		}
		account.Balances = append(account.Balances, exchange.Balance{Asset: "USDC", Available: available, Locked: locked, Total: account.Equity.Value})
	}
	return account, nil
}

func (client *hyperliquidPerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := hlRequireCtx(ctx, exchange.ProductPerp, "Positions"); err != nil {
		return nil, err
	}
	metas, requested, err := client.perpMetasForOptional(ctx, "Positions", req.Instrument)
	if err != nil {
		return nil, err
	}
	requestCtx, tracker := hyperliquidWithRequestTracker(ctx)
	state, err := client.sdk.GetPerpPosition(requestCtx)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductPerp, "Positions", err, tracker)
	}
	positions := make([]exchange.Position, 0, len(state.AssetPositions))
	for _, row := range state.AssetPositions {
		coin := row.Position.Coin
		meta, ok := metas[coin]
		if !ok || hlRejectCoin(coin) {
			return nil, hlMalformed(exchange.ProductPerp, "Positions", "mixed or unknown product row")
		}
		if requested != nil && coin != requested.nativeCoin {
			continue
		}
		pos, err := hlPosition(meta, row.Position.Szi, row.Position.EntryPx, row.Position.UnrealizedPnl, row.Position.LiquidationPx, row.Position.MarginUsed, row.Position.Leverage.Value)
		if err != nil {
			return nil, hlMalformed(exchange.ProductPerp, "Positions", err.Error())
		}
		if !pos.Quantity.IsZero() {
			positions = append(positions, pos)
		}
	}
	return positions, nil
}

func (client *hyperliquidSpotClient) spotMetas(ctx context.Context, operation string) (map[string]hyperliquidMarketMeta, error) {
	return hlCachedMetas(ctx, operation, exchange.ProductSpot, &client.cacheMu, &client.loading, &client.metadata, func(loadCtx context.Context) (map[string]hyperliquidMarketMeta, error) {
		requestCtx, tracker := hyperliquidWithRequestTracker(loadCtx)
		meta, err := client.sdk.GetSpotMeta(requestCtx)
		if err != nil {
			return nil, hlNormalizeQueryErr(exchange.ProductSpot, operation, err, tracker)
		}
		return hlSpotMetas(operation, meta)
	})
}

func (client *hyperliquidPerpClient) perpMetas(ctx context.Context, operation string) (map[string]hyperliquidMarketMeta, error) {
	return hlCachedMetas(ctx, operation, exchange.ProductPerp, &client.cacheMu, &client.loading, &client.metadata, func(loadCtx context.Context) (map[string]hyperliquidMarketMeta, error) {
		fullCtx, fullTracker := hyperliquidWithRequestTracker(loadCtx)
		full, err := client.sdk.GetMetaAndAssetCtxs(fullCtx)
		if err != nil {
			return nil, hlNormalizeQueryErr(exchange.ProductPerp, operation, err, fullTracker)
		}
		prepCtx, prepTracker := hyperliquidWithRequestTracker(loadCtx)
		prep, err := client.sdk.GetPrepMeta(prepCtx)
		if err != nil {
			return nil, hlNormalizeQueryErr(exchange.ProductPerp, operation, err, prepTracker)
		}
		return hlPerpMetas(operation, full, prep)
	})
}

func hlCachedMetas(ctx context.Context, operation string, product exchange.Product, mu *sync.Mutex, loading *chan struct{}, cache *map[string]hyperliquidMarketMeta, load func(context.Context) (map[string]hyperliquidMarketMeta, error)) (map[string]hyperliquidMarketMeta, error) {
	for {
		mu.Lock()
		if *cache != nil {
			out := *cache
			mu.Unlock()
			return out, nil
		}
		if *loading == nil {
			ch := make(chan struct{})
			*loading = ch
			mu.Unlock()
			metas, err := load(ctx)
			mu.Lock()
			if err == nil {
				*cache = metas
			}
			if *loading == ch {
				*loading = nil
				close(ch)
			}
			mu.Unlock()
			if err != nil {
				return nil, err
			}
			return metas, nil
		}
		ch := *loading
		mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, hlContextErr(product, operation, ctx.Err())
		}
	}
}

func (client *hyperliquidSpotClient) spotMeta(ctx context.Context, operation, instrument string) (hyperliquidMarketMeta, error) {
	metas, _, err := client.spotMetasForOptional(ctx, operation, instrument)
	if err != nil {
		return hyperliquidMarketMeta{}, err
	}
	for _, meta := range metas {
		if meta.instrument.Symbol == instrument {
			return meta, nil
		}
	}
	return hyperliquidMarketMeta{}, hlInvalid(exchange.ProductSpot, operation, "instrument is not present in Hyperliquid spot metadata")
}

func (client *hyperliquidPerpClient) perpMeta(ctx context.Context, operation, instrument string) (hyperliquidMarketMeta, error) {
	metas, _, err := client.perpMetasForOptional(ctx, operation, instrument)
	if err != nil {
		return hyperliquidMarketMeta{}, err
	}
	for _, meta := range metas {
		if meta.instrument.Symbol == instrument {
			return meta, nil
		}
	}
	return hyperliquidMarketMeta{}, hlInvalid(exchange.ProductPerp, operation, "instrument is not present in Hyperliquid perp metadata")
}

func (client *hyperliquidSpotClient) spotMetasForOptional(ctx context.Context, operation, instrument string) (map[string]hyperliquidMarketMeta, *hyperliquidMarketMeta, error) {
	metas, err := client.spotMetas(ctx, operation)
	if err != nil {
		return nil, nil, err
	}
	return hlOptionalMeta(exchange.ProductSpot, operation, metas, instrument)
}

func (client *hyperliquidPerpClient) perpMetasForOptional(ctx context.Context, operation, instrument string) (map[string]hyperliquidMarketMeta, *hyperliquidMarketMeta, error) {
	metas, err := client.perpMetas(ctx, operation)
	if err != nil {
		return nil, nil, err
	}
	return hlOptionalMeta(exchange.ProductPerp, operation, metas, instrument)
}

func hlOptionalMeta(product exchange.Product, operation string, metas map[string]hyperliquidMarketMeta, instrument string) (map[string]hyperliquidMarketMeta, *hyperliquidMarketMeta, error) {
	if strings.TrimSpace(instrument) == "" {
		return metas, nil, nil
	}
	for _, meta := range metas {
		if meta.instrument.Symbol == instrument {
			copyMeta := meta
			return metas, &copyMeta, nil
		}
	}
	return nil, nil, hlInvalid(product, operation, "instrument is not present in Hyperliquid metadata")
}

func (client *hyperliquidSpotClient) spotCancelMeta(ctx context.Context, req exchange.CancelOrderRequest) (hyperliquidMarketMeta, int64, error) {
	oid, err := hlValidateCancel(exchange.ProductSpot, req)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	meta, err := client.spotMeta(ctx, "CancelOrder", req.Instrument)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	return meta, oid, nil
}

func (client *hyperliquidPerpClient) perpCancelMeta(ctx context.Context, req exchange.CancelOrderRequest) (hyperliquidMarketMeta, int64, error) {
	oid, err := hlValidateCancel(exchange.ProductPerp, req)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	meta, err := client.perpMeta(ctx, "CancelOrder", req.Instrument)
	if err != nil {
		return hyperliquidMarketMeta{}, 0, err
	}
	return meta, oid, nil
}

func hlSpotMetas(operation string, meta *hyperliquidspot.SpotMeta) (map[string]hyperliquidMarketMeta, error) {
	if meta == nil {
		return nil, hlMalformed(exchange.ProductSpot, operation, "missing spot metadata")
	}
	tokens := map[int]struct {
		name      string
		decimals  int
		canonical bool
	}{}
	for _, token := range meta.Tokens {
		if hlRejectCoin(token.Name) || token.Index < 0 || int64(token.Index) >= hyperliquidOutcomeAssetBase || token.SzDecimals < 0 || token.SzDecimals > 8 {
			return nil, hlMalformed(exchange.ProductSpot, operation, "malformed token metadata")
		}
		if _, exists := tokens[token.Index]; exists {
			return nil, hlMalformed(exchange.ProductSpot, operation, "duplicate token index")
		}
		tokens[token.Index] = struct {
			name      string
			decimals  int
			canonical bool
		}{token.Name, token.SzDecimals, token.IsCanonical}
	}
	out := map[string]hyperliquidMarketMeta{}
	seenSymbols := map[string]struct{}{}
	for _, uni := range meta.Universe {
		rawAssetID := int64(uni.Index)
		if hlRejectCoin(uni.Name) || len(uni.Tokens) != 2 || uni.Index < 0 || rawAssetID >= hyperliquidOutcomeAssetBase-hyperliquidSpotAssetOffset {
			return nil, hlMalformed(exchange.ProductSpot, operation, "malformed spot universe")
		}
		assetID := rawAssetID + hyperliquidSpotAssetOffset
		base, ok := tokens[uni.Tokens[0]]
		if !ok {
			return nil, hlMalformed(exchange.ProductSpot, operation, "unknown base token")
		}
		quote, ok := tokens[uni.Tokens[1]]
		if !ok {
			return nil, hlMalformed(exchange.ProductSpot, operation, "unknown quote token")
		}
		symbol := base.name + "-" + quote.name
		if _, exists := seenSymbols[symbol]; exists {
			return nil, hlMalformed(exchange.ProductSpot, operation, "duplicate spot symbol")
		}
		seenSymbols[symbol] = struct{}{}
		if _, exists := out[uni.Name]; exists {
			return nil, hlMalformed(exchange.ProductSpot, operation, "duplicate spot universe")
		}
		qtyInc := decimal.New(1, int32(-base.decimals))
		priceDecimals := max(0, 8-base.decimals)
		priceInc := decimal.New(1, int32(-priceDecimals))
		out[uni.Name] = hyperliquidMarketMeta{
			instrument: exchange.Instrument{
				Symbol:            symbol,
				BaseAsset:         base.name,
				QuoteAsset:        quote.name,
				Product:           exchange.ProductSpot,
				PriceIncrement:    priceInc,
				QuantityIncrement: qtyInc,
				MinQuantity:       qtyInc,
				MinNotional:       exchange.OptionalDecimal{Value: hyperliquidMinNotional, Valid: true},
			},
			nativeCoin:    uni.Name,
			assetID:       int(assetID),
			sizeDecimals:  base.decimals,
			priceDecimals: priceDecimals,
		}
	}
	return out, nil
}

func hlPerpMetas(operation string, full *hyperliquidperp.MetaAndAssetCtxsFull, prep *hyperliquidperp.PrepMeta) (map[string]hyperliquidMarketMeta, error) {
	if full == nil || prep == nil {
		return nil, hlMalformed(exchange.ProductPerp, operation, "missing perp metadata")
	}
	if len(full.Meta.Universe) != len(prep.Universe) || len(full.Meta.Universe) != len(full.AssetCtxs) {
		return nil, hlMalformed(exchange.ProductPerp, operation, "perp metadata length mismatch")
	}
	out := map[string]hyperliquidMarketMeta{}
	seenSymbols := map[string]struct{}{}
	for i, uni := range full.Meta.Universe {
		if uni.Name != prep.Universe[i].Name {
			return nil, hlMalformed(exchange.ProductPerp, operation, "perp metadata order mismatch")
		}
		if hlRejectCoin(uni.Name) || prep.Universe[i].SzDecimals < 0 || prep.Universe[i].SzDecimals > 6 {
			return nil, hlMalformed(exchange.ProductPerp, operation, "unsupported perp coin")
		}
		mark, err := hlOptionalPositiveDecimal(full.AssetCtxs[i].MarkPx)
		if err != nil {
			return nil, hlMalformed(exchange.ProductPerp, operation, "invalid mark price")
		}
		sz := prep.Universe[i].SzDecimals
		qtyInc := decimal.New(1, int32(-sz))
		priceDecimals := max(0, 6-sz)
		priceInc := decimal.New(1, int32(-priceDecimals))
		symbol := uni.Name + "-USDC"
		if _, exists := seenSymbols[symbol]; exists {
			return nil, hlMalformed(exchange.ProductPerp, operation, "duplicate perp symbol")
		}
		seenSymbols[symbol] = struct{}{}
		if _, exists := out[uni.Name]; exists {
			return nil, hlMalformed(exchange.ProductPerp, operation, "duplicate perp coin")
		}
		out[uni.Name] = hyperliquidMarketMeta{
			instrument: exchange.Instrument{
				Symbol:            symbol,
				BaseAsset:         uni.Name,
				QuoteAsset:        "USDC",
				SettleAsset:       "USDC",
				Product:           exchange.ProductPerp,
				PriceIncrement:    priceInc,
				QuantityIncrement: qtyInc,
				MinQuantity:       qtyInc,
				MinNotional:       exchange.OptionalDecimal{Value: hyperliquidMinNotional, Valid: true},
			},
			nativeCoin:    uni.Name,
			assetID:       i,
			sizeDecimals:  sz,
			priceDecimals: priceDecimals,
			markPrice:     mark,
		}
	}
	return out, nil
}

func hlBook(product exchange.Product, operation string, meta hyperliquidMarketMeta, limit int, coin string, rawLevels any, ms int64) (exchange.OrderBook, error) {
	var levels [][]hlL2Level
	switch typed := rawLevels.(type) {
	case [][]hyperliquidspot.L2Level:
		levels = make([][]hlL2Level, len(typed))
		for i := range typed {
			levels[i] = make([]hlL2Level, len(typed[i]))
			for j, row := range typed[i] {
				levels[i][j] = hlL2Level{px: row.Px, sz: row.Sz}
			}
		}
	case [][]hyperliquidperp.L2Level:
		levels = make([][]hlL2Level, len(typed))
		for i := range typed {
			levels[i] = make([]hlL2Level, len(typed[i]))
			for j, row := range typed[i] {
				levels[i][j] = hlL2Level{px: row.Px, sz: row.Sz}
			}
		}
	default:
		return exchange.OrderBook{}, hlMalformed(product, operation, "unsupported book level shape")
	}
	if coin != meta.nativeCoin || len(levels) != 2 {
		return exchange.OrderBook{}, hlMalformed(product, operation, "book product identity mismatch")
	}
	bids, err := hlBookLevels(levels[0])
	if err != nil {
		return exchange.OrderBook{}, hlMalformed(product, operation, err.Error())
	}
	asks, err := hlBookLevels(levels[1])
	if err != nil {
		return exchange.OrderBook{}, hlMalformed(product, operation, err.Error())
	}
	if limit > 0 {
		if len(bids) > limit {
			bids = bids[:limit]
		}
		if len(asks) > limit {
			asks = asks[:limit]
		}
	}
	if ms <= 0 {
		return exchange.OrderBook{}, hlMalformed(product, operation, "invalid book timestamp")
	}
	return exchange.OrderBook{Instrument: meta.instrument.Symbol, Bids: bids, Asks: asks, Time: time.UnixMilli(ms), Page: exchange.PageInfo{Limit: limit}}, nil
}

type hlL2Level struct {
	px string
	sz string
}

func hlBookLevels(rows []hlL2Level) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, err := hlPositiveDecimal(row.px)
		if err != nil {
			return nil, fmt.Errorf("invalid book price")
		}
		qty, err := hlPositiveDecimal(row.sz)
		if err != nil {
			return nil, fmt.Errorf("invalid book quantity")
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

func hlSpotCandles(meta hyperliquidMarketMeta, req exchange.CandlesRequest, rows []hyperliquidspot.Candle) (exchange.CandlePage, error) {
	out := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		if row.S != meta.nativeCoin || row.I != req.Interval {
			return exchange.CandlePage{}, hlMalformed(exchange.ProductSpot, "Candles", "candle product identity mismatch")
		}
		candle, err := hlCandle(row.T, row.TClose, row.O, row.H, row.L, row.C, row.V)
		if err != nil {
			return exchange.CandlePage{}, hlMalformed(exchange.ProductSpot, "Candles", err.Error())
		}
		out = append(out, candle)
	}
	return hlCandlePage(out, req), nil
}

func hlPerpCandles(meta hyperliquidMarketMeta, req exchange.CandlesRequest, rows []hyperliquidperp.Candle) (exchange.CandlePage, error) {
	out := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		if row.S != meta.nativeCoin || row.I != req.Interval {
			return exchange.CandlePage{}, hlMalformed(exchange.ProductPerp, "Candles", "candle product identity mismatch")
		}
		candle, err := hlCandle(row.T, row.TClose, row.O, row.H, row.L, row.C, row.V)
		if err != nil {
			return exchange.CandlePage{}, hlMalformed(exchange.ProductPerp, "Candles", err.Error())
		}
		out = append(out, candle)
	}
	return hlCandlePage(out, req), nil
}

func hlCandle(openMs, closeMs int64, openS, highS, lowS, closeS, volumeS string) (exchange.Candle, error) {
	open, err := hlPositiveDecimal(openS)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle open")
	}
	high, err := hlPositiveDecimal(highS)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle high")
	}
	low, err := hlPositiveDecimal(lowS)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle low")
	}
	closeValue, err := hlPositiveDecimal(closeS)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle close")
	}
	volume, err := hlNonNegativeDecimal(volumeS)
	if err != nil {
		return exchange.Candle{}, fmt.Errorf("invalid candle volume")
	}
	if openMs <= 0 || closeMs <= openMs || high.LessThan(low) || open.GreaterThan(high) || open.LessThan(low) || closeValue.GreaterThan(high) || closeValue.LessThan(low) {
		return exchange.Candle{}, fmt.Errorf("invalid candle range")
	}
	return exchange.Candle{OpenTime: time.UnixMilli(openMs), CloseTime: time.UnixMilli(closeMs), Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: closeMs <= time.Now().UnixMilli()}, nil
}

func hlCandlePage(candles []exchange.Candle, req exchange.CandlesRequest) exchange.CandlePage {
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	page := exchange.PageInfo{Limit: req.Limit}
	if len(candles) > 0 {
		if req.Limit > 0 && len(candles) > req.Limit {
			candles = candles[:req.Limit]
		}
		page.WindowStart = candles[0].OpenTime
		page.WindowEnd = candles[len(candles)-1].CloseTime
		page.Cursor = strconv.FormatInt(candles[len(candles)-1].OpenTime.UnixMilli(), 10)
	}
	return exchange.CandlePage{Candles: candles, Page: page}
}

func hlNormalizePlace(product exchange.Product, req exchange.PlaceOrderRequest, meta hyperliquidMarketMeta) (exchange.PlaceOrderRequest, error) {
	if req.Instrument != meta.instrument.Symbol {
		return exchange.PlaceOrderRequest{}, hlInvalid(product, "PlaceOrder", "instrument identity mismatch")
	}
	if err := req.Validate(product); err != nil {
		return exchange.PlaceOrderRequest{}, hlInvalid(product, "PlaceOrder", "invalid normalized order request")
	}
	if !req.Quantity.Mod(meta.instrument.QuantityIncrement).IsZero() {
		return exchange.PlaceOrderRequest{}, hlInvalid(product, "PlaceOrder", "quantity must align to Hyperliquid size increment")
	}
	if req.Type == exchange.OrderTypeMarket {
		return req, nil
	}
	normalized, err := hlNormalizeLimitPrice(req.Side, req.LimitPrice, meta.priceDecimals)
	if err != nil {
		return exchange.PlaceOrderRequest{}, hlInvalid(product, "PlaceOrder", "limit_price cannot be represented within Hyperliquid price precision")
	}
	req.LimitPrice = normalized
	return req, nil
}

// hlNormalizeLimitPrice applies Hyperliquid's venue-specific price rule:
// non-integer prices have at most five significant figures and at most the
// metadata-derived number of decimal places. To preserve the caller's maximum
// acceptable buy price and minimum acceptable sell price, buys round down and
// sells round up.
func hlNormalizeLimitPrice(side exchange.Side, requested decimal.Decimal, priceDecimals int) (decimal.Decimal, error) {
	if !requested.IsPositive() {
		return decimal.Zero, fmt.Errorf("price must be positive")
	}
	if priceDecimals < 0 {
		return decimal.Zero, fmt.Errorf("price decimals must be non-negative")
	}
	if side != exchange.SideBuy && side != exchange.SideSell {
		return decimal.Zero, fmt.Errorf("side must be buy or sell")
	}

	quantum := decimal.New(1, int32(-priceDecimals))
	for remainingDecimals := priceDecimals; remainingDecimals >= 0; remainingDecimals-- {
		units := requested.Div(quantum)
		if side == exchange.SideBuy {
			units = units.Floor()
		} else {
			units = units.Ceil()
		}
		normalized := units.Mul(quantum)
		if normalized.IsPositive() {
			isInteger := normalized.Equal(normalized.Truncate(0))
			if isInteger || hlSignificantDigits(normalized) <= 5 {
				return normalized, nil
			}
		}
		quantum = quantum.Mul(decimal.NewFromInt(10))
	}
	return decimal.Zero, fmt.Errorf("price cannot be represented")
}

func hlValidateCancel(product exchange.Product, req exchange.CancelOrderRequest) (int64, error) {
	oid, err := strconv.ParseInt(req.OrderID, 10, 64)
	if err != nil || oid <= 0 || strconv.FormatInt(oid, 10) != req.OrderID {
		return 0, hlInvalid(product, "CancelOrder", "order_id must be a positive numeric native order id")
	}
	return oid, nil
}

func hlValidateFillsRequest(product exchange.Product, req exchange.FillsRequest) error {
	if req.Cursor != "" || !req.Start.IsZero() || !req.End.IsZero() {
		return hlInvalid(product, "Fills", "unsupported fills page/window request")
	}
	if req.Limit < 0 {
		return hlInvalid(product, "Fills", "limit must be non-negative")
	}
	return nil
}

func hlValidateOptionalOrderID(product exchange.Product, operation, orderID string) error {
	if strings.TrimSpace(orderID) == "" {
		return nil
	}
	oid, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil || oid <= 0 || strconv.FormatInt(oid, 10) != orderID {
		return hlInvalid(product, operation, "order_id must be a positive numeric native order id")
	}
	return nil
}

func hlSpotPlaceAck(instrument, cloid string, status *hyperliquidspot.OrderStatus) (exchange.OrderAcknowledgement, error) {
	if status == nil {
		return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductSpot, "PlaceOrder", "missing order status")
	}
	if status.Resting != nil {
		if cloid != "" && (status.Resting.ClientID == nil || !strings.EqualFold(*status.Resting.ClientID, cloid)) {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductSpot, "PlaceOrder", "resting client order id mismatch")
		}
		return hlBaseAck(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, strconv.FormatInt(status.Resting.Oid, 10), cloid, exchange.AckResting), nil
	}
	if status.Filled != nil {
		ack := hlBaseAck(exchange.ProductSpot, exchange.OrderOperationPlace, instrument, strconv.Itoa(status.Filled.Oid), cloid, exchange.AckImmediatelyFilled)
		filled, err := hlPositiveDecimal(status.Filled.TotalSz)
		if err != nil {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductSpot, "PlaceOrder", "invalid filled quantity")
		}
		avg, err := hlPositiveDecimal(status.Filled.AvgPx)
		if err != nil {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductSpot, "PlaceOrder", "invalid average fill price")
		}
		ack.FilledQuantity = filled
		ack.AverageFillPrice = exchange.OptionalDecimal{Value: avg, Valid: true}
		return ack, nil
	}
	return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductSpot, "PlaceOrder", "unexpected order status")
}

func hlPerpPlaceAck(instrument, cloid string, status *hyperliquidperp.OrderStatus) (exchange.OrderAcknowledgement, error) {
	if status == nil {
		return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductPerp, "PlaceOrder", "missing order status")
	}
	if status.Resting != nil {
		if cloid != "" && (status.Resting.ClientID == nil || !strings.EqualFold(*status.Resting.ClientID, cloid)) {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductPerp, "PlaceOrder", "resting client order id mismatch")
		}
		return hlBaseAck(exchange.ProductPerp, exchange.OrderOperationPlace, instrument, strconv.FormatInt(status.Resting.Oid, 10), cloid, exchange.AckResting), nil
	}
	if status.Filled != nil {
		ack := hlBaseAck(exchange.ProductPerp, exchange.OrderOperationPlace, instrument, strconv.Itoa(status.Filled.Oid), cloid, exchange.AckImmediatelyFilled)
		filled, err := hlPositiveDecimal(status.Filled.TotalSz)
		if err != nil {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductPerp, "PlaceOrder", "invalid filled quantity")
		}
		avg, err := hlPositiveDecimal(status.Filled.AvgPx)
		if err != nil {
			return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductPerp, "PlaceOrder", "invalid average fill price")
		}
		ack.FilledQuantity = filled
		ack.AverageFillPrice = exchange.OptionalDecimal{Value: avg, Valid: true}
		return ack, nil
	}
	return exchange.OrderAcknowledgement{}, hlMalformed(exchange.ProductPerp, "PlaceOrder", "unexpected order status")
}

func hlCancelAck(product exchange.Product, instrument, orderID string, status *string) (exchange.OrderAcknowledgement, error) {
	if status == nil || *status != "success" {
		return exchange.OrderAcknowledgement{}, hlMalformed(product, "CancelOrder", "cancel response must be exactly success")
	}
	return hlBaseAck(product, exchange.OrderOperationCancel, instrument, orderID, "", exchange.AckAcceptedPending), nil
}

func hlMutationErr(product exchange.Product, op exchange.OrderOperation, instrument, orderID, cloid string, err error, tracker *hyperliquidRequestTracker) (exchange.OrderAcknowledgement, error) {
	operation := "PlaceOrder"
	if op == exchange.OrderOperationCancel {
		operation = "CancelOrder"
	}
	ack := hlBaseAck(product, op, instrument, orderID, cloid, exchange.AckAmbiguous)
	var rej *hyperliquid.OrderRejectedError
	if errors.As(err, &rej) || errors.Is(err, hyperliquid.ErrOrderRejected) {
		ack.State = exchange.AckRejected
		ack.VenueMessage = "Hyperliquid rejected order command"
		ack.VenueCode = "order_rejected"
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: ack.VenueCode, SafeMessage: "Hyperliquid rejected order command"})
	}
	if errors.Is(err, hyperliquid.ErrCredentialsRequired) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "Hyperliquid credentials required"})
	}
	status := 0
	if tracker != nil {
		status = tracker.responseStatus()
	}
	code := ""
	if status > 0 {
		code = strconv.Itoa(status)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid authentication failed"})
	case status == http.StatusTooManyRequests:
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid rate limit"})
	case status == http.StatusNotFound:
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid order command target not found"})
	case status >= 400 && status < 500:
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid rejected the order command request"})
	case status >= 500:
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "order command outcome is unknown after possible send"})
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "Hyperliquid rate limit"})
	}
	var apiErr *hyperliquid.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden:
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid authentication failed"})
		case apiErr.Code == http.StatusTooManyRequests:
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid rate limit"})
		case apiErr.Code == http.StatusNotFound:
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid order command target not found"})
		case apiErr.Code >= 400 && apiErr.Code < 500:
			return exchange.OrderAcknowledgement{}, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid rejected the order command request"})
		}
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "order command outcome is unknown after possible send"})
	}
	if hlMalformedSDKError(err) {
		return exchange.OrderAcknowledgement{}, hlMalformed(product, operation, "malformed Hyperliquid command response")
	}
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "order command outcome is unknown after possible send"})
}

func hlBaseAck(product exchange.Product, op exchange.OrderOperation, instrument, orderID, cloid string, state exchange.OrderAckState) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{Venue: hyperliquidVenue, Product: product, Operation: op, State: state, Instrument: instrument, OrderID: orderID, ClientOrderID: cloid}
}

func hlOrder(meta hyperliquidMarketMeta, coin, sideS, pxS, szS, origSzS string, oid int64, cloid string, createdMs, updatedMs int64, orderType, tif string, reduceOnly, isTrigger bool) (exchange.Order, error) {
	if coin != meta.nativeCoin || oid <= 0 || createdMs <= 0 || updatedMs <= 0 {
		return exchange.Order{}, fmt.Errorf("order identity mismatch")
	}
	if orderType != "Limit" || isTrigger {
		return exchange.Order{}, fmt.Errorf("unsupported open order semantics")
	}
	policy := exchange.LimitPolicyResting
	switch tif {
	case "Gtc", "":
	case "Ioc":
		policy = exchange.LimitPolicyIOC
	case "Alo":
		policy = exchange.LimitPolicyPostOnly
	default:
		return exchange.Order{}, fmt.Errorf("unsupported time in force")
	}
	side, err := hlSide(sideS)
	if err != nil {
		return exchange.Order{}, err
	}
	remaining, err := hlNonNegativeDecimal(szS)
	if err != nil {
		return exchange.Order{}, fmt.Errorf("invalid order size")
	}
	original, err := hlPositiveDecimal(origSzS)
	if err != nil {
		return exchange.Order{}, fmt.Errorf("invalid original order size")
	}
	filled := original.Sub(remaining)
	if filled.IsNegative() {
		return exchange.Order{}, fmt.Errorf("invalid filled size")
	}
	price, err := hlPositiveDecimal(pxS)
	if err != nil {
		return exchange.Order{}, fmt.Errorf("invalid order price")
	}
	return exchange.Order{Instrument: meta.instrument.Symbol, OrderID: strconv.FormatInt(oid, 10), ClientOrderID: hlPortableClientOrderID(cloid), Side: side, Type: exchange.OrderTypeLimit, Quantity: original, LimitPrice: price, LimitPolicy: policy, ReduceOnly: reduceOnly, Filled: filled, Status: "open", CreatedAt: time.UnixMilli(createdMs), UpdatedAt: time.UnixMilli(updatedMs)}, nil
}

func hlFill(meta hyperliquidMarketMeta, coin, sideS, pxS, szS, feeS, feeToken string, oid, tid int64, hash string, crossed bool, ms int64) (exchange.Fill, error) {
	if coin != meta.nativeCoin || oid <= 0 || tid <= 0 || ms <= 0 || strings.TrimSpace(hash) == "" {
		return exchange.Fill{}, fmt.Errorf("fill identity mismatch")
	}
	side, err := hlSide(sideS)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := hlPositiveDecimal(pxS)
	if err != nil {
		return exchange.Fill{}, fmt.Errorf("invalid fill price")
	}
	qty, err := hlPositiveDecimal(szS)
	if err != nil {
		return exchange.Fill{}, fmt.Errorf("invalid fill size")
	}
	fee, err := hlDecimal(feeS)
	if err != nil {
		return exchange.Fill{}, fmt.Errorf("invalid fill fee")
	}
	liq := exchange.LiquidityMaker
	if crossed {
		liq = exchange.LiquidityTaker
	}
	return exchange.Fill{Instrument: meta.instrument.Symbol, OrderID: strconv.FormatInt(oid, 10), FillID: strconv.FormatInt(tid, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: feeToken, Liquidity: liq, Time: time.UnixMilli(ms)}, nil
}

func hlSpotBalances(operation string, state *hyperliquid.SpotClearinghouseState) ([]exchange.Balance, error) {
	if state == nil {
		return nil, hlMalformed(exchange.ProductSpot, operation, "missing spot clearinghouse state")
	}
	out := make([]exchange.Balance, 0, len(state.Balances))
	for _, row := range state.Balances {
		if hlRejectCoin(row.Coin) || row.Token < 0 || row.Token >= hyperliquidOutcomeAssetBase {
			return nil, hlMalformed(exchange.ProductSpot, operation, "unsupported spot balance asset")
		}
		total, err := hlNonNegativeDecimal(row.Total)
		if err != nil {
			return nil, hlMalformed(exchange.ProductSpot, operation, "invalid balance total")
		}
		locked, err := hlNonNegativeDecimal(row.Hold)
		if err != nil {
			return nil, hlMalformed(exchange.ProductSpot, operation, "invalid balance hold")
		}
		available := total.Sub(locked)
		if available.IsNegative() {
			return nil, hlMalformed(exchange.ProductSpot, operation, "balance hold exceeds total")
		}
		out = append(out, exchange.Balance{Asset: row.Coin, Available: available, Locked: locked, Total: total})
	}
	return out, nil
}

func hlPosition(meta hyperliquidMarketMeta, sziS, entryS, pnlS, liqS, marginS string, leverage int) (exchange.Position, error) {
	szi, err := hlDecimal(sziS)
	if err != nil {
		return exchange.Position{}, fmt.Errorf("invalid position size")
	}
	side := exchange.SideBuy
	if szi.IsNegative() {
		side = exchange.SideSell
	}
	entry, err := hlNonNegativeDecimal(entryS)
	if err != nil {
		return exchange.Position{}, fmt.Errorf("invalid entry price")
	}
	pnl, err := hlDecimal(pnlS)
	if err != nil {
		return exchange.Position{}, fmt.Errorf("invalid unrealized pnl")
	}
	liq, err := hlOptionalNonNegative(liqS)
	if err != nil {
		return exchange.Position{}, fmt.Errorf("invalid liquidation price")
	}
	margin, err := hlOptionalNonNegative(marginS)
	if err != nil {
		return exchange.Position{}, fmt.Errorf("invalid margin used")
	}
	lev := exchange.OptionalDecimal{}
	if leverage > 0 {
		lev = exchange.OptionalDecimal{Value: decimal.NewFromInt(int64(leverage)), Valid: true}
	}
	return exchange.Position{Instrument: meta.instrument.Symbol, Side: side, Quantity: szi, EntryPrice: entry, MarkPrice: meta.markPrice, UnrealizedPnL: pnl, LiquidationPrice: liq, Leverage: lev, MarginUsed: margin}, nil
}

func hlCandleWindow(req exchange.CandlesRequest) (int64, int64, error) {
	if strings.TrimSpace(req.Interval) == "" {
		return 0, 0, fmt.Errorf("interval is required")
	}
	if req.Limit < 0 {
		return 0, 0, fmt.Errorf("limit must be non-negative")
	}
	if req.Cursor != "" {
		start, err := strconv.ParseInt(req.Cursor, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, fmt.Errorf("cursor must be a millisecond timestamp")
		}
		return start, time.Now().UnixMilli(), nil
	}
	if req.Start.IsZero() {
		return 0, time.Now().UnixMilli(), nil
	}
	end := req.End
	if end.IsZero() {
		end = time.Now()
	}
	if !end.After(req.Start) {
		return 0, 0, fmt.Errorf("end must be after start")
	}
	return req.Start.UnixMilli(), end.UnixMilli(), nil
}

func hlRejectCoin(coin string) bool {
	coin = strings.TrimSpace(coin)
	return coin == "" || strings.Contains(coin, ":") || strings.HasPrefix(coin, "#") || strings.HasPrefix(coin, "+")
}

func hlSide(value string) (exchange.Side, error) {
	switch value {
	case "B", "buy":
		return exchange.SideBuy, nil
	case "A", "sell":
		return exchange.SideSell, nil
	default:
		return "", fmt.Errorf("invalid side")
	}
}

func hlDecimal(value string) (decimal.Decimal, error) {
	return decimal.NewFromString(strings.TrimSpace(value))
}

func hlPositiveDecimal(value string) (decimal.Decimal, error) {
	d, err := hlDecimal(value)
	if err != nil || !d.IsPositive() {
		return decimal.Decimal{}, errors.New("not positive")
	}
	return d, nil
}

func hlNonNegativeDecimal(value string) (decimal.Decimal, error) {
	d, err := hlDecimal(value)
	if err != nil || d.IsNegative() {
		return decimal.Decimal{}, errors.New("negative")
	}
	return d, nil
}

func hlOptionalPositiveDecimal(value string) (decimal.Decimal, error) {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero, nil
	}
	return hlPositiveDecimal(value)
}

func hlOptionalNonNegative(value string) (exchange.OptionalDecimal, error) {
	if strings.TrimSpace(value) == "" {
		return exchange.OptionalDecimal{}, nil
	}
	d, err := hlNonNegativeDecimal(value)
	if err != nil {
		return exchange.OptionalDecimal{}, err
	}
	return exchange.OptionalDecimal{Value: d, Valid: true}, nil
}

func hlMustFloat(d decimal.Decimal) float64 {
	f, _ := d.Float64()
	return f
}

func decimalPlaces(d decimal.Decimal) int {
	s := d.String()
	idx := strings.IndexByte(s, '.')
	if idx < 0 {
		return 0
	}
	return len(s) - idx - 1
}

func hlSignificantDigits(d decimal.Decimal) int {
	s := d.Abs().String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
	}
	s = strings.ReplaceAll(s, ".", "")
	s = strings.TrimLeft(s, "0")
	return len(s)
}

func hlContextErr(product exchange.Product, operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "context canceled"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "context deadline exceeded"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "context error"})
}

func hlRequireCtx(ctx context.Context, product exchange.Product, operation string) error {
	if ctx == nil {
		return hlInvalid(product, operation, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return hlContextErr(product, operation, err)
	}
	return nil
}

func hlNormalizeQueryErr(product exchange.Product, operation string, err error, tracker *hyperliquidRequestTracker) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return hlContextErr(product, operation, err)
	}
	status := tracker.responseStatus()
	code := ""
	if status > 0 {
		code = strconv.Itoa(status)
	}
	var apiErr *hyperliquid.APIError
	hasAPIError := errors.As(err, &apiErr)
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid authentication failed"})
	case status == http.StatusTooManyRequests:
		return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid rate limit"})
	case status == http.StatusNotFound:
		return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid resource not found"})
	case status >= 500:
		return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid API unavailable"})
	case status == http.StatusBadRequest && hasAPIError && hyperliquid.IsWalletDoesNotExistError(err):
		return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid account not found"})
	case status >= 400 && status < 500:
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid rejected the request"})
	case status > 0 && status < 400 && hlMalformedSDKError(err):
		return hlMalformed(product, operation, "malformed Hyperliquid response")
	case status > 0:
		return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: code, SafeMessage: "Hyperliquid response read failed"})
	}
	var sdkErr *sdkcore.ExchangeError
	if errors.As(err, &sdkErr) && errors.Is(err, sdkcore.ErrRateLimited) {
		return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: sdkErr.Code, SafeMessage: "Hyperliquid rate limit"})
	}
	if hasAPIError {
		switch {
		case apiErr.Code == http.StatusUnauthorized || apiErr.Code == http.StatusForbidden:
			return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid authentication failed"})
		case apiErr.Code == http.StatusTooManyRequests:
			return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid rate limit"})
		case apiErr.Code == http.StatusNotFound || hyperliquid.IsWalletDoesNotExistError(err):
			return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, Code: strconv.Itoa(apiErr.Code), SafeMessage: "Hyperliquid resource not found"})
		}
	}
	if errors.Is(err, hyperliquid.ErrCredentialsRequired) {
		return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "Hyperliquid credentials required"})
	}
	if hlMalformedSDKError(err) {
		return hlMalformed(product, operation, "malformed Hyperliquid response")
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: "Hyperliquid transport error"})
}

func hlMalformedSDKError(err error) bool {
	errText := err.Error()
	return strings.Contains(errText, "malformed") ||
		strings.Contains(errText, "venue returned") ||
		strings.Contains(errText, "missing response") ||
		strings.Contains(errText, "client order id mismatch") ||
		strings.Contains(errText, "unmarshal") ||
		strings.Contains(errText, "invalid character") ||
		strings.Contains(errText, "unexpected end") ||
		strings.Contains(errText, "cannot unmarshal")
}

func hlInvalid(product exchange.Product, operation, msg string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: msg})
}

func hlMalformed(product exchange.Product, operation, msg string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: hyperliquidVenue, Product: product, Operation: operation, SafeMessage: msg})
}
