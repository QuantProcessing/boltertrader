package sdk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testHTTPClient(t *testing.T, fn func(*http.Request) (int, string)) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, body := fn(req)
		return &http.Response{
			StatusCode: status,
			Status:     http.StatusText(status),
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})}
}

func TestClientListCurrencyPairsDecodesDirectArray(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test/api/v4")
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v4/spot/currency_pairs" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
		}
		return http.StatusOK, `[{"id":"BTC_USDT","base":"BTC","quote":"USDT","amount_precision":6,"precision":2,"trade_status":"tradable"}]`
	}))

	pairs, err := client.ListCurrencyPairs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 1 || pairs[0].ID != "BTC_USDT" || pairs[0].TradeStatus != "tradable" {
		t.Fatalf("unexpected pairs: %+v", pairs)
	}
}

func TestClientSignsPrivateGetWithGateHeaders(t *testing.T) {
	fixed := time.Unix(1700000000, 0)
	client := NewClient().
		WithBaseURL("https://example.test/api/v4").
		WithCredentials("key", "secret").
		WithClock(func() time.Time { return fixed })
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.URL.Path != "/api/v4/spot/accounts" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		if req.URL.RawQuery != "currency=USDT" {
			t.Fatalf("unexpected query %q", req.URL.RawQuery)
		}
		wantPayload := buildSigningPayload(http.MethodGet, req.URL.Path, req.URL.RawQuery, "", "1700000000")
		if got, want := req.Header.Get("KEY"), "key"; got != want {
			t.Fatalf("KEY=%q want %q", got, want)
		}
		if got, want := req.Header.Get("Timestamp"), "1700000000"; got != want {
			t.Fatalf("Timestamp=%q want %q", got, want)
		}
		if got, want := req.Header.Get("SIGN"), sign("secret", wantPayload); got != want {
			t.Fatalf("SIGN=%q want %q", got, want)
		}
		return http.StatusOK, `[{"currency":"USDT","available":"10","locked":"1","update_id":99}]`
	}))

	accounts, err := client.ListSpotAccounts(context.Background(), "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].Available != "10" || accounts[0].Locked != "1" {
		t.Fatalf("unexpected accounts: %+v", accounts)
	}
}

func TestClientCreateSpotOrderPostsOfficialBody(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test/api/v4").WithCredentials("key", "secret")
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/v4/spot/orders" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
		}
		body, _ := io.ReadAll(req.Body)
		text := string(body)
		for _, want := range []string{`"currency_pair":"ETH_USDT"`, `"side":"buy"`, `"amount":"0.01"`, `"price":"1000"`} {
			if !strings.Contains(text, want) {
				t.Fatalf("body %s missing %s", text, want)
			}
		}
		return http.StatusOK, `{"id":"123","currency_pair":"ETH_USDT","side":"buy","amount":"0.01","price":"1000","status":"open","create_time_ms":1783484986705}`
	}))

	order, err := client.CreateSpotOrder(context.Background(), Order{
		CurrencyPair: "ETH_USDT",
		Type:         "limit",
		Side:         "buy",
		Amount:       "0.01",
		Price:        "1000",
		TimeInForce:  "gtc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if order.ID != "123" || order.Status != "open" || order.CreateTimeMS != "1783484986705" {
		t.Fatalf("unexpected order: %+v", order)
	}
}

func TestClientListAllSpotOpenOrdersUsesAggregateEndpoint(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test/api/v4").WithCredentials("key", "secret")
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.Method != http.MethodGet || req.URL.Path != "/api/v4/spot/open_orders" {
			t.Fatalf("unexpected request %s %s", req.Method, req.URL.String())
		}
		if got, want := req.URL.RawQuery, "account=spot&limit=100&page=1"; got != want {
			t.Fatalf("query=%q want %q", got, want)
		}
		return http.StatusOK, `[{"currency_pair":"ETH_USDT","total":1,"orders":[{"id":"123","currency_pair":"ETH_USDT","side":"buy","amount":"0.01","price":"1000","status":"open"}]}]`
	}))

	groups, err := client.ListAllSpotOpenOrders(context.Background(), 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].CurrencyPair != "ETH_USDT" || len(groups[0].Orders) != 1 || groups[0].Orders[0].ID != "123" {
		t.Fatalf("unexpected open orders: %+v", groups)
	}
}

func TestClientFuturesPathsUseSettleSegment(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test/api/v4").WithCredentials("key", "secret")
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		if req.URL.Path != "/api/v4/futures/usdt/accounts" {
			t.Fatalf("unexpected path %s", req.URL.Path)
		}
		return http.StatusOK, `{"user":42,"total":"100","available":"90","currency":"USDT","margin_mode":1,"in_dual_mode":false,"position_mode":"single"}`
	}))

	account, err := client.GetFuturesAccount(context.Background(), SettleUSDT)
	if err != nil {
		t.Fatal(err)
	}
	if account.User != 42 || account.Available != "90" || account.Currency != "USDT" || account.MarginMode != "1" || account.InDualMode || account.PositionMode != "single" {
		t.Fatalf("unexpected futures account: %+v", account)
	}
}

func TestClientParsesGateAPIError(t *testing.T) {
	client := NewClient().WithBaseURL("https://example.test/api/v4")
	client.WithHTTPClient(testHTTPClient(t, func(req *http.Request) (int, string) {
		return http.StatusBadRequest, `{"label":"INVALID_PARAM_VALUE","message":"bad currency_pair"}`
	}))

	_, err := client.GetCurrencyPair(context.Background(), "BAD")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type %T, want *APIError", err)
	}
	if apiErr.Label != "INVALID_PARAM_VALUE" || apiErr.Message != "bad currency_pair" {
		t.Fatalf("unexpected api error: %+v", apiErr)
	}
}
