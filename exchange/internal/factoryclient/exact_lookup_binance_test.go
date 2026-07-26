package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestBinanceExactOrderLookupByClientOrderID(t *testing.T) {
	tests := []struct {
		name    string
		client  exchange.ExactOrderLookup
		product exchange.Product
	}{
		{
			name:    "spot",
			client:  NewBinanceSpot("key", "secret", binanceExactLookupSettings(binanceExactLookupRouter(t, "/api/v3/order", `{"symbol":"ETHUSDT","orderId":77,"clientOrderId":"12345","price":"101","origQty":"0.5","executedQty":"0","cummulativeQuoteQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}`))).(exchange.ExactOrderLookup),
			product: exchange.ProductSpot,
		},
		{
			name:    "perp",
			client:  NewBinanceUSDPerp("key", "secret", binanceExactLookupSettings(binanceExactLookupRouter(t, "/fapi/v1/order", `{"symbol":"ETHUSDT","orderId":88,"clientOrderId":"12345","price":"101","origQty":"0.5","executedQty":"0","avgPrice":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":false,"updateTime":1720000000000}`))).(exchange.ExactOrderLookup),
			product: exchange.ProductPerp,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instrument := binanceCatalogTestInstrument(test.product, "ETH", "USDT", "", oneContract().Value)
			if test.product == exchange.ProductPerp {
				instrument.Identity.SettleAsset = "USDT"
			}
			result, err := test.client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
				Instrument:    &instrument,
				ClientOrderID: "12345",
			})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound {
				t.Fatalf("status = %s, want found", result.Status)
			}
			if result.Order.ClientOrderID != "12345" || result.Order.OrderID == "" {
				t.Fatalf("order identity = %+v", result.Order)
			}
			if result.Order.Status != "open" {
				t.Fatalf("normalized status = %q, want open", result.Order.Status)
			}
			if result.Evidence.Method != exchange.ExactOrderEvidenceByClientOrderID ||
				!result.Evidence.Authoritative || !result.Evidence.Complete ||
				result.Evidence.ObservedClientOrderID != "12345" ||
				result.Evidence.Scope.Venue != exchange.VenueBinance ||
				result.Evidence.Scope.Product != test.product ||
				result.Evidence.Scope.Instrument == nil ||
				result.Evidence.Scope.Instrument.Identity.NativeSymbol != "ETHUSDT" {
				t.Fatalf("evidence = %+v", result.Evidence)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("result does not validate: %v", err)
			}
		})
	}
}

func TestBinanceExactOrderLookupByVenueOrderIDAndNotFound(t *testing.T) {
	t.Run("found by venue id", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/api/v3/order", `{"symbol":"ETHUSDT","orderId":77,"clientOrderId":"12345","price":"101","origQty":"0.5","executedQty":"0.25","cummulativeQuoteQty":"25.25","status":"PARTIALLY_FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY"}`)
		client := NewBinanceSpot("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", oneContract().Value)
		result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, VenueOrderID: "77"})
		if err != nil {
			t.Fatalf("LookupExactOrder: %v", err)
		}
		if result.Status != exchange.ExactOrderFound || result.Order.OrderID != "77" || result.Order.Status != "partially_filled" {
			t.Fatalf("result = %+v", result)
		}
		if result.Evidence.Method != exchange.ExactOrderEvidenceByVenueOrderID || result.Evidence.ObservedVenueOrderID != "77" {
			t.Fatalf("evidence = %+v", result.Evidence)
		}
	})

	t.Run("authoritative not found", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/fapi/v1/order", `{"code":-2013,"msg":"secret order does not exist"}`)
		router.status = http.StatusBadRequest
		client := NewBinanceUSDPerp("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := binanceCatalogTestInstrument(exchange.ProductPerp, "ETH", "USDT", "USDT", oneContract().Value)
		result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, VenueOrderID: "88"})
		if err != nil {
			t.Fatalf("LookupExactOrder: %v", err)
		}
		if result.Status != exchange.ExactOrderNotFound || !result.Evidence.Authoritative || !result.Evidence.Complete {
			t.Fatalf("result = %+v", result)
		}
		if result.Evidence.ObservedVenueOrderID != "88" {
			t.Fatalf("evidence = %+v", result.Evidence)
		}
	})

	t.Run("generic HTTP 404 is not authoritative absence", func(t *testing.T) {
		tests := []struct {
			name    string
			path    string
			product exchange.Product
			new     func(Settings) exchange.ExactOrderLookup
		}{
			{
				name:    "spot",
				path:    "/api/v3/order",
				product: exchange.ProductSpot,
				new: func(settings Settings) exchange.ExactOrderLookup {
					return NewBinanceSpot("key", "secret", settings).(exchange.ExactOrderLookup)
				},
			},
			{
				name:    "perp",
				path:    "/fapi/v1/order",
				product: exchange.ProductPerp,
				new: func(settings Settings) exchange.ExactOrderLookup {
					return NewBinanceUSDPerp("key", "secret", settings).(exchange.ExactOrderLookup)
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				router := binanceExactLookupRouter(t, test.path, `{"code":0,"msg":"route not found"}`)
				router.status = http.StatusNotFound
				client := test.new(binanceExactLookupSettings(router))
				instrument := binanceCatalogTestInstrument(test.product, "ETH", "USDT", "", oneContract().Value)
				if test.product == exchange.ProductPerp {
					instrument.Identity.SettleAsset = "USDT"
				}
				result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
					Instrument:    &instrument,
					ClientOrderID: "12345",
				})
				if err == nil {
					t.Fatalf("generic HTTP 404 returned authoritative result %+v", result)
				}
				if result.Status == exchange.ExactOrderNotFound {
					t.Fatalf("generic HTTP 404 proved exact absence: %+v", result)
				}
			})
		}
	})
}

func TestBinanceExactOrderLookupRejectsInvalidWithoutIOAndMalformedMismatch(t *testing.T) {
	t.Run("invalid request does not send", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/api/v3/order", `{}`)
		client := NewBinanceSpot("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{ClientOrderID: "12345"})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("error = %v, want invalid request", err)
		}
		if got := router.calls(); got != 0 {
			t.Fatalf("invalid request sent %d HTTP calls", got)
		}
	})

	t.Run("identity mismatch is malformed", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/api/v3/order", `{"symbol":"ETHUSDT","orderId":77,"clientOrderId":"99999","price":"101","origQty":"0.5","executedQty":"0","cummulativeQuoteQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}`)
		client := NewBinanceSpot("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", oneContract().Value)
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("error = %v, want malformed response", err)
		}
	})

	t.Run("unknown status is malformed", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/api/v3/order", `{"symbol":"ETHUSDT","orderId":77,"clientOrderId":"12345","price":"101","origQty":"0.5","executedQty":"0","cummulativeQuoteQty":"0","status":"FUTURE_UNKNOWN","timeInForce":"GTC","type":"LIMIT","side":"BUY"}`)
		client := NewBinanceSpot("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", oneContract().Value)
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("error = %v, want malformed response", err)
		}
	})

	t.Run("secret venue message is redacted", func(t *testing.T) {
		router := binanceExactLookupRouter(t, "/api/v3/order", `{"code":-1013,"msg":"secret balance detail"}`)
		router.status = http.StatusBadRequest
		client := NewBinanceSpot("key", "secret", binanceExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := binanceCatalogTestInstrument(exchange.ProductSpot, "ETH", "USDT", "", oneContract().Value)
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
		if err == nil || strings.Contains(err.Error(), "secret balance detail") {
			t.Fatalf("error leaked venue message: %v", err)
		}
	})
}

type binanceExactLookupHTTPRouter struct {
	t         *testing.T
	wantPath  string
	body      string
	status    int
	mu        sync.Mutex
	callCount int
}

func binanceExactLookupRouter(t *testing.T, wantPath, body string) *binanceExactLookupHTTPRouter {
	t.Helper()
	return &binanceExactLookupHTTPRouter{t: t, wantPath: wantPath, body: body, status: http.StatusOK}
}

func (router *binanceExactLookupHTTPRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	router.mu.Lock()
	router.callCount++
	router.mu.Unlock()
	if request.Method != http.MethodGet || request.URL.Path != router.wantPath {
		router.t.Fatalf("request = %s %s, want GET %s", request.Method, request.URL.Path, router.wantPath)
	}
	query := request.URL.Query()
	if query.Get("symbol") != "ETHUSDT" {
		router.t.Fatalf("symbol query = %q, want ETHUSDT; raw=%s", query.Get("symbol"), request.URL.RawQuery)
	}
	if query.Get("origClientOrderId") == "" && query.Get("orderId") == "" {
		router.t.Fatalf("missing exact identity query; raw=%s", request.URL.RawQuery)
	}
	return &http.Response{
		StatusCode: router.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(router.body)),
	}, nil
}

func (router *binanceExactLookupHTTPRouter) calls() int {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.callCount
}

func binanceExactLookupSettings(router *binanceExactLookupHTTPRouter) Settings {
	return Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	}
}
