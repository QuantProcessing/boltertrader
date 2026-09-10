package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
)

const testToken = "offline-gateway-client-token-at-least-32-bytes"
const testInstrument = "binance:live:spot:BTC-USDT"

func requestFixture() PlaceOrderRequest {
	return PlaceOrderRequest{AccountID: "account-a", RouteID: "spot", InstrumentID: testInstrument, IdempotencyKey: "same-intent", Side: "buy", Type: "market", Quantity: "0.001", RuntimeID: "runtime-a"}
}
func orderFixture() Order {
	return Order{AccountID: "account-a", InstrumentID: testInstrument, CommandID: "command-a", GatewayOrderID: "order-a", ClientOrderID: "42", State: "open", ResourceVersion: 1}
}
func clientAt(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: server.URL, Token: testToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func errorKind(t *testing.T, err error, kind string) *Error {
	t.Helper()
	var result *Error
	if !errors.As(err, &result) || result.Kind != kind {
		t.Fatalf("error=%v want %s", err, kind)
	}
	return result
}
func write(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestPlacementTimeoutRecoversOnlyByOriginalKey(t *testing.T) {
	var writes, reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Error("missing service credential")
		}
		if r.Method == http.MethodPost {
			writes.Add(1)
			var request PlaceOrderRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.IdempotencyKey != "same-intent" || r.Header.Get("Idempotency-Key") != "same-intent" {
				t.Error("lost original idempotency key")
			}
			// The order exists although the response is lost.
			<-r.Context().Done()
			return
		}
		reads.Add(1)
		if r.URL.Path != "/v1/orders/by-idempotency-key/same-intent" {
			t.Error("wrong recovery URL")
		}
		write(t, w, wire.OrderLookup{IdempotencyKey: "same-intent", AccountID: "account-a", InstrumentID: testInstrument, Order: orderFixture()})
	}))
	defer server.Close()
	c := clientAt(t, server)
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	_, err := c.PlaceOrder(ctx, requestFixture())
	failure := errorKind(t, err, "transport")
	if failure.Outcome != Unknown || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost response=%+v", failure)
	}
	result, err := c.OrderByKey(t.Context(), "same-intent", "account-a", testInstrument)
	if err != nil || result.GatewayOrderID != "order-a" || writes.Load() != 1 || reads.Load() != 1 {
		t.Fatalf("result=%+v writes=%d reads=%d err=%v", result, writes.Load(), reads.Load(), err)
	}
}
func TestOrderResponsesAllowAdditionsButRejectWrongScope(t *testing.T) {
	for _, scenario := range []string{"additive", "account", "instrument", "order", "state", "version", "duplicate", "trailing", "null"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				result := orderFixture()
				switch scenario {
				case "account":
					result.AccountID = "account-b"
				case "instrument":
					result.InstrumentID = "different"
				case "order":
					result.GatewayOrderID = "order-b"
				case "state":
					result.State = "new-state"
				case "version":
					result.ResourceVersion = 0
				}
				raw, _ := json.Marshal(result)
				switch scenario {
				case "additive":
					raw = append(raw[:len(raw)-1], []byte(`,"future":{"flag":true}}`)...)
				case "duplicate":
					raw = append(raw[:len(raw)-1], []byte(`,"Account_ID":"other"}`)...)
				case "trailing":
					raw = append(raw, []byte(` {}`)...)
				case "null":
					raw = []byte("null")
				}
				w.Write(raw)
			}))
			defer server.Close()
			result, err := clientAt(t, server).Order(t.Context(), OrderScope{"account-a", testInstrument, "order-a"})
			if scenario == "additive" {
				if err != nil || result.GatewayOrderID != "order-a" {
					t.Fatalf("%+v %v", result, err)
				}
			} else {
				errorKind(t, err, "protocol")
			}
		})
	}
}
func TestHTTPErrorPreservesSafeDetailsAndWriteUncertainty(t *testing.T) {
	for _, status := range []int{400, 401, 404, 408, 409, 429, 500, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(status)
				write(t, w, wire.ErrorEnvelope{Error: wire.ErrorBody{Code: "venue_unavailable", Message: "safe detail", CorrelationID: "correlation-a", Retryable: true}})
			}))
			defer server.Close()
			_, err := clientAt(t, server).PlaceOrder(t.Context(), requestFixture())
			e := errorKind(t, err, "http")
			want := Unknown
			if status < 500 && status != 408 {
				want = Rejected
			}
			if e.Outcome != want || e.StatusCode != status || e.Code != "venue_unavailable" || e.Message != "safe detail" || e.CorrelationID != "correlation-a" || e.RetryAfter != 3*time.Second {
				t.Fatalf("error details=%#v", e)
			}
		})
	}
}
func TestInvalidRequestsAndCanceledContextDoNotReachTransport(t *testing.T) {
	var calls atomic.Int64
	c, err := New(Config{URL: "http://127.0.0.1:1", Token: testToken, HTTPClient: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, errors.New("unexpected") })}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func(*PlaceOrderRequest){func(r *PlaceOrderRequest) { r.Quantity = "-1" }, func(r *PlaceOrderRequest) { r.LimitPrice = "1" }, func(r *PlaceOrderRequest) { r.RuntimeID = "" }, func(r *PlaceOrderRequest) { r.Type = "limit" }, func(r *PlaceOrderRequest) { r.IdempotencyKey = "\nother" }, func(r *PlaceOrderRequest) { r.Quantity = "1e999999" }} {
		r := requestFixture()
		mutation(&r)
		_, err = c.PlaceOrder(t.Context(), r)
		if errorKind(t, err, "invalid_request").Outcome != NotAttempted {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = c.PlaceOrder(ctx, requestFixture())
	if errorKind(t, err, "transport").Outcome != NotAttempted || !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestTransportDoesNotReplayRedirectOrLeakToken(t *testing.T) {
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	_, err := clientAt(t, source).PlaceOrder(t.Context(), requestFixture())
	if errorKind(t, err, "http").Outcome != Unknown || redirected.Load() != 0 {
		t.Fatal("followed redirect")
	}
	calls := 0
	c, _ := New(Config{URL: "http://127.0.0.1:1", Token: testToken, HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.GetBody != nil {
			t.Error("write body permits implicit transport replay")
		}
		return nil, fmt.Errorf("failed secret %s", testToken)
	})}})
	_, err = c.PlaceOrder(t.Context(), requestFixture())
	if calls != 1 || strings.Contains(fmt.Sprintf("%v %#v", err, err), testToken) {
		t.Fatal("transport replay or token leak")
	}
}
func TestResponseLimitDetectsTruncationEvenAfterValidJSON(t *testing.T) {
	result, _ := json.Marshal(orderFixture())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(append(result, []byte(strings.Repeat(" ", 1024))...))
	}))
	defer server.Close()
	c, err := New(Config{URL: server.URL, Token: testToken, ResponseLimit: int64(len(result)), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Order(t.Context(), OrderScope{"account-a", testInstrument, "order-a"})
	errorKind(t, err, "protocol")
}
func TestSeparateAccountListenerAndEmptyPositionFreshness(t *testing.T) {
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer internal-token-with-at-least-32-bytes" {
			t.Error("wrong internal credential")
		}
		account := Account{ID: "account-a", Venue: "binance", NativeEnvironment: "live", Status: "active", Routes: []wire.AccountRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1"}}}
		switch r.URL.Path {
		case "/internal/v1/accounts":
			write(t, w, map[string]any{"data": map[string]any{"accounts": []Account{account}}})
		case "/internal/v1/accounts/account-a":
			write(t, w, map[string]any{"data": map[string]any{"account": account}})
		default:
			write(t, w, wire.Envelope[AccountConnection]{Data: AccountConnection{AccountID: "account-a", RuntimeID: "runtime-a", Connected: false, ObservedAt: time.Now()}})
		}
	}))
	defer internal.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			t.Error("internal request on public listener")
		}
		write(t, w, wire.Envelope[PositionSnapshot]{Data: PositionSnapshot{AccountID: "account-a", Freshness: "unavailable", Items: []wire.PositionResource{}}})
	}))
	defer public.Close()
	c, err := New(Config{URL: public.URL, Token: testToken, InternalURL: internal.URL, InternalToken: "internal-token-with-at-least-32-bytes"})
	if err != nil {
		t.Fatal(err)
	}
	if accounts, err := c.Accounts(t.Context()); err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%+v %v", accounts, err)
	}
	if _, err := c.Account(t.Context(), "account-a"); err != nil {
		t.Fatal(err)
	}
	if state, err := c.AccountConnection(t.Context(), "account-a"); err != nil || state.Connected {
		t.Fatalf("connection=%+v %v", state, err)
	}
	if snapshot, err := c.PositionSnapshot(t.Context(), "account-a"); err != nil || snapshot.Freshness != "unavailable" {
		t.Fatalf("unavailable became flat: %+v %v", snapshot, err)
	}
}
func fillFixture() FillResource {
	return FillResource{AccountID: "account-a", InstrumentID: testInstrument, GatewayOrderID: "order-a", Product: "spot", BaseAsset: "BTC", SettlementAsset: "USDT", Fill: wire.Fill{FillID: "fill-a", Side: "buy", Price: "100", Quantity: "1", Fee: "-0.001", FeeAsset: "BNB", Time: time.Now().UTC()}}
}
func TestFillPaginationScopeRebatesAndCycle(t *testing.T) {
	for _, scenario := range []string{"two_pages", "cycle", "wrong_account", "wrong_order", "bad_fee", "missing_currency", "later_failure"} {
		t.Run(scenario, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("gateway_order_id") != "order-a" {
					t.Error("missing order scope")
				}
				item := fillFixture()
				cursor := ""
				if calls == 1 || scenario == "cycle" {
					cursor = "cursor-a"
				} else {
					item.Fill.FillID = "fill-b"
				}
				switch scenario {
				case "wrong_account":
					item.AccountID = "other"
				case "wrong_order":
					item.GatewayOrderID = "other"
				case "bad_fee":
					item.Fill.Fee = "invalid"
				case "missing_currency":
					item.Fill.FeeAsset = ""
				case "later_failure":
					if calls == 2 {
						w.WriteHeader(503)
						return
					}
				}
				write(t, w, wire.Page[FillResource]{Items: []FillResource{item}, NextCursor: cursor})
			}))
			defer server.Close()
			items, err := clientAt(t, server).Fills(t.Context(), OrderScope{"account-a", testInstrument, "order-a"})
			if scenario == "two_pages" {
				if err != nil || len(items) != 2 || items[0].Fill.Fee != "-0.001" || items[0].Fill.FeeAsset != "BNB" {
					t.Fatalf("%+v %v", items, err)
				}
			} else if err == nil || items != nil {
				t.Fatalf("partial or wrong evidence=%+v %v", items, err)
			}
			if calls > 2 {
				t.Fatalf("unbounded traversal: %d", calls)
			}
		})
	}
}
func TestClientConfigurationRejectsCredentialBearingOrNonHTTPOrigins(t *testing.T) {
	for _, url := range []string{"ftp://127.0.0.1", "http://user:password@127.0.0.1", "http://127.0.0.1?token=secret", "http://127.0.0.1/api", "http://example.com", "http://127.0.0.1#fragment"} {
		if _, err := New(Config{URL: url, Token: testToken}); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
}
func TestResponseBodyAlwaysCloses(t *testing.T) {
	closed := false
	c, _ := New(Config{URL: "http://127.0.0.1:1", Token: testToken, HTTPClient: &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: &closingBody{Reader: strings.NewReader("invalid"), closed: &closed}}, nil
	})}})
	_, _ = c.Order(t.Context(), OrderScope{"account-a", testInstrument, "order-a"})
	if !closed {
		t.Fatal("response leaked")
	}
}

type closingBody struct {
	io.Reader
	closed *bool
}

func (b *closingBody) Close() error { *b.closed = true; return nil }

func TestOpaqueNamespacedInstrumentIsEncodedExactlyOnce(t *testing.T) {
	const id = "hyperliquid:live:perp:USDC:xyz%3ATSLA-USDC"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("instrument_id") != id {
			t.Errorf("altered identity: %s", r.URL.RawQuery)
		}
		write(t, w, MarketSnapshot{InstrumentID: id, Snapshot: streamData(BestBidOffer, "", 1, 1, wire.MessageSnapshot)})
	}))
	defer server.Close()
	if _, err := clientAt(t, server).MarketSnapshot(t.Context(), MarketTopic{Topic: BestBidOffer, InstrumentID: id}); err != nil {
		t.Fatal(err)
	}
}
func TestBalancesRequireCompleteSnapshotAndValidAmounts(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong_account", "bad_amount", "truncated", "null"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				row := BalanceResource{AccountID: "account-a", Product: "spot", Balance: wire.Balance{Asset: "BTC", Available: "0.001", Locked: "0", Total: "0.001"}}
				if scenario == "wrong_account" {
					row.AccountID = "account-b"
				}
				if scenario == "bad_amount" {
					row.Balance.Total = "NaN"
				}
				items := []BalanceResource{row}
				if scenario == "truncated" {
					items = make([]BalanceResource, 500)
				}
				if scenario == "null" {
					items = nil
				}
				write(t, w, wire.BalanceSnapshot{Items: items, Complete: scenario != "truncated"})
			}))
			defer server.Close()
			items, err := clientAt(t, server).Balances(t.Context(), "account-a")
			if scenario == "valid" {
				if err != nil || len(items) != 1 || items[0].Balance.Total != "0.001" {
					t.Fatalf("%+v %v", items, err)
				}
			} else if err == nil || items != nil {
				t.Fatalf("invalid balance accepted: %+v %v", items, err)
			}
		})
	}
}
func TestOrderListValidatesScopeAndCursor(t *testing.T) {
	for _, scenario := range []string{"valid", "scope", "cursor", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/open-orders" || r.URL.Query().Get("account_id") != "account-a" || r.URL.Query().Get("pagination.cursor") != "last-order" {
					t.Error("wrong order page request")
				}
				row := orderFixture()
				cursor := "next-order"
				if scenario == "scope" {
					row.AccountID = "other"
				}
				if scenario == "cursor" {
					cursor = "last-order"
				}
				items := []Order{row}
				if scenario == "duplicate" {
					items = append(items, row)
				}
				write(t, w, wire.Page[Order]{Items: items, NextCursor: cursor})
			}))
			defer server.Close()
			page, err := clientAt(t, server).Orders(t.Context(), OrdersQuery{AccountID: "account-a", InstrumentID: testInstrument, Cursor: "last-order", Limit: 10, OpenOnly: true})
			if scenario == "valid" {
				if err != nil || page.NextCursor != "next-order" {
					t.Fatalf("%+v %v", page, err)
				}
			} else {
				errorKind(t, err, "protocol")
			}
		})
	}
}

func TestClientFormattingDoesNotExposeServiceTokens(t *testing.T) {
	config := Config{URL: "http://127.0.0.1:1", Token: testToken, InternalURL: "http://127.0.0.1:2", InternalToken: "internal-secret"}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	text := fmt.Sprintf("%v %+v %#v %v %+v %#v", client, client, client, config, config, config)
	if strings.Contains(text, testToken) || strings.Contains(text, "internal-secret") {
		t.Fatal("client formatting exposed service credentials")
	}
}
