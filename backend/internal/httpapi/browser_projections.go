package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/realtime"
)

type ProjectionReadiness struct {
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
}

type CapabilityOperations struct {
	Place         bool `json:"place"`
	Amend         bool `json:"amend"`
	Cancel        bool `json:"cancel"`
	OpenOrders    bool `json:"openOrders"`
	OrderHistory  bool `json:"orderHistory"`
	Fills         bool `json:"fills"`
	Balances      bool `json:"balances"`
	Account       bool `json:"account"`
	Positions     bool `json:"positions"`
	Leverage      bool `json:"leverage"`
	PrivateStream bool `json:"privateStream"`
	MarginMode    bool `json:"marginMode"`
	PositionMode  bool `json:"positionMode"`
	FundsTransfer bool `json:"fundsTransfer"`
}

type AccountCapabilityProjection struct {
	RowID      string               `json:"rowId"`
	Venue      string               `json:"venue"`
	Product    string               `json:"product"`
	Settlement string               `json:"settlement,omitempty"`
	WriteReady bool                 `json:"writeReady"`
	Operations CapabilityOperations `json:"operations"`
}

type RouteRuntimeProjection struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}

type RuntimeSnapshotProjection struct {
	DesiredStateRevision int64                    `json:"desiredStateRevision"`
	RuntimeGeneration    int64                    `json:"runtimeGeneration"`
	CredentialVersion    int64                    `json:"credentialVersion"`
	RequestedState       string                   `json:"requestedState"`
	Routes               []RouteRuntimeProjection `json:"routes"`
}

type PendingRuntimeSnapshotProjection struct {
	RuntimeSnapshotProjection
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type AccountRuntimeProjection struct {
	AccountID                       string                            `json:"accountId"`
	Active                          *RuntimeSnapshotProjection        `json:"active,omitempty"`
	Pending                         *PendingRuntimeSnapshotProjection `json:"pending,omitempty"`
	Retiring                        []RuntimeSnapshotProjection       `json:"retiring"`
	DesiredStateRevision            int64                             `json:"desiredStateRevision"`
	RuntimeGeneration               int64                             `json:"runtimeGeneration"`
	CredentialVersion               int64                             `json:"credentialVersion"`
	RuntimeVersion                  int64                             `json:"runtimeVersion"`
	Readiness                       ProjectionReadiness               `json:"readiness"`
	WriteReadiness                  ProjectionReadiness               `json:"writeReadiness"`
	LastAppliedDesiredStateRevision int64                             `json:"lastAppliedDesiredStateRevision"`
	LastAppliedRuntimeGeneration    int64                             `json:"lastAppliedRuntimeGeneration"`
	BlockedReason                   *string                           `json:"blockedReason"`
	Capabilities                    []AccountCapabilityProjection     `json:"capabilities"`
	ObservedAt                      time.Time                         `json:"observedAt"`
}

type AccountRuntimeProjectionReader interface {
	ListAccountRuntimes(context.Context, string, string, string) ([]AccountRuntimeProjection, error)
}

type FailClosedAccountRuntimeProjectionReader struct{}

func (FailClosedAccountRuntimeProjectionReader) ListAccountRuntimes(context.Context, string, string, string) ([]AccountRuntimeProjection, error) {
	return nil, fmt.Errorf("account runtime projection is unavailable")
}

type CommandProjectionReader interface {
	ListOwned(context.Context, string, orders.Mode, string) ([]orders.Command, error)
}

type ActivityRecord struct {
	PrincipalID string
	Mode        string
	Kind        string
	ProductID   string
	AccountID   string
	ResourceID  string
	Payload     any
}

type ActivityEvent struct {
	EventID    string          `json:"eventId"`
	Sequence   uint64          `json:"sequence"`
	Cursor     string          `json:"cursor"`
	Kind       string          `json:"kind"`
	ProductID  *string         `json:"productId"`
	AccountID  *string         `json:"accountId"`
	ResourceID *string         `json:"resourceId"`
	Payload    json.RawMessage `json:"payload"`
	ObservedAt time.Time       `json:"observedAt"`
	principal  string
	mode       string
}

type ActivityQuery struct {
	Kind       string
	ProductID  string
	AccountID  string
	ResourceID string
}

type ActivityProjectionReader interface {
	ListActivity(context.Context, string, string, ActivityQuery) ([]ActivityEvent, error)
}

type ActivityPublisher interface {
	Publish(ActivityRecord) (ActivityEvent, error)
}

type ActivityLog struct {
	capacity int
	now      func() time.Time
	hub      *realtime.Hub
	mu       sync.RWMutex
	events   []ActivityEvent
}

func NewActivityLog(capacity int, now func() time.Time, hub *realtime.Hub) (*ActivityLog, error) {
	if capacity <= 0 || now == nil || hub == nil {
		return nil, configErr("activity projection dependencies are incomplete")
	}
	return &ActivityLog{capacity: capacity, now: now, hub: hub}, nil
}

func (log *ActivityLog) Publish(record ActivityRecord) (ActivityEvent, error) {
	if strings.TrimSpace(record.PrincipalID) == "" || (record.Mode != "test" && record.Mode != "live") || strings.TrimSpace(record.Kind) == "" {
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
	realtimePayload := map[string]any{"productId": nullableString(record.ProductID), "accountId": nullableString(record.AccountID), "resourceId": nullableString(record.ResourceID), "payload": json.RawMessage(encoded)}
	log.mu.Lock()
	defer log.mu.Unlock()
	streamEvent, err := log.hub.PublishScoped(record.PrincipalID, record.Mode, record.Kind, realtimePayload)
	if err != nil {
		return ActivityEvent{}, err
	}
	event := ActivityEvent{EventID: streamEvent.EventID, Sequence: streamEvent.Sequence, Cursor: streamEvent.Cursor, Kind: streamEvent.Kind, ProductID: stringPointer(record.ProductID), AccountID: stringPointer(record.AccountID), ResourceID: stringPointer(record.ResourceID), Payload: encoded, ObservedAt: streamEvent.ObservedAt, principal: record.PrincipalID, mode: record.Mode}
	log.events = append(log.events, event)
	if len(log.events) > log.capacity {
		log.events = append([]ActivityEvent(nil), log.events[len(log.events)-log.capacity:]...)
	}
	return cloneActivityEvent(event), nil
}

func (log *ActivityLog) ListActivity(_ context.Context, principal, mode string, query ActivityQuery) ([]ActivityEvent, error) {
	if principal == "" || (mode != "test" && mode != "live") {
		return nil, fmt.Errorf("activity scope is required")
	}
	log.mu.RLock()
	defer log.mu.RUnlock()
	items := make([]ActivityEvent, 0, len(log.events))
	for index := len(log.events) - 1; index >= 0; index-- {
		event := log.events[index]
		if event.principal != principal || event.mode != mode || (query.Kind != "" && event.Kind != query.Kind) || (query.ProductID != "" && stringValuePointer(event.ProductID) != query.ProductID) || (query.AccountID != "" && stringValuePointer(event.AccountID) != query.AccountID) || (query.ResourceID != "" && stringValuePointer(event.ResourceID) != query.ResourceID) {
			continue
		}
		items = append(items, cloneActivityEvent(event))
	}
	return items, nil
}

type BrowserProjectionRoutesOptions struct {
	Runtimes  AccountRuntimeProjectionReader
	Commands  CommandProjectionReader
	Activity  ActivityProjectionReader
	Principal func(*http.Request) (principalID, mode string, err error)
}

func RegisterBrowserProjectionRoutes(mux *http.ServeMux, options BrowserProjectionRoutesOptions) error {
	if mux == nil || options.Runtimes == nil || options.Commands == nil || options.Activity == nil || options.Principal == nil {
		return configErr("browser projection route dependencies are incomplete")
	}
	handler := &browserProjectionHandler{options: options}
	mux.HandleFunc("GET /api/v1/accounts/{accountId}/runtimes", handler.accountRuntimes)
	mux.HandleFunc("GET /api/v1/commands", handler.commands)
	mux.HandleFunc("GET /api/v1/fills", handler.fills)
	mux.HandleFunc("GET /api/v1/activity", handler.activity)
	return nil
}

type browserProjectionHandler struct {
	options BrowserProjectionRoutesOptions
}

func (handler *browserProjectionHandler) accountRuntimes(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, nil, w) {
		return
	}
	items, err := handler.options.Runtimes.ListAccountRuntimes(r.Context(), principal, mode, r.PathValue("accountId"))
	if err != nil {
		projectionError(w, http.StatusServiceUnavailable, "RUNTIME_PROJECTION_UNAVAILABLE")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
}

func (handler *browserProjectionHandler) commands(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"productId", "accountId", "state"}, w) {
		return
	}
	items, err := handler.listCommands(r.Context(), principal, mode, r.URL.Query().Get("productId"))
	if err != nil {
		projectionError(w, http.StatusServiceUnavailable, "COMMAND_PROJECTION_UNAVAILABLE")
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
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": views}})
}

func (handler *browserProjectionHandler) fills(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"productId", "accountId", "instrumentId", "lineageId"}, w) {
		return
	}
	items, err := handler.listCommands(r.Context(), principal, mode, r.URL.Query().Get("productId"))
	if err != nil {
		projectionError(w, http.StatusServiceUnavailable, "FILL_PROJECTION_UNAVAILABLE")
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
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": result}})
}

func (handler *browserProjectionHandler) activity(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok || !validOptionalQuery(r, []string{"kind", "productId", "accountId", "resourceId"}, w) {
		return
	}
	items, err := handler.options.Activity.ListActivity(r.Context(), principal, mode, ActivityQuery{Kind: r.URL.Query().Get("kind"), ProductID: r.URL.Query().Get("productId"), AccountID: r.URL.Query().Get("accountId"), ResourceID: r.URL.Query().Get("resourceId")})
	if err != nil {
		projectionError(w, http.StatusServiceUnavailable, "ACTIVITY_PROJECTION_UNAVAILABLE")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
}

func (handler *browserProjectionHandler) listCommands(ctx context.Context, principal, mode, productID string) ([]orders.Command, error) {
	products := []string{productID}
	if productID == "" {
		products = []string{"manual"}
	}
	items := make([]orders.Command, 0)
	seen := make(map[string]struct{})
	for _, product := range products {
		projected, err := handler.options.Commands.ListOwned(ctx, principal, orders.Mode(mode), product)
		if err != nil {
			return nil, err
		}
		for _, item := range projected {
			if item.PrincipalID != principal || string(item.Mode) != mode || item.ProductID != product {
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

func (handler *browserProjectionHandler) identity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	principal, mode, err := handler.options.Principal(r)
	if err != nil || principal == "" || (mode != "test" && mode != "live") {
		projectionError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
		return "", "", false
	}
	return principal, mode, true
}

type CommandProjection struct {
	CommandID      string           `json:"commandId"`
	AccountID      string           `json:"accountId"`
	LineageID      string           `json:"lineageId"`
	ProductID      string           `json:"productId"`
	Mode           orders.Mode      `json:"mode"`
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
	return CommandProjection{CommandID: command.CommandID, AccountID: command.AccountID, LineageID: command.LineageID, ProductID: command.ProductID, Mode: command.Mode, ReservationID: command.ReservationID, IdempotencyKey: command.IdempotencyKey, CommandKind: command.CommandKind, State: command.State, Blockers: append([]orders.Blocker(nil), command.Blockers...), CreatedAt: command.CreatedAt, UpdatedAt: command.UpdatedAt}
}

func fillProjection(command orders.Command, fill orders.FillEvidence) FillProjection {
	return FillProjection{FillID: fill.FillID, CommandID: command.CommandID, LineageID: command.LineageID, ProductID: command.ProductID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID, Side: fill.Side, Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt}
}

func publishCommandActivity(publisher ActivityPublisher, command orders.Command) {
	if publisher == nil {
		return
	}
	_, _ = publisher.Publish(ActivityRecord{PrincipalID: command.PrincipalID, Mode: string(command.Mode), Kind: "command." + string(command.State), ProductID: command.ProductID, AccountID: command.AccountID, ResourceID: command.CommandID, Payload: commandProjection(command)})
	for _, fill := range command.ExecutionEvidence.Fills {
		_, _ = publisher.Publish(ActivityRecord{PrincipalID: command.PrincipalID, Mode: string(command.Mode), Kind: "fill.recorded", ProductID: command.ProductID, AccountID: fill.AccountID, ResourceID: fill.FillID, Payload: fillProjection(command, fill)})
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
			projectionError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return false
		}
	}
	return true
}

func projectionError(w http.ResponseWriter, status int, code string) {
	writeError(w, status, code, "request rejected", nil, false, "")
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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
