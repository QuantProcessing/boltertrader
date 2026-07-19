package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

type nadoBase struct {
	meta clientMeta
	sdk  *nadosdk.Client
}

type nadoSpotClient struct {
	spotClient
	*nadoBase
	wsMu   sync.Mutex
	ws     exchange.SpotWebSocket
	closed bool
}

type nadoPerpClient struct {
	perpClient
	*nadoBase
	wsMu   sync.Mutex
	ws     exchange.PerpWebSocket
	closed bool
}

type nadoPerpWebSocket struct {
	*perpWebSocket
	base *nadoBase
}

type nadoProduct struct {
	native     string
	canonical  string
	productID  int64
	marketType nadosdk.MarketType
	symbol     nadosdk.Symbol
	book       nadosdk.ProductBookInfo
	base       string
	quote      string
}

func NewNadoSpot(privateKey, subaccount string, settings Settings) exchange.SpotClient {
	base := newNadoBase(exchange.ProductSpot, privateKey, subaccount, settings)
	return &nadoSpotClient{spotClient: spotClient{meta: base.meta}, nadoBase: base}
}

func NewNadoUSDT0Perp(privateKey, subaccount string, settings Settings) exchange.PerpClient {
	base := newNadoBase(exchange.ProductPerp, privateKey, subaccount, settings)
	return &nadoPerpClient{perpClient: perpClient{meta: base.meta}, nadoBase: base}
}

func newNadoBase(product exchange.Product, privateKey, subaccount string, settings Settings) *nadoBase {
	env := nadosdk.EnvironmentMainnet
	if strings.EqualFold(settings.Environment, "testnet") || strings.EqualFold(settings.Environment, "demo") {
		env = nadosdk.EnvironmentTestnet
	}
	profile, err := nadosdk.NewProfile(env)
	if err != nil {
		return &nadoBase{meta: clientMeta{venue: exchange.VenueNado, product: product}}
	}
	overrides := map[nadosdk.EndpointKind]string{}
	if settings.Endpoint != "" {
		overrides[nadosdk.EndpointGatewayV1] = settings.Endpoint
		overrides[nadosdk.EndpointGatewayV2] = strings.TrimSuffix(settings.Endpoint, "/v1") + "/v2"
		overrides[nadosdk.EndpointArchiveV1] = settings.Endpoint
		overrides[nadosdk.EndpointArchiveV2] = strings.TrimSuffix(settings.Endpoint, "/v1") + "/v2"
		overrides[nadosdk.EndpointTrigger] = settings.Endpoint
	}
	if settings.WebSocketEndpoint != "" {
		overrides[nadosdk.EndpointGatewayWS] = settings.WebSocketEndpoint
		overrides[nadosdk.EndpointSubscriptionsWS] = settings.WebSocketEndpoint
	}
	if len(overrides) > 0 {
		profile, err = profile.WithEndpointOverrides(overrides)
		if err != nil {
			return &nadoBase{meta: clientMeta{venue: exchange.VenueNado, product: product}}
		}
	}
	client, err := nadosdk.NewClient(profile)
	if err == nil {
		client.WithHTTPClient(settings.HTTPClient)
		_, _ = client.WithCredentials(privateKey, subaccount)
	}
	return &nadoBase{
		meta: clientMeta{venue: exchange.VenueNado, product: product},
		sdk:  client,
	}
}

func (base *nadoBase) err(operation string, kind exchange.ErrorKind, msg string) error {
	return exchange.NewError(kind, exchange.ErrorDetails{Venue: exchange.VenueNado, Product: base.meta.product, Operation: operation, SafeMessage: msg})
}

func (base *nadoBase) normalize(operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return base.err(operation, exchange.KindCanceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return base.err(operation, exchange.KindDeadlineExceeded, "request deadline exceeded")
	}
	return base.err(operation, exchange.KindTransport, "Nado request failed")
}

func (base *nadoBase) mutationError(operation string, err error, ack *exchange.OrderAcknowledgement) error {
	if err == nil {
		return nil
	}
	var gatewayErr *nadosdk.GatewayApplicationError
	if errors.As(err, &gatewayErr) {
		if gatewayErr.Code != 0 {
			ack.VenueCode = strconv.Itoa(gatewayErr.Code)
		}
	}
	if gatewayErr != nil && errors.Is(err, nadosdk.ErrExecutionRejected) {
		ack.State = exchange.AckRejected
		ack.VenueMessage = "Nado rejected order command"
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueNado, Product: base.meta.product, Operation: operation, Code: ack.VenueCode, SafeMessage: "Nado rejected order command"})
	}
	ack.State = exchange.AckAmbiguous
	ack.VenueMessage = "order command outcome is unknown after possible send"
	return exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueNado, Product: base.meta.product, Operation: operation, Code: ack.VenueCode, SafeMessage: "order command outcome is unknown after possible send"})
}

func (base *nadoBase) ready(operation string) error {
	if base == nil || base.sdk == nil {
		return base.err(operation, exchange.KindInvalidConfig, "Nado SDK client is not configured")
	}
	return nil
}

func (base *nadoBase) discover(ctx context.Context, operation string) ([]nadoProduct, error) {
	if err := base.ready(operation); err != nil {
		return nil, err
	}
	products, err := base.sdk.GetAllProducts(ctx)
	if err != nil {
		return nil, base.normalize(operation, err)
	}
	symbols, err := base.sdk.QuerySymbols(ctx, nadosdk.SymbolsRequest{})
	if err != nil {
		return nil, base.normalize(operation, err)
	}
	if err := nadosdk.ValidateNadoProductDiscovery(*products, *symbols); err != nil {
		return nil, base.err(operation, exchange.KindMalformedResponse, "Nado product discovery was inconsistent")
	}
	pairs, err := base.sdk.GetPairs(ctx, nil)
	if err != nil {
		return nil, base.normalize(operation, err)
	}
	tickerByProductID := make(map[int64]string, len(pairs))
	for _, pair := range pairs {
		if pair.ProductId > 0 && strings.TrimSpace(pair.TickerId) != "" {
			tickerByProductID[pair.ProductId] = pair.TickerId
		}
	}
	out := make([]nadoProduct, 0, len(symbols.Symbols))
	for _, symbol := range symbols.Symbols {
		if symbol.ProductID == 0 || symbol.TradingStatus == nadosdk.TradingStatusNotTradable {
			continue
		}
		marketType := nadosdk.MarketType(symbol.Type)
		if base.meta.product == exchange.ProductSpot && marketType != nadosdk.MarketTypeSpot {
			continue
		}
		if base.meta.product == exchange.ProductPerp && marketType != nadosdk.MarketTypePerp {
			continue
		}
		ticker := tickerByProductID[int64(symbol.ProductID)]
		if ticker == "" {
			return nil, base.err(operation, exchange.KindMalformedResponse, "Nado product is missing its V2 ticker")
		}
		product := nadoProduct{
			native:     ticker,
			canonical:  nadoCanonical(ticker),
			productID:  int64(symbol.ProductID),
			marketType: marketType,
			symbol:     symbol,
			book:       nadoBookInfo(*products, int64(symbol.ProductID), marketType),
		}
		product.base, product.quote = nadoAssets(product.canonical, marketType)
		out = append(out, product)
	}
	return out, nil
}

func (base *nadoBase) product(ctx context.Context, instrument, operation string) (nadoProduct, error) {
	canonical := strings.TrimSpace(instrument)
	if canonical == "" || canonical != instrument {
		return nadoProduct{}, base.err(operation, exchange.KindInvalidRequest, "instrument is required and must not have surrounding whitespace")
	}
	products, err := base.discover(ctx, operation)
	if err != nil {
		return nadoProduct{}, err
	}
	for _, product := range products {
		if product.canonical == canonical || product.native == instrument {
			return product, nil
		}
	}
	return nadoProduct{}, base.err(operation, exchange.KindInvalidRequest, "unknown Nado instrument")
}

func nadoBookInfo(products nadosdk.AllProductsResponse, productID int64, marketType nadosdk.MarketType) nadosdk.ProductBookInfo {
	if marketType == nadosdk.MarketTypeSpot {
		for _, product := range products.SpotProducts {
			if product.ProductID == productID {
				return product.BookInfo
			}
		}
	}
	for _, product := range products.PerpProducts {
		if product.ProductID == productID {
			return product.BookInfo
		}
	}
	return nadosdk.ProductBookInfo{}
}

func nadoCanonical(native string) string {
	return strings.ReplaceAll(native, "_", "-")
}

func nadoAssets(canonical string, marketType nadosdk.MarketType) (string, string) {
	parts := strings.Split(canonical, "-")
	if marketType == nadosdk.MarketTypePerp && len(parts) >= 3 {
		return parts[0], parts[len(parts)-1]
	}
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	return canonical, ""
}

func (client *nadoSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	return client.nadoBase.instruments(ctx, "Instruments")
}

func (client *nadoPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	return client.nadoBase.instruments(ctx, "Instruments")
}

func (base *nadoBase) instruments(ctx context.Context, operation string) ([]exchange.Instrument, error) {
	products, err := base.discover(ctx, operation)
	if err != nil {
		return nil, err
	}
	out := make([]exchange.Instrument, 0, len(products))
	for _, product := range products {
		priceIncrement, err := nadoX18(product.symbol.PriceIncrementX18)
		if err != nil {
			return nil, nadoMalformed(base.meta.product, operation, "invalid price increment")
		}
		quantityIncrement, err := nadoX18(product.symbol.SizeIncrement)
		if err != nil {
			return nil, nadoMalformed(base.meta.product, operation, "invalid quantity increment")
		}
		minSize, err := nadoX18(product.symbol.MinSize)
		if err != nil {
			return nil, nadoMalformed(base.meta.product, operation, "invalid min size")
		}
		out = append(out, exchange.Instrument{
			Symbol:            product.canonical,
			BaseAsset:         product.base,
			QuoteAsset:        product.quote,
			SettleAsset:       nadoSettle(product),
			Product:           base.meta.product,
			PriceIncrement:    priceIncrement,
			QuantityIncrement: quantityIncrement,
			MinQuantity:       quantityIncrement,
			MinNotional: exchange.OptionalDecimal{
				Value: minSize,
				Valid: minSize.IsPositive(),
			},
		})
	}
	return out, nil
}

func nadoSettle(product nadoProduct) string {
	if product.marketType == nadosdk.MarketTypePerp {
		return product.quote
	}
	return ""
}

func (client *nadoSpotClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return client.nadoBase.orderBook(ctx, req)
}

func (client *nadoPerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return client.nadoBase.orderBook(ctx, req)
}

func (base *nadoBase) orderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	product, err := base.product(ctx, req.Instrument, "OrderBook")
	if err != nil {
		return exchange.OrderBook{}, err
	}
	resp, err := base.sdk.GetOrderBook(ctx, product.native, req.Limit)
	if err != nil {
		return exchange.OrderBook{}, base.normalize("OrderBook", err)
	}
	bids := nadoFloatLevels(resp.Bids)
	asks := nadoFloatLevels(resp.Asks)
	return exchange.OrderBook{Instrument: product.canonical, Bids: bids, Asks: asks, Time: time.UnixMilli(resp.Timestamp).UTC(), Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *nadoSpotClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	return client.nadoBase.candles(ctx, req)
}

func (client *nadoPerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	return client.nadoBase.candles(ctx, req)
}

func (base *nadoBase) candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	product, err := base.product(ctx, req.Instrument, "Candles")
	if err != nil {
		return exchange.CandlePage{}, err
	}
	granularity := nadoIntervalSeconds(req.Interval)
	resp, err := base.sdk.GetCandlesticks(ctx, nadosdk.CandlestickRequest{Candlesticks: nadosdk.Candlesticks{ProductID: product.productID, Granularity: granularity, MaxTime: nadoCandleMaxTime(req.End), Limit: req.Limit}})
	if err != nil {
		return exchange.CandlePage{}, base.normalize("Candles", err)
	}
	candles := make([]exchange.Candle, 0, len(resp))
	for _, row := range resp {
		open, err := nadoX18(row.OpenX18)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle open")
		}
		high, err := nadoX18(row.HighX18)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle high")
		}
		low, err := nadoX18(row.LowX18)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle low")
		}
		closeValue, err := nadoX18(row.CloseX18)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle close")
		}
		volume, err := nadoX18(row.Volume)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle volume")
		}
		ts, err := strconv.ParseInt(row.Timestamp, 10, 64)
		if err != nil {
			return exchange.CandlePage{}, nadoMalformed(base.meta.product, "Candles", "invalid candle timestamp")
		}
		candles = append(candles, exchange.Candle{OpenTime: time.Unix(ts, 0).UTC(), Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: true})
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func nadoCandleMaxTime(end time.Time) int64 {
	if end.IsZero() {
		return 0
	}
	return end.Unix()
}

func nadoIntervalSeconds(interval string) int64 {
	switch interval {
	case "1m":
		return 60
	case "5m":
		return 300
	case "1h":
		return 3600
	default:
		return 60
	}
}

func (client *nadoSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	return client.nadoBase.publicTrades(ctx, req)
}

func (client *nadoPerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	return client.nadoBase.publicTrades(ctx, req)
}

func (base *nadoBase) publicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	product, err := base.product(ctx, req.Instrument, "PublicTrades")
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	resp, err := base.sdk.GetTrades(ctx, product.native, ptrInt(req.Limit), nil)
	if err != nil {
		return exchange.PublicTradePage{}, base.normalize("PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(resp))
	for _, row := range resp {
		side := exchange.SideBuy
		if row.TradeType == "sell" {
			side = exchange.SideSell
		}
		trades = append(trades, exchange.PublicTrade{Instrument: product.canonical, TradeID: strconv.FormatInt(row.TradeID, 10), Side: side, Price: decimal.NewFromFloat(row.Price), Quantity: decimal.NewFromFloat(row.BaseFilled), Time: time.UnixMilli(row.Timestamp).UTC()})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *nadoSpotClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return client.nadoBase.placeOrder(ctx, req)
}

func (client *nadoPerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return client.nadoBase.placeOrder(ctx, req)
}

func (base *nadoBase) placeOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := req.Validate(base.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	product, err := base.product(ctx, req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	input, err := base.orderInput(ctx, product, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := base.sdk.PlaceOrder(ctx, input)
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueNado, Product: base.meta.product, Operation: exchange.OrderOperationPlace, State: exchange.AckAcceptedPending, Instrument: product.canonical, OrderType: req.Type, ClientOrderID: req.ClientOrderID}
	if resp != nil {
		if !validNadoOrderID(resp.Digest) {
			return exchange.OrderAcknowledgement{}, nadoMalformed(base.meta.product, "PlaceOrder", "invalid response order digest")
		}
		ack.OrderID = resp.Digest
		ack.TransactionHash = resp.Digest
	}
	if err != nil {
		return ack, base.mutationError("PlaceOrder", err, &ack)
	}
	return ack, ack.Validate()
}

func (base *nadoBase) orderInput(ctx context.Context, product nadoProduct, req exchange.PlaceOrderRequest) (nadosdk.ClientOrderInput, error) {
	orderType := nadosdk.OrderTypeLimit
	if req.Type == exchange.OrderTypeMarket {
		orderType = nadosdk.OrderTypeFOK
	} else if req.LimitPolicy == exchange.LimitPolicyIOC {
		orderType = nadosdk.OrderTypeIOC
	}
	effectivePrice := req.LimitPrice
	if req.Type == exchange.OrderTypeMarket {
		price, err := base.marketOrderLimitPrice(ctx, product, req.Side)
		if err != nil {
			return nadosdk.ClientOrderInput{}, err
		}
		effectivePrice = price
	}
	input := nadosdk.ClientOrderInput{ProductId: product.productID, Price: effectivePrice.String(), Amount: req.Quantity.String(), Side: nadoSide(req.Side), OrderType: orderType, ReduceOnly: req.ReduceOnly, PostOnly: req.LimitPolicy == exchange.LimitPolicyPostOnly}
	return input, nil
}

func (base *nadoBase) marketOrderLimitPrice(ctx context.Context, product nadoProduct, side exchange.Side) (decimal.Decimal, error) {
	book, err := base.sdk.GetOrderBook(ctx, product.native, 1)
	if err != nil {
		return decimal.Zero, base.normalize("PlaceOrder", err)
	}
	tick, err := nadoX18(product.symbol.PriceIncrementX18)
	if err != nil || !tick.IsPositive() {
		return decimal.Zero, base.err("PlaceOrder", exchange.KindMalformedResponse, "Nado product has an invalid price increment")
	}
	switch side {
	case exchange.SideBuy:
		if len(book.Asks) == 0 {
			return decimal.Zero, base.err("PlaceOrder", exchange.KindMalformedResponse, "Nado order book has no ask for market buy")
		}
		return nadoMarketOrderLimitPrice(decimal.NewFromFloat(book.Asks[0][0]), tick, side)
	case exchange.SideSell:
		if len(book.Bids) == 0 {
			return decimal.Zero, base.err("PlaceOrder", exchange.KindMalformedResponse, "Nado order book has no bid for market sell")
		}
		return nadoMarketOrderLimitPrice(decimal.NewFromFloat(book.Bids[0][0]), tick, side)
	default:
		return decimal.Zero, base.err("PlaceOrder", exchange.KindInvalidRequest, "side must be buy or sell")
	}
}

func nadoMarketOrderLimitPrice(best, tick decimal.Decimal, side exchange.Side) (decimal.Decimal, error) {
	if !best.IsPositive() || !tick.IsPositive() {
		return decimal.Zero, fmt.Errorf("best price and price increment must be positive")
	}
	switch side {
	case exchange.SideBuy:
		price := best.Mul(decimal.NewFromInt(105)).Div(decimal.NewFromInt(100))
		return price.Div(tick).Ceil().Mul(tick), nil
	case exchange.SideSell:
		price := best.Mul(decimal.NewFromInt(95)).Div(decimal.NewFromInt(100))
		return price.Div(tick).Floor().Mul(tick), nil
	default:
		return decimal.Zero, fmt.Errorf("side must be buy or sell")
	}
}

func nadoSide(side exchange.Side) nadosdk.OrderSide {
	if side == exchange.SideSell {
		return nadosdk.OrderSideSell
	}
	return nadosdk.OrderSideBuy
}

func (client *nadoSpotClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return client.nadoBase.cancelOrder(ctx, req)
}

func (client *nadoPerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return client.nadoBase.cancelOrder(ctx, req)
}

func (base *nadoBase) cancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if !validNadoOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, base.err("CancelOrder", exchange.KindInvalidRequest, "order id must be portable")
	}
	product, err := base.product(ctx, req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := base.sdk.CancelOrders(ctx, nadosdk.CancelOrdersInput{ProductIds: []int64{product.productID}, Digests: []string{req.OrderID}})
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueNado, Product: base.meta.product, Operation: exchange.OrderOperationCancel, State: exchange.AckCanceled, Instrument: product.canonical, OrderID: req.OrderID}
	if err != nil {
		return ack, base.mutationError("CancelOrder", err, &ack)
	}
	if len(resp.CancelledOrders) > 0 && resp.CancelledOrders[0].Digest != "" {
		if !validNadoOrderID(resp.CancelledOrders[0].Digest) {
			return exchange.OrderAcknowledgement{}, nadoMalformed(base.meta.product, "CancelOrder", "invalid response order digest")
		}
		ack.OrderID = resp.CancelledOrders[0].Digest
	}
	return ack, ack.Validate()
}

func (client *nadoSpotClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	return client.nadoBase.openOrders(ctx, req)
}

func (client *nadoPerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	return client.nadoBase.openOrders(ctx, req)
}

func (base *nadoBase) openOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	product, err := base.product(ctx, req.Instrument, "OpenOrders")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	sender, err := base.sdk.Sender()
	if err != nil {
		return exchange.OrderPage{}, base.normalize("OpenOrders", err)
	}
	resp, err := base.sdk.GetAccountMultiProductsOrders(ctx, []int64{product.productID}, sender)
	if err != nil {
		return exchange.OrderPage{}, base.normalize("OpenOrders", err)
	}
	var orders []exchange.Order
	for _, group := range resp.ProductOrders {
		if group.ProductID != product.productID {
			continue
		}
		for _, row := range group.Orders {
			if row.ProductID != product.productID {
				continue
			}
			order, err := nadoOrder(row, product)
			if err != nil {
				return exchange.OrderPage{}, err
			}
			orders = append(orders, order)
		}
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *nadoSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	return client.nadoBase.orderHistory(ctx, req)
}

func (client *nadoPerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	return client.nadoBase.orderHistory(ctx, req)
}

func (base *nadoBase) orderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	product, err := base.product(ctx, req.Instrument, "OrderHistory")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	sender, err := base.sdk.Sender()
	if err != nil {
		return exchange.OrderPage{}, base.normalize("OrderHistory", err)
	}
	maxTime := int64(0)
	if !req.End.IsZero() {
		maxTime = req.End.Unix()
	}
	archive, err := base.sdk.GetArchiveOrders(ctx, sender, []int64{product.productID}, maxTime, req.Cursor, req.Limit)
	if err != nil {
		return exchange.OrderPage{}, base.normalize("OrderHistory", err)
	}
	orders := make([]exchange.Order, 0, len(archive.Orders))
	for _, row := range archive.Orders {
		if row.ProductID != product.productID {
			continue
		}
		order, err := nadoArchiveOrder(row, product)
		if err != nil {
			return exchange.OrderPage{}, err
		}
		if withinUnixWindow(order.UpdatedAt, req.Start, req.End) {
			orders = append(orders, order)
		}
	}
	page := exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End, HasMoreKnown: req.Limit > 0}
	if req.Limit > 0 && len(archive.Orders) >= req.Limit {
		page.HasMore = true
		page.Cursor = archive.Orders[len(archive.Orders)-1].SubmissionIdx
	}
	return exchange.OrderPage{Orders: orders, Page: page}, nil
}

func (client *nadoSpotClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	return client.nadoBase.fills(ctx, req)
}

func (client *nadoPerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	return client.nadoBase.fills(ctx, req)
}

func (base *nadoBase) fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if req.Cursor != "" || req.OrderID != "" {
		return exchange.FillPage{}, base.err("Fills", exchange.KindInvalidRequest, "cursor and order id filters are not supported")
	}
	product, err := base.product(ctx, req.Instrument, "Fills")
	if err != nil {
		return exchange.FillPage{}, err
	}
	sender, err := base.sdk.Sender()
	if err != nil {
		return exchange.FillPage{}, base.normalize("Fills", err)
	}
	resp, err := base.sdk.GetMatches(ctx, sender, []int64{product.productID}, req.Limit)
	if err != nil {
		return exchange.FillPage{}, base.normalize("Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(resp.Matches))
	for _, row := range resp.Matches {
		if nadoMatchProductID(row) != product.productID {
			continue
		}
		fill, err := nadoMatchFill(row, product)
		if err != nil {
			return exchange.FillPage{}, err
		}
		if !withinUnixWindow(fill.Time, req.Start, req.End) {
			continue
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End, HasMoreKnown: false}}, nil
}

func (client *nadoSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.SpotAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *nadoSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	balances, err := client.balanceSnapshot(ctx, "SpotAccount")
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *nadoPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *nadoPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	resp, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return exchange.PerpAccount{}, client.normalize("PerpAccount", err)
	}
	balances, err := client.balanceSnapshot(ctx, "PerpAccount")
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	account := exchange.PerpAccount{Balances: balances}
	if len(resp.Healths) > 0 {
		assets, err := nadoX18(resp.Healths[0].Assets)
		if err != nil {
			return exchange.PerpAccount{}, nadoMalformed(exchange.ProductPerp, "PerpAccount", "invalid health assets")
		}
		liabilities, err := nadoX18(resp.Healths[0].Liabilities)
		if err != nil {
			return exchange.PerpAccount{}, nadoMalformed(exchange.ProductPerp, "PerpAccount", "invalid health liabilities")
		}
		equity := assets.Sub(liabilities)
		account.Equity = exchange.OptionalDecimal{Value: equity, Valid: true}
	}
	return account, nil
}

func (base *nadoBase) balanceSnapshot(ctx context.Context, operation string) ([]exchange.Balance, error) {
	resp, err := base.sdk.GetAccount(ctx)
	if err != nil {
		return nil, base.normalize(operation, err)
	}
	products, err := base.discover(ctx, operation)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]nadoProduct, len(products))
	for _, product := range products {
		byID[product.productID] = product
	}
	balances := make([]exchange.Balance, 0, len(resp.SpotBalances))
	for _, row := range resp.SpotBalances {
		amount, err := nadoX18(row.Balance.Amount)
		if err != nil {
			return nil, nadoMalformed(base.meta.product, operation, "invalid balance amount")
		}
		asset := strconv.FormatInt(row.ProductID, 10)
		if row.ProductID == 0 {
			asset = "USDT0"
		} else if product, ok := byID[row.ProductID]; ok {
			asset = product.base
		}
		balances = append(balances, exchange.Balance{Asset: asset, Available: amount, Total: amount})
	}
	return balances, nil
}

func (client *nadoPerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	account, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return nil, client.normalize("Positions", err)
	}
	products, err := client.discover(ctx, "Positions")
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]nadoProduct, len(products))
	for _, product := range products {
		byID[product.productID] = product
	}
	var positions []exchange.Position
	for _, row := range account.PerpBalances {
		product, ok := byID[row.ProductID]
		if !ok || (req.Instrument != "" && product.canonical != req.Instrument) {
			continue
		}
		qty, err := nadoX18(row.Balance.Amount)
		if err != nil {
			return nil, nadoMalformed(exchange.ProductPerp, "Positions", "invalid position quantity")
		}
		if qty.IsZero() {
			continue
		}
		vQuote := decimal.Zero
		if row.Balance.VQuoteBalance != nil {
			vQuote, err = nadoX18(*row.Balance.VQuoteBalance)
			if err != nil {
				return nil, nadoMalformed(exchange.ProductPerp, "Positions", "invalid v_quote balance")
			}
		}
		entry := decimal.Zero
		if !qty.IsZero() {
			entry = vQuote.Neg().Div(qty.Abs())
		}
		price, err := client.sdk.GetPerpPrice(ctx, product.productID)
		if err != nil {
			return nil, client.normalize("Positions", err)
		}
		mark, err := nadoX18(price.MarkPriceX18)
		if err != nil {
			return nil, nadoMalformed(exchange.ProductPerp, "Positions", "invalid mark price")
		}
		side := exchange.SideBuy
		if qty.IsNegative() {
			side = exchange.SideSell
		}
		positions = append(positions, exchange.Position{Instrument: product.canonical, Side: side, Quantity: qty.Abs(), EntryPrice: entry, MarkPrice: mark})
	}
	return positions, nil
}

func (client *nadoPerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	product, err := client.product(ctx, req.Instrument, "FundingRate")
	if err != nil {
		return exchange.FundingRate{}, err
	}
	resp, err := client.sdk.GetFundingRate(ctx, product.productID)
	if err != nil {
		return exchange.FundingRate{}, client.normalize("FundingRate", err)
	}
	return nadoFundingRate(*resp, product)
}

func (client *nadoPerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if req.Cursor != "" {
		return exchange.FundingRatePage{}, client.err("FundingRateHistory", exchange.KindInvalidRequest, "cursor is not supported")
	}
	if !req.Start.IsZero() && !req.End.IsZero() && !req.Start.Before(req.End) {
		return exchange.FundingRatePage{}, client.err("FundingRateHistory", exchange.KindInvalidRequest, "start must be before end")
	}
	rate, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: req.Instrument})
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	rates := []exchange.FundingRate{}
	if withinUnixWindow(rate.ObservedAt, req.Start, req.End) {
		rates = append(rates, rate)
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End, HasMoreKnown: false}}, nil
}

func (client *nadoPerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := client.ready("SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	product, err := client.product(ctx, req.Instrument, "SetLeverage")
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 {
		return exchange.Leverage{}, client.err("SetLeverage", exchange.KindInvalidRequest, "leverage must be positive")
	}
	return exchange.Leverage{Instrument: product.canonical, Effective: 0}, nil
}

func (client *nadoSpotClient) WebSocket() exchange.SpotWebSocket {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.ws != nil {
		return client.ws
	}
	backend := &nadoSpotWSBackend{base: client.nadoBase}
	client.ws = newSpotWebSocket(newPublicWebSocket(client.spotClient.meta, backend), backend)
	return client.ws
}

func (client *nadoPerpClient) WebSocket() exchange.PerpWebSocket {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.ws != nil {
		return client.ws
	}
	backend := &nadoPerpWSBackend{base: client.nadoBase}
	client.ws = &nadoPerpWebSocket{
		perpWebSocket: newPerpWebSocket(client.perpClient.meta, backend, backend),
		base:          client.nadoBase,
	}
	return client.ws
}

func (socket *nadoPerpWebSocket) WatchMarkPrice(
	ctx context.Context,
	request exchange.WatchRequest,
) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	if socket == nil || socket.perpWebSocket == nil {
		return nil, websocketError(clientMeta{venue: exchange.VenueNado, product: exchange.ProductPerp}, "WatchMarkPrice", exchange.KindInvalidConfig, "websocket backend is not configured")
	}
	return nil, socket.base.err(
		"WatchMarkPrice",
		exchange.KindUnsupported,
		"Nado does not provide a mark-price WebSocket stream",
	)
}

func (client *nadoSpotClient) Close() error {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.closed {
		return nil
	}
	client.closed = true
	if client.ws != nil {
		return client.ws.Close()
	}
	return nil
}
func (client *nadoPerpClient) Close() error {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.closed {
		return nil
	}
	client.closed = true
	if client.ws != nil {
		return client.ws.Close()
	}
	return nil
}

func nadoOrder(row nadosdk.Order, product nadoProduct) (exchange.Order, error) {
	qty, err := nadoX18(row.Amount)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OpenOrders", "invalid order quantity")
	}
	unfilled, err := nadoX18(row.UnfilledAmount)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OpenOrders", "invalid order unfilled quantity")
	}
	price, err := nadoX18(row.PriceX18)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OpenOrders", "invalid order price")
	}
	side, err := nadoSignedSide(qty)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OpenOrders", err.Error())
	}
	return exchange.Order{Instrument: product.canonical, OrderID: row.Digest, Type: exchange.OrderTypeLimit, Side: side, Quantity: qty.Abs(), LimitPrice: price, Filled: qty.Abs().Sub(unfilled.Abs()), Status: "open"}, nil
}

func nadoMatchFill(row nadosdk.Match, product nadoProduct) (exchange.Fill, error) {
	price, err := nadoX18(row.Order.PriceX18)
	if err != nil {
		return exchange.Fill{}, nadoMalformed(productType(product), "Fills", "invalid fill price")
	}
	qty, err := nadoX18(row.BaseFilled)
	if err != nil {
		return exchange.Fill{}, nadoMalformed(productType(product), "Fills", "invalid fill quantity")
	}
	fee, err := nadoX18(row.Fee)
	if err != nil {
		return exchange.Fill{}, nadoMalformed(productType(product), "Fills", "invalid fill fee")
	}
	fillTime := time.Time{}
	if strings.TrimSpace(row.Timestamp) != "" {
		ts, err := strconv.ParseInt(row.Timestamp, 10, 64)
		if err != nil {
			return exchange.Fill{}, nadoMalformed(productType(product), "Fills", "invalid fill timestamp")
		}
		fillTime = time.Unix(ts, 0).UTC()
	}
	orderQty, err := nadoX18(row.Order.Amount)
	if err != nil {
		return exchange.Fill{}, nadoMalformed(productType(product), "Fills", "invalid order quantity")
	}
	side, err := nadoSignedSide(orderQty)
	if err != nil {
		return exchange.Fill{}, nadoMalformed(productType(product), "Fills", err.Error())
	}
	return exchange.Fill{Instrument: product.canonical, OrderID: row.Digest, FillID: row.SubmissionIdx, Side: side, Price: price, Quantity: qty.Abs(), Fee: fee, FeeAsset: product.quote, Time: fillTime}, nil
}

func nadoMatchProductID(row nadosdk.Match) int64 {
	if row.PostBalance.Base.Spot != nil {
		return row.PostBalance.Base.Spot.ProductID
	}
	if row.PostBalance.Base.Perp != nil {
		return row.PostBalance.Base.Perp.ProductID
	}
	if row.PreBalance.Base.Spot != nil {
		return row.PreBalance.Base.Spot.ProductID
	}
	if row.PreBalance.Base.Perp != nil {
		return row.PreBalance.Base.Perp.ProductID
	}
	return 0
}

func nadoArchiveOrder(row nadosdk.ArchiveOrder, product nadoProduct) (exchange.Order, error) {
	qty, err := nadoX18(row.Amount)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OrderHistory", "invalid order quantity")
	}
	price, err := nadoX18(row.PriceX18)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OrderHistory", "invalid order price")
	}
	filled, err := nadoX18(row.BaseFilled)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OrderHistory", "invalid filled quantity")
	}
	ts, err := strconv.ParseInt(row.LastFillTimestamp, 10, 64)
	if err != nil || ts == 0 {
		ts, err = strconv.ParseInt(row.FirstFillTimestamp, 10, 64)
	}
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OrderHistory", "invalid order timestamp")
	}
	side, err := nadoSignedSide(qty)
	if err != nil {
		return exchange.Order{}, nadoMalformed(productType(product), "OrderHistory", err.Error())
	}
	status := "closed"
	if filled.Abs().Equal(qty.Abs()) {
		status = "filled"
	}
	return exchange.Order{Instrument: product.canonical, OrderID: row.Digest, Type: exchange.OrderTypeLimit, Side: side, Quantity: qty.Abs(), LimitPrice: price, Filled: filled.Abs(), Status: status, UpdatedAt: time.Unix(ts, 0).UTC()}, nil
}

func withinUnixWindow(at, start, end time.Time) bool {
	if !start.IsZero() && at.Before(start) {
		return false
	}
	if !end.IsZero() && !at.Before(end) {
		return false
	}
	return true
}

func nadoFundingRate(resp nadosdk.FundingRateResponse, product nadoProduct) (exchange.FundingRate, error) {
	rate, err := nadoX18(resp.FundingRateX18)
	if err != nil {
		return exchange.FundingRate{}, nadoMalformed(productType(product), "FundingRate", "invalid funding rate")
	}
	at, err := strconv.ParseInt(resp.UpdateTime, 10, 64)
	if err != nil {
		return exchange.FundingRate{}, nadoMalformed(productType(product), "FundingRate", "invalid funding timestamp")
	}
	return exchange.FundingRate{Instrument: product.canonical, Rate: rate, ObservedAt: time.Unix(at, 0).UTC(), FundingTime: time.Unix(at, 0).UTC()}, nil
}

func productType(product nadoProduct) exchange.Product {
	if product.marketType == nadosdk.MarketTypePerp {
		return exchange.ProductPerp
	}
	return exchange.ProductSpot
}

func nadoSignedSide(qty decimal.Decimal) (exchange.Side, error) {
	if qty.IsPositive() {
		return exchange.SideBuy, nil
	}
	if qty.IsNegative() {
		return exchange.SideSell, nil
	}
	return "", fmt.Errorf("zero quantity has no side")
}

func nadoFloatLevels(rows [][2]float64) []exchange.BookLevel {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		out = append(out, exchange.BookLevel{Price: decimal.NewFromFloat(row[0]), Quantity: decimal.NewFromFloat(row[1])})
	}
	return out
}

func nadoX18(value string) (decimal.Decimal, error) {
	intValue, ok := new(big.Int).SetString(strings.TrimSpace(value), 10)
	if !ok {
		return decimal.Zero, fmt.Errorf("invalid x18 decimal")
	}
	return decimal.NewFromBigInt(intValue, -18), nil
}

func nadoDecimalScale(value decimal.Decimal, places int32) *big.Int {
	scaled := value.Shift(places)
	result := new(big.Int)
	result.SetString(scaled.Truncate(0).String(), 10)
	return result
}

func validNadoOrderID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	for _, ch := range value[2:] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func nadoMalformed(product exchange.Product, operation, message string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{
		Venue:       exchange.VenueNado,
		Product:     product,
		Operation:   operation,
		SafeMessage: message,
	})
}

func emitNadoWSError[T any](callbacks streamCallbacks[T], err error) {
	if callbacks.Error != nil {
		callbacks.Error(err)
	}
}

func nadoStringLevels(rows [][2]string, product exchange.Product, operation string) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		price, err := nadoX18(row[0])
		if err != nil {
			return nil, nadoMalformed(product, operation, "invalid book price")
		}
		quantity, err := nadoX18(row[1])
		if err != nil {
			return nil, nadoMalformed(product, operation, "invalid book quantity")
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: quantity})
	}
	return out, nil
}

func nadoUnixTime(value string, product exchange.Product, operation, field string) (time.Time, error) {
	timestamp, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return time.Time{}, nadoMalformed(product, operation, "invalid "+field)
	}
	switch {
	case timestamp < 100_000_000_000:
		return time.Unix(timestamp, 0).UTC(), nil
	case timestamp < 100_000_000_000_000:
		return time.UnixMilli(timestamp).UTC(), nil
	case timestamp < 100_000_000_000_000_000:
		return time.UnixMicro(timestamp).UTC(), nil
	default:
		return time.Unix(0, timestamp).UTC(), nil
	}
}

func nadoBookEvent(book *nadosdk.OrderBook, product nadoProduct, exchangeProduct exchange.Product, operation string) (exchange.BookEvent, error) {
	if book == nil || book.ProductId != product.productID {
		return exchange.BookEvent{}, nadoMalformed(exchangeProduct, operation, "order book product mismatch")
	}
	bids, err := nadoStringLevels(book.Bids, exchangeProduct, operation)
	if err != nil {
		return exchange.BookEvent{}, err
	}
	asks, err := nadoStringLevels(book.Asks, exchangeProduct, operation)
	if err != nil {
		return exchange.BookEvent{}, err
	}
	at, err := nadoUnixTime(book.MaxTimestamp, exchangeProduct, operation, "book timestamp")
	if err != nil {
		return exchange.BookEvent{}, err
	}
	return exchange.BookEvent{Kind: exchange.EventSnapshot, Instrument: product.canonical, Sequence: book.MaxTimestamp, Previous: book.LastMaxTimestamp, Bids: bids, Asks: asks, Time: at}, nil
}

func nadoBBOEvent(ticker *nadosdk.Ticker, product nadoProduct, exchangeProduct exchange.Product, operation string) (exchange.BBOEvent, error) {
	if ticker == nil || ticker.ProductId != product.productID {
		return exchange.BBOEvent{}, nadoMalformed(exchangeProduct, operation, "ticker product mismatch")
	}
	bidPrice, err := nadoX18(ticker.BidPrice)
	if err != nil {
		return exchange.BBOEvent{}, nadoMalformed(exchangeProduct, operation, "invalid bid price")
	}
	bidQty, err := nadoX18(ticker.BidQty)
	if err != nil {
		return exchange.BBOEvent{}, nadoMalformed(exchangeProduct, operation, "invalid bid quantity")
	}
	askPrice, err := nadoX18(ticker.AskPrice)
	if err != nil {
		return exchange.BBOEvent{}, nadoMalformed(exchangeProduct, operation, "invalid ask price")
	}
	askQty, err := nadoX18(ticker.AskQty)
	if err != nil {
		return exchange.BBOEvent{}, nadoMalformed(exchangeProduct, operation, "invalid ask quantity")
	}
	at, err := nadoUnixTime(ticker.Timestamp, exchangeProduct, operation, "ticker timestamp")
	if err != nil {
		return exchange.BBOEvent{}, err
	}
	return exchange.BBOEvent{Instrument: product.canonical, Bid: exchange.BookLevel{Price: bidPrice, Quantity: bidQty}, Ask: exchange.BookLevel{Price: askPrice, Quantity: askQty}, Time: at}, nil
}

func nadoPublicTradeEvent(trade *nadosdk.Trade, product nadoProduct, exchangeProduct exchange.Product, operation string) (exchange.PublicTradeEvent, error) {
	if trade == nil || trade.ProductId != product.productID {
		return exchange.PublicTradeEvent{}, nadoMalformed(exchangeProduct, operation, "trade product mismatch")
	}
	price, err := nadoX18(trade.Price)
	if err != nil {
		return exchange.PublicTradeEvent{}, nadoMalformed(exchangeProduct, operation, "invalid trade price")
	}
	qty, err := nadoX18(trade.TakerQty)
	if err != nil {
		return exchange.PublicTradeEvent{}, nadoMalformed(exchangeProduct, operation, "invalid trade quantity")
	}
	side := exchange.SideSell
	if trade.IsTakerBuyer {
		side = exchange.SideBuy
	}
	at, err := nadoUnixTime(trade.Timestamp, exchangeProduct, operation, "trade timestamp")
	if err != nil {
		return exchange.PublicTradeEvent{}, err
	}
	return exchange.PublicTradeEvent{Instrument: product.canonical, Side: side, Price: price, Quantity: qty.Abs(), Time: at}, nil
}

func nadoCandleEvent(candle *nadosdk.Candlestick, product nadoProduct, interval string, exchangeProduct exchange.Product, operation string) (exchange.CandleEvent, error) {
	if candle == nil || candle.ProductId != product.productID {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "candle product mismatch")
	}
	open, err := nadoX18(candle.OpenX18)
	if err != nil {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "invalid candle open")
	}
	high, err := nadoX18(candle.HighX18)
	if err != nil {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "invalid candle high")
	}
	low, err := nadoX18(candle.LowX18)
	if err != nil {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "invalid candle low")
	}
	closeValue, err := nadoX18(candle.CloseX18)
	if err != nil {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "invalid candle close")
	}
	volume, err := nadoX18(candle.Volume)
	if err != nil {
		return exchange.CandleEvent{}, nadoMalformed(exchangeProduct, operation, "invalid candle volume")
	}
	at, err := nadoUnixTime(candle.Timestamp, exchangeProduct, operation, "candle timestamp")
	if err != nil {
		return exchange.CandleEvent{}, err
	}
	return exchange.CandleEvent{Instrument: product.canonical, Interval: interval, Candle: exchange.Candle{OpenTime: at, Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: true}}, nil
}

func nadoOrderUpdateEvent(update *nadosdk.OrderUpdate, product nadoProduct, exchangeProduct exchange.Product, operation string) (exchange.OrderEvent, error) {
	if update == nil || update.ProductId != product.productID {
		return exchange.OrderEvent{}, nadoMalformed(exchangeProduct, operation, "order update product mismatch")
	}
	qty, err := nadoX18(update.Amount)
	if err != nil {
		return exchange.OrderEvent{}, nadoMalformed(exchangeProduct, operation, "invalid order quantity")
	}
	var side exchange.Side
	if !qty.IsZero() {
		side, err = nadoSignedSide(qty)
		if err != nil {
			return exchange.OrderEvent{}, nadoMalformed(exchangeProduct, operation, err.Error())
		}
	}
	at, err := nadoUnixTime(update.Timestamp, exchangeProduct, operation, "order timestamp")
	if err != nil {
		return exchange.OrderEvent{}, err
	}
	status, err := nadoOrderReasonStatus(update.Reason)
	if err != nil {
		return exchange.OrderEvent{}, nadoMalformed(exchangeProduct, operation, err.Error())
	}
	return exchange.OrderEvent{Kind: exchange.EventDelta, Order: exchange.Order{Instrument: product.canonical, OrderID: update.Digest, Side: side, Quantity: qty.Abs(), Type: exchange.OrderTypeLimit, Status: status, UpdatedAt: at}}, nil
}

func nadoOrderReasonStatus(reason nadosdk.OrderUpdateReason) (string, error) {
	switch reason {
	case nadosdk.OrderReasonPlaced:
		return "open", nil
	case nadosdk.OrderReasonFilled:
		return "filled", nil
	case nadosdk.OrderReasonCancelled:
		return "canceled", nil
	default:
		return "", fmt.Errorf("unknown order update reason")
	}
}

func nadoFillEvent(fill *nadosdk.Fill, product nadoProduct, exchangeProduct exchange.Product, operation string) (exchange.FillEvent, error) {
	if fill == nil || fill.ProductId != product.productID {
		return exchange.FillEvent{}, nadoMalformed(exchangeProduct, operation, "fill product mismatch")
	}
	price, err := nadoX18(fill.Price)
	if err != nil {
		return exchange.FillEvent{}, nadoMalformed(exchangeProduct, operation, "invalid fill price")
	}
	qty, err := nadoX18(fill.FilledQty)
	if err != nil {
		return exchange.FillEvent{}, nadoMalformed(exchangeProduct, operation, "invalid fill quantity")
	}
	fee, err := nadoX18(fill.Fee)
	if err != nil {
		return exchange.FillEvent{}, nadoMalformed(exchangeProduct, operation, "invalid fill fee")
	}
	side := exchange.SideSell
	if fill.IsBid {
		side = exchange.SideBuy
	}
	at, err := nadoUnixTime(fill.Timestamp, exchangeProduct, operation, "fill timestamp")
	if err != nil {
		return exchange.FillEvent{}, err
	}
	return exchange.FillEvent{Kind: exchange.EventDelta, Fill: exchange.Fill{Instrument: product.canonical, OrderID: fill.OrderDigest, FillID: fill.SubmissionIdx, Side: side, Price: price, Quantity: qty.Abs(), Fee: fee, FeeAsset: product.quote, Time: at}}, nil
}

func nadoBalanceEvent(change *nadosdk.PositionChange, exchangeProduct exchange.Product, operation string) (exchange.BalanceEvent, error) {
	if change == nil {
		return exchange.BalanceEvent{}, nadoMalformed(exchangeProduct, operation, "nil balance update")
	}
	amount, err := nadoX18(change.Amount)
	if err != nil {
		return exchange.BalanceEvent{}, nadoMalformed(exchangeProduct, operation, "invalid balance amount")
	}
	at, err := nadoUnixTime(change.Timestamp, exchangeProduct, operation, "balance timestamp")
	if err != nil {
		return exchange.BalanceEvent{}, err
	}
	return exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: []exchange.Balance{{Asset: "USDT0", Available: amount, Total: amount}}, Time: at}, nil
}

func nadoPositionEvent(change *nadosdk.PositionChange, product nadoProduct, operation string) (exchange.PositionEvent, error) {
	if change == nil || change.ProductId != product.productID {
		return exchange.PositionEvent{}, nadoMalformed(exchange.ProductPerp, operation, "position product mismatch")
	}
	qty, err := nadoX18(change.Amount)
	if err != nil {
		return exchange.PositionEvent{}, nadoMalformed(exchange.ProductPerp, operation, "invalid position quantity")
	}
	side := exchange.Side("")
	if !qty.IsZero() {
		side, err = nadoSignedSide(qty)
		if err != nil {
			return exchange.PositionEvent{}, nadoMalformed(exchange.ProductPerp, operation, err.Error())
		}
	}
	vQuote, err := nadoX18(change.VQuoteAmount)
	if err != nil {
		return exchange.PositionEvent{}, nadoMalformed(exchange.ProductPerp, operation, "invalid v_quote amount")
	}
	entry := decimal.Zero
	if !qty.IsZero() {
		entry = vQuote.Neg().Div(qty.Abs())
	}
	at, err := nadoUnixTime(change.Timestamp, exchange.ProductPerp, operation, "position timestamp")
	if err != nil {
		return exchange.PositionEvent{}, err
	}
	return exchange.PositionEvent{Kind: exchange.EventDelta, Positions: []exchange.Position{{Instrument: product.canonical, Side: side, Quantity: qty.Abs(), EntryPrice: entry}}, Time: at}, nil
}

func nadoFundingRateEvent(rate *nadosdk.FundingRate, product nadoProduct, operation string) (exchange.FundingRateEvent, error) {
	if rate == nil || rate.ProductId != product.productID {
		return exchange.FundingRateEvent{}, nadoMalformed(exchange.ProductPerp, operation, "funding product mismatch")
	}
	value, err := nadoX18(rate.FundingRateX18)
	if err != nil {
		return exchange.FundingRateEvent{}, nadoMalformed(exchange.ProductPerp, operation, "invalid funding rate")
	}
	at, err := nadoUnixTime(rate.UpdateTime, exchange.ProductPerp, operation, "funding timestamp")
	if err != nil {
		return exchange.FundingRateEvent{}, err
	}
	return exchange.FundingRateEvent{Instrument: product.canonical, Rate: value, EffectiveAt: at}, nil
}

type nadoSpotWSBackend struct {
	base    *nadoBase
	mu      sync.Mutex
	market  *nadosdk.WsMarketClient
	account *nadosdk.WsAccountClient
	api     *nadosdk.WsApiClient
}

type nadoPerpWSBackend struct {
	base    *nadoBase
	mu      sync.Mutex
	market  *nadosdk.WsMarketClient
	account *nadosdk.WsAccountClient
	api     *nadosdk.WsApiClient
}

func (backend *nadoSpotWSBackend) ensureMarket(ctx context.Context) (*nadosdk.WsMarketClient, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.market != nil {
		return backend.market, nil
	}
	if err := backend.base.ready("WebSocket"); err != nil {
		return nil, err
	}
	client, err := nadosdk.NewWsMarketClient(context.Background(), backend.base.sdk.Profile())
	if err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	if err := client.Connect(); err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	backend.market = client
	return client, nil
}

func (backend *nadoSpotWSBackend) ensureAccount(ctx context.Context) (*nadosdk.WsAccountClient, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.account != nil {
		return backend.account, nil
	}
	if err := backend.base.ready("WebSocket"); err != nil {
		return nil, err
	}
	client, err := nadosdk.NewWsAccountClient(context.Background(), backend.base.sdk)
	if err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	if err := client.Connect(); err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	backend.account = client
	return client, nil
}

func (backend *nadoSpotWSBackend) ensureAPI(ctx context.Context) (*nadosdk.WsApiClient, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.api != nil {
		return backend.api, nil
	}
	if err := backend.base.ready("WebSocket"); err != nil {
		return nil, err
	}
	client, err := nadosdk.NewWsApiClient(context.Background(), backend.base.sdk)
	if err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	if err := client.Connect(); err != nil {
		return nil, backend.base.normalize("WebSocket", err)
	}
	backend.api = client
	return client, nil
}

func (backend *nadoSpotWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchOrderBook")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeOrderBook(product.productID, func(book *nadosdk.OrderBook) {
		event, err := nadoBookEvent(book, product, backend.base.meta.product, "WatchOrderBook")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchOrderBook", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeOrderBook(product.productID) }), nil
}
func (backend *nadoSpotWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchBBO")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeTicker(product.productID, func(ticker *nadosdk.Ticker) {
		event, err := nadoBBOEvent(ticker, product, backend.base.meta.product, "WatchBBO")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchBBO", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeTicker(product.productID) }), nil
}
func (backend *nadoSpotWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchPublicTrades")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeTrades(product.productID, func(trade *nadosdk.Trade) {
		event, err := nadoPublicTradeEvent(trade, product, backend.base.meta.product, "WatchPublicTrades")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchPublicTrades", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeTrades(product.productID) }), nil
}
func (backend *nadoSpotWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchCandles")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	granularity := int32(nadoIntervalSeconds(interval))
	err = ws.SubscribeLatestCandlestick(product.productID, granularity, func(candle *nadosdk.Candlestick) {
		event, err := nadoCandleEvent(candle, product, interval, backend.base.meta.product, "WatchCandles")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchCandles", err)
	}
	return nadoIdempotentUnsubscribe(func() error {
		return ws.UnsubscribeLatestCandlestick(product.productID, granularity)
	}), nil
}
func (backend *nadoSpotWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchOrders")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeOrders(&product.productID, func(update *nadosdk.OrderUpdate) {
		event, err := nadoOrderUpdateEvent(update, product, backend.base.meta.product, "WatchOrders")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchOrders", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeOrders(&product.productID) }), nil
}
func (backend *nadoSpotWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchFills")
	if err != nil {
		return nil, err
	}
	ws, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeFills(&product.productID, func(fill *nadosdk.Fill) {
		event, err := nadoFillEvent(fill, product, backend.base.meta.product, "WatchFills")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchFills", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeFills(&product.productID) }), nil
}
func (backend *nadoSpotWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	ws, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribePositions(nil, func(change *nadosdk.PositionChange) {
		if change == nil {
			return
		}
		balances, err := backend.base.balanceSnapshot(context.Background(), "WatchBalances")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		at, err := nadoUnixTime(change.Timestamp, backend.base.meta.product, "WatchBalances", "balance timestamp")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		event := exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: at}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchBalances", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribePositions(nil) }), nil
}
func (backend *nadoSpotWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := req.Validate(backend.base.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	product, err := backend.base.product(ctx, req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ws, err := backend.ensureAPI(ctx)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	input, err := backend.base.orderInput(ctx, product, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := ws.PlaceOrder(ctx, input)
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueNado, Product: backend.base.meta.product, Operation: exchange.OrderOperationPlace, State: exchange.AckAcceptedPending, Instrument: product.canonical, OrderType: req.Type, ClientOrderID: req.ClientOrderID}
	if resp != nil {
		if !validNadoOrderID(resp.Digest) {
			return exchange.OrderAcknowledgement{}, nadoMalformed(backend.base.meta.product, "PlaceOrder", "invalid response order digest")
		}
		ack.OrderID = resp.Digest
		ack.TransactionHash = resp.Digest
	}
	if err != nil {
		return ack, backend.base.mutationError("PlaceOrder", err, &ack)
	}
	return ack, ack.Validate()
}
func (backend *nadoSpotWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if !validNadoOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, backend.base.err("CancelOrder", exchange.KindInvalidRequest, "order id must be portable")
	}
	product, err := backend.base.product(ctx, req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ws, err := backend.ensureAPI(ctx)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := ws.CancelOrders(ctx, nadosdk.CancelOrdersInput{ProductIds: []int64{product.productID}, Digests: []string{req.OrderID}})
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueNado, Product: backend.base.meta.product, Operation: exchange.OrderOperationCancel, State: exchange.AckCanceled, Instrument: product.canonical, OrderID: req.OrderID}
	if err != nil {
		return ack, backend.base.mutationError("CancelOrder", err, &ack)
	}
	if resp != nil && len(resp.CancelledOrders) > 0 && resp.CancelledOrders[0].Digest != "" {
		if !validNadoOrderID(resp.CancelledOrders[0].Digest) {
			return exchange.OrderAcknowledgement{}, nadoMalformed(backend.base.meta.product, "CancelOrder", "invalid response order digest")
		}
		ack.OrderID = resp.CancelledOrders[0].Digest
	}
	return ack, ack.Validate()
}
func (backend *nadoSpotWSBackend) Close() error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.market != nil {
		backend.market.Close()
		backend.market = nil
	}
	if backend.account != nil {
		backend.account.Close()
		backend.account = nil
	}
	if backend.api != nil {
		backend.api.Close()
		backend.api = nil
	}
	return nil
}

func nadoIdempotentUnsubscribe(unsubscribe func() error) func() error {
	return func() error {
		if unsubscribe == nil {
			return nil
		}
		err := unsubscribe()
		if err != nil && strings.EqualFold(strings.TrimSpace(err.Error()), "not connected") {
			return nil
		}
		return err
	}
}

func (backend *nadoPerpWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartOrderBook(ctx, instrument, callbacks)
}
func (backend *nadoPerpWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartBBO(ctx, instrument, callbacks)
}
func (backend *nadoPerpWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartPublicTrades(ctx, instrument, callbacks)
}
func (backend *nadoPerpWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartCandles(ctx, instrument, interval, callbacks)
}
func (backend *nadoPerpWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartOrders(ctx, instrument, callbacks)
}
func (backend *nadoPerpWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartFills(ctx, instrument, callbacks)
}
func (backend *nadoPerpWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	return (*nadoSpotWSBackend)(backend).StartBalances(ctx, callbacks)
}
func (backend *nadoPerpWSBackend) StartPositions(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchPositions")
	if err != nil {
		return nil, err
	}
	ws, err := (*nadoSpotWSBackend)(backend).ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribePositions(&product.productID, func(change *nadosdk.PositionChange) {
		event, err := nadoPositionEvent(change, product, "WatchPositions")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(event)
	})
	if err != nil {
		return nil, backend.base.normalize("WatchPositions", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribePositions(&product.productID) }), nil
}
func (backend *nadoPerpWSBackend) StartReference(ctx context.Context, instrument string, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	product, err := backend.base.product(ctx, instrument, "WatchReference")
	if err != nil {
		return nil, err
	}
	ws, err := (*nadoSpotWSBackend)(backend).ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeFundingRate(&product.productID, func(rate *nadosdk.FundingRate) {
		event, err := nadoFundingRateEvent(rate, product, "WatchFundingRate")
		if err != nil {
			emitNadoWSError(callbacks, err)
			return
		}
		callbacks.Event(perpReferenceEvent{FundingValid: true, FundingRate: event})
	})
	if err != nil {
		return nil, backend.base.normalize("WatchReference", err)
	}
	return nadoIdempotentUnsubscribe(func() error { return ws.UnsubscribeFundingRate(&product.productID) }), nil
}
func (backend *nadoPerpWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return (*nadoSpotWSBackend)(backend).PlaceOrder(ctx, req)
}
func (backend *nadoPerpWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return (*nadoSpotWSBackend)(backend).CancelOrder(ctx, req)
}
func (backend *nadoPerpWSBackend) Close() error { return (*nadoSpotWSBackend)(backend).Close() }
