package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/connectivity"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type AuthorityOperation string

const (
	AuthorityRuntimeStatus       AuthorityOperation = "runtime.status"
	AuthorityRuntimeReadiness    AuthorityOperation = "runtime.readiness"
	AuthorityRuntimeOpenOrders   AuthorityOperation = "runtime.open_orders"
	AuthorityRuntimePositions    AuthorityOperation = "runtime.positions"
	AuthorityRuntimeUncertain    AuthorityOperation = "runtime.uncertain_commands"
	AuthorityListRuntimeAccounts AuthorityOperation = "accounts.runtime.list"
	AuthorityGetRuntimeAccount   AuthorityOperation = "accounts.runtime.get"
	AuthorityRuntimeEvents       AuthorityOperation = "accounts.runtime.events"
	AuthorityCatalogVenues       AuthorityOperation = "catalog.venues"
	AuthorityCatalogInstruments  AuthorityOperation = "catalog.instruments"
	AuthorityMarketData          AuthorityOperation = "market_data"
	AuthorityCandles             AuthorityOperation = "candles"
	AuthorityPrivateEvents       AuthorityOperation = "private_events"
	AuthorityFundingBoard        AuthorityOperation = "funding_board"
	AuthorityFundingHistory      AuthorityOperation = "funding_history"
)

type AuthorityRequest struct {
	Operation AuthorityOperation
	AccountID string
	Query     url.Values
}
type AuthorityResponse struct {
	Body   any
	Status int
}

type RuntimeReadiness struct {
	Ready bool `json:"ready"`
}

type InternalAuthority interface {
	Execute(context.Context, AuthorityRequest) (AuthorityResponse, error)
}

type AuthorityError struct {
	Status    int
	Code      string
	Retryable bool
}

func (err AuthorityError) Error() string { return err.Code }

type InternalOptions struct {
	Authority    InternalAuthority
	Authenticate func(*http.Request) error
	MaxBodyBytes int64
	WriteReady   func() bool
	// FundingBoardChanges is the resident public-facts projection. It carries
	// only change sequences; the handler obtains each canonical wire snapshot
	// through Authority so HTTP and WebSocket cannot drift in shape.
	FundingBoardChanges InternalFundingBoardChanges
	MarketFacts         InternalMarketFacts
	VenueConnectivity   InternalVenueConnectivity
	FreshBBO            InternalFreshBBO
	MarketHub           MarketHub
	MarketKeys          MarketKeys
	AccountEvents       InternalAccountEvents
	AccountEventOwner   domain.PrincipalID
	FundingSettlements  InternalFundingSettlements
	Hotset              MarketHotset
	Now                 func() time.Time
	Accounts            AccountRegistrations
}

type InternalHandler struct {
	authority           InternalAuthority
	authenticate        func(*http.Request) error
	bodyLimit           int64
	writeReady          func() bool
	accounts            AccountRegistrations
	fundingBoardChanges InternalFundingBoardChanges
	marketFacts         InternalMarketFacts
	venueConnectivity   InternalVenueConnectivity
	freshBBO            InternalFreshBBO
	marketHub           MarketHub
	marketKeys          MarketKeys
	accountEvents       InternalAccountEvents
	accountEventOwner   domain.PrincipalID
	fundingSettlements  InternalFundingSettlements
	hotset              MarketHotset
	now                 func() time.Time
	mux                 *http.ServeMux
}

var internalRoutes = []Route{
	{http.MethodGet, "/internal/v1/runtime/status"},
	{http.MethodGet, "/internal/v1/runtime/readiness"},
	{http.MethodGet, "/internal/v1/runtime/open-orders"},
	{http.MethodGet, "/internal/v1/runtime/positions"},
	{http.MethodGet, "/internal/v1/runtime/uncertain-commands"},
	{http.MethodGet, "/internal/v1/accounts/runtime"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
	{http.MethodGet, "/internal/v1/catalog/venues"},
	{http.MethodGet, "/internal/v1/catalog/instruments"},
	{http.MethodGet, "/internal/v1/market-data"},
	{http.MethodGet, "/internal/v1/candles"},
	{http.MethodGet, "/internal/v1/market/ws"},
	{http.MethodGet, "/internal/v1/account/ws"},
	{http.MethodGet, "/internal/v1/market-facts"},
	{http.MethodGet, "/internal/v1/fresh-bbo"},
	{http.MethodGet, "/internal/v1/private-events"},
	{http.MethodGet, "/internal/v1/funding-board"},
	{http.MethodGet, "/internal/v1/funding-history"},
	{http.MethodGet, "/internal/v1/venues/connectivity"},
}

func InternalRoutes() []Route { return append([]Route(nil), internalRoutes...) }

// InternalPublicMarketRoutes is the intentionally browser-reachable subset of
// the Gateway internal plane. It contains public venue facts only; account and
// order capabilities remain absent from the auth edge's Gateway forwarding
// table.
func InternalPublicMarketRoutes() []Route {
	return []Route{
		{Method: http.MethodGet, Pattern: "/internal/v1/catalog/instruments"},
		{Method: http.MethodGet, Pattern: "/internal/v1/market-data"},
		{Method: http.MethodGet, Pattern: "/internal/v1/candles"},
		{Method: http.MethodGet, Pattern: "/internal/v1/funding-history"},
		{Method: http.MethodGet, Pattern: "/internal/v1/funding-board"},
		{Method: http.MethodGet, Pattern: "/internal/v1/venues/connectivity"},
		{Method: http.MethodGet, Pattern: "/internal/v1/market/ws"},
	}
}

// InternalPrivateRealtimeRoutes is reachable only after Auth Edge has resolved
// a browser session. It is separate from account lifecycle so route audits do
// not accidentally classify a private stream as a public market read.
func InternalPrivateRealtimeRoutes() []Route {
	return []Route{{Method: http.MethodGet, Pattern: "/internal/v1/account/ws"}}
}

func NewInternalHandler(options InternalOptions) (*InternalHandler, error) {
	if options.Authority == nil || options.Authenticate == nil {
		return nil, fmt.Errorf("internal HTTP API dependencies are incomplete")
	}
	if options.AccountEvents != nil && options.AccountEventOwner.Validate() != nil {
		return nil, fmt.Errorf("internal HTTP API account events require an owner")
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = defaultBodyLimit
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	handler := &InternalHandler{authority: options.Authority, authenticate: options.Authenticate, bodyLimit: options.MaxBodyBytes, writeReady: options.WriteReady, accounts: options.Accounts, fundingBoardChanges: options.FundingBoardChanges, marketFacts: options.MarketFacts, venueConnectivity: options.VenueConnectivity, freshBBO: options.FreshBBO, marketHub: options.MarketHub, marketKeys: options.MarketKeys, accountEvents: options.AccountEvents, accountEventOwner: options.AccountEventOwner, fundingSettlements: options.FundingSettlements, hotset: options.Hotset, now: options.Now, mux: http.NewServeMux()}
	handler.register()
	if handler.accounts != nil {
		handler.registerAccountRegistrations()
	}
	return handler, nil
}

func (handler *InternalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if handler.authenticate(r) != nil {
		handler.writeError(w, r, http.StatusUnauthorized, "GATEWAY_ADAPTER_AUTH_REQUIRED", false)
		return
	}
	if isMutationRequest(r) && handler.writeReady != nil && !handler.writeReady() {
		handler.writeError(w, r, http.StatusServiceUnavailable, "GATEWAY_WRITES_NOT_READY", true)
		return
	}
	handler.mux.ServeHTTP(w, r)
}

func (handler *InternalHandler) register() {
	handler.mux.HandleFunc("GET /internal/v1/runtime/status", handler.read(AuthorityRuntimeStatus))
	handler.mux.HandleFunc("GET /internal/v1/runtime/readiness", handler.read(AuthorityRuntimeReadiness))
	handler.mux.HandleFunc("GET /internal/v1/runtime/open-orders", handler.read(AuthorityRuntimeOpenOrders))
	handler.mux.HandleFunc("GET /internal/v1/runtime/positions", handler.read(AuthorityRuntimePositions))
	handler.mux.HandleFunc("GET /internal/v1/runtime/uncertain-commands", handler.read(AuthorityRuntimeUncertain))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime", handler.read(AuthorityListRuntimeAccounts))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}", handler.accountRead(AuthorityGetRuntimeAccount))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}/events", handler.accountRead(AuthorityRuntimeEvents))
	handler.mux.HandleFunc("GET /internal/v1/catalog/venues", handler.read(AuthorityCatalogVenues))
	handler.mux.HandleFunc("GET /internal/v1/catalog/instruments", handler.read(AuthorityCatalogInstruments))
	handler.mux.HandleFunc("GET /internal/v1/market-data", handler.marketOverview)
	handler.mux.HandleFunc("GET /internal/v1/candles", handler.instrumentCandles)
	handler.mux.HandleFunc("GET /internal/v1/market/ws", handler.unifiedMarketStream)
	handler.mux.HandleFunc("GET /internal/v1/account/ws", handler.accountEventStream)
	handler.mux.HandleFunc("GET /internal/v1/market-facts", handler.marketFactsSnapshot)
	handler.mux.HandleFunc("GET /internal/v1/fresh-bbo", handler.freshBBOSnapshot)
	handler.mux.HandleFunc("GET /internal/v1/private-events", handler.read(AuthorityPrivateEvents))
	handler.mux.HandleFunc("GET /internal/v1/funding-board", handler.read(AuthorityFundingBoard))
	handler.mux.HandleFunc("GET /internal/v1/funding-history", handler.fundingHistoryRead)
	handler.mux.HandleFunc("GET /internal/v1/venues/connectivity", handler.venueConnectivitySnapshot)
}

type InternalFundingSettlements interface {
	TrackNextSettlement(context.Context, string) error
	TrackActive(context.Context, string, string) (func(), error)
}

type InternalAccountEvents interface {
	SubscribePrincipalWithConnections(context.Context, domain.PrincipalID, int) (*gatewayevent.Subscription, []gatewayevent.Event, error)
}

func (handler *InternalHandler) fundingHistoryRead(w http.ResponseWriter, r *http.Request) {
	instrumentID := strings.TrimSpace(r.URL.Query().Get("instrumentId"))
	release := handler.trackFundingHistory(r.Context(), instrumentID)
	defer release()
	handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: AuthorityFundingHistory, Query: r.URL.Query()})
}

func (handler *InternalHandler) trackFundingHistory(ctx context.Context, instrumentID string) func() {
	release := func() {}
	if handler.hotset != nil && handler.marketKeys != nil && instrumentID != "" {
		if key, keyErr := handler.marketKeys.SubscriptionKey(ctx, instrumentID, exchange.DatasetMark, "", 0); keyErr == nil {
			if lease, leaseErr := handler.hotset.Acquire(ctx, hotset.Key{Owner: key.Owner, InstrumentID: instrumentID}, hotset.ReasonFundingHistory); leaseErr == nil {
				release = lease.Release
			}
		}
	}
	if handler.fundingSettlements != nil {
		if instrumentID != "" {
			_ = handler.fundingSettlements.TrackNextSettlement(ctx, instrumentID)
		}
	}
	return release
}

func (handler *InternalHandler) marketFactsSnapshot(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	if handler.marketFacts == nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "MARKET_FACTS_UNAVAILABLE", true)
		return
	}
	facts, err := handler.marketFacts.Snapshot(r.Context())
	if err != nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "MARKET_FACTS_UNAVAILABLE", true)
		return
	}
	data := map[string]any{"derivativeFacts": facts}
	if handler.venueConnectivity != nil {
		data["venueConnectivity"] = connectivityWireSnapshot(handler.venueConnectivity.Snapshot())
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

type InternalMarketFacts interface {
	Snapshot(context.Context) ([]exchange.DerivativeFact, error)
}

type InternalVenueConnectivity interface {
	Snapshot() []connectivity.Snapshot
}

type InternalFreshBBO interface {
	GetFreshBBO(context.Context, string, time.Duration) (markethub.BBOQuote, error)
}

func (handler *InternalHandler) freshBBOSnapshot(w http.ResponseWriter, r *http.Request) {
	if handler.freshBBO == nil || len(r.URL.Query()) != 1 || strings.TrimSpace(r.URL.Query().Get("instrumentId")) == "" {
		if handler.freshBBO == nil {
			handler.writeError(w, r, http.StatusServiceUnavailable, "QUOTE_UNAVAILABLE", true)
		} else {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		}
		return
	}
	instrumentID := r.URL.Query().Get("instrumentId")
	if handler.hotset != nil && handler.marketKeys != nil {
		if key, keyErr := handler.marketKeys.SubscriptionKey(r.Context(), instrumentID, exchange.DatasetBBO, "", 0); keyErr == nil {
			if lease, leaseErr := handler.hotset.Acquire(r.Context(), hotset.Key{Owner: key.Owner, InstrumentID: instrumentID}, hotset.ReasonExecution); leaseErr == nil {
				defer lease.Release()
			}
		}
	}
	quote, err := handler.freshBBO.GetFreshBBO(r.Context(), instrumentID, 2*time.Second)
	if err != nil {
		code := "QUOTE_UNAVAILABLE"
		if errors.Is(err, markethub.ErrQuoteStale) {
			code = "QUOTE_STALE"
		}
		handler.writeError(w, r, http.StatusServiceUnavailable, code, true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": quote})
}

func (handler *InternalHandler) venueConnectivitySnapshot(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 0 {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	if handler.venueConnectivity == nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "VENUE_CONNECTIVITY_UNAVAILABLE", true)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": connectivityWireSnapshot(handler.venueConnectivity.Snapshot())})
}

func (handler *InternalHandler) read(operation AuthorityOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: operation, Query: r.URL.Query()})
	}
}

func (handler *InternalHandler) accountRead(operation AuthorityOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: operation, AccountID: r.PathValue("accountId"), Query: r.URL.Query()})
	}
}

func (handler *InternalHandler) execute(w http.ResponseWriter, r *http.Request, status int, request AuthorityRequest) {
	response, ok := handler.executeAuthority(w, r, request)
	if !ok {
		return
	}
	if response.Status != 0 {
		if response.Status != http.StatusOK && response.Status != http.StatusAccepted && response.Status != http.StatusCreated && response.Status != http.StatusNoContent {
			handler.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", true)
			return
		}
		status = response.Status
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]any{"data": response.Body})
}

func (handler *InternalHandler) executeAuthority(w http.ResponseWriter, r *http.Request, request AuthorityRequest) (AuthorityResponse, bool) {
	response, err := handler.authority.Execute(r.Context(), request)
	if err != nil {
		var authorityErr AuthorityError
		if errors.As(err, &authorityErr) && authorityErr.Status >= 400 && authorityErr.Code != "" {
			handler.writeError(w, r, authorityErr.Status, authorityErr.Code, authorityErr.Retryable)
			return AuthorityResponse{}, false
		}
		var authorityErrPointer *AuthorityError
		if errors.As(err, &authorityErrPointer) && authorityErrPointer.Status >= 400 && authorityErrPointer.Code != "" {
			handler.writeError(w, r, authorityErrPointer.Status, authorityErrPointer.Code, authorityErrPointer.Retryable)
			return AuthorityResponse{}, false
		}
		handler.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", true)
		return AuthorityResponse{}, false
	}
	return response, true
}

func (handler *InternalHandler) writeError(w http.ResponseWriter, r *http.Request, status int, code string, retryable bool) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": "request denied", "requestId": r.Header.Get("X-Request-ID"), "retryable": retryable,
	}})
}
