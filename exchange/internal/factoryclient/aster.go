package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	asterperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	asterspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

type asterSpotClient struct {
	spotClient
	sdk      *asterspot.Client
	profile  astercommon.Profile
	security *astercommon.SecurityContext
	wsMu     sync.Mutex
	ws       exchange.SpotWebSocket
	closed   bool
}

type asterPerpClient struct {
	perpClient
	sdk      *asterperp.Client
	profile  astercommon.Profile
	security *astercommon.SecurityContext
	wsMu     sync.Mutex
	ws       exchange.PerpWebSocket
	closed   bool
}

func NewAsterSpot(userAddress, apiWalletPrivateKey, expectedSigner string, settings Settings) exchange.SpotClient {
	profile, security, err := newAsterProfileAndSecurity(astercommon.ProductSpot, userAddress, apiWalletPrivateKey, expectedSigner, settings)
	var sdk *asterspot.Client
	if err == nil {
		sdk, err = asterspot.NewClient(profile, security)
	}
	if err == nil && sdk != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return &asterSpotClient{
		spotClient: spotClient{meta: clientMeta{venue: exchange.VenueAster, product: exchange.ProductSpot}},
		sdk:        sdk,
		profile:    profile,
		security:   security,
	}
}

func NewAsterUSDTPerp(userAddress, apiWalletPrivateKey, expectedSigner string, settings Settings) exchange.PerpClient {
	profile, security, err := newAsterProfileAndSecurity(astercommon.ProductPerp, userAddress, apiWalletPrivateKey, expectedSigner, settings)
	var sdk *asterperp.Client
	if err == nil {
		sdk, err = asterperp.NewClient(profile, security)
	}
	if err == nil && sdk != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return &asterPerpClient{
		perpClient: perpClient{meta: clientMeta{venue: exchange.VenueAster, product: exchange.ProductPerp}},
		sdk:        sdk,
		profile:    profile,
		security:   security,
	}
}

func newAsterProfileAndSecurity(product astercommon.Product, user, key, signer string, settings Settings) (astercommon.Profile, *astercommon.SecurityContext, error) {
	env := astercommon.EnvironmentProduction
	if strings.EqualFold(settings.Environment, "testnet") || strings.EqualFold(settings.Environment, "demo") {
		env = astercommon.EnvironmentTestnet
	}
	profile, err := astercommon.NewProfile(env, product)
	if err != nil {
		return astercommon.Profile{}, nil, err
	}
	profile, err = profile.WithEndpointOverrides(settings.Endpoint, settings.WebSocketEndpoint, settings.WebSocketEndpoint)
	if err != nil {
		return astercommon.Profile{}, nil, err
	}
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{User: user, PrivateKey: key, ExpectedSigner: signer})
	return profile, security, err
}

func (client *asterSpotClient) ready(operation string) error {
	if client == nil || client.sdk == nil {
		return asterErr(exchange.ProductSpot, operation, exchange.KindInvalidConfig, "Aster Spot SDK client is not configured")
	}
	return nil
}

func (client *asterPerpClient) ready(operation string) error {
	if client == nil || client.sdk == nil {
		return asterErr(exchange.ProductPerp, operation, exchange.KindInvalidConfig, "Aster Perp SDK client is not configured")
	}
	return nil
}

func (client *asterSpotClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := client.ready("Instruments"); err != nil {
		return nil, err
	}
	resp, err := client.sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "Instruments", err)
	}
	out := make([]exchange.Instrument, 0, len(resp.Symbols))
	for _, row := range resp.Symbols {
		if row.Status != "TRADING" {
			continue
		}
		price, qty, minQty, minNotional, err := asterSpotFilters(row.Filters)
		if err != nil {
			return nil, asterMalformed(exchange.ProductSpot, "Instruments", err.Error())
		}
		out = append(out, exchange.Instrument{
			Symbol:            asterCanonical(row.Symbol),
			BaseAsset:         row.BaseAsset,
			QuoteAsset:        row.QuoteAsset,
			Product:           exchange.ProductSpot,
			PriceIncrement:    price,
			QuantityIncrement: qty,
			MinQuantity:       minQty,
			MinNotional:       minNotional,
		})
	}
	return out, nil
}

func (client *asterPerpClient) Instruments(ctx context.Context) ([]exchange.Instrument, error) {
	if err := client.ready("Instruments"); err != nil {
		return nil, err
	}
	resp, err := client.sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "Instruments", err)
	}
	out := make([]exchange.Instrument, 0, len(resp.Symbols))
	for _, row := range resp.Symbols {
		if row.Status != "TRADING" || row.ContractType != "PERPETUAL" || row.MarginAsset != "USDT" {
			continue
		}
		price, qty, minQty, minNotional, err := asterPerpFilters(row.Filters)
		if err != nil {
			return nil, asterMalformed(exchange.ProductPerp, "Instruments", err.Error())
		}
		out = append(out, exchange.Instrument{
			Symbol:            asterCanonical(row.Symbol),
			BaseAsset:         row.BaseAsset,
			QuoteAsset:        row.QuoteAsset,
			SettleAsset:       row.MarginAsset,
			Product:           exchange.ProductPerp,
			PriceIncrement:    price,
			QuantityIncrement: qty,
			MinQuantity:       minQty,
			MinNotional:       minNotional,
		})
	}
	return out, nil
}

func (client *asterSpotClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := client.ready("OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OrderBook", exchange.ProductSpot)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	venueLimit, err := asterDepthRequestLimit(req.Limit)
	if err != nil {
		return exchange.OrderBook{}, asterErr(exchange.ProductSpot, "OrderBook", exchange.KindInvalidRequest, err.Error())
	}
	resp, err := client.sdk.Depth(ctx, native, venueLimit)
	if err != nil {
		return exchange.OrderBook{}, asterNormalizeErr(exchange.ProductSpot, "OrderBook", err)
	}
	return asterOrderBook(canonical, req.Limit, resp.Bids, resp.Asks, resp.LastUpdateID, resp.TransactionTime, exchange.ProductSpot)
}

func (client *asterPerpClient) OrderBook(ctx context.Context, req exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := client.ready("OrderBook"); err != nil {
		return exchange.OrderBook{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OrderBook", exchange.ProductPerp)
	if err != nil {
		return exchange.OrderBook{}, err
	}
	venueLimit, err := asterDepthRequestLimit(req.Limit)
	if err != nil {
		return exchange.OrderBook{}, asterErr(exchange.ProductPerp, "OrderBook", exchange.KindInvalidRequest, err.Error())
	}
	resp, err := client.sdk.Depth(ctx, native, venueLimit)
	if err != nil {
		return exchange.OrderBook{}, asterNormalizeErr(exchange.ProductPerp, "OrderBook", err)
	}
	return asterOrderBook(canonical, req.Limit, resp.Bids, resp.Asks, resp.LastUpdateID, resp.TransactionTime, exchange.ProductPerp)
}

func (client *asterSpotClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := client.ready("Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	native, _, err := asterSymbols(req.Instrument, "Candles", exchange.ProductSpot)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	resp, err := client.sdk.Klines(ctx, native, req.Interval, req.Limit, asterMillis(req.Start), asterMillis(req.End))
	if err != nil {
		return exchange.CandlePage{}, asterNormalizeErr(exchange.ProductSpot, "Candles", err)
	}
	page, err := asterCandles(resp, req)
	if err != nil {
		return exchange.CandlePage{}, asterMalformed(exchange.ProductSpot, "Candles", err.Error())
	}
	return page, nil
}

func (client *asterPerpClient) Candles(ctx context.Context, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	if err := client.ready("Candles"); err != nil {
		return exchange.CandlePage{}, err
	}
	native, _, err := asterSymbols(req.Instrument, "Candles", exchange.ProductPerp)
	if err != nil {
		return exchange.CandlePage{}, err
	}
	resp, err := client.sdk.Klines(ctx, native, req.Interval, req.Limit, asterMillis(req.Start), asterMillis(req.End))
	if err != nil {
		return exchange.CandlePage{}, asterNormalizeErr(exchange.ProductPerp, "Candles", err)
	}
	page, err := asterCandles(resp, req)
	if err != nil {
		return exchange.CandlePage{}, asterMalformed(exchange.ProductPerp, "Candles", err.Error())
	}
	return page, nil
}

func (client *asterSpotClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := client.ready("PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "PublicTrades", exchange.ProductSpot)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	resp, err := client.sdk.GetTrades(ctx, native, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, asterNormalizeErr(exchange.ProductSpot, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(resp))
	for _, row := range resp {
		price, qty, err := parse2(row.Price, row.Qty)
		if err != nil {
			return exchange.PublicTradePage{}, asterMalformed(exchange.ProductSpot, "PublicTrades", err.Error())
		}
		side := exchange.SideBuy
		if row.IsBuyerMaker {
			side = exchange.SideSell
		}
		trades = append(trades, exchange.PublicTrade{Instrument: canonical, TradeID: strconv.FormatInt(row.ID, 10), Side: side, Price: price, Quantity: qty, Time: time.UnixMilli(row.Time).UTC()})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *asterPerpClient) PublicTrades(ctx context.Context, req exchange.PublicTradesRequest) (exchange.PublicTradePage, error) {
	if err := client.ready("PublicTrades"); err != nil {
		return exchange.PublicTradePage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "PublicTrades", exchange.ProductPerp)
	if err != nil {
		return exchange.PublicTradePage{}, err
	}
	resp, err := client.sdk.GetAggTrades(ctx, native, req.Limit)
	if err != nil {
		return exchange.PublicTradePage{}, asterNormalizeErr(exchange.ProductPerp, "PublicTrades", err)
	}
	trades := make([]exchange.PublicTrade, 0, len(resp))
	for _, row := range resp {
		price, qty, err := parse2(row.Price, row.Quantity)
		if err != nil {
			return exchange.PublicTradePage{}, asterMalformed(exchange.ProductPerp, "PublicTrades", err.Error())
		}
		side := exchange.SideBuy
		if row.IsBuyerMaker {
			side = exchange.SideSell
		}
		trades = append(trades, exchange.PublicTrade{Instrument: canonical, TradeID: strconv.FormatInt(row.ID, 10), Side: side, Price: price, Quantity: qty, Time: time.UnixMilli(row.Timestamp).UTC()})
	}
	return exchange.PublicTradePage{Trades: trades, Page: exchange.PageInfo{Limit: req.Limit}}, nil
}

func (client *asterSpotClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.ready("PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductSpot); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, canonical, err := asterSymbols(req.Instrument, "PlaceOrder", exchange.ProductSpot)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := client.sdk.PlaceOrder(ctx, asterSpotPlaceParams(native, req))
	if err != nil {
		return asterCommandAck(exchange.ProductSpot, exchange.OrderOperationPlace, canonical, "", req.ClientOrderID, err)
	}
	return asterSpotAck(exchange.OrderOperationPlace, canonical, resp)
}

func (client *asterPerpClient) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.ready("PlaceOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if err := req.Validate(exchange.ProductPerp); err != nil {
		return exchange.OrderAcknowledgement{}, withExchangeOperation(err, "PlaceOrder")
	}
	native, canonical, err := asterSymbols(req.Instrument, "PlaceOrder", exchange.ProductPerp)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := client.sdk.PlaceOrder(ctx, asterPerpPlaceParams(native, req))
	if err != nil {
		return asterCommandAck(exchange.ProductPerp, exchange.OrderOperationPlace, canonical, "", req.ClientOrderID, err)
	}
	return asterPerpAck(exchange.OrderOperationPlace, canonical, resp)
}

func (client *asterSpotClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.ready("CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if !factoryPortableOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, asterErr(exchange.ProductSpot, "CancelOrder", exchange.KindInvalidRequest, "order id must be portable")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(req.OrderID), 10, 64)
	if err != nil || id <= 0 {
		return exchange.OrderAcknowledgement{}, asterErr(exchange.ProductSpot, "CancelOrder", exchange.KindInvalidRequest, "order id must be a positive integer")
	}
	native, canonical, err := asterSymbols(req.Instrument, "CancelOrder", exchange.ProductSpot)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := client.sdk.CancelOrder(ctx, asterspot.CancelOrderParams{Symbol: native, OrderID: &id})
	if err != nil {
		return asterCommandAck(exchange.ProductSpot, exchange.OrderOperationCancel, canonical, req.OrderID, "", err)
	}
	return asterSpotAck(exchange.OrderOperationCancel, canonical, resp)
}

func (client *asterPerpClient) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	if err := client.ready("CancelOrder"); err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	if !factoryPortableOrderID(req.OrderID) {
		return exchange.OrderAcknowledgement{}, asterErr(exchange.ProductPerp, "CancelOrder", exchange.KindInvalidRequest, "order id must be portable")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(req.OrderID), 10, 64)
	if err != nil || id <= 0 {
		return exchange.OrderAcknowledgement{}, asterErr(exchange.ProductPerp, "CancelOrder", exchange.KindInvalidRequest, "order id must be a positive integer")
	}
	native, canonical, err := asterSymbols(req.Instrument, "CancelOrder", exchange.ProductPerp)
	if err != nil {
		return exchange.OrderAcknowledgement{}, err
	}
	resp, err := client.sdk.CancelOrder(ctx, asterperp.CancelOrderParams{Symbol: native, OrderID: &id})
	if err != nil {
		return asterCommandAck(exchange.ProductPerp, exchange.OrderOperationCancel, canonical, req.OrderID, "", err)
	}
	return asterPerpAck(exchange.OrderOperationCancel, canonical, resp)
}

func (client *asterSpotClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := client.ready("OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OpenOrders", exchange.ProductSpot)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	resp, err := client.sdk.OpenOrders(ctx, asterspot.OpenOrdersQuery{Symbol: native})
	if err != nil {
		return exchange.OrderPage{}, asterNormalizeErr(exchange.ProductSpot, "OpenOrders", err)
	}
	orders, err := asterSpotOrders(resp, canonical)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *asterPerpClient) OpenOrders(ctx context.Context, req exchange.OpenOrdersRequest) (exchange.OrderPage, error) {
	if err := client.ready("OpenOrders"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OpenOrders", exchange.ProductPerp)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	resp, err := client.sdk.OpenOrders(ctx, asterperp.OpenOrdersQuery{Symbol: native})
	if err != nil {
		return exchange.OrderPage{}, asterNormalizeErr(exchange.ProductPerp, "OpenOrders", err)
	}
	orders, err := asterPerpOrders(resp, canonical)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return boundedOrderPage(orders, req.Limit, ""), nil
}

func (client *asterSpotClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := client.ready("OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OrderHistory", exchange.ProductSpot)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	resp, err := client.sdk.AllOrders(ctx, asterspot.AllOrdersQuery{Symbol: native, StartTime: ptrMillis(req.Start), EndTime: ptrMillis(req.End), Limit: ptrInt(req.Limit)})
	if err != nil {
		return exchange.OrderPage{}, asterNormalizeErr(exchange.ProductSpot, "OrderHistory", err)
	}
	orders, err := asterSpotOrders(resp, canonical)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (client *asterPerpClient) OrderHistory(ctx context.Context, req exchange.OrderHistoryRequest) (exchange.OrderPage, error) {
	if err := client.ready("OrderHistory"); err != nil {
		return exchange.OrderPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "OrderHistory", exchange.ProductPerp)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	resp, err := client.sdk.AllOrders(ctx, asterperp.AllOrdersQuery{Symbol: native, StartTime: ptrMillis(req.Start), EndTime: ptrMillis(req.End), Limit: ptrInt(req.Limit)})
	if err != nil {
		return exchange.OrderPage{}, asterNormalizeErr(exchange.ProductPerp, "OrderHistory", err)
	}
	orders, err := asterPerpOrders(resp, canonical)
	if err != nil {
		return exchange.OrderPage{}, err
	}
	return exchange.OrderPage{Orders: orders, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (client *asterSpotClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := client.ready("Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "Fills", exchange.ProductSpot)
	if err != nil {
		return exchange.FillPage{}, err
	}
	resp, err := client.sdk.UserTrades(ctx, asterspot.UserTradesQuery{Symbol: native, StartTime: ptrMillis(req.Start), EndTime: ptrMillis(req.End), Limit: ptrInt(req.Limit)})
	if err != nil {
		return exchange.FillPage{}, asterNormalizeErr(exchange.ProductSpot, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(resp))
	for _, row := range resp {
		fill, err := asterSpotFill(row, canonical)
		if err != nil {
			return exchange.FillPage{}, err
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (client *asterPerpClient) Fills(ctx context.Context, req exchange.FillsRequest) (exchange.FillPage, error) {
	if err := client.ready("Fills"); err != nil {
		return exchange.FillPage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "Fills", exchange.ProductPerp)
	if err != nil {
		return exchange.FillPage{}, err
	}
	resp, err := client.sdk.UserTrades(ctx, asterperp.UserTradesQuery{Symbol: native, StartTime: ptrMillis(req.Start), EndTime: ptrMillis(req.End), Limit: ptrInt(req.Limit)})
	if err != nil {
		return exchange.FillPage{}, asterNormalizeErr(exchange.ProductPerp, "Fills", err)
	}
	fills := make([]exchange.Fill, 0, len(resp))
	for _, row := range resp {
		fill, err := asterPerpFill(row, canonical)
		if err != nil {
			return exchange.FillPage{}, err
		}
		fills = append(fills, fill)
	}
	return exchange.FillPage{Fills: fills, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (client *asterSpotClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.SpotAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *asterSpotClient) SpotAccount(ctx context.Context) (exchange.SpotAccount, error) {
	if err := client.ready("SpotAccount"); err != nil {
		return exchange.SpotAccount{}, err
	}
	resp, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return exchange.SpotAccount{}, asterNormalizeErr(exchange.ProductSpot, "SpotAccount", err)
	}
	balances, err := asterSpotBalances(resp.Balances)
	if err != nil {
		return exchange.SpotAccount{}, err
	}
	return exchange.SpotAccount{Balances: balances}, nil
}

func (client *asterPerpClient) Balances(ctx context.Context) ([]exchange.Balance, error) {
	account, err := client.PerpAccount(ctx)
	if err != nil {
		return nil, withExchangeOperation(err, "Balances")
	}
	return account.Balances, nil
}

func (client *asterPerpClient) PerpAccount(ctx context.Context) (exchange.PerpAccount, error) {
	if err := client.ready("PerpAccount"); err != nil {
		return exchange.PerpAccount{}, err
	}
	resp, err := client.sdk.GetAccount(ctx)
	if err != nil {
		return exchange.PerpAccount{}, asterNormalizeErr(exchange.ProductPerp, "PerpAccount", err)
	}
	balances := make([]exchange.Balance, 0, len(resp.Assets))
	for _, row := range resp.Assets {
		total, available, err := parse2(row.WalletBalance, row.AvailableBalance)
		if err != nil {
			return exchange.PerpAccount{}, asterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
		}
		balances = append(balances, exchange.Balance{Asset: row.Asset, Available: available, Total: total, Locked: total.Sub(available)})
	}
	margin, err := decimal.NewFromString(asterDefaultZero(resp.TotalInitialMargin))
	if err != nil {
		return exchange.PerpAccount{}, asterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	equity, err := decimal.NewFromString(asterDefaultZero(resp.TotalMarginBalance))
	if err != nil {
		return exchange.PerpAccount{}, asterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	available, err := decimal.NewFromString(asterDefaultZero(resp.AvailableBalance))
	if err != nil {
		return exchange.PerpAccount{}, asterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	upl, err := decimal.NewFromString(asterDefaultZero(resp.TotalUnrealizedProfit))
	if err != nil {
		return exchange.PerpAccount{}, asterMalformed(exchange.ProductPerp, "PerpAccount", err.Error())
	}
	return exchange.PerpAccount{
		Balances:      balances,
		Equity:        exchange.OptionalDecimal{Value: equity, Valid: true},
		Available:     exchange.OptionalDecimal{Value: available, Valid: true},
		MarginUsed:    exchange.OptionalDecimal{Value: margin, Valid: true},
		UnrealizedPnL: exchange.OptionalDecimal{Value: upl, Valid: true},
	}, nil
}

func (client *asterPerpClient) Positions(ctx context.Context, req exchange.PositionsRequest) ([]exchange.Position, error) {
	if err := client.ready("Positions"); err != nil {
		return nil, err
	}
	native := ""
	canonical := ""
	var err error
	if req.Instrument != "" {
		native, canonical, err = asterSymbols(req.Instrument, "Positions", exchange.ProductPerp)
		if err != nil {
			return nil, err
		}
	}
	resp, err := client.sdk.GetPositionRisk(ctx, native)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "Positions", err)
	}
	out := make([]exchange.Position, 0, len(resp))
	for _, row := range resp {
		inst := asterCanonical(row.Symbol)
		if canonical != "" {
			inst = canonical
		}
		pos, err := asterPosition(row, inst)
		if err != nil {
			return nil, err
		}
		out = append(out, pos)
	}
	return out, nil
}

func (client *asterPerpClient) FundingRate(ctx context.Context, req exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := client.ready("FundingRate"); err != nil {
		return exchange.FundingRate{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "FundingRate", exchange.ProductPerp)
	if err != nil {
		return exchange.FundingRate{}, err
	}
	resp, err := client.sdk.GetFundingRate(ctx, native)
	if err != nil {
		return exchange.FundingRate{}, asterNormalizeErr(exchange.ProductPerp, "FundingRate", err)
	}
	rate, mark, err := parse2(resp.LastFundingRate, resp.MarkPrice)
	if err != nil {
		return exchange.FundingRate{}, asterMalformed(exchange.ProductPerp, "FundingRate", err.Error())
	}
	return exchange.FundingRate{Instrument: canonical, Rate: rate, MarkPrice: exchange.OptionalDecimal{Value: mark, Valid: true}, ObservedAt: time.UnixMilli(resp.Time).UTC(), NextFundingTime: time.UnixMilli(resp.NextFundingTime).UTC()}, nil
}

func (client *asterPerpClient) FundingRateHistory(ctx context.Context, req exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	if err := client.ready("FundingRateHistory"); err != nil {
		return exchange.FundingRatePage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "FundingRateHistory", exchange.ProductPerp)
	if err != nil {
		return exchange.FundingRatePage{}, err
	}
	resp, err := client.sdk.GetFundingRateHistory(ctx, native, asterMillis(req.Start), asterMillis(req.End), req.Limit)
	if err != nil {
		return exchange.FundingRatePage{}, asterNormalizeErr(exchange.ProductPerp, "FundingRateHistory", err)
	}
	rates := make([]exchange.FundingRate, 0, len(resp))
	for _, row := range resp {
		rate, err := decimal.NewFromString(row.FundingRate)
		if err != nil {
			return exchange.FundingRatePage{}, asterMalformed(exchange.ProductPerp, "FundingRateHistory", "invalid funding rate")
		}
		rates = append(rates, exchange.FundingRate{Instrument: canonical, Rate: rate, FundingTime: time.UnixMilli(row.FundingTime).UTC()})
	}
	return exchange.FundingRatePage{Rates: rates, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func (client *asterPerpClient) SetLeverage(ctx context.Context, req exchange.SetLeverageRequest) (exchange.Leverage, error) {
	if err := client.ready("SetLeverage"); err != nil {
		return exchange.Leverage{}, err
	}
	native, canonical, err := asterSymbols(req.Instrument, "SetLeverage", exchange.ProductPerp)
	if err != nil {
		return exchange.Leverage{}, err
	}
	if req.Leverage <= 0 {
		return exchange.Leverage{}, asterErr(exchange.ProductPerp, "SetLeverage", exchange.KindInvalidRequest, "leverage must be positive")
	}
	resp, err := client.sdk.ChangeLeverage(ctx, native, req.Leverage)
	if err != nil {
		return exchange.Leverage{}, asterNormalizeErr(exchange.ProductPerp, "SetLeverage", err)
	}
	if resp.Symbol != native || resp.Leverage != req.Leverage {
		return exchange.Leverage{}, asterMalformed(exchange.ProductPerp, "SetLeverage", "response does not match request")
	}
	return exchange.Leverage{Instrument: canonical, Effective: resp.Leverage}, nil
}

func (client *asterSpotClient) WebSocket() exchange.SpotWebSocket {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.ws != nil {
		return client.ws
	}
	backend := &asterSpotWSBackend{client: client}
	client.ws = newSpotWebSocket(newPublicWebSocket(client.meta, backend), backend)
	return client.ws
}

func (client *asterPerpClient) WebSocket() exchange.PerpWebSocket {
	client.wsMu.Lock()
	defer client.wsMu.Unlock()
	if client.ws != nil {
		return client.ws
	}
	backend := &asterPerpWSBackend{client: client}
	client.ws = newPerpWebSocket(client.meta, backend, backend)
	return client.ws
}

func (client *asterSpotClient) Close() error {
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
func (client *asterPerpClient) Close() error {
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

func asterErr(product exchange.Product, operation string, kind exchange.ErrorKind, msg string) error {
	return exchange.NewError(kind, exchange.ErrorDetails{Venue: exchange.VenueAster, Product: product, Operation: operation, SafeMessage: msg})
}

func asterMalformed(product exchange.Product, operation, msg string) error {
	return asterErr(product, operation, exchange.KindMalformedResponse, msg)
}

func asterNormalizeErr(product exchange.Product, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return asterErr(product, operation, exchange.KindCanceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return asterErr(product, operation, exchange.KindDeadlineExceeded, "request deadline exceeded")
	}
	var spotVenue *astercommon.VenueError
	if errors.As(err, &spotVenue) {
		return exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueAster, Product: product, Operation: operation, Code: strconv.Itoa(spotVenue.Code()), SafeMessage: "Aster rejected the request"})
	}
	return asterErr(product, operation, exchange.KindTransport, "Aster request failed")
}

func asterSymbols(instrument, operation string, product exchange.Product) (native string, canonical string, err error) {
	canonical = strings.TrimSpace(instrument)
	if canonical == "" {
		return "", "", asterErr(product, operation, exchange.KindInvalidRequest, "instrument is required")
	}
	if canonical != instrument {
		return "", "", asterErr(product, operation, exchange.KindInvalidRequest, "instrument must not have surrounding whitespace")
	}
	return strings.ReplaceAll(canonical, "-", ""), canonical, nil
}

func asterCanonical(native string) string {
	if strings.Contains(native, "-") {
		return native
	}
	if strings.HasSuffix(native, "USDT") && len(native) > 4 {
		return native[:len(native)-4] + "-USDT"
	}
	return native
}

func asterSpotFilters(filters []asterspot.SymbolFilter) (decimal.Decimal, decimal.Decimal, decimal.Decimal, exchange.OptionalDecimal, error) {
	return asterFilters(len(filters), func(i int) (string, string, string, string, string) {
		f := filters[i]
		return f.FilterType, f.TickSize, f.StepSize, f.MinQty, f.MinNotional
	})
}

func asterPerpFilters(filters []asterperp.SymbolFilter) (decimal.Decimal, decimal.Decimal, decimal.Decimal, exchange.OptionalDecimal, error) {
	return asterFilters(len(filters), func(i int) (string, string, string, string, string) {
		f := filters[i]
		return f.FilterType, f.TickSize, f.StepSize, f.MinQty, f.Notional
	})
}

func asterFilters(n int, row func(int) (string, string, string, string, string)) (price, qty, minQty decimal.Decimal, minNotional exchange.OptionalDecimal, err error) {
	for i := 0; i < n; i++ {
		filterType, tick, step, min, notional := row(i)
		switch filterType {
		case "PRICE_FILTER":
			price, err = decimal.NewFromString(tick)
		case "LOT_SIZE":
			qty, err = decimal.NewFromString(step)
			if err == nil {
				minQty, err = decimal.NewFromString(min)
			}
		case "MIN_NOTIONAL":
			var value decimal.Decimal
			value, err = decimal.NewFromString(notional)
			if err == nil {
				minNotional = exchange.OptionalDecimal{Value: value, Valid: true}
			}
		}
		if err != nil {
			return price, qty, minQty, minNotional, err
		}
	}
	return price, qty, minQty, minNotional, nil
}

func asterOrderBook(instrument string, limit int, bidRows, askRows [][]string, seq, timestamp int64, product exchange.Product) (exchange.OrderBook, error) {
	bids, err := levels(bidRows)
	if err != nil {
		return exchange.OrderBook{}, asterMalformed(product, "OrderBook", err.Error())
	}
	asks, err := levels(askRows)
	if err != nil {
		return exchange.OrderBook{}, asterMalformed(product, "OrderBook", err.Error())
	}
	if limit > 0 && len(bids) > limit {
		bids = bids[:limit]
	}
	if limit > 0 && len(asks) > limit {
		asks = asks[:limit]
	}
	book := exchange.OrderBook{Instrument: instrument, Bids: bids, Asks: asks, Sequence: strconv.FormatInt(seq, 10), Page: exchange.PageInfo{Limit: limit}}
	if timestamp > 0 {
		book.Time = time.UnixMilli(timestamp).UTC()
	}
	return book, nil
}

func asterDepthRequestLimit(limit int) (int, error) {
	if limit < 0 {
		return 0, fmt.Errorf("limit must be non-negative")
	}
	if limit == 0 {
		return 0, nil
	}
	for _, supported := range []int{5, 10, 20, 50, 100, 500, 1000} {
		if limit <= supported {
			return supported, nil
		}
	}
	return 0, fmt.Errorf("limit exceeds venue maximum 1000")
}

func levels(rows [][]string) ([]exchange.BookLevel, error) {
	out := make([]exchange.BookLevel, 0, len(rows))
	for _, row := range rows {
		if len(row) < 2 {
			return nil, fmt.Errorf("book level requires price and quantity")
		}
		price, qty, err := parse2(row[0], row[1])
		if err != nil {
			return nil, err
		}
		out = append(out, exchange.BookLevel{Price: price, Quantity: qty})
	}
	return out, nil
}

func asterCandles[T ~[]interface{}](rows []T, req exchange.CandlesRequest) (exchange.CandlePage, error) {
	candles := make([]exchange.Candle, 0, len(rows))
	for _, row := range rows {
		if len(row) < 7 {
			continue
		}
		open, high, low, closeValue, volume, err := parse5(fmt.Sprint(row[1]), fmt.Sprint(row[2]), fmt.Sprint(row[3]), fmt.Sprint(row[4]), fmt.Sprint(row[5]))
		if err != nil {
			return exchange.CandlePage{}, err
		}
		openTime, err := int64FromAny(row[0])
		if err != nil {
			return exchange.CandlePage{}, err
		}
		closeTime, err := int64FromAny(row[6])
		if err != nil {
			return exchange.CandlePage{}, err
		}
		candles = append(candles, exchange.Candle{
			OpenTime:  time.UnixMilli(openTime).UTC(),
			CloseTime: time.UnixMilli(closeTime).UTC(),
			Open:      open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: true,
		})
	}
	return exchange.CandlePage{Candles: candles, Page: exchange.PageInfo{Limit: req.Limit, WindowStart: req.Start, WindowEnd: req.End}}, nil
}

func asterSpotPlaceParams(symbol string, req exchange.PlaceOrderRequest) asterspot.PlaceOrderParams {
	side, typ, tif := orderParams(req)
	return asterspot.PlaceOrderParams{Symbol: symbol, Side: side, Type: typ, TimeInForce: tif, Quantity: req.Quantity.String(), Price: asterPriceString(req), NewClientOrderID: req.ClientOrderID}
}

func asterPerpPlaceParams(symbol string, req exchange.PlaceOrderRequest) asterperp.PlaceOrderParams {
	side, typ, tif := orderParams(req)
	return asterperp.PlaceOrderParams{Symbol: symbol, Side: side, Type: asterperp.OrderType(typ), TimeInForce: asterperp.TimeInForce(tif), Quantity: req.Quantity.String(), Price: asterPriceString(req), NewClientOrderID: req.ClientOrderID, ReduceOnly: req.ReduceOnly, PositionSide: "BOTH"}
}

func orderParams(req exchange.PlaceOrderRequest) (side, typ, tif string) {
	side = "BUY"
	if req.Side == exchange.SideSell {
		side = "SELL"
	}
	if req.Type == exchange.OrderTypeMarket {
		return side, "MARKET", ""
	}
	switch req.LimitPolicy {
	case exchange.LimitPolicyIOC:
		return side, "LIMIT", "IOC"
	case exchange.LimitPolicyPostOnly:
		return side, "LIMIT", "GTX"
	default:
		return side, "LIMIT", "GTC"
	}
}

func asterPriceString(req exchange.PlaceOrderRequest) string {
	if req.Type == exchange.OrderTypeLimit {
		return req.LimitPrice.String()
	}
	return ""
}

func asterSpotAck(op exchange.OrderOperation, instrument string, resp *asterspot.OrderResponse) (exchange.OrderAcknowledgement, error) {
	if resp == nil {
		return exchange.OrderAcknowledgement{}, asterMalformed(exchange.ProductSpot, string(op), "missing order response")
	}
	return asterAck(exchange.ProductSpot, op, instrument, resp.OrderID, resp.ClientOrderID, resp.Status, resp.Type, resp.ExecutedQty, resp.CumQuote)
}

func asterPerpAck(op exchange.OrderOperation, instrument string, resp *asterperp.OrderResponse) (exchange.OrderAcknowledgement, error) {
	if resp == nil {
		return exchange.OrderAcknowledgement{}, asterMalformed(exchange.ProductPerp, string(op), "missing order response")
	}
	return asterAck(exchange.ProductPerp, op, instrument, resp.OrderID, resp.ClientOrderID, resp.Status, resp.Type, resp.ExecutedQty, resp.CumQuote)
}

func asterAck(product exchange.Product, op exchange.OrderOperation, instrument string, orderID int64, clientID, status, typ, filledText, quoteText string) (exchange.OrderAcknowledgement, error) {
	state := exchange.AckAcceptedPending
	switch status {
	case "NEW":
		if typ != "MARKET" {
			state = exchange.AckResting
		}
	case "PARTIALLY_FILLED":
		state = exchange.AckPartiallyFilled
	case "FILLED":
		state = exchange.AckImmediatelyFilled
	case "CANCELED":
		state = exchange.AckCanceled
	case "REJECTED", "EXPIRED":
		state = exchange.AckRejected
	}
	orderType := exchange.OrderTypeLimit
	if typ == "MARKET" {
		orderType = exchange.OrderTypeMarket
	}
	filled, err := decimal.NewFromString(asterDefaultZero(filledText))
	if err != nil {
		return exchange.OrderAcknowledgement{}, asterMalformed(product, string(op), err.Error())
	}
	quote, err := decimal.NewFromString(asterDefaultZero(quoteText))
	if err != nil {
		return exchange.OrderAcknowledgement{}, asterMalformed(product, string(op), err.Error())
	}
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueAster, Product: product, Operation: op, State: state, Instrument: instrument, OrderType: orderType, OrderID: strconv.FormatInt(orderID, 10), ClientOrderID: clientID, FilledQuantity: filled}
	if filled.IsPositive() && quote.IsPositive() {
		ack.AverageFillPrice = exchange.OptionalDecimal{Value: quote.Div(filled), Valid: true}
	}
	return ack, ack.Validate()
}

func asterCommandAck(product exchange.Product, op exchange.OrderOperation, instrument, orderID, clientID string, err error) (exchange.OrderAcknowledgement, error) {
	ack := exchange.OrderAcknowledgement{Venue: exchange.VenueAster, Product: product, Operation: op, State: exchange.AckAmbiguous, Instrument: instrument, OrderID: orderID, ClientOrderID: clientID}
	var venueErr *astercommon.VenueError
	if errors.As(err, &venueErr) {
		ack.State = exchange.AckRejected
		ack.VenueCode = strconv.Itoa(venueErr.Code())
		ack.VenueMessage = "Aster rejected order command"
		return ack, exchange.NewError(exchange.KindVenueRejected, exchange.ErrorDetails{Venue: exchange.VenueAster, Product: product, Operation: string(op), Code: ack.VenueCode, SafeMessage: "Aster rejected order command"})
	}
	ack.VenueMessage = "order command outcome is unknown after possible send"
	return ack, exchange.NewError(exchange.KindAmbiguousOutcome, exchange.ErrorDetails{Venue: exchange.VenueAster, Product: product, Operation: string(op), SafeMessage: "order command outcome is unknown after possible send"})
}

func asterSpotOrders(rows []asterspot.OrderResponse, instrument string) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		order, err := asterOrder(exchange.ProductSpot, instrument, row.OrderID, row.ClientOrderID, row.Side, row.Type, row.TimeInForce, row.OrigQty, row.Price, row.ExecutedQty, row.Status, false)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func asterPerpOrders(rows []asterperp.OrderResponse, instrument string) ([]exchange.Order, error) {
	out := make([]exchange.Order, 0, len(rows))
	for _, row := range rows {
		order, err := asterOrder(exchange.ProductPerp, instrument, row.OrderID, row.ClientOrderID, row.Side, row.Type, row.TimeInForce, row.OrigQty, row.Price, row.ExecutedQty, row.Status, row.ReduceOnly)
		if err != nil {
			return nil, err
		}
		out = append(out, order)
	}
	return out, nil
}

func asterOrder(product exchange.Product, instrument string, id int64, clientID, sideText, typeText, tif, qtyText, priceText, filledText, status string, reduce bool) (exchange.Order, error) {
	qty, filled, err := parse2(qtyText, asterDefaultZero(filledText))
	if err != nil {
		return exchange.Order{}, asterMalformed(product, "Order", err.Error())
	}
	price, err := decimal.NewFromString(asterDefaultZero(priceText))
	if err != nil {
		return exchange.Order{}, asterMalformed(product, "Order", err.Error())
	}
	side := exchange.SideBuy
	if sideText == "SELL" {
		side = exchange.SideSell
	}
	orderType := exchange.OrderTypeLimit
	if typeText == "MARKET" {
		orderType = exchange.OrderTypeMarket
	}
	return exchange.Order{Instrument: instrument, OrderID: strconv.FormatInt(id, 10), ClientOrderID: clientID, Side: side, Type: orderType, LimitPrice: price, Quantity: qty, Filled: filled, LimitPolicy: asterLimitPolicy(typeText, tif), Status: strings.ToLower(status), ReduceOnly: reduce}, nil
}

func asterLimitPolicy(typ, tif string) exchange.LimitPolicy {
	if typ == "LIMIT_MAKER" {
		return exchange.LimitPolicyPostOnly
	}
	switch tif {
	case "IOC":
		return exchange.LimitPolicyIOC
	case "GTC":
		return exchange.LimitPolicyResting
	default:
		return ""
	}
}

func asterSpotFill(row asterspot.Trade, instrument string) (exchange.Fill, error) {
	price, qty, err := parse2(row.Price, row.Qty)
	if err != nil {
		return exchange.Fill{}, asterMalformed(exchange.ProductSpot, "Fills", err.Error())
	}
	fee, err := decimal.NewFromString(asterDefaultZero(row.Commission))
	if err != nil {
		return exchange.Fill{}, asterMalformed(exchange.ProductSpot, "Fills", err.Error())
	}
	side := exchange.SideSell
	if row.Buyer {
		side = exchange.SideBuy
	}
	liq := exchange.LiquidityTaker
	if row.Maker {
		liq = exchange.LiquidityMaker
	}
	return exchange.Fill{Instrument: instrument, OrderID: strconv.FormatInt(row.OrderID, 10), FillID: strconv.FormatInt(row.ID, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: row.CommissionAsset, Liquidity: liq, Time: time.UnixMilli(row.Time).UTC()}, nil
}

func asterPerpFill(row asterperp.Trade, instrument string) (exchange.Fill, error) {
	price, qty, err := parse2(row.Price, row.Qty)
	if err != nil {
		return exchange.Fill{}, asterMalformed(exchange.ProductPerp, "Fills", err.Error())
	}
	fee, err := decimal.NewFromString(asterDefaultZero(row.Commission))
	if err != nil {
		return exchange.Fill{}, asterMalformed(exchange.ProductPerp, "Fills", err.Error())
	}
	side := exchange.SideBuy
	if row.Side == "SELL" {
		side = exchange.SideSell
	}
	liq := exchange.LiquidityTaker
	if row.Maker {
		liq = exchange.LiquidityMaker
	}
	return exchange.Fill{Instrument: instrument, OrderID: strconv.FormatInt(row.OrderID, 10), FillID: strconv.FormatInt(row.ID, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: row.CommissionAsset, Liquidity: liq, Time: time.UnixMilli(row.Time).UTC()}, nil
}

func asterSpotBalances(rows []asterspot.Balance) ([]exchange.Balance, error) {
	out := make([]exchange.Balance, 0, len(rows))
	for _, row := range rows {
		free, locked, err := parse2(row.Free, row.Locked)
		if err != nil {
			return nil, asterMalformed(exchange.ProductSpot, "SpotAccount", err.Error())
		}
		out = append(out, exchange.Balance{Asset: row.Asset, Available: free, Locked: locked, Total: free.Add(locked)})
	}
	return out, nil
}

func asterPosition(row asterperp.PositionRiskResponse, instrument string) (exchange.Position, error) {
	qty, entry, err := parse2(row.PositionAmt, row.EntryPrice)
	if err != nil {
		return exchange.Position{}, asterMalformed(exchange.ProductPerp, "Positions", err.Error())
	}
	mark, pnl, err := parse2(row.MarkPrice, row.UnRealizedProfit)
	if err != nil {
		return exchange.Position{}, asterMalformed(exchange.ProductPerp, "Positions", err.Error())
	}
	liq, err := decimal.NewFromString(asterDefaultZero(row.LiquidationPrice))
	if err != nil {
		return exchange.Position{}, asterMalformed(exchange.ProductPerp, "Positions", err.Error())
	}
	lev, err := decimal.NewFromString(asterDefaultZero(row.Leverage))
	if err != nil {
		return exchange.Position{}, asterMalformed(exchange.ProductPerp, "Positions", err.Error())
	}
	margin, err := decimal.NewFromString(asterDefaultZero(row.IsolatedMargin))
	if err != nil {
		return exchange.Position{}, asterMalformed(exchange.ProductPerp, "Positions", err.Error())
	}
	side := exchange.SideBuy
	if qty.IsNegative() || row.PositionSide == "SHORT" {
		side = exchange.SideSell
	}
	return exchange.Position{Instrument: instrument, Side: side, Quantity: qty.Abs(), EntryPrice: entry, MarkPrice: mark, UnrealizedPnL: pnl, LiquidationPrice: exchange.OptionalDecimal{Value: liq, Valid: !liq.IsZero()}, Leverage: exchange.OptionalDecimal{Value: lev, Valid: !lev.IsZero()}, MarginUsed: exchange.OptionalDecimal{Value: margin, Valid: true}}, nil
}

func parse2(a, b string) (decimal.Decimal, decimal.Decimal, error) {
	left, err := decimal.NewFromString(asterDefaultZero(a))
	if err != nil {
		return decimal.Zero, decimal.Zero, err
	}
	right, err := decimal.NewFromString(asterDefaultZero(b))
	return left, right, err
}

func asterSpotWSCandle(event *asterspot.KlineEvent) (exchange.Candle, error) {
	if event == nil {
		return exchange.Candle{}, fmt.Errorf("nil kline event")
	}
	open, high, low, closeValue, volume, err := parse5(event.Kline.OpenPrice, event.Kline.HighPrice, event.Kline.LowPrice, event.Kline.ClosePrice, event.Kline.Volume)
	if err != nil {
		return exchange.Candle{}, err
	}
	return exchange.Candle{OpenTime: time.UnixMilli(event.Kline.StartTime).UTC(), CloseTime: time.UnixMilli(event.Kline.CloseTime).UTC(), Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: event.Kline.IsClosed}, nil
}

func asterPerpWSCandle(event *asterperp.WsKlineEvent) (exchange.Candle, error) {
	if event == nil {
		return exchange.Candle{}, fmt.Errorf("nil kline event")
	}
	open, high, low, closeValue, volume, err := parse5(event.Kline.OpenPrice, event.Kline.HighPrice, event.Kline.LowPrice, event.Kline.ClosePrice, event.Kline.Volume)
	if err != nil {
		return exchange.Candle{}, err
	}
	return exchange.Candle{OpenTime: time.UnixMilli(event.Kline.StartTime).UTC(), CloseTime: time.UnixMilli(event.Kline.EndTime).UTC(), Open: open, High: high, Low: low, Close: closeValue, Volume: volume, Complete: event.Kline.IsClosed}, nil
}

func parse5(a, b, c, d, e string) (decimal.Decimal, decimal.Decimal, decimal.Decimal, decimal.Decimal, decimal.Decimal, error) {
	first, err := decimal.NewFromString(asterDefaultZero(a))
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	second, err := decimal.NewFromString(asterDefaultZero(b))
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	third, err := decimal.NewFromString(asterDefaultZero(c))
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	fourth, err := decimal.NewFromString(asterDefaultZero(d))
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	fifth, err := decimal.NewFromString(asterDefaultZero(e))
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	return first, second, third, fourth, fifth, nil
}

func asterCallbackGate() (func() bool, func() error) {
	var closed atomic.Bool
	return func() bool { return !closed.Load() }, func() error {
		closed.Store(true)
		return nil
	}
}

func asterDefaultZero(value string) string {
	if strings.TrimSpace(value) == "" {
		return "0"
	}
	return value
}

func asterMillis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func ptrMillis(t time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	value := t.UnixMilli()
	return &value
}

func ptrInt(value int) *int {
	if value <= 0 {
		return nil
	}
	return &value
}

func factoryPortableOrderID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 48)
	return err == nil && parsed > 0
}

func int64FromAny(value any) (int64, error) {
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	default:
		return strconv.ParseInt(fmt.Sprint(value), 10, 64)
	}
}

type asterSpotWSBackend struct {
	client  *asterSpotClient
	market  *asterspot.WsMarketClient
	account *asterspot.WsAccountClient
}

type asterPerpWSBackend struct {
	client  *asterPerpClient
	market  *asterperp.WsMarketClient
	account *asterperp.WsAccountClient
}

func (backend *asterSpotWSBackend) ensureAccount(ctx context.Context) (*asterspot.WsAccountClient, error) {
	if backend.account != nil {
		return backend.account, nil
	}
	ws, err := asterspot.NewWsAccountClient(context.Background(), backend.client.profile, backend.client.security)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "PrivateWebSocket", err)
	}
	if err := ws.Connect(); err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "PrivateWebSocket", err)
	}
	backend.account = ws
	return ws, nil
}

func (backend *asterPerpWSBackend) ensureAccount(ctx context.Context) (*asterperp.WsAccountClient, error) {
	if backend.account != nil {
		return backend.account, nil
	}
	ws, err := asterperp.NewWsAccountClient(context.Background(), backend.client.profile, backend.client.security)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "PrivateWebSocket", err)
	}
	if err := ws.Connect(); err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "PrivateWebSocket", err)
	}
	backend.account = ws
	return ws, nil
}

func (backend *asterSpotWSBackend) ensureMarket(ctx context.Context) (*asterspot.WsMarketClient, error) {
	if backend.market != nil && backend.market.IsConnected() {
		return backend.market, nil
	}
	ws, err := asterspot.NewWsMarketClient(context.Background(), backend.client.profile)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "WebSocket", err)
	}
	if err := ws.Connect(); err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "WebSocket", err)
	}
	backend.market = ws
	return ws, nil
}

func (backend *asterPerpWSBackend) ensureMarket(ctx context.Context) (*asterperp.WsMarketClient, error) {
	if backend.market != nil && backend.market.IsConnected() {
		return backend.market, nil
	}
	ws, err := asterperp.NewWsMarketClient(context.Background(), backend.client.profile)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "WebSocket", err)
	}
	if err := ws.Connect(); err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "WebSocket", err)
	}
	backend.market = ws
	return ws, nil
}

func (backend *asterSpotWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchOrderBook", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeIncrementOrderBook(native, "", func(event *asterspot.WsDepthEvent) error {
		book, err := asterOrderBook(canonical, 0, event.Bids, event.Asks, event.FinalUpdateID, event.EventTime, exchange.ProductSpot)
		if err != nil {
			return err
		}
		callbacks.Event(exchange.BookEvent{Kind: exchange.EventDelta, Instrument: canonical, Sequence: book.Sequence, Bids: book.Bids, Asks: book.Asks, Time: book.Time})
		return nil
	})
	return func() error { return ws.UnsubscribeIncrementOrderBook(native, "") }, err
}

func (backend *asterPerpWSBackend) StartOrderBook(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BookEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchOrderBook", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeIncrementOrderBook(native, "", func(event *asterperp.WsDepthEvent) error {
		book, err := asterOrderBook(canonical, 0, event.Bids, event.Asks, event.FinalUpdateID, event.EventTime, exchange.ProductPerp)
		if err != nil {
			return err
		}
		callbacks.Event(exchange.BookEvent{Kind: exchange.EventDelta, Instrument: canonical, Sequence: book.Sequence, Previous: strconv.FormatInt(event.FinalUpdateIDLast, 10), Bids: book.Bids, Asks: book.Asks, Time: book.Time})
		return nil
	})
	return func() error { return ws.UnsubscribeIncrementOrderBook(native, "") }, err
}

func (backend *asterSpotWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchBBO", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	err = ws.SubscribeBookTicker(native, func(event *asterspot.BookTickerEvent) error {
		if !active() {
			return nil
		}
		bidP, bidQ, err := parse2(event.BestBidPrice, event.BestBidQty)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchBBO", err.Error()))
			return nil
		}
		askP, askQ, err := parse2(event.BestAskPrice, event.BestAskQty)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchBBO", err.Error()))
			return nil
		}
		callbacks.Event(exchange.BBOEvent{Instrument: canonical, Bid: exchange.BookLevel{Price: bidP, Quantity: bidQ}, Ask: exchange.BookLevel{Price: askP, Quantity: askQ}})
		return nil
	})
	return stop, err
}

func (backend *asterPerpWSBackend) StartBBO(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.BBOEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchBBO", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeBookTicker(native, func(event *asterperp.WsBookTickerEvent) error {
		bidP, bidQ, err := parse2(event.BestBidPrice, event.BestBidQty)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchBBO", err.Error()))
			return nil
		}
		askP, askQ, err := parse2(event.BestAskPrice, event.BestAskQty)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchBBO", err.Error()))
			return nil
		}
		callbacks.Event(exchange.BBOEvent{Instrument: canonical, Bid: exchange.BookLevel{Price: bidP, Quantity: bidQ}, Ask: exchange.BookLevel{Price: askP, Quantity: askQ}, Time: time.UnixMilli(event.EventTime).UTC()})
		return nil
	})
	return func() error { return ws.UnsubscribeBookTicker(native) }, err
}

func (backend *asterSpotWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchPublicTrades", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeAggTrade(native, func(event *asterspot.AggTradeEvent) error {
		price, qty, err := parse2(event.Price, event.Quantity)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchPublicTrades", err.Error()))
			return nil
		}
		side := exchange.SideBuy
		if event.IsBuyerMaker {
			side = exchange.SideSell
		}
		callbacks.Event(exchange.PublicTradeEvent{Instrument: canonical, TradeID: strconv.FormatInt(event.AggTradeID, 10), Side: side, Price: price, Quantity: qty, Time: time.UnixMilli(event.TradeTime).UTC()})
		return nil
	})
	return func() error { return ws.UnsubscribeAggTrade(native) }, err
}

func (backend *asterPerpWSBackend) StartPublicTrades(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PublicTradeEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchPublicTrades", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeAggTrade(native, func(event *asterperp.WsAggTradeEvent) error {
		price, qty, err := parse2(event.Price, event.Quantity)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchPublicTrades", err.Error()))
			return nil
		}
		side := exchange.SideBuy
		if event.IsBuyerMaker {
			side = exchange.SideSell
		}
		callbacks.Event(exchange.PublicTradeEvent{Instrument: canonical, TradeID: strconv.FormatInt(event.AggTradeID, 10), Side: side, Price: price, Quantity: qty, Time: time.UnixMilli(event.TradeTime).UTC()})
		return nil
	})
	return func() error { return ws.UnsubscribeAggTrade(native) }, err
}

func (backend *asterSpotWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchCandles", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeKline(native, interval, func(event *asterspot.KlineEvent) error {
		candle, err := asterSpotWSCandle(event)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchCandles", err.Error()))
			return nil
		}
		callbacks.Event(exchange.CandleEvent{Instrument: canonical, Interval: interval, Candle: candle})
		return nil
	})
	return func() error { return ws.UnsubscribeKline(native, interval) }, err
}

func (backend *asterPerpWSBackend) StartCandles(ctx context.Context, instrument, interval string, callbacks streamCallbacks[exchange.CandleEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchCandles", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeKline(native, interval, func(event *asterperp.WsKlineEvent) error {
		candle, err := asterPerpWSCandle(event)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchCandles", err.Error()))
			return nil
		}
		callbacks.Event(exchange.CandleEvent{Instrument: canonical, Interval: interval, Candle: candle})
		return nil
	})
	return func() error { return ws.UnsubscribeKline(native, interval) }, err
}

func (backend *asterSpotWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	_, canonical, err := asterSymbols(instrument, "WatchOrders", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeExecutionReport(func(event *asterspot.ExecutionReportEvent) {
		if !active() {
			return
		}
		if event == nil || asterCanonical(event.Symbol) != canonical {
			return
		}
		order, err := asterOrder(exchange.ProductSpot, canonical, event.OrderID, event.ClientOrderID, event.Side, event.OrderType, event.TimeInForce, event.Quantity, event.Price, event.CumulativeFilledQuantity, event.OrderStatus, false)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
	})
	return stop, nil
}
func (backend *asterSpotWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	_, canonical, err := asterSymbols(instrument, "WatchFills", exchange.ProductSpot)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeExecutionReport(func(event *asterspot.ExecutionReportEvent) {
		if !active() {
			return
		}
		if event == nil || event.TradeID <= 0 || asterCanonical(event.Symbol) != canonical {
			return
		}
		price, qty, err := parse2(event.LastExecutedPrice, event.LastExecutedQuantity)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchFills", err.Error()))
			return
		}
		fee, err := decimal.NewFromString(asterDefaultZero(event.CommissionAmount))
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchFills", err.Error()))
			return
		}
		side := exchange.SideBuy
		if event.Side == "SELL" {
			side = exchange.SideSell
		} else if event.Side != "BUY" {
			callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchFills", "unknown side"))
			return
		}
		feeAsset := ""
		if event.CommissionAsset != nil {
			feeAsset = *event.CommissionAsset
		}
		liquidity := exchange.LiquidityTaker
		if event.IsMaker {
			liquidity = exchange.LiquidityMaker
		}
		callbacks.Event(exchange.FillEvent{Kind: exchange.EventDelta, Fill: exchange.Fill{Instrument: canonical, OrderID: strconv.FormatInt(event.OrderID, 10), FillID: strconv.FormatInt(event.TradeID, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: feeAsset, Liquidity: liquidity, Time: time.UnixMilli(event.TransactionTime).UTC()}})
	})
	return stop, nil
}
func (backend *asterSpotWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeAccountPosition(func(event *asterspot.AccountPositionEvent) {
		if !active() {
			return
		}
		if event == nil {
			return
		}
		balances := make([]exchange.Balance, 0, len(event.Balances))
		for _, row := range event.Balances {
			free, locked, err := parse2(row.Free, row.Locked)
			if err != nil {
				callbacks.Error(asterMalformed(exchange.ProductSpot, "WatchBalances", err.Error()))
				return
			}
			balances = append(balances, exchange.Balance{Asset: row.Asset, Available: free, Locked: locked, Total: free.Add(locked)})
		}
		callbacks.Event(exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: balances, Time: time.UnixMilli(event.EventTime).UTC()})
	})
	return stop, nil
}
func (backend *asterSpotWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.PlaceOrder(ctx, req)
}
func (backend *asterSpotWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.CancelOrder(ctx, req)
}
func (backend *asterSpotWSBackend) Close() error {
	if backend.market != nil {
		backend.market.Close()
	}
	if backend.account != nil {
		backend.account.Close()
	}
	return nil
}

func (backend *asterPerpWSBackend) StartOrders(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.OrderEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	_, canonical, err := asterSymbols(instrument, "WatchOrders", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeOrderUpdate(func(event *asterperp.OrderUpdateEvent) {
		if !active() {
			return
		}
		if event == nil || asterCanonical(event.Order.Symbol) != canonical {
			return
		}
		order, err := asterOrder(exchange.ProductPerp, canonical, event.Order.OrderID, event.Order.ClientOrderID, event.Order.Side, event.Order.OrderType, event.Order.TimeInForce, event.Order.OriginalQty, event.Order.OriginalPrice, event.Order.AccumulatedFilledQty, event.Order.OrderStatus, event.Order.IsReduceOnly)
		if err != nil {
			callbacks.Error(err)
			return
		}
		callbacks.Event(exchange.OrderEvent{Kind: exchange.EventDelta, Order: order})
	})
	return stop, nil
}
func (backend *asterPerpWSBackend) StartFills(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.FillEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	_, canonical, err := asterSymbols(instrument, "WatchFills", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeOrderUpdate(func(event *asterperp.OrderUpdateEvent) {
		if !active() {
			return
		}
		if event == nil || event.Order.TradeID <= 0 || asterCanonical(event.Order.Symbol) != canonical {
			return
		}
		price, qty, err := parse2(event.Order.LastFilledPrice, event.Order.LastFilledQty)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchFills", err.Error()))
			return
		}
		fee, err := decimal.NewFromString(asterDefaultZero(event.Order.Commission))
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchFills", err.Error()))
			return
		}
		side := exchange.SideBuy
		if event.Order.Side == "SELL" {
			side = exchange.SideSell
		} else if event.Order.Side != "BUY" {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchFills", "unknown side"))
			return
		}
		liquidity := exchange.LiquidityTaker
		if event.Order.IsMaker {
			liquidity = exchange.LiquidityMaker
		}
		callbacks.Event(exchange.FillEvent{Kind: exchange.EventDelta, Fill: exchange.Fill{Instrument: canonical, OrderID: strconv.FormatInt(event.Order.OrderID, 10), FillID: strconv.FormatInt(event.Order.TradeID, 10), Side: side, Price: price, Quantity: qty, Fee: fee, FeeAsset: event.Order.CommissionAsset, Liquidity: liquidity, Time: time.UnixMilli(event.Order.TradeTime).UTC()}})
	})
	return stop, nil
}
func (backend *asterPerpWSBackend) StartBalances(ctx context.Context, callbacks streamCallbacks[exchange.BalanceEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeAccountUpdate(func(event *asterperp.AccountUpdateEvent) {
		if !active() {
			return
		}
		if event == nil {
			return
		}
		snapshot, err := backend.client.PerpAccount(context.Background())
		if err != nil {
			callbacks.Error(withExchangeOperation(err, "WatchBalances"))
			return
		}
		callbacks.Event(exchange.BalanceEvent{Kind: exchange.EventDelta, Balances: snapshot.Balances, Time: time.UnixMilli(event.EventTime).UTC()})
	})
	return stop, nil
}
func (backend *asterPerpWSBackend) StartPositions(ctx context.Context, instrument string, callbacks streamCallbacks[exchange.PositionEvent]) (func() error, error) {
	account, err := backend.ensureAccount(ctx)
	if err != nil {
		return nil, err
	}
	_, canonical, err := asterSymbols(instrument, "WatchPositions", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	active, stop := asterCallbackGate()
	account.SubscribeAccountUpdate(func(event *asterperp.AccountUpdateEvent) {
		if !active() {
			return
		}
		if event == nil {
			return
		}
		positions := make([]exchange.Position, 0, len(event.UpdateData.Positions))
		for _, row := range event.UpdateData.Positions {
			if asterCanonical(row.Symbol) != canonical {
				continue
			}
			qty, entry, err := parse2(row.PositionAmount, row.EntryPrice)
			if err != nil {
				callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchPositions", err.Error()))
				return
			}
			pnl, err := decimal.NewFromString(asterDefaultZero(row.UnrealizedPnL))
			if err != nil {
				callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchPositions", err.Error()))
				return
			}
			side := exchange.SideBuy
			if qty.IsNegative() || row.PositionSide == "SHORT" {
				side = exchange.SideSell
			}
			positions = append(positions, exchange.Position{Instrument: canonical, Side: side, Quantity: qty.Abs(), EntryPrice: entry, UnrealizedPnL: pnl})
		}
		callbacks.Event(exchange.PositionEvent{Kind: exchange.EventDelta, Positions: positions, Time: time.UnixMilli(event.EventTime).UTC()})
	})
	return stop, nil
}
func (backend *asterPerpWSBackend) StartReference(ctx context.Context, instrument string, callbacks streamCallbacks[perpReferenceEvent]) (func() error, error) {
	ws, err := backend.ensureMarket(ctx)
	if err != nil {
		return nil, err
	}
	native, canonical, err := asterSymbols(instrument, "WatchReference", exchange.ProductPerp)
	if err != nil {
		return nil, err
	}
	err = ws.SubscribeMarkPrice(native, "1s", func(event *asterperp.WsMarkPriceEvent) error {
		price, rate, err := parse2(event.MarkPrice, event.FundingRate)
		if err != nil {
			callbacks.Error(asterMalformed(exchange.ProductPerp, "WatchReference", err.Error()))
			return nil
		}
		callbacks.Event(perpReferenceEvent{MarkValid: true, FundingValid: true, MarkPrice: exchange.MarkPriceEvent{Instrument: canonical, Price: price, Time: time.UnixMilli(event.EventTime).UTC()}, FundingRate: exchange.FundingRateEvent{Instrument: canonical, Rate: rate, EffectiveAt: time.UnixMilli(event.EventTime).UTC(), NextAt: time.UnixMilli(event.NextFundingTime).UTC()}})
		return nil
	})
	return func() error { return ws.UnsubscribeMarkPrice(native, "1s") }, err
}
func (backend *asterPerpWSBackend) PlaceOrder(ctx context.Context, req exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.PlaceOrder(ctx, req)
}
func (backend *asterPerpWSBackend) CancelOrder(ctx context.Context, req exchange.CancelOrderRequest) (exchange.OrderAcknowledgement, error) {
	return backend.client.CancelOrder(ctx, req)
}
func (backend *asterPerpWSBackend) Close() error {
	if backend.market != nil {
		backend.market.Close()
	}
	if backend.account != nil {
		backend.account.Close()
	}
	return nil
}

var _ http.RoundTripper
