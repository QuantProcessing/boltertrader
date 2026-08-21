package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeRequiredBoundedOperationalSignals(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetAccountSnapshot([]AccountState{
		{Venue: "binance", Product: "spot", Environment: "live", ReadReady: true, WriteReady: true},
		{Venue: "binance", Product: "spot", Environment: "live", ReadReady: true, WriteReady: false},
	})
	metrics.SetCatalogSnapshot([]CatalogState{{
		Venue: "binance", Product: "spot", Environment: "live",
		LoadedAt: time.Unix(100, 0), Generation: 7,
	}}, time.Unix(130, 0))
	metrics.SetReconciliation(2, 45*time.Second)
	metrics.ObserveFundingMarket("binance", "binance.perp.usdt.v1", "live", time.Unix(125, 0), 2)
	metrics.RecordStream(StreamGap)
	metrics.RecordStream(StreamResync)
	metrics.RecordStream(StreamReplayEviction)
	metrics.ObservePostgres(PostgresMigrate, PostgresOK, 25*time.Millisecond)
	metrics.RecordShutdown(ShutdownDrained)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", response.Code)
	}
	body := response.Body.String()
	for _, required := range []string{
		"gateway_account_read_ready",
		"gateway_account_write_ready",
		"gateway_command_total",
		"gateway_uncertain_command_count",
		"gateway_oldest_reconciliation_age_seconds",
		"gateway_stream_event_total",
		"gateway_catalog_snapshot_age_seconds",
		"gateway_catalog_snapshot_version",
		"gateway_funding_market_feed_state",
		"gateway_funding_market_last_success_unixtime",
		"gateway_funding_market_reconnect_total",
		"gateway_postgres_operation_duration_seconds",
		"gateway_in_flight_command_count",
		"gateway_shutdown_total",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("metrics output missing %q", required)
		}
	}
	if !strings.Contains(body, `gateway_account_read_ready{environment="live",product="spot",venue="binance"} 2`) {
		t.Fatalf("aggregated read readiness missing:\n%s", body)
	}
	if !strings.Contains(body, `gateway_account_write_ready{environment="live",product="spot",venue="binance"} 1`) {
		t.Fatalf("aggregated write readiness missing:\n%s", body)
	}
	if !strings.Contains(body, `gateway_funding_market_feed_state{route="binance.perp.usdt.v1",state="live",venue="binance"} 1`) ||
		!strings.Contains(body, `gateway_funding_market_reconnect_total{route="binance.perp.usdt.v1",venue="binance"} 2`) {
		t.Fatalf("funding-market lifecycle metrics missing:\n%s", body)
	}
}

func TestMetricsCollapseUntrustedDimensionsAndNeverExposeIdentifiers(t *testing.T) {
	metrics := NewMetrics()
	canary := `acct-secret-"雪"-BTCUSDT-idem-101`
	metrics.RecordCommand(CommandOperation(canary), CommandOutcome(canary))
	metrics.RecordStream(StreamEvent(canary))
	metrics.ObservePostgres(PostgresOperation(canary), PostgresErrorClass(canary), time.Millisecond)
	metrics.SetAccountSnapshot([]AccountState{{
		Venue: canary, Product: canary, Environment: canary,
		ReadReady: true, WriteReady: true,
	}})
	metrics.ObserveFundingMarket(canary, canary, canary, time.Now(), 100)

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if strings.Contains(body, canary) || strings.Contains(body, "BTCUSDT") || strings.Contains(body, "idem-101") {
		t.Fatalf("metrics exposed an unbounded identifier:\n%s", body)
	}
	if !strings.Contains(body, `operation="unknown"`) ||
		!strings.Contains(body, `outcome="unknown"`) ||
		!strings.Contains(body, `event="unknown"`) ||
		!strings.Contains(body, `venue="unknown"`) {
		t.Fatalf("untrusted dimensions were not collapsed:\n%s", body)
	}
}

func TestCommandMiddlewareTracksInFlightOutcomeAndUncertainState(t *testing.T) {
	metrics := NewMetrics()
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("uncertain") == "1" {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"state":"uncertain"}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"state":"accepted"}`))
	})
	handler := metrics.CommandMiddleware(next)

	for _, target := range []string{"/v1/orders", "/v1/orders?uncertain=1"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, target, nil))
		if response.Code != http.StatusAccepted {
			t.Fatalf("%s status = %d", target, response.Code)
		}
	}

	response := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, `gateway_command_total{operation="place_order",outcome="accepted"} 1`) {
		t.Fatalf("accepted command metric missing:\n%s", body)
	}
	if !strings.Contains(body, `gateway_command_total{operation="place_order",outcome="uncertain"} 1`) {
		t.Fatalf("uncertain command metric missing:\n%s", body)
	}
	if !strings.Contains(body, "gateway_in_flight_command_count 0") {
		t.Fatalf("in-flight gauge did not return to zero:\n%s", body)
	}
}

func TestMetricsRefreshReconciliationSnapshotFromProvider(t *testing.T) {
	metrics := NewMetrics()
	metrics.now = func() time.Time { return time.Unix(200, 0).UTC() }
	count := 3
	age := 25 * time.Second
	metrics.SetReconciliationSnapshotProvider(func(ctx context.Context, now time.Time) (int, time.Duration, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("provider context: %v", err)
		}
		if got, want := now, time.Unix(200, 0).UTC(); !got.Equal(want) {
			t.Fatalf("provider now = %v, want %v", got, want)
		}
		return count, age, nil
	})

	assertMetrics := func(wantCount, wantAge string) {
		t.Helper()
		response := httptest.NewRecorder()
		metrics.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		body := response.Body.String()
		if !strings.Contains(body, "gateway_uncertain_command_count "+wantCount) ||
			!strings.Contains(body, "gateway_oldest_reconciliation_age_seconds "+wantAge) {
			t.Fatalf("reconciliation snapshot missing count=%s age=%s:\n%s", wantCount, wantAge, body)
		}
	}

	assertMetrics("3", "25")
	count = 0
	age = 0
	assertMetrics("0", "0")
}

func TestCommandMiddlewareUsesFirstWrittenHTTPStatus(t *testing.T) {
	metrics := NewMetrics()
	handler := metrics.CommandMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.WriteHeader(http.StatusInternalServerError)
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/orders", nil))
	if response.Code != http.StatusAccepted {
		t.Fatalf("HTTP status = %d, want first status %d", response.Code, http.StatusAccepted)
	}

	metricsResponse := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsResponse.Body.String()
	if !strings.Contains(body, `gateway_command_total{operation="place_order",outcome="accepted"} 1`) {
		t.Fatalf("first written HTTP status was not observed:\n%s", body)
	}
}
