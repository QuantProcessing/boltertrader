package observability

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type CommandOperation string

const (
	CommandPlace       CommandOperation = "place_order"
	CommandCancel      CommandOperation = "cancel_order"
	CommandAmend       CommandOperation = "amend_order"
	CommandSetLeverage CommandOperation = "set_leverage"
	CommandUnknown     CommandOperation = "unknown"
)

type CommandOutcome string

const (
	CommandAccepted       CommandOutcome = "accepted"
	CommandRejected       CommandOutcome = "rejected"
	CommandUncertain      CommandOutcome = "uncertain"
	CommandUnsupported    CommandOutcome = "unsupported"
	CommandError          CommandOutcome = "error"
	CommandOutcomeUnknown CommandOutcome = "unknown"
)

type StreamEvent string

const (
	StreamGap            StreamEvent = "gap"
	StreamResync         StreamEvent = "resync"
	StreamReplayEviction StreamEvent = "replay_eviction"
	StreamUnknown        StreamEvent = "unknown"
)

type PostgresOperation string

const (
	PostgresConnect   PostgresOperation = "connect"
	PostgresMigrate   PostgresOperation = "migrate"
	PostgresRegister  PostgresOperation = "register_account"
	PostgresOwnership PostgresOperation = "account_ownership"
	PostgresUnknown   PostgresOperation = "unknown"
)

type PostgresErrorClass string

const (
	PostgresOK           PostgresErrorClass = "none"
	PostgresUnavailable  PostgresErrorClass = "unavailable"
	PostgresConflict     PostgresErrorClass = "conflict"
	PostgresInternal     PostgresErrorClass = "internal"
	PostgresErrorUnknown PostgresErrorClass = "unknown"
)

type ShutdownOutcome string

const (
	ShutdownDrained  ShutdownOutcome = "drained"
	ShutdownTimedOut ShutdownOutcome = "timed_out"
	ShutdownError    ShutdownOutcome = "error"
	ShutdownUnknown  ShutdownOutcome = "unknown"
)

type AccountState struct {
	Venue       string
	Product     string
	Environment string
	ReadReady   bool
	WriteReady  bool
}

type CatalogState struct {
	Venue       string
	Product     string
	Environment string
	LoadedAt    time.Time
	Generation  uint64
}

type SnapshotProvider func(time.Time) ([]AccountState, []CatalogState)

type ReconciliationSnapshotProvider func(context.Context, time.Time) (int, time.Duration, error)

type Metrics struct {
	registry          *prometheus.Registry
	accountReadReady  *prometheus.GaugeVec
	accountWriteReady *prometheus.GaugeVec
	commandTotal      *prometheus.CounterVec
	uncertainCount    prometheus.Gauge
	oldestReconcile   prometheus.Gauge
	streamTotal       *prometheus.CounterVec
	catalogAge        *prometheus.GaugeVec
	catalogVersion    *prometheus.GaugeVec
	postgresDuration  *prometheus.HistogramVec
	inFlight          prometheus.Gauge
	shutdownTotal     *prometheus.CounterVec
	handler           http.Handler
	mu                sync.Mutex
	providerMu        sync.RWMutex
	provider          SnapshotProvider
	reconciliation    ReconciliationSnapshotProvider
	now               func() time.Time
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	metrics := &Metrics{
		registry: registry,
		now:      time.Now,
		accountReadReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_account_read_ready",
			Help: "Number of configured accounts currently ready for normalized reads.",
		}, []string{"venue", "product", "environment"}),
		accountWriteReady: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_account_write_ready",
			Help: "Number of configured accounts currently ready for durable writes.",
		}, []string{"venue", "product", "environment"}),
		commandTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_command_total",
			Help: "Gateway command results by bounded operation and normalized outcome.",
		}, []string{"operation", "outcome"}),
		uncertainCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gateway_uncertain_command_count",
			Help: "Current number of commands requiring exact reconciliation.",
		}),
		oldestReconcile: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gateway_oldest_reconciliation_age_seconds",
			Help: "Age in seconds of the oldest command requiring exact reconciliation.",
		}),
		streamTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_stream_event_total",
			Help: "Private stream continuity events by bounded event class.",
		}, []string{"event"}),
		catalogAge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_catalog_snapshot_age_seconds",
			Help: "Age in seconds of the current instrument Catalog snapshot.",
		}, []string{"venue", "product", "environment"}),
		catalogVersion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_catalog_snapshot_version",
			Help: "Monotonic generation of the current instrument Catalog snapshot.",
		}, []string{"venue", "product", "environment"}),
		postgresDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_postgres_operation_duration_seconds",
			Help:    "PostgreSQL operation latency by bounded operation and safe error class.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"operation", "error_class"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gateway_in_flight_command_count",
			Help: "Number of currently executing gateway mutation requests.",
		}),
		shutdownTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_shutdown_total",
			Help: "Gateway shutdown results by bounded drain outcome.",
		}, []string{"outcome"}),
	}
	registry.MustRegister(
		metrics.accountReadReady,
		metrics.accountWriteReady,
		metrics.commandTotal,
		metrics.uncertainCount,
		metrics.oldestReconcile,
		metrics.streamTotal,
		metrics.catalogAge,
		metrics.catalogVersion,
		metrics.postgresDuration,
		metrics.inFlight,
		metrics.shutdownTotal,
	)
	for _, operation := range []CommandOperation{CommandPlace, CommandCancel, CommandAmend, CommandSetLeverage, CommandUnknown} {
		for _, outcome := range []CommandOutcome{
			CommandAccepted, CommandRejected, CommandUncertain,
			CommandUnsupported, CommandError, CommandOutcomeUnknown,
		} {
			metrics.commandTotal.WithLabelValues(string(operation), string(outcome))
		}
	}
	for _, event := range []StreamEvent{StreamGap, StreamResync, StreamReplayEviction, StreamUnknown} {
		metrics.streamTotal.WithLabelValues(string(event))
	}
	for _, outcome := range []ShutdownOutcome{ShutdownDrained, ShutdownTimedOut, ShutdownError, ShutdownUnknown} {
		metrics.shutdownTotal.WithLabelValues(string(outcome))
	}
	prometheusHandler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	})
	metrics.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		metrics.refreshSnapshots(r.Context())
		prometheusHandler.ServeHTTP(w, r)
	})
	return metrics
}

func (metrics *Metrics) Handler() http.Handler {
	if metrics == nil {
		return http.NotFoundHandler()
	}
	return metrics.handler
}

func (metrics *Metrics) SetSnapshotProvider(provider SnapshotProvider) {
	if metrics == nil {
		return
	}
	metrics.providerMu.Lock()
	defer metrics.providerMu.Unlock()
	metrics.provider = provider
}

func (metrics *Metrics) SetReconciliationSnapshotProvider(provider ReconciliationSnapshotProvider) {
	if metrics == nil {
		return
	}
	metrics.providerMu.Lock()
	defer metrics.providerMu.Unlock()
	metrics.reconciliation = provider
}

func (metrics *Metrics) refreshSnapshots(ctx context.Context) {
	metrics.providerMu.RLock()
	provider := metrics.provider
	reconciliation := metrics.reconciliation
	metrics.providerMu.RUnlock()
	now := metrics.now().UTC()
	if provider != nil {
		accounts, catalogs := provider(now)
		metrics.SetAccountSnapshot(accounts)
		metrics.SetCatalogSnapshot(catalogs, now)
	}
	if reconciliation != nil {
		count, age, err := reconciliation(ctx, now)
		if err == nil {
			metrics.SetReconciliation(count, age)
		}
	}
}

func (metrics *Metrics) SetAccountSnapshot(states []AccountState) {
	if metrics == nil {
		return
	}
	type aggregate struct {
		read  int
		write int
	}
	counts := make(map[[3]string]aggregate)
	for _, state := range states {
		key := [3]string{
			safeVenue(state.Venue),
			safeProduct(state.Product),
			safeEnvironment(state.Environment),
		}
		value := counts[key]
		if state.ReadReady {
			value.read++
		}
		if state.WriteReady {
			value.write++
		}
		counts[key] = value
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.accountReadReady.Reset()
	metrics.accountWriteReady.Reset()
	for key, value := range counts {
		metrics.accountReadReady.WithLabelValues(key[0], key[1], key[2]).Set(float64(value.read))
		metrics.accountWriteReady.WithLabelValues(key[0], key[1], key[2]).Set(float64(value.write))
	}
}

func (metrics *Metrics) SetCatalogSnapshot(states []CatalogState, now time.Time) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.catalogAge.Reset()
	metrics.catalogVersion.Reset()
	for _, state := range states {
		labels := []string{
			safeVenue(state.Venue),
			safeProduct(state.Product),
			safeEnvironment(state.Environment),
		}
		age := float64(0)
		if !state.LoadedAt.IsZero() && now.After(state.LoadedAt) {
			age = now.Sub(state.LoadedAt).Seconds()
		}
		metrics.catalogAge.WithLabelValues(labels...).Set(age)
		metrics.catalogVersion.WithLabelValues(labels...).Set(float64(state.Generation))
	}
}

func (metrics *Metrics) RecordCommand(operation CommandOperation, outcome CommandOutcome) {
	if metrics == nil {
		return
	}
	metrics.commandTotal.WithLabelValues(
		string(safeCommandOperation(operation)),
		string(safeCommandOutcome(outcome)),
	).Inc()
}

func (metrics *Metrics) SetReconciliation(count int, oldestAge time.Duration) {
	if metrics == nil {
		return
	}
	if count < 0 {
		count = 0
	}
	if oldestAge < 0 {
		oldestAge = 0
	}
	metrics.uncertainCount.Set(float64(count))
	metrics.oldestReconcile.Set(oldestAge.Seconds())
}

func (metrics *Metrics) RecordStream(event StreamEvent) {
	if metrics == nil {
		return
	}
	metrics.streamTotal.WithLabelValues(string(safeStreamEvent(event))).Inc()
}

func (metrics *Metrics) ObserveResync(reason string) {
	if metrics == nil {
		return
	}
	metrics.RecordStream(StreamResync)
	switch {
	case strings.Contains(reason, "gap"):
		metrics.RecordStream(StreamGap)
	case reason == "cursor_evicted":
		metrics.RecordStream(StreamReplayEviction)
	}
}

func (metrics *Metrics) ObservePostgres(operation PostgresOperation, errorClass PostgresErrorClass, duration time.Duration) {
	if metrics == nil {
		return
	}
	if duration < 0 {
		duration = 0
	}
	metrics.postgresDuration.WithLabelValues(
		string(safePostgresOperation(operation)),
		string(safePostgresErrorClass(errorClass)),
	).Observe(duration.Seconds())
}

func (metrics *Metrics) RecordShutdown(outcome ShutdownOutcome) {
	if metrics == nil {
		return
	}
	metrics.shutdownTotal.WithLabelValues(string(safeShutdownOutcome(outcome))).Inc()
}

func (metrics *Metrics) CommandMiddleware(next http.Handler) http.Handler {
	if metrics == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation, ok := commandOperation(r.Method, r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		writer := &commandResponseWriter{ResponseWriter: w, status: http.StatusOK}
		metrics.inFlight.Inc()
		defer func() {
			metrics.inFlight.Dec()
			metrics.RecordCommand(operation, commandResult(writer.status, writer.body.Bytes()))
		}()
		next.ServeHTTP(writer, r)
	})
}

const maxObservedCommandBody = 8 << 10

type commandResponseWriter struct {
	http.ResponseWriter
	status      int
	body        bytes.Buffer
	wroteHeader bool
}

func (writer *commandResponseWriter) WriteHeader(status int) {
	if writer.wroteHeader {
		return
	}
	writer.wroteHeader = true
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *commandResponseWriter) Write(data []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	if remaining := maxObservedCommandBody - writer.body.Len(); remaining > 0 {
		if len(data) > remaining {
			_, _ = writer.body.Write(data[:remaining])
		} else {
			_, _ = writer.body.Write(data)
		}
	}
	return writer.ResponseWriter.Write(data)
}

func commandOperation(method, path string) (CommandOperation, bool) {
	switch {
	case method == http.MethodPost && path == "/v1/orders":
		return CommandPlace, true
	case method == http.MethodDelete && strings.HasPrefix(path, "/v1/orders/"):
		return CommandCancel, true
	case method == http.MethodPatch && strings.HasPrefix(path, "/v1/orders/"):
		return CommandAmend, true
	case method == http.MethodPut && strings.HasPrefix(path, "/v1/accounts/") && strings.HasSuffix(path, "/leverage"):
		return CommandSetLeverage, true
	default:
		return "", false
	}
}

func commandResult(status int, body []byte) CommandOutcome {
	if bytes.Contains(body, []byte(`"state":"uncertain"`)) ||
		bytes.Contains(body, []byte(`"code":"outcome_uncertain"`)) {
		return CommandUncertain
	}
	if status >= 200 && status < 300 {
		return CommandAccepted
	}
	if status == http.StatusNotImplemented {
		return CommandUnsupported
	}
	if status >= 400 && status < 500 {
		return CommandRejected
	}
	return CommandError
}

func safeVenue(value string) string {
	return safeFixed(strings.ToLower(value), []string{
		"binance", "okx", "lighter", "hyperliquid", "bybit",
		"bitget", "gate", "aster", "nado",
	})
}

func safeProduct(value string) string {
	return safeFixed(strings.ToLower(value), []string{"spot", "perp"})
}

func safeEnvironment(value string) string {
	return safeFixed(strings.ToLower(value), []string{"live", "demo", "testnet"})
}

func safeCommandOperation(value CommandOperation) CommandOperation {
	switch value {
	case CommandPlace, CommandCancel, CommandAmend, CommandSetLeverage:
		return value
	default:
		return CommandUnknown
	}
}

func safeCommandOutcome(value CommandOutcome) CommandOutcome {
	switch value {
	case CommandAccepted, CommandRejected, CommandUncertain, CommandUnsupported, CommandError:
		return value
	default:
		return CommandOutcomeUnknown
	}
}

func safeStreamEvent(value StreamEvent) StreamEvent {
	switch value {
	case StreamGap, StreamResync, StreamReplayEviction:
		return value
	default:
		return StreamUnknown
	}
}

func safePostgresOperation(value PostgresOperation) PostgresOperation {
	switch value {
	case PostgresConnect, PostgresMigrate, PostgresRegister, PostgresOwnership:
		return value
	default:
		return PostgresUnknown
	}
}

func safePostgresErrorClass(value PostgresErrorClass) PostgresErrorClass {
	switch value {
	case PostgresOK, PostgresUnavailable, PostgresConflict, PostgresInternal:
		return value
	default:
		return PostgresErrorUnknown
	}
}

func safeShutdownOutcome(value ShutdownOutcome) ShutdownOutcome {
	switch value {
	case ShutdownDrained, ShutdownTimedOut, ShutdownError:
		return value
	default:
		return ShutdownUnknown
	}
}

func safeFixed(value string, allowed []string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return candidate
		}
	}
	return "unknown"
}
