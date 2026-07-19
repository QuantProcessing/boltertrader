package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	"github.com/shopspring/decimal"
)

const (
	binancePerpVenue     = exchange.VenueBinance
	binancePerpProduct   = exchange.ProductPerp
	binancePerpPrefix    = "/fapi"
	binancePerpOperation = "binance usd-m perp"
)

func (client *binancePerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := client.binancePerpReady(ctx, "Instruments"); err != nil {
		return nil, err
	}
	info, err := client.sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, binancePerpNormalizeError("Instruments", err)
	}
	instruments := make([]exchange.Instrument, 0, len(info.Symbols))
	seen := make(map[string]struct{}, len(info.Symbols))
	for _, symbol := range info.Symbols {
		if symbol.Status != "TRADING" {
			continue
		}
		if symbol.ContractType != "PERPETUAL" {
			continue
		}
		if symbol.MarginAsset == "" || symbol.BaseAsset == "" || symbol.QuoteAsset == "" {
			return nil, binancePerpMalformed("Instruments", "missing contract asset metadata")
		}
		canonical, supported, err := binancePerpPublicInstrumentSymbols(
			symbol.Symbol,
			symbol.BaseAsset,
			symbol.QuoteAsset,
		)
		if err != nil {
			return nil, binancePerpMalformed("Instruments", err.Error())
		}
		if !supported {
			continue
		}
		if _, ok := seen[symbol.Symbol]; ok {
			return nil, binancePerpMalformed("Instruments", "duplicate symbol")
		}
		seen[symbol.Symbol] = struct{}{}
		priceIncrement, quantityIncrement, minQuantity, minNotional, err := binancePerpInstrumentFilters(symbol.Filters)
		if err != nil {
			return nil, binancePerpMalformed("Instruments", err.Error())
		}
		instruments = append(instruments, exchange.Instrument{
			Symbol:            canonical,
			BaseAsset:         symbol.BaseAsset,
			QuoteAsset:        symbol.QuoteAsset,
			SettleAsset:       symbol.MarginAsset,
			Product:           exchange.ProductPerp,
			PriceIncrement:    priceIncrement,
			QuantityIncrement: quantityIncrement,
			MinQuantity:       minQuantity,
			MinNotional:       minNotional,
		})
	}
	return instruments, nil
}

func (client *binancePerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := client.binancePerpReady(ctx, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	canonical, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.OrderBook{}, binancePerpInvalidRequest("OrderBook", err.Error())
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, binancePerpInvalidRequest("OrderBook", "limit must be non-negative")
	}
	resp, err := client.sdk.Depth(ctx, native, req.Limit)
	if err != nil {
		return exchange.OrderBook{}, binancePerpNormalizeError("OrderBook", err)
	}
	bids, err := binancePerpBookLevels(resp.Bids)
	if err != nil {
		return exchange.OrderBook{}, binancePerpMalformed("OrderBook", err.Error())
	}
	asks, err := binancePerpBookLevels(resp.Asks)
	if err != nil {
		return exchange.OrderBook{}, binancePerpMalformed("OrderBook", err.Error())
	}
	return exchange.OrderBook{
		Instrument: canonical,
		Bids:       bids,
		Asks:       asks,
		Time:       time.UnixMilli(resp.T).UTC(),
		Sequence:   strconv.FormatInt(resp.LastUpdateID, 10),
		Page: exchange.PageInfo{
			Limit: req.Limit,
		},
	}, nil
}

func (client *binancePerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := client.binancePerpReady(ctx, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	_, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.CandlePage{}, binancePerpInvalidRequest("Candles", err.Error())
	}
	if req.Cursor != "" {
		return exchange.CandlePage{}, binancePerpInvalidRequest("Candles", "cursor is not supported by Binance candles")
	}
	if strings.TrimSpace(req.Interval) == "" {
		return exchange.CandlePage{}, binancePerpInvalidRequest("Candles", "interval is required")
	}
	if req.Limit < 0 {
		return exchange.CandlePage{}, binancePerpInvalidRequest("Candles", "limit must be non-negative")
	}
	if !req.Start.IsZero() && !req.End.IsZero() && !req.End.After(req.Start) {
		return exchange.CandlePage{}, binancePerpInvalidRequest("Candles", "end must be after start")
	}
	resp, err := client.sdk.Klines(ctx, native, req.Interval, req.Limit, binancePerpMillis(req.Start), binancePerpMillis(req.End))
	if err != nil {
		return exchange.CandlePage{}, binancePerpNormalizeError("Candles", err)
	}
	candles := make([]exchange.Candle, 0, len(resp))
	for _, row := range resp {
		candle, err := binancePerpCandle(row)
		if err != nil {
			return exchange.CandlePage{}, binancePerpMalformed("Candles", err.Error())
		}
		candles = append(candles, candle)
	}
	return exchange.CandlePage{
		Candles: candles,
		Page: exchange.PageInfo{
			Limit:       req.Limit,
			WindowStart: req.Start,
			WindowEnd:   req.End,
		},
	}, nil
}

func (client *binancePerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.binancePerpReady(ctx, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("PlaceOrder", err.Error())
	}
	canonical, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("PlaceOrder", err.Error())
	}
	ack := binancePerpAck(exchange.OrderOperationPlace, canonical, "", req.ClientOrderID)
	params := binancePerpPlaceParams(native, req)
	resp, err := client.sdk.PlaceOrder(ctx, params)
	if err != nil {
		return binancePerpCommandErrorAck("PlaceOrder", ack, err)
	}
	result, err := binancePerpOrderAck(exchange.OrderOperationPlace, canonical, "", req.ClientOrderID, resp)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if resp.ReduceOnly != req.ReduceOnly {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed("PlaceOrder", "response reduce-only does not match request")
	}
	if result.OrderType != req.Type {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed("PlaceOrder", "response order type does not match request")
	}
	return result, result.Validate()
}

func (client *binancePerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.binancePerpReady(ctx, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := binancePerpValidateCancel(req); err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpInvalidRequest("CancelOrder", err.Error())
	}
	canonical, native, _ := binancePerpRequestSymbols(req.Instrument)
	ack := binancePerpAck(exchange.OrderOperationCancel, canonical, req.OrderID, "")
	resp, err := client.sdk.CancelOrder(ctx, binanceperp.CancelOrderParams{
		Symbol:  native,
		OrderID: req.OrderID,
	})
	if err != nil {
		return binancePerpCommandErrorAck("CancelOrder", ack, err)
	}
	return binancePerpOrderAck(exchange.OrderOperationCancel, canonical, req.OrderID, "", resp)
}

func (client *binancePerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := client.binancePerpReady(ctx, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	canonical, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OpenOrders", err.Error())
	}
	if req.Cursor != "" {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OpenOrders", "cursor is not supported by Binance open orders")
	}
	if req.Limit < 0 {
		return exchange.OrderPage{}, binancePerpInvalidRequest("OpenOrders", "limit must be non-negative")
	}
	resp, err := client.sdk.GetOpenOrders(ctx, native)
	if err != nil {
		return exchange.OrderPage{}, binancePerpNormalizeError("OpenOrders", err)
	}
	orders := make([]exchange.Order, 0, len(resp))
	for _, order := range resp {
		normalized, err := binancePerpOrder(order)
		if err != nil {
			return exchange.OrderPage{}, binancePerpMalformed("OpenOrders", err.Error())
		}
		if normalized.Instrument != canonical {
			return exchange.OrderPage{}, binancePerpMalformed("OpenOrders", "response instrument mismatch")
		}
		orders = append(orders, normalized)
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *binancePerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := client.binancePerpReady(ctx, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	canonical, native, err := binancePerpRequestSymbols(req.Instrument)
	if err != nil {
		return exchange.FillPage{}, binancePerpInvalidRequest("Fills", err.Error())
	}
	if req.Limit < 0 {
		return exchange.FillPage{}, binancePerpInvalidRequest("Fills", "limit must be non-negative")
	}
	if req.OrderID != "" {
		return exchange.FillPage{}, binancePerpInvalidRequest("Fills", "order id filter is not supported by Binance fills")
	}
	if !req.Start.IsZero() && !req.End.IsZero() && !req.End.After(req.Start) {
		return exchange.FillPage{}, binancePerpInvalidRequest("Fills", "end must be after start")
	}
	fromID, err := binancePerpParseCursor(req.Cursor)
	if err != nil {
		return exchange.FillPage{}, binancePerpInvalidRequest("Fills", "cursor must be a numeric fromId")
	}
	resp, err := client.sdk.MyTrades(ctx, native, req.Limit, binancePerpMillis(req.Start), binancePerpMillis(req.End), fromID)
	if err != nil {
		return exchange.FillPage{}, binancePerpNormalizeError("Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(resp))
	cursor := req.Cursor
	for _, trade := range resp {
		fill, err := binancePerpFill(trade)
		if err != nil {
			return exchange.FillPage{}, binancePerpMalformed("Fills", err.Error())
		}
		if fill.Instrument != canonical {
			return exchange.FillPage{}, binancePerpMalformed("Fills", "response instrument mismatch")
		}
		cursor = fill.FillID
		fills = append(fills, fill)
	}
	return exchange.FillPage{
		Fills: fills,
		Page: exchange.PageInfo{
			Cursor:      cursor,
			Limit:       req.Limit,
			WindowStart: req.Start,
			WindowEnd:   req.End,
		},
	}, nil
}

func (client *binancePerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *binancePerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := client.binancePerpReady(ctx, "PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	resp, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return exchange.PerpAccount{}, binancePerpNormalizeError("PerpAccount", err)
	}
	return binancePerpAccount(resp)
}

func (client *binancePerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := client.binancePerpReady(ctx, "Positions"); err != nil {
		return nil, err
	}
	native := ""
	canonical := ""
	if req.Instrument != "" {
		var err error
		canonical, native, err = binancePerpRequestSymbols(req.Instrument)
		if err != nil {
			return nil, binancePerpInvalidRequest("Positions", err.Error())
		}
	}
	resp, err := client.sdk.GetPositionRisk(ctx, native)
	if err != nil {
		return nil, binancePerpNormalizeError("Positions", err)
	}
	positions := make([]exchange.Position, 0, len(resp))
	for _, native := range resp {
		position, err := binancePerpPosition(native)
		if err != nil {
			return nil, binancePerpMalformed("Positions", err.Error())
		}
		if req.Instrument != "" && position.Instrument != canonical {
			return nil, binancePerpMalformed("Positions", "response instrument mismatch")
		}
		if position.Quantity.IsZero() {
			continue
		}
		positions = append(positions, position)
	}
	return positions, nil
}

func (client *binancePerpClient) binancePerpReady(ctx context.Context, operation string) error {
	if client == nil || client.sdk == nil {
		return binancePerpInvalidRequest(operation, "client is not initialized")
	}
	if ctx == nil {
		return binancePerpInvalidRequest(operation, "context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return binancePerpContextError(operation, err)
	}
	if client.sdk.EndpointPrefix != "" && client.sdk.EndpointPrefix != binancePerpPrefix {
		return binancePerpInvalidRequest(operation, "client is not configured for Binance USD-M /fapi")
	}
	return nil
}

func binancePerpInstrumentFilters(filters []map[string]interface{}) (decimal.Decimal, decimal.Decimal, decimal.Decimal, exchange.OptionalDecimal, error) {
	var priceIncrement, quantityIncrement, minQuantity decimal.Decimal
	var minNotional exchange.OptionalDecimal
	for _, filter := range filters {
		filterType, _ := filter["filterType"].(string)
		switch filterType {
		case "PRICE_FILTER":
			value, err := binancePerpDecimalFilter(filter, "tickSize")
			if err != nil {
				return decimal.Zero, decimal.Zero, decimal.Zero, minNotional, err
			}
			priceIncrement = value
		case "LOT_SIZE":
			step, err := binancePerpDecimalFilter(filter, "stepSize")
			if err != nil {
				return decimal.Zero, decimal.Zero, decimal.Zero, minNotional, err
			}
			min, err := binancePerpNonNegativeDecimalFilter(filter, "minQty")
			if err != nil {
				return decimal.Zero, decimal.Zero, decimal.Zero, minNotional, err
			}
			if min.IsZero() {
				min = step
			}
			quantityIncrement = step
			minQuantity = min
		case "MIN_NOTIONAL":
			value, err := binancePerpDecimalFilter(filter, "notional")
			if err != nil {
				return decimal.Zero, decimal.Zero, decimal.Zero, minNotional, err
			}
			minNotional = exchange.OptionalDecimal{Value: value, Valid: true}
		}
	}
	if priceIncrement.IsZero() || quantityIncrement.IsZero() || minQuantity.IsZero() {
		return decimal.Zero, decimal.Zero, decimal.Zero, minNotional, errors.New("missing required instrument filters")
	}
	return priceIncrement, quantityIncrement, minQuantity, minNotional, nil
}

func binancePerpDecimalFilter(filter map[string]interface{}, key string) (decimal.Decimal, error) {
	raw, ok := filter[key].(string)
	if !ok || raw == "" {
		return decimal.Zero, fmt.Errorf("missing %s filter", key)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fmt.Errorf("invalid %s filter", key)
	}
	return value, nil
}

func binancePerpNonNegativeDecimalFilter(filter map[string]interface{}, key string) (decimal.Decimal, error) {
	raw, ok := filter[key].(string)
	if !ok || raw == "" {
		return decimal.Zero, fmt.Errorf("missing %s filter", key)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil || value.IsNegative() {
		return decimal.Zero, fmt.Errorf("invalid %s filter", key)
	}
	return value, nil
}

func binancePerpBookLevels(native [][]string) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(native))
	for _, level := range native {
		if len(level) != 2 {
			return nil, errors.New("book level must have price and quantity")
		}
		price, err := binancePerpPositiveDecimal(level[0])
		if err != nil {
			return nil, err
		}
		quantity, err := binancePerpPositiveDecimal(level[1])
		if err != nil {
			return nil, err
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: quantity})
	}
	return levels, nil
}

func binancePerpCandle(row binanceperp.KlineResponse) (exchange.Candle, error) {
	if len(row) < 7 {
		return exchange.Candle{}, errors.New("kline row has too few fields")
	}
	openTime, err := binancePerpPositiveInt64(row[0])
	if err != nil {
		return exchange.Candle{}, err
	}
	closeTime, err := binancePerpPositiveInt64(row[6])
	if err != nil {
		return exchange.Candle{}, err
	}
	if openTime >= closeTime {
		return exchange.Candle{}, errors.New("kline open time must be before close time")
	}
	open, err := binancePerpPositiveDecimalValue(row[1])
	if err != nil {
		return exchange.Candle{}, err
	}
	high, err := binancePerpPositiveDecimalValue(row[2])
	if err != nil {
		return exchange.Candle{}, err
	}
	low, err := binancePerpPositiveDecimalValue(row[3])
	if err != nil {
		return exchange.Candle{}, err
	}
	closePrice, err := binancePerpPositiveDecimalValue(row[4])
	if err != nil {
		return exchange.Candle{}, err
	}
	volume, err := binancePerpNonNegativeDecimalValue(row[5])
	if err != nil {
		return exchange.Candle{}, err
	}
	closeAt := time.UnixMilli(closeTime).UTC()
	return exchange.Candle{
		OpenTime:  time.UnixMilli(openTime).UTC(),
		CloseTime: closeAt,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		Complete:  !closeAt.After(time.Now()),
	}, nil
}

func binancePerpOrderAck(operation exchange.OrderOperation, expectedInstrument, expectedOrderID, expectedClientOrderID string, resp *binanceperp.OrderResponse) (exchange.OrderAcknowledgement, error) {
	if resp == nil {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "missing order response")
	}
	if resp.OrderID <= 0 {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "invalid response order id")
	}
	canonical, _, err := binancePerpNativeSymbols(resp.Symbol)
	if err != nil {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), err.Error())
	}
	if canonical != expectedInstrument {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "response instrument mismatch")
	}
	nativeOrderID := strconv.FormatInt(resp.OrderID, 10)
	if expectedOrderID != "" && nativeOrderID != expectedOrderID {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "response order id mismatch")
	}
	if expectedClientOrderID != "" && resp.ClientOrderID != expectedClientOrderID {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "response client order id mismatch")
	}
	if resp.PositionSide != "" && resp.PositionSide != "BOTH" {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "hedge position side is not supported")
	}
	if resp.ClosePosition {
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "close-position order shape is not supported")
	}
	ack := binancePerpAck(operation, canonical, nativeOrderID, resp.ClientOrderID)
	switch operation {
	case exchange.OrderOperationPlace:
		switch resp.Type {
		case "MARKET":
			ack.OrderType = exchange.OrderTypeMarket
		case "LIMIT":
			ack.OrderType = exchange.OrderTypeLimit
		default:
			return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "unknown place acknowledgement order type")
		}
		switch resp.Status {
		case "NEW":
			if ack.OrderType == exchange.OrderTypeMarket {
				ack.State = exchange.AckAcceptedPending
			} else {
				ack.State = exchange.AckResting
			}
		case "PARTIALLY_FILLED":
			ack.State = exchange.AckPartiallyFilled
		case "FILLED":
			ack.State = exchange.AckImmediatelyFilled
		default:
			return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "unknown place acknowledgement status")
		}
	case exchange.OrderOperationCancel:
		ack.State = exchange.AckAcceptedPending
	default:
		return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "unknown order operation")
	}
	if operation == exchange.OrderOperationPlace {
		filled, err := binancePerpNonNegativeDecimal(resp.ExecutedQty)
		if err != nil {
			return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "invalid executed quantity")
		}
		ack.FilledQuantity = filled
		if strings.TrimSpace(resp.AvgPrice) != "" {
			avg, err := binancePerpNonNegativeDecimal(resp.AvgPrice)
			if err != nil {
				return exchange.OrderAcknowledgement{}, binancePerpMalformed(string(operation), "invalid average fill price")
			}
			if avg.IsPositive() {
				ack.AverageFillPrice = exchange.OptionalDecimal{Value: avg, Valid: true}
			}
		}
	}
	if err := ack.Validate(); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return ack, nil
}

func binancePerpCommandErrorAck(operation string, ack exchange.OrderAcknowledgement, err error) (exchange.OrderAcknowledgement, error) {
	var apiErr *binanceperp.APIError
	if errors.As(err, &apiErr) && binanceperp.IsDefinitiveOrderRejection(err) {
		ack.State = exchange.AckRejected
		ack.VenueCode = strconv.Itoa(apiErr.Code)
		ack.VenueMessage = "venue rejected order command"
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{
			Venue:       binancePerpVenue,
			Product:     binancePerpProduct,
			Operation:   operation,
			Code:        strconv.Itoa(apiErr.Code),
			SafeMessage: "venue rejected order command",
		})
	}
	if binancePerpIsKnownResponseError(err) {
		return exchange.OrderAcknowledgement{}, binancePerpNormalizeError(operation, err)
	}
	if binancePerpIsPreSendTransport(err) {
		return exchange.OrderAcknowledgement{}, binancePerpNormalizeError(operation, err)
	}
	ack.State = exchange.AckAmbiguous
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
		Venue:       binancePerpVenue,
		Product:     binancePerpProduct,
		Operation:   operation,
		SafeMessage: "order command outcome is unknown after possible send",
	})
}

func binancePerpOrder(resp binanceperp.OrderResponse) (exchange.Order, error) {
	if resp.OrderID <= 0 {
		return exchange.Order{}, errors.New("invalid order id")
	}
	if resp.UpdateTime <= 0 {
		return exchange.Order{}, errors.New("invalid order update time")
	}
	canonical, _, err := binancePerpNativeSymbols(resp.Symbol)
	if err != nil {
		return exchange.Order{}, err
	}
	if resp.PositionSide != "" && resp.PositionSide != "BOTH" {
		return exchange.Order{}, errors.New("hedge position side is not supported")
	}
	if resp.ClosePosition {
		return exchange.Order{}, errors.New("close-position order shape is not supported")
	}
	quantity, err := binancePerpNonNegativeDecimal(resp.OrigQty)
	if err != nil {
		return exchange.Order{}, err
	}
	price, err := binancePerpNonNegativeDecimal(resp.Price)
	if err != nil {
		return exchange.Order{}, err
	}
	filled, err := binancePerpNonNegativeDecimal(resp.ExecutedQty)
	if err != nil {
		return exchange.Order{}, err
	}
	avgPrice := exchange.OptionalDecimal{}
	if resp.AvgPrice != "" {
		avg, err := binancePerpNonNegativeDecimal(resp.AvgPrice)
		if err != nil {
			return exchange.Order{}, err
		}
		if !avg.IsZero() {
			avgPrice = exchange.OptionalDecimal{Value: avg, Valid: true}
		}
	}
	side, err := binancePerpExchangeSide(resp.Side)
	if err != nil {
		return exchange.Order{}, err
	}
	orderType := exchange.OrderTypeLimit
	policy := exchange.LimitPolicyResting
	switch resp.Type {
	case "MARKET":
		orderType = exchange.OrderTypeMarket
		policy = ""
	case "LIMIT", "":
		switch resp.TimeInForce {
		case "IOC":
			policy = exchange.LimitPolicyIOC
		case "GTX":
			policy = exchange.LimitPolicyPostOnly
		}
	default:
		return exchange.Order{}, errors.New("unsupported order type")
	}
	return exchange.Order{
		Instrument:       canonical,
		OrderID:          strconv.FormatInt(resp.OrderID, 10),
		ClientOrderID:    resp.ClientOrderID,
		Side:             side,
		Type:             orderType,
		Quantity:         quantity,
		LimitPrice:       price,
		LimitPolicy:      policy,
		ReduceOnly:       resp.ReduceOnly,
		Filled:           filled,
		AverageFillPrice: avgPrice,
		Status:           strings.ToLower(resp.Status),
		UpdatedAt:        time.UnixMilli(resp.UpdateTime).UTC(),
	}, nil
}

func binancePerpFill(trade binanceperp.Trade) (exchange.Fill, error) {
	if trade.ID <= 0 {
		return exchange.Fill{}, errors.New("invalid trade id")
	}
	if trade.OrderID <= 0 {
		return exchange.Fill{}, errors.New("invalid trade order id")
	}
	if trade.Time <= 0 {
		return exchange.Fill{}, errors.New("invalid trade time")
	}
	canonical, _, err := binancePerpNativeSymbols(trade.Symbol)
	if err != nil {
		return exchange.Fill{}, err
	}
	price, err := binancePerpPositiveDecimal(trade.Price)
	if err != nil {
		return exchange.Fill{}, err
	}
	quantity, err := binancePerpPositiveDecimal(trade.Qty)
	if err != nil {
		return exchange.Fill{}, err
	}
	fee, err := binancePerpNonNegativeDecimal(trade.Commission)
	if err != nil {
		return exchange.Fill{}, err
	}
	liquidity := exchange.LiquidityTaker
	if trade.IsMaker {
		liquidity = exchange.LiquidityMaker
	}
	side := exchange.SideSell
	if trade.IsBuyer {
		side = exchange.SideBuy
	}
	return exchange.Fill{
		Instrument: canonical,
		OrderID:    strconv.FormatInt(trade.OrderID, 10),
		FillID:     strconv.FormatInt(trade.ID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Fee:        fee,
		FeeAsset:   trade.CommissionAsset,
		Liquidity:  liquidity,
		Time:       time.UnixMilli(trade.Time).UTC(),
	}, nil
}

func binancePerpAccount(resp *binanceperp.AccountResponse) (exchange.PerpAccount, error) {
	if resp == nil {
		return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", "missing account response")
	}
	balances := make([]exchange.Balance, 0, len(resp.Assets))
	for _, asset := range resp.Assets {
		total, err := binancePerpNonNegativeDecimal(asset.WalletBalance)
		if err != nil {
			return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
		}
		available, err := binancePerpNonNegativeDecimal(asset.AvailableBalance)
		if err != nil {
			return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
		}
		locked := total.Sub(available)
		if locked.IsNegative() {
			locked = decimal.Zero
		}
		balances = append(balances, exchange.Balance{
			Asset:     asset.Asset,
			Available: available,
			Locked:    locked,
			Total:     total,
		})
	}
	equity, err := binancePerpOptional(resp.TotalMarginBalance)
	if err != nil {
		return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
	}
	available, err := binancePerpOptional(resp.MaxWithdrawAmount)
	if err != nil {
		return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
	}
	marginUsed := decimal.Zero
	if strings.TrimSpace(resp.TotalInitialMargin) != "" {
		marginUsed, err = binancePerpNonNegativeDecimal(resp.TotalInitialMargin)
		if err != nil {
			return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
		}
	} else {
		positionMargin, parseErr := binancePerpNonNegativeDecimal(resp.TotalPositionInitialMargin)
		if parseErr != nil {
			return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", parseErr.Error())
		}
		orderMargin, parseErr := binancePerpNonNegativeDecimal(resp.TotalOpenOrderInitialMargin)
		if parseErr != nil {
			return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", parseErr.Error())
		}
		marginUsed = positionMargin.Add(orderMargin)
	}
	unrealized, err := binancePerpOptional(resp.TotalUnrealizedProfit)
	if err != nil {
		return exchange.PerpAccount{}, binancePerpMalformed("PerpAccount", err.Error())
	}
	return exchange.PerpAccount{
		Balances:      balances,
		Equity:        equity,
		Available:     available,
		MarginUsed:    exchange.OptionalDecimal{Value: marginUsed, Valid: true},
		UnrealizedPnL: unrealized,
	}, nil
}

func binancePerpPosition(resp binanceperp.PositionRiskResponse) (exchange.Position, error) {
	canonical, _, err := binancePerpNativeSymbols(resp.Symbol)
	if err != nil {
		return exchange.Position{}, err
	}
	quantity, err := binancePerpDecimal(resp.PositionAmt)
	if err != nil {
		return exchange.Position{}, err
	}
	entry, err := binancePerpNonNegativeDecimal(resp.EntryPrice)
	if err != nil {
		return exchange.Position{}, err
	}
	mark, err := binancePerpNonNegativeDecimal(resp.MarkPrice)
	if err != nil {
		return exchange.Position{}, err
	}
	pnl, err := binancePerpDecimal(resp.UnRealizedProfit)
	if err != nil {
		return exchange.Position{}, err
	}
	liquidation, err := binancePerpOptional(resp.LiquidationPrice)
	if err != nil {
		return exchange.Position{}, err
	}
	leverage, err := binancePerpOptional(resp.Leverage)
	if err != nil {
		return exchange.Position{}, err
	}
	marginUsed, err := binancePerpOptional(resp.IsolatedMargin)
	if err != nil {
		return exchange.Position{}, err
	}
	side := exchange.SideBuy
	if quantity.IsNegative() {
		side = exchange.SideSell
	}
	return exchange.Position{
		Instrument:       canonical,
		Side:             side,
		Quantity:         quantity,
		EntryPrice:       entry,
		MarkPrice:        mark,
		UnrealizedPnL:    pnl,
		LiquidationPrice: liquidation,
		Leverage:         leverage,
		MarginUsed:       marginUsed,
	}, nil
}

func binancePerpAck(operation exchange.OrderOperation, instrument, orderID, clientOrderID string) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{
		Venue:         binancePerpVenue,
		Product:       binancePerpProduct,
		Operation:     operation,
		Instrument:    instrument,
		OrderID:       orderID,
		ClientOrderID: clientOrderID,
	}
}

func binancePerpValidatePlace(req exchange.PlaceOrderRequest) error {
	if _, _, err := binancePerpRequestSymbols(req.Instrument); err != nil {
		return err
	}
	if req.Side != exchange.SideBuy && req.Side != exchange.SideSell {
		return errors.New("side must be buy or sell")
	}
	if req.Quantity.LessThanOrEqual(decimal.Zero) {
		return errors.New("quantity must be positive")
	}
	if req.LimitPrice.LessThanOrEqual(decimal.Zero) {
		return errors.New("limit price must be positive")
	}
	if req.ClientOrderID == "" {
		return errors.New("client order id is required")
	}
	if strings.TrimSpace(req.ClientOrderID) != req.ClientOrderID {
		return errors.New("client order id must not have surrounding whitespace")
	}
	return nil
}

func binancePerpValidateCancel(req exchange.CancelOrderRequest) error {
	if _, _, err := binancePerpRequestSymbols(req.Instrument); err != nil {
		return err
	}
	orderID, err := strconv.ParseInt(req.OrderID, 10, 64)
	if err != nil || orderID <= 0 || strconv.FormatInt(orderID, 10) != req.OrderID {
		return errors.New("order id must be a positive decimal int64")
	}
	return nil
}

func binancePerpRequestSymbols(instrument string) (canonical string, native string, err error) {
	if instrument == "" {
		return "", "", errors.New("instrument is required")
	}
	if strings.TrimSpace(instrument) != instrument || strings.Contains(instrument, "/") || strings.ToUpper(instrument) != instrument {
		return "", "", errors.New("instrument must be an uppercase BASE-QUOTE Binance USD-M symbol")
	}
	parts := strings.Split(instrument, "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("instrument must be an uppercase BASE-QUOTE Binance USD-M symbol")
	}
	joined := parts[0] + parts[1]
	for _, r := range joined {
		if r < 'A' || r > 'Z' {
			if r < '0' || r > '9' {
				return "", "", errors.New("instrument must be alphanumeric")
			}
		}
	}
	for _, quote := range []string{"USDT", "USDC"} {
		if parts[1] == quote {
			return instrument, joined, nil
		}
	}
	return "", "", errors.New("instrument must be a USD-M quote symbol")
}

func binancePerpPublicInstrumentSymbols(symbol, baseAsset, quoteAsset string) (canonical string, supported bool, err error) {
	canonical, native, err := binancePerpRequestSymbols(baseAsset + "-" + quoteAsset)
	if err != nil {
		return "", false, nil
	}
	if symbol != native {
		return "", true, errors.New("USD-M symbol does not match asset metadata")
	}
	return canonical, true, nil
}

func binancePerpNativeSymbols(symbol string) (canonical string, native string, err error) {
	if symbol == "" {
		return "", "", errors.New("instrument is required")
	}
	if strings.TrimSpace(symbol) != symbol || strings.Contains(symbol, "/") {
		return "", "", errors.New("instrument must be a Binance USD-M symbol")
	}
	upper := strings.ToUpper(strings.ReplaceAll(symbol, "-", ""))
	for _, r := range upper {
		if r < 'A' || r > 'Z' {
			if r < '0' || r > '9' {
				return "", "", errors.New("instrument must be alphanumeric")
			}
		}
	}
	for _, quote := range []string{"USDT", "USDC"} {
		if strings.HasSuffix(upper, quote) && len(upper) > len(quote) {
			base := strings.TrimSuffix(upper, quote)
			return base + "-" + quote, upper, nil
		}
	}
	return "", "", errors.New("instrument must be a USD-M quote symbol")
}

func binancePerpSide(side exchange.Side) string {
	if side == exchange.SideBuy {
		return "BUY"
	}
	return "SELL"
}

func binancePerpExchangeSide(side string) (exchange.Side, error) {
	switch side {
	case "BUY":
		return exchange.SideBuy, nil
	case "SELL":
		return exchange.SideSell, nil
	default:
		return "", errors.New("unknown native order side")
	}
}

func binancePerpParseCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	return strconv.ParseInt(cursor, 10, 64)
}

func binancePerpMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func binancePerpPositiveDecimal(raw string) (decimal.Decimal, error) {
	value, err := binancePerpDecimal(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, errors.New("decimal must be positive")
	}
	return value, nil
}

func binancePerpNonNegativeDecimal(raw string) (decimal.Decimal, error) {
	value, err := binancePerpDecimal(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.IsNegative() {
		return decimal.Zero, errors.New("decimal must be non-negative")
	}
	return value, nil
}

func binancePerpDecimal(raw string) (decimal.Decimal, error) {
	if raw == "" {
		raw = "0"
	}
	return decimal.NewFromString(raw)
}

func binancePerpOptional(raw string) (exchange.OptionalDecimal, error) {
	if raw == "" {
		return exchange.OptionalDecimal{}, nil
	}
	value, err := binancePerpDecimal(raw)
	if err != nil {
		return exchange.OptionalDecimal{}, err
	}
	return exchange.OptionalDecimal{Value: value, Valid: true}, nil
}

func binancePerpPositiveDecimalValue(raw interface{}) (decimal.Decimal, error) {
	value, err := binancePerpDecimalValue(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, errors.New("decimal must be positive")
	}
	return value, nil
}

func binancePerpNonNegativeDecimalValue(raw interface{}) (decimal.Decimal, error) {
	value, err := binancePerpDecimalValue(raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.IsNegative() {
		return decimal.Zero, errors.New("decimal must be non-negative")
	}
	return value, nil
}

func binancePerpDecimalValue(raw interface{}) (decimal.Decimal, error) {
	switch value := raw.(type) {
	case string:
		return decimal.NewFromString(value)
	case float64:
		return decimal.NewFromFloat(value), nil
	default:
		return decimal.Zero, fmt.Errorf("unexpected decimal field type %T", raw)
	}
}

func binancePerpPositiveInt64(raw interface{}) (int64, error) {
	switch value := raw.(type) {
	case float64:
		if value <= 0 || math.Trunc(value) != value {
			return 0, errors.New("timestamp must be a positive integer")
		}
		return int64(value), nil
	case int64:
		if value <= 0 {
			return 0, errors.New("timestamp must be a positive integer")
		}
		return value, nil
	case string:
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, err
		}
		if parsed <= 0 {
			return 0, errors.New("timestamp must be a positive integer")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected integer field type %T", raw)
	}
}

func binancePerpNormalizeError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctxErr := binancePerpContextError(operation, err); ctxErr != nil {
		return ctxErr
	}
	var exchangeErr *sdkcore.ExchangeError
	if errors.As(err, &exchangeErr) {
		if errors.Is(err, sdkcore.ErrAuthFailed) {
			return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{
				Venue:       binancePerpVenue,
				Product:     binancePerpProduct,
				Operation:   operation,
				Code:        exchangeErr.Code,
				SafeMessage: "authentication failed",
			})
		}
		if errors.Is(err, sdkcore.ErrRateLimited) {
			return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{
				Venue:       binancePerpVenue,
				Product:     binancePerpProduct,
				Operation:   operation,
				Code:        exchangeErr.Code,
				SafeMessage: "rate limited",
			})
		}
	}
	var apiErr *binanceperp.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatus >= http.StatusInternalServerError:
			return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
				Venue:       binancePerpVenue,
				Product:     binancePerpProduct,
				Operation:   operation,
				Code:        strconv.Itoa(apiErr.Code),
				SafeMessage: "venue server error",
			})
		case apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.HTTPStatus == http.StatusForbidden:
			return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{
				Venue:       binancePerpVenue,
				Product:     binancePerpProduct,
				Operation:   operation,
				Code:        strconv.Itoa(apiErr.Code),
				SafeMessage: "authentication failed",
			})
		case apiErr.HTTPStatus == http.StatusNotFound || apiErr.Code == -2013 || apiErr.Code == -1121:
			return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{
				Venue:       binancePerpVenue,
				Product:     binancePerpProduct,
				Operation:   operation,
				Code:        strconv.Itoa(apiErr.Code),
				SafeMessage: "not found",
			})
		}
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{
			Venue:       binancePerpVenue,
			Product:     binancePerpProduct,
			Operation:   operation,
			Code:        strconv.Itoa(apiErr.Code),
			SafeMessage: "binance usd-m perp api error",
		})
	}
	if strings.Contains(err.Error(), "failed to unmarshal response") {
		return binancePerpMalformed(operation, "malformed venue response")
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
		Venue:       binancePerpVenue,
		Product:     binancePerpProduct,
		Operation:   operation,
		SafeMessage: "binance usd-m perp transport failed",
	})
}

func binancePerpContextError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{
			Venue:       binancePerpVenue,
			Product:     binancePerpProduct,
			Operation:   operation,
			SafeMessage: "context canceled",
		})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{
			Venue:       binancePerpVenue,
			Product:     binancePerpProduct,
			Operation:   operation,
			SafeMessage: "context deadline exceeded",
		})
	}
	return nil
}

func binancePerpIsKnownResponseError(err error) bool {
	var apiErr *binanceperp.APIError
	if errors.As(err, &apiErr) {
		return true
	}
	var exchangeErr *sdkcore.ExchangeError
	if errors.As(err, &exchangeErr) {
		return true
	}
	return strings.Contains(err.Error(), "failed to unmarshal response")
}

func binancePerpIsPreSendTransport(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "websocket not connected") ||
		strings.Contains(message, "connection is not established")
}

func binancePerpInvalidRequest(operation, message string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
		Venue:       binancePerpVenue,
		Product:     binancePerpProduct,
		Operation:   operation,
		SafeMessage: message,
	})
}

func binancePerpMalformed(operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{
		Venue:       binancePerpVenue,
		Product:     binancePerpProduct,
		Operation:   operation,
		SafeMessage: message,
	})
}
