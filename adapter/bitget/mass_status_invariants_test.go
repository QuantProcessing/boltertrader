package bitget

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
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

func TestBitgetExecutionMassStatusRejectsInvalidQueryBeforeIO(t *testing.T) {
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
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), bitgetTestProvider(), clock.NewRealClock())

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

func TestBitgetExecutionMassStatusUsesOneEffectiveFillThrough(t *testing.T) {
	now := time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	provider := bitgetTestProvider()
	spotID := provider.All()[0].ID
	var requestedEnd atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/trade/unfilled-orders":
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}}})
		case "/api/v3/trade/fills":
			end, _ := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
			requestedEnd.Store(end)
			list := []any{}
			if r.URL.Query().Get("category") == "SPOT" {
				list = append(list, bitgetFillFixture("too-new", "order", "client", now.Add(time.Millisecond)))
			}
			writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{
				"list":   list,
				"cursor": "",
			}})
		default:
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now))
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

func TestBitgetExecutionMassStatusUsesProviderSnapshotAcrossIO(t *testing.T) {
	provider := bitgetTestProvider()
	spotID := provider.All()[0].ID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.LoadSnapshot(nil)
		records := []any{}
		if r.URL.Query().Get("category") == "SPOT" {
			records = append(records, map[string]any{
				"orderId": "order-1", "clientOid": "client-1", "category": "SPOT", "symbol": "ETHUSDT", "side": "buy",
				"orderType": "limit", "timeInForce": "gtc", "price": "100", "qty": "0.01", "orderStatus": "live",
			})
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": records}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewRealClock())
	query := model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{spotID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 || mass.OrderReports["order-1"].Order.Request.InstrumentID != spotID {
		t.Fatalf("reports=%+v, want response resolved from pre-I/O snapshot", mass.OrderReports)
	}
}

func TestBitgetExecutionMassStatusReturnsScopedUnavailableAfterAttempt(t *testing.T) {
	now := time.Date(2026, 7, 15, 19, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		failPath string
		kind     enums.InstrumentKind
		query    func(model.InstrumentID) model.MassStatusQuery
		coverage func(*model.ExecutionMassStatus) model.ReportCoverage
	}{
		{name: "open orders", failPath: "/api/v3/trade/unfilled-orders", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.OpenOrdersCoverage }},
		{name: "fills", failPath: "/api/v3/trade/fills", kind: enums.KindSpot, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, Since: now.Add(-time.Hour), IncludeFills: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.FillsCoverage }},
		{name: "positions", failPath: "/api/v3/position/current-position", kind: enums.KindPerp, query: func(id model.InstrumentID) model.MassStatusQuery {
			return model.MassStatusQuery{Venue: VenueName, InstrumentIDs: []model.InstrumentID{id}, IncludePositions: true}
		}, coverage: func(m *model.ExecutionMassStatus) model.ReportCoverage { return m.PositionsCoverage }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := bitgetTestProvider()
			id := firstBitgetIDOfKind(t, provider, tc.kind)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tc.failPath {
					http.Error(w, "temporary outage", http.StatusServiceUnavailable)
					return
				}
				writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
			}))
			t.Cleanup(server.Close)
			exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now))
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

func TestBitgetExecutionMassStatusRetainsSuccessfulCategoriesAsPartial(t *testing.T) {
	now := time.Date(2026, 7, 15, 19, 30, 0, 0, time.UTC)
	provider := bitgetTestProvider()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("category") == bitgetsdk.ProductTypeUSDCFutures {
			http.Error(w, "temporary USDC outage", http.StatusServiceUnavailable)
			return
		}
		writeJSON(t, w, map[string]any{"code": "00000", "msg": "success", "data": map[string]any{"list": []any{}, "cursor": ""}})
	}))
	t.Cleanup(server.Close)
	exec := newExecutionClient(bitgetsdk.NewClient().WithCredentials("key", "secret", "pass").WithBaseURL(server.URL).WithHTTPClient(server.Client()), provider, clock.NewSimulatedClock(now))
	query := model.MassStatusQuery{
		Venue:            VenueName,
		InstrumentIDs:    []model.InstrumentID{provider.All()[2].ID, provider.All()[0].ID, provider.All()[1].ID},
		Since:            now.Add(-time.Hour),
		Until:            now,
		IncludeFills:     true,
		IncludePositions: true,
	}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	for name, coverage := range map[string]model.ReportCoverage{
		"open": mass.OpenOrdersCoverage, "fills": mass.FillsCoverage, "positions": mass.PositionsCoverage,
	} {
		if coverage.State != model.CoveragePartial || coverage.Scope.IsZero() {
			t.Fatalf("%s coverage=%+v, want retained successful categories with Partial scope", name, coverage)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func firstBitgetIDOfKind(t *testing.T, provider *instrumentProvider, kind enums.InstrumentKind) model.InstrumentID {
	t.Helper()
	for _, inst := range provider.All() {
		if inst.ID.Kind == kind {
			return inst.ID
		}
	}
	t.Fatalf("missing Bitget instrument kind %s", kind)
	return model.InstrumentID{}
}
