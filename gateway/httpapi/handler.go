package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/shopspring/decimal"
)

const (
	defaultListLimit    = 100
	maxListLimit        = 500
	defaultBodyLimit    = int64(64 << 10)
	websocketPingPeriod = 30 * time.Second
)

type AccountRegistry interface {
	Accounts() []account.Resource
	Status(domain.AccountID) (account.Status, error)
	Capability(domain.AccountID) (capability.Row, error)
	Instruments(domain.AccountID) (catalog.Snapshot, error)
	Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error)
	Spot(domain.AccountID) (exchange.SpotClient, error)
	Perp(domain.AccountID) (exchange.PerpClient, error)
}

type ReferenceService interface {
	Accounts() []account.Resource
	Capabilities(domain.AccountID) ([]reference.AccountCapability, error)
	Instruments(domain.AccountID) ([]catalog.Instrument, error)
	Rules(domain.AccountID) ([]reference.InstrumentRules, error)
	RiskLimits(domain.AccountID) reference.RiskLimitReference
}

type OrderService interface {
	Place(context.Context, order.PlaceRequest) (order.Result, error)
	Get(context.Context, domain.PrincipalID, domain.GatewayOrderID) (order.Result, error)
	List(context.Context, order.ListRequest) ([]order.Result, error)
	Cancel(context.Context, order.CancelRequest) (order.Result, error)
}

type Options struct {
	Monitor      *health.Monitor
	Accounts     AccountRegistry
	References   ReferenceService
	Orders       OrderService
	Events       *gatewayevent.Hub
	Auditor      store.AuditStore
	Identity     *domain.IdentityGenerator
	MaxBodyBytes int64
}

type Handler struct {
	monitor    *health.Monitor
	accounts   AccountRegistry
	references ReferenceService
	orders     OrderService
	events     *gatewayevent.Hub
	auditor    store.AuditStore
	identity   *domain.IdentityGenerator
	bodyLimit  int64
	mux        *http.ServeMux
}

type Route struct {
	Method  string
	Pattern string
}

var routes = []Route{
	{http.MethodGet, "/v1/health/live"},
	{http.MethodGet, "/v1/health/ready"},
	{http.MethodGet, "/v1/accounts"},
	{http.MethodGet, "/v1/accounts/{account_id}"},
	{http.MethodGet, "/v1/capabilities"},
	{http.MethodGet, "/v1/instruments"},
	{http.MethodGet, "/v1/instruments/{instrument_id}"},
	{http.MethodGet, "/v1/rules"},
	{http.MethodGet, "/v1/risk-limits"},
	{http.MethodPost, "/v1/orders"},
	{http.MethodGet, "/v1/orders"},
	{http.MethodGet, "/v1/orders/{gateway_order_id}"},
	{http.MethodPatch, "/v1/orders/{gateway_order_id}"},
	{http.MethodDelete, "/v1/orders/{gateway_order_id}"},
	{http.MethodGet, "/v1/open-orders"},
	{http.MethodGet, "/v1/fills"},
	{http.MethodGet, "/v1/balances"},
	{http.MethodGet, "/v1/positions"},
	{http.MethodGet, "/v1/funding-rates"},
	{http.MethodPut, "/v1/accounts/{account_id}/instruments/{instrument_id}/leverage"},
	{http.MethodGet, "/v1/ws"},
}

func Routes() []Route {
	return append([]Route(nil), routes...)
}

func NewHandler(options Options) (*Handler, error) {
	if options.Monitor == nil || options.Accounts == nil || options.References == nil ||
		options.Orders == nil || options.Events == nil || options.Auditor == nil || options.Identity == nil {
		return nil, fmt.Errorf("HTTP API dependencies are incomplete")
	}
	bodyLimit := options.MaxBodyBytes
	if bodyLimit <= 0 {
		bodyLimit = defaultBodyLimit
	}
	handler := &Handler{
		monitor: options.Monitor, accounts: options.Accounts, references: options.References,
		orders: options.Orders, events: options.Events, auditor: options.Auditor,
		identity: options.Identity, bodyLimit: bodyLimit,
		mux: http.NewServeMux(),
	}
	handler.register()
	return handler, nil
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, ok := auth.Principal(r.Context()); !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", "", false)
		return
	}
	handler.mux.ServeHTTP(w, r)
}

func (handler *Handler) register() {
	handler.mux.HandleFunc("GET /v1/health/live", handler.live)
	handler.mux.HandleFunc("GET /v1/health/ready", handler.ready)
	handler.mux.HandleFunc("GET /v1/accounts", handler.listAccounts)
	handler.mux.HandleFunc("GET /v1/accounts/{account_id}", handler.getAccount)
	handler.mux.HandleFunc("GET /v1/capabilities", handler.listCapabilities)
	handler.mux.HandleFunc("GET /v1/instruments", handler.listInstruments)
	handler.mux.HandleFunc("GET /v1/instruments/{instrument_id}", handler.getInstrument)
	handler.mux.HandleFunc("GET /v1/rules", handler.listRules)
	handler.mux.HandleFunc("GET /v1/risk-limits", handler.listRiskLimits)
	handler.mux.HandleFunc("POST /v1/orders", handler.placeOrder)
	handler.mux.HandleFunc("GET /v1/orders", handler.listOrders(false))
	handler.mux.HandleFunc("GET /v1/orders/{gateway_order_id}", handler.getOrder)
	handler.mux.HandleFunc("PATCH /v1/orders/{gateway_order_id}", handler.amendOrder)
	handler.mux.HandleFunc("DELETE /v1/orders/{gateway_order_id}", handler.cancelOrder)
	handler.mux.HandleFunc("GET /v1/open-orders", handler.listOrders(true))
	handler.mux.HandleFunc("GET /v1/fills", handler.listFills)
	handler.mux.HandleFunc("GET /v1/balances", handler.listBalances)
	handler.mux.HandleFunc("GET /v1/positions", handler.listPositions)
	handler.mux.HandleFunc("GET /v1/funding-rates", handler.listFundingRates)
	handler.mux.HandleFunc("PUT /v1/accounts/{account_id}/instruments/{instrument_id}/leverage", handler.setLeverage)
	handler.mux.HandleFunc("GET /v1/ws", handler.privateEvents)

	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if _, exists := seen[route.Pattern]; exists {
			continue
		}
		seen[route.Pattern] = struct{}{}
		handler.mux.HandleFunc(route.Pattern, handler.methodNotAllowed)
	}
	handler.mux.HandleFunc("/", handler.notFound)
}

func (handler *Handler) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, handler.monitor.Liveness())
}

func (handler *Handler) ready(w http.ResponseWriter, _ *http.Request) {
	readiness := handler.monitor.Readiness()
	status := http.StatusOK
	if !readiness.Ready {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, readiness)
}

func (handler *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	limit, err := listLimit(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	items := handler.references.Accounts()
	if len(items) > limit {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, collection[account.Resource]{Items: nonNil(items)})
}

func (handler *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	accountID, err := parseAccountID(r.PathValue("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	status, err := handler.accounts.Status(accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (handler *Handler) listCapabilities(w http.ResponseWriter, r *http.Request) {
	accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	items, err := handler.references.Capabilities(accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, collection[reference.AccountCapability]{Items: nonNil(items)})
}

func (handler *Handler) listInstruments(w http.ResponseWriter, r *http.Request) {
	accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	limit, err := listLimit(r)
	if err != nil {
		handler.invalid(w)
		return
	}
	items, err := handler.references.Instruments(accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	venue := exchange.Venue(r.URL.Query().Get("venue"))
	product := exchange.Product(r.URL.Query().Get("product"))
	filtered := make([]catalog.Instrument, 0, min(len(items), limit))
	for _, item := range items {
		if venue != "" && item.Venue != venue {
			continue
		}
		if product != "" && item.Product != product {
			continue
		}
		filtered = append(filtered, item)
		if len(filtered) == limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, collection[catalog.Instrument]{Items: filtered})
}

func (handler *Handler) getInstrument(w http.ResponseWriter, r *http.Request) {
	instrumentID, err := parseInstrumentID(r.PathValue("instrument_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	items, err := handler.references.Instruments("")
	if err != nil {
		handler.fail(w, err)
		return
	}
	for _, item := range items {
		if item.ID == instrumentID {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	handler.fail(w, store.ErrNotFound)
}

func (handler *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	items, err := handler.references.Rules(accountID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, collection[reference.InstrumentRules]{Items: nonNil(items)})
}

func (handler *Handler) listRiskLimits(w http.ResponseWriter, r *http.Request) {
	accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	writeJSON(w, http.StatusOK, handler.references.RiskLimits(accountID))
}

type placeOrderBody struct {
	AccountID      string `json:"account_id"`
	InstrumentID   string `json:"instrument_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Side           string `json:"side"`
	Type           string `json:"type"`
	Quantity       string `json:"quantity"`
	LimitPrice     string `json:"limit_price"`
	LimitPolicy    string `json:"limit_policy"`
	ReduceOnly     bool   `json:"reduce_only"`
}

func (handler *Handler) placeOrder(w http.ResponseWriter, r *http.Request) {
	var body placeOrderBody
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &body); err != nil {
		handler.invalid(w)
		return
	}
	headerKey := r.Header.Get("Idempotency-Key")
	if headerKey == "" || headerKey != body.IdempotencyKey {
		handler.invalid(w)
		return
	}
	principal, _ := auth.Principal(r.Context())
	correlationID, err := handler.nextCorrelationID()
	if err != nil {
		handler.fail(w, err)
		return
	}
	request, err := body.placeRequest(principal, correlationID)
	if err != nil {
		handler.invalid(w)
		return
	}
	result, err := handler.orders.Place(r.Context(), request)
	if err != nil {
		switch {
		case errors.Is(err, order.ErrOutcomeUncertain):
		case errors.Is(err, order.ErrIdempotencyConflict):
			writeJSON(w, http.StatusConflict, struct {
				Error             errorBody        `json:"error"`
				ExistingCommandID domain.CommandID `json:"existing_command_id"`
			}{
				Error: errorBody{
					Code: "idempotency_conflict", Message: safeMessage("idempotency_conflict"),
					CorrelationID: string(correlationID), Retryable: false,
				},
				ExistingCommandID: result.CommandID,
			})
			return
		default:
			handler.failWithCorrelation(w, err, correlationID)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (body placeOrderBody) placeRequest(principal domain.PrincipalID, correlationID domain.EventID) (order.PlaceRequest, error) {
	accountID, err := parseAccountID(body.AccountID)
	if err != nil {
		return order.PlaceRequest{}, err
	}
	instrumentID, err := parseInstrumentID(body.InstrumentID)
	if err != nil {
		return order.PlaceRequest{}, err
	}
	key := domain.IdempotencyKey(body.IdempotencyKey)
	if err := key.Validate(); err != nil {
		return order.PlaceRequest{}, err
	}
	quantity, err := parsePositiveDecimal(body.Quantity, "quantity")
	if err != nil {
		return order.PlaceRequest{}, err
	}
	var limitPrice decimal.Decimal
	if body.LimitPrice != "" {
		limitPrice, err = parsePositiveDecimal(body.LimitPrice, "limit_price")
		if err != nil {
			return order.PlaceRequest{}, err
		}
	}
	side := exchange.Side(body.Side)
	if side != exchange.SideBuy && side != exchange.SideSell {
		return order.PlaceRequest{}, fmt.Errorf("invalid side")
	}
	orderType := exchange.OrderType(body.Type)
	if orderType != exchange.OrderTypeMarket && orderType != exchange.OrderTypeLimit {
		return order.PlaceRequest{}, fmt.Errorf("invalid order type")
	}
	policy := exchange.LimitPolicy(body.LimitPolicy)
	if orderType == exchange.OrderTypeMarket && (body.LimitPrice != "" || policy != "") {
		return order.PlaceRequest{}, fmt.Errorf("market order has limit fields")
	}
	if orderType == exchange.OrderTypeLimit {
		if !limitPrice.IsPositive() {
			return order.PlaceRequest{}, fmt.Errorf("limit price is required")
		}
		switch policy {
		case exchange.LimitPolicyResting, exchange.LimitPolicyIOC, exchange.LimitPolicyPostOnly:
		default:
			return order.PlaceRequest{}, fmt.Errorf("invalid limit policy")
		}
	}
	return order.PlaceRequest{
		PrincipalID: principal, AccountID: accountID, InstrumentID: instrumentID,
		IdempotencyKey: key, CorrelationID: correlationID, Side: side, Type: orderType, Quantity: quantity,
		LimitPrice: limitPrice, LimitPolicy: policy, ReduceOnly: body.ReduceOnly,
	}, nil
}

func (handler *Handler) listOrders(openOnly bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, _ := auth.Principal(r.Context())
		accountID, err := optionalAccountID(r.URL.Query().Get("account_id"))
		if err != nil {
			handler.invalid(w)
			return
		}
		instrumentID, err := optionalInstrumentID(r.URL.Query().Get("instrument_id"))
		if err != nil {
			handler.invalid(w)
			return
		}
		limit, err := listLimit(r)
		if err != nil {
			handler.invalid(w)
			return
		}
		cursor, err := optionalGatewayOrderID(r.URL.Query().Get("pagination.cursor"))
		if err != nil {
			handler.invalid(w)
			return
		}
		items, err := handler.orders.List(r.Context(), order.ListRequest{
			PrincipalID: principal, AccountID: accountID, InstrumentID: instrumentID,
			OpenOnly: openOnly, Limit: limit, Cursor: cursor,
		})
		if err != nil {
			handler.fail(w, err)
			return
		}
		response := collection[order.Result]{Items: nonNil(items)}
		if len(items) == limit {
			response.NextCursor = string(items[len(items)-1].GatewayOrderID)
		}
		writeJSON(w, http.StatusOK, response)
	}
}

func (handler *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.Principal(r.Context())
	gatewayOrderID, err := parseGatewayOrderID(r.PathValue("gateway_order_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	result, err := handler.orders.Get(r.Context(), principal, gatewayOrderID)
	if err != nil {
		handler.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (handler *Handler) amendOrder(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.Principal(r.Context())
	gatewayOrderID, err := parseGatewayOrderID(r.PathValue("gateway_order_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	correlationID, err := handler.nextCorrelationID()
	if err != nil {
		handler.fail(w, err)
		return
	}
	if err := handler.auditor.AppendAudit(r.Context(), store.AuditRecord{
		PrincipalID: principal, Action: store.AuditAmendOrder,
		ResourceID: string(gatewayOrderID), Outcome: store.AuditUnsupported,
		CorrelationID: correlationID,
	}); err != nil {
		handler.failWithCorrelation(w, err, correlationID)
		return
	}
	writeError(w, http.StatusNotImplemented, "unsupported_capability", "order amendment is not supported", string(correlationID), false)
}

func (handler *Handler) cancelOrder(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.Principal(r.Context())
	gatewayOrderID, err := parseGatewayOrderID(r.PathValue("gateway_order_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	correlationID, err := handler.nextCorrelationID()
	if err != nil {
		handler.fail(w, err)
		return
	}
	if err := handler.auditor.AppendAudit(r.Context(), store.AuditRecord{
		PrincipalID: principal, Action: store.AuditCancelOrder,
		ResourceID: string(gatewayOrderID), Outcome: store.AuditRequested,
		CorrelationID: correlationID,
	}); err != nil {
		handler.failWithCorrelation(w, err, correlationID)
		return
	}
	result, err := handler.orders.Cancel(r.Context(), order.CancelRequest{
		PrincipalID: principal, GatewayOrderID: gatewayOrderID,
	})
	if err != nil && !errors.Is(err, order.ErrOutcomeUncertain) {
		handler.failWithCorrelation(w, err, correlationID)
		return
	}
	writeJSON(w, http.StatusAccepted, result)
}

func (handler *Handler) privateEvents(w http.ResponseWriter, r *http.Request) {
	principal, ok := auth.Principal(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "unauthorized", handler.correlationID(), false)
		return
	}
	accountID, err := parseAccountID(r.URL.Query().Get("account_id"))
	if err != nil {
		handler.invalid(w)
		return
	}
	if _, err := handler.accounts.Status(accountID); err != nil {
		handler.fail(w, err)
		return
	}
	kinds, err := parseEventKinds(r.URL.Query()["kind"])
	if err != nil {
		handler.invalid(w)
		return
	}
	subscription, err := handler.events.Subscribe(r.Context(), gatewayevent.SubscribeRequest{
		PrincipalID: principal,
		AccountID:   accountID,
		Cursor:      r.URL.Query().Get("cursor"),
		Kinds:       kinds,
	})
	if err != nil {
		handler.invalid(w)
		return
	}
	defer subscription.Close()

	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer connection.CloseNow()
	streamContext := connection.CloseRead(r.Context())
	ping := time.NewTicker(websocketPingPeriod)
	defer ping.Stop()

	for {
		select {
		case <-streamContext.Done():
			return
		case <-ping.C:
			pingContext, cancel := context.WithTimeout(streamContext, 10*time.Second)
			err := connection.Ping(pingContext)
			cancel()
			if err != nil {
				return
			}
		case event, open := <-subscription.Events():
			if !open {
				select {
				case streamErr := <-subscription.Errors():
					closePrivateEventStream(connection, streamErr)
				default:
					closePrivateEventStream(connection, nil)
				}
				return
			}
			if err := wsjson.Write(streamContext, connection, event); err != nil {
				return
			}
		}
	}
}

func closePrivateEventStream(connection *websocket.Conn, streamErr error) {
	switch {
	case streamErr == nil, errors.Is(streamErr, gatewayevent.ErrClosed):
		_ = connection.Close(websocket.StatusNormalClosure, "event_stream_complete")
	case errors.Is(streamErr, gatewayevent.ErrSlowConsumer):
		_ = connection.Close(websocket.StatusPolicyViolation, "slow_consumer_resync_required")
	default:
		_ = connection.Close(websocket.StatusInternalError, "event_stream_unavailable")
	}
}

func parseEventKinds(values []string) ([]gatewayevent.Kind, error) {
	if len(values) == 0 {
		return nil, nil
	}
	kinds := make([]gatewayevent.Kind, 0, len(values))
	seen := make(map[gatewayevent.Kind]struct{}, len(values))
	for _, value := range values {
		kind := gatewayevent.Kind(value)
		switch kind {
		case gatewayevent.KindOrder, gatewayevent.KindFill, gatewayevent.KindBalance,
			gatewayevent.KindPosition, gatewayevent.KindAccountHealth:
		default:
			return nil, fmt.Errorf("private event kind is invalid")
		}
		if _, exists := seen[kind]; exists {
			continue
		}
		seen[kind] = struct{}{}
		kinds = append(kinds, kind)
	}
	return kinds, nil
}

func (handler *Handler) methodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method is not allowed", handler.correlationID(), false)
}

func (handler *Handler) notFound(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found", safeMessage("not_found"), handler.correlationID(), false)
}

func (handler *Handler) invalid(w http.ResponseWriter) {
	writeError(w, http.StatusBadRequest, "invalid_request", "request is invalid", handler.correlationID(), false)
}

func (handler *Handler) fail(w http.ResponseWriter, err error) {
	correlationID, _ := handler.nextCorrelationID()
	handler.failWithCorrelation(w, err, correlationID)
}

func (handler *Handler) failWithCorrelation(w http.ResponseWriter, err error, correlationID domain.EventID) {
	status, code, retryable := classifyError(err)
	writeError(w, status, code, safeMessage(code), string(correlationID), retryable)
}

func (handler *Handler) correlationID() string {
	id, err := handler.nextCorrelationID()
	if err != nil {
		return ""
	}
	return string(id)
}

func (handler *Handler) nextCorrelationID() (domain.EventID, error) {
	return handler.identity.EventID()
}

func classifyError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, account.ErrUnknownAccount), errors.Is(err, exchange.ErrNotFound):
		return http.StatusNotFound, "not_found", false
	case errors.Is(err, order.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", false
	case errors.Is(err, order.ErrWriteNotReady), errors.Is(err, order.ErrRecoveryIncomplete),
		errors.Is(err, order.ErrCoordinatorClosed), errors.Is(err, account.ErrManagerClosed):
		return http.StatusServiceUnavailable, "temporarily_unavailable", true
	case errors.Is(err, exchange.ErrAuthentication):
		return http.StatusBadGateway, "venue_authentication_failed", false
	case errors.Is(err, exchange.ErrPermission), errors.Is(err, exchange.ErrVenueRejected):
		return http.StatusUnprocessableEntity, "venue_rejected", false
	case errors.Is(err, exchange.ErrRateLimit):
		return http.StatusTooManyRequests, "rate_limited", true
	case errors.Is(err, exchange.ErrInvalidRequest), errors.Is(err, account.ErrProductMismatch):
		return http.StatusBadRequest, "invalid_request", false
	case errors.Is(err, exchange.ErrUnsupported):
		return http.StatusNotImplemented, "unsupported_capability", false
	case errors.Is(err, context.Canceled):
		return 499, "request_canceled", false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, exchange.ErrTransport):
		return http.StatusBadGateway, "venue_unavailable", true
	case errors.Is(err, exchange.ErrAmbiguousOutcome):
		return http.StatusBadGateway, "outcome_uncertain", false
	default:
		return http.StatusInternalServerError, "internal_error", false
	}
}

func safeMessage(code string) string {
	switch code {
	case "not_found":
		return "resource was not found"
	case "idempotency_conflict":
		return "idempotency key conflicts with an existing request"
	case "temporarily_unavailable":
		return "service is temporarily unavailable"
	case "venue_authentication_failed":
		return "venue authentication failed"
	case "venue_rejected":
		return "venue rejected the request"
	case "rate_limited":
		return "request was rate limited"
	case "invalid_request":
		return "request is invalid"
	case "unsupported_capability":
		return "capability is not supported"
	case "request_canceled":
		return "request was canceled"
	case "venue_unavailable":
		return "venue is unavailable"
	case "outcome_uncertain":
		return "operation outcome is uncertain"
	default:
		return "internal error"
	}
}

func listLimit(r *http.Request) (int, error) {
	value := r.URL.Query().Get("pagination.limit")
	if value == "" {
		return defaultListLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maxListLimit {
		return 0, fmt.Errorf("pagination limit out of range")
	}
	return limit, nil
}

func parseAccountID(value string) (domain.AccountID, error) {
	id := domain.AccountID(value)
	return id, id.Validate()
}

func optionalAccountID(value string) (domain.AccountID, error) {
	if value == "" {
		return "", nil
	}
	return parseAccountID(value)
}

func parseInstrumentID(value string) (domain.InstrumentID, error) {
	id := domain.InstrumentID(value)
	return id, id.Validate()
}

func optionalInstrumentID(value string) (domain.InstrumentID, error) {
	if value == "" {
		return "", nil
	}
	return parseInstrumentID(value)
}

func parseGatewayOrderID(value string) (domain.GatewayOrderID, error) {
	id := domain.GatewayOrderID(value)
	return id, id.Validate()
}

func optionalGatewayOrderID(value string) (domain.GatewayOrderID, error) {
	if value == "" {
		return "", nil
	}
	return parseGatewayOrderID(value)
}

func parsePositiveDecimal(value string, name string) (decimal.Decimal, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return decimal.Decimal{}, fmt.Errorf("%s is required", name)
	}
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return decimal.Decimal{}, fmt.Errorf("%s is invalid", name)
	}
	return parsed, nil
}

type collection[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

func nonNil[T any](items []T) []T {
	if items == nil {
		return []T{}
	}
	return items
}
