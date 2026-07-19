package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	sdkcore "github.com/QuantProcessing/boltertrader/sdk"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

const (
	binanceSpotProduct = exchange.ProductSpot
	binanceSpotVenue   = exchange.VenueBinance
)

func (client *binanceSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := binanceSpotContextErr(ctx, "Instruments"); err != nil {
		return nil, err
	}
	res, err := client.sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, binanceSpotNormalizeErr(err, "Instruments")
	}
	seen := make(map[string]struct{}, len(res.Symbols))
	instruments := make([]exchange.Instrument, 0, len(res.Symbols))
	for _, symbol := range res.Symbols {
		if symbol.Status != "TRADING" {
			continue
		}
		normalized, err := binanceSpotNormalizeVenueSymbol(symbol.Symbol, symbol.BaseAsset, symbol.QuoteAsset)
		if err != nil {
			return nil, binanceSpotMalformed("Instruments", err.Error())
		}
		if _, exists := seen[normalized]; exists {
			return nil, binanceSpotMalformed("Instruments", "duplicate symbol")
		}
		seen[normalized] = struct{}{}
		filters, err := binanceSpotInstrumentFilters(symbol.Filters)
		if err != nil {
			return nil, binanceSpotMalformed("Instruments", err.Error())
		}
		instruments = append(instruments, exchange.Instrument{
			Symbol:            normalized,
			BaseAsset:         symbol.BaseAsset,
			QuoteAsset:        symbol.QuoteAsset,
			Product:           binanceSpotProduct,
			PriceIncrement:    filters.priceIncrement,
			QuantityIncrement: filters.quantityIncrement,
			MinQuantity:       filters.minQuantity,
			MinNotional:       filters.minNotional,
		})
	}
	return instruments, nil
}

func (client *binanceSpotClient) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := binanceSpotContextErr(ctx, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "OrderBook")
	if err != nil {
		return exchange.OrderBook{}, err
	}
	if request.Limit < 0 {
		return exchange.OrderBook{}, binanceSpotInvalid("OrderBook", "limit must be non-negative")
	}
	res, err := client.sdk.Depth(ctx, symbol, request.Limit)
	if err != nil {
		return exchange.OrderBook{}, binanceSpotNormalizeErr(err, "OrderBook")
	}
	bids, err := binanceSpotBookLevels(res.Bids)
	if err != nil {
		return exchange.OrderBook{}, binanceSpotMalformed("OrderBook", err.Error())
	}
	asks, err := binanceSpotBookLevels(res.Asks)
	if err != nil {
		return exchange.OrderBook{}, binanceSpotMalformed("OrderBook", err.Error())
	}
	return exchange.OrderBook{
		Instrument: instrument,
		Bids:       bids,
		Asks:       asks,
		Sequence:   strconv.FormatInt(res.LastUpdateID, 10),
		Page:       exchange.PageInfo{Limit: request.Limit},
	}, nil
}

func (client *binanceSpotClient) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := binanceSpotContextErr(ctx, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	symbol, _, err := binanceSpotSymbols(request.Instrument, "Candles")
	if err != nil {
		return exchange.CandlePage{}, err
	}
	if strings.TrimSpace(request.Interval) == "" {
		return exchange.CandlePage{}, binanceSpotInvalid("Candles", "interval is required")
	}
	if request.Cursor != "" {
		return exchange.CandlePage{}, binanceSpotInvalid("Candles", "cursor is not supported by Binance Spot klines")
	}
	if request.Limit < 0 {
		return exchange.CandlePage{}, binanceSpotInvalid("Candles", "limit must be non-negative")
	}
	if !request.Start.IsZero() && !request.End.IsZero() && !request.Start.Before(request.End) {
		return exchange.CandlePage{}, binanceSpotInvalid("Candles", "start must be before end")
	}
	res, err := client.sdk.Klines(ctx, symbol, request.Interval, request.Limit, binanceSpotMillis(request.Start), binanceSpotMillis(request.End))
	if err != nil {
		return exchange.CandlePage{}, binanceSpotNormalizeErr(err, "Candles")
	}
	candles := make([]exchange.Candle, 0, len(res))
	for _, native := range res {
		candle, err := binanceSpotCandle(native)
		if err != nil {
			return exchange.CandlePage{}, binanceSpotMalformed("Candles", err.Error())
		}
		candles = append(candles, candle)
	}
	return exchange.CandlePage{
		Candles: candles,
		Page: exchange.PageInfo{
			Limit:       request.Limit,
			WindowStart: request.Start,
			WindowEnd:   request.End,
		},
	}, nil
}

func (client *binanceSpotClient) PlaceOrder(ctx context.Context, request exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := binanceSpotContextErr(ctx, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := request.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, binanceSpotInvalid("PlaceOrder", "invalid normalized order request")
	}
	side := "BUY"
	if request.Side == exchange.SideSell {
		side = "SELL"
	}
	params := binanceSpotPlaceParams(symbol, side, request)
	res, err := client.sdk.PlaceOrder(ctx, params)
	if err != nil {
		return binanceSpotCommandAck(instrument, exchange.OrderOperationPlace, "PlaceOrder", "", request.ClientOrderID, err)
	}
	if err := binanceSpotRequireNativeSymbol(res.Symbol, symbol, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if request.ClientOrderID != "" && res.ClientOrderID != request.ClientOrderID {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "response client order id does not match request")
	}
	if res.OrderID <= 0 {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "response order id must be positive")
	}
	state, err := binanceSpotAckState(res.Status)
	if err != nil {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", err.Error())
	}
	ack := exchange.OrderAcknowledgement{
		Venue:         binanceSpotVenue,
		Product:       binanceSpotProduct,
		Operation:     exchange.OrderOperationPlace,
		State:         state,
		Instrument:    instrument,
		OrderType:     request.Type,
		OrderID:       strconv.FormatInt(res.OrderID, 10),
		ClientOrderID: res.ClientOrderID,
	}
	filled, parseErr := decimal.NewFromString(res.ExecutedQty)
	if parseErr != nil || filled.IsNegative() {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "invalid executed quantity")
	}
	ack.FilledQuantity = filled
	if filled.IsPositive() {
		quote, parseErr := decimal.NewFromString(res.CummulativeQuoteQty)
		if parseErr != nil || quote.IsNegative() {
			return exchange.OrderAcknowledgement{}, binanceSpotMalformed("PlaceOrder", "invalid cumulative quote quantity")
		}
		if quote.IsPositive() {
			ack.AverageFillPrice = exchange.OptionalDecimal{Value: quote.Div(filled), Valid: true}
		}
	}
	if err := ack.Validate(); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return ack, nil
}

func (client *binanceSpotClient) CancelOrder(ctx context.Context, request exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := binanceSpotContextErr(ctx, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	orderID, err := binanceSpotOrderID(request.OrderID, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	res, err := client.sdk.CancelOrder(ctx, symbol, orderID, "")
	if err != nil {
		return binanceSpotCommandAck(instrument, exchange.OrderOperationCancel, "CancelOrder", request.OrderID, "", err)
	}
	if err := binanceSpotRequireNativeSymbol(res.Symbol, symbol, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if res.OrderID <= 0 {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "response order id must be positive")
	}
	if request.OrderID != "" && res.OrderID != orderID {
		return exchange.OrderAcknowledgement{}, binanceSpotMalformed("CancelOrder", "response order id does not match request")
	}
	ack := exchange.OrderAcknowledgement{
		Venue:         binanceSpotVenue,
		Product:       binanceSpotProduct,
		Operation:     exchange.OrderOperationCancel,
		State:         exchange.AckAcceptedPending,
		Instrument:    instrument,
		OrderID:       strconv.FormatInt(res.OrderID, 10),
		ClientOrderID: res.ClientOrderID,
	}
	if err := ack.Validate(); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	return ack, nil
}

func (client *binanceSpotClient) OpenOrders(ctx context.Context, request exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := binanceSpotContextErr(ctx, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "OpenOrders")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	if request.Cursor != "" {
		return exchange.OrderPage{}, binanceSpotInvalid("OpenOrders", "cursor is not supported by Binance Spot open orders")
	}
	if request.Limit < 0 {
		return exchange.OrderPage{}, binanceSpotInvalid("OpenOrders", "limit must be non-negative")
	}
	res, err := client.sdk.GetOpenOrders(ctx, symbol)
	if err != nil {
		return exchange.OrderPage{}, binanceSpotNormalizeErr(err, "OpenOrders")
	}
	orders := make([]exchange.Order, 0, len(res))
	for _, native := range res {
		order, err := binanceSpotOrder(native, symbol, instrument)
		if err != nil {
			return exchange.OrderPage{}, binanceSpotMalformed("OpenOrders", err.Error())
		}
		orders = append(orders, order)
	}
	return boundedOrderPage(orders, request.Limit, ""), nil
}

func (client *binanceSpotClient) Fills(ctx context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	if err := binanceSpotContextErr(ctx, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	symbol, instrument, err := binanceSpotSymbols(request.Instrument, "Fills")
	if err != nil {
		return exchange.FillPage{}, err
	}
	if request.Limit < 0 {
		return exchange.FillPage{}, binanceSpotInvalid("Fills", "limit must be non-negative")
	}
	if request.OrderID != "" {
		return exchange.FillPage{}, binanceSpotInvalid("Fills", "order id is not supported by Binance Spot trade history")
	}
	if !request.Start.IsZero() && !request.End.IsZero() && !request.Start.Before(request.End) {
		return exchange.FillPage{}, binanceSpotInvalid("Fills", "start must be before end")
	}
	fromID := int64(0)
	if request.Cursor != "" {
		fromID, err = strconv.ParseInt(request.Cursor, 10, 64)
		if err != nil || fromID <= 0 {
			return exchange.FillPage{}, binanceSpotInvalid("Fills", "cursor must be a positive native trade id")
		}
	}
	res, err := client.sdk.MyTrades(ctx, symbol, request.Limit, binanceSpotMillis(request.Start), binanceSpotMillis(request.End), fromID)
	if err != nil {
		return exchange.FillPage{}, binanceSpotNormalizeErr(err, "Fills")
	}
	fills := make([]exchange.Fill, 0, len(res))
	for _, native := range res {
		fill, err := binanceSpotFill(native, symbol, instrument)
		if err != nil {
			return exchange.FillPage{}, binanceSpotMalformed("Fills", err.Error())
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{
		Fills: fills,
		Page: exchange.PageInfo{
			Cursor:      request.Cursor,
			Limit:       request.Limit,
			WindowStart: request.Start,
			WindowEnd:   request.End,
		},
	}, nil
}

func (client *binanceSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := binanceSpotContextErr(ctx, "Balances"); err != nil {
		return nil, err
	}
	res, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return nil, binanceSpotNormalizeErr(err, "Balances")
	}
	return binanceSpotBalances(res, "Balances")
}

func (client *binanceSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	if err := binanceSpotContextErr(ctx, "SpotAccount"); err != nil {
		return exchange.SpotAccount{}, err
	}
	res, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return exchange.SpotAccount{}, binanceSpotNormalizeErr(err, "SpotAccount")
	}
	balances, err := binanceSpotBalances(res, "SpotAccount")
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

type binanceSpotFilters struct {
	priceIncrement    decimal.Decimal
	quantityIncrement decimal.Decimal
	minQuantity       decimal.Decimal
	minNotional       exchange.OptionalDecimal
}

func binanceSpotInstrumentFilters(filters []map[string]interface{}) (binanceSpotFilters, error) {
	var got binanceSpotFilters
	var priceOK, lotOK bool
	for _, filter := range filters {
		filterType, _ := filter["filterType"].(string)
		switch filterType {
		case "PRICE_FILTER":
			tick, err := binanceSpotFilterDecimal(filter, "tickSize")
			if err != nil || !tick.IsPositive() {
				return got, fmt.Errorf("malformed price filter")
			}
			got.priceIncrement = tick
			priceOK = true
		case "LOT_SIZE":
			step, err := binanceSpotFilterDecimal(filter, "stepSize")
			if err != nil || !step.IsPositive() {
				return got, fmt.Errorf("malformed lot step filter")
			}
			minQty, err := binanceSpotFilterDecimal(filter, "minQty")
			if err != nil || !minQty.IsPositive() {
				return got, fmt.Errorf("malformed lot min filter")
			}
			got.quantityIncrement = step
			got.minQuantity = minQty
			lotOK = true
		case "MIN_NOTIONAL", "NOTIONAL":
			minNotional, err := binanceSpotFilterDecimal(filter, "minNotional")
			if err != nil || !minNotional.IsPositive() {
				return got, fmt.Errorf("malformed min notional filter")
			}
			got.minNotional = exchange.OptionalDecimal{Value: minNotional, Valid: true}
		}
	}
	if !priceOK || !lotOK {
		return got, fmt.Errorf("missing required filters")
	}
	return got, nil
}

func binanceSpotFilterDecimal(filter map[string]interface{}, key string) (decimal.Decimal, error) {
	value, ok := filter[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return decimal.Decimal{}, fmt.Errorf("missing %s", key)
	}
	return decimal.NewFromString(value)
}

func binanceSpotBookLevels(native [][]string) ([]exchange.BookLevel, error) {
	levels := make([]exchange.BookLevel, 0, len(native))
	for _, level := range native {
		if len(level) != 2 {
			return nil, fmt.Errorf("book level must have price and quantity")
		}
		price, err := decimal.NewFromString(level[0])
		if err != nil || !price.IsPositive() {
			return nil, fmt.Errorf("book price must be positive decimal")
		}
		quantity, err := decimal.NewFromString(level[1])
		if err != nil || !quantity.IsPositive() {
			return nil, fmt.Errorf("book quantity must be positive decimal")
		}
		levels = append(levels, exchange.BookLevel{Price: price, Quantity: quantity})
	}
	return levels, nil
}

func binanceSpotCandle(native binancespot.KlineResponse) (exchange.Candle, error) {
	if len(native) < 7 {
		return exchange.Candle{}, fmt.Errorf("kline row has fewer than 7 fields")
	}
	openTime, err := binanceSpotKlineMillis(native[0])
	if err != nil {
		return exchange.Candle{}, err
	}
	open, err := binanceSpotKlineDecimal(native[1])
	if err != nil {
		return exchange.Candle{}, err
	}
	high, err := binanceSpotKlineDecimal(native[2])
	if err != nil {
		return exchange.Candle{}, err
	}
	low, err := binanceSpotKlineDecimal(native[3])
	if err != nil {
		return exchange.Candle{}, err
	}
	closePrice, err := binanceSpotKlineDecimal(native[4])
	if err != nil {
		return exchange.Candle{}, err
	}
	volume, err := binanceSpotKlineDecimal(native[5])
	if err != nil {
		return exchange.Candle{}, err
	}
	closeTime, err := binanceSpotKlineMillis(native[6])
	if err != nil {
		return exchange.Candle{}, err
	}
	if !openTime.Before(closeTime) {
		return exchange.Candle{}, fmt.Errorf("kline open time must be before close time")
	}
	for _, value := range []decimal.Decimal{open, high, low, closePrice} {
		if !value.IsPositive() {
			return exchange.Candle{}, fmt.Errorf("kline OHLC prices must be positive")
		}
	}
	if volume.IsNegative() {
		return exchange.Candle{}, fmt.Errorf("kline volume must be non-negative")
	}
	return exchange.Candle{
		OpenTime:  openTime,
		CloseTime: closeTime,
		Open:      open,
		High:      high,
		Low:       low,
		Close:     closePrice,
		Volume:    volume,
		Complete:  !time.Now().UTC().Before(closeTime),
	}, nil
}

func binanceSpotOrder(native binancespot.OrderResponse, expectedSymbol, instrument string) (exchange.Order, error) {
	if native.Symbol != expectedSymbol {
		return exchange.Order{}, fmt.Errorf("order symbol does not match requested instrument")
	}
	if native.OrderID <= 0 {
		return exchange.Order{}, fmt.Errorf("order id must be positive")
	}
	quantity, err := decimal.NewFromString(native.OrigQty)
	if err != nil || quantity.IsNegative() {
		return exchange.Order{}, fmt.Errorf("order quantity must be non-negative decimal")
	}
	price, err := decimal.NewFromString(native.Price)
	if err != nil || price.IsNegative() {
		return exchange.Order{}, fmt.Errorf("order price must be non-negative decimal")
	}
	filled, err := decimal.NewFromString(native.ExecutedQty)
	if err != nil || filled.IsNegative() {
		return exchange.Order{}, fmt.Errorf("order filled quantity must be non-negative decimal")
	}
	side, err := binanceSpotSide(native.Side)
	if err != nil {
		return exchange.Order{}, err
	}
	orderType := exchange.OrderTypeLimit
	limitPolicy := exchange.LimitPolicyResting
	switch native.Type {
	case "MARKET":
		orderType = exchange.OrderTypeMarket
		limitPolicy = ""
	case "LIMIT_MAKER":
		limitPolicy = exchange.LimitPolicyPostOnly
	case "LIMIT", "":
		if native.TimeInForce == "IOC" {
			limitPolicy = exchange.LimitPolicyIOC
		}
	default:
		return exchange.Order{}, fmt.Errorf("unsupported order type")
	}
	order := exchange.Order{
		Instrument:    instrument,
		OrderID:       strconv.FormatInt(native.OrderID, 10),
		ClientOrderID: native.ClientOrderID,
		Side:          side,
		Type:          orderType,
		Quantity:      quantity,
		LimitPrice:    price,
		LimitPolicy:   limitPolicy,
		Filled:        filled,
		Status:        native.Status,
	}
	if quote, err := decimal.NewFromString(native.CummulativeQuoteQty); err == nil && filled.IsPositive() && quote.IsPositive() {
		order.AverageFillPrice = exchange.OptionalDecimal{Value: quote.Div(filled), Valid: true}
	}
	return order, nil
}

func binanceSpotFill(native binancespot.Trade, expectedSymbol, instrument string) (exchange.Fill, error) {
	if native.Symbol != expectedSymbol {
		return exchange.Fill{}, fmt.Errorf("fill symbol does not match requested instrument")
	}
	if native.ID <= 0 {
		return exchange.Fill{}, fmt.Errorf("fill id must be positive")
	}
	if native.OrderID <= 0 {
		return exchange.Fill{}, fmt.Errorf("fill order id must be positive")
	}
	if native.Time <= 0 {
		return exchange.Fill{}, fmt.Errorf("fill time must be positive")
	}
	price, err := decimal.NewFromString(native.Price)
	if err != nil || !price.IsPositive() {
		return exchange.Fill{}, fmt.Errorf("fill price must be positive decimal")
	}
	quantity, err := decimal.NewFromString(native.Qty)
	if err != nil || !quantity.IsPositive() {
		return exchange.Fill{}, fmt.Errorf("fill quantity must be positive decimal")
	}
	fee, err := decimal.NewFromString(native.Commission)
	if err != nil || fee.IsNegative() {
		return exchange.Fill{}, fmt.Errorf("fill fee must be non-negative decimal")
	}
	side := exchange.SideSell
	if native.IsBuyer {
		side = exchange.SideBuy
	}
	liquidity := exchange.LiquidityTaker
	if native.IsMaker {
		liquidity = exchange.LiquidityMaker
	}
	return exchange.Fill{
		Instrument: instrument,
		OrderID:    strconv.FormatInt(native.OrderID, 10),
		FillID:     strconv.FormatInt(native.ID, 10),
		Side:       side,
		Price:      price,
		Quantity:   quantity,
		Fee:        fee,
		FeeAsset:   native.CommissionAsset,
		Liquidity:  liquidity,
		Time:       time.UnixMilli(native.Time).UTC(),
	}, nil
}

func binanceSpotBalances(res *binancespot.AccountResponse, operation string) ([]exchange.Balance, error) {
	if res == nil {
		return nil, binanceSpotMalformed(operation, "missing account response")
	}
	balances := make([]exchange.Balance, 0, len(res.Balances))
	for _, native := range res.Balances {
		if native.Asset == "" {
			return nil, binanceSpotMalformed(operation, "balance asset is required")
		}
		free, err := decimal.NewFromString(native.Free)
		if err != nil || free.IsNegative() {
			return nil, binanceSpotMalformed(operation, "balance free must be non-negative decimal")
		}
		locked, err := decimal.NewFromString(native.Locked)
		if err != nil || locked.IsNegative() {
			return nil, binanceSpotMalformed(operation, "balance locked must be non-negative decimal")
		}
		balances = append(balances, exchange.Balance{
			Asset:     native.Asset,
			Available: free,
			Locked:    locked,
			Total:     free.Add(locked),
		})
	}
	return balances, nil
}

func binanceSpotNormalizeVenueSymbol(symbol, base, quote string) (string, error) {
	if strings.TrimSpace(symbol) == "" || strings.TrimSpace(base) == "" || strings.TrimSpace(quote) == "" {
		return "", fmt.Errorf("symbol, base, and quote are required")
	}
	if symbol != strings.ToUpper(symbol) {
		return "", fmt.Errorf("symbol must be uppercase")
	}
	if symbol != base+quote {
		return "", fmt.Errorf("symbol does not match base and quote assets")
	}
	return base + "-" + quote, nil
}

func binanceSpotSymbols(instrument, operation string) (venueSymbol string, canonical string, err error) {
	parts := strings.Split(strings.TrimSpace(instrument), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", binanceSpotInvalid(operation, "instrument must be a normalized spot symbol like BTC-USDT")
	}
	if parts[0] != strings.ToUpper(parts[0]) || parts[1] != strings.ToUpper(parts[1]) {
		return "", "", binanceSpotInvalid(operation, "instrument must use canonical uppercase BASE-QUOTE form")
	}
	canonical = parts[0] + "-" + parts[1]
	return parts[0] + parts[1], canonical, nil
}

func binanceSpotRequireNativeSymbol(got, want, operation string) error {
	if got != want {
		return binanceSpotMalformed(operation, "response symbol does not match requested instrument")
	}
	return nil
}

func binanceSpotOrderID(orderID, operation string) (int64, error) {
	if strings.TrimSpace(orderID) == "" {
		return 0, binanceSpotInvalid(operation, "order id is required")
	}
	parsed, err := strconv.ParseInt(orderID, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != orderID {
		return 0, binanceSpotInvalid(operation, "order id must be a positive integer")
	}
	return parsed, nil
}

func binanceSpotAckState(status string) (exchange.OrderAckState, error) {
	switch status {
	case "NEW":
		return exchange.AckResting, nil
	case "PARTIALLY_FILLED":
		return exchange.AckPartiallyFilled, nil
	case "FILLED":
		return exchange.AckImmediatelyFilled, nil
	default:
		return "", fmt.Errorf("unsupported order acknowledgement status %q", status)
	}
}

func binanceSpotCommandAck(instrument string, operation exchange.OrderOperation, operationName, orderID, clientOrderID string, err error) (exchange.OrderAcknowledgement, error) {
	apiErr, apiOK := binanceSpotAPIError(err)
	if binancespot.IsDefinitiveOrderRejection(err) && apiOK {
		ack := exchange.OrderAcknowledgement{
			Venue:         binanceSpotVenue,
			Product:       binanceSpotProduct,
			Operation:     operation,
			State:         exchange.AckRejected,
			Instrument:    instrument,
			OrderID:       orderID,
			ClientOrderID: clientOrderID,
			VenueCode:     strconv.Itoa(apiErr.Code),
			VenueMessage:  "venue rejected request",
		}
		if validateErr := ack.Validate(); validateErr != nil {
			return exchange.OrderAcknowledgement{}, validateErr
		}
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{
			Venue:       binanceSpotVenue,
			Product:     binanceSpotProduct,
			Operation:   operationName,
			Code:        strconv.Itoa(apiErr.Code),
			SafeMessage: "venue rejected request",
		})
	}
	if errors.Is(err, binancespot.ErrWSOutcomeUnknown) || binanceSpotIsTransport(err) {
		ack := exchange.OrderAcknowledgement{
			Venue:         binanceSpotVenue,
			Product:       binanceSpotProduct,
			Operation:     operation,
			State:         exchange.AckAmbiguous,
			Instrument:    instrument,
			OrderID:       orderID,
			ClientOrderID: clientOrderID,
			VenueMessage:  "transport outcome unknown",
		}
		if validateErr := ack.Validate(); validateErr != nil {
			return exchange.OrderAcknowledgement{}, validateErr
		}
		return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{
			Venue:       binanceSpotVenue,
			Product:     binanceSpotProduct,
			Operation:   operationName,
			SafeMessage: "transport outcome unknown",
		})
	}
	return exchange.OrderAcknowledgement{}, binanceSpotNormalizeErr(err, operationName)
}

func binanceSpotNormalizeErr(err error, operation string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return binanceSpotContextError(exchange.KindCanceled, operation)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return binanceSpotContextError(exchange.KindDeadlineExceeded, operation)
	}
	var exchangeErr *sdkcore.ExchangeError
	if errors.As(err, &exchangeErr) {
		if errors.Is(err, sdkcore.ErrAuthFailed) {
			return exchange.NewError(exchange.KindAuthentication, exchange.ErrorDetails{
				Venue:       binanceSpotVenue,
				Product:     binanceSpotProduct,
				Operation:   operation,
				Code:        exchangeErr.Code,
				SafeMessage: "authentication failed",
			})
		}
		if errors.Is(err, sdkcore.ErrRateLimited) {
			return exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{
				Venue:       binanceSpotVenue,
				Product:     binanceSpotProduct,
				Operation:   operation,
				Code:        exchangeErr.Code,
				SafeMessage: "rate limited",
			})
		}
	}
	if apiErr, ok := binanceSpotAPIError(err); ok {
		if apiErr.HTTPStatus >= http.StatusInternalServerError {
			return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
				Venue:       binanceSpotVenue,
				Product:     binanceSpotProduct,
				Operation:   operation,
				Code:        strconv.Itoa(apiErr.Code),
				SafeMessage: "venue server error",
			})
		}
		if apiErr.Code == -1121 || apiErr.Code == -2013 || apiErr.HTTPStatus == http.StatusNotFound {
			return exchange.NewError(exchange.KindNotFound, exchange.ErrorDetails{
				Venue:       binanceSpotVenue,
				Product:     binanceSpotProduct,
				Operation:   operation,
				Code:        strconv.Itoa(apiErr.Code),
				SafeMessage: "not found",
			})
		}
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{
			Venue:       binanceSpotVenue,
			Product:     binanceSpotProduct,
			Operation:   operation,
			Code:        strconv.Itoa(apiErr.Code),
			SafeMessage: "venue rejected request",
		})
	}
	if strings.Contains(err.Error(), "failed to unmarshal response") {
		return binanceSpotMalformed(operation, "malformed venue response")
	}
	if binanceSpotIsTransport(err) {
		return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
			Venue:       binanceSpotVenue,
			Product:     binanceSpotProduct,
			Operation:   operation,
			SafeMessage: "transport failed",
		})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{
		Venue:       binanceSpotVenue,
		Product:     binanceSpotProduct,
		Operation:   operation,
		SafeMessage: "request failed",
	})
}

func binanceSpotAPIError(err error) (*binancespot.APIError, bool) {
	var apiErr *binancespot.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr, true
	}
	return nil, false
}

func binanceSpotIsTransport(err error) bool {
	return err != nil && strings.Contains(err.Error(), "transport failed")
}

func binanceSpotSide(side string) (exchange.Side, error) {
	switch side {
	case "BUY":
		return exchange.SideBuy, nil
	case "SELL":
		return exchange.SideSell, nil
	default:
		return "", fmt.Errorf("unknown side %q", side)
	}
}

func binanceSpotKlineDecimal(value interface{}) (decimal.Decimal, error) {
	text, ok := value.(string)
	if !ok {
		return decimal.Decimal{}, fmt.Errorf("kline decimal must be a string")
	}
	return decimal.NewFromString(text)
}

func binanceSpotKlineMillis(value interface{}) (time.Time, error) {
	switch typed := value.(type) {
	case float64:
		if typed <= 0 || typed != float64(int64(typed)) {
			return time.Time{}, fmt.Errorf("kline timestamp must be integer milliseconds")
		}
		return time.UnixMilli(int64(typed)).UTC(), nil
	case int64:
		if typed <= 0 {
			return time.Time{}, fmt.Errorf("kline timestamp must be positive")
		}
		return time.UnixMilli(typed).UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("kline timestamp has unexpected type")
	}
}

func binanceSpotMillis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func binanceSpotContextErr(ctx context.Context, operation string) error {
	if ctx == nil {
		return binanceSpotInvalid(operation, "context is required")
	}
	switch err := ctx.Err(); {
	case errors.Is(err, context.Canceled):
		return binanceSpotContextError(exchange.KindCanceled, operation)
	case errors.Is(err, context.DeadlineExceeded):
		return binanceSpotContextError(exchange.KindDeadlineExceeded, operation)
	default:
		return nil
	}
}

func binanceSpotContextError(kind exchange.ErrorKind, operation string) error {
	return exchange.NewError(kind, exchange.ErrorDetails{
		Venue:     binanceSpotVenue,
		Product:   binanceSpotProduct,
		Operation: operation,
	})
}

func binanceSpotInvalid(operation, message string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
		Venue:       binanceSpotVenue,
		Product:     binanceSpotProduct,
		Operation:   operation,
		SafeMessage: message,
	})
}

func binanceSpotMalformed(operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{
		Venue:       binanceSpotVenue,
		Product:     binanceSpotProduct,
		Operation:   operation,
		SafeMessage: message,
	})
}
