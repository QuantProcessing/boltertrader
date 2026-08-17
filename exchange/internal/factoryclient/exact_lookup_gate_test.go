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
	"github.com/shopspring/decimal"
)

func TestGateExactOrderLookupFindsSpotAndPerp(t *testing.T) {
	tests := []struct {
		name       string
		client     exchange.ExactOrderLookup
		router     *gateExactLookupHTTPRouter
		instrument exchange.Instrument
		request    func(*exchange.Instrument) exchange.ExactOrderLookupRequest
		assert     func(t *testing.T, result exchange.ExactOrderLookupResult, requestURI string)
	}{
		{
			name:       "spot by client order id",
			router:     gateExactLookupRouter(t, "/api/v4/spot/orders/t-12345", `{"id":"77","text":"t-12345","currency_pair":"ETH_USDT","status":"open","type":"limit","side":"buy","amount":"0.5","price":"101","left":"0.5","time_in_force":"gtc","create_time_ms":"1720000000000","update_time_ms":"1720000000000"}`),
			instrument: gateExactLookupInstrument(exchange.ProductSpot),
			request: func(instrument *exchange.Instrument) exchange.ExactOrderLookupRequest {
				return exchange.ExactOrderLookupRequest{Instrument: instrument, ClientOrderID: "12345"}
			},
			assert: func(t *testing.T, result exchange.ExactOrderLookupResult, requestURI string) {
				t.Helper()
				if !strings.Contains(requestURI, "currency_pair=ETH_USDT") {
					t.Fatalf("requestURI = %q, want spot currency pair", requestURI)
				}
				if result.Order.OrderID != "77" || result.Order.ClientOrderID != "12345" || result.Order.Status != "open" {
					t.Fatalf("result order = %+v", result.Order)
				}
			},
		},
		{
			name:       "perp by client order id",
			router:     gateExactLookupRouter(t, "/api/v4/futures/usdt/orders/t-12345", `{"id":88,"text":"t-12345","contract":"ETH_USDT","status":"open","size":2,"left":1,"price":"101","tif":"gtc","create_time_ms":"1720000000000","update_time":1720000000000}`),
			instrument: gateExactLookupInstrument(exchange.ProductPerp),
			request: func(instrument *exchange.Instrument) exchange.ExactOrderLookupRequest {
				return exchange.ExactOrderLookupRequest{Instrument: instrument, ClientOrderID: "12345"}
			},
			assert: func(t *testing.T, result exchange.ExactOrderLookupResult, requestURI string) {
				t.Helper()
				if strings.Contains(requestURI, "contract=") {
					t.Fatalf("requestURI = %q, futures exact endpoint should not need contract query", requestURI)
				}
				if result.Order.OrderID != "88" || result.Order.ClientOrderID != "12345" || result.Order.Status != "partially_filled" ||
					!result.Order.Quantity.Equal(decimal.RequireFromString("0.02")) ||
					!result.Order.Filled.Equal(decimal.RequireFromString("0.01")) {
					t.Fatalf("result order = %+v", result.Order)
				}
			},
		},
		{
			name:       "perp by venue order id",
			router:     gateExactLookupRouter(t, "/api/v4/futures/usdt/orders/88", `{"id":88,"text":"t-12345","contract":"ETH_USDT","finish_as":"filled","size":-2,"left":0,"price":"101","fill_price":"100.5","tif":"ioc","is_reduce_only":true,"create_time_ms":"1720000000000","update_time":1720000000000}`),
			instrument: gateExactLookupInstrument(exchange.ProductPerp),
			request: func(instrument *exchange.Instrument) exchange.ExactOrderLookupRequest {
				return exchange.ExactOrderLookupRequest{Instrument: instrument, VenueOrderID: "88"}
			},
			assert: func(t *testing.T, result exchange.ExactOrderLookupResult, requestURI string) {
				t.Helper()
				if result.Order.Side != exchange.SideSell || result.Order.Status != "filled" || !result.Order.ReduceOnly ||
					result.Evidence.Method != exchange.ExactOrderEvidenceByVenueOrderID || result.Evidence.ObservedVenueOrderID != "88" {
					t.Fatalf("result = %+v", result)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.instrument.Identity.Product == exchange.ProductPerp {
				test.client = NewGateUSDTPerp("key", "secret", gateExactLookupSettings(test.router)).(exchange.ExactOrderLookup)
			} else {
				test.client = NewGateSpot("key", "secret", gateExactLookupSettings(test.router)).(exchange.ExactOrderLookup)
			}
			instrument := test.instrument
			request := test.request(&instrument)
			result, err := test.client.LookupExactOrder(context.Background(), request)
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound || !result.Evidence.Authoritative || !result.Evidence.Complete ||
				result.Evidence.Scope.Venue != exchange.VenueGate || result.Evidence.Scope.Product != instrument.Identity.Product ||
				result.Evidence.Scope.Instrument == nil || result.Evidence.Scope.Instrument.Identity.NativeSymbol != "ETH_USDT" {
				t.Fatalf("result = %+v", result)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("result Validate: %v", err)
			}
			test.assert(t, result, test.router.requestAt(0))
		})
	}
}

func TestGateExactOrderLookupAuthoritativeNotFoundOnlyForOrderNotFoundLabel(t *testing.T) {
	t.Run("order not found label", func(t *testing.T) {
		router := gateExactLookupRouter(t, "/api/v4/spot/orders/t-99999", `{"label":"ORDER_NOT_FOUND","message":"secret missing detail"}`)
		router.status = http.StatusNotFound
		client := NewGateSpot("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := gateExactLookupInstrument(exchange.ProductSpot)
		result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "99999"})
		if err != nil {
			t.Fatalf("LookupExactOrder: %v", err)
		}
		if result.Status != exchange.ExactOrderNotFound || !result.Evidence.Authoritative || !result.Evidence.Complete {
			t.Fatalf("result = %+v", result)
		}
		if strings.Contains(result.Evidence.ObservedClientOrderID, "secret") {
			t.Fatalf("not-found evidence leaked venue message: %+v", result)
		}
	})

	t.Run("generic not found label is not authoritative", func(t *testing.T) {
		router := gateExactLookupRouter(t, "/api/v4/futures/usdt/orders/t-99999", `{"label":"NOT_FOUND","message":"secret route detail"}`)
		router.status = http.StatusNotFound
		client := NewGateUSDTPerp("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := gateExactLookupInstrument(exchange.ProductPerp)
		result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "99999"})
		if err == nil {
			t.Fatalf("generic NOT_FOUND returned result %+v", result)
		}
		if result.Status == exchange.ExactOrderNotFound {
			t.Fatalf("generic NOT_FOUND proved exact absence: %+v", result)
		}
	})
}

func TestGateExactOrderLookupNormalizesTerminalFinishReasons(t *testing.T) {
	tests := []struct {
		finishAs string
		want     string
	}{
		{finishAs: "_new", want: "open"},
		{finishAs: "_update", want: "open"},
		{finishAs: "open", want: "open"},
		{finishAs: "filled", want: "filled"},
		{finishAs: "succeeded", want: "filled"},
		{finishAs: "failed", want: "rejected"},
		{finishAs: "small", want: "rejected"},
		{finishAs: "depth_not_enough", want: "rejected"},
		{finishAs: "trader_not_enough", want: "rejected"},
		{finishAs: "cancelled", want: "canceled"},
		{finishAs: "expired", want: "canceled"},
		{finishAs: "ioc", want: "canceled"},
		{finishAs: "poc", want: "canceled"},
		{finishAs: "stp", want: "canceled"},
		{finishAs: "liquidated", want: "canceled"},
		{finishAs: "auto_deleveraging", want: "canceled"},
		{finishAs: "auto_deleveraged", want: "canceled"},
		{finishAs: "reduce_only", want: "canceled"},
		{finishAs: "position_close", want: "canceled"},
		{finishAs: "position_closed", want: "canceled"},
		{finishAs: "reduce_out", want: "canceled"},
		{finishAs: "cb", want: "canceled"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.finishAs, func(t *testing.T) {
			got, err := gateExactPortableOrderStatus(exchange.Order{Status: test.finishAs})
			if err != nil {
				t.Fatalf("gateExactPortableOrderStatus(%q): %v", test.finishAs, err)
			}
			if got != test.want {
				t.Fatalf("gateExactPortableOrderStatus(%q) = %q, want %q", test.finishAs, got, test.want)
			}
		})
	}

	for _, test := range tests {
		test := test
		if test.finishAs == "_new" || test.finishAs == "_update" || test.finishAs == "open" {
			continue
		}
		t.Run("exact_lookup_"+test.finishAs, func(t *testing.T) {
			router := gateExactLookupRouter(t, "/api/v4/spot/orders/t-12345", `{"id":"77","text":"t-12345","currency_pair":"ETH_USDT","status":"closed","finish_as":"`+test.finishAs+`","type":"limit","side":"buy","amount":"0.5","price":"101","left":"0.5"}`)
			client := NewGateSpot("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
			instrument := gateExactLookupInstrument(exchange.ProductSpot)

			result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound || result.Order.Status != test.want {
				t.Fatalf("result = %+v", result)
			}
		})
	}

	t.Run("unknown remains fail closed", func(t *testing.T) {
		router := gateExactLookupRouter(t, "/api/v4/futures/usdt/orders/t-12345", `{"id":88,"text":"t-12345","contract":"ETH_USDT","finish_as":"unknown","size":2,"left":2,"price":"101"}`)
		client := NewGateUSDTPerp("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
		instrument := gateExactLookupInstrument(exchange.ProductPerp)

		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("LookupExactOrder error = %v, want malformed", err)
		}
	})
}

func TestGateExactOrderLookupDerivesStatusFromFilledQuantity(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		finishAs string
		amount   string
		left     string
		want     string
	}{
		{name: "open partial", status: "open", amount: "1", left: "0.4", want: "partially_filled"},
		{name: "update partial", finishAs: "_update", amount: "1", left: "0.4", want: "partially_filled"},
		{name: "ioc partial", finishAs: "ioc", amount: "1", left: "0.4", want: "partially_filled"},
		{name: "poc zero canceled", finishAs: "poc", amount: "1", left: "1", want: "canceled"},
		{name: "failed zero rejected", finishAs: "failed", amount: "1", left: "1", want: "rejected"},
		{name: "failed nonzero not rejected", finishAs: "failed", amount: "1", left: "0.4", want: "partially_filled"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			statusField := ""
			if test.status != "" {
				statusField = `,"status":"` + test.status + `"`
			}
			finishField := ""
			if test.finishAs != "" {
				finishField = `,"finish_as":"` + test.finishAs + `"`
			}
			router := gateExactLookupRouter(t, "/api/v4/spot/orders/t-12345", `{"id":"77","text":"t-12345","currency_pair":"ETH_USDT","type":"limit","side":"buy","amount":"`+test.amount+`","price":"101","left":"`+test.left+`"`+statusField+finishField+`}`)
			client := NewGateSpot("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
			instrument := gateExactLookupInstrument(exchange.ProductSpot)

			result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Order.Status != test.want {
				t.Fatalf("status = %q, want %q; result=%+v", result.Order.Status, test.want, result)
			}
			if test.name == "failed nonzero not rejected" && result.Order.Status == "rejected" {
				t.Fatalf("failed order with nonzero fill must not be rejected: %+v", result)
			}
		})
	}
}

func TestGateExactOrderLookupRejectsInvalidAndMalformedEvidence(t *testing.T) {
	t.Run("invalid request does not send", func(t *testing.T) {
		router := gateExactLookupRouter(t, "/api/v4/spot/orders/t-12345", `{}`)
		client := NewGateSpot("key", "secret", gateExactLookupSettings(router)).(exchange.ExactOrderLookup)
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{ClientOrderID: "12345"})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("LookupExactOrder error = %v, want invalid request", err)
		}
		if got := router.calls(); got != 0 {
			t.Fatalf("invalid request sent %d HTTP calls", got)
		}
	})

	tests := []struct {
		name    string
		body    string
		product exchange.Product
		new     func(Settings) exchange.ExactOrderLookup
	}{
		{
			name:    "spot client id mismatch",
			body:    `{"id":"77","text":"t-other","currency_pair":"ETH_USDT","status":"open","type":"limit","side":"buy","amount":"0.5","price":"101","left":"0.5"}`,
			product: exchange.ProductSpot,
			new: func(settings Settings) exchange.ExactOrderLookup {
				return NewGateSpot("key", "secret", settings).(exchange.ExactOrderLookup)
			},
		},
		{
			name:    "perp instrument mismatch",
			body:    `{"id":88,"text":"t-12345","contract":"BTC_USDT","status":"open","size":2,"left":1,"price":"101"}`,
			product: exchange.ProductPerp,
			new: func(settings Settings) exchange.ExactOrderLookup {
				return NewGateUSDTPerp("key", "secret", settings).(exchange.ExactOrderLookup)
			},
		},
		{
			name:    "unknown status",
			body:    `{"id":"77","text":"t-12345","currency_pair":"ETH_USDT","status":"future_unknown","type":"limit","side":"buy","amount":"0.5","price":"101","left":"0.5"}`,
			product: exchange.ProductSpot,
			new: func(settings Settings) exchange.ExactOrderLookup {
				return NewGateSpot("key", "secret", settings).(exchange.ExactOrderLookup)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := gateExactLookupRouter(t, gateExactLookupPath(test.product, "t-12345"), test.body)
			client := test.new(gateExactLookupSettings(router))
			instrument := gateExactLookupInstrument(test.product)
			_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "12345"})
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("LookupExactOrder error = %v, want malformed", err)
			}
		})
	}
}

type gateExactLookupHTTPRouter struct {
	t         *testing.T
	wantPath  string
	body      string
	status    int
	mu        sync.Mutex
	requests  []string
	callCount int
}

func gateExactLookupRouter(t *testing.T, wantPath, body string) *gateExactLookupHTTPRouter {
	t.Helper()
	return &gateExactLookupHTTPRouter{t: t, wantPath: wantPath, body: body, status: http.StatusOK}
}

func (router *gateExactLookupHTTPRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	router.mu.Lock()
	router.callCount++
	router.requests = append(router.requests, request.URL.RequestURI())
	router.mu.Unlock()
	if request.Method != http.MethodGet || request.URL.Path != router.wantPath {
		router.t.Fatalf("request = %s %s, want GET %s", request.Method, request.URL.Path, router.wantPath)
	}
	return &http.Response{
		StatusCode: router.status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(router.body)),
	}, nil
}

func (router *gateExactLookupHTTPRouter) calls() int {
	router.mu.Lock()
	defer router.mu.Unlock()
	return router.callCount
}

func (router *gateExactLookupHTTPRouter) requestAt(index int) string {
	router.mu.Lock()
	defer router.mu.Unlock()
	if index < 0 || index >= len(router.requests) {
		return ""
	}
	return router.requests[index]
}

func gateExactLookupSettings(router *gateExactLookupHTTPRouter) Settings {
	return Settings{Endpoint: "https://openapi.invalid/api/v4", HTTPClient: &http.Client{Transport: router}}
}

func gateExactLookupInstrument(product exchange.Product) exchange.Instrument {
	settle := ""
	if product == exchange.ProductPerp {
		settle = "USDT"
	}
	multiplier := exchange.OptionalDecimal{Valid: true, Value: decimal.RequireFromString("0.01")}
	return newInstrument(
		exchange.VenueGate,
		product,
		"ETH/USDT",
		"ETH",
		"USDT",
		settle,
		"ETH_USDT",
		"ETH_USDT",
		decimal.RequireFromString("0.01"),
		decimal.RequireFromString("0.01"),
		decimal.RequireFromString("0.01"),
		exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: product == exchange.ProductSpot},
		multiplier,
	)
}

func gateExactLookupPath(product exchange.Product, orderID string) string {
	if product == exchange.ProductPerp {
		return "/api/v4/futures/usdt/orders/" + orderID
	}
	return "/api/v4/spot/orders/" + orderID
}
