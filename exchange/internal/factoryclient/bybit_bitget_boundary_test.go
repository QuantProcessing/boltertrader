package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBybitBitgetValidRESTRequestsRejectNilContextBeforeNetwork(t *testing.T) {
	transport := &noSendTransport{}
	settings := Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}}
	spotClients := map[string]exchange.SpotClient{
		"bybit":  NewBybitSpot("key", "secret", settings),
		"bitget": NewBitgetSpot("key", "secret", "passphrase", settings),
	}
	for name, client := range spotClients {
		t.Run(name+"/spot", func(t *testing.T) {
			venue := exchange.Venue(name)
			instrument := bybitBitgetTestInstrument(venue, exchange.ProductSpot, "BTC-USDT")
			instrumentPtr := &instrument
			assertNilContextInvalid(t, map[string]error{
				"Instruments":  errorFromSlice(client.Instruments(nil)),
				"OrderBook":    errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{Instrument: instrument, Limit: 5})),
				"Candles":      errorFromValue(client.Candles(nil, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Limit: 1})),
				"PublicTrades": errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1})),
				"PlaceOrder":   errorFromValue(client.PlaceOrder(nil, validBoundaryOrder(instrument))),
				"CancelOrder":  errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{Instrument: instrument, OrderID: "11"})),
				"OpenOrders":   errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{Instrument: instrumentPtr, Limit: 1})),
				"OrderHistory": errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{Instrument: instrumentPtr, Limit: 1})),
				"Fills":        errorFromValue(client.Fills(nil, exchange.FillsRequest{Instrument: instrumentPtr, Limit: 1})),
				"Balances":     errorFromSlice(client.Balances(nil)),
				"SpotAccount":  errorFromValue(client.SpotAccount(nil)),
			})
		})
	}

	perpClients := map[string]exchange.PerpClient{
		"bybit":  NewBybitLinearPerp("key", "secret", "USDT", settings),
		"bitget": NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings),
	}
	for name, client := range perpClients {
		t.Run(name+"/perp", func(t *testing.T) {
			venue := exchange.Venue(name)
			instrument := bybitBitgetTestInstrument(venue, exchange.ProductPerp, "BTC-USDT")
			instrumentPtr := &instrument
			assertNilContextInvalid(t, map[string]error{
				"Instruments":        errorFromSlice(client.Instruments(nil)),
				"OrderBook":          errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{Instrument: instrument, Limit: 5})),
				"Candles":            errorFromValue(client.Candles(nil, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Limit: 1})),
				"PublicTrades":       errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1})),
				"PlaceOrder":         errorFromValue(client.PlaceOrder(nil, validBoundaryOrder(instrument))),
				"CancelOrder":        errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{Instrument: instrument, OrderID: "11"})),
				"OpenOrders":         errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{Instrument: instrumentPtr, Limit: 1})),
				"OrderHistory":       errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{Instrument: instrumentPtr, Limit: 1})),
				"Fills":              errorFromValue(client.Fills(nil, exchange.FillsRequest{Instrument: instrumentPtr, Limit: 1})),
				"Balances":           errorFromSlice(client.Balances(nil)),
				"PerpAccount":        errorFromValue(client.PerpAccount(nil)),
				"Positions":          errorFromSlice(client.Positions(nil, exchange.PositionsRequest{Instrument: instrumentPtr})),
				"FundingRate":        errorFromValue(client.FundingRate(nil, exchange.FundingRateRequest{Instrument: instrument})),
				"FundingRateHistory": errorFromValue(client.FundingRateHistory(nil, exchange.FundingRateHistoryRequest{Instrument: instrument, Limit: 1})),
				"SetLeverage":        errorFromValue(client.SetLeverage(nil, exchange.SetLeverageRequest{Instrument: instrument, Leverage: 2})),
			})
		})
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("valid nil-context requests made %d network calls", got)
	}
}

func TestBybitBitgetPerpClientsRejectQuoteSettlementMismatch(t *testing.T) {
	transport := &noSendTransport{}
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "ws://openapi.invalid",
		HTTPClient:        &http.Client{Transport: transport},
	}
	tests := []struct {
		name       string
		client     exchange.PerpClient
		instrument exchange.Instrument
	}{
		{name: "bybit/usdt", client: NewBybitLinearPerp("key", "secret", "USDT", settings), instrument: bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDC")},
		{name: "bybit/usdc", client: NewBybitLinearPerp("key", "secret", "USDC", settings), instrument: bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")},
		{name: "bitget/usdt", client: NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings), instrument: bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDC")},
		{name: "bitget/usdc", client: NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings), instrument: bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.client.OrderBook(context.Background(), exchange.OrderBookRequest{Instrument: test.instrument, Limit: 1})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("REST quote mismatch error = %v, want ErrInvalidRequest", err)
			}
			_, err = test.client.WebSocket().PlaceOrder(context.Background(), validBoundaryOrder(test.instrument))
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("WS quote mismatch error = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("quote mismatch requests made %d network calls", got)
	}
}

func TestBitgetFillsUsesVenueSymbolFilterBeforePagination(t *testing.T) {
	instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	settings := Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Path != "/api/v3/trade/fills" {
				return bitgetData(`{}`), nil
			}
			query := request.URL.Query()
			if got := query.Get("symbol"); got != "BTCUSDT" {
				return openAPIJSONResponse(`{"code":"40001","msg":"missing venue-side symbol filter: ` + got + `","data":{}}`), nil
			}
			if got := query.Get("cursor"); got != "" {
				return openAPIJSONResponse(`{"code":"40002","msg":"unexpected second page: ` + got + `","data":{}}`), nil
			}
			return bitgetData(`{"list":[{"orderId":"10","clientOid":"100","execId":"e1","symbol":"BTCUSDT","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}],"cursor":"next"}`), nil
		})},
	}
	settings = bybitBitgetStaticCatalogSettings(t, settings, exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)

	page, err := client.Fills(context.Background(), exchange.FillsRequest{Instrument: &instrument, Limit: 1})
	if err != nil {
		t.Fatalf("Fills: %v", err)
	}
	if len(page.Fills) != 1 || page.Fills[0].NativeSymbol != "BTCUSDT" || !page.Page.HasMore {
		t.Fatalf("fills page = %+v", page)
	}
}

func TestConvertBitgetPositionsPreservesNegativePnLAndSkipsZeroQuantity(t *testing.T) {
	descriptor := exchange.SourceDescriptor{Name: "bitget-position-test", Endpoint: "https://identity.invalid", WebSocketEndpoint: "wss://identity.invalid/ws", Transport: "test"}
	scope := exchange.CatalogScope{Venue: exchange.VenueBitget, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, EndpointFingerprint: descriptor.Fingerprint()}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: []exchange.Instrument{{
		Identity: exchange.InstrumentIdentity{
			Venue:        exchange.VenueBitget,
			Product:      exchange.ProductPerp,
			Symbol:       "BTC-USDT",
			BaseAsset:    "BTC",
			QuoteAsset:   "USDT",
			SettleAsset:  "USDT",
			NativeSymbol: "BTCUSDT",
			NativeID:     "btc-native-id",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.1"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
	}}}}); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	meta := clientMeta{venue: exchange.VenueBitget, product: exchange.ProductPerp, catalog: catalog, catalogScope: scope}
	rows := []bitget.PositionRecord{
		{Symbol: "ETHUSDT", PosSide: "long", Qty: "0", AverageOpenPrice: "99", UnrealisedPnl: "0"},
		{Symbol: "BTCUSDT", PosSide: "long", Qty: "1", AverageOpenPrice: "99", MarkPrice: "100", LiquidationPrice: "50", Leverage: "5", UnrealisedPnl: "-1.25"},
	}

	positions, err := convertBitgetPositions(context.Background(), meta, "Positions", rows, "")
	if err != nil {
		t.Fatalf("convertBitgetPositions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions = %+v, want one non-zero row", positions)
	}
	if !positions[0].UnrealizedPnL.Equal(decimal.RequireFromString("-1.25")) {
		t.Fatalf("unrealized pnl = %s, want -1.25", positions[0].UnrealizedPnL)
	}
}

func TestBybitUSDCNormalizedInstrumentUsesNativePerpetualSymbol(t *testing.T) {
	client := NewBybitLinearPerp("key", "secret", "USDC", Settings{}).(*bybitPerpClient)
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDC")
	native, canonical, err := client.symbols(instrument, "OrderBook")
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if native != "BTCPERP" || canonical.Identity.Symbol != "BTC-USDC" {
		t.Fatalf("symbols = (%q, %q), want (BTCPERP, BTC-USDC)", native, canonical.Identity.Symbol)
	}
}

func TestBitgetUSDCNormalizedInstrumentUsesNativePerpetualSymbol(t *testing.T) {
	client := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", Settings{}).(*bitgetPerpClient)
	instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDC")
	native, canonical, err := client.symbols(instrument, "OrderBook")
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if native != "BTCPERP" || canonical.Identity.Symbol != "BTC-USDC" {
		t.Fatalf("symbols = (%q, %q), want (BTCPERP, BTC-USDC)", native, canonical.Identity.Symbol)
	}
}

func TestFundingHistoryRequestSemanticsAreTruthful(t *testing.T) {
	t.Run("Bybit wires supported window and rejects cursor", func(t *testing.T) {
		var query string
		client := NewBybitLinearPerp("key", "secret", "USDT", Settings{
			Endpoint: "https://openapi.invalid",
			HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				query = request.URL.RawQuery
				return bybitData(`{"category":"linear","list":[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateTimestamp":"1720000000000"}]}`), nil
			})},
		})
		start := time.UnixMilli(1719990000000)
		end := start.Add(time.Hour)
		instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
		if _, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: instrument, Start: start, End: end, Limit: 7}); err != nil {
			t.Fatalf("FundingRateHistory: %v", err)
		}
		for _, want := range []string{"startTime=1719990000000", "endTime=1719993600000", "limit=7"} {
			if !strings.Contains(query, want) {
				t.Fatalf("Bybit funding query %q missing %q", query, want)
			}
		}
		_, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: instrument, Cursor: "next"})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("Bybit cursor error = %v, want ErrInvalidRequest", err)
		}
	})

	t.Run("Bitget rejects unsupported cursor before network", func(t *testing.T) {
		transport := &noSendTransport{}
		client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
		instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
		_, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{
			Instrument: instrument, Cursor: "next",
		})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("error = %v, want ErrInvalidRequest", err)
		}
		if got := transport.calls.Load(); got != 0 {
			t.Fatalf("unsupported Bitget funding cursor made %d network calls", got)
		}
	})
}

func TestBitgetFundingHistoryPaginatesAndFiltersPortableWindow(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * 24 * time.Hour)
	calls := 0
	client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.Path != "/api/v2/mix/market/history-fund-rate" {
				t.Fatalf("unexpected path %s", request.URL.Path)
			}
			if got := request.URL.Query().Get("pageSize"); got != "100" {
				t.Fatalf("pageSize = %q, want 100", got)
			}
			if got := request.URL.Query().Get("pageNo"); got != fmt.Sprint(calls) {
				t.Fatalf("pageNo = %q, want %d", got, calls)
			}
			rows := make([]string, 0, 100)
			switch calls {
			case 1:
				for index := 0; index < 100; index++ {
					timestamp := end.Add(time.Duration(100-index) * time.Hour).UnixMilli()
					rows = append(rows, fmt.Sprintf(`{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":"%d"}`, timestamp))
				}
			case 2:
				rows = append(rows,
					fmt.Sprintf(`{"symbol":"BTCUSDT","fundingRate":"0.0002","fundingTime":"%d"}`, end.Add(-time.Hour).UnixMilli()),
					fmt.Sprintf(`{"symbol":"BTCUSDT","fundingRate":"0.0003","fundingTime":"%d"}`, start.Add(-time.Hour).UnixMilli()),
				)
			default:
				t.Fatalf("unexpected funding history page %d", calls)
			}
			return bitgetData("[" + strings.Join(rows, ",") + "]"), nil
		})},
	})
	instrument := bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	page, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{
		Instrument: instrument, Start: start, End: end, Limit: 10,
	})
	if err != nil {
		t.Fatalf("FundingRateHistory: %v", err)
	}
	if calls != 2 {
		t.Fatalf("history calls = %d, want 2", calls)
	}
	if len(page.Rates) != 1 || !page.Rates[0].FundingTime.Equal(end.Add(-time.Hour)) {
		t.Fatalf("filtered rates = %+v, want the one rate inside the portable window", page.Rates)
	}
	if !page.Page.WindowStart.Equal(start) || !page.Page.WindowEnd.Equal(end) || page.Page.Limit != 10 {
		t.Fatalf("page metadata = %+v", page.Page)
	}
}

func TestRequiredRESTTimestampsReturnMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		call func(http.RoundTripper) error
	}{
		{
			name: "bybit order book",
			call: func(transport http.RoundTripper) error {
				client := NewBybitSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.OrderBook(context.Background(), exchange.OrderBookRequest{Instrument: bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT")})
				return err
			},
		},
		{
			name: "bitget public trade",
			call: func(transport http.RoundTripper) error {
				client := NewBitgetSpot("key", "secret", "passphrase", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.PublicTrades(context.Background(), exchange.PublicTradesRequest{Instrument: bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT")})
				return err
			},
		},
		{
			name: "bybit candle",
			call: func(transport http.RoundTripper) error {
				client := NewBybitSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.Candles(context.Background(), exchange.CandlesRequest{Instrument: bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductSpot, "BTC-USDT"), Interval: "1m"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v5/market/orderbook":
					return bybitData(`{"s":"BTCUSDT","b":[["99","1"]],"a":[["101","1"]],"ts":0,"u":1}`), nil
				case "/api/v3/market/fills":
					return bitgetData(`[{"execId":"1","price":"100","size":"1","side":"buy","ts":"bad"}]`), nil
				case "/v5/market/kline":
					return bybitData(`{"category":"spot","symbol":"BTCUSDT","list":[["bad","100","101","99","100","1","100"]]}`), nil
				default:
					t.Fatalf("unexpected path %s", request.URL.Path)
					return nil, nil
				}
			})
			assertExchangeErrorKind(t, test.call(transport), exchange.KindMalformedResponse)
		})
	}
}

func TestWebSocketReferenceRejectsMalformedRequiredTimestamps(t *testing.T) {
	meta := clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}
	instrument := bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	_, err := strictReference(meta, "WatchReference", instrument, "100", "0.0001", "", "bad", time.UnixMilli(1720000000000))
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)

	_, err = strictReference(meta, "WatchReference", instrument, "100", "", "", "", time.Time{})
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)
}

func validBoundaryOrder(instrument exchange.Instrument) exchange.PlaceOrderRequest {
	return exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
		ReduceOnly:    instrument.Identity.Product == exchange.ProductPerp,
	}
}

func assertNilContextInvalid(t *testing.T, results map[string]error) {
	t.Helper()
	for operation, err := range results {
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Errorf("%s error = %v, want ErrInvalidRequest", operation, err)
		}
	}
}
