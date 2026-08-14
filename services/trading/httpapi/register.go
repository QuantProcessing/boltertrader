// Package httpapi is the trading service's browser-facing HTTP surface: manual
// product orders, the command ledger, reservations, positions, the accounting
// ledger and P&L, activity and fills projections, and the public catalog /
// market-data / realtime reads.
//
// It owns no authentication of its own. The caller resolves the principal and
// hands it in — today that caller is the backend auth edge mounting these
// routes in-process, and after the service splits out it becomes a service
// token plus an X-Bolter-Principal header. Keeping the principal an injected
// function is what lets that swap happen without touching a single handler.
package httpapi

import "net/http"

// Options names the four route groups this surface can mount. A nil group is
// simply not registered, so a caller that has not wired (say) public data yet
// still gets a working service instead of a construction error.
type Options struct {
	Audit       *AuditRouteOptions
	PublicData  *PublicDataAPI
	Trading     *TradingOptions
	Projections *BrowserProjectionRoutesOptions
}

// The route groups this surface can mount. They are named here rather than
// inline in Register so the set of routes the service serves can be read
// without calling it — the auth edge's proxy forwards exactly this surface,
// and a table it cannot compare against is a table that silently drifts.
var (
	auditRoutes = []string{"GET /api/v1/audit"}

	publicDataRoutes = []string{
		"GET /api/v1/system/status", "GET /api/v1/system/readiness",
		"GET /api/v1/catalog/venues", "GET /api/v1/catalog/instruments",
		"GET /api/v1/market-data", "GET /api/v1/candles", "GET /api/v1/realtime/stream",
	}

	tradingRoutes = []string{
		"POST /api/v1/reservations", "GET /api/v1/reservations", "DELETE /api/v1/reservations/{reservationId}",
		"POST /api/v1/commands", "GET /api/v1/commands/{commandId}",
		"GET /api/v1/positions", "GET /api/v1/ledger", "GET /api/v1/pnl",
	}

	manualRoutes = []string{
		"GET /api/v1/products/manual/venues", "POST /api/v1/products/manual/execution-previews",
		"POST /api/v1/products/manual/commands", "GET /api/v1/products/manual/orders",
		"GET /api/v1/products/manual/orders/{orderId}",
	}

	projectionRoutes = []string{
		"GET /api/v1/accounts/{accountId}/runtimes", "GET /api/v1/commands",
		"GET /api/v1/fills", "GET /api/v1/activity",
	}
)

// Routes reports every route this service serves when every group is wired.
// A deployment missing a group serves fewer; none serves more.
func Routes() []string {
	all := make([]string, 0, 24)
	all = append(all, auditRoutes...)
	all = append(all, publicDataRoutes...)
	all = append(all, tradingRoutes...)
	all = append(all, manualRoutes...)
	all = append(all, projectionRoutes...)
	return all
}

// Register mounts every configured group on mux and reports the routes it now
// owns. The caller needs that list because it is the one deciding what the
// browser-facing surface is: it mounts its own routes alongside these and has
// to know which patterns are already taken.
func Register(mux *http.ServeMux, options Options) ([]string, error) {
	owned := make([]string, 0, 24)
	if options.Audit != nil {
		if err := RegisterAuditRoute(mux, *options.Audit); err != nil {
			return nil, err
		}
		owned = append(owned, auditRoutes...)
	}
	if options.PublicData != nil {
		if err := options.PublicData.Register(mux); err != nil {
			return nil, err
		}
		owned = append(owned, publicDataRoutes...)
	}
	if options.Trading != nil {
		if err := RegisterTradingRoutes(mux, *options.Trading); err != nil {
			return nil, err
		}
		owned = append(owned, tradingRoutes...)
		// The manual product rides on the same registration but only appears
		// when it has both a catalog to validate against and a service to
		// execute with; RegisterTradingRoutes makes the same judgement, so the
		// ownership list has to agree with it or a mounted route goes unclaimed.
		if options.Trading.Catalog != nil && options.Trading.Manual != nil {
			owned = append(owned, manualRoutes...)
		}
	}
	if options.Projections != nil {
		if err := RegisterBrowserProjectionRoutes(mux, *options.Projections); err != nil {
			return nil, err
		}
		owned = append(owned, projectionRoutes...)
	}
	return owned, nil
}
