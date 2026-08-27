package observability

import (
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics contains only bounded realtime labels. Instruments and principals
// are deliberately excluded to keep the registry cardinality fixed.
type Metrics struct {
	consumerLag *prometheus.GaugeVec
	resync      *prometheus.CounterVec
	handler     http.Handler
	now         func() time.Time
}

func New() *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		consumerLag: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "trading_market_consumer_lag_seconds", Help: "Age of the latest market fact consumed from Gateway.",
		}, []string{"dataset"}),
		resync: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trading_realtime_resync_total", Help: "Realtime resyncs by bounded browser topic.",
		}, []string{"topic"}),
		now: time.Now,
	}
	registry.MustRegister(metrics.consumerLag, metrics.resync)
	metrics.handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{ErrorHandling: promhttp.ContinueOnError})
	return metrics
}

func (metrics *Metrics) Handler() http.Handler {
	if metrics == nil {
		return http.NotFoundHandler()
	}
	return metrics.handler
}

func (metrics *Metrics) ObserveMarketMessage(topic realtimewire.Topic, observedAt time.Time) {
	if metrics == nil || observedAt.IsZero() {
		return
	}
	age := metrics.now().UTC().Sub(observedAt.UTC())
	if age < 0 {
		age = 0
	}
	metrics.consumerLag.WithLabelValues(safeMarketDataset(topic)).Set(age.Seconds())
}

func (metrics *Metrics) ObserveRealtimeResync(topic realtimewire.Topic) {
	if metrics != nil {
		metrics.resync.WithLabelValues(safeBusinessTopic(topic)).Inc()
	}
}

func safeMarketDataset(topic realtimewire.Topic) string {
	switch topic {
	case realtimewire.TopicMarkPrice, realtimewire.TopicFundingBoard:
		return string(topic)
	default:
		return "unknown"
	}
}

func safeBusinessTopic(topic realtimewire.Topic) string {
	switch topic {
	case realtimewire.TopicArbitrageOpportunities:
		return string(topic)
	default:
		return "unknown"
	}
}
