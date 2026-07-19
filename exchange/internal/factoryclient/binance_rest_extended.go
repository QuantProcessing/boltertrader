package factoryclient

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func (client *binanceSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := binanceSpotContextErr(ctx, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(req.Instrument, "PublicTrades")
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, binanceSpotInvalid("PublicTrades", "limit must be non-negative")
	}
	rows, err := client.sdk.GetTrades(ctx, symbol, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, binanceSpotNormalizeErr(err, "PublicTrades")
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		trade, err := binanceSpotPublicTrade(instrument, row)
		if err != nil {
			return exchange.PublicTradePage{}, binanceSpotMalformed("PublicTrades", err.Error())
		}
		trades = append(trades, trade)
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *binanceSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := binanceSpotContextErr(ctx, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(req.Instrument, "OrderHistory")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	if err := validateBoundedHistory(req.Cursor, req.Limit, req.Start, req.End); err != nil {
		return exchange.OrderPage{}, binanceSpotInvalid("OrderHistory", err.Error())
	}
	orderID, err := positiveInt64Cursor(req.Cursor)
	if err != nil {
		return exchange.OrderPage{}, binanceSpotInvalid("OrderHistory", err.Error())
	}
	rows, err := client.sdk.AllOrders(ctx, symbol, req.Limit, req.Start.UnixMilli(), req.End.UnixMilli(), orderID)
	if err != nil {
		return exchange.OrderPage{}, binanceSpotNormalizeErr(err, "OrderHistory")
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if binanceOrderActive(row.Status) {
			continue
		}
		order, err := binanceSpotOrder(row, symbol, instrument)
		if err != nil {
			return exchange.OrderPage{}, binanceSpotMalformed("OrderHistory", err.Error())
		}
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders, Page: historyPage(req.Cursor, req.Limit, req.Start, req.End)}, nil
}

func (client *binancePerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := client.binancePerpReady(ctx, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	instrument, symbol, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, binancePerpInvalidRequest("PublicTrades", err.Error())
	}
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, binancePerpInvalidRequest("PublicTrades", "limit must be non-negative")
	}
	rows, err := client.sdk.GetAggTrades(ctx, symbol, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, binancePerpNormalizeError("PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		trade, err := binancePerpPublicTrade(instrument, row)
		if err != nil {
			return exchange.PublicTradePage{}, binancePerpMalformed("PublicTrades", err.Error())
		}
		trades = append(trades, trade)
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *binancePerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := client.binancePerpReady(ctx, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	instrument, symbol, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OrderHistory", err.Error())
	}
	if err := validateBoundedHistory(req.Cursor, req.Limit, req.Start, req.End); err != nil {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OrderHistory", err.Error())
	}
	orderID, err := positiveInt64Cursor(req.Cursor)
	if err != nil {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OrderHistory", err.Error())
	}
	rows, err := client.sdk.AllOrders(ctx, symbol, req.Limit, optionalMillis(req.Start), optionalMillis(req.End), orderID)
	if err != nil {
		return exchange.OrderPage{}, binancePerpNormalizeError("OrderHistory", err)
	}
	orders := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if binanceOrderActive(row.Status) {
			continue
		}
		order, err := binancePerpOrder(row)
		if err != nil {
			return exchange.OrderPage{}, binancePerpMalformed("OrderHistory", err.Error())
		}
		if order.Instrument != instrument {
			return exchange.OrderPage{}, binancePerpMalformed("OrderHistory", "response instrument does not match request")
		}
		orders = append(orders, order)
	}
	return exchange.OrderPage{Orders: orders, Page: historyPage(req.Cursor, req.Limit, req.Start, req.End)}, nil
}

func (client *binancePerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := client.binancePerpReady(ctx, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	instrument, symbol, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.FundingRate{}, binancePerpInvalidRequest("FundingRate", err.Error())
	}
	row, err := client.sdk.GetFundingRate(ctx, symbol)
	if err != nil {
		return exchange.FundingRate{}, binancePerpNormalizeError("FundingRate", err)
	}
	rate, err := positiveOrNegativeDecimal(row.LastFundingRate)
	if err != nil {
		return exchange.FundingRate{}, binancePerpMalformed("FundingRate", "invalid funding rate")
	}
	mark, err := positiveDecimal(row.MarkPrice)
	if err != nil {
		return exchange.FundingRate{}, binancePerpMalformed("FundingRate", "invalid mark price")
	}
	return exchange.FundingRate{
		Instrument:      instrument,
		Rate:            rate,
		MarkPrice:       exchange.OptionalDecimal{Value: mark, Valid: true},
		ObservedAt:      time.UnixMilli(row.Time).UTC(),
		NextFundingTime: time.UnixMilli(row.NextFundingTime).UTC(),
	}, nil
}

func (client *binancePerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := client.binancePerpReady(ctx, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	instrument, symbol, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.FundingRatePage{}, binancePerpInvalidRequest("FundingRateHistory", err.Error())
	}
	if err := validateBoundedHistory(req.Cursor, req.Limit, req.Start, req.End); err != nil {
		return exchange.FundingRatePage{}, binancePerpInvalidRequest("FundingRateHistory", err.Error())
	}
	if req.Cursor != "" {
		return exchange.FundingRatePage{}, binancePerpInvalidRequest("FundingRateHistory", "cursor is not supported")
	}
	rows, err := client.sdk.GetFundingRateHistory(ctx, symbol, optionalMillis(req.Start), optionalMillis(req.End), req.Limit)
	if err != nil {
		return exchange.FundingRatePage{}, binancePerpNormalizeError("FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		rate, err := binanceFundingHistory(instrument, row)
		if err != nil {
			return exchange.FundingRatePage{}, binancePerpMalformed("FundingRateHistory", err.Error())
		}
		rates = append(rates, rate)
	}
	return exchange.FundingRatePage{Rates: rates, Page: historyPage("", req.Limit, req.Start, req.End)}, nil
}

func (client *binancePerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := client.binancePerpReady(ctx, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	instrument, symbol, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.Leverage{}, binancePerpInvalidRequest("SetLeverage", err.Error())
	}
	if req.Leverage <= 0 || req.Leverage > 125 {
		return exchange.Leverage{}, binancePerpInvalidRequest("SetLeverage", "leverage must be between 1 and 125")
	}
	row, err := client.sdk.ChangeLeverage(ctx, symbol, req.Leverage)
	if err != nil {
		return exchange.Leverage{}, binancePerpNormalizeError("SetLeverage", err)
	}
	if !strings.EqualFold(row.Symbol, symbol) || row.Leverage <= 0 {
		return exchange.Leverage{}, binancePerpMalformed("SetLeverage", "response does not match request")
	}
	return exchange.Leverage{Instrument: instrument, Effective: row.Leverage}, nil
}

func binanceSpotPublicTrade(instrument string, row binancespot.PublicTrade) (exchange.PublicTrade, error) {
	price, err := positiveDecimal(row.Price)
	if err != nil {
		return exchange.PublicTrade{}, err
	}
	quantity, err := positiveDecimal(row.Qty)
	if err != nil {
		return exchange.PublicTrade{}, err
	}
	side := exchange.SideBuy
	if row.IsBuyerMaker {
		side = exchange.SideSell
	}
	return exchange.PublicTrade{
		Instrument: instrument,
		TradeID:    strconv.FormatInt(row.ID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Time:       time.UnixMilli(row.Time).UTC(),
	}, nil
}

func binancePerpPublicTrade(instrument string, row binanceperp.AggTrade) (exchange.PublicTrade, error) {
	price, err := positiveDecimal(row.Price)
	if err != nil {
		return exchange.PublicTrade{}, err
	}
	quantity, err := positiveDecimal(row.Quantity)
	if err != nil {
		return exchange.PublicTrade{}, err
	}
	side := exchange.SideBuy
	if row.IsBuyerMaker {
		side = exchange.SideSell
	}
	return exchange.PublicTrade{
		Instrument: instrument,
		TradeID:    strconv.FormatInt(row.ID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Time:       time.UnixMilli(row.Timestamp).UTC(),
	}, nil
}

func binanceFundingHistory(instrument string, row binanceperp.FundingRateHistoryEntry) (exchange.FundingRate, error) {
	rate, err := positiveOrNegativeDecimal(row.FundingRate)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	out := exchange.FundingRate{
		Instrument:  instrument,
		Rate:        rate,
		FundingTime: time.UnixMilli(row.FundingTime).UTC(),
	}
	if row.MarkPrice != "" {
		mark, err := positiveDecimal(row.MarkPrice)
		if err != nil {
			return exchange.FundingRate{}, err
		}
		out.MarkPrice = exchange.OptionalDecimal{Value: mark, Valid: true}
	}
	return out, nil
}

func positiveDecimal(value string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return decimal.Zero, strconv.ErrSyntax
	}
	return parsed, nil
}

func positiveOrNegativeDecimal(value string) (decimal.Decimal, error) {
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, strconv.ErrSyntax
	}
	return parsed, nil
}

func validateBoundedHistory(cursor string, limit int, start, end time.Time) error {
	if limit < 0 {
		return strconv.ErrRange
	}
	if !start.IsZero() && !end.IsZero() && !start.Before(end) {
		return strconv.ErrRange
	}
	return nil
}

func positiveInt64Cursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || value <= 0 {
		return 0, strconv.ErrSyntax
	}
	return value, nil
}

func optionalMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func historyPage(cursor string, limit int, start, end time.Time) exchange.PageInfo {
	return exchange.PageInfo{Cursor: cursor, Limit: limit, WindowStart: start, WindowEnd: end}
}

func binanceOrderActive(status string) bool {
	switch strings.ToUpper(status) {
	case "NEW", "PARTIALLY_FILLED", "PENDING_CANCEL":
		return true
	default:
		return false
	}
}

func binanceSpotPlaceParams(symbol, side string, req exchange.PlaceOrderRequest) binancespot.PlaceOrderParams {
	params := binancespot.PlaceOrderParams{
		Symbol:           symbol,
		Side:             side,
		Quantity:         req.Quantity.String(),
		NewClientOrderID: req.ClientOrderID,
		NewOrderRespType: "RESULT",
	}
	if req.Type == exchange.OrderTypeMarket {
		params.Type = "MARKET"
		return params
	}
	params.Type = "LIMIT"
	params.Price = req.LimitPrice.String()
	switch req.LimitPolicy {
	case exchange.LimitPolicyIOC:
		params.TimeInForce = "IOC"
	case exchange.LimitPolicyPostOnly:
		params.Type = "LIMIT_MAKER"
	default:
		params.TimeInForce = "GTC"
	}
	return params
}

func binancePerpPlaceParams(symbol string, req exchange.PlaceOrderRequest) binanceperp.PlaceOrderParams {
	params := binanceperp.PlaceOrderParams{
		Symbol:           symbol,
		Side:             binancePerpSide(req.Side),
		Quantity:         req.Quantity.String(),
		NewClientOrderID: req.ClientOrderID,
		ReduceOnly:       req.ReduceOnly,
	}
	if req.Type == exchange.OrderTypeMarket {
		params.Type = "MARKET"
		return params
	}
	params.Type = "LIMIT"
	params.Price = req.LimitPrice.String()
	switch req.LimitPolicy {
	case exchange.LimitPolicyIOC:
		params.TimeInForce = "IOC"
	case exchange.LimitPolicyPostOnly:
		params.TimeInForce = "GTX"
	default:
		params.TimeInForce = "GTC"
	}
	return params
}
