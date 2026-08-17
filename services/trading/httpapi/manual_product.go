package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
	"github.com/shopspring/decimal"
)

type manualProductHandler struct {
	trading *tradingHandler
}

type manualPreviewRequest struct {
	AccountID, InstrumentID, Side, OrderType, Quantity, LimitPrice, TimeInForce, Mode string
	ReduceOnly                                                                        bool
}

type manualPreview struct {
	PreviewID string `json:"previewId"`
	manualPreviewRequest
	PrincipalID string   `json:"-"`
	Blockers    []string `json:"blockers"`
	Warnings    []string `json:"warnings"`
}

type manualCommandRequest struct {
	PreviewID, Action, LineageID, OrderID, IdempotencyKey string
}

type manualPreviewAuthority struct {
	PrincipalID      string               `json:"principalId"`
	Mode             string               `json:"mode"`
	Request          manualPreviewRequest `json:"request"`
	AccountFence     manualAccountFence   `json:"accountFence"`
	CatalogRevision  string               `json:"catalogRevision"`
	Venue            string               `json:"venue"`
	VenueEnvironment string               `json:"venueEnvironment"`
	Product          string               `json:"product"`
	// RouteID is the account route the preview was priced against. It is
	// sealed here for the same reason the three fences are: the Gateway
	// refuses a placement whose route does not match the account's current
	// one, so an order must carry the route it was authorized under rather
	// than whichever route the account happens to hold at submission.
	RouteID   string    `json:"routeId"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type manualAccountFence struct {
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

// ManualAccount is the slice of an account the manual product must see to
// decide whether an order may be placed against it. The gateway owns the
// account record; this is the read-only projection carried across the wire, so
// it holds no credential reference.
//
// The three fence fields travel together with the identity on purpose. A
// preview is authorized against the account as it stood when the preview was
// sealed, and the order is refused if any of them moved in between — a rotated
// credential or a regenerated runtime must not inherit a prior authorization.
type ManualAccount struct {
	ID                   string
	Venue                string
	VenueEnvironment     string
	State                string
	ProductIDs           []string
	Routes               []ManualAccountRoute
	DesiredStateRevision int64
	RuntimeGeneration    int64
	CredentialVersion    int64
}

// ManualAccountRoute is one of the account's declared trading routes. The
// gateway refuses an order that does not name the route it is trading through,
// so the manual product has to resolve one before it can place anything: the
// instrument's product picks the route, and the route's identifier is sealed
// into the preview.
type ManualAccountRoute struct {
	RouteID         string
	Product         string
	SettlementAsset string
	RequestedState  string
}

// The states the manual product tests for. They match the gateway lifecycle
// vocabulary; anything else reads as not-ready.
const (
	manualAccountActive     = "active"
	manualProductIdentifier = "manual"
)

// ManualAccountAuthority answers for an account on behalf of a named principal.
// The principal travels with the lookup because the authority is the gateway,
// whose account plane refuses a read that cannot say who is asking; a service
// token alone establishes the caller, not the user behind it.
type ManualAccountAuthority interface {
	Get(ctx context.Context, principal, accountID string) (ManualAccount, error)
}

type manualPreviewCodec interface {
	SealPreview(principal string, mode orders.Mode, payload []byte) (string, error)
	OpenPreview(principal string, mode orders.Mode, token string) ([]byte, error)
	PreviewNow() time.Time
}

const manualPreviewTTL = 30 * time.Second
const manualTestMaxNotional int64 = 100

func registerManualProductRoutes(mux *http.ServeMux, trading *tradingHandler) {
	handler := &manualProductHandler{trading: trading}
	mux.HandleFunc("GET /api/v1/products/manual/venues", handler.venues)
	mux.HandleFunc("POST /api/v1/products/manual/execution-previews", handler.createPreview)
	mux.HandleFunc("POST /api/v1/products/manual/commands", handler.createCommand)
	mux.HandleFunc("GET /api/v1/products/manual/orders", handler.listOrders)
	mux.HandleFunc("GET /api/v1/products/manual/orders/{orderId}", handler.getOrder)
}

func (handler *manualProductHandler) venues(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := handler.trading.identity(w, r); !ok {
		return
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"revision": snapshot.Revision, "items": snapshot.Venues}})
}

func (handler *manualProductHandler) createPreview(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request manualPreviewRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.Mode != mode || validateManualPreview(request) != nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	if handler.trading.options.ManualAccounts == nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	account, err := handler.trading.options.ManualAccounts.Get(r.Context(), principal, request.AccountID)
	if err != nil || account.ID != request.AccountID || !manualAccountReady(account) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	authority, ok := manualCatalogAuthority(snapshot, account, request.InstrumentID)
	if !ok {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if mode == string(orders.ModeTest) {
		allowed, err := manualTestNotionalAllowed(r.Context(), request, authority, handler.trading.options.ManualMarketData)
		if err != nil {
			tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
			return
		}
		if !allowed {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
	}
	codec, ok := handler.trading.options.Manual.(manualPreviewCodec)
	if !ok {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	now := codec.PreviewNow()
	if now.IsZero() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	authority.PrincipalID = principal
	authority.Mode = mode
	authority.Request = request
	authority.AccountFence = manualAccountFence{
		AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
	}
	authority.CatalogRevision = snapshot.Revision
	authority.IssuedAt = now
	authority.ExpiresAt = now.Add(manualPreviewTTL)
	canonical, _ := json.Marshal(authority)
	previewID, err := codec.SealPreview(principal, orders.Mode(mode), canonical)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	preview := manualPreview{PreviewID: previewID, manualPreviewRequest: request, PrincipalID: principal, Blockers: []string{}, Warnings: []string{}}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"meta": httpx.Meta(r), "data": preview})
}

func validateManualPreview(request manualPreviewRequest) error {
	quantity, quantityErr := decimal.NewFromString(request.Quantity)
	if request.AccountID == "" || request.InstrumentID == "" || (request.Side != "buy" && request.Side != "sell") || quantityErr != nil || !quantity.IsPositive() || (request.Mode != "test" && request.Mode != "live") {
		return orders.ErrNotFound
	}
	switch strings.ToLower(request.OrderType) {
	case "market":
		if request.LimitPrice != "" {
			return orders.ErrNotFound
		}
	case "limit":
		price, err := decimal.NewFromString(request.LimitPrice)
		if err != nil || !price.IsPositive() {
			return orders.ErrNotFound
		}
	default:
		return orders.ErrNotFound
	}
	return nil
}

func (handler *manualProductHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request manualCommandRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.LineageID == "" || request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(r.Header.Get("Idempotency-Key")) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	switch request.Action {
	case "cancel":
		if request.PreviewID != "" || request.OrderID == "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.cancelCommand(w, r, principal, mode, request)
		return
	case "close":
		if request.PreviewID != "" || request.OrderID != "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.closePosition(w, r, principal, mode, request)
		return
	case "place":
		if request.PreviewID == "" || request.OrderID != "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
	default:
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	codec, ok := handler.trading.options.Manual.(manualPreviewCodec)
	if !ok {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	payload, err := codec.OpenPreview(principal, orders.Mode(mode), request.PreviewID)
	if err != nil {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	var authority manualPreviewAuthority
	now := codec.PreviewNow()
	if json.Unmarshal(payload, &authority) != nil || authority.PrincipalID != principal || authority.Mode != mode || authority.Request.Mode != mode ||
		validateManualPreview(authority.Request) != nil || authority.Venue == "" || authority.VenueEnvironment == "" || authority.CatalogRevision == "" ||
		authority.IssuedAt.IsZero() || authority.ExpiresAt.IsZero() || authority.ExpiresAt.Sub(authority.IssuedAt) != manualPreviewTTL || now.Before(authority.IssuedAt) || !now.Before(authority.ExpiresAt) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if handler.trading.options.ManualAccounts == nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	account, err := handler.trading.options.ManualAccounts.Get(r.Context(), principal, authority.Request.AccountID)
	if err != nil || authority.Request.AccountID != authority.AccountFence.AccountID || !manualAccountMatches(account, authority.AccountFence) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	currentRoute, routeReady := manualCatalogAuthority(snapshot, account, authority.Request.InstrumentID)
	if !routeReady || snapshot.Revision != authority.CatalogRevision || currentRoute.Venue != authority.Venue || currentRoute.VenueEnvironment != authority.VenueEnvironment || currentRoute.Product != authority.Product || currentRoute.RouteID != authority.RouteID {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if mode == string(orders.ModeTest) {
		allowed, err := manualTestNotionalAllowed(r.Context(), authority.Request, currentRoute, handler.trading.options.ManualMarketData)
		if err != nil {
			tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
			return
		}
		if !allowed {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
	}
	preview := manualPreview{PreviewID: request.PreviewID, manualPreviewRequest: authority.Request, PrincipalID: principal}
	handler.submitPlace(w, r, principal, mode, request, preview, authority)
}

func (handler *manualProductHandler) cancelCommand(w http.ResponseWriter, r *http.Request, principal, mode string, request manualCommandRequest) {
	if mode == string(orders.ModeLive) {
		tradingError(w, http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED")
		return
	}
	if !handler.trading.options.Manual.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	command, err := handler.trading.options.Commands.GetOwned(r.Context(), principal, orders.Mode(mode), request.OrderID)
	if err != nil || command.ProductID != "manual" || command.LineageID != request.LineageID {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	status := http.StatusOK
	if resumableManualState(command.State) {
		command, err = handler.trading.options.Manual.Cancel(r.Context(), command)
		if err != nil {
			tradingFailure(w, r, "manual.cancel", err)
			return
		}
		publishCommandActivityAs(handler.trading.options.Activity, command, "command.cancel_accepted")
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": command})
}

func (handler *manualProductHandler) closePosition(w http.ResponseWriter, r *http.Request, principal, mode string, request manualCommandRequest) {
	if mode == string(orders.ModeLive) {
		tradingError(w, http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED")
		return
	}
	commands, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, orders.Mode(mode), "manual")
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	for _, command := range commands {
		if command.IdempotencyKey != request.IdempotencyKey {
			continue
		}
		var intent struct {
			OrderType  string `json:"orderType"`
			ReduceOnly bool   `json:"reduceOnly"`
		}
		if command.LineageID != request.LineageID || json.Unmarshal(command.Payload, &intent) != nil || intent.OrderType != "market" || !intent.ReduceOnly {
			tradingError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		if resumableManualState(command.State) {
			command, err = handler.trading.options.Manual.Execute(r.Context(), command)
			if err != nil {
				tradingFailure(w, r, "manual.close", err)
				return
			}
		} else if command.State == orders.StateFailed || command.State == orders.StateCanceled {
			tradingError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": command})
		return
	}
	positions, err := handler.trading.options.Portfolio.ListContext(r.Context(), principal, portfolio.Mode(mode), "", "manual")
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	var position portfolio.Position
	matches := 0
	for _, item := range positions {
		quantity, quantityErr := decimal.NewFromString(item.Quantity)
		if item.LineageID == request.LineageID && quantityErr == nil && !quantity.IsZero() {
			position = item
			matches++
		}
	}
	quantity, quantityErr := decimal.NewFromString(position.Quantity)
	if matches != 1 || quantityErr != nil || quantity.IsZero() || !manualPositionOwned(commands, position) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if handler.trading.options.ManualAccounts == nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	account, err := handler.trading.options.ManualAccounts.Get(r.Context(), principal, position.AccountID)
	if err != nil || !manualAccountReady(account) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	snapshot, err := handler.trading.options.Catalog.Snapshot(r.Context())
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	authority, routeReady := manualCatalogAuthority(snapshot, account, position.InstrumentID)
	if !routeReady {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	side := "sell"
	if quantity.IsNegative() {
		side = "buy"
	}
	closeRequest := manualPreviewRequest{
		AccountID: position.AccountID, InstrumentID: position.InstrumentID, Side: side,
		OrderType: "market", Quantity: quantity.Abs().String(), ReduceOnly: true, Mode: mode,
	}
	if mode == string(orders.ModeTest) {
		allowed, err := manualTestNotionalAllowed(r.Context(), closeRequest, authority, handler.trading.options.ManualMarketData)
		if err != nil {
			tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
			return
		}
		if !allowed {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
	}
	authority.AccountFence = manualAccountFence{
		AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
	}
	preview := manualPreview{manualPreviewRequest: closeRequest, PrincipalID: principal}
	handler.submitPlace(w, r, principal, mode, request, preview, authority)
}

func manualPositionOwned(commands []orders.Command, position portfolio.Position) bool {
	for _, command := range commands {
		var intent struct {
			InstrumentID string `json:"instrumentId"`
			ReduceOnly   bool   `json:"reduceOnly"`
		}
		if command.LineageID == position.LineageID && command.AccountID == position.AccountID &&
			json.Unmarshal(command.Payload, &intent) == nil && intent.InstrumentID == position.InstrumentID && !intent.ReduceOnly {
			return true
		}
	}
	return false
}

func (handler *manualProductHandler) submitPlace(w http.ResponseWriter, r *http.Request, principal, mode string, request manualCommandRequest, preview manualPreview, authority manualPreviewAuthority) {
	if mode == string(orders.ModeLive) {
		tradingError(w, http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED")
		return
	}
	if !handler.trading.options.Manual.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	intentPayload, _ := json.Marshal(map[string]any{
		"instrumentId": preview.InstrumentID, "side": preview.Side, "quantity": preview.Quantity,
		"orderType": strings.ToLower(preview.OrderType), "limitPrice": preview.LimitPrice,
		"timeInForce": preview.TimeInForce, "reduceOnly": preview.ReduceOnly,
		"routeId":              authority.RouteID,
		"desiredStateRevision": authority.AccountFence.DesiredStateRevision,
		"runtimeGeneration":    authority.AccountFence.RuntimeGeneration,
		"credentialVersion":    authority.AccountFence.CredentialVersion,
	})
	kind := reservations.KindOpen
	if preview.ReduceOnly {
		kind = reservations.KindClose
	}
	reservation, err := handler.trading.options.Reservations.Create(r.Context(), reservations.CreateRequest{
		PrincipalID: principal, Mode: reservations.Mode(mode), ProductID: "manual", Kind: kind,
		Scope: reservations.NettingScope{
			AccountID: preview.AccountID, Venue: authority.Venue, InstrumentID: preview.InstrumentID, VenueEnvironment: authority.VenueEnvironment,
		},
		Quantity: preview.Quantity, IdempotencyKey: request.IdempotencyKey + ":reservation",
	})
	if err != nil {
		tradingFailure(w, r, "manual.place.reserve", err)
		return
	}
	command, replay, err := handler.trading.options.Commands.Create(r.Context(), orders.CreateRequest{
		PrincipalID: principal, AccountID: preview.AccountID, ProductID: "manual", LineageID: request.LineageID,
		ReservationID: reservation.ReservationID, IdempotencyKey: request.IdempotencyKey, Method: r.Method, Path: r.URL.Path,
		CommandKind: "manual.place", Mode: orders.Mode(mode), Payload: intentPayload,
	})
	if err != nil {
		tradingFailure(w, r, "manual.place.command", err)
		return
	}
	publish := !replay || resumableManualState(command.State)
	if publish {
		command, err = handler.trading.options.Manual.Execute(r.Context(), command)
		if err != nil {
			tradingFailure(w, r, "manual.place.execute", err)
			return
		}
	}
	if publish {
		publishCommandActivity(handler.trading.options.Activity, command)
	}
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": command})
}

func manualCatalogAuthority(snapshot catalog.Snapshot, account ManualAccount, instrumentID string) (manualPreviewAuthority, bool) {
	venueID := ""
	product := ""
	settlement := ""
	for _, instrument := range snapshot.Instruments {
		if instrument.InstrumentID == instrumentID && instrument.Ready {
			venueID = instrument.Venue
			product = instrument.Product
			settlement = instrument.Settlement
			break
		}
	}
	routeID, routed := manualAccountRoute(account, product, settlement)
	if !routed {
		return manualPreviewAuthority{}, false
	}
	for _, venue := range snapshot.Venues {
		// The environment clause is now only the agreement between two records
		// written by different systems — the catalog's view of the venue and the
		// account's. It used to be cross-checked against the mode as well, so a
		// test-mode request could resolve only a non-live venue; with live the
		// one environment left, that clause was false for every test-mode
		// request and this loop resolved no authority at all.
		if venue.Venue == venueID && venue.Venue == account.Venue && venue.Environment == account.VenueEnvironment && venue.Ready {
			return manualPreviewAuthority{Venue: venue.Venue, VenueEnvironment: venue.Environment, Product: product, RouteID: routeID}, true
		}
	}
	return manualPreviewAuthority{}, false
}

// manualAccountRoute picks the account route an order for this product and
// settlement asset must be placed through.
//
// A route that names a settlement asset serves only instruments settling in it;
// one that names none serves the whole product. The named route therefore wins
// when both match, so adding a settlement-specific route to an account does not
// silently reroute the instruments it was added for through the general one.
//
// No match means the account cannot trade this instrument at all, which the
// callers render the same way as an unknown instrument: the order is refused
// here rather than at the Gateway, where the refusal would arrive as a fence
// violation and read as a race.
func manualAccountRoute(account ManualAccount, product, settlement string) (string, bool) {
	fallback := ""
	for _, route := range account.Routes {
		if route.RequestedState != manualAccountActive || route.Product != product || route.RouteID == "" {
			continue
		}
		if route.SettlementAsset == "" {
			if fallback == "" {
				fallback = route.RouteID
			}
			continue
		}
		if route.SettlementAsset == settlement {
			return route.RouteID, true
		}
	}
	return fallback, fallback != ""
}

func manualTestNotionalAllowed(ctx context.Context, request manualPreviewRequest, authority manualPreviewAuthority, source ManualMarketAuthority) (bool, error) {
	quantity, err := decimal.NewFromString(request.Quantity)
	if err != nil || !quantity.IsPositive() {
		return false, nil
	}
	if source == nil {
		return false, fmt.Errorf("manual market-data authority is unavailable")
	}
	fact, err := source.Get(ctx, marketdata.Query{
		Venue: authority.Venue, Environment: authority.VenueEnvironment,
		Product: authority.Product, InstrumentID: request.InstrumentID,
	})
	if err != nil {
		return false, err
	}
	priceText := fact.BestAsk
	if request.Side == "sell" {
		priceText = fact.BestBid
	}
	price, err := decimal.NewFromString(priceText)
	if err != nil || !price.IsPositive() {
		return false, nil
	}
	return !quantity.Mul(price).GreaterThan(decimal.NewFromInt(manualTestMaxNotional)), nil
}

// manualAccountReady reports whether an account can carry a manual order.
//
// Mode is not one of the conditions, and the account's environment is no longer
// read at all. The two were only ever cross-checked against each other — test
// mode admitted a non-live account, live mode a live one — and with live the
// one environment left, that check admitted nothing whatsoever in test mode.
// Readiness is now the account's own state and its grant for this product.
func manualAccountReady(account ManualAccount) bool {
	if account.ID == "" || account.RuntimeGeneration < 1 || account.DesiredStateRevision < 1 || account.CredentialVersion < 1 || account.State != manualAccountActive {
		return false
	}
	for _, productID := range account.ProductIDs {
		if productID == manualProductIdentifier {
			return true
		}
	}
	return false
}

func manualAccountMatches(account ManualAccount, fence manualAccountFence) bool {
	return manualAccountReady(account) && account.ID == fence.AccountID &&
		account.DesiredStateRevision == fence.DesiredStateRevision && account.RuntimeGeneration == fence.RuntimeGeneration &&
		account.CredentialVersion == fence.CredentialVersion
}

func (handler *manualProductHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, orders.Mode(mode), "manual")
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *manualProductHandler) getOrder(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	id := r.PathValue("orderId")
	command, err := handler.trading.options.Commands.GetOwned(r.Context(), principal, orders.Mode(mode), id)
	if err != nil || command.ProductID != "manual" {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": command})
}
