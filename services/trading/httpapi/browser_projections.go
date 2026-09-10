package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

type ProjectionReadiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

type AccountRuntimeProjection = accountpositions.AccountRuntimeProjection

type AccountRuntimeProjectionReader interface {
	ListAccountRuntimes(context.Context, string, string) ([]AccountRuntimeProjection, error)
}

// AccountBalanceProjection is the browser-safe slice of a venue balance. The
// Gateway owns the raw resource and its account routing; trading owns the
// principal-scoped browser projection, so account facts do not bypass the same
// identity boundary as positions and orders.
type AccountBalanceProjection = accountpositions.AccountBalanceProjection

type AccountBalanceProjectionReader interface {
	ListAccountBalances(context.Context, string, string) ([]AccountBalanceProjection, error)
}

// AccountPositionProjection is the current venue position for one owned
// account. Quantity is signed so every browser consumer reads long/short the
// same way: positive is long, negative is short.
type AccountPositionProjection = accountpositions.AccountPositionProjection

type AccountPositionProjectionReader interface {
	ListAccountPositions(context.Context, string, string, string) ([]AccountPositionProjection, error)
}

type FailClosedAccountBalanceProjectionReader struct{}

func (FailClosedAccountBalanceProjectionReader) ListAccountBalances(context.Context, string, string) ([]AccountBalanceProjection, error) {
	return nil, fmt.Errorf("account balance projection is unavailable")
}

type FailClosedAccountPositionProjectionReader struct{}

func (FailClosedAccountPositionProjectionReader) ListAccountPositions(context.Context, string, string, string) ([]AccountPositionProjection, error) {
	return nil, fmt.Errorf("account position projection is unavailable")
}

type FailClosedAccountRuntimeProjectionReader struct{}

func (FailClosedAccountRuntimeProjectionReader) ListAccountRuntimes(context.Context, string, string) ([]AccountRuntimeProjection, error) {
	return nil, fmt.Errorf("account runtime projection is unavailable")
}

type CommandProjectionReader interface {
	ListOwned(context.Context, string, string) ([]orders.Command, error)
}

type ActivityRecord struct {
	PrincipalID string
	Kind        string
	ProductID   string
	AccountID   string
	ResourceID  string
	Payload     any
}

type ActivityEvent struct {
	EventID    string          `json:"eventId"`
	Sequence   uint64          `json:"sequence"`
	Kind       string          `json:"kind"`
	ProductID  *string         `json:"productId"`
	AccountID  *string         `json:"accountId"`
	ResourceID *string         `json:"resourceId"`
	Payload    json.RawMessage `json:"payload"`
	ObservedAt time.Time       `json:"observedAt"`
	principal  string
}

type ActivityQuery struct {
	Kind       string
	ProductID  string
	AccountID  string
	ResourceID string
}

type ActivityProjectionReader interface {
	ListActivity(context.Context, string, ActivityQuery) ([]ActivityEvent, error)
}

type ActivityPublisher interface {
	Publish(ActivityRecord) (ActivityEvent, error)
}

type ActivityLog struct {
	capacity int
	now      func() time.Time
	next     uint64
	mu       sync.RWMutex
	events   []ActivityEvent
}

func NewActivityLog(capacity int, now func() time.Time) (*ActivityLog, error) {
	if capacity <= 0 || now == nil {
		return nil, httpx.ConfigErr("activity projection dependencies are incomplete")
	}
	return &ActivityLog{capacity: capacity, now: now}, nil
}

func (log *ActivityLog) Publish(record ActivityRecord) (ActivityEvent, error) {
	if strings.TrimSpace(record.PrincipalID) == "" || strings.TrimSpace(record.Kind) == "" {
		return ActivityEvent{}, fmt.Errorf("activity scope and kind are required")
	}
	payload := record.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return ActivityEvent{}, fmt.Errorf("encode activity payload: %w", err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	log.next++
	event := ActivityEvent{EventID: fmt.Sprintf("activity-%d", log.next), Sequence: log.next, Kind: record.Kind, ProductID: stringPointer(record.ProductID), AccountID: stringPointer(record.AccountID), ResourceID: stringPointer(record.ResourceID), Payload: encoded, ObservedAt: log.now().UTC(), principal: record.PrincipalID}
	log.events = append(log.events, event)
	if len(log.events) > log.capacity {
		log.events = append([]ActivityEvent(nil), log.events[len(log.events)-log.capacity:]...)
	}
	return cloneActivityEvent(event), nil
}

func (log *ActivityLog) ListActivity(_ context.Context, principal string, query ActivityQuery) ([]ActivityEvent, error) {
	if principal == "" {
		return nil, fmt.Errorf("activity scope is required")
	}
	log.mu.RLock()
	defer log.mu.RUnlock()
	items := make([]ActivityEvent, 0, len(log.events))
	for index := len(log.events) - 1; index >= 0; index-- {
		event := log.events[index]
		if event.principal != principal || (query.Kind != "" && event.Kind != query.Kind) || (query.ProductID != "" && stringValuePointer(event.ProductID) != query.ProductID) || (query.AccountID != "" && stringValuePointer(event.AccountID) != query.AccountID) || (query.ResourceID != "" && stringValuePointer(event.ResourceID) != query.ResourceID) {
			continue
		}
		items = append(items, cloneActivityEvent(event))
	}
	return items, nil
}

type BrowserProjectionRoutesOptions struct {
	Runtimes           AccountRuntimeProjectionReader
	Balances           AccountBalanceProjectionReader
	Positions          AccountPositionProjectionReader
	PortfolioPositions accountpositions.Reader
	Commands           CommandProjectionReader
	Activity           ActivityProjectionReader
	Principal          func(*http.Request) (principalID string, err error)
}

func RegisterBrowserProjectionRoutes(mux *http.ServeMux, options BrowserProjectionRoutesOptions) error {
	if mux == nil || options.Runtimes == nil || options.Balances == nil || options.Commands == nil || options.Activity == nil || options.Principal == nil {
		return httpx.ConfigErr("browser projection route dependencies are incomplete")
	}
	if options.Positions == nil {
		options.Positions = FailClosedAccountPositionProjectionReader{}
	}
	authoritativePositions := options.PortfolioPositions
	if options.PortfolioPositions == nil {
		options.PortfolioPositions = failClosedPortfolioPositionReader{}
	}
	handler := &browserProjectionHandler{options: options, authoritativePositions: authoritativePositions}
	mux.HandleFunc("GET /api/v1/accounts/{accountId}/runtimes", handler.accountRuntimes)
	mux.HandleFunc("GET /api/v1/balances", handler.balances)
	mux.HandleFunc("GET /api/v1/account-positions", handler.accountPositions)
	mux.HandleFunc("GET /api/v1/portfolio/positions", handler.portfolioPositions)
	mux.HandleFunc("GET /api/v1/commands", handler.commands)
	mux.HandleFunc("GET /api/v1/fills", handler.fills)
	mux.HandleFunc("GET /api/v1/activity", handler.activity)
	return nil
}

func (handler *browserProjectionHandler) accountPositions(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"accountId", "instrumentId"}, w) {
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	instrumentID := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	if accountID == "" || instrumentID == "" {
		projectionError(w, r, http.StatusBadRequest, "ACCOUNT_POSITION_SCOPE_REQUIRED")
		return
	}
	if handler.authoritativePositions != nil {
		snapshot, err := handler.authoritativePositions.Snapshot(r.Context(), principal, accountpositions.Filter{AccountID: accountID, InstrumentID: instrumentID})
		if err != nil {
			projectionFailure(w, r, "ACCOUNT_POSITION_PROJECTION_UNAVAILABLE", err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": snapshot})
		return
	}
	items, err := handler.options.Positions.ListAccountPositions(r.Context(), principal, accountID, instrumentID)
	if err != nil {
		projectionFailure(w, r, "ACCOUNT_POSITION_PROJECTION_UNAVAILABLE", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *browserProjectionHandler) portfolioPositions(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"accountId", "instrumentId", "venue", "product", "status"}, w) {
		return
	}
	status := accountpositions.Freshness(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != accountpositions.FreshnessSyncing && status != accountpositions.FreshnessLive &&
		status != accountpositions.FreshnessDelayed && status != accountpositions.FreshnessStale && status != accountpositions.FreshnessUnavailable {
		projectionError(w, r, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	snapshot, err := handler.options.PortfolioPositions.Snapshot(r.Context(), principal, accountpositions.Filter{
		AccountID: strings.TrimSpace(r.URL.Query().Get("accountId")), InstrumentID: strings.TrimSpace(r.URL.Query().Get("instrumentId")),
		Venue: strings.TrimSpace(r.URL.Query().Get("venue")), Product: strings.TrimSpace(r.URL.Query().Get("product")), Status: status,
	})
	if err != nil {
		projectionFailure(w, r, "PORTFOLIO_POSITION_PROJECTION_UNAVAILABLE", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": snapshot})
}

type failClosedPortfolioPositionReader struct{}

func (failClosedPortfolioPositionReader) Snapshot(context.Context, string, accountpositions.Filter) (accountpositions.Snapshot, error) {
	return accountpositions.Snapshot{}, fmt.Errorf("portfolio position projection is unavailable")
}

func (handler *browserProjectionHandler) balances(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"accountId"}, w) {
		return
	}
	accountID := strings.TrimSpace(r.URL.Query().Get("accountId"))
	if accountID == "" {
		projectionError(w, r, http.StatusBadRequest, "ACCOUNT_ID_REQUIRED")
		return
	}
	items, err := handler.options.Balances.ListAccountBalances(r.Context(), principal, accountID)
	if err != nil {
		projectionFailure(w, r, "BALANCE_PROJECTION_UNAVAILABLE", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

type browserProjectionHandler struct {
	options                BrowserProjectionRoutesOptions
	authoritativePositions accountpositions.Reader
}

func (handler *browserProjectionHandler) accountRuntimes(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, nil, w) {
		return
	}
	items, err := handler.options.Runtimes.ListAccountRuntimes(r.Context(), principal, r.PathValue("accountId"))
	if err != nil {
		projectionFailure(w, r, "RUNTIME_PROJECTION_UNAVAILABLE", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *browserProjectionHandler) commands(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"productId", "accountId", "state"}, w) {
		return
	}
	items, err := handler.listCommands(r.Context(), principal, r.URL.Query().Get("productId"))
	if err != nil {
		projectionFailure(w, r, "COMMAND_PROJECTION_UNAVAILABLE", err)
		return
	}
	views := make([]CommandProjection, 0, len(items))
	for _, item := range items {
		if accountID := r.URL.Query().Get("accountId"); accountID != "" && item.AccountID != accountID {
			continue
		}
		if state := r.URL.Query().Get("state"); state != "" && string(item.State) != state {
			continue
		}
		views = append(views, commandProjection(item))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": views}})
}

func (handler *browserProjectionHandler) fills(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"productId", "accountId", "instrumentId", "lineageId"}, w) {
		return
	}
	items, err := handler.listCommands(r.Context(), principal, r.URL.Query().Get("productId"))
	if err != nil {
		projectionFailure(w, r, "FILL_PROJECTION_UNAVAILABLE", err)
		return
	}
	result := make([]FillProjection, 0)
	seen := make(map[string]struct{})
	for _, command := range items {
		for _, fill := range command.ExecutionEvidence.Fills {
			key := command.CommandID + "\x00" + fill.FillID
			if _, duplicate := seen[key]; duplicate || (r.URL.Query().Get("accountId") != "" && fill.AccountID != r.URL.Query().Get("accountId")) || (r.URL.Query().Get("instrumentId") != "" && fill.InstrumentID != r.URL.Query().Get("instrumentId")) || (r.URL.Query().Get("lineageId") != "" && command.LineageID != r.URL.Query().Get("lineageId")) {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, fillProjection(command, fill))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OccurredAt.Equal(result[j].OccurredAt) {
			return result[i].FillID < result[j].FillID
		}
		return result[i].OccurredAt.After(result[j].OccurredAt)
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": result}})
}

func (handler *browserProjectionHandler) activity(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"kind", "productId", "accountId", "resourceId"}, w) {
		return
	}
	items, err := handler.options.Activity.ListActivity(r.Context(), principal, ActivityQuery{Kind: r.URL.Query().Get("kind"), ProductID: r.URL.Query().Get("productId"), AccountID: r.URL.Query().Get("accountId"), ResourceID: r.URL.Query().Get("resourceId")})
	if err != nil {
		projectionFailure(w, r, "ACTIVITY_PROJECTION_UNAVAILABLE", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *browserProjectionHandler) listCommands(ctx context.Context, principal, productID string) ([]orders.Command, error) {
	products := []string{productID}
	if productID == "" {
		products = []string{"manual"}
	}
	items := make([]orders.Command, 0)
	seen := make(map[string]struct{})
	for _, product := range products {
		projected, err := handler.options.Commands.ListOwned(ctx, principal, product)
		if err != nil {
			return nil, err
		}
		for _, item := range projected {
			if item.PrincipalID != principal || item.ProductID != product {
				continue
			}
			if _, duplicate := seen[item.CommandID]; duplicate {
				continue
			}
			seen[item.CommandID] = struct{}{}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CommandID < items[j].CommandID
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

func (handler *browserProjectionHandler) identity(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, err := handler.options.Principal(r)
	if err != nil || principal == "" {
		projectionError(w, r, http.StatusUnauthorized, "AUTH_REQUIRED")
		return "", false
	}
	return principal, true
}

type CommandProjection struct {
	CommandID      string           `json:"commandId"`
	AccountID      string           `json:"accountId"`
	LineageID      string           `json:"lineageId"`
	ProductID      string           `json:"productId"`
	ReservationID  *string          `json:"reservationId"`
	IdempotencyKey string           `json:"idempotencyKey"`
	CommandKind    string           `json:"commandKind"`
	State          orders.State     `json:"state"`
	Blockers       []orders.Blocker `json:"blockers"`
	CreatedAt      time.Time        `json:"createdAt"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}

type FillProjection struct {
	FillID             string    `json:"fillId"`
	CommandID          string    `json:"commandId"`
	LineageID          string    `json:"lineageId"`
	ProductID          string    `json:"productId"`
	AccountID          string    `json:"accountId"`
	InstrumentID       string    `json:"instrumentId"`
	Side               string    `json:"side"`
	Quantity           string    `json:"quantity"`
	Price              string    `json:"price"`
	Fee                string    `json:"fee"`
	FeeCurrency        string    `json:"feeCurrency"`
	SettlementCurrency string    `json:"settlementCurrency,omitempty"`
	VenueSequence      *int64    `json:"venueSequence"`
	OccurredAt         time.Time `json:"occurredAt"`
}

func commandProjection(command orders.Command) CommandProjection {
	return CommandProjection{CommandID: command.CommandID, AccountID: command.AccountID, LineageID: command.LineageID, ProductID: command.ProductID, ReservationID: command.ReservationID, IdempotencyKey: command.IdempotencyKey, CommandKind: command.CommandKind, State: command.State, Blockers: append([]orders.Blocker(nil), command.Blockers...), CreatedAt: command.CreatedAt, UpdatedAt: command.UpdatedAt}
}

func fillProjection(command orders.Command, fill orders.FillEvidence) FillProjection {
	return FillProjection{FillID: fill.FillID, CommandID: command.CommandID, LineageID: command.LineageID, ProductID: command.ProductID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID, Side: fill.Side, Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt}
}

// publishCommandActivity names the event after the state the command now
// holds, which is what a placement changes.
func publishCommandActivity(publisher ActivityPublisher, command orders.Command) {
	publishCommandActivityAs(publisher, command, "command."+string(command.State))
}

// publishCommandActivityAs lets a caller name the event after the intent
// instead of the state. A cancel needs this: the venue has not confirmed it
// yet, so the command is still "submitted", and deriving the kind from the
// state would file the cancellation under the same name as the placement it
// cancels — leaving the audit chain unable to say a cancel ever happened.
func publishCommandActivityAs(publisher ActivityPublisher, command orders.Command, kind string) {
	if publisher == nil {
		return
	}
	_, _ = publisher.Publish(ActivityRecord{PrincipalID: command.PrincipalID, Kind: kind, ProductID: command.ProductID, AccountID: command.AccountID, ResourceID: command.CommandID, Payload: commandProjection(command)})
	for _, fill := range command.ExecutionEvidence.Fills {
		_, _ = publisher.Publish(ActivityRecord{PrincipalID: command.PrincipalID, Kind: "fill.recorded", ProductID: command.ProductID, AccountID: fill.AccountID, ResourceID: fill.FillID, Payload: fillProjection(command, fill)})
	}
}

func PublishCommandActivity(publisher ActivityPublisher, command orders.Command) {
	publishCommandActivity(publisher, command)
}

func validOptionalQuery(r *http.Request, allowedFields []string, w http.ResponseWriter) bool {
	allowed := make(map[string]struct{}, len(allowedFields))
	for _, field := range allowedFields {
		allowed[field] = struct{}{}
	}
	for field, values := range r.URL.Query() {
		if _, ok := allowed[field]; !ok || len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			projectionError(w, r, http.StatusBadRequest, "VALIDATION_ERROR")
			return false
		}
	}
	return true
}

func projectionFailure(w http.ResponseWriter, r *http.Request, code string, err error) {
	slog.Error("trading projection read failed", "code", code, "method", r.Method, "path", r.URL.Path, "cause", err)
	projectionError(w, r, http.StatusServiceUnavailable, code)
}

func projectionError(w http.ResponseWriter, r *http.Request, status int, code string) {
	httpx.WriteError(w, status, code, "request rejected", nil, status >= http.StatusInternalServerError, httpx.RequestID(r))
}

func stringPointer(value string) *string {
	if value == "" {
		return nil
	}
	result := value
	return &result
}

func stringValuePointer(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneActivityEvent(event ActivityEvent) ActivityEvent {
	event.Payload = append(json.RawMessage(nil), event.Payload...)
	event.ProductID = stringPointer(stringValuePointer(event.ProductID))
	event.AccountID = stringPointer(stringValuePointer(event.AccountID))
	event.ResourceID = stringPointer(stringValuePointer(event.ResourceID))
	return event
}
