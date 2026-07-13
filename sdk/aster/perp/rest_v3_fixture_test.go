package perp

import (
	"context"
	"encoding/json"
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

func TestPerpV3FixtureModels(t *testing.T) {
	t.Run("exchange info", func(t *testing.T) {
		client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			assertPerpRequest(t, request, http.MethodGet, "/fapi/v3/exchangeInfo")
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "exchange_info.json")), nil
		})
		got, err := client.ExchangeInfo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Assets) != 1 || got.Assets[0].Asset != "USDT" || got.Assets[0].AutoAssetExchange == nil {
			t.Fatalf("assets = %+v", got.Assets)
		}
		var numeric AssetInfo
		if err := json.Unmarshal([]byte(`{"asset":"USDT","marginAvailable":true,"autoAssetExchange":0}`), &numeric); err != nil {
			t.Fatalf("numeric autoAssetExchange: %v", err)
		}
		if numeric.AutoAssetExchange == nil || numeric.AutoAssetExchange.String() != "0" {
			t.Fatalf("numeric autoAssetExchange = %v", numeric.AutoAssetExchange)
		}
		if len(got.Symbols) != 1 {
			t.Fatalf("symbols = %d", len(got.Symbols))
		}
		symbol := got.Symbols[0]
		if symbol.ContractType != "PERPETUAL" || symbol.MarginAsset != "USDT" || len(symbol.OrderTypes) != 2 {
			t.Fatalf("symbol = %+v", symbol)
		}
		if len(symbol.Filters) != 3 || symbol.Filters[0].TickSize != "0.0001" || symbol.Filters[1].StepSize != "0.01" {
			t.Fatalf("filters = %+v", symbol.Filters)
		}
		var compatible SymbolInfo
		if err := json.Unmarshal([]byte(`{"symbol":"ASTERUSDT","orderTypes":["LIMIT"]}`), &compatible); err != nil {
			t.Fatal(err)
		}
		if len(compatible.OrderTypes) != 1 || compatible.OrderTypes[0] != "LIMIT" {
			t.Fatalf("compatible orderTypes = %+v", compatible.OrderTypes)
		}
	})

	t.Run("depth", func(t *testing.T) {
		client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "depth.json")), nil
		})
		got, err := client.Depth(context.Background(), "asterusdt", 100)
		if err != nil {
			t.Fatal(err)
		}
		if got.LastUpdateID != 2027024 || got.EventTime != 1783641600005 || got.TransactionTime != 1783641600004 {
			t.Fatalf("depth = %+v", got)
		}
	})

	t.Run("account and one-way position", func(t *testing.T) {
		client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			switch request.URL.Path {
			case "/fapi/v3/accountWithJoinMargin":
				return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "account.json")), nil
			case "/fapi/v3/balance":
				return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "balance.json")), nil
			case "/fapi/v3/positionRisk":
				return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "position_risk.json")), nil
			case "/fapi/v3/positionSide/dual":
				return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "position_mode.json")), nil
			default:
				t.Fatalf("unexpected path %s", request.URL.Path)
				return nil, nil
			}
		})
		account, err := client.GetAccount(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if account.AvailableBalance != "492.50000000" || account.TotalCrossWalletBalance != "500.00000000" || account.TotalCrossUnPnl != "5.00000000" {
			t.Fatalf("account totals = %+v", account)
		}
		if len(account.Positions) != 1 || account.Positions[0].PositionSide != "BOTH" {
			t.Fatalf("account positions = %+v", account.Positions)
		}
		balances, err := client.GetBalance(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(balances) != 1 || !balances[0].MarginAvailable || balances[0].UpdateTime == 0 {
			t.Fatalf("balances = %+v", balances)
		}
		positions, err := client.GetPositionRisk(context.Background(), "ASTERUSDT")
		if err != nil {
			t.Fatal(err)
		}
		if len(positions) != 1 || positions[0].PositionSide != "BOTH" || positions[0].PositionAmt != "100.00" {
			t.Fatalf("positions = %+v", positions)
		}
		mode, err := client.GetPositionMode(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if mode.DualSidePosition {
			t.Fatal("fixture must represent one-way mode")
		}
	})

	t.Run("user trades", func(t *testing.T) {
		client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "user_trades.json")), nil
		})
		got, err := client.UserTrades(context.Background(), UserTradesQuery{Symbol: "ASTERUSDT"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[0].Buyer || got[0].Maker || got[0].PositionSide != "BOTH" ||
			got[0].Commission != "0.00500000" || got[0].CommissionAsset != "USDT" || got[0].RealizedPnl != "0.00000000" {
			t.Fatalf("trades = %+v", got)
		}
	})
}

func TestCancelAllResponseCodeAcceptsStringAndNumber(t *testing.T) {
	for _, raw := range []string{`{"code":"200","msg":"ok"}`, `{"code":200,"msg":"ok"}`} {
		var response CancelAllOrdersResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		if response.Code.String() != "200" {
			t.Fatalf("decode %s code=%q, want 200", raw, response.Code)
		}
	}
}

func TestPerpV3PlaceOrderRequestMatrix(t *testing.T) {
	tests := []struct {
		name       string
		params     PlaceOrderParams
		want       map[string]string
		wantAbsent []string
	}{
		{
			name:       "limit gtc",
			params:     PlaceOrderParams{Symbol: "asterusdt", Side: "BUY", Type: OrderType_LIMIT, TimeInForce: TimeInForce_GTC, Quantity: "10", Price: "1.24", NewClientOrderID: "fixture-gtc"},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "LIMIT", "timeInForce": "GTC", "quantity": "10", "price": "1.24", "newClientOrderId": "fixture-gtc"},
			wantAbsent: []string{"reduceOnly", "positionSide"},
		},
		{
			name:   "limit ioc",
			params: PlaceOrderParams{Symbol: "ASTERUSDT", Side: "SELL", Type: OrderType_LIMIT, TimeInForce: TimeInForce_IOC, Quantity: "2", Price: "1.25"},
			want:   map[string]string{"symbol": "ASTERUSDT", "side": "SELL", "type": "LIMIT", "timeInForce": "IOC", "quantity": "2", "price": "1.25"},
		},
		{
			name:   "post only gtx",
			params: PlaceOrderParams{Symbol: "ASTERUSDT", Side: "BUY", Type: OrderType_LIMIT, TimeInForce: TimeInForce_GTX, Quantity: "3", Price: "1.20"},
			want:   map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "LIMIT", "timeInForce": "GTX", "quantity": "3", "price": "1.20"},
		},
		{
			name:       "market",
			params:     PlaceOrderParams{Symbol: "ASTERUSDT", Side: "BUY", Type: OrderType_MARKET, Quantity: "5"},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "BUY", "type": "MARKET", "quantity": "5"},
			wantAbsent: []string{"price", "timeInForce", "reduceOnly"},
		},
		{
			name:       "reduce only close",
			params:     PlaceOrderParams{Symbol: "ASTERUSDT", Side: "SELL", Type: OrderType_MARKET, Quantity: "5", ReduceOnly: true},
			want:       map[string]string{"symbol": "ASTERUSDT", "side": "SELL", "type": "MARKET", "quantity": "5", "reduceOnly": "true"},
			wantAbsent: []string{"positionSide", "closePosition"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
				assertPerpRequest(t, request, http.MethodPost, "/fapi/v3/order")
				assertPerpSignedQuery(t, request)
				if request.Body != nil && request.Body != http.NoBody {
					body, err := io.ReadAll(request.Body)
					if err != nil {
						t.Fatal(err)
					}
					if len(body) != 0 {
						t.Fatalf("unexpected body %q", body)
					}
				}
				query := request.URL.Query()
				for key, value := range test.want {
					if got := query.Get(key); got != value {
						t.Errorf("%s = %q, want %q; query=%s", key, got, value, request.URL.RawQuery)
					}
				}
				for _, key := range test.wantAbsent {
					if _, exists := query[key]; exists {
						t.Errorf("unexpected %s in query %s", key, request.URL.RawQuery)
					}
				}
				return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "order.json")), nil
			})
			order, err := client.PlaceOrder(context.Background(), test.params)
			if err != nil {
				t.Fatal(err)
			}
			if order.OrderID != 30001 || order.PositionSide != "BOTH" || order.UpdateTime != 1783641600000 {
				t.Fatalf("order = %+v", order)
			}
		})
	}
}

func TestPerpV3PlaceOrderRejectsInvalidModeCombinationsBeforeTransport(t *testing.T) {
	tests := []struct {
		name   string
		params PlaceOrderParams
	}{
		{
			name: "close position with quantity",
			params: PlaceOrderParams{
				Symbol: "ASTERUSDT", Side: "SELL", Type: OrderType_STOP_MARKET,
				Quantity: "1", StopPrice: "1.20", ClosePosition: true,
			},
		},
		{
			name: "close position with reduce only",
			params: PlaceOrderParams{
				Symbol: "ASTERUSDT", Side: "SELL", Type: OrderType_STOP_MARKET,
				StopPrice: "1.20", ClosePosition: true, ReduceOnly: true,
			},
		},
		{
			name: "reduce only with hedge side",
			params: PlaceOrderParams{
				Symbol: "ASTERUSDT", Side: "SELL", Type: OrderType_MARKET,
				Quantity: "1", ReduceOnly: true, PositionSide: "LONG",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, fmt.Errorf("transport must not be called")
			})
			if _, err := client.PlaceOrder(context.Background(), test.params); err == nil {
				t.Fatal("invalid order combination unexpectedly succeeded")
			}
			if calls.Load() != 0 {
				t.Fatalf("transport calls = %d, want 0", calls.Load())
			}
		})
	}
}

func TestPerpV3OrderReportAndCancelRoutes(t *testing.T) {
	orderID := int64(30001)
	limit := 100
	var requests atomic.Int64
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		assertPerpSignedQuery(t, request)
		var payload []byte
		switch request.Method + " " + request.URL.Path {
		case http.MethodGet + " /fapi/v3/order":
			payload = readPerpFixture(t, "order.json")
		case http.MethodDelete + " /fapi/v3/order":
			payload = readPerpFixture(t, "order_canceled.json")
		case http.MethodGet + " /fapi/v3/openOrders", http.MethodGet + " /fapi/v3/allOrders":
			payload = append(append([]byte{'['}, readPerpFixture(t, "order.json")...), ']')
		case http.MethodGet + " /fapi/v3/userTrades":
			payload = readPerpFixture(t, "user_trades.json")
		case http.MethodDelete + " /fapi/v3/allOpenOrders":
			payload = readPerpFixture(t, "cancel_all.json")
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return perpFixtureResponse(request, http.StatusOK, payload), nil
	})

	if _, err := client.QueryOrder(context.Background(), OrderQuery{Symbol: "ASTERUSDT", OrigClientOrderID: "fixture-perp-001"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.OpenOrders(context.Background(), OpenOrdersQuery{Symbol: "ASTERUSDT"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AllOrders(context.Background(), AllOrdersQuery{Symbol: "ASTERUSDT", OrderID: &orderID, Limit: &limit}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UserTrades(context.Background(), UserTradesQuery{Symbol: "ASTERUSDT", Limit: &limit}); err != nil {
		t.Fatal(err)
	}
	canceled, err := client.CancelOrder(context.Background(), CancelOrderParams{Symbol: "ASTERUSDT", OrigClientOrderID: "fixture-perp-001"})
	if err != nil {
		t.Fatal(err)
	}
	if canceled.Status != "CANCELED" || canceled.UpdateTime != 1783641600500 {
		t.Fatalf("canceled order = %+v", canceled)
	}
	result, err := client.CancelAllOpenOrders(context.Background(), CancelAllOrdersParams{Symbol: "ASTERUSDT"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != "200" {
		t.Fatalf("cancel-all = %+v", result)
	}
	if requests.Load() != 6 {
		t.Fatalf("requests = %d, want 6", requests.Load())
	}
}

func TestPerpV3ReferenceAndProbeBackedOpenInterest(t *testing.T) {
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/fapi/v3/premiumIndex":
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "premium_index.json")), nil
		case "/fapi/v3/fundingRate":
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "funding_rate_history.json")), nil
		case "/fapi/v3/openInterest":
			return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "open_interest.json")), nil
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
			return nil, nil
		}
	})
	index, err := client.GetFundingRate(context.Background(), "ASTERUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if index.MarkPrice != "1.2500" || index.IndexPrice != "1.2480" || index.InterestRate != "0.00010000" || index.LastFundingRate != "0.00010000" {
		t.Fatalf("premium index = %+v", index)
	}
	history, err := client.GetFundingRateHistory(context.Background(), "ASTERUSDT", 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].FundingRate != "0.00010000" {
		t.Fatalf("funding history = %+v", history)
	}
	oi, err := client.GetOpenInterest(context.Background(), "ASTERUSDT")
	if err != nil {
		t.Fatal(err)
	}
	if oi.Symbol != "ASTERUSDT" || oi.OpenInterest != "123456.78" || oi.Time == 0 {
		t.Fatalf("open interest = %+v", oi)
	}
}

func TestPerpV3MarkAndIndexPriceKlinesUseDocumentedRoutes(t *testing.T) {
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("interval") != "5m" || query.Get("limit") != "12" || query.Get("startTime") != "1783641300000" || query.Get("endTime") != "1783641599999" {
			t.Fatalf("query = %s", request.URL.RawQuery)
		}
		switch request.URL.Path {
		case "/fapi/v3/markPriceKlines":
			if query.Get("symbol") != "ASTERUSDT" || query.Get("pair") != "" {
				t.Fatalf("mark-price query = %s", request.URL.RawQuery)
			}
		case "/fapi/v3/indexPriceKlines":
			if query.Get("pair") != "ASTERUSDT" || query.Get("symbol") != "" {
				t.Fatalf("index-price query = %s", request.URL.RawQuery)
			}
		default:
			t.Fatalf("unexpected path %s", request.URL.Path)
		}
		return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "reference_klines.json")), nil
	})

	query := ReferenceKlinesQuery{
		Symbol: "asterusdt", Interval: "5m", Limit: 12,
		StartTime: 1783641300000, EndTime: 1783641599999,
	}
	mark, err := client.MarkPriceKlines(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	index, err := client.IndexPriceKlines(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	for name, rows := range map[string][]KlineResponse{"mark": mark, "index": index} {
		if len(rows) != 1 || len(rows[0]) != 12 || rows[0][4] != "1.2500" {
			t.Fatalf("%s klines = %#v", name, rows)
		}
	}
}

func TestPerpOpenInterestMissingOrIncompatibleIsTypedReleaseBlocker(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		payload string
	}{
		{name: "missing route", status: http.StatusNotFound, payload: `{"code":-1020,"msg":"Unsupported operation."}`},
		{name: "incompatible payload", status: http.StatusOK, payload: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				if request.URL.Path != "/fapi/v3/openInterest" {
					t.Fatalf("fallback path used: %s", request.URL.Path)
				}
				return perpFixtureResponse(request, test.status, []byte(test.payload)), nil
			})
			_, err := client.GetOpenInterest(context.Background(), "ASTERUSDT")
			var unavailable *OpenInterestUnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("error = %T %v", err, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want one V3 probe", calls.Load())
			}
		})
	}
}

func TestPerpClientRejectsRedirectWithoutFollowing(t *testing.T) {
	var calls atomic.Int64
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return &http.Response{
				StatusCode: http.StatusFound,
				Header:     http.Header{"Location": []string{"https://fapi.asterdex.com/fapi/v3/depth?symbol=ASTERUSDT"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    request,
			}, nil
		}
		return perpFixtureResponse(request, http.StatusOK, readPerpFixture(t, "depth.json")), nil
	})
	if _, err := client.Depth(context.Background(), "ASTERUSDT", 10); err == nil {
		t.Fatal("redirect unexpectedly succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
}

func TestPerpPlaceOrderDoesNotRetryAmbiguousTransportFailure(t *testing.T) {
	var calls atomic.Int64
	client := newPerpFixtureClient(t, func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("injected timeout after write")
	})
	_, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol: "ASTERUSDT", Side: "BUY", Type: OrderType_LIMIT, TimeInForce: TimeInForce_GTC,
		Quantity: "1", Price: "1", NewClientOrderID: "fixture-ambiguous-001",
	})
	var transportErr *astercommon.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("error = %T %v", err, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want one un-retried write", calls.Load())
	}
}

func newPerpFixtureClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
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

func readPerpFixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile("testdata/v3/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func perpFixtureResponse(request *http.Request, status int, payload []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(payload))),
		Request:    request,
	}
}

func assertPerpRequest(t *testing.T, request *http.Request, method, path string) {
	t.Helper()
	if request.Method != method || request.URL.Path != path {
		t.Fatalf("request = %s %s, want %s %s", request.Method, request.URL.Path, method, path)
	}
}

func assertPerpSignedQuery(t *testing.T, request *http.Request) {
	t.Helper()
	query := request.URL.Query()
	for _, key := range []string{"user", "signer", "nonce", "timestamp", "signature"} {
		if query.Get(key) == "" {
			t.Errorf("signed query missing %s: %s", key, request.URL.RawQuery)
		}
	}
}
