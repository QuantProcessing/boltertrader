package httpapi

import (
	"net/http"
	"strconv"

	platformaudit "github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
)

type AuditRouteOptions struct {
	Service      *platformaudit.Service
	Authenticate func(*http.Request) (string, error)
}

func RegisterAuditRoute(mux *http.ServeMux, options AuditRouteOptions) error {
	if mux == nil || options.Service == nil || options.Authenticate == nil {
		return configErr("audit route dependencies are required")
	}
	mux.HandleFunc("GET /api/v1/audit", func(w http.ResponseWriter, r *http.Request) {
		if principal, err := options.Authenticate(r); err != nil || principal == "" {
			writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
			return
		}
		limit := 100
		if raw := r.URL.Query().Get("limit"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, requestID(r))
				return
			}
			limit = parsed
		}
		records, err := options.Service.List(r.Context(), platformaudit.Filter{ResourceID: r.URL.Query().Get("resourceId"), Limit: limit})
		if err != nil {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, requestID(r))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"audit": records}})
	})
	return nil
}
