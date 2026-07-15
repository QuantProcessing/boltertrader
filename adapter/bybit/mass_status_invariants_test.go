package bybit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestBybitExecutionMassStatusRejectsInvalidQueryBeforeIO(t *testing.T) {
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
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), bybitTestProvider(), clock.NewRealClock())

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

func TestBybitExecutionMassStatusUsesOneEffectiveFillThrough(t *testing.T) {
	now := time.Date(2026, 7, 15, 16, 0, 0, 0, time.UTC)
	provider := bybitTestProvider()
	spotID := provider.All()[0].ID
	var requestedEnd atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/execution/list":
			end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			requestedEnd.Store(end)
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
				"list":           []any{bybitExecutionFixture("too-new", "order", "ETHUSDT", now.Add(time.Millisecond))},
				"nextPageCursor": "",
			}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now)).withCategories("spot")
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{spotID}, Since: now.Add(-time.Hour), IncludeFills: true}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if requestedEnd.Load() != mass.FillsCoverage.Scope.Through.UnixMilli() {
		t.Fatalf("requested end=%d coverage through=%d", requestedEnd.Load(), mass.FillsCoverage.Scope.Through.UnixMilli())
	}
	if len(mass.FillReports) != 0 {
		t.Fatalf("fills=%+v, want post-through fill filtered", mass.FillReports)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestBybitExecutionMassStatusUsesProviderSnapshotAcrossIO(t *testing.T) {
	provider := bybitTestProvider()
	spotID := provider.All()[0].ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.LoadSnapshot(nil)
		writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{
			"list":           []any{bybitOrderFixture("order-1", "client-1", "ETHUSDT", "Buy", 0, "New")},
			"nextPageCursor": "",
		}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock()).withCategories("spot")
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{spotID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 || mass.OrderReports["order-1"].Order.Request.InstrumentID != spotID {
		t.Fatalf("reports=%+v, want response resolved from pre-I/O snapshot", mass.OrderReports)
	}
}

func TestBybitExecutionMassStatusReturnsScopedUnavailableAfterAttempt(t *testing.T) {
	now := time.Date(2026, 7, 15, 17, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		failPath string
		kind     enums.InstrumentKind
		query    func(model.InstrumentID) model.MassStatusQuery
		coverage func(*model.ExecutionMassStatus) model.ReportCoverage
	}{
		{name: "open orders", failPath: "/v5/order/realtime", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.OpenOrdersCoverage }},
		{name: "fills", failPath: "/v5/execution/list", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, Since: now.Add(-time.Hour), IncludeFills: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.FillsCoverage }},
		{name: "positions", failPath: "/v5/position/list", kind: enums.KindPerp, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, IncludePositions: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.PositionsCoverage }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := bybitTestProvider()
			id := firstBybitIDOfKind(t, provider, tc.kind)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failPath {
					http.Error(w, "temporary outage", http.StatusServiceUnavailable)
					return
				}
				writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now))
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

func TestBybitExecutionMassStatusRetainsSuccessfulSettlementsAsPartial(t *testing.T) {
	provider := bybitTestProvider()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v5/order/realtime":
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}, "nextPageCursor": ""}})
		case "/v5/position/list":
			if r.URL.Query().Get("settleCoin") == bybitsdk.SettleCoinUSDC {
				http.Error(w, "temporary USDC outage", http.StatusServiceUnavailable)
				return
			}
			writeJSON(t, w, map[string]any{"retCode": 0, "retMsg": "OK", "result": map[string]any{"list": []any{}}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bybitsdk.NewClient().WithCredentials("key", "secret").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	configured := provider.All()
	query := model.MassStatusQuery{
		Venue:            VenueName,
		InstrumentIDs:    []model.InstrumentID{configured[2].ID, configured[0].ID, configured[1].ID},
		IncludePositions: true,
	}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.PositionsCoverage.State != model.CoveragePartial || mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("positions coverage=%+v, want retained successful settlement with Partial scope", mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func firstBybitIDOfKind(t *testing.T, provider *instrumentProvider, kind enums.InstrumentKind) model.InstrumentID {
	t.Helper()
	for _, inst := range provider.All() {
		if inst.ID.Kind == kind {
			return inst.ID
		}
	}
	t.Fatalf("missing Bybit instrument kind %s", kind)
	return model.InstrumentID{}
}
