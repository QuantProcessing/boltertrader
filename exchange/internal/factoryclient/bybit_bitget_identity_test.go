package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
)

func TestBybitBitgetRESTLifecycleUnknownNativeResolution(t *testing.T) {
	for _, test := range []struct {
		name      string
		venue     exchange.Venue
		client    func(Settings) exchange.PerpClient
		transport http.RoundTripper
	}{
		{
			name:  "bybit",
			venue: exchange.VenueBybit,
			client: func(settings Settings) exchange.PerpClient {
				return NewBybitLinearPerp("key", "secret", "USDT", settings)
			},
			transport: bybitUnknownLifecycleRouter{},
		},
		{
			name:  "bitget",
			venue: exchange.VenueBitget,
			client: func(settings Settings) exchange.PerpClient {
				return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
			},
			transport: bitgetUnknownLifecycleRouter{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			catalog, scope := bybitBitgetIdentityCatalog(t, test.venue, exchange.ProductPerp)
			client := test.client(Settings{Endpoint: "https://identity.invalid", InstrumentCatalog: catalog, CatalogScope: scope, HTTPClient: &http.Client{Transport: test.transport}})
			btc := bybitBitgetTestInstrument(test.venue, exchange.ProductPerp, "BTC-USDT")
			eth := bybitBitgetTestInstrument(test.venue, exchange.ProductPerp, "ETH-USDT")

			for _, run := range []struct {
				name string
				call func(*exchange.Instrument) error
			}{
				{name: "orders", call: func(instrument *exchange.Instrument) error {
					_, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 10})
					return err
				}},
				{name: "fills", call: func(instrument *exchange.Instrument) error {
					_, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, Limit: 10})
					return err
				}},
				{name: "positions", call: func(instrument *exchange.Instrument) error {
					_, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: instrument})
					return err
				}},
			} {
				t.Run(run.name+"/account-wide", func(t *testing.T) {
					if err := run.call(nil); !errors.Is(err, exchange.ErrMetadataResolution) {
						t.Fatalf("account-wide error = %v, want ErrMetadataResolution", err)
					}
				})
				t.Run(run.name+"/filter-skips-unrelated", func(t *testing.T) {
					if err := run.call(&btc); err != nil {
						t.Fatalf("BTC filter error = %v, want nil", err)
					}
				})
				t.Run(run.name+"/filter-matching-unknown-fails", func(t *testing.T) {
					if err := run.call(&eth); !errors.Is(err, exchange.ErrMetadataResolution) {
						t.Fatalf("ETH filter error = %v, want ErrMetadataResolution", err)
					}
				})
			}
		})
	}
}

func TestBybitBitgetRESTLifecycleUsesCatalogIdentity(t *testing.T) {
	for _, test := range []struct {
		name      string
		venue     exchange.Venue
		client    func(Settings) exchange.PerpClient
		transport http.RoundTripper
	}{
		{
			name:  "bybit",
			venue: exchange.VenueBybit,
			client: func(settings Settings) exchange.PerpClient {
				return NewBybitLinearPerp("key", "secret", "USDT", settings)
			},
			transport: bybitUnknownLifecycleRouter{},
		},
		{
			name:  "bitget",
			venue: exchange.VenueBitget,
			client: func(settings Settings) exchange.PerpClient {
				return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
			},
			transport: bitgetUnknownLifecycleRouter{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			catalog, scope := bybitBitgetIdentityCatalog(t, test.venue, exchange.ProductPerp)
			client := test.client(Settings{Endpoint: "https://identity.invalid", InstrumentCatalog: catalog, CatalogScope: scope, HTTPClient: &http.Client{Transport: test.transport}})
			instrument := bybitBitgetTestInstrument(test.venue, exchange.ProductPerp, "BTC-USDT")

			orders, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: &instrument, Limit: 10})
			if err != nil {
				t.Fatalf("OpenOrders: %v", err)
			}
			if len(orders.Orders) != 1 || orders.Orders[0].NativeSymbol != "BTCUSDT" || orders.Orders[0].NativeID != "catalog-native-id" {
				t.Fatalf("OpenOrders identity = %+v", orders.Orders)
			}

			fills, err := client.Fills(ctx, exchange.FillsRequest{Instrument: &instrument, Limit: 10})
			if err != nil {
				t.Fatalf("Fills: %v", err)
			}
			if len(fills.Fills) != 1 || fills.Fills[0].NativeSymbol != "BTCUSDT" || fills.Fills[0].NativeID != "catalog-native-id" {
				t.Fatalf("Fills identity = %+v", fills.Fills)
			}

			positions, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: &instrument})
			if err != nil {
				t.Fatalf("Positions: %v", err)
			}
			if len(positions) != 1 || positions[0].NativeSymbol != "BTCUSDT" || positions[0].NativeID != "catalog-native-id" {
				t.Fatalf("Positions identity = %+v", positions)
			}
		})
	}
}

func TestBybitBitgetRESTLifecycleRequiresConfiguredCatalog(t *testing.T) {
	for _, test := range []struct {
		name      string
		client    func(Settings) exchange.PerpClient
		transport http.RoundTripper
	}{
		{
			name: "bybit",
			client: func(settings Settings) exchange.PerpClient {
				return NewBybitLinearPerp("key", "secret", "USDT", settings)
			},
			transport: bybitUnknownLifecycleRouter{},
		},
		{
			name: "bitget",
			client: func(settings Settings) exchange.PerpClient {
				return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
			},
			transport: bitgetUnknownLifecycleRouter{},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			client := test.client(Settings{Endpoint: "https://identity.invalid", HTTPClient: &http.Client{Transport: test.transport}})

			for _, run := range []struct {
				name string
				call func() error
			}{
				{name: "orders", call: func() error {
					_, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Limit: 10})
					return err
				}},
				{name: "fills", call: func() error {
					_, err := client.Fills(ctx, exchange.FillsRequest{Limit: 10})
					return err
				}},
				{name: "positions", call: func() error {
					_, err := client.Positions(ctx, exchange.PositionsRequest{})
					return err
				}},
			} {
				t.Run(run.name, func(t *testing.T) {
					if err := run.call(); !errors.Is(err, exchange.ErrMetadataResolution) {
						t.Fatalf("%s error = %v, want ErrMetadataResolution", run.name, err)
					}
				})
			}
		})
	}
}

func TestBybitBitgetRESTAckIdentityBinding(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name      string
		venue     exchange.Venue
		client    func(Settings) exchange.SpotClient
		transport http.RoundTripper
		operation exchange.OrderOperation
	}{
		{
			name:      "bybit/place client mismatch",
			venue:     exchange.VenueBybit,
			client:    func(settings Settings) exchange.SpotClient { return NewBybitSpot("key", "secret", settings) },
			transport: bybitAckMismatchRouter{placeOrderID: "11", placeClientID: "wrong-client", cancelOrderID: "11", cancelClientID: ""},
			operation: exchange.OrderOperationPlace,
		},
		{
			name:      "bybit/cancel order mismatch",
			venue:     exchange.VenueBybit,
			client:    func(settings Settings) exchange.SpotClient { return NewBybitSpot("key", "secret", settings) },
			transport: bybitAckMismatchRouter{placeOrderID: "11", placeClientID: "401", cancelOrderID: "12", cancelClientID: ""},
			operation: exchange.OrderOperationCancel,
		},
		{
			name:  "bitget/place client mismatch",
			venue: exchange.VenueBitget,
			client: func(settings Settings) exchange.SpotClient {
				return NewBitgetSpot("key", "secret", "passphrase", settings)
			},
			transport: bitgetAckMismatchRouter{placeOrderID: "11", placeClientID: "wrong-client", cancelOrderID: "11", cancelClientID: ""},
			operation: exchange.OrderOperationPlace,
		},
		{
			name:  "bitget/cancel order mismatch",
			venue: exchange.VenueBitget,
			client: func(settings Settings) exchange.SpotClient {
				return NewBitgetSpot("key", "secret", "passphrase", settings)
			},
			transport: bitgetAckMismatchRouter{placeOrderID: "11", placeClientID: "401", cancelOrderID: "12", cancelClientID: ""},
			operation: exchange.OrderOperationCancel,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			catalog, scope := bybitBitgetIdentityCatalog(t, test.venue, exchange.ProductSpot)
			client := test.client(Settings{Endpoint: "https://ack.invalid", InstrumentCatalog: catalog, CatalogScope: scope, HTTPClient: &http.Client{Transport: test.transport}})
			instrument := bybitBitgetTestInstrument(test.venue, exchange.ProductSpot, "BTC-USDT")
			place := exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "401", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting}
			if test.operation == exchange.OrderOperationPlace {
				if _, err := client.PlaceOrder(ctx, place); !errors.Is(err, exchange.ErrMalformedResponse) {
					t.Fatalf("PlaceOrder mismatch error = %v, want ErrMalformedResponse", err)
				}
				return
			}
			if _, err := client.PlaceOrder(ctx, place); err != nil {
				t.Fatalf("PlaceOrder setup: %v", err)
			}
			if _, err := client.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument, OrderID: "11"}); !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("CancelOrder mismatch error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestBybitBitgetPrivateWSAckIdentityBinding(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name      string
		venue     exchange.Venue
		settings  func(string) Settings
		client    func(Settings) exchange.SpotClient
		transport http.RoundTripper
	}{
		{
			name:  "bybit",
			venue: exchange.VenueBybit,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://ack-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bybitBitgetNoSendTransport{}}}
			},
			client: func(settings Settings) exchange.SpotClient { return NewBybitSpot("key", "secret", settings) },
		},
		{
			name:  "bitget",
			venue: exchange.VenueBitget,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://ack-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bitgetWSAuxTransport{}}}
			},
			client: func(settings Settings) exchange.SpotClient {
				return NewBitgetSpot("key", "secret", "passphrase", settings)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			wsURL, closeServer := newBybitBitgetAckMismatchWSServer(t, test.venue)
			defer closeServer()
			catalog, scope := bybitBitgetIdentityCatalog(t, test.venue, exchange.ProductSpot)
			settings := test.settings(wsURL)
			settings.InstrumentCatalog = catalog
			settings.CatalogScope = scope
			client := test.client(settings)
			instrument := bybitBitgetTestInstrument(test.venue, exchange.ProductSpot, "BTC-USDT")
			if _, err := client.WebSocket().PlaceOrder(ctx, exchange.PlaceOrderRequest{Instrument: instrument, ClientOrderID: "401", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting}); !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("WS PlaceOrder mismatch error = %v, want ErrMalformedResponse", err)
			}
			if _, err := client.WebSocket().CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: instrument, OrderID: "11"}); !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("WS CancelOrder mismatch error = %v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestBybitBitgetPrivateWSLifecycleRequiresConfiguredCatalog(t *testing.T) {
	for _, test := range []struct {
		name     string
		venue    exchange.Venue
		settings func(string) Settings
		client   func(Settings) exchange.PerpClient
	}{
		{
			name:  "bybit",
			venue: exchange.VenueBybit,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://lifecycle-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bybitBitgetNoSendTransport{}}}
			},
			client: func(settings Settings) exchange.PerpClient {
				return NewBybitLinearPerp("key", "secret", "USDT", settings)
			},
		},
		{
			name:  "bitget",
			venue: exchange.VenueBitget,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://lifecycle-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bitgetWSAuxTransport{}}}
			},
			client: func(settings Settings) exchange.PerpClient {
				return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			wsURL, closeServer := newBybitBitgetUnknownLifecycleWSServer(t, test.venue, "BTCUSDT")
			defer closeServer()
			client := test.client(test.settings(wsURL))
			filter := bybitBitgetTestInstrument(test.venue, exchange.ProductPerp, "BTC-USDT")

			orders, err := client.WebSocket().WatchOrders(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchOrders: %v", err)
			}
			defer orders.Close()
			assertStreamErrorIs(t, orders.Errors(), exchange.ErrMetadataResolution, "orders")

			fills, err := client.WebSocket().WatchFills(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchFills: %v", err)
			}
			defer fills.Close()
			assertStreamErrorIs(t, fills.Errors(), exchange.ErrMetadataResolution, "fills")

			positions, err := client.WebSocket().WatchPositions(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchPositions: %v", err)
			}
			defer positions.Close()
			assertStreamErrorIs(t, positions.Errors(), exchange.ErrMetadataResolution, "positions")
		})
	}
}

func TestBybitBitgetPrivateWSLifecycleUnknownNativeResolution(t *testing.T) {
	for _, test := range []struct {
		name     string
		venue    exchange.Venue
		settings func(string) Settings
		client   func(Settings) exchange.PerpClient
	}{
		{
			name:  "bybit",
			venue: exchange.VenueBybit,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://lifecycle-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bybitBitgetNoSendTransport{}}}
			},
			client: func(settings Settings) exchange.PerpClient {
				return NewBybitLinearPerp("key", "secret", "USDT", settings)
			},
		},
		{
			name:  "bitget",
			venue: exchange.VenueBitget,
			settings: func(wsURL string) Settings {
				return Settings{Endpoint: "https://lifecycle-ws.invalid", WebSocketEndpoint: wsURL, HTTPClient: &http.Client{Transport: &bitgetWSAuxTransport{}}}
			},
			client: func(settings Settings) exchange.PerpClient {
				return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			wsURL, closeServer := newBybitBitgetUnknownLifecycleWSServer(t, test.venue, "ETHUSDT")
			defer closeServer()
			catalog, scope := bybitBitgetIdentityCatalog(t, test.venue, exchange.ProductPerp)
			settings := test.settings(wsURL)
			settings.InstrumentCatalog = catalog
			settings.CatalogScope = scope
			client := test.client(settings)
			filter := bybitBitgetTestInstrument(test.venue, exchange.ProductPerp, "ETH-USDT")
			orders, err := client.WebSocket().WatchOrders(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchOrders: %v", err)
			}
			defer orders.Close()
			assertStreamErrorIs(t, orders.Errors(), exchange.ErrMetadataResolution, "orders")

			fills, err := client.WebSocket().WatchFills(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchFills: %v", err)
			}
			defer fills.Close()
			assertStreamErrorIs(t, fills.Errors(), exchange.ErrMetadataResolution, "fills")

			positions, err := client.WebSocket().WatchPositions(context.Background(), exchange.WatchRequest{Instrument: filter})
			if err != nil {
				t.Fatalf("WatchPositions: %v", err)
			}
			defer positions.Close()
			assertStreamErrorIs(t, positions.Errors(), exchange.ErrMetadataResolution, "positions")
		})
	}
}

func bybitBitgetIdentityCatalog(t *testing.T, venue exchange.Venue, product exchange.Product) (exchange.InstrumentCatalog, exchange.CatalogScope) {
	t.Helper()
	descriptor := exchange.SourceDescriptor{Name: string(venue) + "-identity", Endpoint: "https://identity.invalid", WebSocketEndpoint: "wss://identity.invalid/ws", Transport: "test"}
	scope := exchange.CatalogScope{Venue: venue, Environment: exchange.EnvironmentLive, Product: product, EndpointFingerprint: descriptor.Fingerprint()}
	loader := &mutableBybitBitgetInstrumentLoader{snapshot: exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: []exchange.Instrument{
		bybitBitgetIdentityInstrument(venue, product, "BTC", "USDT", "catalog-native-id"),
	}}}
	catalog := exchange.NewDefaultMemoryCatalog()
	if err := catalog.RegisterSource(scope, descriptor, loader); err != nil {
		t.Fatalf("RegisterSource: %v", err)
	}
	if _, err := catalog.Refresh(context.Background(), scope); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return catalog, scope
}

func assertStreamErrorIs(t *testing.T, errorsCh <-chan error, want error, label string) {
	t.Helper()
	select {
	case err := <-errorsCh:
		if !errors.Is(err, want) {
			t.Fatalf("%s stream error = %v, want %v", label, err, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timeout waiting for %s stream error", label)
	}
}

type bybitBitgetNoSendTransport struct{}

func (*bybitBitgetNoSendTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusInternalServerError, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unexpected REST call"}`))}, nil
}

type bybitUnknownLifecycleRouter struct{}

func (bybitUnknownLifecycleRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/v5/order/realtime":
		return bybitData(`{"list":[` + bybitOrderJSON("11", "101", "New", false) + `,` + strings.ReplaceAll(bybitOrderJSON("12", "102", "New", false), `"symbol":"BTCUSDT"`, `"symbol":"ETHUSDT"`) + `]}`), nil
	case "/v5/execution/list":
		return bybitData(`{"list":[{"execId":"e1","orderId":"11","orderLinkId":"101","symbol":"BTCUSDT","side":"Buy","execPrice":"99","execQty":"1","execFee":"0.01","feeCurrency":"USDT","isMaker":false,"execTime":"1720000000000"},{"execId":"e2","orderId":"12","orderLinkId":"102","symbol":"ETHUSDT","side":"Buy","execPrice":"99","execQty":"1","execFee":"0.01","feeCurrency":"USDT","isMaker":false,"execTime":"1720000000000"}]}`), nil
	case "/v5/position/list":
		return bybitData(`{"list":[{"category":"linear","symbol":"BTCUSDT","side":"Buy","size":"1","avgPrice":"99","leverage":"5","unrealisedPnl":"1","liqPrice":"50"},{"category":"linear","symbol":"ETHUSDT","side":"Buy","size":"1","avgPrice":"99","leverage":"5","unrealisedPnl":"1","liqPrice":"50"}]}`), nil
	default:
		return bybitData(`{}`), nil
	}
}

type bitgetUnknownLifecycleRouter struct{}

func (bitgetUnknownLifecycleRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/api/v3/trade/unfilled-orders":
		return bitgetData(`{"list":[` + bitgetOrderJSON("11", "101", "live", false) + `,` + strings.ReplaceAll(bitgetOrderJSON("12", "102", "live", false), `"symbol":"BTCUSDT"`, `"symbol":"ETHUSDT"`) + `],"cursor":""}`), nil
	case "/api/v3/trade/fills":
		return bitgetData(`{"list":[{"orderId":"11","clientOid":"101","execId":"e1","symbol":"BTCUSDT","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"},{"orderId":"12","clientOid":"102","execId":"e2","symbol":"ETHUSDT","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}],"cursor":""}`), nil
	case "/api/v3/position/current-position":
		return bitgetData(`{"list":[{"symbol":"BTCUSDT","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"1"},{"symbol":"ETHUSDT","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"1"}]}`), nil
	default:
		return bitgetData(`{}`), nil
	}
}

type bybitAckMismatchRouter struct {
	placeOrderID, placeClientID   string
	cancelOrderID, cancelClientID string
	placeSymbol, cancelSymbol     string
}

func (router bybitAckMismatchRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/v5/order/create":
		return bybitData(`{"orderId":"` + router.placeOrderID + `","orderLinkId":"` + router.placeClientID + `","symbol":"` + router.placeSymbol + `"}`), nil
	case "/v5/order/cancel":
		return bybitData(`{"orderId":"` + router.cancelOrderID + `","orderLinkId":"` + router.cancelClientID + `","symbol":"` + router.cancelSymbol + `"}`), nil
	default:
		return bybitOpenAPIRouter{}.RoundTrip(request)
	}
}

type bitgetAckMismatchRouter struct {
	placeOrderID, placeClientID   string
	cancelOrderID, cancelClientID string
	placeSymbol, cancelSymbol     string
}

func (router bitgetAckMismatchRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/api/v3/trade/place-order":
		return bitgetData(`{"orderId":"` + router.placeOrderID + `","clientOid":"` + router.placeClientID + `","symbol":"` + router.placeSymbol + `"}`), nil
	case "/api/v3/trade/cancel-order":
		return bitgetData(`{"orderId":"` + router.cancelOrderID + `","clientOid":"` + router.cancelClientID + `","symbol":"` + router.cancelSymbol + `"}`), nil
	default:
		return bitgetOpenAPIRouter{}.RoundTrip(request)
	}
}

func newBybitBitgetAckMismatchWSServer(t *testing.T, venue exchange.Venue) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if string(payload) == "ping" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			if venue == exchange.VenueBybit {
				var req bybitScriptedWSRequest
				_ = jsonUnmarshal(payload, &req)
				if req.Op == "auth" {
					_ = conn.WriteJSON(map[string]any{"op": "auth", "success": true, "retCode": 0})
					continue
				}
				if req.Op == "order.create" {
					_ = conn.WriteJSON(map[string]any{"reqId": req.TradeReqID, "retCode": 0, "retMsg": "OK", "op": req.Op, "data": map[string]string{"orderId": "11", "orderLinkId": "wrong-client"}})
					continue
				}
				if req.Op == "order.cancel" {
					_ = conn.WriteJSON(map[string]any{"reqId": req.TradeReqID, "retCode": 0, "retMsg": "OK", "op": req.Op, "data": map[string]string{"orderId": "12", "orderLinkId": ""}})
					return
				}
				continue
			}
			var req struct {
				Op       string `json:"op"`
				ID       string `json:"id"`
				Category string `json:"category"`
				Topic    string `json:"topic"`
			}
			_ = jsonUnmarshal(payload, &req)
			if req.Op == "login" {
				_ = conn.WriteJSON(map[string]any{"event": "login", "code": "0"})
				continue
			}
			if req.Op == "trade" {
				if req.Topic == "place-order" {
					_ = conn.WriteJSON(map[string]any{"event": "trade", "id": req.ID, "category": req.Category, "topic": req.Topic, "code": "0", "msg": "success", "args": []map[string]string{{"orderId": "11", "clientOid": "wrong-client"}}})
					continue
				}
				_ = conn.WriteJSON(map[string]any{"event": "trade", "id": req.ID, "category": req.Category, "topic": req.Topic, "code": "0", "msg": "success", "args": []map[string]string{{"orderId": "12", "clientOid": ""}}})
				return
			}
		}
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), server.Close
}

func newBybitBitgetUnknownLifecycleWSServer(t *testing.T, venue exchange.Venue, native string) (string, func()) {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		for {
			_, payload, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if string(payload) == "ping" {
				_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
				continue
			}
			if venue == exchange.VenueBybit {
				var req bybitScriptedWSRequest
				_ = jsonUnmarshal(payload, &req)
				if req.Op == "auth" {
					_ = conn.WriteJSON(map[string]any{"op": "auth", "success": true, "retCode": 0})
					continue
				}
				if req.Op == "subscribe" {
					topic := ""
					if len(req.Args) > 0 {
						_ = jsonUnmarshal(req.Args[0], &topic)
					}
					_ = conn.WriteJSON(map[string]any{"op": "subscribe", "req_id": req.ReqID, "success": true})
					switch {
					case strings.Contains(topic, "execution"):
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"topic":"execution","data":[{"execId":"e2","orderId":"12","orderLinkId":"102","symbol":"`+native+`","side":"Buy","execPrice":"99","execQty":"1","execFee":"0.01","feeCurrency":"USDT","isMaker":false,"execTime":"1720000000000"}]}`))
					case strings.Contains(topic, "position"):
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"topic":"position","data":[{"category":"linear","symbol":"`+native+`","side":"Buy","size":"1","avgPrice":"99","leverage":"5","unrealisedPnl":"1","liqPrice":"50"}]}`))
					default:
						_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"topic":"order","data":[{"orderId":"12","orderLinkId":"102","symbol":"`+native+`","side":"Buy","orderType":"Limit","timeInForce":"GTC","price":"99","qty":"1","cumExecQty":"0","orderStatus":"New","createdTime":"1720000000000","updatedTime":"1720000001000"}]}`))
					}
					return
				}
				continue
			}
			var req struct {
				Op   string `json:"op"`
				Args []struct {
					Topic string `json:"topic"`
				} `json:"args"`
			}
			_ = jsonUnmarshal(payload, &req)
			if req.Op == "login" {
				_ = conn.WriteJSON(map[string]any{"event": "login", "code": "0"})
				continue
			}
			if req.Op == "subscribe" && len(req.Args) > 0 {
				arg := map[string]string{"instType": "UTA", "topic": req.Args[0].Topic, "symbol": ""}
				_ = conn.WriteJSON(map[string]any{"event": "subscribe", "code": "0", "arg": arg})
				switch req.Args[0].Topic {
				case "fill":
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"arg":{"instType":"UTA","topic":"fill","symbol":""},"data":[{"orderId":"12","clientOid":"102","execId":"e2","symbol":"`+native+`","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}]}`))
				case "position":
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"arg":{"instType":"UTA","topic":"position","symbol":""},"data":[{"symbol":"`+native+`","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"1"}]}`))
				default:
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"arg":{"instType":"UTA","topic":"order","symbol":""},"data":[{"orderId":"12","clientOid":"102","symbol":"`+native+`","side":"buy","orderType":"limit","timeInForce":"gtc","price":"99","qty":"1","filledQty":"0","orderStatus":"live","cTime":"1720000000000","uTime":"1720000001000"}]}`))
				}
				continue
			}
		}
	}))
	return "ws" + strings.TrimPrefix(server.URL, "http"), server.Close
}

func bybitBitgetIdentityInstrument(venue exchange.Venue, product exchange.Product, base, quote, nativeID string) exchange.Instrument {
	instrument := bybitBitgetCatalogTestInstrument(venue, product, base, quote, quote, decimal.RequireFromString("0.001"))
	if product == exchange.ProductSpot {
		instrument.Identity.SettleAsset = ""
	}
	instrument.Identity.NativeID = nativeID
	return instrument
}

func jsonUnmarshal(payload []byte, v any) error {
	return json.NewDecoder(io.NopCloser(strings.NewReader(string(payload)))).Decode(v)
}
