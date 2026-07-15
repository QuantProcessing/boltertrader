package gate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

func TestGateExecutionMassStatusRejectsInvalidQueryBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query model.MassStatusQuery
	}{
		{name: "foreign venue", query: model.MassStatusQuery{Venue: "OTHER"}},
		{name: "unknown explicit instrument", query: model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{{Venue: VenueName, Symbol: "UNKNOWN-USDT", Kind: enums.KindSpot}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				writeJSON(t, w, []any{})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), gateSpotTestProvider(), clock.NewRealClock()).withScope([]enums.InstrumentKind{enums.KindSpot})

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), tc.query)
			if err == nil || mass != nil {
				t.Fatalf("mass=%+v err=%v, want pre-I/O validation error", mass, err)
			}
			if calls.Load() != 0 {
				t.Fatalf("venue calls=%d, want zero", calls.Load())
			}
		})
	}
}

func TestGateExecutionMassStatusRejectsWrongAccountBeforeAnyTransportIO(t *testing.T) {
	var calls atomic.Int32
	httpClient := &http.Client{Transport: gateRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	})}
	exec := newExecutionClient(
		gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL("https://gate.test").WithHTTPClient(httpClient),
		gateFullTestProvider(),
		clock.NewRealClock(),
	)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:        "GATE-OTHER",
		IncludeFills:     true,
		IncludePositions: true,
	})
	if err == nil || mass != nil {
		t.Fatalf("mass=%+v err=%v, want wrong-account pre-I/O rejection", mass, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls=%d, want zero total I/O", calls.Load())
	}
}

func TestGateExecutionMassStatusUsesOneEffectiveFillThrough(t *testing.T) {
	now := time.Date(2026, 7, 15, 20, 0, 0, 0, time.UTC)
	provider := gateSpotTestProvider()
	spotID := provider.All()[0].ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/spot/open_orders":
			writeJSON(t, w, []any{})
		case "/spot/my_trades":
			writeJSON(t, w, []any{gateSpotFillFixture("too-new", "order", "client", now.Add(time.Millisecond))})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now)).withScope([]enums.InstrumentKind{enums.KindSpot})
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{spotID}, Since: now.Add(-time.Hour), IncludeFills: true}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) != 0 {
		t.Fatalf("fills=%+v, want post-through fill filtered", mass.FillReports)
	}
	if !mass.FillsCoverage.Scope.Through.Equal(now) {
		t.Fatalf("coverage through=%s, want %s", mass.FillsCoverage.Scope.Through, now)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestGateExecutionMassStatusUsesProviderSnapshotAcrossIO(t *testing.T) {
	provider := gateSpotTestProvider()
	spotID := provider.All()[0].ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.LoadSnapshot(nil)
		writeJSON(t, w, []any{map[string]any{
			"currency_pair": "ETH_USDT", "total": 1, "orders": []any{map[string]any{
				"id": "order-1", "text": "t-client-1", "currency_pair": "ETH_USDT", "type": "limit", "side": "buy",
				"amount": "0.01", "price": "100", "time_in_force": "gtc", "status": "open",
			}},
		}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock()).withScope([]enums.InstrumentKind{enums.KindSpot})
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{spotID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 || mass.OrderReports["order-1"].Order.Request.InstrumentID != spotID {
		t.Fatalf("reports=%+v, want response resolved from pre-I/O snapshot", mass.OrderReports)
	}
}

func TestGateExecutionMassStatusReturnsScopedUnavailableAfterAttempt(t *testing.T) {
	now := time.Date(2026, 7, 15, 21, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		failPath string
		kind     enums.InstrumentKind
		query    func(model.InstrumentID) model.MassStatusQuery
		coverage func(*model.ExecutionMassStatus) model.ReportCoverage
	}{
		{name: "open orders", failPath: "/spot/open_orders", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.OpenOrdersCoverage }},
		{name: "fills", failPath: "/spot/my_trades", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, Since: now.Add(-time.Hour), IncludeFills: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.FillsCoverage }},
		{name: "positions", failPath: "/futures/usdt/positions", kind: enums.KindPerp, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, IncludePositions: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.PositionsCoverage }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := gateFullTestProvider()
			id := firstGateIDOfKind(t, provider, tc.kind)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failPath {
					http.Error(w, "temporary outage", http.StatusServiceUnavailable)
					return
				}
				if r.URL.Path == "/futures/usdt/accounts" {
					writeJSON(t, w, map[string]any{"user": 42, "position_mode": "single"})
					return
				}
				writeJSON(t, w, []any{})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now)).withScope([]enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
			query := tc.query(id)

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err != nil || mass == nil {
				t.Fatalf("mass=%+v err=%v, want typed attempted evidence", mass, err)
			}
			coverage := tc.coverage(mass)
			if coverage.State != model.CoverageUnavailable || coverage.Scope.IsZero() || len(coverage.Scope.InstrumentIDs) != 1 {
				t.Fatalf("coverage=%+v, want scoped attempted Unavailable", coverage)
			}
			if err := mass.ValidateFor(query); err != nil {
				t.Fatalf("ValidateFor: %v", err)
			}
		})
	}
}

func TestGateExecutionMassStatusRetainsSuccessfulProductsAsPartial(t *testing.T) {
	provider := gateFullTestProvider()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/spot/open_orders":
			writeJSON(t, w, []any{})
		case "/futures/usdt/accounts":
			writeJSON(t, w, map[string]any{"user": 42, "position_mode": "single"})
		case "/futures/usdt/orders":
			http.Error(w, "temporary futures outage", http.StatusServiceUnavailable)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock()).withScope([]enums.InstrumentKind{enums.KindSpot, enums.KindPerp})
	configured := provider.All()
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{configured[1].ID, configured[0].ID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoveragePartial || mass.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("open coverage=%+v, want retained successful product with Partial scope", mass.OpenOrdersCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestGateExecutionMassStatusMarksSaturatedFuturesOpenOrdersPartial(t *testing.T) {
	provider := gateFullTestProvider()
	perpID := firstGateIDOfKind(t, provider, enums.KindPerp)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/futures/usdt/accounts":
			writeJSON(t, w, map[string]any{"user": 42, "position_mode": "single"})
		case "/futures/usdt/orders":
			orders := make([]any, 100)
			for i := range orders {
				orders[i] = map[string]any{"id": i + 1, "contract": "BTC_USDT", "size": 1, "left": 1, "price": "50000", "tif": "gtc", "status": "open"}
			}
			writeJSON(t, w, orders)
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(gatesdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock()).withScope([]enums.InstrumentKind{enums.KindPerp})
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{perpID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoveragePartial || !gateHasWarning(mass.Warnings, "OPEN_ORDERS_LIMIT_REACHED") {
		t.Fatalf("coverage=%+v warnings=%+v, want saturated Partial", mass.OpenOrdersCoverage, mass.Warnings)
	}
	if len(mass.OrderReports) != 100 {
		t.Fatalf("reports=%d, want retained positive rows", len(mass.OrderReports))
	}
}

func firstGateIDOfKind(t *testing.T, provider *instrumentProvider, kind enums.InstrumentKind) model.InstrumentID {
	t.Helper()
	for _, inst := range provider.All() {
		if inst.ID.Kind == kind {
			return inst.ID
		}
	}
	t.Fatalf("missing Gate instrument kind %s", kind)
	return model.InstrumentID{}
}
