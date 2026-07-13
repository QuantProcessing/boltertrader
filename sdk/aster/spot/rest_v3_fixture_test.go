package spot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

func TestSpotV3FixtureModels(t *testing.T) {
	t.Run("exchange info", func(t *testing.T) {
		client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertRequest(t, request, http.MethodGet, "/api/v3/exchangeInfo")
			return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "exchange_info.json")), nil
		})
		got, err := client.ExchangeInfo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Symbols) != 1 {
			t.Fatalf("symbols = %d, want 1", len(got.Symbols))
		}
		symbol := got.Symbols[0]
		if symbol.Symbol != "ASTERUSDT" || symbol.PricePrecision != 4 || symbol.QuantityPrecision != 2 {
			t.Fatalf("symbol metadata = %+v", symbol)
		}
		if len(symbol.TimeInForce) != 3 || symbol.TimeInForce[2] != "GTX" {
			t.Fatalf("timeInForce = %v", symbol.TimeInForce)
		}
		if len(symbol.Filters) != 3 || symbol.Filters[0].TickSize != "0.0001" || symbol.Filters[1].StepSize != "0.01" {
			t.Fatalf("filters = %+v", symbol.Filters)
		}
	})

	t.Run("depth", func(t *testing.T) {
		client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertRequest(t, request, http.MethodGet, "/api/v3/depth")
			return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "depth.json")), nil
		})
		got, err := client.Depth(context.Background(), "asterusdt", 100)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastUpdateID != 1027024 || got.EventTime != 1783641600005 || got.TransactionTime != 1783641600004 {
			t.Fatalf("depth metadata = %+v", got)
		}
		if got.Bids[0][0] != "1.2499" || got.Asks[0][1] != "125.00" {
			t.Fatalf("depth levels = bids %v asks %v", got.Bids, got.Asks)
		}
	})

	t.Run("public trades", func(t *testing.T) {
		client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertRequest(t, request, http.MethodGet, "/api/v3/trades")
			return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "trades.json")), nil
		})
		got, err := client.GetTrades(context.Background(), "ASTERUSDT", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].BaseQty != "3.20" || got[0].IsBuyerMaker {
			t.Fatalf("trades = %+v", got)
		}
	})

	t.Run("cash account", func(t *testing.T) {
		client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertRequest(t, request, http.MethodGet, "/api/v3/account")
			assertSignedQuery(t, request)
			return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "account.json")), nil
		})
		got, err := client.GetAccount(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.FeeTier != 0 || !got.CanBurnAsset || len(got.Balances) != 2 {
			t.Fatalf("account = %+v", got)
		}
		if got.Balances[1].Asset != "USDT" || got.Balances[1].Free != "250.00000000" {
			t.Fatalf("balances = %+v", got.Balances)
		}
	})

	t.Run("user trades", func(t *testing.T) {
		client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertRequest(t, request, http.MethodGet, "/api/v3/userTrades")
			return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "user_trades.json")), nil
		})
		got, err := client.UserTrades(context.Background(), UserTradesQuery{Symbol: "ASTERUSDT"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Side != "BUY" || got[0].Maker || !got[0].Buyer || got[0].CreateUpdateID != nil {
			t.Fatalf("user trades = %+v", got)
		}
	})
}

func TestSpotV3PlaceOrderRequestMatrix(t *testing.T) {
	tests := []struct {
		name       string
		params     PlaceOrderParams
		want       map[string]string
		wantAbsent []string
	}{
		{
			name: "limit gtc",
			params: PlaceOrderParams{
				Symbol: "asterusdt", Side: "BUY", Type: "LIMIT", TimeInForce: "GTC",
				Quantity: "10", Price: "1.25", NewClientOrderID: "fixture-limit-gtc",
			},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "LIMIT", "timeInForce": "GTC", "quantity": "10", "price": "1.25", "newClientOrderId": "fixture-limit-gtc"},
			wantAbsent: []string{"quoteOrderQty"},
		},
		{
			name:   "limit ioc",
			params: PlaceOrderParams{Symbol: "ASTERUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "IOC", Quantity: "2", Price: "1.24"},
			want:   map[string]string{"symbol": "ASTERUSDT", "side": "SELL", "type": "LIMIT", "timeInForce": "IOC", "quantity": "2", "price": "1.24"},
		},
		{
			name:   "post only gtx",
			params: PlaceOrderParams{Symbol: "ASTERUSDT", Side: "BUY", Type: "LIMIT", TimeInForce: "GTX", Quantity: "3", Price: "1.20"},
			want:   map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "LIMIT", "timeInForce": "GTX", "quantity": "3", "price": "1.20"},
		},
		{
			name:       "market buy quote quantity",
			params:     PlaceOrderParams{Symbol: "ASTERUSDT", Side: "BUY", Type: "MARKET", QuoteOrderQty: "100"},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "MARKET", "quoteOrderQty": "100"},
			wantAbsent: []string{"quantity", "price", "timeInForce"},
		},
		{
			name:       "market sell base quantity",
			params:     PlaceOrderParams{Symbol: "ASTERUSDT", Side: "SELL", Type: "MARKET", Quantity: "5"},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "SELL", "type": "MARKET", "quantity": "5"},
			wantAbsent: []string{"quoteOrderQty", "price", "timeInForce"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
				assertRequest(t, request, http.MethodPost, "/api/v3/order")
				if request.Body != nil && request.Body != http.NoBody {
					body, err := io.ReadAll(request.Body)
					if err != nil {
						t.Fatal(err)
					}
					if len(body) != 0 {
						t.Fatalf("unexpected request body %q", body)
					}
				}
				query := request.URL.Query()
				assertSignedQuery(t, request)
				for key, value := range test.want {
					if got := query.Get(key); got != value {
						t.Errorf("%s = %q, want %q; query=%s", key, got, value, request.URL.RawQuery)
					}
				}
				for _, key := range test.wantAbsent {
					if _, ok := query[key]; ok {
						t.Errorf("unexpected %s in query %s", key, request.URL.RawQuery)
					}
				}
				return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "order.json")), nil
			})

			got, err := client.PlaceOrder(context.Background(), test.params)
			if err != nil {
				t.Fatal(err)
			}
			if got.OrderID != 10001 || got.AvgPrice != "0.0000" || got.CumQuote != "0.00" || got.UpdateTime == nil {
				t.Fatalf("order response = %+v", got)
			}
		})
	}
}

func TestSpotV3OrderReportAndCancelRoutes(t *testing.T) {
	orderID := int64(10001)
	limit := 100
	var requests atomic.Int64
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		assertSignedQuery(t, request)
		var payload []byte
		switch request.Method + " " + request.URL.Path {
		case http.MethodGet + " /api/v3/order", http.MethodDelete + " /api/v3/order":
			payload = readSpotFixture(t, "order.json")
		case http.MethodGet + " /api/v3/openOrders", http.MethodGet + " /api/v3/allOrders":
			payload = append(append([]byte{'['}, readSpotFixture(t, "order.json")...), ']')
		case http.MethodGet + " /api/v3/userTrades":
			payload = readSpotFixture(t, "user_trades.json")
		case http.MethodDelete + " /api/v3/allOpenOrders":
			payload = readSpotFixture(t, "cancel_all.json")
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return fixtureHTTPResponse(request, http.StatusOK, payload), nil
	})

	if _, err := client.QueryOrder(context.Background(), OrderQuery{Symbol: "ASTERUSDT", OrigClientOrderID: "fixture-spot-001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenOrders(context.Background(), OpenOrdersQuery{Symbol: "ASTERUSDT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AllOrders(context.Background(), AllOrdersQuery{Symbol: "ASTERUSDT", OrderID: &orderID, Limit: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserTrades(context.Background(), UserTradesQuery{Symbol: "ASTERUSDT", OrderID: &orderID, Limit: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CancelOrder(context.Background(), CancelOrderParams{Symbol: "ASTERUSDT", OrigClientOrderID: "fixture-spot-001"}); err != nil {
		t.Fatal(err)
	}
	result, err := client.CancelAllOpenOrders(context.Background(), CancelAllOrdersParams{Symbol: "ASTERUSDT"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 {
		t.Fatalf("cancel-all result = %+v", result)
	}
	if requests.Load() != 6 {
		t.Fatalf("requests = %d, want 6", requests.Load())
	}
}

func TestSpotV3ListenKeyLifecycleRoutes(t *testing.T) {
	var methods []string
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		methods = append(methods, request.Method)
		assertRequest(t, request, request.Method, "/api/v3/listenKey")
		assertSignedQuery(t, request)
		if request.Method != http.MethodPost && request.URL.Query().Get("listenKey") != "fixture-listen-key" {
			t.Fatalf("listenKey query = %q", request.URL.Query().Get("listenKey"))
		}
		payload := []byte(`{}`)
		if request.Method == http.MethodPost {
			payload = readSpotFixture(t, "listen_key.json")
		}
		return fixtureHTTPResponse(request, http.StatusOK, payload), nil
	})

	listenKey, err := client.CreateListenKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if listenKey != "fixture-listen-key" {
		t.Fatalf("listen key = %q", listenKey)
	}
	if err := client.KeepAliveListenKey(context.Background(), listenKey); err != nil {
		t.Fatal(err)
	}
	if err := client.CloseListenKey(context.Background(), listenKey); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(methods, ","); got != "POST,PUT,DELETE" {
		t.Fatalf("methods = %s", got)
	}
}

func TestSpotV3VenueErrorFixtureIsTyped(t *testing.T) {
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		return fixtureHTTPResponse(request, http.StatusBadRequest, readSpotFixture(t, "error_invalid_symbol.json")), nil
	})
	_, err := client.Depth(context.Background(), "ASTERUSDT", 10)
	var venueErr *astercommon.VenueError
	if !errors.As(err, &venueErr) || venueErr.Code() != -1121 || venueErr.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestSpotClientRejectsRedirectWithoutFollowing(t *testing.T) {
	var calls atomic.Int64
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		if call == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://sapi.asterdex.com/api/v3/depth?symbol=ASTERUSDT"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}
		return fixtureHTTPResponse(request, http.StatusOK, readSpotFixture(t, "depth.json")), nil
	})

	if _, err := client.Depth(context.Background(), "ASTERUSDT", 10); err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want 1", calls.Load())
	}
}

func TestSpotPlaceOrderDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	var calls atomic.Int64
	client := newSpotFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("injected timeout after write")
	})
	_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol: "ASTERUSDT", Side: "BUY", Type: "LIMIT", TimeInForce: "GTC",
		Quantity: "1", Price: "1", NewClientOrderID: "fixture-ambiguous-001",
	})
	var transportErr *astercommon.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("transport calls = %d, want one un-retried write", calls.Load())
	}
}

func newSpotFixtureClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatal(err)
	}
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	}, astercommon.WithClock(astercommon.ClockFunc(func() time.Time {
		return time.UnixMicro(1_783_641_600_000_000)
	})))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(profile, security)
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: transport})
	return client
}

func readSpotFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/v3/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func fixtureHTTPResponse(request *http.Request, status int, payload []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
		Request:    request,
	}
}

func assertRequest(t *testing.T, request *http.Request, method, path string) {
	t.Helper()
	if request.Method != method || request.URL.Path != path {
		t.Fatalf("request = %s %s, want %s %s", request.Method, request.URL.Path, method, path)
	}
}

func assertSignedQuery(t *testing.T, request *http.Request) {
	t.Helper()
	query := request.URL.Query()
	for _, key := range []string{"user", "signer", "nonce", "timestamp", "signature"} {
		if query.Get(key) == "" {
			t.Errorf("signed query missing %s: %s", key, request.URL.RawQuery)
		}
	}
}
