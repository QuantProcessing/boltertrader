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

// Register mounts every configured group on mux and reports the routes it now
// owns. The caller needs that list because the frozen product contract also
// names routes nobody implements yet: whoever holds the contract registers the
// remainder as scaffolds, and must not shadow what was just mounted here.
func Register(mux *http.ServeMux, options Options) ([]string, error) {
	owned := make([]string, 0, 24)
	if options.Audit != nil {
		if err := RegisterAuditRoute(mux, *options.Audit); err != nil {
			return nil, err
		}
		owned = append(owned, "GET /api/v1/audit")
	}
	if options.PublicData != nil {
		if err := options.PublicData.Register(mux); err != nil {
			return nil, err
		}
		owned = append(owned,
			"GET /api/v1/system/status", "GET /api/v1/system/readiness",
			"GET /api/v1/catalog/venues", "GET /api/v1/catalog/instruments",
			"GET /api/v1/market-data", "GET /api/v1/realtime/stream",
		)
	}
	if options.Trading != nil {
		if err := RegisterTradingRoutes(mux, *options.Trading); err != nil {
			return nil, err
		}
		owned = append(owned,
			"POST /api/v1/reservations", "GET /api/v1/reservations", "DELETE /api/v1/reservations/{reservationId}",
			"POST /api/v1/commands", "GET /api/v1/commands/{commandId}",
			"GET /api/v1/positions", "GET /api/v1/ledger", "GET /api/v1/pnl",
		)
		// The manual product rides on the same registration but only appears
		// when it has both a catalog to validate against and a service to
		// execute with; RegisterTradingRoutes makes the same judgement, so the
		// ownership list has to agree with it or a live route gets scaffolded.
		if options.Trading.Catalog != nil && options.Trading.Manual != nil {
			owned = append(owned,
				"GET /api/v1/products/manual/venues", "POST /api/v1/products/manual/execution-previews",
				"POST /api/v1/products/manual/commands", "GET /api/v1/products/manual/orders",
				"GET /api/v1/products/manual/orders/{orderId}",
			)
		}
	}
	if options.Projections != nil {
		if err := RegisterBrowserProjectionRoutes(mux, *options.Projections); err != nil {
			return nil, err
		}
		owned = append(owned,
			"GET /api/v1/accounts/{accountId}/runtimes", "GET /api/v1/commands",
			"GET /api/v1/fills", "GET /api/v1/activity",
		)
	}
	return owned, nil
}
