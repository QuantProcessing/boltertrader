package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
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
	api, err := NewPublicDataAPI(PublicDataDependencies{Catalog: catalogProjection, MarketData: marketProjection, Candles: &candleStub{}, FundingBoard: &fundingBoardStub{}, FundingHistory: &fundingHistoryStub{}, Readiness: readiness, Realtime: hub, Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }})
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
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&interval=1m", want: http.StatusOK, body: `"interval":"1m"`},
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&interval=1m&limit=100", want: http.StatusOK, body: `"interval":"1m"`},
		// limit is the one optional field, so it must be rejected on its own
		// terms rather than by the required-field sweep.
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&interval=1m&limit=0", want: http.StatusBadRequest, body: "must be between 1 and 500"},
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&interval=1m&limit=501", want: http.StatusBadRequest, body: "must be between 1 and 500"},
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt", want: http.StatusBadRequest, body: `field \"interval\" is required`},
		{path: "/api/v1/candles?venue=binance&environment=testnet&product=perp&instrumentId=btc-usdt&interval=1m&accountId=secret-scope", want: http.StatusBadRequest, body: "unknown query field"},
		{path: "/api/v1/funding-history?venue=binance&environment=demo&symbol=BTC-USDT&window=7d", want: http.StatusOK, body: `"intervalHours":8`},
		{path: "/api/v1/funding-history?venue=binance&environment=demo&symbol=BTC-USDT&window=30d", want: http.StatusOK, body: `"symbol":"BTC-USDT"`},
		// The window is a closed set, not a duration: a span this surface will
		// not fetch in one page has to be refused here rather than truncated
		// silently at a venue.
		{path: "/api/v1/funding-history?venue=binance&environment=demo&symbol=BTC-USDT&window=90d", want: http.StatusBadRequest, body: `must be \"7d\" or \"30d\"`},
		{path: "/api/v1/funding-history?venue=binance&environment=demo&symbol=BTC-USDT", want: http.StatusBadRequest, body: `field \"window\" is required`},
		{path: "/api/v1/funding-history?venue=binance&environment=demo&symbol=BTC-USDT&window=7d&since=2026-01-01T00:00:00Z", want: http.StatusBadRequest, body: "unknown query field"},
		// A venue with no history endpoint answers permanently, so the browser
		// stops asking instead of retrying a chart that cannot exist.
		{path: "/api/v1/funding-history?venue=nado&environment=testnet&symbol=BTC-USDT&window=7d", want: http.StatusNotFound, body: "FUNDING_HISTORY_UNSUPPORTED"},
		// Equally permanent and deliberately not the same code. A venue that
		// simply does not list one pair must not be reported as publishing no
		// funding history, which is false for every other pair it does list.
		{path: "/api/v1/funding-history?venue=bybit&environment=demo&symbol=NOTLISTED-USDT&window=7d", want: http.StatusNotFound, body: "FUNDING_HISTORY_INSTRUMENT_UNKNOWN"},
		{path: "/api/v1/system/readiness", want: http.StatusOK, body: `"accounts"`},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want || !strings.Contains(response.Body.String(), test.body) {
			t.Errorf("GET %s = %d %s, want %d containing %q", test.path, response.Code, response.Body.String(), test.want, test.body)
		}
		// Every 404 on this surface is a permanent answer, and the browser's
		// shared retry policy reads exactly this flag to decide whether to keep
		// asking. A refusal that arrived retryable would leave a leg of the
		// arbitrage chart spinning for the life of the page.
		if test.want == http.StatusNotFound && !strings.Contains(response.Body.String(), `"retryable":false`) {
			t.Errorf("GET %s answered 404 without retryable:false: %s", test.path, response.Body.String())
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
	api, _ := NewPublicDataAPI(PublicDataDependencies{Catalog: &catalogStub{}, MarketData: &marketStub{}, Candles: &candleStub{}, FundingBoard: &fundingBoardStub{}, FundingHistory: &fundingHistoryStub{}, Readiness: platformsystem.NewReadiness(), Realtime: small, Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }})
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

type candleStub struct{}

func (*candleStub) Get(_ context.Context, query marketdata.CandleQuery) (marketdata.CandlePage, error) {
	return marketdata.CandlePage{
		Venue: query.Venue, Environment: query.Environment, Product: query.Product,
		InstrumentID: query.InstrumentID, Interval: query.Interval,
	}, nil
}
func (*candleStub) BudgetStatus(string) marketdata.BudgetStatus { return marketdata.BudgetStatus{} }

type fundingBoardStub struct{}

func (*fundingBoardStub) Get(context.Context) (marketdata.BoardSnapshot, error) {
	return marketdata.BoardSnapshot{
		Mode: "test",
		Venues: []marketdata.BoardVenue{{
			Venue: "binance", Environment: "demo", Product: "perp",
			RouteProfileID: "binance.perp.usdt.demo.v1",
			Rows:           []marketdata.BoardRow{{Symbol: "BTC-USDT", Rate: "0.0001"}},
		}},
	}, nil
}
func (*fundingBoardStub) BudgetStatus() marketdata.BudgetStatus { return marketdata.BudgetStatus{} }

// fundingHistoryStub answers for every venue except the two that refuse
// permanently. Neither case is decoration: the arbitrage chart draws two legs,
// so the surface has to be able to say why one of them did not render beside one
// that did, and it has to say the right thing. "This venue keeps no history" and
// "this venue does not list this pair" are separate facts with separate codes,
// and only the first is a claim about the venue.
type fundingHistoryStub struct{}

func (*fundingHistoryStub) Get(_ context.Context, query marketdata.FundingHistoryQuery) (marketdata.FundingHistorySeries, error) {
	if query.Venue == "nado" {
		return marketdata.FundingHistorySeries{}, marketdata.ErrFundingHistoryUnsupported
	}
	if query.Symbol == "NOTLISTED-USDT" {
		return marketdata.FundingHistorySeries{}, marketdata.ErrFundingHistoryUnknownInstrument
	}
	return marketdata.FundingHistorySeries{
		Venue: query.Venue, Environment: query.Environment, Symbol: query.Symbol,
		Points: []marketdata.FundingHistoryPoint{{
			FundingTime: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), Rate: "0.0001", IntervalHours: 8,
		}},
	}, nil
}
func (*fundingHistoryStub) BudgetStatus(string) marketdata.BudgetStatus {
	return marketdata.BudgetStatus{}
}
