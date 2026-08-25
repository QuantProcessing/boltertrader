package observability

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
)

func TestRealtimeMetricsUseBoundedTopicLabels(t *testing.T) {
	metrics := New()
	metrics.now = func() time.Time { return time.Date(2026, 8, 25, 2, 0, 2, 0, time.UTC) }
	metrics.ObserveMarketMessage(realtimewire.TopicBBO, time.Date(2026, 8, 25, 2, 0, 0, 0, time.UTC))
	metrics.ObserveRealtimeResync(realtimewire.Topic("user-controlled-symbol"))
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	if !strings.Contains(body, `trading_market_consumer_lag_seconds{dataset="market.bbo"} 2`) || !strings.Contains(body, `trading_realtime_resync_total{topic="unknown"} 1`) {
		t.Fatalf("metrics body=%s", body)
	}
}
