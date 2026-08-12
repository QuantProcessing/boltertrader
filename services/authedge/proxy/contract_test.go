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
	sort.Strings(want)

	var got []string
	for _, route := range Routes() {
		if route.Upstream != Gateway {
			continue
		}
		got = append(got, route.Method+" "+route.UpstreamPath())
	}
	sort.Strings(got)

	assertSameRoutes(t, "gateway account lifecycle", want, got)
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
		Trading:  Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken, ModeHeader: TradingModeHeader},
		Gateway:  Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken, ModeHeader: GatewayModeHeader},
		Sessions: &fakeSessions{principal: session.Principal{ID: "browser-session", Mode: "test"}},
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
