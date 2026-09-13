package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/notifications"
)

var notificationRoutes = []string{
	"GET /api/v1/notifications", "PUT /api/v1/notifications/{provider}",
	"DELETE /api/v1/notifications/{provider}", "POST /api/v1/notifications/{provider}/test",
}

type NotificationRoutesOptions struct {
	Service   *notifications.Service
	Principal func(*http.Request) (string, error)
}

func RegisterNotificationRoutes(mux *http.ServeMux, options NotificationRoutesOptions) error {
	if mux == nil || options.Service == nil || options.Principal == nil {
		return errors.New("notification route dependencies are required")
	}
	h := notificationHandler{service: options.Service, principal: options.Principal}
	mux.HandleFunc("GET /api/v1/notifications", h.list)
	mux.HandleFunc("PUT /api/v1/notifications/{provider}", h.save)
	mux.HandleFunc("DELETE /api/v1/notifications/{provider}", h.delete)
	mux.HandleFunc("POST /api/v1/notifications/{provider}/test", h.test)
	return nil
}

type notificationHandler struct {
	service   *notifications.Service
	principal func(*http.Request) (string, error)
}

func (h notificationHandler) identity(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, err := h.principal(r)
	if err != nil || p == "" {
		httpx.WriteError(w, 401, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return "", false
	}
	return p, true
}
func (h notificationHandler) list(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identity(w, r)
	if !ok {
		return
	}
	items, err := h.service.List(r.Context(), p)
	if err != nil {
		notificationError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"items": items}})
}
func (h notificationHandler) save(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identity(w, r)
	if !ok {
		return
	}
	var input notifications.Input
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&input) != nil {
		notificationError(w, r, notifications.ErrInvalid)
		return
	}
	provider := notifications.Provider(strings.ToLower(r.PathValue("provider")))
	item, err := h.service.Save(r.Context(), p, provider, input)
	if err != nil {
		notificationError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"meta": httpx.Meta(r), "data": item})
}
func (h notificationHandler) delete(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identity(w, r)
	if !ok {
		return
	}
	err := h.service.Delete(r.Context(), p, notifications.Provider(strings.ToLower(r.PathValue("provider"))))
	if err != nil {
		notificationError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h notificationHandler) test(w http.ResponseWriter, r *http.Request) {
	p, ok := h.identity(w, r)
	if !ok {
		return
	}
	err := h.service.Test(r.Context(), p, notifications.Provider(strings.ToLower(r.PathValue("provider"))))
	if err != nil {
		notificationError(w, r, err)
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"sent": true}})
}
func notificationError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := 500, "INTERNAL_ERROR"
	switch {
	case errors.Is(err, notifications.ErrInvalid):
		status, code = 400, "VALIDATION_ERROR"
	case errors.Is(err, notifications.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(err, notifications.ErrUnavailable):
		status, code = 503, "DEPENDENCY_UNAVAILABLE"
	case errors.Is(err, notifications.ErrDelivery):
		status, code = 502, "NOTIFICATION_FAILED"
	}
	httpx.WriteError(w, status, code, "notification request failed", nil, false, httpx.RequestID(r))
}
