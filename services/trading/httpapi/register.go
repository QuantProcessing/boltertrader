// Package httpapi is the trading service's browser-facing HTTP surface: manual
// product orders, the command ledger, positions, the accounting
// ledger and P&L, activity and fills projections, system health.
//
// Authentication and mutation authorization are injected by the host. Production
// requests arrive through Auth Edge with a service token and attested principal.
package httpapi

import "net/http"

// Options names the four route groups this surface can mount. A nil group is
// simply not registered, so a caller that has not wired (say) public data yet
// still gets a working service instead of a construction error.
type Options struct {
	Audit         *AuditRouteOptions
	PublicData    *PublicDataAPI
	Trading       *TradingOptions
	Projections   *BrowserProjectionRoutesOptions
	Notifications *NotificationRoutesOptions
}

// The route groups this surface can mount. They are named here rather than
// inline in Register so the set of routes the service serves can be read
// without calling it — the auth edge's proxy forwards exactly this surface,
// and a table it cannot compare against is a table that silently drifts.
var (
	auditRoutes = []string{"GET /api/v1/audit"}

	publicDataRoutes = []string{
		"GET /api/v1/system/status", "GET /api/v1/system/readiness",
	}

	tradingRoutes = []string{
		"GET /api/v1/trading/realtime/ws",
		"GET /api/v1/commands/{commandId}",
		"GET /api/v1/positions", "GET /api/v1/ledger", "GET /api/v1/pnl",
		"GET /api/v1/notifications", "PUT /api/v1/notifications/{provider}",
		"DELETE /api/v1/notifications/{provider}", "POST /api/v1/notifications/{provider}/test",
	}

	manualRoutes = []string{
		"GET /api/v1/products/manual/venues", "POST /api/v1/products/manual/commands", "GET /api/v1/products/manual/orders",
		"GET /api/v1/products/manual/orders/{orderId}",
	}

	projectionRoutes = []string{
		"GET /api/v1/accounts/{accountId}/runtimes", "GET /api/v1/commands",
		"GET /api/v1/balances", "GET /api/v1/account-positions", "GET /api/v1/portfolio/positions", "GET /api/v1/fills", "GET /api/v1/activity",
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
	// Notification channels are mounted by the trading host alongside its
	// projections; keep the contract visible even when a test omits the group.
	all = append(all, notificationRoutes...)
	return all
}

// PublicDataRoutes reports Trading-owned reads that may be anonymous.
//
// "Public data" is a statement about the *data*, not about who may ask: every
// route here is mounted behind the same service token as the rest. The auth edge is the
// only thing that decides a request may arrive unauthenticated, and this list
// is the outer bound on what it is allowed to decide that about — its contract
// test compares its own carve-out against this, so widening one without the
// other fails rather than drifts.
func PublicDataRoutes() []string {
	routes := append([]string(nil), publicDataRoutes...)
	return routes
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
	if options.Notifications != nil {
		if err := RegisterNotificationRoutes(mux, *options.Notifications); err != nil {
			return nil, err
		}
		owned = append(owned, notificationRoutes...)
	}
	return owned, nil
}
