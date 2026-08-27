package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/QuantProcessing/boltertrader/internal/httpx"
	platformsystem "github.com/QuantProcessing/boltertrader/internal/system"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
)

// CatalogProjection and FundingBoardProjection are business dependencies of
// the resident arbitrage projection. They remain in Trading even though the
// browser now reads their public snapshots directly from Gateway.
type CatalogProjection interface {
	Snapshot(context.Context) (catalog.Snapshot, error)
	Status() catalog.Status
}

type FundingBoardProjection interface {
	Get(context.Context) (marketdata.BoardSnapshot, error)
}

type ReadinessProjection interface {
	Status() platformsystem.Snapshot
	Readiness() platformsystem.Snapshot
}

type ArbitrageRealtimeProjection interface {
	Subscribe(context.Context, int) (arbitrageProjectionSnapshot, bool, <-chan arbitrageProjectionSnapshot, func(), error)
}

type RealtimeObserver interface {
	ObserveRealtimeResync(topic realtimewire.Topic)
}

type PublicDataDependencies struct {
	Readiness        ReadinessProjection
	RealtimeObserver RealtimeObserver
	Arbitrage        ArbitrageRealtimeProjection
}

type PublicDataAPI struct{ dependencies PublicDataDependencies }

func NewPublicDataAPI(dependencies PublicDataDependencies) (*PublicDataAPI, error) {
	if dependencies.Readiness == nil || dependencies.Arbitrage == nil {
		return nil, httpx.ConfigErr("trading public dependencies are incomplete")
	}
	return &PublicDataAPI{dependencies: dependencies}, nil
}

// Register mounts only Trading-owned public state. Venue facts, catalog,
// funding and connectivity terminate at Gateway and therefore cannot drift
// through a second cache or rate limiter here.
func (api *PublicDataAPI) Register(mux *http.ServeMux) error {
	if mux == nil {
		return fmt.Errorf("public-data mux is required")
	}
	for _, route := range []string{
		"GET /api/v1/system/status",
		"GET /api/v1/system/readiness",
		"GET /api/v1/realtime/ws",
	} {
		route := route
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) { api.Handle(w, r, route) })
	}
	return nil
}

func (api *PublicDataAPI) Handle(w http.ResponseWriter, r *http.Request, route string) bool {
	switch route {
	case "GET /api/v1/system/status":
		api.system(w, r, false)
	case "GET /api/v1/system/readiness":
		api.system(w, r, true)
	case "GET /api/v1/realtime/ws":
		api.unifiedRealtime(w, r)
	default:
		return false
	}
	return true
}

func (api *PublicDataAPI) system(w http.ResponseWriter, r *http.Request, readiness bool) {
	if len(r.URL.Query()) != 0 {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
		return
	}
	var snapshot platformsystem.Snapshot
	status := http.StatusOK
	if readiness {
		snapshot = api.dependencies.Readiness.Readiness()
		if !snapshot.Ready {
			status = http.StatusServiceUnavailable
		}
	} else {
		snapshot = api.dependencies.Readiness.Status()
	}
	httpx.WriteJSON(w, status, map[string]any{"meta": httpx.Meta(r), "data": snapshot})
}
