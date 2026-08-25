package proxy

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
)

// The routes the auth edge serves itself. Everything else on the browser
// surface is forwarded, so these are the only patterns absent from the table.
var authEdgeOwned = []string{
	"POST /api/v1/auth/login",
	"GET /api/v1/auth/session",
	"DELETE /api/v1/auth/session",
	"GET /api/v1/me",
}

// The forwarding table names an upstream path for every route, and a path the
// upstream does not serve is a 404 nobody notices until a browser hits it.
// Nothing sits between the two any more, so this is what keeps them from
// drifting: the table is compared against what the upstreams declare they mount.
func TestTableMatchesWhatTradingServes(t *testing.T) {
	t.Parallel()
	want := append([]string(nil), tradinghttp.Routes()...)
	sort.Strings(want)

	var got []string
	for _, route := range Routes() {
		if route.Upstream != Trading {
			continue
		}
		if route.Target != "" {
			t.Errorf("%s rewrites onto %q; trading serves the browser path unchanged", route.Pattern(), route.Target)
		}
		got = append(got, route.Method+" "+route.UpstreamPath())
	}
	sort.Strings(got)

	assertSameRoutes(t, "trading", want, got)
}

func TestTableMatchesWhatTheGatewayServes(t *testing.T) {
	t.Parallel()
	var want []string
	for _, route := range gatewayhttp.InternalAccountLifecycleRoutes() {
		want = append(want, route.Method+" "+route.Pattern)
	}
	for _, route := range gatewayhttp.InternalPublicMarketRoutes() {
		want = append(want, route.Method+" "+route.Pattern)
	}
	sort.Strings(want)

	var got []string
	for _, route := range Routes() {
		if route.Upstream != Gateway {
			continue
		}
		got = append(got, route.Method+" "+route.UpstreamPath())
	}
	sort.Strings(got)

	assertSameRoutes(t, "gateway browser surface", want, got)
}

// A gateway route reachable from the browser must land on the internal plane.
// The public plane is a different surface with a different authentication story
// and no account lifecycle at all; forwarding there would be a silent downgrade.
func TestGatewayRoutesStayOnTheInternalPlane(t *testing.T) {
	t.Parallel()
	for _, route := range Routes() {
		if route.Upstream != Gateway {
			continue
		}
		if !strings.HasPrefix(route.UpstreamPath(), "/internal/v1/") {
			t.Errorf("%s targets %q outside the internal plane", route.Pattern(), route.UpstreamPath())
		}
	}
}

// The browser surface is the union of what the auth edge serves and what it
// forwards, and the two must not overlap: a forwarded route shadowing
// /api/v1/auth/login would hand the password to an upstream instead of
// terminating it here. Registering a duplicate pattern panics, so mounting the
// table beside the owned routes is the assertion.
func TestForwardedRoutesDoNotShadowOwnedRoutes(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	for _, pattern := range authEdgeOwned {
		mux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
	proxy, err := New(Options{
		Trading:  Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken},
		Sessions: &fakeSessions{principal: session.Principal{ID: "browser-session"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mounted, err := proxy.Register(mux)
	if err != nil {
		t.Fatal(err)
	}
	if len(mounted) != len(Routes()) {
		t.Fatalf("mounted %d routes, table has %d", len(mounted), len(Routes()))
	}
}

// Every Public route must be declared public by the upstream that serves it.
// Those declarations are the outer bound on what may be reached without a
// session, and keep the auth edge from widening the carve-out on its own.
func TestPublicRoutesAreDeclaredPublicData(t *testing.T) {
	t.Parallel()
	allowed := make(map[string]struct{})
	for _, pattern := range tradinghttp.PublicDataRoutes() {
		allowed[pattern] = struct{}{}
	}
	for _, upstreamRoute := range gatewayhttp.InternalPublicMarketRoutes() {
		for _, route := range Routes() {
			if route.Upstream == Gateway && route.Method == upstreamRoute.Method && route.UpstreamPath() == upstreamRoute.Pattern {
				allowed[route.Pattern()] = struct{}{}
			}
		}
	}
	for _, route := range Routes() {
		if !route.Public {
			continue
		}
		if _, ok := allowed[route.Pattern()]; !ok {
			t.Errorf("%s is Public but its upstream does not classify it as public data", route.Pattern())
		}
	}
}

// A Public route must be a GET. The carve-out in handler already tests the
// method at request time, so a POST marked Public would never actually skip
// authorize — which is exactly why this belongs in a test: the table would
// read as though writes were open while the code quietly disagreed, and the
// next person to touch either side would have to guess which one was the rule.
func TestPublicRoutesAreReads(t *testing.T) {
	t.Parallel()
	for _, route := range Routes() {
		if route.Public && route.Method != http.MethodGet {
			t.Errorf("%s is Public but not a GET", route.Pattern())
		}
	}
}

// The one socket is public at connection level. Authentication is evaluated
// per topic so anonymous market viewers can connect while account.events is
// rejected with AUTH_REQUIRED.
func TestRealtimeWebSocketIsPublicAndOwnedByTrading(t *testing.T) {
	t.Parallel()
	const pattern = "GET /api/v1/realtime/ws"
	found := false
	for _, route := range Routes() {
		if route.Pattern() != pattern {
			continue
		}
		found = true
		if !route.Public || !route.WebSocket || route.Upstream != Trading {
			t.Errorf("%s must be a public Trading websocket: %+v", pattern, route)
		}
	}
	if !found {
		t.Fatalf("%s is not in the forwarding table", pattern)
	}
}

// Nothing outside the trading service's public-data group is Public. Stated as a count so that a
// Public flag landing on a route this file has never heard of still fails.
func TestPublicRoutesAreExactlyTheElevenExpected(t *testing.T) {
	t.Parallel()
	want := []string{
		"GET /api/v1/candles",
		"GET /api/v1/catalog/instruments",
		"GET /api/v1/catalog/venues",
		"GET /api/v1/funding-board",
		"GET /api/v1/funding-history",
		"GET /api/v1/market-data",
		"GET /api/v1/products/arbitrage/opportunities",
		"GET /api/v1/realtime/ws",
		"GET /api/v1/system/readiness",
		"GET /api/v1/system/status",
		"GET /api/v1/venues/connectivity",
	}
	var got []string
	for _, route := range Routes() {
		if route.Public {
			got = append(got, route.Pattern())
		}
	}
	sort.Strings(got)

	// Spelled out rather than run through assertSameRoutes: that helper phrases
	// both directions as an upstream disagreeing with the table, and here both
	// sides of the comparison are this package's own.
	for _, route := range routesMissingFrom(want, got) {
		t.Errorf("%s should be reachable without a session but is not Public", route)
	}
	for _, route := range routesMissingFrom(got, want) {
		t.Errorf("%s is Public; the carve-out was not meant to include it", route)
	}
}

func assertSameRoutes(t *testing.T, upstream string, want, got []string) {
	t.Helper()
	for _, route := range routesMissingFrom(want, got) {
		t.Errorf("%s serves %s but the auth edge does not forward it", upstream, route)
	}
	for _, route := range routesMissingFrom(got, want) {
		t.Errorf("the auth edge forwards %s but %s does not serve it", route, upstream)
	}
}

func routesMissingFrom(from, minus []string) []string {
	present := make(map[string]struct{}, len(minus))
	for _, value := range minus {
		present[value] = struct{}{}
	}
	var out []string
	for _, value := range from {
		if _, ok := present[value]; !ok {
			out = append(out, value)
		}
	}
	return out
}
