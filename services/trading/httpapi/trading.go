package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
)

type ManualExecutor interface {
	Execute(context.Context, orders.Command) (orders.Command, error)
	Cancel(context.Context, orders.Command) (orders.Command, error)
	Ready() bool
}

// ArbitrageExecutor is ManualExecutor's shape because the two products dispatch
// through the same Gateway channel. It is named separately rather than reusing
// the manual interface so that wiring one product does not silently satisfy the
// other's field: a deployment that has manual but not arbitrage should mount
// manual's routes and nothing else.
type ArbitrageExecutor interface {
	Execute(context.Context, orders.Command) (orders.Command, error)
	Cancel(context.Context, orders.Command) (orders.Command, error)
	Ready() bool
}

// FundingBoardAuthority is the cross-venue funding matrix, which the arbitrage
// product reads for two different purposes: to rank the pairs worth opening,
// and to price the one being opened with the same numbers the operator just
// read off the board.
type FundingBoardAuthority interface {
	Get(context.Context) (marketdata.BoardSnapshot, error)
}

type TradingOptions struct {
	Reservations      *reservations.Service
	Commands          *orders.Service
	Portfolio         *portfolio.Projection
	Ledger            *ledger.Service
	Catalog           *catalog.Projection
	Manual            ManualExecutor
	ManualAccounts    ManualAccountAuthority
	Arbitrage         ArbitrageExecutor
	FundingBoard      FundingBoardAuthority
	Principal         func(*http.Request) (principalID string, err error)
	AuthorizeMutation func(*http.Request) error
	Activity          ActivityPublisher
}

func RegisterTradingRoutes(mux *http.ServeMux, options TradingOptions) error {
	if mux == nil || options.Reservations == nil || options.Commands == nil || options.Portfolio == nil || options.Ledger == nil || options.Principal == nil || options.AuthorizeMutation == nil {
		return fmt.Errorf("complete trading options are required")
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
	if options.Catalog != nil && options.Arbitrage != nil && options.FundingBoard != nil {
		registerArbitrageProductRoutes(mux, handler)
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
}

func (handler *tradingHandler) createReservation(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if !handler.authorizeMutation(w, r) {
		return
	}
	var request reservationRequest
	if !decodeTrading(w, r, &request) || request.AccountID != request.NettingScope.AccountID {
		if request.AccountID != request.NettingScope.AccountID {
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
		PrincipalID: principal, ProductID: request.ProductID, Kind: request.ReservationKind,
		Scope: request.NettingScope, Quantity: quantity, Notional: notional, Currency: currency, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
	})
	if err != nil {
		tradingFailure(w, r, "reservations.create", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"meta": httpx.Meta(r), "data": created})
}

func (handler *tradingHandler) listReservations(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.options.Reservations.List(r.Context(), principal)
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) releaseReservation(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	if !handler.authorizeMutation(w, r) {
		return
	}
	item, err := handler.options.Reservations.Release(r.Context(), principal, r.PathValue("reservationId"))
	if err != nil {
		tradingFailure(w, r, "reservations.release", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": item})
}

type commandRequest struct {
	AccountID, ProductID, LineageID, ReservationID, IdempotencyKey, CommandKind string
	Payload                                                                     json.RawMessage `json:"payload"`
}

func (handler *tradingHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
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
	if request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(r.Header.Get("Idempotency-Key")) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	// Manual and arbitrage commands are minted by their own routes, which seal
	// an execution preview first. One arriving here has skipped that, so it is a
	// malformed request rather than a rejected one. This used to answer 403
	// LIVE_PRIVATE_WRITE_DISABLED when the command declared live mode, back when
	// a test command could legitimately take this route.
	if request.ProductID == "manual" || request.ProductID == "arbitrage" {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	command, replay, err := handler.options.Commands.Create(r.Context(), orders.CreateRequest{
		PrincipalID: principal, AccountID: request.AccountID, ProductID: request.ProductID, LineageID: request.LineageID,
		ReservationID: request.ReservationID, IdempotencyKey: request.IdempotencyKey, Method: r.Method, Path: r.URL.Path, CommandKind: request.CommandKind, Payload: request.Payload,
	})
	if err != nil {
		tradingFailure(w, r, "commands.create", err)
		return
	}
	status := http.StatusAccepted
	if replay {
		status = http.StatusOK
	} else {
		publishCommandActivity(handler.options.Activity, command)
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": command})
}

func resumableManualState(state orders.State) bool {
	return state == orders.StateAccepted || state == orders.StateSubmitted || state == orders.StateUncertain
}

func (handler *tradingHandler) getCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	command, err := handler.options.Commands.GetOwned(r.Context(), principal, r.PathValue("commandId"))
	if err != nil {
		tradingError(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": commandProjection(command)})
}

func (handler *tradingHandler) positions(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.options.Portfolio.ListContext(r.Context(), principal, r.URL.Query().Get("accountId"), r.URL.Query().Get("productId"))
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) ledgerEntries(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	items, err := handler.options.Ledger.List(r.Context(), ledger.Query{PrincipalID: principal, ProductID: r.URL.Query().Get("productId"), LineageID: r.URL.Query().Get("lineageId"), AccountID: r.URL.Query().Get("accountId"), InstrumentID: r.URL.Query().Get("instrumentId")})
	if err != nil {
		tradingError(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}

func (handler *tradingHandler) pnl(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	query := ledger.Query{PrincipalID: principal, ProductID: r.URL.Query().Get("productId"), LineageID: r.URL.Query().Get("lineageId"), AccountID: r.URL.Query().Get("accountId"), InstrumentID: r.URL.Query().Get("instrumentId")}
	item, err := handler.options.Ledger.PnL(r.Context(), query)
	if err != nil {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": item})
}

func (handler *tradingHandler) identity(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, err := handler.options.Principal(r)
	if err != nil || principal == "" {
		tradingError(w, http.StatusUnauthorized, "AUTH_REQUIRED")
		return "", false
	}
	return principal, true
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
	httpx.WriteError(w, status, code, "request rejected", nil, false, "")
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

// tradingFailure renders err to the caller and, when the mapping above has lost
// the cause, records it.
//
// codeForTradingError collapses everything it does not recognise into
// VALIDATION_ERROR, which tells a caller only that something was refused — not
// whether the reservation, the command ledger, or the venue itself refused it.
// That is the right answer to send outward, because the causes here come from
// the Gateway and carry venue text we do not control, but it leaves nothing
// behind to act on: a placement rejected by Binance and a placement rejected by
// our own ledger are indistinguishable at the wire.
//
// Only the collapsed case is logged. The three mapped codes already name their
// cause exactly, so logging those would add a line per idempotent replay — which
// is the common case, not a fault.
func tradingFailure(w http.ResponseWriter, r *http.Request, operation string, err error) {
	code := codeForTradingError(err)
	if code == "VALIDATION_ERROR" {
		slog.Error("trading command failed", "operation", operation, "method", r.Method, "path", r.URL.Path, "cause", err)
	}
	tradingError(w, statusForTradingError(err), code)
}
