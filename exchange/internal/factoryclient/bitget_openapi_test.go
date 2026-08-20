package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestOpenAPIBitgetRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()
	settings := Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: bitgetOpenAPIRouter{}}}

	spotSettings := bybitBitgetStaticCatalogSettings(t, settings, exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT")
	spot := NewBitgetSpot("key", "secret", "passphrase", spotSettings)
	spotInstrument := assertSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, spotInstrument)
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: spotInstrument, OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, spotInstrument)

	perpSettings := bybitBitgetStaticCatalogSettings(t, settings, exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	perp := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", perpSettings)
	perpInstrument := assertPerpReadMatrix(t, ctx, perp, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, perpInstrument)
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: perpInstrument, OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrixWithFundingRequest(t, ctx, perp, perpInstrument, exchange.FundingRateHistoryRequest{Instrument: perpInstrument, Limit: 10})
}

func TestBitgetFillsScopesNativeSymbolOnlyWhenInstrumentProvided(t *testing.T) {
	ctx := context.Background()
	sawScoped := false
	sawAccountWide := false
	settings := bybitBitgetStaticCatalogSettings(t, Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/trade/fills":
				symbol := request.URL.Query().Get("symbol")
				if request.URL.Query().Get("orderId") == "scoped" {
					sawScoped = true
					if symbol != "BTCUSDT" {
						return bitgetDataError("40001", "missing scoped symbol"), nil
					}
				} else {
					sawAccountWide = true
					if symbol != "" {
						return bitgetDataError("40002", "account-wide fills must not include symbol"), nil
					}
				}
				return bitgetData(`{"list":[{"category":"USDT-FUTURES","orderId":"10","clientOid":"100","execId":"e1","symbol":"BTCUSDT","side":"buy","execPrice":"99","execQty":"1","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}],"execTime":"1720000000000"}],"cursor":""}`), nil
			default:
				return (&bitgetOpenAPIRouter{}).RoundTrip(request)
			}
		})},
	}, exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)

	instrument := bybitBitgetTestInstrumentPtr(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	if _, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, OrderID: "scoped", Limit: 10}); err != nil {
		t.Fatalf("scoped Fills: %v", err)
	}
	if _, err := client.Fills(ctx, exchange.FillsRequest{OrderID: "account-wide", Limit: 10}); err != nil {
		t.Fatalf("account-wide Fills: %v", err)
	}
	if !sawScoped || !sawAccountWide {
		t.Fatalf("sawScoped=%v sawAccountWide=%v, want both requests", sawScoped, sawAccountWide)
	}
}

func TestBitgetPositionsPreserveSignedPnLAndSkipZeroQuantityRows(t *testing.T) {
	ctx := context.Background()
	settings := bybitBitgetStaticCatalogSettings(t, Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/position/current-position":
				return bitgetData(`{"list":[{"symbol":"BTCUSDT","posSide":"long","qty":"1","averageOpenPrice":"99","markPrice":"100","liquidationPrice":"50","leverage":"5","unrealisedPnl":"-1.25"},{"symbol":"ETHUSDT","posSide":"short","qty":"0","averageOpenPrice":"","markPrice":"2000","liquidationPrice":"","leverage":"","unrealisedPnl":"0"}]}`), nil
			default:
				return (&bitgetOpenAPIRouter{}).RoundTrip(request)
			}
		})},
	}, exchange.VenueBitget, exchange.ProductPerp, "BTC-USDT")
	client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)

	positions, err := client.Positions(ctx, exchange.PositionsRequest{})
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 1 {
		t.Fatalf("positions len = %d, want 1: %+v", len(positions), positions)
	}
	if !positions[0].UnrealizedPnL.Equal(decimal.RequireFromString("-1.25")) {
		t.Fatalf("unrealized PnL = %s, want -1.25", positions[0].UnrealizedPnL)
	}
}

func TestBitgetBuildersSatisfyExchangeContracts(t *testing.T) {
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://stream.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatalf("constructor performed network I/O: %s %s", request.Method, request.URL.String())
			return nil, nil
		})},
	}

	spot := NewBitgetSpot("key", "secret", "passphrase", settings)
	usdtPerp := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
	usdcPerp := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings)
	var _ exchange.SpotClient = spot
	var _ exchange.PerpClient = usdtPerp
	var _ exchange.PerpClient = usdcPerp
	if _, ok := spot.(exchange.ExactOrderLookup); !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", spot)
	}
	if _, ok := usdtPerp.(exchange.ExactOrderLookup); !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", usdtPerp)
	}
	if _, ok := usdcPerp.(exchange.ExactOrderLookup); !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", usdcPerp)
	}
}

func TestBitgetExactOrderLookupIsInstrumentScoped(t *testing.T) {
	for _, test := range []struct {
		name         string
		product      exchange.Product
		settlement   string
		wantCategory string
		build        func(Settings) any
	}{
		{name: "spot", product: exchange.ProductSpot, wantCategory: "spot", build: func(settings Settings) any {
			return NewBitgetSpot("key", "secret", "passphrase", settings)
		}},
		{name: "usdt perp", product: exchange.ProductPerp, settlement: "USDT", wantCategory: "USDT-FUTURES", build: func(settings Settings) any {
			return NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings)
		}},
		{name: "usdc perp", product: exchange.ProductPerp, settlement: "USDC", wantCategory: "USDC-FUTURES", build: func(settings Settings) any {
			return NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			symbol := "BTC-USDT"
			if test.settlement == "USDC" {
				symbol = "BTC-USDC"
			}
			instrument := bybitBitgetTestInstrument(exchange.VenueBitget, test.product, symbol)
			native := instrument.Identity.NativeSymbol
			var query string
			settings := bybitBitgetStaticCatalogSettings(t, Settings{
				Endpoint: "https://exact-order.invalid",
				HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					query = request.URL.Query().Encode()
					if request.URL.Path != "/api/v3/trade/order-info" {
						return bitgetDataError("40017", "unexpected route"), nil
					}
					row := strings.ReplaceAll(bitgetOrderJSON("11", "101", "filled", false), "BTCUSDT", native)
					return bitgetData(row), nil
				})},
			}, exchange.VenueBitget, test.product, symbol)
			raw := test.build(settings)
			client, ok := raw.(exchange.ExactOrderLookup)
			if !ok {
				t.Fatalf("%T does not implement ExactOrderLookup", raw)
			}
			result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
				Instrument: &instrument, ClientOrderID: "101",
			})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" || result.Order.NativeSymbol != native {
				t.Fatalf("result=%+v", result)
			}
			for _, part := range []string{"category=" + test.wantCategory, "symbol=" + native, "clientOid=101"} {
				if !strings.Contains(query, part) {
					t.Fatalf("query=%q, missing %q", query, part)
				}
			}
		})
	}
}

func TestBitgetUSDCPerpUsesUSDCFuturesProductTypeAndMarginCoin(t *testing.T) {
	ctx := context.Background()
	var placeBody string
	var instrumentsCategory string
	client := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/market/instruments":
				instrumentsCategory = request.URL.Query().Get("category")
				return bitgetData(`[{"symbol":"BTCUSDC","category":"USDC-FUTURES","baseCoin":"BTC","quoteCoin":"USDC","minOrderQty":"0.001","minOrderAmount":"5","pricePrecision":"1","quantityPrecision":"3","priceMultiplier":"0.1","quantityMultiplier":"0.001","status":"online"}]`), nil
			case "/api/v3/account/settings":
				return bitgetData(`{"accountMode":"unified","holdMode":"hedge_mode"}`), nil
			case "/api/v3/trade/place-order":
				body, _ := io.ReadAll(request.Body)
				placeBody = string(body)
				return bitgetData(`{"orderId":"11","clientOid":"401"}`), nil
			default:
				return bitgetData(`{}`), nil
			}
		})},
	})
	if _, err := client.Instruments(ctx); err != nil {
		t.Fatalf("Instruments: %v", err)
	}
	if instrumentsCategory != "USDC-FUTURES" {
		t.Fatalf("instrument category = %q, want USDC-FUTURES", instrumentsCategory)
	}
	_, err := client.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductPerp, "BTC-USDC"),
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(99),
		LimitPolicy:   exchange.LimitPolicyResting,
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	for _, want := range []string{`"category":"USDC-FUTURES"`, `"marginCoin":"USDC"`, `"posSide":"short"`} {
		if !strings.Contains(placeBody, want) {
			t.Fatalf("place body %s missing %s", placeBody, want)
		}
	}
	if strings.Contains(placeBody, `"reduceOnly"`) {
		t.Fatalf("hedge-mode place body assigns reduceOnly with posSide: %s", placeBody)
	}
}

func TestBitgetSpotMarketBuyConvertsBaseQuantityToQuoteQuantity(t *testing.T) {
	var placeBody string
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/api/v3/market/orderbook":
				return bitgetData(`{"b":[["99","1"]],"a":[["101","2"]],"ts":"1720000000000"}`), nil
			case "/api/v3/trade/place-order":
				body, _ := io.ReadAll(request.Body)
				placeBody = string(body)
				return bitgetData(`{"orderId":"11","clientOid":"401"}`), nil
			default:
				return bitgetData(`{}`), nil
			}
		})},
	})
	_, err := client.PlaceOrder(t.Context(), exchange.PlaceOrderRequest{
		Instrument:    bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT"),
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if !strings.Contains(placeBody, `"qty":"101"`) {
		t.Fatalf("place body %s missing quote quantity", placeBody)
	}
}

func TestBitgetCancelAcceptsVenueSizedOrderID(t *testing.T) {
	const orderID = "1462622650495291392"
	var cancelBody string
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			cancelBody = string(body)
			return bitgetData(`{"orderId":"` + orderID + `"}`), nil
		})},
	})
	if _, err := client.CancelOrder(t.Context(), exchange.CancelOrderRequest{Instrument: bybitBitgetTestInstrument(exchange.VenueBitget, exchange.ProductSpot, "BTC-USDT"), OrderID: orderID}); err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if !strings.Contains(cancelBody, `"orderId":"`+orderID+`"`) {
		t.Fatalf("cancel body %s missing venue order id", cancelBody)
	}
}

func TestBitgetMalformedNumericFieldsReturnMalformedResponse(t *testing.T) {
	client := NewBitgetSpot("key", "secret", "passphrase", Settings{
		Endpoint: "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
			return bitgetData(`[{"symbol":"BTCUSDT","category":"spot","baseCoin":"BTC","quoteCoin":"USDT","minOrderQty":"not-a-decimal","pricePrecision":"1","quantityPrecision":"3","status":"online"}]`), nil
		})},
	})
	_, err := client.Instruments(context.Background())
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)
}

func assertExchangeErrorKind(t *testing.T, err error, want exchange.ErrorKind) {
	t.Helper()
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error = %v, want exchange error kind %s", err, want)
	}
	if exchangeErr.Kind() != want {
		t.Fatalf("error kind = %s, want %s: %v", exchangeErr.Kind(), want, err)
	}
}
