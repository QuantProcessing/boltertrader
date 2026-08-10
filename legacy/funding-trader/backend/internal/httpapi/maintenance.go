package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/maintenance"
)

type MaintenanceConfig struct {
	Mode    string
	Version string
	Build   string
	Reason  string
	Logger  *slog.Logger
}

func NewMaintenance(cfg MaintenanceConfig) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	gate := maintenance.NewGate()
	gate.Enter(cfg.Reason)
	server := &Server{
		cfg: ServerConfig{
			Mode:            cfg.Mode,
			Version:         cfg.Version,
			Build:           cfg.Build,
			MaintenanceGate: gate,
			Logger:          cfg.Logger,
		},
		started:       time.Now().UTC(),
		mux:           http.NewServeMux(),
		loginThrottle: newLoginThrottle(nil),
	}
	server.mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, request *http.Request) {
		server.writeJSON(w, request, http.StatusOK, map[string]any{
			"data": map[string]any{
				"status":      "live",
				"maintenance": true,
				"mode":        cfg.Mode,
				"version":     cfg.Version,
				"build":       cfg.Build,
			},
			"meta": meta(request),
		})
	})
	server.mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, request *http.Request) {
		server.writeJSON(w, request, http.StatusServiceUnavailable, map[string]any{
			"data": map[string]any{
				"ready":       false,
				"maintenance": true,
				"reason":      gate.Snapshot().Reason,
			},
			"meta": meta(request),
		})
	})
	return server.withRequestID(gate.Wrap(server.mux))
}
