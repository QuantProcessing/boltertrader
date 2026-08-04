package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestBybitExactOrderLookupUsesRealtimeThenHistory(t *testing.T) {
	for _, test := range []struct {
		name          string
		router        bybitExactLookupRouter
		request       exchange.ExactOrderLookupRequest
		wantOrderID   string
		wantClientID  string
		wantStatus    string
		wantRealtime  int
		wantHistory   int
		wantQueryPart string
	}{
		{
			name: "realtime by client order id",
			router: bybitExactLookupRouter{
				realtime: []string{bybitOrderJSON("11", "101", "New", false)},
			},
			request:     exchange.ExactOrderLookupRequest{ClientOrderID: "101"},
			wantOrderID: "11", wantClientID: "101", wantRealtime: 1,
			wantStatus:    "open",
			wantQueryPart: "orderLinkId=101",
		},
		{
			name: "realtime partially filled status",
			router: bybitExactLookupRouter{
				realtime: []string{bybitOrderJSON("12", "102", "PartiallyFilled", false)},
			},
			request:     exchange.ExactOrderLookupRequest{ClientOrderID: "102"},
			wantOrderID: "12", wantClientID: "102", wantRealtime: 1,
			wantStatus:    "partially_filled",
			wantQueryPart: "orderLinkId=102",
		},
		{
			name: "history fallback by venue order id",
			router: bybitExactLookupRouter{
				history: []string{bybitOrderJSON("22", "202", "Filled", true)},
			},
			request:     exchange.ExactOrderLookupRequest{VenueOrderID: "22"},
			wantOrderID: "22", wantClientID: "202", wantRealtime: 1, wantHistory: 1,
			wantStatus:    "filled",
			wantQueryPart: "orderId=22",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, instrument, router := newBybitExactLookupClient(t, test.router)
			request := test.request
			request.Instrument = &instrument

			result, err := client.LookupExactOrder(context.Background(), request)
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound {
				t.Fatalf("status = %s, want %s", result.Status, exchange.ExactOrderFound)
			}
			if result.Order.OrderID != test.wantOrderID || result.Order.ClientOrderID != test.wantClientID {
				t.Fatalf("order identity = %+v", result.Order)
			}
			if result.Order.Status != test.wantStatus {
				t.Fatalf("order status = %q, want %q", result.Order.Status, test.wantStatus)
			}
			if result.Order.NativeSymbol != instrument.Identity.NativeSymbol || result.Order.NativeID != instrument.Identity.NativeID {
				t.Fatalf("order instrument identity = %+v", result.Order)
			}
			if result.Evidence.Method == "" || !strings.Contains(router.lastRequestQuery, test.wantQueryPart) {
				t.Fatalf("evidence=%+v last query=%q", result.Evidence, router.lastRequestQuery)
			}
			if router.realtimeCalls != test.wantRealtime || router.historyCalls != test.wantHistory {
				t.Fatalf("calls realtime/history = %d/%d, want %d/%d", router.realtimeCalls, router.historyCalls, test.wantRealtime, test.wantHistory)
			}
		})
	}
}

func TestBybitExactOrderLookupDoesNotPromoteEmptyHistoryToNotFound(t *testing.T) {
	client, instrument, _ := newBybitExactLookupClient(t, bybitExactLookupRouter{})
	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:    &instrument,
		ClientOrderID: "404",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderUnconfirmed || result.Evidence.Authoritative || result.Evidence.Complete {
		t.Fatalf("result = %+v, want unconfirmed non-authoritative incomplete", result)
	}
}

func TestBybitExactOrderLookupRejectsScopeAndIdentityMismatch(t *testing.T) {
	t.Run("invalid request before IO", func(t *testing.T) {
		client, _, router := newBybitExactLookupClient(t, bybitExactLookupRouter{})
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{ClientOrderID: "101"})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("error = %v, want ErrInvalidRequest", err)
		}
		if router.realtimeCalls != 0 || router.historyCalls != 0 {
			t.Fatalf("unexpected IO calls realtime/history = %d/%d", router.realtimeCalls, router.historyCalls)
		}
	})

	t.Run("malformed venue order id before IO", func(t *testing.T) {
		client, instrument, router := newBybitExactLookupClient(t, bybitExactLookupRouter{})
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
			Instrument:   &instrument,
			VenueOrderID: "not-a-bybit-native-id",
		})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("error = %v, want ErrInvalidRequest", err)
		}
		if router.realtimeCalls != 0 || router.historyCalls != 0 {
			t.Fatalf("unexpected IO calls realtime/history = %d/%d", router.realtimeCalls, router.historyCalls)
		}
	})

	t.Run("response outside requested instrument is malformed", func(t *testing.T) {
		client, instrument, _ := newBybitExactLookupClient(t, bybitExactLookupRouter{
			realtime: []string{strings.ReplaceAll(bybitOrderJSON("11", "101", "New", false), `"symbol":"BTCUSDT"`, `"symbol":"ETHUSDT"`)},
		})
		_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
			Instrument:    &instrument,
			ClientOrderID: "101",
		})
		if !errors.Is(err, exchange.ErrMalformedResponse) {
			t.Fatalf("error = %v, want ErrMalformedResponse", err)
		}
	})
}

func newBybitExactLookupClient(t *testing.T, router bybitExactLookupRouter) (exchange.ExactOrderLookup, exchange.Instrument, *bybitExactLookupRouter) {
	t.Helper()
	settings := bybitBitgetStaticCatalogSettings(t, Settings{
		Endpoint:    "https://exact-lookup.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: &router},
	}, exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT")
	raw := NewBybitLinearPerp("key", "secret", "USDT", settings)
	client, ok := raw.(exchange.ExactOrderLookup)
	if !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", raw)
	}
	return client, bybitBitgetTestInstrument(exchange.VenueBybit, exchange.ProductPerp, "BTC-USDT"), &router
}

type bybitExactLookupRouter struct {
	realtime []string
	history  []string

	realtimeCalls    int
	historyCalls     int
	lastRequestQuery string
}

func (router *bybitExactLookupRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	query := request.URL.Query()
	router.lastRequestQuery = query.Encode()
	switch request.URL.Path {
	case "/v5/order/realtime":
		router.realtimeCalls++
		return bybitData(`{"list":[` + strings.Join(router.realtime, ",") + `]}`), nil
	case "/v5/order/history":
		router.historyCalls++
		return bybitData(`{"list":[` + strings.Join(router.history, ",") + `]}`), nil
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"retCode":10001,"retMsg":"unexpected route"}`))}, nil
	}
}
