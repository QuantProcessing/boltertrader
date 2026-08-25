package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	AccountID, InstrumentID, Side, OrderType, Quantity, LimitPrice, TimeInForce string
	ReduceOnly                                                                  bool
	Leverage                                                                    int
}

type manualPreview struct {
	PreviewID string `json:"previewId"`
	manualPreviewRequest
	PrincipalID     string               `json:"-"`
	Blockers        []string             `json:"blockers"`
	Warnings        []string             `json:"warnings"`
	Checks          []manualPreviewCheck `json:"checks"`
	Notional        string               `json:"notional,omitempty"`
	BestBid         string               `json:"bestBid,omitempty"`
	BestAsk         string               `json:"bestAsk,omitempty"`
	ReferencePrice  string               `json:"referencePrice,omitempty"`
	QuoteObservedAt time.Time            `json:"quoteObservedAt,omitzero"`
}

type manualPreviewCheck struct {
	Code     string `json:"code"`
	Status   string `json:"status"`
	Actual   string `json:"actual,omitempty"`
	Required string `json:"required,omitempty"`
}

type manualCommandRequest struct {
	PreviewID, Action, LineageID, OrderID, IdempotencyKey string
}

type manualPreviewAuthority struct {
	PrincipalID      string               `json:"principalId"`
	Request          manualPreviewRequest `json:"request"`
	AccountFence     manualAccountFence   `json:"accountFence"`
	CatalogRevision  string               `json:"catalogRevision"`
	Venue            string               `json:"venue"`
	VenueEnvironment string               `json:"venueEnvironment"`
	Product          string               `json:"product"`
	// RouteID is the account route the preview was priced against. It is
	// sealed here for the same reason as the runtime identity: the Gateway
	// refuses a placement whose route does not match the account's current
	// one, so an order must carry the route it was authorized under rather
	// than whichever route the account happens to hold at submission.
	RouteID   string    `json:"routeId"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type manualAccountFence struct {
	AccountID string `json:"accountId"`
	RuntimeID string `json:"runtimeId"`
}

// ManualAccount is the slice of an account the manual product must see to
// decide whether an order may be placed against it. The gateway owns the
// account record; this is the read-only projection carried across the wire, so
// it holds no credential reference.
//
// RuntimeID is an internal write fence. A preview is authorized against the
// currently active runtime and cannot be replayed after the account is removed
// and re-created.
type ManualAccount struct {
	ID               string
	RuntimeID        string
	Venue            string
	VenueEnvironment string
	State            string
	ProductIDs       []string
	Routes           []ManualAccountRoute
	// Deprecated source-compatibility fields. Gateway reads no longer fill
	// these and previews never serialize them.
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
	RouteProfileID  string
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
	SealPreview(principal string, payload []byte) (string, error)
	OpenPreview(principal string, token string) ([]byte, error)
	PreviewNow() time.Time
}

const manualPreviewTTL = 30 * time.Second

func registerManualProductRoutes(mux *http.ServeMux, trading *tradingHandler) {
	handler := &manualProductHandler{trading: trading}
	mux.HandleFunc("GET /api/v1/products/manual/venues", handler.venues)
	mux.HandleFunc("POST /api/v1/products/manual/execution-previews", handler.createPreview)
	mux.HandleFunc("POST /api/v1/products/manual/commands", handler.createCommand)
	mux.HandleFunc("GET /api/v1/products/manual/orders", handler.listOrders)
	mux.HandleFunc("GET /api/v1/products/manual/orders/{orderId}", handler.getOrder)
}

func (handler *manualProductHandler) venues(w http.ResponseWriter, r *http.Request) {
	if _, ok := handler.trading.identity(w, r); !ok {
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
	principal, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request manualPreviewRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if validateManualPreview(request) != nil {
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
	instrument, ok := snapshot.Instrument(request.InstrumentID)
	if !ok {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if !manualLeverageValid(authority.Product, request.Leverage) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
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
	authority.Request = request
	authority.AccountFence = manualAccountFence{
		AccountID: account.ID, RuntimeID: manualRuntimeID(account),
	}
	authority.CatalogRevision = snapshot.Revision
	authority.IssuedAt = now
	authority.ExpiresAt = now.Add(manualPreviewTTL)
	canonical, _ := json.Marshal(authority)
	previewID, err := codec.SealPreview(principal, canonical)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	var bestBid, bestAsk, referencePrice string
	var quoteObservedAt time.Time
	if handler.trading.options.FreshBBO != nil {
		quote, quoteErr := handler.trading.options.FreshBBO.GetFreshBBO(r.Context(), request.InstrumentID)
		if quoteErr != nil {
			code := "QUOTE_UNAVAILABLE"
			if errors.Is(quoteErr, marketdata.ErrQuoteStale) {
				code = "QUOTE_STALE"
			}
			tradingError(w, http.StatusServiceUnavailable, code)
			return
		}
		bestBid, bestAsk, quoteObservedAt = quote.BestBid, quote.BestAsk, quote.ObservedAt
		if request.Side == "buy" {
			referencePrice = bestAsk
		} else {
			referencePrice = bestBid
		}
	}
	checks, blockers, warnings, notional := manualOrderChecksAtPrice(instrument, request, referencePrice)
	preview := manualPreview{
		PreviewID: previewID, manualPreviewRequest: request, PrincipalID: principal,
		Blockers: blockers, Warnings: warnings, Checks: checks, Notional: notional,
		BestBid: bestBid, BestAsk: bestAsk, ReferencePrice: referencePrice, QuoteObservedAt: quoteObservedAt,
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"meta": httpx.Meta(r), "data": preview})
}

func manualOrderChecks(instrument catalog.Instrument, request manualPreviewRequest) ([]manualPreviewCheck, []string, []string, string) {
	return manualOrderChecksAtPrice(instrument, request, "")
}

func manualOrderChecksAtPrice(instrument catalog.Instrument, request manualPreviewRequest, marketPrice string) ([]manualPreviewCheck, []string, []string, string) {
	quantity, _ := decimal.NewFromString(request.Quantity)
	quantityStep, _ := decimal.NewFromString(instrument.Rules.QuantityIncrement)
	minimumQuantity, _ := decimal.NewFromString(instrument.Rules.MinQuantity)
	checks := make([]manualPreviewCheck, 0, 4)
	blockers := make([]string, 0, 4)
	warnings := make([]string, 0, 1)

	appendCheck := func(code string, passed bool, actual, required decimal.Decimal) {
		status := "passed"
		if !passed {
			status = "blocked"
			blockers = append(blockers, code)
		}
		checks = append(checks, manualPreviewCheck{
			Code: code, Status: status, Actual: actual.String(), Required: required.String(),
		})
	}

	if minimumQuantity.IsPositive() {
		appendCheck("quantity_minimum", !quantity.LessThan(minimumQuantity), quantity, minimumQuantity)
	}
	appendCheck("quantity_increment", quantity.Mod(quantityStep).IsZero(), quantity, quantityStep)

	if strings.EqualFold(request.OrderType, "market") {
		if minimumNotional, err := decimal.NewFromString(instrument.Rules.MinNotional); err == nil && minimumNotional.IsPositive() {
			price, priceErr := decimal.NewFromString(marketPrice)
			if priceErr == nil && price.IsPositive() {
				notional := quantity.Mul(price)
				appendCheck("minimum_notional", !notional.LessThan(minimumNotional), notional, minimumNotional)
				return checks, blockers, warnings, notional.String()
			}
			checks = append(checks, manualPreviewCheck{Code: "market_notional", Status: "unavailable", Required: minimumNotional.String()})
			warnings = append(warnings, "market_notional")
		}
		return checks, blockers, warnings, ""
	}

	price, _ := decimal.NewFromString(request.LimitPrice)
	priceStep, _ := decimal.NewFromString(instrument.Rules.PriceIncrement)
	appendCheck("price_increment", price.Mod(priceStep).IsZero(), price, priceStep)
	notional := quantity.Mul(price)
	if minimumNotional, err := decimal.NewFromString(instrument.Rules.MinNotional); err == nil && minimumNotional.IsPositive() {
		appendCheck("minimum_notional", !notional.LessThan(minimumNotional), notional, minimumNotional)
	}
	return checks, blockers, warnings, notional.String()
}

func validateManualPreview(request manualPreviewRequest) error {
	quantity, quantityErr := decimal.NewFromString(request.Quantity)
	if request.AccountID == "" || request.InstrumentID == "" || (request.Side != "buy" && request.Side != "sell") || quantityErr != nil || !quantity.IsPositive() || request.Leverage < 0 {
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

func manualLeverageValid(product string, leverage int) bool {
	if leverage < 0 {
		return false
	}
	if strings.EqualFold(product, "spot") {
		return leverage == 0
	}
	return strings.EqualFold(product, "perp")
}

func (handler *manualProductHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
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
		handler.cancelCommand(w, r, principal, request)
		return
	case "close":
		if request.PreviewID != "" || request.OrderID != "" {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
			return
		}
		handler.closePosition(w, r, principal, request)
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
	payload, err := codec.OpenPreview(principal, request.PreviewID)
	if err != nil {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	var authority manualPreviewAuthority
	now := codec.PreviewNow()
	if json.Unmarshal(payload, &authority) != nil || authority.PrincipalID != principal ||
		validateManualPreview(authority.Request) != nil || !manualLeverageValid(authority.Product, authority.Request.Leverage) || authority.Venue == "" || authority.VenueEnvironment == "" || authority.CatalogRevision == "" ||
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
	preview := manualPreview{PreviewID: request.PreviewID, manualPreviewRequest: authority.Request, PrincipalID: principal}
	handler.submitPlace(w, r, principal, request, preview, authority)
}

func (handler *manualProductHandler) cancelCommand(w http.ResponseWriter, r *http.Request, principal string, request manualCommandRequest) {
	if !handler.trading.options.Manual.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	command, err := handler.trading.options.Commands.GetOwned(r.Context(), principal, request.OrderID)
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

func (handler *manualProductHandler) closePosition(w http.ResponseWriter, r *http.Request, principal string, request manualCommandRequest) {
	commands, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, "manual")
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
	positions, err := handler.trading.options.Portfolio.ListContext(r.Context(), principal, "", "manual")
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
		OrderType: "market", Quantity: quantity.Abs().String(), ReduceOnly: true,
	}
	authority.AccountFence = manualAccountFence{
		AccountID: account.ID, RuntimeID: manualRuntimeID(account),
	}
	preview := manualPreview{manualPreviewRequest: closeRequest, PrincipalID: principal}
	handler.submitPlace(w, r, principal, request, preview, authority)
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

func (handler *manualProductHandler) submitPlace(w http.ResponseWriter, r *http.Request, principal string, request manualCommandRequest, preview manualPreview, authority manualPreviewAuthority) {
	if !handler.trading.options.Manual.Ready() {
		tradingError(w, http.StatusServiceUnavailable, "GATEWAY_UNAVAILABLE")
		return
	}
	intentPayload, _ := json.Marshal(map[string]any{
		"instrumentId": preview.InstrumentID, "side": preview.Side, "quantity": preview.Quantity,
		"orderType": strings.ToLower(preview.OrderType), "limitPrice": preview.LimitPrice,
		"timeInForce": preview.TimeInForce, "reduceOnly": preview.ReduceOnly,
		"leverage":  preview.Leverage,
		"routeId":   authority.RouteID,
		"runtimeId": authority.AccountFence.RuntimeID,
	})
	kind := reservations.KindOpen
	if preview.ReduceOnly {
		kind = reservations.KindClose
	}
	reservation, err := handler.trading.options.Reservations.Create(r.Context(), reservations.CreateRequest{
		PrincipalID: principal, ProductID: "manual", Kind: kind,
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
		CommandKind: "manual.place", Payload: intentPayload,
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
	instrument, found := snapshot.Instrument(instrumentID)
	if !found || !instrument.Ready {
		return manualPreviewAuthority{}, false
	}
	routeID, routed := manualAccountRoute(account, instrument.Product, instrument.Settlement)
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
		if venue.Venue == instrument.Venue && venue.Venue == account.Venue && venue.Environment == account.VenueEnvironment && venue.Ready {
			return manualPreviewAuthority{Venue: venue.Venue, VenueEnvironment: venue.Environment, Product: instrument.Product, RouteID: routeID}, true
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

// manualAccountReady reports whether an account can carry a manual order.
//
// Mode is not one of the conditions, and the account's environment is no longer
// read at all. The two were only ever cross-checked against each other — test
// mode admitted a non-live account, live mode a live one — and with live the
// one environment left, that check admitted nothing whatsoever in test mode.
// Readiness is now the account's own state and its grant for this product.
func manualAccountReady(account ManualAccount) bool {
	if account.ID == "" || manualRuntimeID(account) == "" || account.State != manualAccountActive {
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
		manualRuntimeID(account) == fence.RuntimeID
}

func manualRuntimeID(account ManualAccount) string {
	if runtimeID := strings.TrimSpace(account.RuntimeID); runtimeID != "" {
		return runtimeID
	}
	if account.DesiredStateRevision > 0 && account.RuntimeGeneration > 0 && account.CredentialVersion > 0 {
		return "legacy-runtime"
	}
	return ""
}

func (handler *manualProductHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.trading.options.Commands.ListOwned(r.Context(), principal, "manual")
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *manualProductHandler) getOrder(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok {
		return
	}
	id := r.PathValue("orderId")
	command, err := handler.trading.options.Commands.GetOwned(r.Context(), principal, id)
	if err != nil || command.ProductID != "manual" {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": command})
}
