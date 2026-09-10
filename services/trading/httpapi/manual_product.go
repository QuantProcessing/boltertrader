package httpapi

import (
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
)

type manualProductHandler struct{ trading *tradingHandler }

func registerManualProductRoutes(mux *http.ServeMux, trading *tradingHandler) {
	handler := &manualProductHandler{trading: trading}
	mux.HandleFunc("GET /api/v1/products/manual/venues", handler.venues)
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

func (handler *manualProductHandler) createCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.trading.identity(w, r)
	if !ok || !handler.trading.authorizeMutation(w, r) {
		return
	}
	var request manual.CommandRequest
	if !decodeTrading(w, r, &request) {
		return
	}
	if request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(r.Header.Get("Idempotency-Key")) {
		tradingError(w, http.StatusBadRequest, "VALIDATION_ERROR")
		return
	}
	result, err := handler.trading.options.Manual.Submit(r.Context(), principal, request)
	if err != nil {
		tradingFailure(w, r, "manual.submit", err)
		return
	}
	if result.Changed {
		if request.Action == "cancel" {
			publishCommandActivityAs(handler.trading.options.Activity, result.Command, "command.cancel_accepted")
		} else {
			publishCommandActivity(handler.trading.options.Activity, result.Command)
		}
	}
	handler.trading.options.Commands.NotifyChange(principal)
	status := http.StatusOK
	if result.Changed {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": result.Command})
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
