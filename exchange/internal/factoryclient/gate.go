package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type gateSpotClient struct {
	*spotClient
	sdk *gate.Client
	ws  exchange.SpotWebSocket
}

type gatePerpClient struct {
	*perpClient
	sdk          *gate.Client
	ws           exchange.PerpWebSocket
	settle       string
	multiplierMu sync.RWMutex
	multipliers  map[string]decimal.Decimal
}

func NewGateSpot(apiKey, secretKey string, settings Settings) exchange.SpotClient {
	sdkClient, profile := newGateSDK(apiKey, secretKey, settings)
	client := &gateSpotClient{
		spotClient: &spotClient{meta: clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}},
		sdk:        sdkClient,
	}
	wsClient, _ := gate.NewWSClientWithProfile(profile, gate.ProductSpot)
	if wsClient == nil {
		wsClient = gate.MustNewWSClient(gate.ProductSpot)
	}
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	wsClient.WithCredentials(apiKey, secretKey)
	backend := newGateSpotWSBackend(wsClient, client)
	client.ws = newSpotWebSocket(newPublicWebSocket(client.meta, backend), backend)
	return client
}

func NewGateUSDTPerp(apiKey, secretKey string, settings Settings) exchange.PerpClient {
	sdkClient, profile := newGateSDK(apiKey, secretKey, settings)
	client := &gatePerpClient{
		perpClient:  &perpClient{meta: clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}},
		sdk:         sdkClient,
		settle:      gate.SettleUSDT,
		multipliers: make(map[string]decimal.Decimal),
	}
	wsClient, _ := gate.NewWSClientWithProfile(profile, gate.ProductFuturesUSDT)
	if wsClient == nil {
		wsClient = gate.MustNewWSClient(gate.ProductFuturesUSDT)
	}
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	wsClient.WithCredentials(apiKey, secretKey)
	backend := newGatePerpWSBackend(wsClient, client)
	client.ws = newPerpWebSocket(client.meta, backend, backend)
	return client
}

func newGateSDK(apiKey, secretKey string, settings Settings) (*gate.Client, gate.EnvironmentProfile) {
	profile := gate.MainnetEnvironmentProfile()
	if settings.Environment == "demo" || settings.Environment == "testnet" {
		profile = gate.TestnetEnvironmentProfile()
	}
	if settings.Endpoint != "" {
		profile.RESTBaseURL = strings.TrimRight(settings.Endpoint, "/")
	}
	if settings.WebSocketEndpoint != "" {
		profile.SpotWSURL = settings.WebSocketEndpoint
		profile.FuturesUSDTWSURL = settings.WebSocketEndpoint
	}
	client := gate.NewClient().WithCredentials(apiKey, secretKey).WithEnvironmentProfile(profile)
	if settings.HTTPClient != nil {
		client.WithHTTPClient(settings.HTTPClient)
	}
	return client, profile
}

func (client *gateSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }
func (client *gatePerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }
func (client *gatePerpClient) gateFuturesUserID(ctx context.Context) (string, error) {
	account, err := client.sdk.GetFuturesAccount(ctx, client.settle)
	if err != nil {
		return "", gateNormalizeErr(client.meta, "Watch", err)
	}
	if account.User <= 0 {
		return "", gateMalformed(client.meta, "Watch", "gate futures account user id is missing")
	}
	return strconv.FormatInt(account.User, 10), nil
}

func (client *gatePerpClient) gateContractMultiplier(ctx context.Context, operation, instrument string) (decimal.Decimal, error) {
	client.multiplierMu.RLock()
	multiplier, ok := client.multipliers[instrument]
	client.multiplierMu.RUnlock()
	if ok {
		return multiplier, nil
	}
	contract, err := client.sdk.GetFuturesContract(ctx, client.settle, instrument)
	if err != nil {
		return decimal.Zero, gateNormalizeErr(client.meta, operation, err)
	}
	multiplier, err = gateFuturesMultiplier(client.meta, operation, contract.QuantoMultiplier)
	if err != nil {
		return decimal.Zero, err
	}
	client.multiplierMu.Lock()
	client.multipliers[instrument] = multiplier
	client.multiplierMu.Unlock()
	return multiplier, nil
}
func (client *gateSpotClient) String() string {
	if client == nil || client.spotClient == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.spotClient.String()
}
func (client *gateSpotClient) GoString() string { return client.String() }
func (client *gatePerpClient) String() string {
	if client == nil || client.perpClient == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return client.perpClient.String()
}
func (client *gatePerpClient) GoString() string { return client.String() }
func (client *gateSpotClient) Close() error {
	if client == nil || client.ws == nil {
		return nil
	}
	return client.ws.Close()
}
func (client *gatePerpClient) Close() error {
	if client == nil || client.ws == nil {
		return nil
	}
	return client.ws.Close()
}

func (client *gateSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := gateContext(ctx, client.meta, "Instruments"); err != nil {
		return nil, err
	}
	rows, err := client.sdk.ListCurrencyPairs(ctx)
	if err != nil {
		return nil, gateNormalizeErr(client.meta, "Instruments", err)
	}
	if err := gateValidateSpotInstruments(client.meta, "Instruments", rows); err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.TradeStatus != "" && row.TradeStatus != "tradable" {
			continue
		}
		out = append(out, exchange.Instrument{
			Symbol:            row.ID,
			BaseAsset:         row.Base,
			QuoteAsset:        row.Quote,
			Product:           exchange.ProductSpot,
			PriceIncrement:    decimal.New(1, -int32(row.Precision)),
			QuantityIncrement: decimal.New(1, -int32(row.AmountPrecision)),
			MinQuantity:       gateDecimal(row.MinBaseAmount),
			MinNotional:       gateOptionalDecimal(row.MinQuoteAmount),
		})
	}
	return out, nil
}

func (client *gatePerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := gateContext(ctx, client.meta, "Instruments"); err != nil {
		return nil, err
	}
	rows, err := client.sdk.ListFuturesContracts(ctx, client.settle)
	if err != nil {
		return nil, gateNormalizeErr(client.meta, "Instruments", err)
	}
	if err := gateValidateFuturesInstruments(client.meta, "Instruments", rows); err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.Status != "" && row.Status != "trading" {
			continue
		}
		multiplier, err := gateFuturesMultiplier(client.meta, "Instruments", row.QuantoMultiplier)
		if err != nil {
			return nil, err
		}
		client.multiplierMu.Lock()
		client.multipliers[row.Name] = multiplier
		client.multiplierMu.Unlock()
		base, quote := gateSplitSymbol(row.Name)
		out = append(out, exchange.Instrument{
			Symbol:            row.Name,
			BaseAsset:         base,
			QuoteAsset:        quote,
			SettleAsset:       strings.ToUpper(client.settle),
			Product:           exchange.ProductPerp,
			PriceIncrement:    gateDecimal(row.OrderPriceRound),
			QuantityIncrement: multiplier,
			MinQuantity:       decimal.NewFromInt(row.OrderSizeMin).Mul(multiplier),
		})
	}
	return out, nil
}

func (client *gateSpotClient) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := gateContext(ctx, client.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	book, err := client.sdk.GetSpotOrderBook(ctx, request.Instrument, request.Limit, true)
	if err != nil {
		return exchange.OrderBook{}, gateNormalizeErr(client.meta, "OrderBook", err)
	}
	if err := gateValidateSpotOrderBook(client.meta, "OrderBook", *book); err != nil {
		return exchange.OrderBook{}, err
	}
	return exchange.OrderBook{Instrument: request.Instrument, Bids: gateBookLevels(book.Bids), Asks: gateBookLevels(book.Asks), Time: gateUnixMilli(book.Update), Sequence: strconv.FormatInt(book.ID, 10), Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func (client *gatePerpClient) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := gateContext(ctx, client.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	book, err := client.sdk.GetFuturesOrderBook(ctx, client.settle, request.Instrument, request.Limit, true)
	if err != nil {
		return exchange.OrderBook{}, gateNormalizeErr(client.meta, "OrderBook", err)
	}
	if err := gateValidateFuturesOrderBook(client.meta, "OrderBook", *book); err != nil {
		return exchange.OrderBook{}, err
	}
	multiplier, err := client.gateContractMultiplier(ctx, "OrderBook", request.Instrument)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	return exchange.OrderBook{Instrument: request.Instrument, Bids: gateFuturesBookLevels(book.Bids, multiplier), Asks: gateFuturesBookLevels(book.Asks, multiplier), Time: gateUnixMilliString(string(book.Update)), Sequence: strconv.FormatInt(book.ID, 10), Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func (client *gateSpotClient) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := gateContext(ctx, client.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	interval, err := gateValidateCandleRequest(client.meta, request)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	rows, err := client.sdk.ListSpotCandlesticksWindow(ctx, request.Instrument, request.Interval, request.Start, request.End, request.Limit)
	if err != nil {
		return exchange.CandlePage{}, gateNormalizeErr(client.meta, "Candles", err)
	}
	if err := gateValidateCandles(client.meta, "Candles", rows); err != nil {
		return exchange.CandlePage{}, err
	}
	return gateCandlePage(rows, request, interval), nil
}

func (client *gatePerpClient) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := gateContext(ctx, client.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	interval, err := gateValidateCandleRequest(client.meta, request)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	rows, err := client.sdk.ListFuturesCandlesticksWindow(ctx, client.settle, request.Instrument, request.Interval, request.Start, request.End, request.Limit)
	if err != nil {
		return exchange.CandlePage{}, gateNormalizeErr(client.meta, "Candles", err)
	}
	candlesticks := gateFuturesCandlesticks(rows)
	if err := gateValidateCandles(client.meta, "Candles", candlesticks); err != nil {
		return exchange.CandlePage{}, err
	}
	return gateCandlePage(candlesticks, request, interval), nil
}

func (client *gateSpotClient) PublicTrades(ctx context.Context, request exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := gateContext(ctx, client.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	rows, err := client.sdk.ListSpotTrades(ctx, request.Instrument, request.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, gateNormalizeErr(client.meta, "PublicTrades", err)
	}
	if err := gateValidateSpotPublicTrades(client.meta, "PublicTrades", rows); err != nil {
		return exchange.PublicTradePage{}, err
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		trades = append(trades, exchange.PublicTrade{Instrument: row.CurrencyPair, TradeID: row.ID, Side: gateSide(row.Side), Price: gateDecimal(row.Price), Quantity: gateDecimal(row.Amount), Time: gateTimeMS(row.CreateTimeMS)})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func (client *gatePerpClient) PublicTrades(ctx context.Context, request exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := gateContext(ctx, client.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	rows, err := client.sdk.ListFuturesTrades(ctx, client.settle, request.Instrument, request.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, gateNormalizeErr(client.meta, "PublicTrades", err)
	}
	if err := gateValidateFuturesPublicTrades(client.meta, "PublicTrades", rows); err != nil {
		return exchange.PublicTradePage{}, err
	}
	multiplier, err := client.gateContractMultiplier(ctx, "PublicTrades", request.Instrument)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	trades := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		side := exchange.SideBuy
		size := gateDecimal(string(row.Size))
		if size.IsNegative() {
			side = exchange.SideSell
		}
		trades = append(trades, exchange.PublicTrade{Instrument: row.Contract, TradeID: strconv.FormatInt(row.ID, 10), Side: side, Price: gateDecimal(row.Price), Quantity: size.Abs().Mul(multiplier), Time: gateUnixSecondDecimalString(string(row.CreateTime))})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: request.Limit}}, nil
}

func gateNormalizeErr(meta clientMeta, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "operation canceled"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "operation deadline exceeded"})
	}
	var apiErr *gate.APIError
	if errors.As(err, &apiErr) {
		kind := exchange.KindVenueRejected
		switch apiErr.StatusCode {
		case http.StatusUnauthorized:
			kind = exchange.KindAuthentication
		case http.StatusForbidden:
			kind = exchange.KindPermission
		case http.StatusNotFound:
			kind = exchange.KindNotFound
		case http.StatusTooManyRequests:
			kind = exchange.KindRateLimit
		}
		return exchange.NewError(kind, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, Code: apiErr.Label, SafeMessage: "gate request failed"})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "gate transport failed"})
}

func gateCommandErr(meta clientMeta, operation exchange.OrderOperation, instrument, orderID, clientID string, err error) (exchange.OrderAcknowledgement, error) {
	ack := exchange.OrderAcknowledgement{Venue: meta.venue, Product: meta.product, Operation: operation, State: exchange.AckAmbiguous, Instrument: instrument, OrderID: orderID, ClientOrderID: clientID}
	if gate.IsDefinitiveCommandRejection(err) {
		ack.State = exchange.AckRejected
		ack.VenueMessage = "gate rejected command"
		return ack, gateNormalizeErr(meta, string(operation), err)
	}
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: string(operation), SafeMessage: "gate command outcome is ambiguous"})
}

func gateWriteErr(meta clientMeta, operation string, err error) error {
	if gate.IsDefinitiveCommandRejection(err) {
		return gateNormalizeErr(meta, operation, err)
	}
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "operation canceled"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "gate command outcome is ambiguous after deadline"})
	}
	return exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "gate command outcome is ambiguous"})
}

func gateContext(ctx context.Context, meta clientMeta, operation string) error {
	if ctx == nil {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "context is required"})
	}
	return nil
}

func gateMalformed(meta clientMeta, operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: message})
}

func gateDecimal(value string) decimal.Decimal {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero
	}
	got, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero
	}
	return got
}

func gateOptionalDecimal(value string) exchange.OptionalDecimal {
	got := gateDecimal(value)
	return exchange.OptionalDecimal{Value: got, Valid: strings.TrimSpace(value) != ""}
}

func gateBookLevels(rows [][]gate.NumberString) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) >= 2 {
			out = append(out, exchange.BookLevel{Price: gateDecimal(string(row[0])), Quantity: gateDecimal(string(row[1]))})
		}
	}
	return out
}

func gateFuturesBookLevels(rows []gate.FuturesOrderBookItem, multiplier decimal.Decimal) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.BookLevel{Price: gateDecimal(row.Price), Quantity: decimal.NewFromInt(row.Size).Mul(multiplier)})
	}
	return out
}

func gateFuturesMultiplier(meta clientMeta, operation, value string) (decimal.Decimal, error) {
	multiplier, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || !multiplier.IsPositive() {
		return decimal.Zero, gateMalformed(meta, operation, "invalid gate futures contract multiplier")
	}
	return multiplier, nil
}

func gateCandlePage(rows []gate.Candlestick, request exchange.CandlesRequest, interval time.Duration) exchange.CandlePage {
	candles := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		openTime := gateUnixSecondString(string(row[0]))
		candles = append(candles, exchange.Candle{
			OpenTime:  openTime,
			CloseTime: openTime.Add(interval),
			Open:      gateDecimal(string(row[5])),
			High:      gateDecimal(string(row[3])),
			Low:       gateDecimal(string(row[4])),
			Close:     gateDecimal(string(row[2])),
			Volume:    gateDecimal(string(row[1])),
			Complete:  true,
		})
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: request.Limit, WindowStart: request.Start, WindowEnd: request.End}}
}

func gateFuturesCandlesticks(rows []gate.FuturesCandlestick) []gate.Candlestick {
	out := make([]gate.Candlestick, 0, len(rows))
	for _, row := range rows {
		out = append(out, gate.Candlestick{row.Time, row.Volume, row.Close, row.High, row.Low, row.Open})
	}
	return out
}

func gateSide(value string) exchange.Side {
	if strings.EqualFold(value, "sell") {
		return exchange.SideSell
	}
	return exchange.SideBuy
}

func gateParseSide(value string) (exchange.Side, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "buy":
		return exchange.SideBuy, nil
	case "sell":
		return exchange.SideSell, nil
	default:
		return "", fmt.Errorf("invalid side %q", value)
	}
}

func gateParseDecimal(value string) (decimal.Decimal, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return decimal.Zero, nil
	}
	return decimal.NewFromString(text)
}

func gateValidateDecimal(meta clientMeta, operation, field, value string) error {
	if _, err := gateParseDecimal(value); err != nil {
		return gateMalformed(meta, operation, "invalid gate "+field)
	}
	return nil
}

func gateValidateSide(meta clientMeta, operation, value string) error {
	if _, err := gateParseSide(value); err != nil {
		return gateMalformed(meta, operation, "invalid gate side")
	}
	return nil
}

func gateValidateStatus(meta clientMeta, operation, status, finishAs string) error {
	if value := strings.ToLower(strings.TrimSpace(status)); value != "" {
		switch value {
		case "open", "closed", "cancelled", "finished":
		default:
			return gateMalformed(meta, operation, "invalid gate order status")
		}
	}
	if value := strings.ToLower(strings.TrimSpace(finishAs)); value != "" {
		switch value {
		case "_new", "_update", "open", "unknown", "filled", "cancelled", "liquidated", "ioc", "poc", "stp",
			"auto_deleveraging", "auto_deleveraged", "reduce_only", "position_close",
			"position_closed", "reduce_out", "cb", "expired", "succeeded", "failed",
			"small", "depth_not_enough", "trader_not_enough":
		default:
			return gateMalformed(meta, operation, "invalid gate order finish status")
		}
	}
	return nil
}

func gateValidateCandleRequest(meta clientMeta, request exchange.CandlesRequest) (time.Duration, error) {
	if strings.TrimSpace(request.Instrument) == "" {
		return 0, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Candles", SafeMessage: "instrument is required"})
	}
	if strings.TrimSpace(request.Cursor) != "" {
		return 0, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Candles", SafeMessage: "cursor is not supported"})
	}
	if !request.Start.IsZero() && !request.End.IsZero() && !request.Start.Before(request.End) {
		return 0, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Candles", SafeMessage: "start must be before end"})
	}
	interval, err := gateIntervalDuration(request.Interval)
	if err != nil {
		return 0, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "Candles", SafeMessage: err.Error()})
	}
	return interval, nil
}

func gateIntervalDuration(interval string) (time.Duration, error) {
	switch strings.TrimSpace(interval) {
	case "10s":
		return 10 * time.Second, nil
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "30m":
		return 30 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "8h":
		return 8 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	case "7d":
		return 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unsupported interval")
	}
}

func gateValidateCandles(meta clientMeta, operation string, rows []gate.Candlestick) error {
	for _, row := range rows {
		if len(row) < 6 {
			return gateMalformed(meta, operation, "gate candle row is incomplete")
		}
		fields := []struct {
			name  string
			value string
		}{
			{"open time", string(row[0])},
			{"volume", string(row[1])},
			{"close", string(row[2])},
			{"high", string(row[3])},
			{"low", string(row[4])},
			{"open", string(row[5])},
		}
		for _, field := range fields {
			if field.name == "open time" {
				if _, err := strconv.ParseInt(strings.TrimSpace(field.value), 10, 64); err != nil {
					return gateMalformed(meta, operation, "invalid gate candle open time")
				}
				continue
			}
			if err := gateValidateDecimal(meta, operation, "candle "+field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateSpotPublicTrades(meta clientMeta, operation string, rows []gate.Trade) error {
	for _, row := range rows {
		if err := gateValidateSide(meta, operation, row.Side); err != nil {
			return err
		}
		if err := gateValidateDecimal(meta, operation, "trade price", row.Price); err != nil {
			return err
		}
		if err := gateValidateDecimal(meta, operation, "trade amount", row.Amount); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateFuturesPublicTrades(meta clientMeta, operation string, rows []gate.FuturesTrade) error {
	for _, row := range rows {
		if err := gateValidateDecimal(meta, operation, "trade price", row.Price); err != nil {
			return err
		}
		if err := gateValidateDecimal(meta, operation, "trade size", string(row.Size)); err != nil {
			return err
		}
		if err := gateValidateDecimal(meta, operation, "trade create time", string(row.CreateTime)); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateSpotInstruments(meta clientMeta, operation string, rows []gate.CurrencyPair) error {
	for _, row := range rows {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"min base amount", row.MinBaseAmount},
			{"min quote amount", row.MinQuoteAmount},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateFuturesInstruments(meta clientMeta, operation string, rows []gate.Contract) error {
	for _, row := range rows {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"order price round", row.OrderPriceRound},
			{"quanto multiplier", row.QuantoMultiplier},
			{"funding rate", row.FundingRate},
			{"mark price", row.MarkPrice},
		} {
			if err := gateValidateDecimal(meta, operation, field.name, field.value); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateSpotOrderBook(meta clientMeta, operation string, book gate.OrderBook) error {
	for _, side := range [][][]gate.NumberString{book.Bids, book.Asks} {
		for _, row := range side {
			if len(row) < 2 {
				return gateMalformed(meta, operation, "gate order book level is incomplete")
			}
			if err := gateValidateDecimal(meta, operation, "book price", string(row[0])); err != nil {
				return err
			}
			if err := gateValidateDecimal(meta, operation, "book quantity", string(row[1])); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateValidateFuturesOrderBook(meta clientMeta, operation string, book gate.FuturesOrderBook) error {
	for _, side := range [][]gate.FuturesOrderBookItem{book.Bids, book.Asks} {
		for _, row := range side {
			if err := gateValidateDecimal(meta, operation, "book price", row.Price); err != nil {
				return err
			}
		}
	}
	return nil
}

func gateSplitSymbol(symbol string) (string, string) {
	parts := strings.Split(symbol, "_")
	if len(parts) != 2 {
		return symbol, ""
	}
	return parts[0], parts[1]
}

func gateTimeMS(value string) time.Time {
	text := strings.TrimSpace(value)
	if text == "" {
		return time.Time{}
	}
	if strings.Contains(text, ".") {
		seconds, _ := decimal.NewFromString(text)
		return time.UnixMilli(seconds.Mul(decimal.NewFromInt(1000)).IntPart()).UTC()
	}
	ms, _ := strconv.ParseInt(text, 10, 64)
	return time.UnixMilli(ms).UTC()
}

func gateUnixMilli(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func gateUnixMilliString(value string) time.Time {
	ms, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return gateUnixMilli(ms)
}

func gateUnixSecondString(value string) time.Time {
	seconds, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func gateUnixSecondDecimalString(value string) time.Time {
	seconds, err := decimal.NewFromString(strings.TrimSpace(value))
	if err != nil || !seconds.IsPositive() {
		return time.Time{}
	}
	whole := seconds.IntPart()
	nanos := seconds.Sub(decimal.NewFromInt(whole)).Mul(decimal.NewFromInt(time.Second.Nanoseconds())).IntPart()
	return time.Unix(whole, nanos).UTC()
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func gateOrderID(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func parseGateOrderID(meta clientMeta, operation, value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 || value != strconv.FormatInt(id, 10) {
		return 0, exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: operation, SafeMessage: "order id must be a canonical positive int64"})
	}
	return id, nil
}

func validateGateCancel(meta clientMeta, request exchange.CancelOrderRequest) error {
	if strings.TrimSpace(request.Instrument) == "" {
		return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: "CancelOrder", SafeMessage: "instrument is required"})
	}
	_, err := parseGateOrderID(meta, "CancelOrder", request.OrderID)
	return err
}

func gateAckValidate(ack exchange.OrderAcknowledgement) (exchange.OrderAcknowledgement, error) {
	if err := ack.Validate(); err != nil {
		return ack, err
	}
	return ack, nil
}

func gateBadResponse(meta clientMeta, operation string, err error) error {
	if err == nil {
		return nil
	}
	return gateMalformed(meta, operation, fmt.Sprintf("gate response conversion failed: %T", err))
}
