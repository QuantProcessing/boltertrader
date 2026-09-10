package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const marketTestID = "binance:live:perp:USDT:BTC-USDT"

type apiMarketSource struct {
	instrument publicmarket.Instrument
	snapshot   publicmarket.Snapshot
	err        error
	calls      atomic.Int64
	candles    chan publicmarket.Query
}

func newAPIMarketSource() *apiMarketSource {
	instrument := publicmarket.Instrument{Ready: true, RouteProfileID: "binance.perp.usdt.v1", Catalog: catalog.Instrument{
		ID: marketTestID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp,
		Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}}
	return &apiMarketSource{instrument: instrument, snapshot: publicmarket.Snapshot{
		Revision: "catalog-revision", ObservedAt: time.Unix(1720000000, 0).UTC(), Instruments: []publicmarket.Instrument{instrument},
	}, candles: make(chan publicmarket.Query, 1)}
}

func (source *apiMarketSource) Snapshot(context.Context) (publicmarket.Snapshot, error) {
	source.calls.Add(1)
	return source.snapshot, source.err
}

func (source *apiMarketSource) ResolveInstrument(_ context.Context, id string) (publicmarket.Instrument, error) {
	source.calls.Add(1)
	if source.err != nil {
		return publicmarket.Instrument{}, source.err
	}
	if id != string(source.instrument.Catalog.ID) {
		return publicmarket.Instrument{}, exchange.ErrNotFound
	}
	return source.instrument, nil
}

func (source *apiMarketSource) Candles(_ context.Context, query publicmarket.Query, _ string, _ int) (publicmarket.CandleResult, error) {
	source.calls.Add(1)
	source.candles <- query
	return publicmarket.CandleResult{Instrument: source.instrument.Canonical}, source.err
}

func (*apiMarketSource) SubscriptionKey(_ context.Context, id string, dataset exchange.MarketDataset, interval string, depth int) (markethub.SubscriptionKey, error) {
	return markethub.SubscriptionKey{Owner: markethub.OwnerKey{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual"}, InstrumentID: id, Dataset: dataset, Interval: interval, Depth: depth}, nil
}

type apiNativeStream struct {
	events chan markethub.Message
	closed chan struct{}
}

type apiMarketOwner struct {
	data   json.RawMessage
	opens  atomic.Int64
	opened chan *apiNativeStream
}

func (owner *apiMarketOwner) Open(_ context.Context, key markethub.SubscriptionKey) (markethub.NativeStream, error) {
	owner.opens.Add(1)
	stream := &apiNativeStream{events: make(chan markethub.Message, 8), closed: make(chan struct{})}
	owner.opened <- stream
	snapshot := markethub.Message{Kind: markethub.MessageSnapshot, Key: key, ObservedAt: time.Unix(1720000000, 0).UTC(), SourceSequence: "10", Data: json.RawMessage(`{"bids":[{"price":"99","quantity":"1"}],"asks":[{"price":"101","quantity":"1"}]}`)}
	if owner.data != nil {
		snapshot.Data = owner.data
	}
	return markethub.NativeStream{Snapshot: snapshot, Events: stream.events, Errors: make(chan error), Replay: func() (markethub.Message, bool) { return snapshot, true }, Close: func() error { close(stream.closed); return nil }}, nil
}

func newMarketTestHandler(t *testing.T, source *apiMarketSource, hub MarketHub) http.Handler {
	t.Helper()
	// These have no usable account/order implementation. Any accidental private
	// dependency in the market routes panics instead of quietly passing the test.
	handler, err := NewHandler(Options{
		Monitor: health.NewMonitor(), Accounts: struct{ AccountRegistry }{},
		References: struct{ ReferenceService }{}, Orders: struct{ OrderService }{},
		Events: newTestEventHub(t), Auditor: &fakeHTTPAuditor{}, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
		Markets: MarketOptions{Source: source, Hub: hub},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://MARKET_TEST_TOKEN")
	middleware, err := auth.NewBearerMiddleware(t.Context(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	return middleware.Wrap(handler)
}

func marketRequest(handler http.Handler, path string, authenticated bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if authenticated {
		r.Header.Set("Authorization", "Bearer test-token")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestMarketAPIAuthenticatesAndValidatesBeforeAnySourceCall(t *testing.T) {
	source := newAPIMarketSource()
	handler := newMarketTestHandler(t, source, nil)
	for _, path := range []string{"/v1/market/instruments", "/v1/market/snapshot", "/v1/market/candles", "/v1/market/ws"} {
		if w := marketRequest(handler, path, false); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s unauthorized status=%d", path, w.Code)
		}
	}
	for _, path := range []string{
		"/v1/market/instruments?account_id=account-a",
		"/v1/market/snapshot?instrument_id=" + marketTestID + "&topic=account.events",
		"/v1/market/snapshot?instrument_id=" + marketTestID + "&topic=market.order_book&depth=101",
		"/v1/market/snapshot?instrument_id=" + marketTestID + "&topic=market.bbo&depth=0",
		"/v1/market/snapshot?instrument_id=" + marketTestID + "&topic=market.bbo&internalPurpose=execution",
		"/v1/market/candles?instrument_id=" + marketTestID + "&interval=1m&limit=501",
		"/v1/market/candles?instrument_id=" + marketTestID + "&interval=1m&limit=1&limit=2",
		"/v1/market/candles?instrument_id=" + marketTestID + "&interval=bad",
		"/v1/market/ws?token=not-accepted",
	} {
		if w := marketRequest(handler, path, true); w.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", path, w.Code, w.Body.String())
		}
	}
	if source.calls.Load() != 0 {
		t.Fatal("invalid or unauthenticated request reached the market source")
	}
}

func TestMigrationValidationDoesNotFabricateMarketData(t *testing.T) {
	handler := NewMigrationValidationHandler(health.NewMonitor())
	for _, path := range []string{"/v1/market/instruments", "/v1/market/snapshot", "/v1/market/candles", "/v1/market/ws"} {
		if response := marketRequest(handler, path, true); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"market_unavailable"`) {
			t.Fatalf("validation mode %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestMarketAPICatalogCoverageAndAccountIndependentCandles(t *testing.T) {
	source := newAPIMarketSource()
	source.snapshot.UnavailableProfiles = []string{"bybit.perp.usdt.v1"}
	handler := newMarketTestHandler(t, source, nil)
	w := marketRequest(handler, "/v1/market/instruments", true)
	var result marketCatalogResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s error=%v", w.Code, w.Body.String(), err)
	}
	if result.Complete || len(result.UnavailableProfiles) != 1 || len(result.Items) != 1 || result.Items[0].InstrumentID != marketTestID || result.ObservedAt != source.snapshot.ObservedAt {
		t.Fatalf("partial catalog=%+v", result)
	}
	if strings.Contains(w.Body.String(), "account_id") || strings.Contains(w.Body.String(), "native") {
		t.Fatal("catalog leaked account/native identity")
	}
	source.snapshot.UnavailableProfiles = nil
	source.snapshot.Instruments[0].Ready = false
	w = marketRequest(handler, "/v1/market/instruments", true)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Complete {
		t.Fatalf("stale catalog=%s error=%v", w.Body.String(), err)
	}
	// A stale directory must not prevent reading a known public instrument.
	source.instrument.Ready = false
	w = marketRequest(handler, "/v1/market/candles?instrument_id="+marketTestID+"&interval=1m&limit=10", true)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"candles":[]`) {
		t.Fatalf("candles status=%d body=%s", w.Code, w.Body.String())
	}
	query := <-source.candles
	if query.InstrumentID != marketTestID || query.Venue != "binance" || query.Environment != "live" || query.Product != "perp" {
		t.Fatalf("candle query=%+v", query)
	}
}

func TestMarketAPIUnknownAndUnavailableAreDifferent(t *testing.T) {
	source := newAPIMarketSource()
	handler := newMarketTestHandler(t, source, nil)
	w := marketRequest(handler, "/v1/market/candles?instrument_id=binance:live:perp:USDT:UNKNOWN-USDT&interval=1m", true)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown status=%d body=%s", w.Code, w.Body.String())
	}
	source.err = errors.Join(publicmarket.ErrUnavailable, errors.New("sensitive-upstream-text"))
	w = marketRequest(handler, "/v1/market/instruments", true)
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "sensitive-upstream-text") {
		t.Fatalf("unavailable status=%d body=%s", w.Code, w.Body.String())
	}
}

func marketTestHub(t *testing.T) (*markethub.Hub, *apiMarketOwner) {
	t.Helper()
	owner := &apiMarketOwner{opened: make(chan *apiNativeStream, 128)}
	hub, err := markethub.New(markethub.Options{Owner: owner, IdleGrace: 20 * time.Millisecond, ReconnectBackoff: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	return hub, owner
}

func dialMarket(t *testing.T, ctx context.Context, server *httptest.Server, path string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+path, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-token"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func readMarketMessage(t *testing.T, ctx context.Context, conn *websocket.Conn) realtimewire.Message {
	t.Helper()
	var message realtimewire.Message
	if err := wsjson.Read(ctx, conn, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func writeMarketControl(t *testing.T, ctx context.Context, conn *websocket.Conn, control realtimewire.Control) {
	t.Helper()
	if err := wsjson.Write(ctx, conn, control); err != nil {
		t.Fatal(err)
	}
}

func TestMarketAPIReusesHubAcrossRESTAndWebSocketAndRecoversGaps(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	source := newAPIMarketSource()
	hub, owner := marketTestHub(t)
	handler := newMarketTestHandler(t, source, hub)
	server := httptest.NewServer(handler)
	defer server.Close()
	conn := dialMarket(t, ctx, server, "/v1/market/ws")
	defer conn.CloseNow()
	request := realtimewire.TopicRequest{Topic: realtimewire.TopicOrderBook, InstrumentID: marketTestID, Depth: 20}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "start", Topics: []realtimewire.TopicRequest{request}})
	ack, snapshot := readMarketMessage(t, ctx, conn), readMarketMessage(t, ctx, conn)
	if ack.Type != realtimewire.MessageAck || snapshot.Type != realtimewire.MessageSnapshot || snapshot.SubscriptionID != ack.SubscriptionID {
		t.Fatalf("ack=%+v snapshot=%+v", ack, snapshot)
	}
	first := <-owner.opened
	w := marketRequest(handler, "/v1/market/snapshot?instrument_id="+url.QueryEscape(marketTestID)+"&topic=market.order_book&depth=20", true)
	var rest marketSnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rest); err != nil || w.Code != http.StatusOK {
		t.Fatalf("REST snapshot status=%d body=%s error=%v", w.Code, w.Body.String(), err)
	}
	if owner.opens.Load() != 1 || rest.Snapshot.Generation != snapshot.Generation || rest.Snapshot.Sequence != snapshot.Sequence || string(rest.Snapshot.Data) != string(snapshot.Data) || rest.Snapshot.ObservedAt != snapshot.ObservedAt {
		t.Fatalf("REST did not reuse the native snapshot: %+v vs %+v, opens=%d", rest, snapshot, owner.opens.Load())
	}
	first.events <- markethub.Message{Kind: markethub.MessageDelta, ObservedAt: snapshot.ObservedAt.Add(time.Second), SourceSequence: "11", Data: json.RawMessage(`{"previous":"10","bids":[{"price":"99","quantity":"0"}],"asks":[]}`)}
	delta := readMarketMessage(t, ctx, conn)
	if delta.Type != realtimewire.MessageDelta || delta.Sequence <= snapshot.Sequence || delta.Generation != snapshot.Generation {
		t.Fatalf("delta=%+v", delta)
	}
	first.events <- markethub.Message{Kind: markethub.MessageResyncRequired, Reason: "native-gap"}
	gap, recovered := readMarketMessage(t, ctx, conn), readMarketMessage(t, ctx, conn)
	if gap.Type != realtimewire.MessageResyncRequired || gap.Code != "UPSTREAM_GAP" || recovered.Type != realtimewire.MessageSnapshot || recovered.Generation <= snapshot.Generation {
		t.Fatalf("gap=%+v recovered=%+v", gap, recovered)
	}
	second := <-owner.opened
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationUnsubscribe, RequestID: "stop", Topics: []realtimewire.TopicRequest{{SubscriptionID: ack.SubscriptionID}}})
	if stopped := readMarketMessage(t, ctx, conn); stopped.Type != realtimewire.MessageAck || stopped.RequestID != "stop" {
		t.Fatalf("unsubscribe=%+v", stopped)
	}
	select {
	case <-second.closed:
	case <-ctx.Done():
		t.Fatal("unsubscribe did not release native demand")
	}
}

func TestMarketAPIRejectsPrivateTopicsAndProtectedPurposeBeforeSubscribing(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	source := newAPIMarketSource()
	hub, owner := marketTestHub(t)
	server := httptest.NewServer(newMarketTestHandler(t, source, hub))
	defer server.Close()
	conn := dialMarket(t, ctx, server, "/v1/market/ws")
	defer conn.CloseNow()
	valid := realtimewire.TopicRequest{Topic: realtimewire.TopicBBO, InstrumentID: marketTestID}
	for _, forbidden := range []realtimewire.TopicRequest{
		{Topic: realtimewire.TopicAccountEvents}, {Topic: realtimewire.TopicFundingBoard},
		{Topic: realtimewire.TopicBBO, InstrumentID: marketTestID, InternalPurpose: "execution"},
		{Topic: realtimewire.TopicBBO, InstrumentID: marketTestID, Cursor: "pretend-replay"},
	} {
		writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "forbidden", Topics: []realtimewire.TopicRequest{valid, forbidden}})
		if message := readMarketMessage(t, ctx, conn); message.Type != realtimewire.MessageError || message.Code != "INVALID_TOPIC" {
			t.Fatalf("forbidden topic response=%+v", message)
		}
	}
	if source.calls.Load() != 0 || owner.opens.Load() != 0 {
		t.Fatal("rejected control reached the source")
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationPing, RequestID: "still-connected"})
	if message := readMarketMessage(t, ctx, conn); message.Type != realtimewire.MessagePong {
		t.Fatalf("ping=%+v", message)
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "unknown", Topics: []realtimewire.TopicRequest{{Topic: realtimewire.TopicBBO, InstrumentID: "binance:live:perp:USDT:UNKNOWN-USDT"}}})
	ack, missing := readMarketMessage(t, ctx, conn), readMarketMessage(t, ctx, conn)
	if ack.Type != realtimewire.MessageAck || missing.Type != realtimewire.MessageError || missing.Code != "INSTRUMENT_UNKNOWN" {
		t.Fatalf("unknown instrument ack=%+v result=%+v", ack, missing)
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationUnsubscribe, RequestID: "untrusted-topic", Topics: []realtimewire.TopicRequest{{SubscriptionID: "missing", Topic: realtimewire.TopicAccountEvents}}})
	if message := readMarketMessage(t, ctx, conn); message.Type != realtimewire.MessageAck || message.Topic != "" {
		t.Fatalf("unsubscribe echoed an untrusted topic: %+v", message)
	}
}

func TestMarketAPIMarkSnapshotDoesNotResetCachedAge(t *testing.T) {
	source := newAPIMarketSource()
	hub, owner := marketTestHub(t)
	received := time.Now().UTC().Add(-time.Minute)
	owner.data = realtimewire.EncodeData(map[string]any{"price": "100", "ageMs": 0, "staleAfterMs": 5000, "receivedAt": received})
	handler := newMarketTestHandler(t, source, hub)
	w := marketRequest(handler, "/v1/market/snapshot?instrument_id="+marketTestID+"&topic=market.mark_price", true)
	var response marketSnapshotResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK {
		t.Fatalf("mark status=%d body=%s error=%v", w.Code, w.Body.String(), err)
	}
	var data struct {
		Age        int64     `json:"ageMs"`
		ReceivedAt time.Time `json:"receivedAt"`
	}
	if err := json.Unmarshal(response.Snapshot.Data, &data); err != nil || data.Age < 59000 || !data.ReceivedAt.Equal(received) {
		t.Fatalf("cached mark appeared fresh: %s error=%v", response.Snapshot.Data, err)
	}
	if !strings.Contains(string(owner.data), `"ageMs":0`) {
		t.Fatal("wire projection mutated the retained source fact")
	}
}

func TestInternalAndApplicationMarketStreamsShareOneHubTopic(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	source := newAPIMarketSource()
	hub, owner := marketTestHub(t)
	internal, err := NewInternalHandler(InternalOptions{Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil }, MarketHub: hub, MarketKeys: source})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/internal/", internal)
	mux.Handle("/v1/", newMarketTestHandler(t, source, hub))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, path := range []string{"/internal/v1/market/ws", "/v1/market/ws"} {
		conn := dialMarket(t, ctx, server, path)
		defer conn.CloseNow()
		writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "start", Topics: []realtimewire.TopicRequest{{Topic: realtimewire.TopicOrderBook, InstrumentID: marketTestID, Depth: 20}}})
		ack, snapshot := readMarketMessage(t, ctx, conn), readMarketMessage(t, ctx, conn)
		if ack.Type != realtimewire.MessageAck || snapshot.Type != realtimewire.MessageSnapshot {
			t.Fatalf("%s ack=%+v snapshot=%+v", path, ack, snapshot)
		}
	}
	if owner.opens.Load() != 1 {
		t.Fatalf("opened %d native topics for one shared market", owner.opens.Load())
	}
}

func TestMarketAPIEnforcesActiveSubscriptionBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	source := newAPIMarketSource()
	hub, _ := marketTestHub(t)
	server := httptest.NewServer(newMarketTestHandler(t, source, hub))
	defer server.Close()
	conn := dialMarket(t, ctx, server, "/v1/market/ws")
	defer conn.CloseNow()
	requests := make([]realtimewire.TopicRequest, realtimewire.MaxActiveTopics)
	for index := range requests {
		requests[index] = realtimewire.TopicRequest{Topic: realtimewire.TopicOrderBook, InstrumentID: marketTestID, Depth: 20}
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "fill", Topics: requests})
	var first string
	for count := 0; count < realtimewire.MaxActiveTopics; {
		message := readMarketMessage(t, ctx, conn)
		if message.Type == realtimewire.MessageAck {
			count++
			first = message.SubscriptionID
		}
		if message.Type == realtimewire.MessageError {
			t.Fatalf("initial subscription failed: %+v", message)
		}
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "overflow", Topics: requests[:1]})
	for {
		message := readMarketMessage(t, ctx, conn)
		if message.RequestID != "overflow" {
			continue
		}
		if message.Type != realtimewire.MessageError || message.Code != "TOPIC_LIMIT_EXCEEDED" {
			t.Fatalf("topic budget response=%+v", message)
		}
		break
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationUnsubscribe, RequestID: "release", Topics: []realtimewire.TopicRequest{{SubscriptionID: first}}})
	for readMarketMessage(t, ctx, conn).RequestID != "release" {
	}
	writeMarketControl(t, ctx, conn, realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "replacement", Topics: requests[:1]})
	for {
		message := readMarketMessage(t, ctx, conn)
		if message.RequestID != "replacement" {
			continue
		}
		if message.Type != realtimewire.MessageAck {
			t.Fatalf("released capacity was not reusable: %+v", message)
		}
		break
	}
}
