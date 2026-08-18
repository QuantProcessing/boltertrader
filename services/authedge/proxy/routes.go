package proxy

import "net/http"

// UpstreamName identifies which service answers a route.
type UpstreamName string

const (
	// Trading is the manual-trading service: the command ledger, reservations,
	// positions, the accounting ledger, the projections, and the public
	// catalog / market-data / realtime reads it fronts for the gateway.
	Trading UpstreamName = "trading"
	// Gateway is the shared capability layer. Only the account lifecycle is
	// reachable through the browser, and it lives on the gateway's internal
	// plane because it accepts credential material.
	Gateway UpstreamName = "gateway"
)

// Route is one browser-facing path and the upstream path it becomes.
//
// Target is a template in the same shape as Path, with the same wildcard
// names, because the upstream path is rebuilt from the values the mux matched
// rather than patched out of the raw URL. An empty Target means the upstream
// serves the route at the path the browser used.
type Route struct {
	Method   string
	Path     string
	Upstream UpstreamName
	Target   string
	// Stream marks a response that arrives over time rather than all at once.
	// Such a route gets no response deadline — a deadline is what would cut a
	// healthy event stream off mid-session.
	Stream bool
	// Public marks a read that carries no identity at all, so a browser with no
	// session may make it. Every venue behind these routes publishes the same
	// numbers to anyone who asks — a book, a candle, a funding rate — and a
	// login that guards a public number protects nothing while costing the
	// product the only screen worth opening on a fresh install.
	//
	// It is a licence to read, not a hole in the boundary. The carve-out in
	// handler applies only to a safe method and only when no session cookie
	// arrived at all, so a mutation on a public path still goes through
	// authorize, a stale cookie is still reported stale rather than silently
	// downgraded to anonymous, and a signed-in reader still forwards their
	// principal. What reaches the upstream in the anonymous case is a valid
	// service token with an empty identity, which servicetoken.Wrap passes
	// through for the handler to judge for itself.
	Public bool
}

// UpstreamPath is the template the upstream serves this route at.
func (route Route) UpstreamPath() string {
	if route.Target == "" {
		return route.Path
	}
	return route.Target
}

// Pattern is the route in http.ServeMux's vocabulary.
func (route Route) Pattern() string { return route.Method + " " + route.Path }

// routes is the whole forwarding surface of the auth edge, and the only place
// that knows which service owns what.
//
// The table is explicit rather than a set of prefix rules for one concrete
// reason: `/api/v1/accounts/{accountId}/runtimes` is a trading projection,
// while every other `/api/v1/accounts` route is the gateway's account
// lifecycle. No prefix separates them, and guessing wrong sends credential
// material to the wrong service.
var routes = []Route{
	// Trading: everything the manual product and its ledgers expose.
	{Method: http.MethodGet, Path: "/api/v1/audit", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/system/status", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/system/readiness", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/catalog/venues", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/catalog/instruments", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/market-data", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/candles", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/funding-board", Upstream: Trading, Public: true},
	{Method: http.MethodGet, Path: "/api/v1/funding-history", Upstream: Trading, Public: true},
	// Eight of the trading service's nine public-data routes are Public here.
	// This is the ninth, and it is not: realtime replays a principal-scoped
	// buffer and answers 401 on an empty identity itself, so marking it would
	// widen the unauthenticated surface to a route that would only refuse.
	{Method: http.MethodGet, Path: "/api/v1/realtime/stream", Upstream: Trading, Stream: true},
	{Method: http.MethodPost, Path: "/api/v1/reservations", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/reservations", Upstream: Trading},
	{Method: http.MethodDelete, Path: "/api/v1/reservations/{reservationId}", Upstream: Trading},
	{Method: http.MethodPost, Path: "/api/v1/commands", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/commands", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/commands/{commandId}", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/positions", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/ledger", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/pnl", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/fills", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/activity", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/accounts/{accountId}/runtimes", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/products/manual/venues", Upstream: Trading},
	{Method: http.MethodPost, Path: "/api/v1/products/manual/execution-previews", Upstream: Trading},
	{Method: http.MethodPost, Path: "/api/v1/products/manual/commands", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/products/manual/orders", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/products/manual/orders/{orderId}", Upstream: Trading},

	{Method: http.MethodGet, Path: "/api/v1/products/arbitrage/opportunities", Upstream: Trading},
	{Method: http.MethodPost, Path: "/api/v1/products/arbitrage/execution-previews", Upstream: Trading},
	{Method: http.MethodPost, Path: "/api/v1/products/arbitrage/commands", Upstream: Trading},
	{Method: http.MethodGet, Path: "/api/v1/products/arbitrage/positions", Upstream: Trading},

	// Gateway: the account lifecycle, rewritten onto the internal plane.
	{Method: http.MethodGet, Path: "/api/v1/accounts", Upstream: Gateway, Target: "/internal/v1/accounts"},
	{Method: http.MethodPost, Path: "/api/v1/accounts", Upstream: Gateway, Target: "/internal/v1/accounts"},
	{Method: http.MethodGet, Path: "/api/v1/accounts/{accountId}", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}"},
	{Method: http.MethodPatch, Path: "/api/v1/accounts/{accountId}", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}"},
	{Method: http.MethodDelete, Path: "/api/v1/accounts/{accountId}", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}"},
	{Method: http.MethodPost, Path: "/api/v1/accounts/{accountId}/validate", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}/validate"},
	{Method: http.MethodPost, Path: "/api/v1/accounts/{accountId}/rotate", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}/rotate"},
	{Method: http.MethodPost, Path: "/api/v1/accounts/{accountId}/disable", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}/disable"},
	{Method: http.MethodPost, Path: "/api/v1/accounts/{accountId}/drain", Upstream: Gateway, Target: "/internal/v1/accounts/{accountId}/drain"},
}

// Routes reports the forwarding table. The copy is deliberate: the table is
// the contract between three services, and a caller that could mutate it could
// silently redirect credential writes.
func Routes() []Route { return append([]Route(nil), routes...) }
