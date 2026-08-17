package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/shopspring/decimal"
)

func TestPublicMarketRESTUsesOwnedClientAndRedactsNativeMetadata(t *testing.T) {
	instrumentID, instrument, client, handler := newPublicMarketFixture(t)
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	tests := []string{
		"/v1/funding-rates?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)),
		"/v1/order-book?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)) + "&limit=25",
		"/v1/bbo?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)),
		"/v1/mark-price?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)),
		"/v1/funding-rates/history?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)) +
			"&start=" + url.QueryEscape(start.Format(time.RFC3339)) +
			"&end=" + url.QueryEscape(end.Format(time.RFC3339)) +
			"&pagination.cursor=opaque-cursor&pagination.limit=7",
	}
	for _, path := range tests {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		for _, forbidden := range []string{"native_symbol", "native_id", "BTCUSDT", `"identity"`, `"encoding"`} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s leaked %q in %s", path, forbidden, body)
			}
		}
		if !strings.Contains(body, `"account_id":"acct-p"`) ||
			!strings.Contains(body, `"instrument_id":"`+string(instrumentID)+`"`) {
			t.Fatalf("%s lost account/instrument scope: %s", path, body)
		}
	}
	if client.orderBookCalls != 2 {
		t.Fatalf("OrderBook calls=%d, want 2", client.orderBookCalls)
	}
	if client.lastBookRequest.Limit != defaultOrderBookLimit || client.lastBookRequest.Instrument.Identity.NativeSymbol != instrument.Identity.NativeSymbol {
		t.Fatalf("last BBO OrderBook request=%+v", client.lastBookRequest)
	}
	if client.fundingCalls != 1 {
		t.Fatalf("FundingRate calls=%d, want 1", client.fundingCalls)
	}
	if client.historyCalls != 1 || client.lastHistoryRequest.Limit != 7 ||
		client.lastHistoryRequest.Cursor != "opaque-cursor" ||
		!client.lastHistoryRequest.Start.Equal(start) || !client.lastHistoryRequest.End.Equal(end) {
		t.Fatalf("history request=%+v calls=%d", client.lastHistoryRequest, client.historyCalls)
	}
}

func TestPublicMarketRESTRejectsInvalidScopeAndBoundsBeforeVenueIO(t *testing.T) {
	instrumentID, _, client, handler := newPublicMarketFixture(t)
	paths := []string{
		"/v1/order-book?instrument_id=" + url.QueryEscape(string(instrumentID)),
		"/v1/order-book?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)) + "&limit=501",
		"/v1/funding-rates/history?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)) + "&start=not-time",
		"/v1/funding-rates/history?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID)) +
			"&start=2026-07-02T00%3A00%3A00Z&end=2026-07-01T00%3A00%3A00Z",
		"/v1/mark-price?account_id=missing&instrument_id=" + url.QueryEscape(string(instrumentID)),
	}
	for _, path := range paths {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code < 400 || recorder.Code >= 500 && recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	if client.orderBookCalls != 0 || client.fundingCalls != 0 || client.historyCalls != 0 {
		t.Fatalf("invalid requests reached venue client: %+v", client)
	}
}

func TestPublicMarketRESTRejectsMislabeledVenueResponses(t *testing.T) {
	tests := []struct {
		name   string
		path   func(domain.InstrumentID) string
		mutate func(*fakePublicPerpClient)
	}{
		{
			name: "order book",
			path: func(instrumentID domain.InstrumentID) string {
				return "/v1/order-book?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID))
			},
			mutate: func(client *fakePublicPerpClient) {
				client.book.Instrument.Identity.Symbol = "ETH-USDT"
				client.book.Instrument.Identity.NativeSymbol = "ETHUSDT"
			},
		},
		{
			name: "current funding",
			path: func(instrumentID domain.InstrumentID) string {
				return "/v1/funding-rates?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID))
			},
			mutate: func(client *fakePublicPerpClient) {
				client.funding.NativeSymbol = "ETHUSDT"
			},
		},
		{
			name: "mark price",
			path: func(instrumentID domain.InstrumentID) string {
				return "/v1/mark-price?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID))
			},
			mutate: func(client *fakePublicPerpClient) {
				client.instrument.Identity.Symbol = "ETH-USDT"
				client.instrument.Identity.NativeSymbol = "ETHUSDT"
			},
		},
		{
			name: "funding history",
			path: func(instrumentID domain.InstrumentID) string {
				return "/v1/funding-rates/history?account_id=acct-p&instrument_id=" + url.QueryEscape(string(instrumentID))
			},
			mutate: func(client *fakePublicPerpClient) {
				client.history.Rates[0].NativeSymbol = "ETHUSDT"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instrumentID, _, client, handler := newPublicMarketFixture(t)
			test.mutate(client)
			request := httptest.NewRequest(http.MethodGet, test.path(instrumentID), nil)
			request.Header.Set("Authorization", "Bearer test-token")
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusInternalServerError {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), "ETH") {
				t.Fatalf("mismatched venue identity leaked: %s", recorder.Body.String())
			}
		})
	}
}

func TestPublicMarketMarkPriceDoesNotRequireFundingCapability(t *testing.T) {
	row := capability.StaticMatrix().Rows[1]
	row.Operations.Funding = false
	instrumentID, _, client, handler := newPublicMarketFixtureWithCapability(t, row)

	markRequest := httptest.NewRequest(http.MethodGet,
		"/v1/mark-price?account_id=acct-p&instrument_id="+url.QueryEscape(string(instrumentID)), nil)
	markRequest.Header.Set("Authorization", "Bearer test-token")
	markRecorder := httptest.NewRecorder()
	handler.ServeHTTP(markRecorder, markRequest)
	if markRecorder.Code != http.StatusOK {
		t.Fatalf("mark status=%d body=%s", markRecorder.Code, markRecorder.Body.String())
	}
	assertBody := markRecorder.Body.String()
	if !strings.Contains(assertBody, `"price":"100.5"`) || strings.Contains(assertBody, "native_") {
		t.Fatalf("mark response=%s", assertBody)
	}

	fundingRequest := httptest.NewRequest(http.MethodGet,
		"/v1/funding-rates?account_id=acct-p&instrument_id="+url.QueryEscape(string(instrumentID)), nil)
	fundingRequest.Header.Set("Authorization", "Bearer test-token")
	fundingRecorder := httptest.NewRecorder()
	handler.ServeHTTP(fundingRecorder, fundingRequest)
	if fundingRecorder.Code != http.StatusNotImplemented {
		t.Fatalf("funding status=%d body=%s", fundingRecorder.Code, fundingRecorder.Body.String())
	}
	if client.fundingCalls != 0 {
		t.Fatalf("funding calls=%d, want 0", client.fundingCalls)
	}
}

func TestPublicMarketMarkPricePrefersRESTWhenWebSocketUnavailable(t *testing.T) {
	instrumentID, _, client, handler := newPublicMarketFixture(t)
	client.markWebSocketErr = exchange.ErrUnsupported
	client.restMark = exchange.MarkPriceEvent{
		Instrument: client.instrument,
		Price:      decimal.RequireFromString("101.25"),
		Time:       time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC),
	}

	request := httptest.NewRequest(http.MethodGet,
		"/v1/mark-price?account_id=acct-p&instrument_id="+url.QueryEscape(string(instrumentID)), nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"price":"101.25"`) {
		t.Fatalf("mark response=%s", recorder.Body.String())
	}
	if client.restMarkCalls != 1 {
		t.Fatalf("REST mark calls=%d, want 1", client.restMarkCalls)
	}
	if calls := client.publicCallOrder(); len(calls) != 1 || calls[0] != "mark_price_rest" {
		t.Fatalf("public call order=%v, want REST only", calls)
	}
}

func TestPublicMarketMarkPriceFallsBackToWebSocketWhenRESTUnsupported(t *testing.T) {
	instrumentID, _, client, handler := newPublicMarketFixture(t)
	client.restMarkErr = exchange.ErrUnsupported

	request := httptest.NewRequest(http.MethodGet,
		"/v1/mark-price?account_id=acct-p&instrument_id="+url.QueryEscape(string(instrumentID)), nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if client.restMarkCalls != 1 {
		t.Fatalf("REST mark calls=%d, want 1", client.restMarkCalls)
	}
	if calls := client.publicCallOrder(); len(calls) != 2 ||
		calls[0] != "mark_price_rest" || calls[1] != "watch_mark_price" {
		t.Fatalf("public call order=%v, want REST fallback to WebSocket", calls)
	}
}

func TestPublicMarketWebSocketSendsSnapshotUpdateThenResnapshotOnGap(t *testing.T) {
	instrumentID, _, client, handler := newPublicMarketFixture(t)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	target := websocketURL(server.URL) + "/v1/public/ws?account_id=acct-p&instrument_id=" +
		url.QueryEscape(string(instrumentID)) + "&kind=funding_rate"
	connection, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("dial response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })

	snapshot := readPublicMarketEvent(t, ctx, connection)
	if snapshot.EventType != "snapshot" || snapshot.Kind != "funding_rate" || snapshot.Sequence != 1 ||
		snapshot.BootID == "" || snapshot.EventID == "" || snapshot.AccountID != "acct-p" ||
		snapshot.InstrumentID != string(instrumentID) {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	assertPublicPayloadRedacted(t, snapshot.Payload)
	if calls := client.publicCallOrder(); len(calls) != 2 ||
		calls[0] != "watch_funding" || calls[1] != "funding_snapshot" {
		t.Fatalf("public stream initialization order=%v, want subscription before snapshot", calls)
	}

	client.subscription.events <- exchange.FundingRateEvent{
		Instrument: client.instrument, Rate: decimal.RequireFromString("0.0002"),
		EffectiveAt: time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC),
		NextAt:      time.Date(2026, 7, 2, 16, 0, 0, 0, time.UTC),
	}
	update := readPublicMarketEvent(t, ctx, connection)
	if update.EventType != "update" || update.Sequence != 2 || update.BootID != snapshot.BootID {
		t.Fatalf("update=%+v", update)
	}
	assertPublicPayloadRedacted(t, update.Payload)

	client.subscription.status <- exchange.StreamStatusEvent{
		State: exchange.SubscriptionGap, Phase: exchange.GapStarted, Reason: "raw venue details must not escape",
	}
	resync := readPublicMarketEvent(t, ctx, connection)
	if resync.EventType != "resync_required" || resync.Sequence != 3 || resync.BootID != snapshot.BootID {
		t.Fatalf("resync=%+v", resync)
	}
	rawPayload, _ := json.Marshal(resync.Payload)
	if strings.Contains(string(rawPayload), "raw venue") || !strings.Contains(string(rawPayload), "stream-gap") {
		t.Fatalf("unsafe resync payload=%s", rawPayload)
	}
	var trailing publicMarketEventFixture
	err = wsjson.Read(ctx, connection, &trailing)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("post-resync read err=%v status=%d", err, websocket.CloseStatus(err))
	}
	select {
	case <-client.subscription.closed:
	case <-ctx.Done():
		t.Fatal("public subscription was not closed")
	}
}

func TestPublicMarketWebSocketRejectsUnsupportedKindBeforeUpgrade(t *testing.T) {
	instrumentID, _, _, handler := newPublicMarketFixture(t)
	request := httptest.NewRequest(http.MethodGet,
		"/v1/public/ws?account_id=acct-p&instrument_id="+url.QueryEscape(string(instrumentID))+"&kind=orders", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicMarketWebSocketSupportsEveryDeclaredKind(t *testing.T) {
	tests := []struct {
		kind      string
		callOrder []string
		publish   func(*fakePublicPerpClient)
	}{
		{
			kind: "order_book", callOrder: []string{"watch_order_book", "order_book_snapshot"},
			publish: func(client *fakePublicPerpClient) {
				client.bookSubscription.events <- exchange.BookEvent{
					Kind: exchange.EventDelta, Instrument: client.instrument,
					Sequence: "2", Previous: "1",
					Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(4)}},
					Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(5)}},
					Time: time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC),
				}
			},
		},
		{
			kind: "bbo", callOrder: []string{"watch_bbo", "order_book_snapshot"},
			publish: func(client *fakePublicPerpClient) {
				client.bboSubscription.events <- exchange.BBOEvent{
					Instrument: client.instrument,
					Bid:        exchange.BookLevel{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(4)},
					Ask:        exchange.BookLevel{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(5)},
					Time:       time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC),
				}
			},
		},
		{
			kind: "mark_price", callOrder: []string{"watch_mark_price"},
			publish: func(client *fakePublicPerpClient) {
				client.markSubscription.events <- exchange.MarkPriceEvent{
					Instrument: client.instrument, Price: decimal.RequireFromString("100.75"),
					Time: time.Date(2026, 7, 2, 2, 0, 0, 0, time.UTC),
				}
			},
		},
		{
			kind: "funding_rate", callOrder: []string{"watch_funding", "funding_snapshot"},
			publish: func(client *fakePublicPerpClient) {
				client.subscription.events <- exchange.FundingRateEvent{
					Instrument: client.instrument, Rate: decimal.RequireFromString("0.0002"),
					EffectiveAt: time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC),
					NextAt:      time.Date(2026, 7, 2, 16, 0, 0, 0, time.UTC),
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.kind, func(t *testing.T) {
			instrumentID, _, client, handler := newPublicMarketFixture(t)
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(handler)
			server.Listener = listener
			server.Start()
			t.Cleanup(server.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := websocketURL(server.URL) + "/v1/public/ws?account_id=acct-p&instrument_id=" +
				url.QueryEscape(string(instrumentID)) + "&kind=" + test.kind
			connection, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": []string{"Bearer test-token"}},
			})
			if err != nil {
				t.Fatalf("dial response=%v err=%v", response, err)
			}

			snapshot := readPublicMarketEvent(t, ctx, connection)
			if snapshot.EventType != "snapshot" || snapshot.Kind != test.kind || snapshot.Sequence != 1 {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			assertPublicPayloadRedacted(t, snapshot.Payload)
			if calls := client.publicCallOrder(); !equalStrings(calls, test.callOrder) {
				t.Fatalf("public stream initialization order=%v want=%v", calls, test.callOrder)
			}

			test.publish(client)
			update := readPublicMarketEvent(t, ctx, connection)
			if update.EventType != "update" || update.Kind != test.kind || update.Sequence != 2 ||
				update.BootID != snapshot.BootID {
				t.Fatalf("update=%+v", update)
			}
			assertPublicPayloadRedacted(t, update.Payload)
			_ = connection.CloseNow()
			select {
			case <-client.closedFor(test.kind):
			case <-ctx.Done():
				t.Fatal("public subscription was not closed")
			}
		})
	}
}

func TestPublicMarketWebSocketRejectsMislabeledInstrumentForEveryKind(t *testing.T) {
	kinds := []string{"order_book", "bbo", "mark_price", "funding_rate"}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			instrumentID, _, client, handler := newPublicMarketFixture(t)
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(handler)
			server.Listener = listener
			server.Start()
			t.Cleanup(server.Close)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			target := websocketURL(server.URL) + "/v1/public/ws?account_id=acct-p&instrument_id=" +
				url.QueryEscape(string(instrumentID)) + "&kind=" + kind
			connection, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{
				HTTPHeader: http.Header{"Authorization": []string{"Bearer test-token"}},
			})
			if err != nil {
				t.Fatalf("dial response=%v err=%v", response, err)
			}
			t.Cleanup(func() { _ = connection.CloseNow() })
			_ = readPublicMarketEvent(t, ctx, connection)

			mismatch := client.instrument
			mismatch.Identity.Symbol = "ETH-USDT"
			mismatch.Identity.NativeSymbol = "ETHUSDT"
			switch kind {
			case "order_book":
				client.bookSubscription.events <- exchange.BookEvent{
					Instrument: mismatch,
					Bids:       []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}},
					Asks:       []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)}},
				}
			case "bbo":
				client.bboSubscription.events <- exchange.BBOEvent{
					Instrument: mismatch,
					Bid:        exchange.BookLevel{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)},
					Ask:        exchange.BookLevel{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)},
				}
			case "mark_price":
				client.markSubscription.events <- exchange.MarkPriceEvent{
					Instrument: mismatch, Price: decimal.NewFromInt(100),
				}
			case "funding_rate":
				client.subscription.events <- exchange.FundingRateEvent{
					Instrument: mismatch, Rate: decimal.RequireFromString("0.0002"),
				}
			}

			resync := readPublicMarketEvent(t, ctx, connection)
			if resync.EventType != "resync_required" || resync.Sequence != 2 {
				t.Fatalf("resync=%+v", resync)
			}
			raw, _ := json.Marshal(resync.Payload)
			if strings.Contains(string(raw), "ETH") || !strings.Contains(string(raw), "malformed-update") {
				t.Fatalf("unsafe mismatch payload=%s", raw)
			}
			var trailing publicMarketEventFixture
			err = wsjson.Read(ctx, connection, &trailing)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("post-resync read err=%v status=%d", err, websocket.CloseStatus(err))
			}
		})
	}
}

type publicMarketEventFixture struct {
	BootID       string         `json:"boot_id"`
	Sequence     uint64         `json:"sequence"`
	EventID      string         `json:"event_id"`
	AccountID    string         `json:"account_id"`
	InstrumentID string         `json:"instrument_id"`
	Kind         string         `json:"kind"`
	EventType    string         `json:"event_type"`
	OccurredAt   time.Time      `json:"occurred_at"`
	Payload      map[string]any `json:"payload"`
}

func readPublicMarketEvent(t *testing.T, ctx context.Context, connection *websocket.Conn) publicMarketEventFixture {
	t.Helper()
	var event publicMarketEventFixture
	if err := wsjson.Read(ctx, connection, &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func assertPublicPayloadRedacted(t *testing.T, payload map[string]any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"native_symbol", "native_id", "BTCUSDT", `"identity"`, `"encoding"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("public payload leaked %q: %s", forbidden, raw)
		}
	}
}

func newPublicMarketFixture(t *testing.T) (domain.InstrumentID, exchange.Instrument, *fakePublicPerpClient, http.Handler) {
	t.Helper()
	return newPublicMarketFixtureWithCapability(t, capability.StaticMatrix().Rows[1])
}

func newPublicMarketFixtureWithCapability(
	t *testing.T,
	capabilityRow capability.Row,
) (domain.InstrumentID, exchange.Instrument, *fakePublicPerpClient, http.Handler) {
	t.Helper()
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
			NativeSymbol: "BTCUSDT", NativeID: "native-BTCUSDT",
		},
	}
	public := catalog.Instrument{
		ID: instrumentID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	subscription := newFakePublicSubscription[exchange.FundingRateEvent]()
	client := &fakePublicPerpClient{
		instrument:       instrument,
		subscription:     subscription,
		bookSubscription: newFakePublicSubscription[exchange.BookEvent](),
		bboSubscription:  newFakePublicSubscription[exchange.BBOEvent](),
		markSubscription: newFakePublicSubscription[exchange.MarkPriceEvent](),
		book: exchange.OrderBook{
			Instrument: instrument,
			Bids:       []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks:       []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
			Time:       time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC),
			Sequence:   "book-sequence",
			Page:       exchange.PageInfo{Limit: 25},
		},
		funding: exchange.FundingRate{
			NativeSymbol: "BTCUSDT", NativeID: "native-BTCUSDT",
			Rate:            decimal.RequireFromString("0.0001"),
			MarkPrice:       exchange.OptionalDecimal{Value: decimal.RequireFromString("100.5"), Valid: true},
			ObservedAt:      time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC),
			FundingTime:     time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC),
			NextFundingTime: time.Date(2026, 7, 2, 16, 0, 0, 0, time.UTC),
		},
		history: exchange.FundingRatePage{
			Rates: []exchange.FundingRate{{
				NativeSymbol: "BTCUSDT", NativeID: "native-BTCUSDT",
				Rate: decimal.RequireFromString("0.0001"), ObservedAt: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC),
			}},
			Page: exchange.PageInfo{Cursor: "next-opaque", Limit: 7, HasMoreKnown: true, HasMore: true},
		},
	}
	client.restMark = exchange.MarkPriceEvent{
		Instrument: instrument,
		Price:      client.funding.MarkPrice.Value,
		Time:       client.funding.ObservedAt,
	}
	accounts := &fakeHTTPAccounts{
		resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
		},
		status: account.Status{Resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
		}},
		instrument: instrument, public: public, capability: capabilityRow, perp: client,
	}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: &fakeHTTPReferences{accounts: accounts},
		Orders: &fakeHTTPOrders{}, Events: newTestEventHub(t), Auditor: &fakeHTTPAuditor{},
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
			Now:    func() time.Time { return time.Date(2026, 7, 2, 1, 0, 0, 0, time.UTC) },
			Random: strings.NewReader(strings.Repeat("p", 4096)),
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, err := auth.NewBearerMiddleware(context.Background(),
		secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	return instrumentID, instrument, client, middleware.Wrap(handler)
}

type fakePublicPerpClient struct {
	exchange.PerpClient
	instrument         exchange.Instrument
	book               exchange.OrderBook
	funding            exchange.FundingRate
	history            exchange.FundingRatePage
	subscription       *fakePublicSubscription[exchange.FundingRateEvent]
	bookSubscription   *fakePublicSubscription[exchange.BookEvent]
	bboSubscription    *fakePublicSubscription[exchange.BBOEvent]
	markSubscription   *fakePublicSubscription[exchange.MarkPriceEvent]
	restMark           exchange.MarkPriceEvent
	restMarkErr        error
	markWebSocketErr   error
	orderBookCalls     int
	fundingCalls       int
	historyCalls       int
	restMarkCalls      int
	lastBookRequest    exchange.OrderBookRequest
	lastHistoryRequest exchange.FundingRateHistoryRequest
	callMu             sync.Mutex
	callOrder          []string
}

func (client *fakePublicPerpClient) OrderBook(_ context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	client.recordPublicCall("order_book_snapshot")
	client.orderBookCalls++
	client.lastBookRequest = request
	return client.book, nil
}

func (client *fakePublicPerpClient) FundingRate(_ context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	client.recordPublicCall("funding_snapshot")
	client.fundingCalls++
	client.instrument = request.Instrument
	return client.funding, nil
}

func (client *fakePublicPerpClient) FundingRateHistory(_ context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	client.historyCalls++
	client.lastHistoryRequest = request
	return client.history, nil
}

func (client *fakePublicPerpClient) MarkPrice(_ context.Context, instrument exchange.Instrument) (exchange.MarkPriceEvent, error) {
	client.recordPublicCall("mark_price_rest")
	client.restMarkCalls++
	if client.restMarkErr != nil {
		return exchange.MarkPriceEvent{}, client.restMarkErr
	}
	event := client.restMark
	if client.restMark.Instrument.Identity == (exchange.InstrumentIdentity{}) {
		event.Instrument = instrument
	} else {
		event.Instrument = client.instrument
	}
	return event, nil
}

func (client *fakePublicPerpClient) WebSocket() exchange.PerpWebSocket {
	return fakePublicPerpWebSocket{client: client}
}

func (client *fakePublicPerpClient) recordPublicCall(name string) {
	client.callMu.Lock()
	defer client.callMu.Unlock()
	client.callOrder = append(client.callOrder, name)
}

func (client *fakePublicPerpClient) publicCallOrder() []string {
	client.callMu.Lock()
	defer client.callMu.Unlock()
	return append([]string(nil), client.callOrder...)
}

func (client *fakePublicPerpClient) closedFor(kind string) <-chan struct{} {
	switch kind {
	case "order_book":
		return client.bookSubscription.closed
	case "bbo":
		return client.bboSubscription.closed
	case "mark_price":
		return client.markSubscription.closed
	default:
		return client.subscription.closed
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type fakePublicPerpWebSocket struct {
	exchange.PerpWebSocket
	client *fakePublicPerpClient
}

func (socket fakePublicPerpWebSocket) WatchFundingRate(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.FundingRateEvent], error) {
	socket.client.recordPublicCall("watch_funding")
	return socket.client.subscription, nil
}

func (socket fakePublicPerpWebSocket) WatchOrderBook(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.BookEvent], error) {
	socket.client.recordPublicCall("watch_order_book")
	return socket.client.bookSubscription, nil
}

func (socket fakePublicPerpWebSocket) WatchBBO(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.BBOEvent], error) {
	socket.client.recordPublicCall("watch_bbo")
	return socket.client.bboSubscription, nil
}

func (socket fakePublicPerpWebSocket) WatchMarkPrice(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	socket.client.recordPublicCall("watch_mark_price")
	if socket.client.markWebSocketErr != nil {
		return nil, socket.client.markWebSocketErr
	}
	socket.client.markSubscription.events <- exchange.MarkPriceEvent{
		Instrument: socket.client.instrument,
		Price:      socket.client.funding.MarkPrice.Value,
		Time:       socket.client.funding.ObservedAt,
	}
	return socket.client.markSubscription, nil
}

type fakePublicSubscription[T any] struct {
	events    chan T
	status    chan exchange.StreamStatusEvent
	errors    chan error
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakePublicSubscription[T any]() *fakePublicSubscription[T] {
	return &fakePublicSubscription[T]{
		events: make(chan T, 4), status: make(chan exchange.StreamStatusEvent, 4),
		errors: make(chan error, 4), closed: make(chan struct{}),
	}
}

func (*fakePublicSubscription[T]) ID() string { return "public-test-stream" }

func (subscription *fakePublicSubscription[T]) Events() <-chan T {
	return subscription.events
}

func (subscription *fakePublicSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	return subscription.status
}

func (subscription *fakePublicSubscription[T]) Errors() <-chan error {
	return subscription.errors
}

func (subscription *fakePublicSubscription[T]) Close() error {
	subscription.closeOnce.Do(func() { close(subscription.closed) })
	return nil
}
