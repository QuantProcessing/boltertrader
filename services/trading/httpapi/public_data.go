package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
)

type CatalogProjection interface {
	Snapshot(context.Context) (catalog.Snapshot, error)
	Status() catalog.Status
}

type MarketDataProjection interface {
	Get(context.Context, marketdata.Query) (marketdata.Fact, error)
	BudgetStatus(string) marketdata.BudgetStatus
}

type ReadinessProjection interface {
	Status() platformsystem.Snapshot
	Readiness() platformsystem.Snapshot
}

type RealtimeProjection interface {
	ReplayScoped(string, string, string) ([]realtime.Event, error)
	SubscribeScoped(int, string, string) (<-chan realtime.Event, func())
}

type PublicDataDependencies struct {
	Catalog    CatalogProjection
	MarketData MarketDataProjection
	Readiness  ReadinessProjection
	Realtime   RealtimeProjection
	Principal  func(*http.Request) (principalID, mode string, err error)
}

type PublicDataAPI struct{ dependencies PublicDataDependencies }

func NewPublicDataAPI(dependencies PublicDataDependencies) (*PublicDataAPI, error) {
	if dependencies.Catalog == nil || dependencies.MarketData == nil || dependencies.Readiness == nil || dependencies.Realtime == nil || dependencies.Principal == nil {
		return nil, httpx.ConfigErr("public-data dependencies are incomplete")
	}
	return &PublicDataAPI{dependencies: dependencies}, nil
}

// Register mounts only the task-2 browser read surface. The shared server may
// instead call Handle from its frozen-contract dispatcher.
func (api *PublicDataAPI) Register(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("public-data mux is required")
	}
	for _, route := range []string{
		"GET /api/v1/system/status",
		"GET /api/v1/system/readiness",
		"GET /api/v1/catalog/venues",
		"GET /api/v1/catalog/instruments",
		"GET /api/v1/market-data",
		"GET /api/v1/realtime/stream",
	} {
		route := route
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { api.Handle(w, r, route) })
	}
	return nil
}

// Handle returns true when route belongs to the task-2 surface.
func (api *PublicDataAPI) Handle(w http.ResponseWriter, r *http.Request, route string) bool {
	switch route {
	case "GET /api/v1/system/status":
		api.system(w, r, false)
	case "GET /api/v1/system/readiness":
		api.system(w, r, true)
	case "GET /api/v1/catalog/venues":
		api.catalog(w, r, true)
	case "GET /api/v1/catalog/instruments":
		api.catalog(w, r, false)
	case "GET /api/v1/market-data":
		api.marketData(w, r)
	case "GET /api/v1/realtime/stream":
		api.realtime(w, r)
	default:
		return false
	}
	return true
}

func (api *PublicDataAPI) system(w http.ResponseWriter, r *http.Request, readiness bool) {
	if err := validateQuery(r, nil); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	snapshot := api.dependencies.Readiness.Status()
	status := http.StatusOK
	if readiness {
		snapshot = api.dependencies.Readiness.Readiness()
		if !snapshot.Ready {
			status = http.StatusServiceUnavailable
		}
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": snapshot})
}

func (api *PublicDataAPI) catalog(w http.ResponseWriter, r *http.Request, venues bool) {
	if err := validateQuery(r, nil); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	snapshot, err := api.dependencies.Catalog.Snapshot(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "PUBLIC_DATA_NOT_READY", "catalog projection is unavailable", map[string]any{"reason": api.dependencies.Catalog.Status().Reason}, true, httpx.RequestID(r))
		return
	}
	var data any = snapshot.Instruments
	if venues {
		data = snapshot.Venues
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": data, "projection": map[string]any{"revision": snapshot.Revision, "observedAt": snapshot.ObservedAt}})
}

func (api *PublicDataAPI) marketData(w http.ResponseWriter, r *http.Request) {
	if err := validateQuery(r, []string{"venue", "environment", "product", "instrumentId"}); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	query := marketdata.Query{Venue: r.URL.Query().Get("venue"), Environment: r.URL.Query().Get("environment"), Product: r.URL.Query().Get("product"), InstrumentID: r.URL.Query().Get("instrumentId")}
	fact, err := api.dependencies.MarketData.Get(r.Context(), query)
	if err != nil {
		status := http.StatusServiceUnavailable
		code := "PUBLIC_DATA_NOT_READY"
		if errors.Is(err, marketdata.ErrRateLimited) {
			status = http.StatusTooManyRequests
			code = "RATE_LIMITED"
		}
		httpx.WriteError(w, status, code, "market-data projection is unavailable", map[string]any{"venue": query.Venue}, true, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"meta": httpx.Meta(r), "data": fact, "rateLimit": api.dependencies.MarketData.BudgetStatus(query.Venue),
	})
}

func (api *PublicDataAPI) realtime(w http.ResponseWriter, r *http.Request) {
	if err := validateOptionalQuery(r, []string{"cursor"}); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if header := strings.TrimSpace(r.Header.Get("Last-Event-ID")); cursor == "" && header != "" {
		cursor = header
	}
	principal, mode, err := api.dependencies.Principal(r)
	if err != nil || strings.TrimSpace(principal) == "" || (mode != "test" && mode != "live") {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	events, err := api.dependencies.Realtime.ReplayScoped(cursor, principal, mode)
	if errors.Is(err, realtime.ErrResnapshotRequired) {
		httpx.WriteError(w, http.StatusConflict, "RESNAPSHOT_REQUIRED", "realtime cursor cannot prove continuity", nil, true, httpx.RequestID(r))
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "REALTIME_UNAVAILABLE", "realtime stream is unavailable", nil, true, httpx.RequestID(r))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	flusher.Flush()
	for _, event := range events {
		if err := writeSSE(w, event); err != nil {
			return
		}
	}
	flusher.Flush()
	updates, unsubscribe := api.dependencies.Realtime.SubscribeScoped(16, principal, mode)
	defer unsubscribe()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-updates:
			if !open || writeSSE(w, event) != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func validateQuery(r *http.Request, required []string) error {
	if err := validateOptionalQuery(r, required); err != nil {
		return err
	}
	for _, name := range required {
		if strings.TrimSpace(r.URL.Query().Get(name)) == "" {
			return fmt.Errorf("query field %q is required", name)
		}
	}
	return nil
}

func validateOptionalQuery(r *http.Request, optional []string) error {
	allowed := make(map[string]struct{}, len(optional))
	for _, name := range optional {
		allowed[name] = struct{}{}
	}
	for name, values := range r.URL.Query() {
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("unknown query field %q", name)
		}
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			return fmt.Errorf("query field %q must appear once with a value", name)
		}
	}
	return nil
}

func writeSSE(w http.ResponseWriter, event realtime.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.Cursor, event.Kind, data)
	return err
}
