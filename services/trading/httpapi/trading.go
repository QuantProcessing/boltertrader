package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

type ManualApplication interface {
	Submit(context.Context, string, manual.CommandRequest) (manual.Submission, error)
}

type TradingOptions struct {
	Commands          *orders.Service
	Portfolio         *portfolio.Projection
	Ledger            *ledger.Service
	Catalog           *catalog.Projection
	Manual            ManualApplication
	Principal         func(*http.Request) (principalID string, err error)
	AuthorizeMutation func(*http.Request) error
	Activity          ActivityPublisher
}

func RegisterTradingRoutes(mux *http.ServeMux, options TradingOptions) error {
	if mux == nil || options.Commands == nil || options.Portfolio == nil || options.Ledger == nil || options.Principal == nil || options.AuthorizeMutation == nil {
		return fmt.Errorf("complete trading options are required")
	}
	handler := &tradingHandler{options: options}
	mux.HandleFunc("GET /api/v1/trading/realtime/ws", handler.ordersStream)
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
	positions, err := handler.options.Portfolio.ListContext(r.Context(), principal, r.URL.Query().Get("accountId"), r.URL.Query().Get("productId"))
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "ACCOUNTING_UNAVAILABLE")
		return
	}
	resources := projectLedgerEntries(items, positions)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": resources}})
}

func (handler *tradingHandler) pnl(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.identity(w, r)
	if !ok {
		return
	}
	query := ledger.Query{PrincipalID: principal, ProductID: r.URL.Query().Get("productId"), LineageID: r.URL.Query().Get("lineageId"), AccountID: r.URL.Query().Get("accountId"), InstrumentID: r.URL.Query().Get("instrumentId")}
	if query.ProductID == "" || query.LineageID == "" || query.AccountID == "" || query.InstrumentID == "" {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	positions, err := handler.options.Portfolio.ListContext(r.Context(), principal, query.AccountID, query.ProductID)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "ACCOUNTING_UNAVAILABLE")
		return
	}
	for _, position := range positions {
		if position.LineageID == query.LineageID && position.InstrumentID == query.InstrumentID && position.AccountingStatus != "" && position.AccountingStatus != "ready" {
			tradingError(w, http.StatusConflict, "ACCOUNTING_REVIEW_REQUIRED")
			return
		}
	}
	item, err := handler.options.Ledger.PnL(r.Context(), query)
	if err != nil {
		tradingError(w, http.StatusServiceUnavailable, "ACCOUNTING_UNAVAILABLE")
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
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return false
	}
	return true
}

func tradingError(w http.ResponseWriter, status int, code string) {
	httpx.WriteError(w, status, code, "request rejected", nil, false, "")
}
func statusForTradingError(err error) int {
	if errors.Is(err, portfolio.ErrAccountingReview) {
		return http.StatusConflict
	}
	if errors.Is(err, orders.ErrIdempotencyConflict) {
		return http.StatusConflict
	}
	if errors.Is(err, orders.ErrLiveWriteDisabled) {
		return http.StatusForbidden
	}
	if errors.Is(err, orders.ErrNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, manual.ErrValidation) {
		return http.StatusBadRequest
	}
	return http.StatusServiceUnavailable
}
func codeForTradingError(err error) string {
	if errors.Is(err, portfolio.ErrAccountingReview) {
		return "ACCOUNTING_REVIEW_REQUIRED"
	}
	if errors.Is(err, orders.ErrIdempotencyConflict) {
		return "IDEMPOTENCY_CONFLICT"
	}
	if errors.Is(err, orders.ErrLiveWriteDisabled) {
		return "LIVE_PRIVATE_WRITE_DISABLED"
	}
	if errors.Is(err, orders.ErrNotFound) {
		return "NOT_FOUND"
	}
	if errors.Is(err, manual.ErrValidation) {
		return "VALIDATION_ERROR"
	}
	return "GATEWAY_UNAVAILABLE"
}

// Dependency failures are logged without exposing request bodies or upstream errors.
func tradingFailure(w http.ResponseWriter, r *http.Request, operation string, err error) {
	code := codeForTradingError(err)
	if statusForTradingError(err) >= http.StatusInternalServerError {
		slog.Error("trading command failed", "operation", operation, "method", r.Method, "path", r.URL.Path, "code", code)
	}
	tradingError(w, statusForTradingError(err), code)
}

type ledgerEntryResource struct {
	ledger.Entry
	AccountingStatus string `json:"accountingStatus,omitempty"`
}

func projectLedgerEntries(items []ledger.Entry, positions []portfolio.Position) []ledgerEntryResource {
	statuses := map[[4]string]string{}
	for _, position := range positions {
		statuses[[4]string{position.ProductID, position.LineageID, position.AccountID, position.InstrumentID}] = position.AccountingStatus
	}
	resources := make([]ledgerEntryResource, 0, len(items))
	for _, entry := range items {
		resources = append(resources, ledgerEntryResource{Entry: entry, AccountingStatus: statuses[[4]string{entry.ProductID, entry.LineageID, entry.AccountID, entry.InstrumentID}]})
	}
	return resources
}
