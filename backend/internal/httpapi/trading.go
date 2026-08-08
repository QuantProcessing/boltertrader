package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
)

type ManualExecutor interface {
	Execute(context.Context, orders.Command) (orders.Command, error)
	Cancel(context.Context, orders.Command) (orders.Command, error)
	Ready() bool
}

type ManualMarketAuthority interface {
	Get(context.Context, marketdata.Query) (marketdata.Fact, error)
}

type FundingAccountingRefresher interface {
	RefreshAccounting(context.Context, string, string) (FundingReadScopes, error)
}

type unavailableFundingAccountingRefresher struct{}

func (unavailableFundingAccountingRefresher) RefreshAccounting(context.Context, string, string) (FundingReadScopes, error) {
	return nil, fmt.Errorf("authoritative Funding accounting is unavailable")
}

type FundingReadScope struct {
	LineageID, AccountID, InstrumentID string
}

type FundingReadScopes []FundingReadScope

func (scopes FundingReadScopes) owns(lineageID, accountID, instrumentID string) bool {
	for _, scope := range scopes {
		if scope.LineageID == lineageID && scope.AccountID == accountID && scope.InstrumentID == instrumentID {
			return true
		}
	}
	return false
}

type TradingOptions struct {
	Reservations      *reservations.Service
	Commands          *orders.Service
	Portfolio         *portfolio.Projection
	Ledger            *ledger.Service
	Catalog           *catalog.Projection
	Manual            ManualExecutor
	ManualAccounts    ManualAccountAuthority
	ManualMarketData  ManualMarketAuthority
	Principal         func(*http.Request) (principalID, mode string, err error)
	AuthorizeMutation func(*http.Request) error
	Activity          ActivityPublisher
	FundingAccounting FundingAccountingRefresher
}

func RegisterTradingRoutes(mux *http.ServeMux, options TradingOptions) error {
	if mux == nil || options.Reservations == nil || options.Commands == nil || options.Portfolio == nil || options.Ledger == nil || options.Principal == nil || options.AuthorizeMutation == nil {
		return fmt.Errorf("complete trading options are required")
	}
	if options.FundingAccounting == nil {
		options.FundingAccounting = unavailableFundingAccountingRefresher{}
	}
	handler := &tradingHandler{options: options}
	mux.HandleFunc("POST /api/v1/reservations", handler.createReservation)
	mux.HandleFunc("GET /api/v1/reservations", handler.listReservations)
	mux.HandleFunc("DELETE /api/v1/reservations/{reservationId}", handler.releaseReservation)
	mux.HandleFunc("POST /api/v1/commands", handler.createCommand)
	mux.HandleFunc("GET /api/v1/commands/{commandId}", handler.getCommand)
	mux.HandleFunc("GET /api/v1/positions", handler.positions)
	mux.HandleFunc("GET /api/v1/ledger", handler.ledgerEntries)
	mux.HandleFunc("GET /api/v1/pnl", handler.pnl)
	if options.Catalog != nil && options.Manual != nil {
		registerManualProductRoutes(mux, handler)
	}
	return nil
}

type tradingHandler struct{ options TradingOptions }

type reservationRequest struct {
	AccountID       string                    `json:"accountId"`
	ProductID       string                    `json:"productId"`
	ReservationKind reservations.Kind         `json:"reservationKind"`
	NettingScope    reservations.NettingScope `json:"nettingScope"`
	Quantity        *string                   `json:"quantity"`
	Notional        *string                   `json:"notional"`
	Currency        *string                   `json:"currency"`
	Mode            string                    `json:"mode"`
}

func (handler *tradingHandler) createReservation(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if !handler.authorizeMutation(w, r) {
		return
	}
	var request reservationRequest
	if !decodeTrading(w, r, &request) || request.Mode != mode || request.AccountID != request.NettingScope.AccountID {
		if request.Mode != mode || request.AccountID != request.NettingScope.AccountID {
			tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		}
		return
	}
	quantity, notional, currency := "", "", ""
	if request.Quantity != nil {
		quantity = *request.Quantity
	}
	if request.Notional != nil {
		notional = *request.Notional
	}
	if request.Currency != nil {
		currency = *request.Currency
	}
	created, err := handler.options.Reservations.Create(r.Context(), reservations.CreateRequest{
		PrincipalID: principal, Mode: reservations.Mode(mode), ProductID: request.ProductID, Kind: request.ReservationKind,
		Scope: request.NettingScope, Quantity: quantity, Notional: notional, Currency: currency, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	})
	if err != nil {
		tradingError(w, statusForTradingError(err), codeForTradingError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"meta": meta(r), "data": created})
}

func (handler *tradingHandler) listReservations(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.options.Reservations.List(r.Context(), principal, reservations.Mode(mode))
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) releaseReservation(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if !handler.authorizeMutation(w, r) {
		return
	}
	item, err := handler.options.Reservations.Release(r.Context(), principal, reservations.Mode(mode), r.PathValue("reservationId"))
	if err != nil {
		tradingError(w, statusForTradingError(err), codeForTradingError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": item})
}

type commandRequest struct {
	AccountID, ProductID, LineageID, ReservationID, IdempotencyKey, CommandKind, Mode string
	Payload                                                                           json.RawMessage `json:"payload"`
}

func (handler *tradingHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if !handler.authorizeMutation(w, r) {
		return
	}
	var request commandRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.Mode != mode || request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(r.Header.Get("Idempotency-Key")) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	if request.ProductID == "manual" {
		if request.Mode == string(orders.ModeLive) {
			tradingError(w, http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED")
			return
		}
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	command, replay, err := handler.options.Commands.Create(r.Context(), orders.CreateRequest{
		PrincipalID: principal, Mode: orders.Mode(mode), AccountID: request.AccountID, ProductID: request.ProductID, LineageID: request.LineageID,
		ReservationID: request.ReservationID, IdempotencyKey: request.IdempotencyKey, Method: r.Method, Path: r.URL.Path, CommandKind: request.CommandKind, Payload: request.Payload,
	})
	if err != nil {
		tradingError(w, statusForTradingError(err), codeForTradingError(err))
		return
	}
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	} else {
		publishCommandActivity(handler.options.Activity, command)
	}
	writeJSON(w, status, map[string]any{"meta": meta(r), "data": command})
}

func resumableManualState(state orders.State) bool {
	return state == orders.StateAccepted || state == orders.StateSubmitted || state == orders.StateUncertain
}

func (handler *tradingHandler) getCommand(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	command, err := handler.options.Commands.GetOwned(r.Context(), principal, orders.Mode(mode), r.PathValue("commandId"))
	if err != nil {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": commandProjection(command)})
}

func (handler *tradingHandler) positions(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	scopes, ok := handler.refreshFundingAccounting(w, r, principal, mode)
	if !ok {
		return
	}
	items, err := handler.options.Portfolio.ListContext(r.Context(), principal, portfolio.Mode(mode), r.URL.Query().Get("accountId"), r.URL.Query().Get("productId"))
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	if scopes != nil {
		items = filterFundingPositions(items, scopes)
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) ledgerEntries(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	scopes, ok := handler.refreshFundingAccounting(w, r, principal, mode)
	if !ok {
		return
	}
	items, err := handler.options.Ledger.List(r.Context(), ledger.Query{PrincipalID: principal, Mode: ledger.Mode(mode), ProductID: r.URL.Query().Get("productId"), LineageID: r.URL.Query().Get("lineageId"), AccountID: r.URL.Query().Get("accountId"), InstrumentID: r.URL.Query().Get("instrumentId")})
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	if scopes != nil {
		items = filterFundingLedgerEntries(items, scopes)
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) pnl(w http.ResponseWriter, r *http.Request) {
	principal, mode, ok := handler.identity(w, r)
	if !ok {
		return
	}
	scopes, ok := handler.refreshFundingAccounting(w, r, principal, mode)
	if !ok {
		return
	}
	query := ledger.Query{PrincipalID: principal, Mode: ledger.Mode(mode), ProductID: r.URL.Query().Get("productId"), LineageID: r.URL.Query().Get("lineageId"), AccountID: r.URL.Query().Get("accountId"), InstrumentID: r.URL.Query().Get("instrumentId")}
	if scopes != nil && query.ProductID == "funding" && !scopes.owns(query.LineageID, query.AccountID, query.InstrumentID) {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	item, err := handler.options.Ledger.PnL(r.Context(), query)
	if err != nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": item})
}

func (handler *tradingHandler) refreshFundingAccounting(w http.ResponseWriter, r *http.Request, principal, mode string) (FundingReadScopes, bool) {
	productID := r.URL.Query().Get("productId")
	if mode != "test" || (productID != "" && productID != "funding") {
		return nil, true
	}
	scopes, err := handler.options.FundingAccounting.RefreshAccounting(r.Context(), principal, mode)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "AUTHORITATIVE_ACCOUNTING_UNAVAILABLE")
		return nil, false
	}
	if scopes == nil {
		scopes = FundingReadScopes{}
	}
	return scopes, true
}

func filterFundingPositions(items []portfolio.Position, scopes FundingReadScopes) []portfolio.Position {
	filtered := make([]portfolio.Position, 0, len(items))
	for _, item := range items {
		if item.ProductID != "funding" || scopes.owns(item.LineageID, item.AccountID, item.InstrumentID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func filterFundingLedgerEntries(items []ledger.Entry, scopes FundingReadScopes) []ledger.Entry {
	filtered := make([]ledger.Entry, 0, len(items))
	for _, item := range items {
		if item.ProductID != "funding" || scopes.owns(item.LineageID, item.AccountID, item.InstrumentID) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func (handler *tradingHandler) identity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	principal, mode, err := handler.options.Principal(r)
	if err != nil || principal == "" || (mode != "test" && mode != "live") {
		tradingError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
		return "", "", false
	}
	return principal, mode, true
}

func (handler *tradingHandler) authorizeMutation(w http.ResponseWriter, r *http.Request) bool {
	if err := handler.options.AuthorizeMutation(r); err != nil {
		tradingError(w, http.StatusForbidden, "AUTH_REQUIRED")
		return false
	}
	return true
}

func decodeTrading(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return false
	}
	if decoder.Decode(&struct{}{}) == nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return false
	}
	return true
}

func tradingError(w http.ResponseWriter, status int, code string) {
	writeError(w, status, code, "request rejected", nil, false, "")
}
func statusForTradingError(err error) int {
	if err == reservations.ErrIdempotencyConflict || err == reservations.ErrScopeOwned || err == orders.ErrIdempotencyConflict {
		return http.StatusConflict
	}
	if err == orders.ErrLiveWriteDisabled {
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
func codeForTradingError(err error) string {
	if err == reservations.ErrIdempotencyConflict || err == orders.ErrIdempotencyConflict {
		return "IDEMPOTENCY_CONFLICT"
	}
	if err == orders.ErrLiveWriteDisabled {
		return "LIVE_PRIVATE_WRITE_DISABLED"
	}
	return "VALIDATION_ERROR"
}
