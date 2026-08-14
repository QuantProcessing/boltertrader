package httpapi

import (
	"net/http"
	"strconv"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	platformaudit "github.com/QuantProcessing/boltertrader/services/trading/audit"
)

type AuditRouteOptions struct {
	Service      *platformaudit.Service
	Authenticate func(*http.Request) (string, error)
}

func RegisterAuditRoute(mux *http.ServeMux, options AuditRouteOptions) error {
	if mux == nil || options.Service == nil || options.Authenticate == nil {
		return httpx.ConfigErr("audit route dependencies are required")
	}
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		if principal, err := options.Authenticate(r); err != nil || principal == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
				return
			}
			limit = parsed
		}
		records, err := options.Service.List(r.Context(), platformaudit.Filter{ResourceID: r.URL.Query().Get("resourceId"), Limit: limit})
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"audit": records}})
	})
	return nil
}
