package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/realtime"
	platformsystem "github.com/QuantProcessing/boltertrader/backend/internal/platform/system"
)

type catalogSourceFunc func(context.Context) (catalog.Snapshot, error)

func (function catalogSourceFunc) Fetch(ctx context.Context) (catalog.Snapshot, error) {
	return function(ctx)
}

type marketSourceFunc func(context.Context, marketdata.Query) (marketdata.Fact, error)

func (function marketSourceFunc) Fetch(ctx context.Context, query marketdata.Query) (marketdata.Fact, error) {
	return function(ctx, query)
}

func newPublicDataHandler(t *testing.T) (*http.ServeMux, *realtime.Hub) {
	t.Helper()
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	catalogProjection, err := catalog.NewProjection(catalogSourceFunc(func(context.Context) (catalog.Snapshot, error) {
		return catalog.Snapshot{Revision: "gateway-1", ObservedAt: now, Venues: []catalog.Venue{{Venue: "binance", Environment: "demo", Ready: true}}, Instruments: []catalog.Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: true}}}, nil
	}), time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	budget, _ := marketdata.NewCoordinator(10, time.Minute, func() time.Time { return now })
	marketProjection, err := marketdata.NewService(marketSourceFunc(func(_ context.Context, query marketdata.Query) (marketdata.Fact, error) {
		return marketdata.Fact{Venue: query.Venue, Environment: query.Environment, Product: query.Product, InstrumentID: query.InstrumentID, MarkPrice: "100.00", BestBid: "99", BestBidSize: "1", BestAsk: "101", BestAskSize: "2", Bids: []marketdata.Level{{Price: "99", Quantity: "1"}}, Asks: []marketdata.Level{{Price: "101", Quantity: "2"}}, ObservedAt: now}, nil
	}), budget, time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	readiness := platformsystem.NewReadiness()
	for _, component := range platformsystem.RequiredComponents {
		readiness.Update(component, platformsystem.Component{Ready: true, Reason: "ready", ObservedAt: now})
	}
	hub, _ := realtime.NewHub(8, func() time.Time { return now })
	api, err := NewPublicDataAPI(PublicDataDependencies{Catalog: catalogProjection, MarketData: marketProjection, Readiness: readiness, Realtime: hub, Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux); err != nil {
		t.Fatal(err)
	}
	return mux, hub
}

func TestPublicDataRoutesUseStrictGatewayBackedDTOs(t *testing.T) {
	handler, _ := newPublicDataHandler(t)
	for _, test := range []struct {
		path string
		want int
		body string
	}{
		{path: "/api/v1/catalog/venues", want: http.StatusOK, body: `"revision":"gateway-1"`},
		{path: "/api/v1/catalog/instruments?accountId=secret-scope", want: http.StatusBadRequest, body: "unknown query field"},
		{path: "/api/v1/market-data?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt", want: http.StatusOK, body: `"markPrice":"100.00"`},
		{path: "/api/v1/market-data?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&gatewayUrl=http://internal", want: http.StatusBadRequest, body: "unknown query field"},
		{path: "/api/v1/market-data?venue=binance&instrumentId=btc-usdt", want: http.StatusBadRequest, body: "request is invalid"},
		{path: "/api/v1/system/readiness", want: http.StatusOK, body: `"accounts"`},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want || !strings.Contains(response.Body.String(), test.body) {
			t.Errorf("GET %s = %d %s, want %d containing %q", test.path, response.Code, response.Body.String(), test.want, test.body)
		}
	}
}

func TestRealtimeStreamResumesFromCursorAndRejectsExpiredCursor(t *testing.T) {
	handler, hub := newPublicDataHandler(t)
	first, _ := hub.Publish("catalog.updated", map[string]string{"revision": "one"})
	second, _ := hub.Publish("market_data.updated", map[string]string{"instrumentId": "btc-usdt"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/stream?cursor="+first.Cursor, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), first.EventID) || !strings.Contains(response.Body.String(), second.EventID) {
		t.Fatalf("resume response = %d %s", response.Code, response.Body.String())
	}

	small, _ := realtime.NewHub(1, time.Now)
	expired, _ := small.Publish("one", map[string]bool{"ready": true})
	_, _ = small.Publish("two", map[string]bool{"ready": true})
	api, _ := NewPublicDataAPI(PublicDataDependencies{Catalog: &catalogStub{}, MarketData: &marketStub{}, Readiness: platformsystem.NewReadiness(), Realtime: small, Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }})
	expiredRequest := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/stream?cursor="+expired.Cursor, nil)
	expiredResponse := httptest.NewRecorder()
	api.Handle(expiredResponse, expiredRequest, "GET /api/v1/realtime/stream")
	if expiredResponse.Code != http.StatusConflict || !strings.Contains(expiredResponse.Body.String(), "RESNAPSHOT_REQUIRED") {
		t.Fatalf("expired response = %d %s", expiredResponse.Code, expiredResponse.Body.String())
	}
}

func TestRealtimeStreamAllowsInitialSubscriptionWithoutCursor(t *testing.T) {
	handler, _ := newPublicDataHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/stream", nil).WithContext(ctx)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(response.Body.String(), ": connected\n\n") {
		t.Fatalf("initial realtime response = %d %s", response.Code, response.Body.String())
	}
}

func TestRealtimeStreamResumesFromLastEventID(t *testing.T) {
	handler, hub := newPublicDataHandler(t)
	first, _ := hub.Publish("catalog.updated", map[string]string{"revision": "one"})
	second, _ := hub.Publish("market_data.updated", map[string]string{"instrumentId": "btc-usdt"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/stream", nil).WithContext(ctx)
	request.Header.Set("Last-Event-ID", first.Cursor)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), first.EventID) || !strings.Contains(response.Body.String(), second.EventID) {
		t.Fatalf("Last-Event-ID resume response = %d %s", response.Code, response.Body.String())
	}
}

func TestRealtimeStreamScopesPrivateReplayToPrincipal(t *testing.T) {
	handler, hub := newPublicDataHandler(t)
	activity, err := NewActivityLog(8, time.Now, hub)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := hub.Publish("catalog.updated", map[string]string{"revision": "before-private-events"})
	_, _ = activity.Publish(ActivityRecord{PrincipalID: "principal-a", Mode: "test", Kind: "command.accepted", ResourceID: "command-a"})
	_, _ = activity.Publish(ActivityRecord{PrincipalID: "principal-b", Mode: "test", Kind: "command.accepted", ResourceID: "command-b"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/stream?cursor="+before.Cursor, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "command-a") || strings.Contains(response.Body.String(), "command-b") {
		t.Fatalf("scoped realtime response=%d %s", response.Code, response.Body.String())
	}
}

type catalogStub struct{}

func (*catalogStub) Snapshot(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{}, nil
}
func (*catalogStub) Status() catalog.Status { return catalog.Status{} }

type marketStub struct{}

func (*marketStub) Get(context.Context, marketdata.Query) (marketdata.Fact, error) {
	return marketdata.Fact{}, nil
}
func (*marketStub) BudgetStatus(string) marketdata.BudgetStatus { return marketdata.BudgetStatus{} }
