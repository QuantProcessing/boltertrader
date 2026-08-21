package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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

// CandlesProjection is separate from MarketDataProjection because the two are
// backed by different services — the top-of-book projection caches, the candle
// one does not — and folding them into one interface would force whichever
// implementation answers it to fake the half it does not own.
type CandlesProjection interface {
	Get(context.Context, marketdata.CandleQuery) (marketdata.CandlePage, error)
	BudgetStatus(string) marketdata.BudgetStatus
}

// FundingBoardProjection is its own interface for the same reason
// CandlesProjection is, plus one of its own: the board's BudgetStatus takes no
// venue, because the board is one Gateway read rather than one read per venue.
type FundingBoardProjection interface {
	Get(context.Context) (marketdata.BoardSnapshot, error)
}

// FundingHistoryProjection is its own interface rather than a second method on
// FundingBoardProjection: the board answers one parameterless question about
// every venue at once, this answers a windowed question about one canonical
// instrument id, and the two are backed by services with different caching rules.
type FundingHistoryProjection interface {
	Get(context.Context, marketdata.FundingHistoryQuery) (marketdata.FundingHistorySeries, error)
	BudgetStatus(string) marketdata.BudgetStatus
}

type ReadinessProjection interface {
	Status() platformsystem.Snapshot
	Readiness() platformsystem.Snapshot
}

type RealtimeProjection interface {
	ReplayScoped(string, string) ([]realtime.Event, error)
	SubscribeScoped(int, string) (<-chan realtime.Event, func())
}

type PublicDataDependencies struct {
	Catalog        CatalogProjection
	MarketData     MarketDataProjection
	Candles        CandlesProjection
	FundingBoard   FundingBoardProjection
	FundingHistory FundingHistoryProjection
	Readiness      ReadinessProjection
	Realtime       RealtimeProjection
	Principal      func(*http.Request) (principalID string, err error)
}

type PublicDataAPI struct{ dependencies PublicDataDependencies }

func NewPublicDataAPI(dependencies PublicDataDependencies) (*PublicDataAPI, error) {
	if dependencies.Catalog == nil || dependencies.MarketData == nil || dependencies.Candles == nil || dependencies.FundingBoard == nil || dependencies.FundingHistory == nil || dependencies.Readiness == nil || dependencies.Realtime == nil || dependencies.Principal == nil {
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
		"GET /api/v1/candles",
		"GET /api/v1/funding-board",
		"GET /api/v1/funding-history",
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
	case "GET /api/v1/candles":
		api.candles(w, r)
	case "GET /api/v1/funding-board":
		api.fundingBoard(w, r)
	case "GET /api/v1/funding-history":
		api.fundingHistory(w, r)
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
	var snapshot platformsystem.Snapshot
	status := http.StatusOK
	if readiness {
		snapshot = api.dependencies.Readiness.Readiness()
		if !snapshot.Ready {
			status = http.StatusServiceUnavailable
		}
	} else {
		snapshot = api.dependencies.Readiness.Status()
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
	if err := validateQuery(r, []string{"instrumentId"}); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	instrument, ok := api.resolveInstrument(w, r, "market-data", "")
	if !ok {
		return
	}
	query := marketdata.Query{
		Venue: instrument.Venue, Environment: instrument.Environment,
		Product: instrument.Product, InstrumentID: instrument.InstrumentID,
	}
	fact, err := api.dependencies.MarketData.Get(r.Context(), query)
	if err != nil {
		slog.Warn("public market-data read failed",
			"instrument_id", query.InstrumentID,
			"venue", query.Venue,
			"cause", err,
		)
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

// candles answers the chart's read.
//
// The query is validated field by field rather than through validateQuery
// because limit is genuinely optional — an absent limit means "a screenful",
// which is what the first paint of a chart wants — and validateQuery treats
// every name it is given as required.
func (api *PublicDataAPI) candles(w http.ResponseWriter, r *http.Request) {
	err := validateOptionalQuery(r, []string{"instrumentId", "interval", "limit"})
	if err == nil {
		for _, name := range []string{"instrumentId", "interval"} {
			if strings.TrimSpace(r.URL.Query().Get(name)) == "" {
				err = fmt.Errorf("query field %q is required", name)
				break
			}
		}
	}
	limit := 0
	if err == nil {
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, parseErr := strconv.Atoi(raw)
			if parseErr != nil || parsed <= 0 || parsed > marketdata.MaxCandles {
				err = fmt.Errorf("query field \"limit\" must be between 1 and %d", marketdata.MaxCandles)
			} else {
				limit = parsed
			}
		}
	}
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	instrument, ok := api.resolveInstrument(w, r, "candles", "")
	if !ok {
		return
	}
	query := marketdata.CandleQuery{
		Venue: instrument.Venue, Environment: instrument.Environment,
		Product: instrument.Product, InstrumentID: instrument.InstrumentID,
		Interval: r.URL.Query().Get("interval"), Limit: limit,
	}
	page, err := api.dependencies.Candles.Get(r.Context(), query)
	if err != nil {
		slog.Warn("public candle read failed",
			"instrument_id", query.InstrumentID,
			"venue", query.Venue,
			"interval", query.Interval,
			"cause", err,
		)
		status := http.StatusServiceUnavailable
		code := "PUBLIC_DATA_NOT_READY"
		if errors.Is(err, marketdata.ErrRateLimited) {
			status = http.StatusTooManyRequests
			code = "RATE_LIMITED"
		}
		httpx.WriteError(w, status, code, "candles projection is unavailable", map[string]any{"venue": query.Venue}, true, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"meta": httpx.Meta(r), "data": page, "rateLimit": api.dependencies.Candles.BudgetStatus(query.Venue),
	})
}

// fundingBoard answers the cross-venue funding matrix.
//
// It is the only read on this surface that takes no parameters at all, which is
// the point of it: the board is the one market view that exists before a
// principal has configured any account, so there is nothing to name and nothing
// to scope. validateQuery with no names still runs, and rejects a request that
// carries any query string — a caller passing venue= is asking for something
// this route does not do, and silently ignoring it would let them believe they
// had filtered the board when they had not.
func (api *PublicDataAPI) fundingBoard(w http.ResponseWriter, r *http.Request) {
	if err := validateQuery(r, nil); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	snapshot, err := api.dependencies.FundingBoard.Get(r.Context())
	if err != nil {
		status := http.StatusServiceUnavailable
		code := "PUBLIC_DATA_NOT_READY"
		if errors.Is(err, marketdata.ErrRateLimited) {
			status = http.StatusTooManyRequests
			code = "RATE_LIMITED"
		}
		httpx.WriteError(w, status, code, "funding-board projection is unavailable", nil, true, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": snapshot})
}

// fundingHistory serves one leg of a spread over a named window.
//
// The window is a name rather than a pair of instants because every venue's
// history endpoint is paginated, and "7d" and "30d" are the two spans that fit
// in a single page everywhere on the board. Accepting an arbitrary range would
// mean silently truncating the answer, and a funding chart missing its middle
// looks exactly like a funding chart that is complete.
func (api *PublicDataAPI) fundingHistory(w http.ResponseWriter, r *http.Request) {
	err := validateQuery(r, []string{"instrumentId", "window"})
	var window marketdata.FundingWindow
	if err == nil {
		window, err = marketdata.ParseFundingWindow(r.URL.Query().Get("window"))
	}
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"query": err.Error()}, false, httpx.RequestID(r))
		return
	}
	instrument, ok := api.resolveInstrument(w, r, "funding-history", "perp")
	if !ok {
		return
	}
	query := marketdata.FundingHistoryQuery{
		InstrumentID: instrument.InstrumentID,
		Venue:        instrument.Venue,
		Window:       window,
	}
	series, err := api.dependencies.FundingHistory.Get(r.Context(), query)
	if err != nil {
		status := http.StatusServiceUnavailable
		code := "PUBLIC_DATA_NOT_READY"
		retryable := true
		switch {
		case errors.Is(err, marketdata.ErrRateLimited):
			status = http.StatusTooManyRequests
			code = "RATE_LIMITED"
		// A venue with no history endpoint is a permanent answer, and it has to
		// arrive as one: the arbitrage chart draws two legs, and a leg that
		// cannot be drawn should say so beside the one that can rather than
		// retry behind a spinner for the life of the page.
		case errors.Is(err, marketdata.ErrFundingHistoryUnsupported):
			status = http.StatusNotFound
			code = "FUNDING_HISTORY_UNSUPPORTED"
			retryable = false
		// Also permanent for as long as the board is carrying what it carries,
		// and kept apart from the line above because it is a fact about the
		// symbol rather than about the venue. A chart that reported "bybit
		// publishes no funding history" on the strength of one unlisted symbol
		// would be telling the operator something false and checkable.
		case errors.Is(err, marketdata.ErrFundingHistoryUnknownInstrument):
			status = http.StatusNotFound
			code = "FUNDING_HISTORY_INSTRUMENT_UNKNOWN"
			retryable = false
		}
		httpx.WriteError(w, status, code, "funding-history projection is unavailable",
			map[string]any{"venue": query.Venue, "instrumentId": query.InstrumentID}, retryable, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"meta": httpx.Meta(r), "data": series, "rateLimit": api.dependencies.FundingHistory.BudgetStatus(query.Venue),
	})
}

// resolveInstrument is the public input boundary for every instrument-scoped
// market read. A request supplies one canonical InstrumentID; the catalog owns
// all route and product coordinates derived from it. This makes a mixed request
// (for example an OKX id beside venue=binance) impossible to express instead of
// relying on each handler to reject it consistently.
func (api *PublicDataAPI) resolveInstrument(w http.ResponseWriter, r *http.Request, surface, requiredProduct string) (catalog.Instrument, bool) {
	instrumentID := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	snapshot, err := api.dependencies.Catalog.Snapshot(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "PUBLIC_DATA_NOT_READY", "catalog projection is unavailable", nil, true, httpx.RequestID(r))
		return catalog.Instrument{}, false
	}
	instrument, found := snapshot.Instrument(instrumentID)
	if !found || !instrument.Ready || (requiredProduct != "" && instrument.Product != requiredProduct) {
		code := "MARKET_INSTRUMENT_UNKNOWN"
		if surface == "funding-history" {
			code = "FUNDING_HISTORY_INSTRUMENT_UNKNOWN"
		}
		httpx.WriteError(w, http.StatusNotFound, code, surface+" instrument is unknown",
			map[string]any{"instrumentId": instrumentID}, false, httpx.RequestID(r))
		return catalog.Instrument{}, false
	}
	return instrument, true
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
	principal, err := api.dependencies.Principal(r)
	if err != nil || strings.TrimSpace(principal) == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	events, err := api.dependencies.Realtime.ReplayScoped(cursor, principal)
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
	updates, unsubscribe := api.dependencies.Realtime.SubscribeScoped(16, principal)
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
