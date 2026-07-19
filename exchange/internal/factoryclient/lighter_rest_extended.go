package factoryclient

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func (client *lighterSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	meta, err := client.lighterMeta(ctx, "PublicTrades", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return lighterPublicTrades(ctx, client.sdk, exchange.ProductSpot, meta, req)
}

func (client *lighterPerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	meta, err := client.lighterMeta(ctx, "PublicTrades", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	return lighterPublicTrades(ctx, client.sdk, exchange.ProductPerp, meta, req)
}

func (client *lighterSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	meta, err := client.lighterMeta(ctx, "OrderHistory", exchange.ProductSpot, lighterSpot, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return lighterOrderHistory(ctx, client.sdk, exchange.ProductSpot, meta, req)
}

func (client *lighterPerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	meta, err := client.lighterMeta(ctx, "OrderHistory", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return lighterOrderHistory(ctx, client.sdk, exchange.ProductPerp, meta, req)
}

func (client *lighterPerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	meta, err := client.lighterMeta(ctx, "FundingRate", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	row, err := client.sdk.GetFundingRate(ctx, meta.marketID)
	if err != nil {
		return exchange.FundingRate{}, lighterNormalizeErr(exchange.ProductPerp, "FundingRate", err)
	}
	if row.MarketId != meta.marketID {
		return exchange.FundingRate{}, lighterMalformed(exchange.ProductPerp, "FundingRate", "response market does not match request")
	}
	rate := decimal.NewFromFloat(row.Rate)
	return exchange.FundingRate{
		Instrument: req.Instrument,
		Rate:       rate,
		ObservedAt: time.Now().UTC(),
	}, nil
}

func (client *lighterPerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	meta, err := client.lighterMeta(ctx, "FundingRateHistory", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	if req.Cursor != "" {
		return exchange.FundingRatePage{}, lighterInvalid(exchange.ProductPerp, "FundingRateHistory", "cursor is not supported")
	}
	if req.Limit < 0 {
		return exchange.FundingRatePage{}, lighterInvalid(exchange.ProductPerp, "FundingRateHistory", "limit must be non-negative")
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
		start = end.Add(-time.Duration(windowSize) * time.Hour)
	}
	if !start.Before(end) {
		return exchange.FundingRatePage{}, lighterInvalid(exchange.ProductPerp, "FundingRateHistory", "start must be before end")
	}
	rows, err := client.sdk.GetFundingHistory(ctx, meta.marketID, "1h", start.Unix(), end.Unix(), int64(req.Limit))
	if err != nil {
		return exchange.FundingRatePage{}, lighterNormalizeErr(exchange.ProductPerp, "FundingRateHistory", err)
	}
	if rows.Code != 200 {
		return exchange.FundingRatePage{}, lighterMalformed(exchange.ProductPerp, "FundingRateHistory", "response code is not 200")
	}
	rates := make([]exchange.FundingRate, 0, len(rows.Fundings))
	for _, row := range rows.Fundings {
		rate, err := lighterDecimal(row.Rate)
		if err != nil {
			return exchange.FundingRatePage{}, lighterMalformed(exchange.ProductPerp, "FundingRateHistory", "invalid funding rate")
		}
		rates = append(rates, exchange.FundingRate{
			Instrument:  req.Instrument,
			Rate:        rate,
			FundingTime: lighterFlexibleUnix(row.Timestamp),
		})
	}
	return exchange.FundingRatePage{Rates: rates, Page: historyPage("", req.Limit, start, end)}, nil
}

func (client *lighterPerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	meta, err := client.lighterMeta(ctx, "SetLeverage", exchange.ProductPerp, lighterPerp, req.Instrument)
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 || req.Leverage > 100 {
		return exchange.Leverage{}, lighterInvalid(exchange.ProductPerp, "SetLeverage", "leverage must be between 1 and 100")
	}
	account, err := lighterAccount(ctx, client.sdk, exchange.ProductPerp, "SetLeverage")
	if err != nil {
		return exchange.Leverage{}, err
	}
	marginMode, err := lighterCurrentMarginMode(account, meta.marketID)
	if err != nil {
		return exchange.Leverage{}, lighterMalformed(exchange.ProductPerp, "SetLeverage", err.Error())
	}
	row, err := client.sdk.UpdateLeverage(ctx, meta.marketID, uint16(req.Leverage), marginMode)
	if err != nil {
		return exchange.Leverage{}, lighterNormalizeErr(exchange.ProductPerp, "SetLeverage", err)
	}
	if row.Code != 200 {
		return exchange.Leverage{}, lighterMalformed(exchange.ProductPerp, "SetLeverage", "response code is not 200")
	}
	return exchange.Leverage{Instrument: req.Instrument, Effective: req.Leverage}, nil
}

func lighterPublicTrades(ctx context.Context, sdk *lighter.Client, product exchange.Product, meta lighterMarketMeta, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, lighterInvalid(product, "PublicTrades", "limit must be non-negative")
	}
	limit := req.Limit
	if limit == 0 {
		limit = 100
	}
	rows, err := sdk.GetRecentTrades(ctx, meta.marketID, int64(limit))
	if err != nil {
		return exchange.PublicTradePage{}, lighterNormalizeErr(product, "PublicTrades", err)
	}
	if rows.Code != 200 {
		return exchange.PublicTradePage{}, lighterMalformed(product, "PublicTrades", "response code is not 200")
	}
	trades := make([]exchange.PublicTrade, 0, len(rows.Trades))
	for _, row := range rows.Trades {
		if row.MarketId != meta.marketID || row.TradeId <= 0 {
			return exchange.PublicTradePage{}, lighterMalformed(product, "PublicTrades", "response market or trade id is invalid")
		}
		price, err := lighterPositiveDecimal(row.Price)
		if err != nil {
			return exchange.PublicTradePage{}, lighterMalformed(product, "PublicTrades", "invalid trade price")
		}
		quantity, err := lighterPositiveDecimal(row.Size)
		if err != nil {
			return exchange.PublicTradePage{}, lighterMalformed(product, "PublicTrades", "invalid trade quantity")
		}
		side := exchange.SideSell
		if row.IsMakerAsk {
			side = exchange.SideBuy
		}
		trades = append(trades, exchange.PublicTrade{
			Instrument: req.Instrument,
			TradeID:    strconv.FormatInt(row.TradeId, 10),
			Side:       side,
			Price:      price,
			Quantity:   quantity,
			Time:       lighterFlexibleUnix(row.Timestamp),
		})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: limit}}, nil
}

func lighterOrderHistory(ctx context.Context, sdk *lighter.Client, product exchange.Product, meta lighterMarketMeta, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if req.Cursor != "" {
		return exchange.OrderPage{}, lighterInvalid(product, "OrderHistory", "cursor is not supported by Lighter inactive orders")
	}
	if !req.Start.IsZero() || !req.End.IsZero() {
		return exchange.OrderPage{}, lighterInvalid(product, "OrderHistory", "Lighter inactive orders does not support exchange time windows")
	}
	if req.Limit < 0 {
		return exchange.OrderPage{}, lighterInvalid(product, "OrderHistory", "limit must be non-negative")
	}
	rows, err := sdk.GetInactiveOrders(ctx, &meta.marketID, int64(req.Limit))
	if err != nil {
		return exchange.OrderPage{}, lighterNormalizeErr(product, "OrderHistory", err)
	}
	if rows.Code != 200 {
		return exchange.OrderPage{}, lighterMalformed(product, "OrderHistory", "response code is not 200")
	}
	orders := make([]exchange.Order, 0, len(rows.Orders))
	for _, row := range rows.Orders {
		if row.MarketIndex != meta.marketID {
			return exchange.OrderPage{}, lighterMalformed(product, "OrderHistory", "mixed order market")
		}
		if lighterOrderOutsideExchangeSubset(row) {
			continue
		}
		order, err := lighterOrder(row, meta)
		if err != nil {
			return exchange.OrderPage{}, lighterMalformed(product, "OrderHistory", err.Error())
		}
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Cursor: rows.NextCursor, Limit: req.Limit}}, nil
}

func lighterCurrentMarginMode(account *lighter.Account, marketID int) (uint8, error) {
	for _, position := range account.Positions {
		if position != nil && position.MarketId == marketID {
			if position.MarginMode != lighter.CrossMarginMode && position.MarginMode != lighter.IsolatedMarginMode {
				return 0, strconv.ErrRange
			}
			return uint8(position.MarginMode), nil
		}
	}
	if account.AccountTradingMode != lighter.CrossMarginMode && account.AccountTradingMode != lighter.IsolatedMarginMode {
		return 0, strconv.ErrRange
	}
	return uint8(account.AccountTradingMode), nil
}

func lighterFlexibleUnix(value int64) time.Time {
	if value > 100_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func lighterPlaceRequest(meta lighterMarketMeta, req exchange.PlaceOrderRequest, price uint32, quantity, clientID int64, isAsk uint32) lighter.CreateOrderRequest {
	orderType := uint32(lighter.OrderTypeLimit)
	timeInForce := uint32(lighter.OrderTimeInForceGoodTillTime)
	orderExpiry := time.Now().Add(28 * 24 * time.Hour).UnixMilli()
	if req.Type == exchange.OrderTypeMarket {
		orderType = lighter.OrderTypeMarket
		timeInForce = lighter.OrderTimeInForceImmediateOrCancel
		orderExpiry = lighter.DefaultIocExpiry
	} else {
		switch req.LimitPolicy {
		case exchange.LimitPolicyIOC:
			timeInForce = lighter.OrderTimeInForceImmediateOrCancel
			orderExpiry = lighter.DefaultIocExpiry
		case exchange.LimitPolicyPostOnly:
			timeInForce = lighter.OrderTimeInForcePostOnly
		}
	}
	reduceOnly := uint32(0)
	if req.ReduceOnly {
		reduceOnly = 1
	}
	return lighter.CreateOrderRequest{
		MarketId:      meta.marketID,
		Price:         price,
		BaseAmount:    quantity,
		IsAsk:         isAsk,
		OrderType:     orderType,
		ClientOrderId: clientID,
		TimeInForce:   timeInForce,
		ReduceOnly:    reduceOnly,
		TriggerPrice:  lighter.NilTriggerPrice,
		OrderExpiry:   orderExpiry,
	}
}
