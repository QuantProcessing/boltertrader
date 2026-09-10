package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

type bybitBase struct {
	meta       clientMeta
	sdk        *bybit.Client
	profile    bybit.EnvironmentProfile
	apiKey     string
	secretKey  string
	category   string
	settleCoin string
}

type bybitSpotClient struct {
	*bybitBase
	ws exchange.SpotWebSocket
}

type bybitPerpClient struct {
	*bybitBase
	ws exchange.PerpWebSocket
}

type bitgetBase struct {
	meta        clientMeta
	sdk         *bitget.Client
	profile     bitget.EnvironmentProfile
	apiKey      string
	secretKey   string
	passphrase  string
	category    string
	productType string
	marginCoin  string
	holdModeMu  sync.Mutex
	holdMode    string
}

type bitgetSpotClient struct {
	*bitgetBase
	ws exchange.SpotWebSocket
}

type bitgetPerpClient struct {
	*bitgetBase
	ws exchange.PerpWebSocket
}

func NewBybitSpot(apiKey, secretKey string, settings Settings) exchange.SpotClient {
	base := newBybitBase(apiKey, secretKey, settings, exchange.ProductSpot, "spot", "")
	client := &bybitSpotClient{bybitBase: base}
	client.ws = newSpotWebSocket(newPublicWebSocket(base.meta, &bybitWSBackend{client: base}), newBybitPrivateWSBackend(base))
	return client
}

func NewBybitLinearPerp(apiKey, secretKey, settleCoin string, settings Settings) exchange.PerpClient {
	base := newBybitBase(apiKey, secretKey, settings, exchange.ProductPerp, "linear", strings.ToUpper(strings.TrimSpace(settleCoin)))
	client := &bybitPerpClient{bybitBase: base}
	client.ws = newPerpWebSocket(base.meta, &bybitWSBackend{client: base}, newBybitPrivateWSBackend(base))
	return client
}

func newBybitBase(apiKey, secretKey string, settings Settings, product exchange.Product, category, settleCoin string) *bybitBase {
	profile := bybit.MainnetEnvironmentProfile()
	if settings.Endpoint != "" {
		profile.RESTBaseURL = settings.Endpoint
	}
	if settings.WebSocketEndpoint != "" {
		if category == "spot" {
			profile.PublicSpotWSURL = settings.WebSocketEndpoint
		} else {
			profile.PublicLinearWSURL = settings.WebSocketEndpoint
		}
		profile.PrivateWSURL = settings.WebSocketEndpoint
		profile.TradeWSURL = settings.WebSocketEndpoint
	}
	sdk := bybit.NewClient().WithCredentials(apiKey, secretKey).WithEnvironmentProfile(profile)
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return &bybitBase{meta: clientMetaFromSettings(exchange.VenueBybit, product, settings), sdk: sdk, profile: profile, apiKey: apiKey, secretKey: secretKey, category: category, settleCoin: settleCoin}
}

func NewBitgetSpot(apiKey, secretKey, passphrase string, settings Settings) exchange.SpotClient {
	base := newBitgetBase(apiKey, secretKey, passphrase, settings, exchange.ProductSpot, "spot", "", "")
	client := &bitgetSpotClient{bitgetBase: base}
	client.ws = newSpotWebSocket(newPublicWebSocket(base.meta, &bitgetWSBackend{client: base}), newBitgetPrivateWSBackend(base))
	return client
}

func NewBitgetPerp(apiKey, secretKey, passphrase, productType string, settings Settings) exchange.PerpClient {
	productType = strings.ToUpper(strings.TrimSpace(productType))
	marginCoin := "USDT"
	if strings.Contains(productType, "USDC") {
		marginCoin = "USDC"
	}
	base := newBitgetBase(apiKey, secretKey, passphrase, settings, exchange.ProductPerp, productType, productType, marginCoin)
	client := &bitgetPerpClient{bitgetBase: base}
	client.ws = newPerpWebSocket(base.meta, &bitgetWSBackend{client: base}, newBitgetPrivateWSBackend(base))
	return client
}

func newBitgetBase(apiKey, secretKey, passphrase string, settings Settings, product exchange.Product, category, productType, marginCoin string) *bitgetBase {
	profile := bitget.MainnetEnvironmentProfile()
	if settings.Endpoint != "" {
		profile.RESTBaseURL = settings.Endpoint
	}
	if settings.WebSocketEndpoint != "" {
		profile.PublicWSURL = settings.WebSocketEndpoint
		profile.PrivateWSURL = settings.WebSocketEndpoint
	}
	sdk := bitget.NewClient().WithCredentials(apiKey, secretKey, passphrase).WithEnvironmentProfile(profile)
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return &bitgetBase{meta: clientMetaFromSettings(exchange.VenueBitget, product, settings), sdk: sdk, profile: profile, apiKey: apiKey, secretKey: secretKey, passphrase: passphrase, category: category, productType: productType, marginCoin: marginCoin}
}

func (c *bybitSpotClient) WebSocket() exchange.SpotWebSocket {
	return c.ws
}

func (c *bybitSpotClient) Close() error {
	if c == nil || c.ws == nil {
		return nil
	}
	return c.ws.Close()
}

func (c *bybitSpotClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return c.bybitBase.meta.ResolveInstrument(ctx, symbol)
}

func (c *bybitSpotClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return c.bybitBase.meta.RefreshInstruments(ctx)
}

func (c *bybitPerpClient) WebSocket() exchange.PerpWebSocket {
	return c.ws
}

func (c *bybitPerpClient) Close() error {
	if c == nil || c.ws == nil {
		return nil
	}
	return c.ws.Close()
}

func (c *bybitPerpClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return c.bybitBase.meta.ResolveInstrument(ctx, symbol)
}

func (c *bybitPerpClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return c.bybitBase.meta.RefreshInstruments(ctx)
}

func (c *bitgetSpotClient) WebSocket() exchange.SpotWebSocket {
	return c.ws
}

func (c *bitgetSpotClient) Close() error {
	if c == nil || c.ws == nil {
		return nil
	}
	return c.ws.Close()
}

func (c *bitgetSpotClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return c.bitgetBase.meta.ResolveInstrument(ctx, symbol)
}

func (c *bitgetSpotClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return c.bitgetBase.meta.RefreshInstruments(ctx)
}

func (c *bitgetPerpClient) WebSocket() exchange.PerpWebSocket {
	return c.ws
}

func (c *bitgetPerpClient) Close() error {
	if c == nil || c.ws == nil {
		return nil
	}
	return c.ws.Close()
}

func (c *bitgetPerpClient) ResolveInstrument(ctx context.Context, symbol string) (exchange.Instrument, error) {
	return c.bitgetBase.meta.ResolveInstrument(ctx, symbol)
}

func (c *bitgetPerpClient) RefreshInstruments(ctx context.Context) ([]exchange.Instrument, error) {
	return c.bitgetBase.meta.RefreshInstruments(ctx)
}

func (c *bybitSpotClient) String() string {
	if c == nil || c.bybitBase == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return c.meta.redactedString()
}

func (c *bybitSpotClient) GoString() string { return c.String() }

func (c *bybitPerpClient) String() string {
	if c == nil || c.bybitBase == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return c.meta.redactedString()
}

func (c *bybitPerpClient) GoString() string { return c.String() }

func (c *bitgetSpotClient) String() string {
	if c == nil || c.bitgetBase == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return c.meta.redactedString()
}

func (c *bitgetSpotClient) GoString() string { return c.String() }

func (c *bitgetPerpClient) String() string {
	if c == nil || c.bitgetBase == nil {
		return "exchange/factory.Client{nil, credentials:redacted}"
	}
	return c.meta.redactedString()
}

func (c *bitgetPerpClient) GoString() string { return c.String() }

func (c *bybitBase) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := ctxErr(ctx, c.meta, "Instruments"); err != nil {
		return nil, err
	}
	if c.meta.catalog != nil && c.meta.catalogScope != (exchange.CatalogScope{}) {
		snapshot, err := c.meta.catalog.Snapshot(ctx, c.meta.catalogScope)
		if err != nil {
			return nil, withExchangeOperation(err, "Instruments")
		}
		return append([]exchange.Instrument(nil), snapshot.Instruments...), nil
	}
	rows, err := c.sdk.GetInstruments(ctx, c.category)
	if err != nil {
		return nil, normErr(c.meta, "Instruments", err)
	}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if !strings.EqualFold(row.Status, "Trading") {
			continue
		}
		if c.settleCoin != "" && !strings.EqualFold(row.SettleCoin, c.settleCoin) {
			continue
		}
		priceIncrement, err := parseOptionalDecimal(c.meta, "Instruments", row.PriceFilter.TickSize)
		if err != nil {
			return nil, err
		}
		quantityIncrement, err := firstPositiveDecimalStrict(c.meta, "Instruments", row.LotSizeFilter.QtyStep, row.LotSizeFilter.BasePrecision)
		if err != nil {
			return nil, err
		}
		minQuantity, err := parseOptionalDecimal(c.meta, "Instruments", row.LotSizeFilter.MinOrderQty)
		if err != nil {
			return nil, err
		}
		minNotional, err := optionalPositiveStrict(c.meta, "Instruments", row.LotSizeFilter.MinNotionalValue, row.LotSizeFilter.MinOrderAmt)
		if err != nil {
			return nil, err
		}
		instrument := newInstrument(
			c.meta.venue,
			c.meta.product,
			canonicalSymbol(row.Symbol, row.BaseCoin, row.QuoteCoin),
			row.BaseCoin,
			row.QuoteCoin,
			row.SettleCoin,
			row.Symbol,
			"",
			priceIncrement,
			quantityIncrement,
			minQuantity,
			minNotional,
			oneContract(),
		)
		out = append(out, instrument)
	}
	return out, nil
}

func (c *bitgetBase) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := ctxErr(ctx, c.meta, "Instruments"); err != nil {
		return nil, err
	}
	if c.meta.catalog != nil && c.meta.catalogScope != (exchange.CatalogScope{}) {
		snapshot, err := c.meta.catalog.Snapshot(ctx, c.meta.catalogScope)
		if err != nil {
			return nil, withExchangeOperation(err, "Instruments")
		}
		return append([]exchange.Instrument(nil), snapshot.Instruments...), nil
	}
	category, symbol := c.bitgetCategoryAndSymbol("")
	rows, err := c.sdk.GetInstruments(ctx, category, symbol)
	if err != nil {
		return nil, normErr(c.meta, "Instruments", err)
	}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.Status != "" && !strings.EqualFold(row.Status, "online") && !strings.EqualFold(row.Status, "normal") {
			continue
		}
		if c.meta.product == exchange.ProductSpot &&
			row.SymbolType != "" &&
			!strings.EqualFold(row.SymbolType, "crypto") {
			continue
		}
		if c.meta.product == exchange.ProductPerp {
			rowQuote := strings.ToUpper(strings.TrimSpace(row.QuoteCoin))
			if rowQuote == "" {
				rowQuote = quoteAsset(row.Symbol)
			}
			if rowQuote != c.marginCoin {
				continue
			}
		}
		priceIncrement, err := firstPositiveDecimalStrict(c.meta, "Instruments", row.PriceMultiplier, precisionIncrement(row.PricePrecision))
		if err != nil {
			return nil, err
		}
		if !priceIncrement.IsPositive() {
			return nil, malformed(c.meta, "Instruments", "price increment is required")
		}
		quantityIncrement, err := firstPositiveDecimalStrict(c.meta, "Instruments", row.QuantityMultiplier, precisionIncrement(row.QuantityPrecision))
		if err != nil {
			return nil, err
		}
		if !quantityIncrement.IsPositive() {
			return nil, malformed(c.meta, "Instruments", "quantity increment is required")
		}
		minQuantity, err := parseOptionalDecimal(c.meta, "Instruments", row.MinOrderQty)
		if err != nil {
			return nil, err
		}
		minNotional, err := optionalPositiveStrict(c.meta, "Instruments", row.MinOrderAmount)
		if err != nil {
			return nil, err
		}
		inst := newInstrument(
			c.meta.venue,
			c.meta.product,
			canonicalSymbol(row.Symbol, row.BaseCoin, row.QuoteCoin),
			row.BaseCoin,
			row.QuoteCoin,
			c.marginCoin,
			row.Symbol,
			"",
			priceIncrement,
			quantityIncrement,
			minQuantity,
			minNotional,
			oneContract(),
		)
		out = append(out, inst)
	}
	return out, nil
}

func (c *bybitBase) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := ctxErr(ctx, c.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, canonical, err := c.symbols(req.Instrument, "OrderBook")
	if err != nil {
		return exchange.OrderBook{}, err
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, invalid(c.meta, "OrderBook", "limit must be non-negative")
	}
	row, err := c.sdk.GetOrderBook(ctx, c.category, native, req.Limit)
	if err != nil {
		return exchange.OrderBook{}, normErr(c.meta, "OrderBook", err)
	}
	bids, err := numberStringBook(row.Bids)
	if err != nil {
		return exchange.OrderBook{}, malformed(c.meta, "OrderBook", err.Error())
	}
	asks, err := numberStringBook(row.Asks)
	if err != nil {
		return exchange.OrderBook{}, malformed(c.meta, "OrderBook", err.Error())
	}
	observedAt, err := parseRequiredMilliInt64(c.meta, "OrderBook", "timestamp", row.TS)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	return exchange.OrderBook{Instrument: canonical, Bids: bids, Asks: asks, Time: observedAt, Sequence: strconv.FormatInt(row.U, 10), Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (c *bitgetBase) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := ctxErr(ctx, c.meta, "OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, canonical, err := c.symbols(req.Instrument, "OrderBook")
	if err != nil {
		return exchange.OrderBook{}, err
	}
	if req.Limit < 0 {
		return exchange.OrderBook{}, invalid(c.meta, "OrderBook", "limit must be non-negative")
	}
	category := c.bitgetCategory()
	row, err := c.sdk.GetOrderBook(ctx, category, native, req.Limit)
	if err != nil {
		return exchange.OrderBook{}, normErr(c.meta, "OrderBook", err)
	}
	bids, err := numberStringBook(row.Bids)
	if err != nil {
		return exchange.OrderBook{}, malformed(c.meta, "OrderBook", err.Error())
	}
	asks, err := numberStringBook(row.Asks)
	if err != nil {
		return exchange.OrderBook{}, malformed(c.meta, "OrderBook", err.Error())
	}
	observedAt, err := parseRequiredMilli(c.meta, "OrderBook", "timestamp", row.TS)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	return exchange.OrderBook{Instrument: canonical, Bids: bids, Asks: asks, Time: observedAt, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (c *bybitBase) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := ctxErr(ctx, c.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "Candles")
	if err != nil {
		return exchange.CandlePage{}, err
	}
	if err := validateWindow(c.meta, "Candles", req.Interval, req.Start, req.End, req.Limit, req.Cursor); err != nil {
		return exchange.CandlePage{}, err
	}
	interval, err := bybitInterval(c.meta, "Candles", req.Interval)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	rows, err := c.sdk.GetKlines(ctx, c.category, native, interval, millis(req.Start), millis(req.End), req.Limit)
	if err != nil {
		return exchange.CandlePage{}, normErr(c.meta, "Candles", err)
	}
	candles, err := convertCandles(c.meta, "Candles", rows)
	if err != nil {
		return exchange.CandlePage{}, malformed(c.meta, "Candles", err.Error())
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (c *bitgetBase) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := ctxErr(ctx, c.meta, "Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "Candles")
	if err != nil {
		return exchange.CandlePage{}, err
	}
	if err := validateWindow(c.meta, "Candles", req.Interval, req.Start, req.End, req.Limit, req.Cursor); err != nil {
		return exchange.CandlePage{}, err
	}
	interval, err := bitgetInterval(c.meta, "Candles", req.Interval)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	category := c.bitgetCategory()
	remaining := req.Limit
	pageEnd := req.End
	candlesByOpenTime := make(map[int64]exchange.Candle)
	for {
		pageLimit := remaining
		if pageLimit == 0 || pageLimit > bitgetCandlePageLimit {
			pageLimit = bitgetCandlePageLimit
		}
		rows, err := c.sdk.GetCandles(ctx, category, native, interval, "market", millis(req.Start), millis(pageEnd), pageLimit)
		if err != nil {
			return exchange.CandlePage{}, normErr(c.meta, "Candles", err)
		}
		pageCandles, err := convertCandles(c.meta, "Candles", rows)
		if err != nil {
			return exchange.CandlePage{}, malformed(c.meta, "Candles", err.Error())
		}
		if len(pageCandles) == 0 {
			break
		}

		earliest := pageCandles[0].OpenTime
		added := 0
		for _, candle := range pageCandles {
			if candle.OpenTime.Before(earliest) {
				earliest = candle.OpenTime
			}
			key := candle.OpenTime.UnixMilli()
			if _, exists := candlesByOpenTime[key]; !exists {
				added++
			}
			candlesByOpenTime[key] = candle
		}
		if req.Limit == 0 || len(rows) < pageLimit {
			break
		}
		remaining -= added
		if remaining <= 0 {
			break
		}
		nextEnd := earliest.Add(-time.Millisecond)
		if (!req.Start.IsZero() && !nextEnd.After(req.Start)) || (!pageEnd.IsZero() && !nextEnd.Before(pageEnd)) {
			break
		}
		pageEnd = nextEnd
	}

	candles := make([]exchange.Candle, 0, len(candlesByOpenTime))
	for _, candle := range candlesByOpenTime {
		candles = append(candles, candle)
	}
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].OpenTime.Before(candles[j].OpenTime)
	})
	if req.Limit > 0 && len(candles) > req.Limit {
		candles = candles[len(candles)-req.Limit:]
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (c *bybitBase) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := ctxErr(ctx, c.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, canonical, err := c.symbols(req.Instrument, "PublicTrades")
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, invalid(c.meta, "PublicTrades", "limit must be non-negative")
	}
	rows, err := c.sdk.GetRecentTrades(ctx, c.category, native, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, normErr(c.meta, "PublicTrades", err)
	}
	out := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		side, err := parseSide(c.meta, "PublicTrades", row.Side)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		price, err := parseRequiredDecimal(c.meta, "PublicTrades", "price", row.Price)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		quantity, err := parseRequiredDecimal(c.meta, "PublicTrades", "quantity", row.Size)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		executedAt, err := parseRequiredMilli(c.meta, "PublicTrades", "timestamp", row.Time)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		out = append(out, exchange.PublicTrade{Instrument: canonical, TradeID: row.ExecID, Side: side, Price: price, Quantity: quantity, Time: executedAt})
	}
	return exchange.PublicTradePage{Trades: out, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (c *bitgetBase) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := ctxErr(ctx, c.meta, "PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, canonical, err := c.symbols(req.Instrument, "PublicTrades")
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	if req.Limit < 0 {
		return exchange.PublicTradePage{}, invalid(c.meta, "PublicTrades", "limit must be non-negative")
	}
	category := c.bitgetCategory()
	rows, err := c.sdk.GetRecentFills(ctx, category, native, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, normErr(c.meta, "PublicTrades", err)
	}
	out := make([]exchange.PublicTrade, 0, len(rows))
	for _, row := range rows {
		id := row.ExecID
		if id == "" {
			id = row.ExecLinkID
		}
		side, err := parseSide(c.meta, "PublicTrades", row.Side)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		price, err := parseRequiredDecimal(c.meta, "PublicTrades", "price", row.Price)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		quantity, err := parseRequiredDecimal(c.meta, "PublicTrades", "quantity", row.Size)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		executedAt, err := parseRequiredMilli(c.meta, "PublicTrades", "timestamp", row.Timestamp)
		if err != nil {
			return exchange.PublicTradePage{}, err
		}
		out = append(out, exchange.PublicTrade{Instrument: canonical, TradeID: id, Side: side, Price: price, Quantity: quantity, Time: executedAt})
	}
	return exchange.PublicTradePage{Trades: out, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (c *bybitBase) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return c.bybitPlace(ctx, req)
}

func (c *bitgetBase) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return c.bitgetPlace(ctx, req)
}

func (c *bybitBase) bybitPlace(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, c.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(c.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, _, err := c.symbols(req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	quantity, marketUnit, err := c.bybitOrderQuantity(ctx, native, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := c.sdk.PlaceOrder(ctx, bybit.PlaceOrderRequest{Category: c.category, Symbol: native, Side: bybitSide(req.Side), OrderType: bybitOrderType(req.Type), Qty: quantity, Price: priceString(req), TimeInForce: tif(req.LimitPolicy), ReduceOnly: req.ReduceOnly, OrderLinkID: req.ClientOrderID, MarketUnit: marketUnit})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(c.meta, "PlaceOrder", err.Error())
		}
		return commandAck(c.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	if err := validateSuccessfulOrderAction(c.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, resp.OrderID, resp.OrderLinkID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(c.meta, exchange.OrderOperationPlace, native, resp.OrderID, resp.OrderLinkID)
	ack.OrderType = req.Type
	return ack, ack.Validate()
}

func (c *bybitBase) bybitOrderQuantity(ctx context.Context, native string, req exchange.PlaceOrderRequest) (string, string, error) {
	if c.meta.product != exchange.ProductSpot || req.Type != exchange.OrderTypeMarket || req.Side != exchange.SideBuy {
		return req.Quantity.String(), "", nil
	}
	book, err := c.sdk.GetOrderBook(ctx, c.category, native, 1)
	if err != nil {
		return "", "", normErr(c.meta, "PlaceOrder", err)
	}
	if len(book.Asks) == 0 || len(book.Asks[0]) < 2 {
		return "", "", malformed(c.meta, "PlaceOrder", "empty best ask")
	}
	ask, err := parseRequiredDecimal(c.meta, "PlaceOrder", "best ask", string(book.Asks[0][0]))
	if err != nil {
		return "", "", err
	}
	return req.Quantity.Mul(ask).String(), "quoteCoin", nil
}

func (c *bitgetBase) bitgetPlace(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, c.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(c.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, _, err := c.symbols(req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	quantity, err := c.bitgetOrderQuantity(ctx, native, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	positionSide, err := c.bitgetPositionSide(ctx, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := c.sdk.PlaceOrder(ctx, &bitget.PlaceOrderRequest{Category: c.bitgetPrivateCategory(), Symbol: native, Side: strings.ToLower(string(req.Side)), OrderType: strings.ToLower(string(req.Type)), Qty: quantity, Price: priceString(req), TimeInForce: bitgetTIF(req.LimitPolicy), MarginCoin: c.marginCoin, MarginMode: "crossed", ClientOID: req.ClientOrderID, ReduceOnly: bitgetReduceOnly(req, positionSide), PosSide: positionSide})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(c.meta, "PlaceOrder", err.Error())
		}
		return commandAck(c.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	if err := validateSuccessfulOrderAction(c.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, resp.OrderID, resp.ClientOID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(c.meta, exchange.OrderOperationPlace, native, resp.OrderID, resp.ClientOID)
	ack.OrderType = req.Type
	return ack, ack.Validate()
}

func bitgetReduceOnly(req exchange.PlaceOrderRequest, positionSide string) string {
	if positionSide != "" || !req.ReduceOnly {
		return ""
	}
	return "yes"
}

func (c *bitgetBase) bitgetOrderQuantity(ctx context.Context, native string, req exchange.PlaceOrderRequest) (string, error) {
	if c.meta.product != exchange.ProductSpot || req.Type != exchange.OrderTypeMarket || req.Side != exchange.SideBuy {
		return req.Quantity.String(), nil
	}
	book, err := c.sdk.GetOrderBook(ctx, c.bitgetCategory(), native, 1)
	if err != nil {
		return "", normErr(c.meta, "PlaceOrder", err)
	}
	if len(book.Asks) == 0 || len(book.Asks[0]) < 2 {
		return "", malformed(c.meta, "PlaceOrder", "empty best ask")
	}
	ask, err := parseRequiredDecimal(c.meta, "PlaceOrder", "best ask", string(book.Asks[0][0]))
	if err != nil {
		return "", err
	}
	return req.Quantity.Mul(ask).String(), nil
}

func (c *bitgetBase) bitgetPositionSide(ctx context.Context, req exchange.PlaceOrderRequest) (string, error) {
	if c.meta.product != exchange.ProductPerp {
		return "", nil
	}

	c.holdModeMu.Lock()
	defer c.holdModeMu.Unlock()
	if c.holdMode == "" {
		settings, err := c.sdk.GetAccountSettings(ctx)
		if err != nil {
			return "", normErr(c.meta, "PlaceOrder", err)
		}
		c.holdMode = strings.ToLower(strings.TrimSpace(settings.HoldMode))
	}

	switch c.holdMode {
	case "one_way_mode", "single_hold":
		return "", nil
	case "hedge_mode", "double_hold":
		if (req.Side == exchange.SideBuy && !req.ReduceOnly) || (req.Side == exchange.SideSell && req.ReduceOnly) {
			return "long", nil
		}
		return "short", nil
	default:
		return "", malformed(c.meta, "PlaceOrder", "unsupported account hold mode")
	}
}

func (c *bybitBase) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, c.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, _, err := c.symbols(req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, invalid(c.meta, "CancelOrder", "order id is required")
	}
	if !bybitNativeOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, invalid(c.meta, "CancelOrder", "order id must be a positive decimal int64 or UUID")
	}
	resp, err := c.sdk.CancelOrder(ctx, bybit.CancelOrderRequest{Category: c.category, Symbol: native, OrderID: req.OrderID})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(c.meta, "CancelOrder", err.Error())
		}
		return commandAck(c.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	if err := validateSuccessfulOrderAction(c.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", resp.OrderID, resp.OrderLinkID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(c.meta, exchange.OrderOperationCancel, native, resp.OrderID, resp.OrderLinkID)
	ack.State = exchange.AckCanceled
	return ack, ack.Validate()
}

func (c *bitgetBase) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, c.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, _, err := c.symbols(req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, invalid(c.meta, "CancelOrder", "order id is required")
	}
	if !positiveDecimalOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, invalid(c.meta, "CancelOrder", "order id must be a positive decimal int64")
	}
	resp, err := c.sdk.CancelOrder(ctx, &bitget.CancelOrderRequest{Category: c.bitgetPrivateCategory(), Symbol: native, OrderID: req.OrderID})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(c.meta, "CancelOrder", err.Error())
		}
		return commandAck(c.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	if err := validateSuccessfulOrderAction(c.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", resp.OrderID, resp.ClientOID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(c.meta, exchange.OrderOperationCancel, native, resp.OrderID, resp.ClientOID)
	ack.State = exchange.AckCanceled
	return ack, ack.Validate()
}

func (c *bybitBase) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := ctxErr(ctx, c.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "OpenOrders")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := c.sdk.GetOpenOrders(ctx, c.category, native)
	if err != nil {
		return exchange.OrderPage{}, normErr(c.meta, "OpenOrders", err)
	}
	orders, err := convertBybitOrders(ctx, c.meta, "OpenOrders", rows, native)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (c *bitgetBase) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := ctxErr(ctx, c.meta, "OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "OpenOrders")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := c.sdk.GetOpenOrders(ctx, c.bitgetPrivateCategory(), native)
	if err != nil {
		return exchange.OrderPage{}, normErr(c.meta, "OpenOrders", err)
	}
	orders, err := convertBitgetOrders(ctx, c.meta, "OpenOrders", rows, native)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (c *bybitBase) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := ctxErr(ctx, c.meta, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "OrderHistory")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	rows, err := c.sdk.GetOrderHistoryWithRequest(ctx, bybit.GetOrderHistoryRequest{Category: c.category, Symbol: native, SettleCoin: c.settleCoin, StartMillis: millis(req.Start), EndMillis: millis(req.End)})
	if err != nil {
		return exchange.OrderPage{}, normErr(c.meta, "OrderHistory", err)
	}
	orders, err := convertBybitOrders(ctx, c.meta, "OrderHistory", rows, native)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (c *bitgetBase) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := ctxErr(ctx, c.meta, "OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "OrderHistory")
	if err != nil {
		return exchange.OrderPage{}, err
	}
	rows, hasMore, err := c.sdk.GetOrderHistoryBounded(ctx, bitget.GetOrderHistoryRequest{Category: c.bitgetPrivateCategory(), Symbol: native, StartTime: milliString(req.Start), EndTime: milliString(req.End), Limit: limitString(req.Limit), Cursor: req.Cursor})
	if err != nil {
		return exchange.OrderPage{}, normErr(c.meta, "OrderHistory", err)
	}
	orders, err := convertBitgetOrders(ctx, c.meta, "OrderHistory", rows, native)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	page := boundedOrderPage(orders, req.Limit, req.Cursor)
	page.Page.HasMoreKnown = true
	page.Page.HasMore = hasMore
	return page, nil
}

func (c *bybitBase) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := ctxErr(ctx, c.meta, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "Fills")
	if err != nil {
		return exchange.FillPage{}, err
	}
	if req.Cursor != "" {
		return exchange.FillPage{}, invalid(c.meta, "Fills", "cursor is not supported by Bybit executions")
	}
	rows, hasMore, err := c.sdk.GetExecutionsBounded(ctx, bybit.GetExecutionsRequest{Category: c.category, Symbol: native, OrderID: req.OrderID, StartMillis: millis(req.Start), EndMillis: millis(req.End), Limit: req.Limit})
	if err != nil {
		return exchange.FillPage{}, normErr(c.meta, "Fills", err)
	}
	fills, err := convertBybitFills(ctx, c.meta, "Fills", rows, native)
	if err != nil {
		return exchange.FillPage{}, err
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Cursor: req.Cursor, Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End, HasMoreKnown: true, HasMore: hasMore}}, nil
}

func (c *bitgetBase) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := ctxErr(ctx, c.meta, "Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "Fills")
	if err != nil {
		return exchange.FillPage{}, err
	}
	rows, hasMore, err := c.sdk.GetFillsBounded(ctx, bitget.GetFillsRequest{Category: c.bitgetPrivateCategory(), Symbol: native, OrderID: req.OrderID, StartTime: milliString(req.Start), EndTime: milliString(req.End), Limit: limitString(req.Limit), Cursor: req.Cursor})
	if err != nil {
		return exchange.FillPage{}, normErr(c.meta, "Fills", err)
	}
	fills, err := convertBitgetFills(ctx, c.meta, "Fills", rows, native)
	if err != nil {
		return exchange.FillPage{}, err
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Cursor: req.Cursor, Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End, HasMoreKnown: true, HasMore: hasMore}}, nil
}

func (c *bybitBase) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := ctxErr(ctx, c.meta, "Balances"); err != nil {
		return nil, err
	}
	if c.meta.product == exchange.ProductPerp {
		account, err := c.PerpAccount(ctx)
		return account.Balances, err
	}
	account, err := c.SpotAccount(ctx)
	return account.Balances, err
}

func (c *bitgetBase) Balances(ctx context.Context) ([]exchange.Balance, error) {
	if err := ctxErr(ctx, c.meta, "Balances"); err != nil {
		return nil, err
	}
	if c.meta.product == exchange.ProductPerp {
		account, err := c.PerpAccount(ctx)
		return account.Balances, err
	}
	account, err := c.SpotAccount(ctx)
	return account.Balances, err
}

func (c *bybitBase) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	if err := ctxErr(ctx, c.meta, "SpotAccount"); err != nil {
		return exchange.SpotAccount{}, err
	}
	resp, err := c.sdk.GetWalletBalance(ctx, "UNIFIED", "")
	if err != nil {
		return exchange.SpotAccount{}, normErr(c.meta, "SpotAccount", err)
	}
	balances, err := bybitBalances(c.meta, "SpotAccount", resp)
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (c *bitgetBase) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	if err := ctxErr(ctx, c.meta, "SpotAccount"); err != nil {
		return exchange.SpotAccount{}, err
	}
	resp, err := c.sdk.GetAccountAssets(ctx)
	if err != nil {
		return exchange.SpotAccount{}, normErr(c.meta, "SpotAccount", err)
	}
	balances, err := bitgetBalances(c.meta, "SpotAccount", resp.Assets)
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (c *bybitBase) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := ctxErr(ctx, c.meta, "PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	resp, err := c.sdk.GetWalletBalance(ctx, "UNIFIED", c.settleCoin)
	if err != nil {
		return exchange.PerpAccount{}, normErr(c.meta, "PerpAccount", err)
	}
	balances, err := bybitBalances(c.meta, "PerpAccount", resp)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	account := exchange.PerpAccount{Balances: balances}
	if len(resp.List) > 0 {
		row := resp.List[0]
		equity, err := optionalPositiveStrict(c.meta, "PerpAccount", row.TotalEquity)
		if err != nil {
			return exchange.PerpAccount{}, err
		}
		available, err := optionalPositiveStrict(c.meta, "PerpAccount", row.TotalAvailableBalance)
		if err != nil {
			return exchange.PerpAccount{}, err
		}
		unrealizedPnL, err := optStrict(c.meta, "PerpAccount", row.TotalPerpUPL)
		if err != nil {
			return exchange.PerpAccount{}, err
		}
		account.Equity = equity
		account.Available = available
		if account.Equity.Valid && account.Available.Valid {
			account.MarginUsed = exchange.OptionalDecimal{Value: account.Equity.Value.Sub(account.Available.Value), Valid: true}
		}
		account.UnrealizedPnL = unrealizedPnL
	}
	return account, nil
}

func (c *bitgetBase) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := ctxErr(ctx, c.meta, "PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	resp, err := c.sdk.GetAccountAssets(ctx)
	if err != nil {
		return exchange.PerpAccount{}, normErr(c.meta, "PerpAccount", err)
	}
	balances, err := bitgetBalances(c.meta, "PerpAccount", resp.Assets)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	equity, err := optStrict(c.meta, "PerpAccount", resp.AccountEquity)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	available, err := optStrict(c.meta, "PerpAccount", resp.Available)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	unrealizedPnL, err := optStrict(c.meta, "PerpAccount", resp.UnrealizedPL)
	if err != nil {
		return exchange.PerpAccount{}, err
	}
	account := exchange.PerpAccount{Balances: balances, Equity: equity, Available: available, UnrealizedPnL: unrealizedPnL}
	if account.Equity.Valid && account.Available.Valid {
		account.MarginUsed = exchange.OptionalDecimal{Value: account.Equity.Value.Sub(account.Available.Value), Valid: true}
	}
	return account, nil
}

func (c *bybitBase) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := ctxErr(ctx, c.meta, "Positions"); err != nil {
		return nil, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "Positions")
	if err != nil {
		return nil, err
	}
	rows, err := c.sdk.GetPositions(ctx, c.category, native, c.settleCoin)
	if err != nil {
		return nil, normErr(c.meta, "Positions", err)
	}
	return convertBybitPositions(ctx, c.meta, "Positions", rows, native)
}

func (c *bitgetBase) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := ctxErr(ctx, c.meta, "Positions"); err != nil {
		return nil, err
	}
	native, err := c.optionalNativeSymbol(req.Instrument, "Positions")
	if err != nil {
		return nil, err
	}
	rows, err := c.sdk.GetCurrentPositions(ctx, c.bitgetPrivateCategory(), native)
	if err != nil {
		return nil, normErr(c.meta, "Positions", err)
	}
	return convertBitgetPositions(ctx, c.meta, "Positions", rows, native)
}

func (c *bybitBase) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := ctxErr(ctx, c.meta, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	native, _, err := c.symbols(req.Instrument, "FundingRate")
	if err != nil {
		return exchange.FundingRate{}, err
	}
	ticker, err := c.sdk.GetTicker(ctx, c.category, native)
	if err != nil {
		return exchange.FundingRate{}, normErr(c.meta, "FundingRate", err)
	}
	rate, err := parseRequiredDecimal(c.meta, "FundingRate", "funding rate", ticker.FundingRate)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	markPrice, err := optStrict(c.meta, "FundingRate", ticker.MarkPrice)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	observedAt, err := parseRequiredMilli(c.meta, "FundingRate", "timestamp", firstNonEmpty(ticker.Time, ticker.TS))
	if err != nil {
		return exchange.FundingRate{}, err
	}
	nextFundingTime, err := parseRequiredMilli(c.meta, "FundingRate", "next funding timestamp", ticker.NextFundingTime)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	return exchange.FundingRate{NativeSymbol: native, Rate: rate, MarkPrice: markPrice, ObservedAt: observedAt, NextFundingTime: nextFundingTime}, nil
}

func (c *bitgetBase) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := ctxErr(ctx, c.meta, "FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	native, _, err := c.symbols(req.Instrument, "FundingRate")
	if err != nil {
		return exchange.FundingRate{}, err
	}
	rows, err := c.sdk.GetCurrentFundRate(ctx, native, c.productType)
	if err != nil {
		return exchange.FundingRate{}, normErr(c.meta, "FundingRate", err)
	}
	if len(rows) == 0 {
		return exchange.FundingRate{}, malformed(c.meta, "FundingRate", "empty funding rate response")
	}
	row := rows[0]
	rate, err := parseRequiredDecimal(c.meta, "FundingRate", "funding rate", row.FundingRate)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	observedAt, err := parseRequiredMilliInt64(c.meta, "FundingRate", "request timestamp", row.RequestTime)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	nextFundingTime, err := parseRequiredMilli(c.meta, "FundingRate", "next funding timestamp", row.NextUpdate)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	return exchange.FundingRate{NativeSymbol: native, Rate: rate, ObservedAt: observedAt, NextFundingTime: nextFundingTime}, nil
}

func (c *bybitBase) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := ctxErr(ctx, c.meta, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	if err := validateFundingHistoryRequest(c.meta, "FundingRateHistory", req, true); err != nil {
		return exchange.FundingRatePage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "FundingRateHistory")
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	rows, err := c.sdk.GetFundingHistory(ctx, c.category, native, millis(req.Start), millis(req.End), req.Limit)
	if err != nil {
		return exchange.FundingRatePage{}, normErr(c.meta, "FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		rate, err := parseRequiredDecimal(c.meta, "FundingRateHistory", "funding rate", row.FundingRate)
		if err != nil {
			return exchange.FundingRatePage{}, err
		}
		fundingTime, err := parseRequiredMilli(c.meta, "FundingRateHistory", "funding timestamp", row.FundingRateTimestamp)
		if err != nil {
			return exchange.FundingRatePage{}, err
		}
		rates = append(rates, exchange.FundingRate{NativeSymbol: native, Rate: rate, FundingTime: fundingTime})
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (c *bybitPerpClient) FundingPayments(ctx context.Context, req exchange.FundingPaymentHistoryRequest) (exchange.FundingPaymentPage, error) {
	const operation = "FundingPayments"
	if err := ctxErr(ctx, c.meta, operation); err != nil {
		return exchange.FundingPaymentPage{}, err
	}
	if c.category != "linear" || c.settleCoin != "USDT" {
		return exchange.FundingPaymentPage{}, invalid(c.meta, operation, "bybit funding payments support only USDT linear settlement")
	}
	settlement := strings.ToUpper(strings.TrimSpace(req.SettlementAsset))
	if settlement == "" {
		settlement = c.settleCoin
	}
	if settlement != "USDT" {
		return exchange.FundingPaymentPage{}, invalid(c.meta, operation, "bybit funding payments support only USDT settlement")
	}

	native := ""
	baseCoin := ""
	if req.Instrument != nil {
		resolvedNative, _, err := c.symbols(*req.Instrument, operation)
		if err != nil {
			return exchange.FundingPaymentPage{}, err
		}
		native = resolvedNative
		baseCoin = strings.ToUpper(strings.TrimSpace(req.Instrument.Identity.BaseAsset))
	}
	if err := validateBoundedHistory(req.Cursor, req.Limit, req.Start, req.End); err != nil {
		return exchange.FundingPaymentPage{}, invalid(c.meta, operation, err.Error())
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > bybitFundingPaymentPortableLimit {
		return exchange.FundingPaymentPage{}, invalid(c.meta, operation, "limit must be between 1 and 500")
	}

	cursorScope := fundingPaymentCursorScope(req.Instrument, settlement, req.Start, req.End, limit)
	cursorKey := c.bybitFundingPaymentCursorKey()
	cursorEnvelope, err := decodeFundingPaymentCursor(req.Cursor, cursorKey, cursorScope)
	if err != nil {
		return exchange.FundingPaymentPage{}, invalid(c.meta, operation, err.Error())
	}

	items := make([]exchange.FundingPayment, 0, limit)
	observedAt := time.Now().UTC()
	hasMore := false
	cursor := ""
	sliceStart := req.Start
	if cursorEnvelope.SliceStartMillis > 0 {
		sliceStart = time.UnixMilli(cursorEnvelope.SliceStartMillis).UTC()
	}
	splitWindow := bybitFundingPaymentSplitWindow(req.Start, req.End)
	venueCursor := cursorEnvelope.VenueCursor
	for {
		nativeStart := req.Start
		nativeEnd := req.End
		if splitWindow {
			if sliceStart.Before(req.Start) || !sliceStart.Before(req.End) {
				return exchange.FundingPaymentPage{}, invalid(c.meta, operation, "cursor slice is outside funding payment window")
			}
			nativeStart = sliceStart
			nativeEnd = sliceStart.Add(bybitFundingPaymentNativeWindow)
			if nativeEnd.After(req.End) {
				nativeEnd = req.End
			}
		}
		nativeQueryEnd := nativeEnd
		if splitWindow && nativeEnd.Before(req.End) {
			nativeQueryEnd = nativeEnd.Add(-time.Millisecond)
		}
		remaining := limit - len(items)
		if remaining <= 0 {
			if splitWindow && nativeStart.Before(req.End) {
				next := cursorScope
				next.ItemsSeen = cursorEnvelope.ItemsSeen + len(items)
				next.SliceStartMillis = optionalMillis(nativeStart)
				cursor = encodeFundingPaymentCursor(cursorKey, next)
				hasMore = true
			}
			break
		}
		nativeLimit := remaining
		if nativeLimit > bybitFundingPaymentNativeLimit {
			nativeLimit = bybitFundingPaymentNativeLimit
		}
		result, err := c.sdk.GetTransactionLog(ctx, bybit.TransactionLogRequest{
			AccountType: "UNIFIED",
			Category:    c.category,
			Currency:    settlement,
			BaseCoin:    baseCoin,
			Type:        "SETTLEMENT",
			StartMillis: optionalMillis(nativeStart),
			EndMillis:   optionalMillis(nativeQueryEnd),
			Limit:       nativeLimit,
			Cursor:      venueCursor,
		})
		if err != nil {
			return exchange.FundingPaymentPage{}, normErr(c.meta, operation, err)
		}

		for _, row := range result.List {
			if row.Type != "SETTLEMENT" {
				return exchange.FundingPaymentPage{}, malformed(c.meta, operation, "response transaction type does not match request")
			}
			if row.Category != "" && row.Category != c.category {
				return exchange.FundingPaymentPage{}, malformed(c.meta, operation, "response category does not match request")
			}
			if native != "" && row.Symbol != native {
				return exchange.FundingPaymentPage{}, malformed(c.meta, operation, "response instrument does not match request")
			}
			if row.Currency != settlement {
				return exchange.FundingPaymentPage{}, malformed(c.meta, operation, "response settlement asset does not match request")
			}
			payment, err := c.bybitFundingPayment(operation, req.Instrument, row, observedAt)
			if err != nil {
				return exchange.FundingPaymentPage{}, err
			}
			if !withinHalfOpen(payment.EffectiveAt, req.Start, req.End) {
				continue
			}
			items = append(items, payment)
		}

		if result.NextPageCursor != "" {
			next := cursorScope
			next.VenueCursor = result.NextPageCursor
			next.ItemsSeen = cursorEnvelope.ItemsSeen + len(items)
			if splitWindow {
				next.SliceStartMillis = optionalMillis(nativeStart)
			}
			cursor = encodeFundingPaymentCursor(cursorKey, next)
			hasMore = true
			break
		}
		venueCursor = ""
		if len(items) >= limit {
			if splitWindow && nativeEnd.Before(req.End) {
				next := cursorScope
				next.ItemsSeen = cursorEnvelope.ItemsSeen + len(items)
				next.SliceStartMillis = optionalMillis(nativeEnd)
				cursor = encodeFundingPaymentCursor(cursorKey, next)
				hasMore = true
			}
			break
		}
		if !splitWindow || !nativeEnd.Before(req.End) {
			break
		}
		sliceStart = nativeEnd
	}
	status := fundingPaymentCompletionStatus(cursorEnvelope.ItemsSeen+len(items), hasMore, req.Start, req.End, fundingPaymentRetainedSince(req.Start, observedAt, 24), observedAt)
	return exchange.FundingPaymentPage{
		Items: items,
		Page: exchange.PageInfo{
			Cursor: cursor, Limit: limit, WindowStart: req.Start, WindowEnd: req.End,
			HasMoreKnown: true, HasMore: hasMore,
		},
		Status:          status,
		Watermark:       fundingPaymentWatermark(cursor, req.End, status, observedAt),
		CompleteThrough: fundingPaymentCompleteThrough(req.End, status),
		Revision:        1,
	}, nil
}

const (
	bybitFundingPaymentNativeLimit   = 50
	bybitFundingPaymentPortableLimit = 500
	bybitFundingPaymentNativeWindow  = 7 * 24 * time.Hour
)

func bybitFundingPaymentSplitWindow(start, end time.Time) bool {
	return !start.IsZero() && !end.IsZero() && end.Sub(start) > bybitFundingPaymentNativeWindow
}

func (c *bybitPerpClient) bybitFundingPaymentCursorKey() []byte {
	return fundingPaymentCursorKey(
		"venue="+string(exchange.VenueBybit),
		"product="+string(exchange.ProductPerp),
		"category="+c.category,
		"settle="+c.settleCoin,
		"base_url="+c.profile.RESTBaseURL,
		"api_key="+c.apiKey,
		"secret_key="+c.secretKey,
	)
}

func (c *bybitPerpClient) bybitFundingPayment(operation string, instrument *exchange.Instrument, row bybit.TransactionLogRecord, observedAt time.Time) (exchange.FundingPayment, error) {
	paymentID := strings.TrimSpace(row.ID)
	if paymentID == "" {
		return exchange.FundingPayment{}, malformed(c.meta, operation, "bybit funding payment id is missing")
	}
	nativeSymbol := strings.TrimSpace(row.Symbol)
	if nativeSymbol == "" {
		return exchange.FundingPayment{}, malformed(c.meta, operation, "bybit funding payment symbol is missing")
	}
	amount, err := parseRequiredDecimal(c.meta, operation, "funding payment amount", row.Funding)
	if err != nil {
		return exchange.FundingPayment{}, err
	}
	effectiveAt, err := parseRequiredMilli(c.meta, operation, "transaction timestamp", row.TransactionTime)
	if err != nil {
		return exchange.FundingPayment{}, err
	}
	nativeID := ""
	if instrument != nil {
		nativeID = instrument.Identity.NativeID
	}
	sourceRef := "bybit:v5:account:transaction-log:SETTLEMENT:" + paymentID
	return exchange.FundingPayment{
		PaymentID:       paymentID,
		FactID:          stableFundingPaymentFactID(sourceRef, nativeSymbol, row.Funding, row.TransactionTime),
		NativeSymbol:    nativeSymbol,
		NativeID:        nativeID,
		SettlementAsset: row.Currency,
		Amount:          amount,
		EffectiveAt:     effectiveAt,
		ObservedAt:      observedAt,
		Revision:        1,
		Version:         "bybit-v5-transaction-log-v1",
		SourceRef:       sourceRef,
		FormulaVersion:  "actual-funding-payment-v1",
	}, nil
}

func (c *bitgetBase) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := ctxErr(ctx, c.meta, "FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	if err := validateFundingHistoryRequest(c.meta, "FundingRateHistory", req, true); err != nil {
		return exchange.FundingRatePage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "FundingRateHistory")
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	resultLimit := req.Limit
	if resultLimit <= 0 {
		resultLimit = 20
	}
	pageSize := resultLimit
	if !req.Start.IsZero() || !req.End.IsZero() || pageSize > 100 {
		pageSize = 100
	}
	rates := make([]exchange.FundingRate, 0, resultLimit)
	for pageNo := 1; len(rates) < resultLimit; pageNo++ {
		rows, err := c.sdk.GetHistoryFundRate(ctx, native, c.productType, pageSize, pageNo)
		if err != nil {
			return exchange.FundingRatePage{}, normErr(c.meta, "FundingRateHistory", err)
		}
		if len(rows) == 0 {
			break
		}
		oldest := time.Time{}
		for _, row := range rows {
			fundingTime, err := parseRequiredMilli(c.meta, "FundingRateHistory", "funding timestamp", row.FundingTime)
			if err != nil {
				return exchange.FundingRatePage{}, err
			}
			if oldest.IsZero() || fundingTime.Before(oldest) {
				oldest = fundingTime
			}
			if !withinHalfOpen(fundingTime, req.Start, req.End) {
				continue
			}
			rate, err := parseRequiredDecimal(c.meta, "FundingRateHistory", "funding rate", row.FundingRate)
			if err != nil {
				return exchange.FundingRatePage{}, err
			}
			rates = append(rates, exchange.FundingRate{NativeSymbol: native, Rate: rate, FundingTime: fundingTime})
			if len(rates) == resultLimit {
				break
			}
		}
		if len(rows) < pageSize || (!req.Start.IsZero() && !oldest.After(req.Start)) {
			break
		}
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{
		Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End,
	}}, nil
}

func (c *bybitBase) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := ctxErr(ctx, c.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "SetLeverage")
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 {
		return exchange.Leverage{}, invalid(c.meta, "SetLeverage", "leverage must be positive")
	}
	value := strconv.Itoa(req.Leverage)
	err = c.sdk.SetLeverage(ctx, bybit.SetLeverageRequest{Category: c.category, Symbol: native, BuyLeverage: value, SellLeverage: value})
	if err != nil {
		return exchange.Leverage{}, normErr(c.meta, "SetLeverage", err)
	}
	return exchange.Leverage{NativeSymbol: native, Effective: req.Leverage}, nil
}

func (c *bitgetBase) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := ctxErr(ctx, c.meta, "SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	native, _, err := c.symbols(req.Instrument, "SetLeverage")
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 {
		return exchange.Leverage{}, invalid(c.meta, "SetLeverage", "leverage must be positive")
	}
	err = c.sdk.SetLeverage(ctx, &bitget.SetLeverageRequest{Category: c.bitgetPrivateCategory(), Symbol: native, Leverage: strconv.Itoa(req.Leverage), Coin: c.marginCoin, MarginMode: "crossed"})
	if err != nil {
		return exchange.Leverage{}, normErr(c.meta, "SetLeverage", err)
	}
	return exchange.Leverage{NativeSymbol: native, Effective: req.Leverage}, nil
}

func (c *bybitBase) symbols(instrument exchange.Instrument, operation string) (string, exchange.Instrument, error) {
	if err := validateBybitBitgetInstrument(c.meta, operation, instrument); err != nil {
		return "", exchange.Instrument{}, err
	}
	native := instrument.Identity.NativeSymbol
	if c.meta.product == exchange.ProductPerp {
		if instrument.Identity.SettleAsset != c.settleCoin {
			return "", exchange.Instrument{}, invalid(c.meta, operation, "instrument quote must match "+c.settleCoin+" settlement")
		}
	}
	return native, instrument, nil
}

func (c *bitgetBase) symbols(instrument exchange.Instrument, operation string) (string, exchange.Instrument, error) {
	if err := validateBybitBitgetInstrument(c.meta, operation, instrument); err != nil {
		return "", exchange.Instrument{}, err
	}
	native := instrument.Identity.NativeSymbol
	if c.meta.product == exchange.ProductPerp {
		if instrument.Identity.SettleAsset != c.marginCoin {
			return "", exchange.Instrument{}, invalid(c.meta, operation, "instrument quote must match "+c.marginCoin+" settlement")
		}
	}
	return native, instrument, nil
}

func (c *bybitBase) optionalNativeSymbol(instrument *exchange.Instrument, operation string) (string, error) {
	if instrument == nil {
		return "", nil
	}
	native, _, err := c.symbols(*instrument, operation)
	return native, err
}

func (c *bitgetBase) optionalNativeSymbol(instrument *exchange.Instrument, operation string) (string, error) {
	if instrument == nil {
		return "", nil
	}
	native, _, err := c.symbols(*instrument, operation)
	return native, err
}

func validateBybitBitgetInstrument(meta clientMeta, operation string, instrument exchange.Instrument) error {
	if err := instrument.Validate(); err != nil {
		return withExchangeOperation(err, operation)
	}
	if instrument.Identity.Venue != meta.venue {
		return invalid(meta, operation, "instrument venue does not match client venue")
	}
	if instrument.Identity.Product != meta.product {
		return invalid(meta, operation, "instrument product does not match client product")
	}
	if instrument.Identity.NativeSymbol == "" {
		return invalid(meta, operation, "instrument native symbol is required")
	}
	if instrument.Identity.NativeSymbol != strings.TrimSpace(instrument.Identity.NativeSymbol) {
		return invalid(meta, operation, "instrument native symbol must be trimmed")
	}
	return nil
}

func quoteAsset(instrument string) string {
	canonical := strings.ToUpper(strings.TrimSpace(instrument))
	if index := strings.LastIndexAny(canonical, "-_"); index >= 0 && index+1 < len(canonical) {
		return canonical[index+1:]
	}
	for _, quote := range []string{"USDT", "USDC", "USD"} {
		if strings.HasSuffix(canonical, quote) && len(canonical) > len(quote) {
			return quote
		}
	}
	return ""
}

func validateFundingHistoryRequest(meta clientMeta, operation string, req exchange.FundingRateHistoryRequest, supportsWindow bool) error {
	if req.Limit < 0 {
		return invalid(meta, operation, "limit must be non-negative")
	}
	if req.Cursor != "" {
		return invalid(meta, operation, "cursor is not supported")
	}
	if !req.Start.IsZero() && !req.End.IsZero() && !req.End.After(req.Start) {
		return invalid(meta, operation, "end must be after start")
	}
	if !supportsWindow && (!req.Start.IsZero() || !req.End.IsZero()) {
		return invalid(meta, operation, "time window is not supported by Bitget funding history")
	}
	return nil
}

func (c *bitgetBase) bitgetCategoryAndSymbol(symbol string) (string, string) {
	return c.bitgetCategory(), symbol
}

func (c *bitgetBase) bitgetPrivateCategory() string {
	return c.bitgetCategory()
}

func (c *bitgetBase) bitgetPublicWSInstType() string {
	return strings.ToLower(c.bitgetCategory())
}

func (c *bitgetBase) bitgetPrivateWSInstType() string {
	return "UTA"
}

func (c *bitgetBase) bitgetCategory() string {
	if c.meta.product == exchange.ProductSpot {
		return "spot"
	}
	return c.productType
}

func convertBybitOrders(ctx context.Context, meta clientMeta, op string, rows []bybit.OrderRecord, nativeFilter string) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		side, err := parseSide(meta, op, row.Side)
		if err != nil {
			return nil, err
		}
		orderType, err := parseOrderType(meta, op, row.OrderType)
		if err != nil {
			return nil, err
		}
		status, err := parseStatus(meta, op, row.OrderStatus)
		if err != nil {
			return nil, err
		}
		quantity, err := parseRequiredDecimal(meta, op, "quantity", row.Qty)
		if err != nil {
			return nil, err
		}
		limitPrice, err := parseOptionalDecimal(meta, op, row.Price)
		if err != nil {
			return nil, err
		}
		filled, err := parseOptionalDecimal(meta, op, row.CumExecQty)
		if err != nil {
			return nil, err
		}
		averageFillPrice, err := optStrict(meta, op, row.AvgPrice)
		if err != nil {
			return nil, err
		}
		createdAt, err := parseRequiredMilli(meta, op, "created timestamp", row.CreatedTime)
		if err != nil {
			return nil, err
		}
		updatedAt, err := parseRequiredMilli(meta, op, "updated timestamp", row.UpdatedTime)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Order{NativeSymbol: row.Symbol, NativeID: nativeID, OrderID: row.OrderID, ClientOrderID: row.OrderLinkID, Side: side, Type: orderType, Quantity: quantity, LimitPrice: limitPrice, LimitPolicy: limitPolicy(row.TimeInForce), ReduceOnly: row.ReduceOnly, Filled: filled, AverageFillPrice: averageFillPrice, Status: status, CreatedAt: createdAt, UpdatedAt: updatedAt})
	}
	return out, nil
}

func convertBitgetOrders(ctx context.Context, meta clientMeta, op string, rows []bitget.OrderRecord, nativeFilter string) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		side, err := parseSide(meta, op, row.Side)
		if err != nil {
			return nil, err
		}
		orderType, err := parseOrderType(meta, op, row.OrderType)
		if err != nil {
			return nil, err
		}
		status, err := parseStatus(meta, op, row.OrderStatus)
		if err != nil {
			return nil, err
		}
		quantity, err := firstPositiveDecimalStrict(meta, op, row.Qty, row.Amount)
		if err != nil {
			return nil, err
		}
		limitPrice, err := parseOptionalDecimal(meta, op, row.Price)
		if err != nil {
			return nil, err
		}
		filled, err := firstPositiveDecimalStrict(meta, op, row.FilledQty, row.CumExecQty, row.BaseVolume)
		if err != nil {
			return nil, err
		}
		averageFillPrice, err := optStrict(meta, op, row.AvgPrice)
		if err != nil {
			return nil, err
		}
		createdAt, err := parseRequiredMilli(meta, op, "created timestamp", firstNonEmpty(row.CreatedTime, row.CTime))
		if err != nil {
			return nil, err
		}
		updatedAt, err := parseRequiredMilli(meta, op, "updated timestamp", firstNonEmpty(row.UpdatedTime, row.UTime))
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Order{NativeSymbol: row.Symbol, NativeID: nativeID, OrderID: row.OrderID, ClientOrderID: row.ClientOID, Side: side, Type: orderType, Quantity: quantity, LimitPrice: limitPrice, LimitPolicy: limitPolicy(row.TimeInForce), ReduceOnly: strings.EqualFold(row.ReduceOnly, "yes") || strings.EqualFold(row.ReduceOnly, "true"), Filled: filled, AverageFillPrice: averageFillPrice, Status: status, CreatedAt: createdAt, UpdatedAt: updatedAt})
	}
	return out, nil
}

func convertBybitFills(ctx context.Context, meta clientMeta, op string, rows []bybit.ExecutionRecord, nativeFilter string) ([]exchange.Fill, error) {
	out := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		liq := exchange.LiquidityTaker
		if row.IsMaker {
			liq = exchange.LiquidityMaker
		}
		side, err := parseSide(meta, op, row.Side)
		if err != nil {
			return nil, err
		}
		price, err := parseRequiredDecimal(meta, op, "price", row.ExecPrice)
		if err != nil {
			return nil, err
		}
		quantity, err := parseRequiredDecimal(meta, op, "quantity", row.ExecQty)
		if err != nil {
			return nil, err
		}
		fee, err := parseRequiredDecimal(meta, op, "execution fee", row.ExecFee)
		if err != nil {
			return nil, err
		}
		executedAt, err := parseRequiredMilli(meta, op, "execution timestamp", row.ExecTime)
		if err != nil {
			return nil, err
		}
		if row.FeeCoin != "" && row.FeeCurrency != "" && row.FeeCoin != row.FeeCurrency {
			return nil, malformed(meta, op, "execution fee currencies conflict")
		}
		if !fee.IsZero() && row.FeeCoin == "" && row.FeeCurrency == "" {
			return nil, malformed(meta, op, "execution fee currency is missing")
		}
		switch strings.TrimSpace(string(row.ExtraFees)) {
		case "", `""`, "null", "[]":
		default:
			return nil, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: "regional additional execution fees are not supported"})
		}
		out = append(out, exchange.Fill{NativeSymbol: row.Symbol, NativeID: nativeID, OrderID: row.OrderID, ClientOrderID: row.OrderLinkID, FillID: row.ExecID, Side: side, Price: price, Quantity: quantity, Fee: fee, FeeAsset: firstNonEmpty(row.FeeCoin, row.FeeCurrency), Liquidity: liq, Time: executedAt})
	}
	return out, nil
}

func convertBitgetFills(ctx context.Context, meta clientMeta, op string, rows []bitget.FillRecord, nativeFilter string) ([]exchange.Fill, error) {
	out := make([]exchange.Fill, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		if row.FeeDetail == nil {
			return nil, malformed(meta, op, "execution fee details are missing")
		}
		feeAsset, fee := "", decimal.Zero
		for _, charge := range row.FeeDetail {
			if charge.FeeCoin == "" {
				return nil, malformed(meta, op, "execution fee currency is missing")
			}
			if feeAsset != "" && charge.FeeCoin != feeAsset {
				return nil, exchange.NewError(exchange.KindUnsupported, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: "multiple fee currencies on one fill are not supported"})
			}
			feeAsset = charge.FeeCoin
			parsedFee, err := parseRequiredDecimal(meta, op, "execution fee", charge.Fee)
			if err != nil {
				return nil, err
			}
			fee = fee.Add(parsedFee)
		}

		side, err := parseSide(meta, op, row.Side)
		if err != nil {
			return nil, err
		}
		price, err := parseRequiredDecimal(meta, op, "price", row.ExecPrice)
		if err != nil {
			return nil, err
		}
		quantity, err := parseRequiredDecimal(meta, op, "quantity", row.ExecQty)
		if err != nil {
			return nil, err
		}
		executedAt, err := parseRequiredMilli(meta, op, "execution timestamp", firstNonEmpty(row.ExecTime, row.CreatedTime))
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Fill{NativeSymbol: row.Symbol, NativeID: nativeID, OrderID: row.OrderID, ClientOrderID: row.ClientOID, FillID: firstNonEmpty(row.ExecID, row.ExecLinkID), Side: side, Price: price, Quantity: quantity, Fee: fee, FeeAsset: feeAsset, Time: executedAt})
	}
	return out, nil
}

func bybitBalances(meta clientMeta, op string, resp *bybit.WalletBalanceResult) ([]exchange.Balance, error) {
	if resp == nil {
		return nil, nil
	}
	var out []exchange.Balance
	for _, account := range resp.List {
		for _, coin := range account.Coin {
			total, err := firstPositiveDecimalStrict(meta, op, coin.Equity, coin.WalletBalance)
			if err != nil {
				return nil, err
			}
			locked, err := parseOptionalDecimal(meta, op, firstNonEmpty(coin.Locked, coin.BorrowAmount))
			if err != nil {
				return nil, err
			}
			out = append(out, exchange.Balance{Asset: coin.Coin, Available: total.Sub(locked), Locked: locked, Total: total})
		}
	}
	return out, nil
}

func bitgetBalances(meta clientMeta, op string, rows []bitget.AccountAsset) ([]exchange.Balance, error) {
	out := make([]exchange.Balance, 0, len(rows))
	for _, row := range rows {
		total, err := firstPositiveDecimalStrict(meta, op, row.Equity, row.USDTValue, row.USDValue)
		if err != nil {
			return nil, err
		}
		locked, err := firstPositiveDecimalStrict(meta, op, row.Frozen, row.Locked)
		if err != nil {
			return nil, err
		}
		available, err := parseOptionalDecimal(meta, op, row.Available)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Balance{Asset: row.Coin, Available: available, Locked: locked, Total: total})
	}
	return out, nil
}

func convertBybitPositions(ctx context.Context, meta clientMeta, op string, rows []bybit.PositionRecord, nativeFilter string) ([]exchange.Position, error) {
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		quantity, err := parseOptionalDecimal(meta, op, row.Size)
		if err != nil {
			return nil, err
		}
		if !quantity.IsPositive() {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		side, err := parseSide(meta, op, row.Side)
		if err != nil {
			return nil, err
		}
		entryPrice, err := parseOptionalDecimal(meta, op, row.AvgPrice)
		if err != nil {
			return nil, err
		}
		unrealizedPnL, err := parseOptionalDecimal(meta, op, row.UnrealisedPnl)
		if err != nil {
			return nil, err
		}
		liquidationPrice, err := optStrict(meta, op, row.LiqPrice)
		if err != nil {
			return nil, err
		}
		leverage, err := optStrict(meta, op, row.Leverage)
		if err != nil {
			return nil, err
		}
		slot, err := bybitPositionSlot(row.PositionIdx)
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.Position{NativeSymbol: row.Symbol, NativeID: nativeID, PositionSlot: slot, Side: side, Quantity: quantity, EntryPrice: entryPrice, UnrealizedPnL: unrealizedPnL, LiquidationPrice: liquidationPrice, Leverage: leverage})
	}
	return out, nil
}

func convertBitgetPositions(ctx context.Context, meta clientMeta, op string, rows []bitget.PositionRecord, nativeFilter string) ([]exchange.Position, error) {
	out := make([]exchange.Position, 0, len(rows))
	for _, row := range rows {
		if nativeFilter != "" && row.Symbol != nativeFilter {
			continue
		}
		quantity, err := firstPositiveDecimalStrict(meta, op, row.Qty, row.Total, row.Size)
		if err != nil {
			return nil, err
		}
		if !quantity.IsPositive() {
			continue
		}
		nativeID, err := resolveEmittedNativeID(ctx, meta, op, row.Symbol)
		if err != nil {
			return nil, err
		}
		side, err := parseSide(meta, op, firstNonEmpty(row.PosSide, row.HoldSide))
		if err != nil {
			return nil, err
		}
		entryPrice, err := firstPositiveDecimalStrict(meta, op, row.AverageOpenPrice, row.OpenPriceAvg, row.AvgPrice)
		if err != nil {
			return nil, err
		}
		markPrice, err := parseOptionalDecimal(meta, op, row.MarkPrice)
		if err != nil {
			return nil, err
		}
		unrealizedPnL, err := parseOptionalDecimal(meta, op, firstNonEmpty(row.UnrealisedPnl, row.UnrealizedPL))
		if err != nil {
			return nil, err
		}
		liquidationPrice, err := optStrict(meta, op, firstNonEmpty(row.LiquidationPrice, row.LiqPrice))
		if err != nil {
			return nil, err
		}
		leverage, err := optStrict(meta, op, row.Leverage)
		if err != nil {
			return nil, err
		}
		slot := bitgetPositionSlot(row.HoldMode, firstNonEmpty(row.PosSide, row.HoldSide), side)
		out = append(out, exchange.Position{NativeSymbol: row.Symbol, NativeID: nativeID, PositionSlot: slot, Side: side, Quantity: quantity, EntryPrice: entryPrice, MarkPrice: markPrice, UnrealizedPnL: unrealizedPnL, LiquidationPrice: liquidationPrice, Leverage: leverage})
	}
	return out, nil
}

func bybitPositionSlot(index int) (exchange.PositionSlot, error) {
	switch index {
	case 0:
		return exchange.PositionSlotNet, nil
	case 1:
		return exchange.PositionSlotLong, nil
	case 2:
		return exchange.PositionSlotShort, nil
	default:
		return "", fmt.Errorf("unknown Bybit position index")
	}
}

func bitgetPositionSlot(mode, nativeSide string, side exchange.Side) exchange.PositionSlot {
	mode = strings.ToLower(strings.TrimSpace(mode))
	nativeSide = strings.ToLower(strings.TrimSpace(nativeSide))
	if strings.Contains(mode, "double") || strings.Contains(mode, "hedge") || nativeSide == "long" || nativeSide == "short" {
		if side == exchange.SideSell {
			return exchange.PositionSlotShort
		}
		return exchange.PositionSlotLong
	}
	return exchange.PositionSlotNet
}

func canonicalSymbol(native, base, quote string) string {
	if base != "" && quote != "" {
		return strings.ToUpper(base + "-" + quote)
	}
	native = strings.ToUpper(strings.TrimSpace(native))
	for _, quote := range []string{"USDT", "USDC", "USD"} {
		if strings.HasSuffix(native, quote) && len(native) > len(quote) {
			return native[:len(native)-len(quote)] + "-" + quote
		}
	}
	return native
}

func ctxErr(ctx context.Context, meta clientMeta, op string) error {
	if ctx == nil {
		return invalid(meta, op, "context is required")
	}
	if err := ctx.Err(); err != nil {
		return websocketContextError(meta, op, err)
	}
	return nil
}

func normErr(meta clientMeta, op string, err error) (returnErr error) {
	defer func() { returnErr = normalizeHTTPError(returnErr, err, meta.venue, meta.product, op) }()
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return exchange.NewError(exchange.KindCanceled, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: "request context ended"})
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return exchange.NewError(exchange.KindDeadlineExceeded, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: "request context ended"})
	}
	var bybitErr *bybit.ResponseError
	if errors.As(err, &bybitErr) {
		return exchange.NewError(classifyCode(strconv.Itoa(bybitErr.Code)), exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, Code: strconv.Itoa(bybitErr.Code), SafeMessage: safeVenueFailure(meta.venue)})
	}
	var bitgetErr *bitget.ResponseError
	if errors.As(err, &bitgetErr) {
		return exchange.NewError(classifyCode(bitgetErr.Code), exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, Code: bitgetErr.Code, SafeMessage: safeVenueFailure(meta.venue)})
	}
	return exchange.NewError(exchange.KindTransport, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: "transport failure"})
}

func safeVenueFailure(venue exchange.Venue) string {
	if venue == "" {
		return "venue rejected request"
	}
	return string(venue) + " rejected request"
}

func classifyCode(code string) exchange.ErrorKind {
	switch code {
	case "401", "-2015", "33004", "10003", "10004", "10007",
		"40001", "40002", "40003", "40005", "40006", "40009", "40011", "40012":
		return exchange.KindAuthentication
	case "10005", "10009", "10010", "40014", "40018", "40301":
		return exchange.KindPermission
	case "10006", "429", "42900":
		return exchange.KindRateLimit
	case "10001", "40017":
		return exchange.KindInvalidRequest
	default:
		return exchange.KindVenueRejected
	}
}

func invalid(meta clientMeta, op, msg string) error {
	return exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: msg})
}

func malformed(meta clientMeta, op, msg string) error {
	return exchange.NewError(exchange.KindMalformedResponse, exchange.ErrorDetails{Venue: meta.venue, Product: meta.product, Operation: op, SafeMessage: msg})
}

func resolveEmittedNativeID(ctx context.Context, meta clientMeta, op, nativeSymbol string) (string, error) {
	if meta.catalog == nil || meta.catalogScope == (exchange.CatalogScope{}) {
		return "", meta.catalogMetadataResolution(op, "instrument catalog is not configured")
	}
	instrument, err := meta.catalog.Resolve(ctx, meta.catalogScope, exchange.InstrumentQuery{NativeSymbol: nativeSymbol})
	if err != nil {
		return "", withExchangeOperation(err, op)
	}
	return instrument.Identity.NativeID, nil
}

func isOrderActionMalformed(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "returned a partial response") ||
		strings.Contains(message, "returned mismatched order id") ||
		strings.Contains(message, "returned mismatched order link id") ||
		strings.Contains(message, "returned mismatched client order id")
}

func validateSuccessfulOrderAction(meta clientMeta, operation string, kind exchange.OrderOperation, nativeSymbol, expectedOrderID, expectedClientID, orderID, clientID string) error {
	if strings.TrimSpace(orderID) == "" {
		return malformed(meta, operation, "successful order acknowledgement is missing order id")
	}
	if kind == exchange.OrderOperationCancel && expectedOrderID != "" && orderID != expectedOrderID {
		return malformed(meta, operation, "successful cancel acknowledgement order id mismatch")
	}
	if kind == exchange.OrderOperationPlace && expectedClientID != "" && clientID != expectedClientID {
		return malformed(meta, operation, "successful place acknowledgement client order id mismatch")
	}
	if strings.TrimSpace(nativeSymbol) == "" {
		return malformed(meta, operation, "successful order acknowledgement is missing native symbol")
	}
	return nil
}

func commandAck(meta clientMeta, operation string, kind exchange.OrderOperation, instrument, orderID, clientID string, err error) (returnAck exchange.OrderAcknowledgement, returnErr error) {
	defer func() { normalizeHTTPCommand(&returnAck, &returnErr, err, meta.venue, meta.product, operation) }()
	ack := baseAck(meta, kind, instrument, orderID, clientID)
	ack.State = exchange.AckAmbiguous
	if bybit.IsDefinitiveCommandRejection(err) || bitget.IsDefinitiveCommandRejection(err) {
		ack.State = exchange.AckRejected
		ack.VenueMessage = fmt.Sprintf("%s rejected order command", meta.venue)
		var bybitErr *bybit.ResponseError
		if errors.As(err, &bybitErr) {
			ack.VenueCode = strconv.Itoa(bybitErr.Code)
		}
		var bitgetErr *bitget.ResponseError
		if errors.As(err, &bitgetErr) {
			ack.VenueCode = bitgetErr.Code
		}
		return ack, nil
	}
	return ack, normErr(meta, operation, err)
}

func baseAck(meta clientMeta, op exchange.OrderOperation, nativeSymbol, orderID, clientID string) exchange.OrderAcknowledgement {
	return exchange.OrderAcknowledgement{Venue: meta.venue, Product: meta.product, Operation: op, State: exchange.AckAcceptedPending, NativeSymbol: nativeSymbol, OrderID: orderID, ClientOrderID: clientID}
}

func priceString(req exchange.PlaceOrderRequest) string {
	if req.Type == exchange.OrderTypeLimit {
		return req.LimitPrice.String()
	}
	return ""
}

func bybitSide(value exchange.Side) string {
	if value == exchange.SideSell {
		return "Sell"
	}
	return "Buy"
}

func bybitOrderType(value exchange.OrderType) string {
	if value == exchange.OrderTypeMarket {
		return "Market"
	}
	return "Limit"
}

func tif(policy exchange.LimitPolicy) string {
	switch policy {
	case exchange.LimitPolicyIOC:
		return "IOC"
	case exchange.LimitPolicyPostOnly:
		return "PostOnly"
	default:
		return "GTC"
	}
}

func bitgetTIF(policy exchange.LimitPolicy) string {
	switch policy {
	case exchange.LimitPolicyIOC:
		return "ioc"
	case exchange.LimitPolicyPostOnly:
		return "post_only"
	default:
		return "gtc"
	}
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func positiveDecimalOrderID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return err == nil && parsed > 0
}

func bybitNativeOrderID(value string) bool {
	if positiveDecimalOrderID(value) {
		return true
	}
	return validUUIDOrderID(value)
}

func side(value string) exchange.Side {
	switch strings.ToLower(value) {
	case "sell", "ask", "short":
		return exchange.SideSell
	default:
		return exchange.SideBuy
	}
}

func parseSide(meta clientMeta, op, value string) (exchange.Side, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "buy", "bid", "long":
		return exchange.SideBuy, nil
	case "sell", "ask", "short":
		return exchange.SideSell, nil
	default:
		return "", malformed(meta, op, "unknown side")
	}
}

func orderType(value string) exchange.OrderType {
	if strings.EqualFold(value, "market") {
		return exchange.OrderTypeMarket
	}
	return exchange.OrderTypeLimit
}

func parseOrderType(meta clientMeta, op, value string) (exchange.OrderType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "market":
		return exchange.OrderTypeMarket, nil
	case "limit":
		return exchange.OrderTypeLimit, nil
	default:
		return "", malformed(meta, op, "unknown order type")
	}
}

func parseStatus(meta clientMeta, op, value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch normalized {
	case "new", "partiallyfilled", "partially_filled", "filled", "cancelled", "canceled",
		"rejected", "live", "init", "fail", "success", "not_trigger", "triggered", "deactivated":
		return value, nil
	default:
		return "", malformed(meta, op, "unknown order status")
	}
}

func limitPolicy(value string) exchange.LimitPolicy {
	switch strings.ToLower(value) {
	case "ioc":
		return exchange.LimitPolicyIOC
	case "postonly", "post_only", "post-only":
		return exchange.LimitPolicyPostOnly
	default:
		return exchange.LimitPolicyResting
	}
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func milliString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixMilli(), 10)
}

func limitString(limit int) string {
	if limit <= 0 {
		return ""
	}
	return strconv.Itoa(limit)
}

func parseRequiredMilli(meta clientMeta, operation, field, value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, malformed(meta, operation, field+" is required")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return time.Time{}, malformed(meta, operation, "invalid "+field)
	}
	return time.UnixMilli(parsed).UTC(), nil
}

func parseRequiredMilliInt64(meta clientMeta, operation, field string, value int64) (time.Time, error) {
	if value <= 0 {
		return time.Time{}, malformed(meta, operation, "invalid "+field)
	}
	return time.UnixMilli(value).UTC(), nil
}

func parseRequiredDecimal(meta clientMeta, op, field, value string) (decimal.Decimal, error) {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero, malformed(meta, op, field+" is required")
	}
	return parseOptionalDecimal(meta, op, value)
}

func parseOptionalDecimal(meta clientMeta, op, value string) (decimal.Decimal, error) {
	if strings.TrimSpace(value) == "" {
		return decimal.Zero, nil
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, malformed(meta, op, "invalid decimal value")
	}
	return parsed, nil
}

func firstPositiveDecimalStrict(meta clientMeta, op string, values ...string) (decimal.Decimal, error) {
	for _, value := range values {
		parsed, err := parseOptionalDecimal(meta, op, value)
		if err != nil {
			return decimal.Zero, err
		}
		if parsed.IsPositive() {
			return parsed, nil
		}
	}
	return decimal.Zero, nil
}

func optionalPositiveStrict(meta clientMeta, op string, values ...string) (exchange.OptionalDecimal, error) {
	for _, value := range values {
		parsed, err := parseOptionalDecimal(meta, op, value)
		if err != nil {
			return exchange.OptionalDecimal{}, err
		}
		if parsed.IsPositive() {
			return exchange.OptionalDecimal{Value: parsed, Valid: true}, nil
		}
	}
	return exchange.OptionalDecimal{}, nil
}

func optStrict(meta clientMeta, op, value string) (exchange.OptionalDecimal, error) {
	if strings.TrimSpace(value) == "" {
		return exchange.OptionalDecimal{}, nil
	}
	parsed, err := parseOptionalDecimal(meta, op, value)
	if err != nil {
		return exchange.OptionalDecimal{}, err
	}
	return exchange.OptionalDecimal{Value: parsed, Valid: true}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func precisionIncrement(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	places, err := strconv.Atoi(value)
	if err != nil || places < 0 {
		return ""
	}
	return decimal.New(1, int32(-places)).String()
}

func numberStringBook[T ~string](rows [][]T) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("book level has fewer than two fields")
		}
		price, err := decimal.NewFromString(string(row[0]))
		if err != nil {
			return nil, fmt.Errorf("invalid book price")
		}
		quantity, err := decimal.NewFromString(string(row[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid book quantity")
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: quantity})
	}
	return out, nil
}

func bboLevels(meta clientMeta, op, bidPrice, bidQty, askPrice, askQty string) (exchange.BookLevel, exchange.BookLevel, error) {
	parsedBidPrice, err := parseRequiredDecimal(meta, op, "bid price", bidPrice)
	if err != nil {
		return exchange.BookLevel{}, exchange.BookLevel{}, err
	}
	parsedBidQty, err := parseRequiredDecimal(meta, op, "bid quantity", bidQty)
	if err != nil {
		return exchange.BookLevel{}, exchange.BookLevel{}, err
	}
	parsedAskPrice, err := parseRequiredDecimal(meta, op, "ask price", askPrice)
	if err != nil {
		return exchange.BookLevel{}, exchange.BookLevel{}, err
	}
	parsedAskQty, err := parseRequiredDecimal(meta, op, "ask quantity", askQty)
	if err != nil {
		return exchange.BookLevel{}, exchange.BookLevel{}, err
	}
	return exchange.BookLevel{Price: parsedBidPrice, Quantity: parsedBidQty}, exchange.BookLevel{Price: parsedAskPrice, Quantity: parsedAskQty}, nil
}

func strictCandle(meta clientMeta, op, openTime, open, high, low, closeValue, volume string) (exchange.Candle, error) {
	parsedOpenTime, err := parseRequiredMilli(meta, op, "open timestamp", openTime)
	if err != nil {
		return exchange.Candle{}, err
	}
	parsedOpen, err := parseRequiredDecimal(meta, op, "open", open)
	if err != nil {
		return exchange.Candle{}, err
	}
	parsedHigh, err := parseRequiredDecimal(meta, op, "high", high)
	if err != nil {
		return exchange.Candle{}, err
	}
	parsedLow, err := parseRequiredDecimal(meta, op, "low", low)
	if err != nil {
		return exchange.Candle{}, err
	}
	parsedClose, err := parseRequiredDecimal(meta, op, "close", closeValue)
	if err != nil {
		return exchange.Candle{}, err
	}
	parsedVolume, err := parseRequiredDecimal(meta, op, "volume", volume)
	if err != nil {
		return exchange.Candle{}, err
	}
	return exchange.Candle{OpenTime: parsedOpenTime, Open: parsedOpen, High: parsedHigh, Low: parsedLow, Close: parsedClose, Volume: parsedVolume, Complete: true}, nil
}

func strictReference(meta clientMeta, op string, instrument exchange.Instrument, markPrice, fundingRate, effectiveAt, nextAt string, observedAt time.Time) (perpReferenceEvent, error) {
	event := perpReferenceEvent{}
	if (markPrice != "" || fundingRate != "") && observedAt.IsZero() {
		return perpReferenceEvent{}, malformed(meta, op, "observed timestamp is required")
	}
	if markPrice != "" {
		price, err := parseRequiredDecimal(meta, op, "mark price", markPrice)
		if err != nil {
			return perpReferenceEvent{}, err
		}
		event.MarkValid = true
		event.MarkPrice = exchange.MarkPriceEvent{Instrument: instrument, Price: price, Time: observedAt}
	}
	if fundingRate != "" {
		rate, err := parseRequiredDecimal(meta, op, "funding rate", fundingRate)
		if err != nil {
			return perpReferenceEvent{}, err
		}
		effectiveTime := observedAt
		if effectiveAt != "" {
			effectiveTime, err = parseRequiredMilli(meta, op, "funding effective timestamp", effectiveAt)
			if err != nil {
				return perpReferenceEvent{}, err
			}
		}
		nextTime := time.Time{}
		if nextAt != "" {
			nextTime, err = parseRequiredMilli(meta, op, "next funding timestamp", nextAt)
			if err != nil {
				return perpReferenceEvent{}, err
			}
		}
		event.FundingValid = true
		event.FundingRate = exchange.FundingRateEvent{Instrument: instrument, Rate: rate, EffectiveAt: effectiveTime, NextAt: nextTime}
	}
	return event, nil
}

func convertCandles[T any](meta clientMeta, op string, rows []T) ([]exchange.Candle, error) {
	out := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		values, ok := candleValues(row)
		if !ok {
			return nil, fmt.Errorf("unknown candle row type %T", row)
		}
		candle, err := strictCandle(meta, op, values[0], values[1], values[2], values[3], values[4], values[5])
		if err != nil {
			return nil, err
		}
		out = append(out, candle)
	}
	return out, nil
}

func candleValues(row any) ([7]string, bool) {
	var values [7]string
	switch r := row.(type) {
	case bybit.Candle:
		for i := range r {
			values[i] = string(r[i])
		}
		return values, true
	case bitget.Candle:
		for i := range r {
			values[i] = string(r[i])
		}
		return values, true
	default:
		return values, false
	}
}

func validateWindow(meta clientMeta, op, interval string, start, end time.Time, limit int, cursor string) error {
	if strings.TrimSpace(interval) == "" {
		return invalid(meta, op, "interval is required")
	}
	if cursor != "" {
		return invalid(meta, op, "cursor is not supported")
	}
	if limit < 0 {
		return invalid(meta, op, "limit must be non-negative")
	}
	if !start.IsZero() && !end.IsZero() && !end.After(start) {
		return invalid(meta, op, "end must be after start")
	}
	return nil
}

func bybitInterval(meta clientMeta, op, interval string) (string, error) {
	switch interval {
	case "1m":
		return "1", nil
	case "5m":
		return "5", nil
	case "15m":
		return "15", nil
	case "30m":
		return "30", nil
	case "1h":
		return "60", nil
	case "4h":
		return "240", nil
	case "12h":
		return "720", nil
	case "1d":
		return "D", nil
	default:
		return "", invalid(meta, op, "unsupported interval")
	}
}

const bitgetCandlePageLimit = 100

func bitgetInterval(meta clientMeta, op, interval string) (string, error) {
	switch interval {
	case "1m", "5m", "15m", "30m":
		return interval, nil
	case "1h":
		return "1H", nil
	case "4h":
		return "4H", nil
	case "12h":
		return "12H", nil
	case "1d":
		return "1D", nil
	default:
		return "", invalid(meta, op, "unsupported interval")
	}
}

type bybitWSBackend struct{ client *bybitBase }
type bitgetWSBackend struct{ client *bitgetBase }
type bybitPrivateWSBackend struct {
	client    *bybitBase
	orders    *bybitPrivateTopicHub[exchange.OrderEvent]
	fills     *bybitPrivateTopicHub[exchange.FillEvent]
	positions *bybitPrivateTopicHub[exchange.PositionEvent]
	balances  *bybitPrivateTopicHub[exchange.BalanceEvent]
}
type bitgetPrivateWSBackend struct {
	client    *bitgetBase
	ws        *bitget.PrivateWSClient
	orders    *bitgetPrivateTopicHub[exchange.OrderEvent]
	fills     *bitgetPrivateTopicHub[exchange.FillEvent]
	positions *bitgetPrivateTopicHub[exchange.PositionEvent]
	balances  *bitgetPrivateTopicHub[exchange.BalanceEvent]
}

func newBybitPrivateWSBackend(client *bybitBase) *bybitPrivateWSBackend {
	return &bybitPrivateWSBackend{
		client:    client,
		orders:    newBybitPrivateTopicHub[exchange.OrderEvent](client, "order"),
		fills:     newBybitPrivateTopicHub[exchange.FillEvent](client, "execution"),
		positions: newBybitPrivateTopicHub[exchange.PositionEvent](client, "position"),
		balances:  newBybitPrivateTopicHub[exchange.BalanceEvent](client, "wallet"),
	}
}

type bybitPrivateTopicHub[T any] struct {
	mu        sync.Mutex
	client    *bybitBase
	topic     string
	ws        *bybit.PrivateWSClient
	nextID    uint64
	callbacks map[string]map[uint64]streamCallbacks[T]
}

func newBybitPrivateTopicHub[T any](client *bybitBase, topic string) *bybitPrivateTopicHub[T] {
	return &bybitPrivateTopicHub[T]{
		client:    client,
		topic:     topic,
		callbacks: make(map[string]map[uint64]streamCallbacks[T]),
	}
}

func (hub *bybitPrivateTopicHub[T]) start(
	native string,
	callbacks streamCallbacks[T],
	handler func(*bybit.PrivateWSClient) error,
) (func() error, error) {
	hub.mu.Lock()
	if hub.callbacks == nil {
		hub.callbacks = make(map[string]map[uint64]streamCallbacks[T])
	}
	hub.nextID++
	id := hub.nextID
	if hub.callbacks[native] == nil {
		hub.callbacks[native] = make(map[uint64]streamCallbacks[T])
	}
	hub.callbacks[native][id] = callbacks
	if hub.ws != nil {
		hub.mu.Unlock()
		return func() error { return hub.remove(native, id) }, nil
	}
	ws := bybit.NewPrivateWSClientWithProfile(hub.client.profile).WithCredentials(hub.client.apiKey, hub.client.secretKey)
	hub.ws = ws
	hub.mu.Unlock()

	if err := handler(ws); err != nil {
		_ = ws.Close()
		hub.mu.Lock()
		if hub.ws == ws {
			hub.ws = nil
		}
		if routes := hub.callbacks[native]; routes != nil {
			delete(routes, id)
			if len(routes) == 0 {
				delete(hub.callbacks, native)
			}
		}
		hub.mu.Unlock()
		return nil, err
	}
	return func() error { return hub.remove(native, id) }, nil
}

func (hub *bybitPrivateTopicHub[T]) remove(native string, id uint64) error {
	hub.mu.Lock()
	if routes := hub.callbacks[native]; routes != nil {
		delete(routes, id)
		if len(routes) == 0 {
			delete(hub.callbacks, native)
		}
	}
	empty := len(hub.callbacks) == 0
	ws := hub.ws
	if empty {
		hub.ws = nil
	}
	hub.mu.Unlock()
	if empty && ws != nil {
		return ws.Close()
	}
	return nil
}

func (hub *bybitPrivateTopicHub[T]) close() error {
	hub.mu.Lock()
	ws := hub.ws
	hub.ws = nil
	hub.callbacks = make(map[string]map[uint64]streamCallbacks[T])
	hub.mu.Unlock()
	if ws != nil {
		return ws.Close()
	}
	return nil
}

func (hub *bybitPrivateTopicHub[T]) emit(native string, event T) {
	for _, callbacks := range hub.snapshot(native) {
		callbacks.Event(event)
	}
}

func (hub *bybitPrivateTopicHub[T]) emitError(native string, err error) {
	for _, callbacks := range hub.snapshot(native) {
		callbacks.Error(err)
	}
}

func (hub *bybitPrivateTopicHub[T]) emitAllError(err error) {
	for _, callbacks := range hub.snapshotAll() {
		callbacks.Error(err)
	}
}

func (hub *bybitPrivateTopicHub[T]) snapshot(native string) []streamCallbacks[T] {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	routes := hub.callbacks[native]
	accountRoutes := hub.callbacks[accountWideNative]
	out := make([]streamCallbacks[T], 0, len(routes)+len(accountRoutes))
	for _, callbacks := range routes {
		out = append(out, callbacks)
	}
	if native != accountWideNative {
		for _, callbacks := range accountRoutes {
			out = append(out, callbacks)
		}
	}
	return out
}

func (hub *bybitPrivateTopicHub[T]) snapshotAll() []streamCallbacks[T] {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var out []streamCallbacks[T]
	for _, routes := range hub.callbacks {
		for _, callbacks := range routes {
			out = append(out, callbacks)
		}
	}
	return out
}

func newBitgetPrivateWSBackend(client *bitgetBase) *bitgetPrivateWSBackend {
	ws := bitget.NewPrivateWSClientWithProfile(client.profile).
		WithCredentials(client.apiKey, client.secretKey, client.passphrase)
	backend := &bitgetPrivateWSBackend{
		client: client,
		ws:     ws,
	}
	instType := client.bitgetPrivateWSInstType()
	backend.orders = newBitgetPrivateTopicHub[exchange.OrderEvent](ws, bitget.WSArg{InstType: instType, Topic: "order"})
	backend.fills = newBitgetPrivateTopicHub[exchange.FillEvent](ws, bitget.WSArg{InstType: instType, Topic: "fill"})
	backend.positions = newBitgetPrivateTopicHub[exchange.PositionEvent](ws, bitget.WSArg{InstType: instType, Topic: "position"})
	backend.balances = newBitgetPrivateTopicHub[exchange.BalanceEvent](ws, bitget.WSArg{InstType: instType, Topic: "account"})
	return backend
}

func (b *bybitPrivateWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, b.client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(b.client.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, _, err := b.client.symbols(req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ws, err := bybit.NewTradeWSClientWithProfile(b.client.profile)
	if err != nil {
		return commandAck(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	ws.WithCredentials(b.client.apiKey, b.client.secretKey)
	defer ws.Close()
	resp, err := ws.PlaceOrderWithResponse(ctx, bybit.PlaceOrderRequest{Category: b.client.category, Symbol: native, Side: bybitSide(req.Side), OrderType: bybitOrderType(req.Type), Qty: req.Quantity.String(), Price: priceString(req), TimeInForce: tif(req.LimitPolicy), ReduceOnly: req.ReduceOnly, OrderLinkID: req.ClientOrderID})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(b.client.meta, "PlaceOrder", err.Error())
		}
		return commandAck(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	if err := validateSuccessfulOrderAction(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, resp.OrderID, resp.OrderLinkID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(b.client.meta, exchange.OrderOperationPlace, native, resp.OrderID, resp.OrderLinkID)
	ack.OrderType = req.Type
	return ack, ack.Validate()
}
func (b *bybitPrivateWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, b.client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, _, err := b.client.symbols(req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, invalid(b.client.meta, "CancelOrder", "order id is required")
	}
	if !bybitNativeOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, invalid(b.client.meta, "CancelOrder", "order id must be a positive decimal int64 or UUID")
	}
	ws, err := bybit.NewTradeWSClientWithProfile(b.client.profile)
	if err != nil {
		return commandAck(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	ws.WithCredentials(b.client.apiKey, b.client.secretKey)
	defer ws.Close()
	resp, err := ws.CancelOrderWithResponse(ctx, bybit.CancelOrderRequest{Category: b.client.category, Symbol: native, OrderID: req.OrderID})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(b.client.meta, "CancelOrder", err.Error())
		}
		return commandAck(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	if err := validateSuccessfulOrderAction(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", resp.OrderID, resp.OrderLinkID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(b.client.meta, exchange.OrderOperationCancel, native, resp.OrderID, resp.OrderLinkID)
	ack.State = exchange.AckCanceled
	return ack, ack.Validate()
}
func (b *bitgetPrivateWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, b.client.meta, "PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(b.client.meta.product); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, _, err := b.client.symbols(req.Instrument, "PlaceOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	quantity, err := b.client.bitgetOrderQuantity(ctx, native, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	positionSide, err := b.client.bitgetPositionSide(ctx, req)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ws := b.ws
	if err := ws.Connect(ctx); err != nil {
		return commandAck(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	resp, err := ws.PlaceUTAOrderWSContext(ctx, &bitget.PlaceOrderRequest{Category: b.client.bitgetPrivateCategory(), Symbol: native, Side: strings.ToLower(string(req.Side)), OrderType: strings.ToLower(string(req.Type)), Qty: quantity, Price: priceString(req), TimeInForce: bitgetTIF(req.LimitPolicy), MarginCoin: b.client.marginCoin, MarginMode: "crossed", ClientOID: req.ClientOrderID, ReduceOnly: bitgetReduceOnly(req, positionSide), PosSide: positionSide})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(b.client.meta, "PlaceOrder", err.Error())
		}
		return commandAck(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, err)
	}
	if err := validateSuccessfulOrderAction(b.client.meta, "PlaceOrder", exchange.OrderOperationPlace, native, "", req.ClientOrderID, resp.OrderID, resp.ClientOID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(b.client.meta, exchange.OrderOperationPlace, native, resp.OrderID, resp.ClientOID)
	ack.OrderType = req.Type
	return ack, ack.Validate()
}
func (b *bitgetPrivateWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := ctxErr(ctx, b.client.meta, "CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	native, _, err := b.client.symbols(req.Instrument, "CancelOrder")
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return exchange.OrderAcknowledgement{}, invalid(b.client.meta, "CancelOrder", "order id is required")
	}
	if !positiveDecimalOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, invalid(b.client.meta, "CancelOrder", "order id must be a positive decimal int64")
	}
	ws := b.ws
	if err := ws.Connect(ctx); err != nil {
		return commandAck(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	resp, err := ws.CancelUTAOrderWSContext(ctx, &bitget.CancelOrderRequest{Category: b.client.bitgetPrivateCategory(), Symbol: native, OrderID: req.OrderID})
	if err != nil {
		if isOrderActionMalformed(err) {
			return exchange.OrderAcknowledgement{}, malformed(b.client.meta, "CancelOrder", err.Error())
		}
		return commandAck(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", err)
	}
	if err := validateSuccessfulOrderAction(b.client.meta, "CancelOrder", exchange.OrderOperationCancel, native, req.OrderID, "", resp.OrderID, resp.ClientOID); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	ack := baseAck(b.client.meta, exchange.OrderOperationCancel, native, resp.OrderID, resp.ClientOID)
	ack.State = exchange.AckCanceled
	return ack, ack.Validate()
}

func (b *bybitPrivateWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchOrders")
	if err != nil {
		return nil, err
	}
	return b.startOrders(ctx, native, callbacks)
}

func (b *bybitPrivateWSBackend) startOrders(ctx context.Context, native string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	stop, err := b.orders.start(native, callbacks, func(ws *bybit.PrivateWSClient) error {
		return ws.Subscribe(ctx, "order", func(payload json.RawMessage) {
			msg, err := bybit.DecodeOrderMessage(payload)
			if err != nil {
				b.orders.emitAllError(malformed(b.client.meta, "WatchOrders", err.Error()))
				return
			}
			for _, row := range msg.Data {
				orders, err := convertBybitOrders(ctx, b.client.meta, "WatchOrders", []bybit.OrderRecord{row}, row.Symbol)
				if err != nil {
					b.orders.emitError(row.Symbol, err)
					continue
				}
				for _, order := range orders {
					b.orders.emit(row.Symbol, exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
				}
			}
		})
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchOrders", err)
	}
	return stop, nil
}
func (b *bybitPrivateWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchFills")
	if err != nil {
		return nil, err
	}
	return b.startFills(ctx, native, callbacks)
}

func (b *bybitPrivateWSBackend) startFills(ctx context.Context, native string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	stop, err := b.fills.start(native, callbacks, func(ws *bybit.PrivateWSClient) error {
		return ws.Subscribe(ctx, "execution", func(payload json.RawMessage) {
			msg, err := bybit.DecodeExecutionMessage(payload)
			if err != nil {
				b.fills.emitAllError(malformed(b.client.meta, "WatchFills", err.Error()))
				return
			}
			for _, row := range msg.Data {
				fills, err := convertBybitFills(ctx, b.client.meta, "WatchFills", []bybit.ExecutionRecord{row}, row.Symbol)
				if err != nil {
					b.fills.emitError(row.Symbol, err)
					continue
				}
				for _, fill := range fills {
					b.fills.emit(row.Symbol, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
				}
			}
		})
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchFills", err)
	}
	return stop, nil
}
func (b *bybitPrivateWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	stop, err := b.balances.start("account", callbacks, func(ws *bybit.PrivateWSClient) error {
		return ws.Subscribe(ctx, "wallet", func(payload json.RawMessage) {
			msg, err := bybit.DecodeWalletMessage(payload)
			if err != nil {
				b.balances.emitAllError(malformed(b.client.meta, "WatchBalances", err.Error()))
				return
			}
			resp := &bybit.WalletBalanceResult{}
			for _, account := range msg.Data {
				resp.List = append(resp.List, bybit.WalletAccount{AccountType: account.AccountType, Coin: account.Coins})
			}
			balances, err := bybitBalances(b.client.meta, "WatchBalances", resp)
			if err != nil {
				b.balances.emitAllError(err)
				return
			}
			b.balances.emit("account", exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: time.Now().UTC()})
		})
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchBalances", err)
	}
	return stop, nil
}
func (b *bybitPrivateWSBackend) StartPositions(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchPositions")
	if err != nil {
		return nil, err
	}
	return b.startPositions(ctx, native, callbacks)
}

func (b *bybitPrivateWSBackend) startPositions(ctx context.Context, native string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	stop, err := b.positions.start(native, callbacks, func(ws *bybit.PrivateWSClient) error {
		return ws.Subscribe(ctx, "position", func(payload json.RawMessage) {
			msg, err := bybit.DecodePositionMessage(payload)
			if err != nil {
				b.positions.emitAllError(malformed(b.client.meta, "WatchPositions", err.Error()))
				return
			}
			rowsBySymbol := make(map[string][]bybit.PositionRecord)
			for _, row := range msg.Data {
				rowsBySymbol[row.Symbol] = append(rowsBySymbol[row.Symbol], row)
			}
			for symbol, rows := range rowsBySymbol {
				positions, err := convertBybitPositions(ctx, b.client.meta, "WatchPositions", rows, symbol)
				if err != nil {
					b.positions.emitError(symbol, err)
					continue
				}
				b.positions.emit(symbol, exchange.PositionEvent{Kind: exchange.EventDelta, Positions: positions, Time: time.Now().UTC()})
			}
		})
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchPositions", err)
	}
	return stop, nil
}

func (b *bybitPrivateWSBackend) StartAccountEvents(ctx context.Context, _ accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	orders, err := b.startOrders(ctx, accountWideNative, streamCallbacks[exchange.OrderEvent]{
		Event: func(event exchange.OrderEvent) {
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &event})
		},
		Error: callbacks.Error,
	})
	if err != nil {
		return nil, err
	}
	fills, err := b.startFills(ctx, accountWideNative, streamCallbacks[exchange.FillEvent]{
		Event: func(event exchange.FillEvent) {
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventFill, Fill: &event})
		},
		Error: callbacks.Error,
	})
	if err != nil {
		_ = orders()
		return nil, err
	}
	balances, err := b.StartBalances(ctx, streamCallbacks[exchange.BalanceEvent]{
		Event: func(event exchange.BalanceEvent) {
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &event})
		},
		Error: callbacks.Error,
	})
	if err != nil {
		_ = closeAccountStops(orders, fills)()
		return nil, err
	}
	stops := []func() error{orders, fills, balances}
	if b.client.meta.product == exchange.ProductPerp {
		positions, positionErr := b.startPositions(ctx, accountWideNative, streamCallbacks[exchange.PositionEvent]{
			Event: func(event exchange.PositionEvent) {
				callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventPosition, Position: &event})
			},
			Error: callbacks.Error,
		})
		if positionErr != nil {
			_ = closeAccountStops(stops...)()
			return nil, positionErr
		}
		stops = append(stops, positions)
	}
	return closeAccountStops(stops...), nil
}
func (b *bybitPrivateWSBackend) Close() error {
	var errs []error
	if b.orders != nil {
		errs = append(errs, b.orders.close())
	}
	if b.fills != nil {
		errs = append(errs, b.fills.close())
	}
	if b.positions != nil {
		errs = append(errs, b.positions.close())
	}
	if b.balances != nil {
		errs = append(errs, b.balances.close())
	}
	return errors.Join(errs...)
}

func (b *bitgetPrivateWSBackend) StartOrders(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchOrders")
	if err != nil {
		return nil, err
	}
	return b.startOrders(ctx, native, callbacks)
}

func (b *bitgetPrivateWSBackend) startOrders(ctx context.Context, native string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	stop, err := b.orders.start(ctx, native, callbacks, func(payload json.RawMessage) {
		msg, err := bitget.DecodeOrderMessage(payload)
		if err != nil {
			b.orders.emitAllError(malformed(b.client.meta, "WatchOrders", err.Error()))
			return
		}
		for _, row := range msg.Data {
			orders, err := convertBitgetOrders(context.Background(), b.client.meta, "WatchOrders", []bitget.OrderRecord{row}, row.Symbol)
			if err != nil {
				b.orders.emitError(row.Symbol, err)
				continue
			}
			for _, order := range orders {
				b.orders.emit(row.Symbol, exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
			}
		}
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchOrders", err)
	}
	return stop, nil
}
func (b *bitgetPrivateWSBackend) StartFills(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchFills")
	if err != nil {
		return nil, err
	}
	return b.startFills(ctx, native, callbacks)
}

func (b *bitgetPrivateWSBackend) startFills(ctx context.Context, native string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	stop, err := b.fills.start(ctx, native, callbacks, func(payload json.RawMessage) {
		msg, err := bitget.DecodeFillMessage(payload)
		if err != nil {
			b.fills.emitAllError(malformed(b.client.meta, "WatchFills", err.Error()))
			return
		}
		for _, row := range msg.Data {
			fills, err := convertBitgetFills(context.Background(), b.client.meta, "WatchFills", []bitget.FillRecord{row}, row.Symbol)
			if err != nil {
				b.fills.emitError(row.Symbol, err)
				continue
			}
			for _, fill := range fills {
				b.fills.emit(row.Symbol, exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill})
			}
		}
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchFills", err)
	}
	return stop, nil
}
func (b *bitgetPrivateWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	stop, err := b.balances.start(ctx, "account", callbacks, func(payload json.RawMessage) {
		msg, err := bitget.DecodeAccountMessage(payload)
		if err != nil {
			b.balances.emitAllError(malformed(b.client.meta, "WatchBalances", err.Error()))
			return
		}
		balances, err := bitgetBalances(b.client.meta, "WatchBalances", msg.Data)
		if err != nil {
			b.balances.emitAllError(err)
			return
		}
		b.balances.emit("account", exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: time.Now().UTC()})
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchBalances", err)
	}
	return stop, nil
}
func (b *bitgetPrivateWSBackend) StartPositions(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	native, _, err := b.client.symbols(instrument, "WatchPositions")
	if err != nil {
		return nil, err
	}
	return b.startPositions(ctx, native, callbacks)
}

func (b *bitgetPrivateWSBackend) startPositions(ctx context.Context, native string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	stop, err := b.positions.start(ctx, native, callbacks, func(payload json.RawMessage) {
		msg, err := bitget.DecodePositionMessage(payload)
		if err != nil {
			b.positions.emitAllError(malformed(b.client.meta, "WatchPositions", err.Error()))
			return
		}
		rowsBySymbol := make(map[string][]bitget.PositionRecord)
		for _, row := range msg.Data {
			rowsBySymbol[row.Symbol] = append(rowsBySymbol[row.Symbol], row)
		}
		for symbol, rows := range rowsBySymbol {
			positions, err := convertBitgetPositions(context.Background(), b.client.meta, "WatchPositions", rows, symbol)
			if err != nil {
				b.positions.emitError(symbol, err)
				continue
			}
			b.positions.emit(symbol, exchange.PositionEvent{Kind: exchange.EventDelta, Positions: positions, Time: time.Now().UTC()})
		}
	})
	if err != nil {
		return nil, normErr(b.client.meta, "WatchPositions", err)
	}
	return stop, nil
}

func (b *bitgetPrivateWSBackend) StartAccountEvents(ctx context.Context, _ accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	orders, err := b.startOrders(ctx, accountWideNative, streamCallbacks[exchange.OrderEvent]{Event: func(event exchange.OrderEvent) {
		callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &event})
	}, Error: callbacks.Error})
	if err != nil {
		return nil, err
	}
	fills, err := b.startFills(ctx, accountWideNative, streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) {
		callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventFill, Fill: &event})
	}, Error: callbacks.Error})
	if err != nil {
		_ = orders()
		return nil, err
	}
	balances, err := b.StartBalances(ctx, streamCallbacks[exchange.BalanceEvent]{Event: func(event exchange.BalanceEvent) {
		callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventBalance, Balance: &event})
	}, Error: callbacks.Error})
	if err != nil {
		_ = closeAccountStops(orders, fills)()
		return nil, err
	}
	stops := []func() error{orders, fills, balances}
	if b.client.meta.product == exchange.ProductPerp {
		positions, positionErr := b.startPositions(ctx, accountWideNative, streamCallbacks[exchange.PositionEvent]{Event: func(event exchange.PositionEvent) {
			callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventPosition, Position: &event})
		}, Error: callbacks.Error})
		if positionErr != nil {
			_ = closeAccountStops(stops...)()
			return nil, positionErr
		}
		stops = append(stops, positions)
	}
	return closeAccountStops(stops...), nil
}
func (b *bitgetPrivateWSBackend) Close() error {
	if b == nil || b.ws == nil {
		return nil
	}
	b.orders.reset()
	b.fills.reset()
	b.positions.reset()
	b.balances.reset()
	return b.ws.Close()
}

func (b *bybitWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchOrderBook")
	if err != nil {
		return nil, err
	}
	ws := bybit.NewPublicWSClientWithProfile(b.client.profile, b.client.category)
	topic := "orderbook.50." + native
	seenEvent := false
	if err := ws.Subscribe(ctx, topic, func(payload json.RawMessage) {
		msg, err := bybit.DecodeOrderBookMessage(payload)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		bids, err := numberStringBook(msg.Data.Bids)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		asks, err := numberStringBook(msg.Data.Asks)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		timestamp := msg.Data.CTS
		if timestamp <= 0 {
			timestamp = msg.TS
		}
		observedAt, err := parseRequiredMilliInt64(b.client.meta, "WatchOrderBook", "timestamp", timestamp)
		if err != nil {
			callbacks.Error(err)
			return
		}
		kind, err := bybitOrderBookEventKind(msg.Type)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		resync := kind == exchange.EventSnapshot && seenEvent
		seenEvent = true
		callbacks.Event(exchange.BookEvent{Kind: kind, Instrument: canonical, Sequence: strconv.FormatInt(msg.Data.UpdateID, 10), Resync: resync, Bids: bids, Asks: asks, Time: observedAt})
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchOrderBook", err)
	}
	return bybitPublicStop(ctx, ws, topic), nil
}

func bybitOrderBookEventKind(raw string) (exchange.EventKind, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "snapshot":
		return exchange.EventSnapshot, nil
	case "delta":
		return exchange.EventDelta, nil
	default:
		return "", fmt.Errorf("unsupported order book event type %q", raw)
	}
}

func (b *bybitWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchBBO")
	if err != nil {
		return nil, err
	}
	ws := bybit.NewPublicWSClientWithProfile(b.client.profile, b.client.category)
	topic := "orderbook.1." + native
	if err := ws.Subscribe(ctx, topic, func(payload json.RawMessage) {
		msg, err := bybit.DecodeOrderBookMessage(payload)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchBBO", err.Error()))
			return
		}
		if len(msg.Data.Bids) == 0 || len(msg.Data.Bids[0]) < 2 {
			callbacks.Error(malformed(b.client.meta, "WatchBBO", "bid level is required"))
			return
		}
		if len(msg.Data.Asks) == 0 || len(msg.Data.Asks[0]) < 2 {
			callbacks.Error(malformed(b.client.meta, "WatchBBO", "ask level is required"))
			return
		}
		bid, ask, err := bboLevels(b.client.meta, "WatchBBO", string(msg.Data.Bids[0][0]), string(msg.Data.Bids[0][1]), string(msg.Data.Asks[0][0]), string(msg.Data.Asks[0][1]))
		if err != nil {
			callbacks.Error(err)
			return
		}
		timestamp := msg.Data.CTS
		if timestamp <= 0 {
			timestamp = msg.TS
		}
		observedAt, err := parseRequiredMilliInt64(b.client.meta, "WatchBBO", "timestamp", timestamp)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(exchange.BBOEvent{Instrument: canonical, Bid: bid, Ask: ask, Time: observedAt})
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchBBO", err)
	}
	return bybitPublicStop(ctx, ws, topic), nil
}
func (b *bybitWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchPublicTrades")
	if err != nil {
		return nil, err
	}
	ws := bybit.NewPublicWSClientWithProfile(b.client.profile, b.client.category)
	topic := "publicTrade." + native
	if err := ws.Subscribe(ctx, topic, func(payload json.RawMessage) {
		var msg struct {
			Data []bybit.PublicTrade `json:"data"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchPublicTrades", err.Error()))
			return
		}
		for _, row := range msg.Data {
			side, err := parseSide(b.client.meta, "WatchPublicTrades", row.Side)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			price, err := parseRequiredDecimal(b.client.meta, "WatchPublicTrades", "price", row.Price)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			quantity, err := parseRequiredDecimal(b.client.meta, "WatchPublicTrades", "quantity", row.Size)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			executedAt, err := parseRequiredMilli(b.client.meta, "WatchPublicTrades", "timestamp", row.Time)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			callbacks.Event(exchange.PublicTradeEvent{Instrument: canonical, TradeID: row.ExecID, Side: side, Price: price, Quantity: quantity, Time: executedAt})
		}
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchPublicTrades", err)
	}
	return bybitPublicStop(ctx, ws, topic), nil
}
func (b *bybitWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchCandles")
	if err != nil {
		return nil, err
	}
	nativeInterval, err := bybitInterval(b.client.meta, "WatchCandles", interval)
	if err != nil {
		return nil, err
	}
	ws := bybit.NewPublicWSClientWithProfile(b.client.profile, b.client.category)
	topic := "kline." + nativeInterval + "." + native
	if err := ws.Subscribe(ctx, topic, func(payload json.RawMessage) {
		var msg struct {
			Data []struct {
				Start  int64  `json:"start"`
				Open   string `json:"open"`
				High   string `json:"high"`
				Low    string `json:"low"`
				Close  string `json:"close"`
				Volume string `json:"volume"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchCandles", err.Error()))
			return
		}
		for _, row := range msg.Data {
			candle, err := strictCandle(b.client.meta, "WatchCandles", strconv.FormatInt(row.Start, 10), row.Open, row.High, row.Low, row.Close, row.Volume)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			callbacks.Event(exchange.CandleEvent{Instrument: canonical, Interval: interval, Candle: candle})
		}
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchCandles", err)
	}
	return bybitPublicStop(ctx, ws, topic), nil
}
func (b *bybitWSBackend) StartReference(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchReference")
	if err != nil {
		return nil, err
	}
	ws := bybit.NewPublicWSClientWithProfile(b.client.profile, b.client.category)
	topic := "tickers." + native
	if err := ws.Subscribe(ctx, topic, func(payload json.RawMessage) {
		var msg struct {
			TS   int64        `json:"ts"`
			Data bybit.Ticker `json:"data"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchReference", err.Error()))
			return
		}
		observedAt, err := parseRequiredMilliInt64(b.client.meta, "WatchReference", "timestamp", msg.TS)
		if err != nil {
			callbacks.Error(err)
			return
		}
		ref, err := strictReference(b.client.meta, "WatchReference", canonical, msg.Data.MarkPrice, msg.Data.FundingRate, msg.Data.Time, msg.Data.NextFundingTime, observedAt)
		if err != nil {
			callbacks.Error(err)
			return
		}
		fact, fields := bybitTickerFact(canonical.Identity, msg.Data, observedAt)
		ref.Derivative = derivativeReferenceEvent(canonical, fact, fields, observedAt)
		ref.DerivativeValid = fields != 0
		callbacks.Event(ref)
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchReference", err)
	}
	return bybitPublicStop(ctx, ws, topic), nil
}
func (b *bybitWSBackend) Close() error { return nil }

func (b *bitgetWSBackend) StartOrderBook(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchOrderBook")
	if err != nil {
		return nil, err
	}
	ws := bitget.NewPublicWSClientWithProfile(b.client.profile)
	arg := bitget.WSArg{InstType: b.client.bitgetPublicWSInstType(), Topic: "books5", Symbol: native}
	first := true
	if err := ws.Subscribe(ctx, arg, func(payload json.RawMessage) {
		msg, err := bitget.DecodeOrderBookMessage(payload)
		if err != nil || len(msg.Data) == 0 {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", firstNonEmpty(fmt.Sprint(err), "empty orderbook message")))
			return
		}
		row := msg.Data[0]
		bids, err := numberStringBook(row.Bids)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		asks, err := numberStringBook(row.Asks)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		observedAt, err := parseRequiredMilli(b.client.meta, "WatchOrderBook", "timestamp", row.TS)
		if err != nil {
			callbacks.Error(err)
			return
		}
		kind, err := bitgetOrderBookEventKind(msg.Action)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchOrderBook", err.Error()))
			return
		}
		if !first {
			kind = exchange.EventFull
		}
		first = false
		callbacks.Event(exchange.BookEvent{Kind: kind, Instrument: canonical, Sequence: strconv.FormatInt(row.Seq, 10), Bids: bids, Asks: asks, Time: observedAt})
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchOrderBook", err)
	}
	return bitgetPublicStop(ctx, ws, arg), nil
}

// Bitget books5 is a bounded full-book channel. Unlike the incremental books
// channel, every data push replaces all five visible levels.
func bitgetOrderBookEventKind(raw string) (exchange.EventKind, error) {
	if strings.EqualFold(strings.TrimSpace(raw), "snapshot") {
		return exchange.EventSnapshot, nil
	}
	return "", fmt.Errorf("books5 requires a snapshot event, got %q", raw)
}

func (b *bitgetWSBackend) StartBBO(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchBBO")
	if err != nil {
		return nil, err
	}
	ws := bitget.NewPublicWSClientWithProfile(b.client.profile)
	arg := bitget.WSArg{InstType: b.client.bitgetPublicWSInstType(), Topic: "ticker", Symbol: native}
	if err := ws.Subscribe(ctx, arg, func(payload json.RawMessage) {
		var msg struct {
			Data []bitget.Ticker     `json:"data"`
			TS   bitget.NumberString `json:"ts"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Data) == 0 {
			callbacks.Error(malformed(b.client.meta, "WatchBBO", firstNonEmpty(fmt.Sprint(err), "empty ticker message")))
			return
		}
		row := msg.Data[0]
		bid, ask, err := bboLevels(b.client.meta, "WatchBBO", row.Bid1Price, row.Bid1Size, row.Ask1Price, row.Ask1Size)
		if err != nil {
			callbacks.Error(err)
			return
		}
		timestamp := firstNonEmpty(row.Timestamp, string(msg.TS))
		observedAt, err := parseRequiredMilli(b.client.meta, "WatchBBO", "timestamp", timestamp)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(exchange.BBOEvent{Instrument: canonical, Bid: bid, Ask: ask, Time: observedAt})
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchBBO", err)
	}
	return bitgetPublicStop(ctx, ws, arg), nil
}
func (b *bitgetWSBackend) StartPublicTrades(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchPublicTrades")
	if err != nil {
		return nil, err
	}
	ws := bitget.NewPublicWSClientWithProfile(b.client.profile)
	arg := bitget.WSArg{InstType: b.client.bitgetPublicWSInstType(), Topic: "publicTrade", Symbol: native}
	if err := ws.Subscribe(ctx, arg, func(payload json.RawMessage) {
		var msg struct {
			Data []bitget.PublicFill `json:"data"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchPublicTrades", err.Error()))
			return
		}
		for _, row := range msg.Data {
			side, err := parseSide(b.client.meta, "WatchPublicTrades", row.Side)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			price, err := parseRequiredDecimal(b.client.meta, "WatchPublicTrades", "price", row.Price)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			quantity, err := parseRequiredDecimal(b.client.meta, "WatchPublicTrades", "quantity", row.Size)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			executedAt, err := parseRequiredMilli(b.client.meta, "WatchPublicTrades", "timestamp", row.Timestamp)
			if err != nil {
				callbacks.Error(err)
				continue
			}
			callbacks.Event(exchange.PublicTradeEvent{Instrument: canonical, TradeID: firstNonEmpty(row.ExecID, row.ExecLinkID), Side: side, Price: price, Quantity: quantity, Time: executedAt})
		}
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchPublicTrades", err)
	}
	return bitgetPublicStop(ctx, ws, arg), nil
}
func (b *bitgetWSBackend) StartCandles(ctx context.Context, instrument exchange.Instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchCandles")
	if err != nil {
		return nil, err
	}
	wireInterval, err := bitgetInterval(b.client.meta, "WatchCandles", interval)
	if err != nil {
		return nil, err
	}
	ws := bitget.NewPublicWSClientWithProfile(b.client.profile)
	arg := bitget.WSArg{InstType: b.client.bitgetPublicWSInstType(), Topic: "kline", Symbol: native, Interval: wireInterval}
	if err := ws.Subscribe(ctx, arg, func(payload json.RawMessage) {
		var msg struct {
			Data []bitget.Candle `json:"data"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchCandles", err.Error()))
			return
		}
		rows, err := convertCandles(b.client.meta, "WatchCandles", msg.Data)
		if err != nil {
			callbacks.Error(malformed(b.client.meta, "WatchCandles", err.Error()))
			return
		}
		for _, row := range rows {
			callbacks.Event(exchange.CandleEvent{Instrument: canonical, Interval: interval, Candle: row})
		}
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchCandles", err)
	}
	return bitgetPublicStop(ctx, ws, arg), nil
}
func (b *bitgetWSBackend) StartReference(ctx context.Context, instrument exchange.Instrument, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	native, canonical, err := b.client.symbols(instrument, "WatchReference")
	if err != nil {
		return nil, err
	}
	ws := bitget.NewPublicWSClientWithProfile(b.client.profile)
	arg := bitget.WSArg{InstType: b.client.bitgetPublicWSInstType(), Topic: "ticker", Symbol: native}
	if err := ws.Subscribe(ctx, arg, func(payload json.RawMessage) {
		var msg struct {
			Data []bitget.Ticker     `json:"data"`
			TS   bitget.NumberString `json:"ts"`
		}
		if err := json.Unmarshal(payload, &msg); err != nil || len(msg.Data) == 0 {
			callbacks.Error(malformed(b.client.meta, "WatchReference", firstNonEmpty(fmt.Sprint(err), "empty ticker message")))
			return
		}
		row := msg.Data[0]
		timestamp := firstNonEmpty(row.Timestamp, string(msg.TS))
		observedAt, err := parseRequiredMilli(b.client.meta, "WatchReference", "timestamp", timestamp)
		if err != nil {
			callbacks.Error(err)
			return
		}
		ref, err := strictReference(b.client.meta, "WatchReference", canonical, row.MarkPrice, row.FundingRate, timestamp, row.NextFundingTime, observedAt)
		if err != nil {
			callbacks.Error(err)
			return
		}
		fact, fields := bitgetTickerFact(canonical.Identity, row, observedAt)
		ref.Derivative = derivativeReferenceEvent(canonical, fact, fields, observedAt)
		ref.DerivativeValid = fields != 0
		callbacks.Event(ref)
	}); err != nil {
		_ = ws.Close()
		return nil, normErr(b.client.meta, "WatchReference", err)
	}
	return bitgetPublicStop(ctx, ws, arg), nil
}
func (b *bitgetWSBackend) Close() error { return nil }

func derivativeReferenceEvent(instrument exchange.Instrument, fact nativeDerivativeFact, fields nativeDerivativeField, observedAt time.Time) exchange.DerivativeReferenceEvent {
	event := exchange.DerivativeReferenceEvent{Instrument: instrument}
	if fields.Has(nativeDerivativeFieldMarkPrice) {
		event.MarkPrice = fact.MarkPrice
		event.MarkObservedAt = firstObservedAt(fact.MarkObservedAt, firstObservedAt(fact.PriceObservedAt, observedAt))
	}
	if fields.Has(nativeDerivativeFieldIndexPrice) {
		event.IndexPrice = fact.IndexPrice
		event.IndexObservedAt = firstObservedAt(fact.IndexObservedAt, firstObservedAt(fact.PriceObservedAt, observedAt))
	}
	if fields.Has(nativeDerivativeFieldFunding) {
		event.FundingRate = fact.FundingRate
		event.FundingObservedAt = firstObservedAt(fact.FundingObservedAt, observedAt)
	}
	if fields.Has(nativeDerivativeFieldFundingInterval) {
		event.FundingIntervalHours = fact.FundingIntervalHours
		if event.FundingObservedAt.IsZero() {
			event.FundingObservedAt = observedAt.UTC()
		}
	}
	if fields.Has(nativeDerivativeFieldNextFundingTime) {
		event.NextFundingTime = fact.NextFundingTime
		if event.FundingObservedAt.IsZero() {
			event.FundingObservedAt = observedAt.UTC()
		}
	}
	if fields.Has(nativeDerivativeFieldOpenInterest) {
		event.OpenInterest = fact.OpenInterest
		event.OpenInterestObservedAt = firstObservedAt(fact.OpenInterestObservedAt, observedAt)
	}
	if fields.Has(nativeDerivativeFieldOpenInterestNotional) {
		event.OpenInterestNotional = fact.OpenInterestNotional
		event.OpenInterestAsset = fact.OpenInterestNotionalAsset
		if event.OpenInterestObservedAt.IsZero() {
			event.OpenInterestObservedAt = observedAt.UTC()
		}
	}
	return event
}

func bybitPublicStop(ctx context.Context, ws *bybit.PublicWSClient, topic string) func() error {
	var once sync.Once
	var stopErr error
	cleanupCtx := context.WithoutCancel(ctx)
	return func() error {
		once.Do(func() {
			unsubscribeErr := ws.Unsubscribe(cleanupCtx, topic)
			closeErr := ws.Close()
			stopErr = errors.Join(unsubscribeErr, closeErr)
		})
		return stopErr
	}
}

func bitgetPublicStop(ctx context.Context, ws *bitget.PublicWSClient, arg bitget.WSArg) func() error {
	var once sync.Once
	var stopErr error
	cleanupCtx := context.WithoutCancel(ctx)
	return func() error {
		once.Do(func() {
			unsubscribeErr := ws.Unsubscribe(cleanupCtx, arg)
			closeErr := ws.Close()
			stopErr = errors.Join(unsubscribeErr, closeErr)
		})
		return stopErr
	}
}

func noopStop() error { return nil }

func _jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
