package proxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeSessions struct {
	principal session.Principal
	err       error
	csrfErr   error
	csrfSeen  string
}

func (fake *fakeSessions) Current(_ context.Context, _ string) (session.Result, error) {
	if fake.err != nil {
		return session.Result{}, fake.err
	}
	return session.Result{Principal: fake.principal}, nil
}

func (fake *fakeSessions) RequireCSRF(_ context.Context, _ string, csrf string) error {
	fake.csrfSeen = csrf
	return fake.csrfErr
}

// capture records what an upstream actually received, which is the only place
// the forgery-stripping and header-injection rules can be observed.
type capture struct {
	method  string
	path    string
	escaped string
	query   string
	header  http.Header
	body    string
}

// harness wires a proxy in front of two recording upstreams and serves it, so
// tests exercise the same path a browser would.
type harness struct {
	proxy    *httptest.Server
	sessions *fakeSessions
	trading  *capture
	gateway  *capture
}

func newHarness(t *testing.T, configure func(*Options)) *harness {
	t.Helper()
	h := &harness{
		sessions: &fakeSessions{principal: session.Principal{ID: "browser-session"}},
		trading:  &capture{},
		gateway:  &capture{},
	}
	tradingUpstream := httptest.NewServer(recorder(h.trading, nil))
	gatewayUpstream := httptest.NewServer(recorder(h.gateway, nil))
	t.Cleanup(tradingUpstream.Close)
	t.Cleanup(gatewayUpstream.Close)

	options := Options{
		Trading:  Upstream{BaseURL: mustURL(t, tradingUpstream.URL), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, gatewayUpstream.URL), Token: testToken + "-gateway"},
		Sessions: h.sessions,
	}
	if configure != nil {
		configure(&options)
	}
	proxy, err := New(options)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	if _, err := proxy.Register(mux); err != nil {
		t.Fatalf("Register: %v", err)
	}
	h.proxy = httptest.NewServer(mux)
	t.Cleanup(h.proxy.Close)
	return h
}

func recorder(into *capture, respond http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		into.method, into.path, into.escaped = r.Method, r.URL.Path, r.URL.EscapedPath()
		into.query, into.header, into.body = r.URL.RawQuery, r.Header.Clone(), string(body)
		if respond != nil {
			respond(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

// do sends a request through the proxy with a session cookie attached.
func (h *harness) do(t *testing.T, method, path string, headers map[string]string, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, h.proxy.URL+path, reader)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: "session-token"})
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := h.proxy.Client().Do(request)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// doAnonymous sends a request with no session cookie at all, which is the only
// state in which the public carve-out applies.
func (h *harness) doAnonymous(t *testing.T, method, path string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, h.proxy.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	response, err := h.proxy.Client().Do(request)
	if err != nil {
		t.Fatalf("do %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	return response
}

// publicRoutes is the carve-out as the table declares it. Tests walk it rather
// than naming paths, so a route marked Public later is exercised here whether
// or not anyone remembers to add a case.
func publicRoutes(t *testing.T) []Route {
	t.Helper()
	var out []Route
	for _, route := range Routes() {
		if !route.Public {
			continue
		}
		if strings.Contains(route.Path, "{") {
			t.Fatalf("%s is Public and has a wildcard; these tests request route.Path verbatim", route.Pattern())
		}
		out = append(out, route)
	}
	if len(out) == 0 {
		t.Fatal("no Public routes; the carve-out is not wired")
	}
	return out
}

func errorCode(t *testing.T, response *http.Response) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	return envelope.Error.Code
}

// TestForgedIdentityHeadersAreStripped is the reason this package exists: a
// browser must not be able to name itself.
func TestForgedIdentityHeadersAreStripped(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.principal = session.Principal{ID: "real-principal"}

	response := h.do(t, http.MethodGet, "/api/v1/positions", map[string]string{
		"Authorization":         "Bearer stolen-service-token",
		"X-Bolter-Principal":    "someone-else",
		tradingModeHeader:       "live",
		gatewayModeHeader:       "live",
		"X-Forwarded-For":       "10.0.0.1",
		"X-Some-Unknown-Header": "smuggled",
	}, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if got := h.trading.header.Get("Authorization"); got != "Bearer "+testToken {
		t.Fatalf("Authorization = %q, want the service token", got)
	}
	if got := h.trading.header.Get(principalHeader); got != "real-principal" {
		t.Fatalf("principal = %q, want the session's principal", got)
	}
	if got := h.trading.header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie = %q, want the session token to stop at the edge", got)
	}
	// The two mode headers are in here rather than in a test of their own: no
	// upstream reads either one now, so there is nothing left to assert about
	// them except that a browser still cannot smuggle one through. That is the
	// same claim this test makes about every other header it sends.
	for _, name := range []string{tradingModeHeader, gatewayModeHeader, "X-Forwarded-For", "X-Some-Unknown-Header"} {
		if got := h.trading.header.Get(name); got != "" {
			t.Fatalf("%s = %q, want an allowlist that drops it", name, got)
		}
	}
}

// TestEachUpstreamGetsItsOwnServiceToken guards the one thing that is still per
// upstream. The two services authenticate the edge with different credentials,
// and handing one the other's token would fail as an authorization error at the
// far end — the shape of failure that reads like "the gateway is down" rather
// than "the proxy crossed two wires".
func TestEachUpstreamGetsItsOwnServiceToken(t *testing.T) {
	h := newHarness(t, nil)

	h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if got := h.trading.header.Get(authorizationHeader); got != "Bearer "+testToken {
		t.Fatalf("trading Authorization = %q, want trading's token", got)
	}

	h.do(t, http.MethodGet, "/api/v1/accounts", nil, "")
	if got := h.gateway.header.Get(authorizationHeader); got != "Bearer "+testToken+"-gateway" {
		t.Fatalf("gateway Authorization = %q, want the gateway's token", got)
	}
}

// TestAccountRoutesSplitByPath pins the one ambiguity a prefix router would get
// wrong — and getting it wrong sends credential material to the wrong service.
func TestAccountRoutesSplitByPath(t *testing.T) {
	h := newHarness(t, nil)

	h.do(t, http.MethodGet, "/api/v1/accounts/acct-1/runtimes", nil, "")
	if h.trading.path != "/api/v1/accounts/acct-1/runtimes" {
		t.Fatalf("runtimes went to trading as %q", h.trading.path)
	}
	if h.gateway.path != "" {
		t.Fatalf("runtimes also reached the gateway as %q", h.gateway.path)
	}

	h.do(t, http.MethodGet, "/api/v1/accounts/acct-1", nil, "")
	if h.gateway.path != "/internal/v1/accounts/acct-1" {
		t.Fatalf("account read went to the gateway as %q, want the internal plane", h.gateway.path)
	}
}

func TestGatewayLifecycleRewritesOntoInternalPlane(t *testing.T) {
	for _, testCase := range []struct{ method, browser, upstream string }{
		{http.MethodPost, "/api/v1/accounts", "/internal/v1/accounts"},
		{http.MethodDelete, "/api/v1/accounts/acct-1", "/internal/v1/accounts/acct-1"},
	} {
		t.Run(testCase.method+" "+testCase.browser, func(t *testing.T) {
			h := newHarness(t, nil)
			response := h.do(t, testCase.method, testCase.browser, map[string]string{csrfHeader: "csrf"}, `{}`)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if h.gateway.path != testCase.upstream {
				t.Fatalf("upstream path = %q, want %q", h.gateway.path, testCase.upstream)
			}
			if h.gateway.method != testCase.method {
				t.Fatalf("upstream method = %q, want %q", h.gateway.method, testCase.method)
			}
		})
	}
}

func TestRetiredGatewayLifecycleRoutesAreNotForwarded(t *testing.T) {
	for _, testCase := range []struct {
		method string
		path   string
	}{
		{http.MethodPatch, "/api/v1/accounts/acct-1"},
		{http.MethodPost, "/api/v1/accounts/acct-1/validate"},
		{http.MethodPost, "/api/v1/accounts/acct-1/rotate"},
		{http.MethodPost, "/api/v1/accounts/acct-1/drain"},
	} {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			h := newHarness(t, nil)
			response := h.do(t, testCase.method, testCase.path, map[string]string{csrfHeader: "csrf"}, `{}`)
			if response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotFound {
				t.Fatalf("status = %d, want retired route refusal", response.StatusCode)
			}
			if h.gateway.path != "" {
				t.Fatalf("retired route reached Gateway as %q", h.gateway.path)
			}
		})
	}
}

// TestPathValuesRoundTripEscaping checks that a matched value keeps its
// escaping upstream. Rebuilding the path is what makes this true; string
// surgery on the raw URL would let the separator through decoded.
func TestPathValuesRoundTripEscaping(t *testing.T) {
	h := newHarness(t, nil)

	h.do(t, http.MethodGet, "/api/v1/accounts/acct%2F..%2Fadmin", nil, "")
	if h.gateway.escaped != "/internal/v1/accounts/acct%2F..%2Fadmin" {
		t.Fatalf("escaped path = %q, want the separator to stay encoded", h.gateway.escaped)
	}
	if h.gateway.path != "/internal/v1/accounts/acct/../admin" {
		t.Fatalf("decoded path = %q", h.gateway.path)
	}
}

func TestQueryAndBodyAreForwarded(t *testing.T) {
	h := newHarness(t, nil)

	h.do(t, http.MethodGet, "/api/v1/commands?status=open&limit=25", nil, "")
	if h.trading.query != "status=open&limit=25" {
		t.Fatalf("query = %q", h.trading.query)
	}

	h.do(t, http.MethodPost, "/api/v1/products/manual/commands", map[string]string{
		csrfHeader:        "csrf",
		"Content-Type":    "application/json",
		"Idempotency-Key": "key-1",
		"If-Match":        `"rev-2"`,
	}, `{"kind":"place"}`)
	if h.trading.body != `{"kind":"place"}` {
		t.Fatalf("body = %q", h.trading.body)
	}
	for name, want := range map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "key-1", "If-Match": `"rev-2"`,
	} {
		if got := h.trading.header.Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestRequestIDIsMintedWhenAbsentAndPreservedWhenGiven(t *testing.T) {
	h := newHarness(t, nil)

	h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if h.trading.header.Get("X-Request-Id") == "" {
		t.Fatal("X-Request-Id was not minted")
	}

	h.do(t, http.MethodGet, "/api/v1/positions", map[string]string{"X-Request-Id": "req_given"}, "")
	if got := h.trading.header.Get("X-Request-Id"); got != "req_given" {
		t.Fatalf("X-Request-Id = %q, want the caller's", got)
	}
}

func TestMutationsRequireCSRF(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.csrfErr = session.ErrInvalidCSRF

	response := h.do(t, http.MethodPost, "/api/v1/products/manual/commands", map[string]string{csrfHeader: "forged"}, `{}`)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
	if code := errorCode(t, response); code != "CSRF_REQUIRED" {
		t.Fatalf("code = %q", code)
	}
	if h.sessions.csrfSeen != "forged" {
		t.Fatalf("csrf checked = %q, want the value from %s", h.sessions.csrfSeen, csrfHeader)
	}
	if h.trading.path != "" {
		t.Fatalf("request reached trading despite a failed CSRF check: %q", h.trading.path)
	}
}

// TestCSRFFailureThatIsNotCSRFReads401 keeps an expired session from being
// reported as a forbidden one; the browser's remedy differs.
func TestCSRFFailureThatIsNotCSRFReads401(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.csrfErr = session.ErrInvalidSession

	response := h.do(t, http.MethodPost, "/api/v1/products/manual/commands", nil, `{}`)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if code := errorCode(t, response); code != "AUTH_REQUIRED" {
		t.Fatalf("code = %q", code)
	}
}

func TestSafeMethodsSkipCSRF(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.csrfErr = session.ErrInvalidCSRF

	response := h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
}

func TestMissingOrInvalidSessionIsUnauthorized(t *testing.T) {
	h := newHarness(t, nil)

	request, _ := http.NewRequest(http.MethodGet, h.proxy.URL+"/api/v1/positions", nil)
	response, err := h.proxy.Client().Do(request)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no cookie: status = %d, want 401", response.StatusCode)
	}

	h.sessions.err = session.ErrInvalidSession
	if got := h.do(t, http.MethodGet, "/api/v1/positions", nil, "").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("invalid session: status = %d, want 401", got)
	}
	if h.trading.path != "" {
		t.Fatalf("an unauthenticated request reached trading: %q", h.trading.path)
	}
}

// A public read from a browser with no session at all is forwarded, not
// refused. This is the carve-out working: the venues behind these routes
// publish the same numbers to anyone who asks, and a login that guards a
// public number protects nothing while costing the product its first screen.
//
// The session service is armed to fail here on purpose. If authorize were
// still being consulted the request would 401, so a 200 is proof the carve-out
// skipped it rather than proof the fake happened to say yes.
func TestPublicRoutesAnswerWithoutASession(t *testing.T) {
	for _, route := range publicRoutes(t) {
		if route.WebSocket {
			continue // Exercised as an actual upgrade below, not as plain HTTP.
		}
		t.Run(route.Pattern(), func(t *testing.T) {
			h := newHarness(t, nil)
			h.sessions.err = session.ErrInvalidSession
			upstream, token := h.trading, testToken
			if route.Upstream == Gateway {
				upstream, token = h.gateway, testToken+"-gateway"
			}

			response := h.doAnonymous(t, route.Method, route.Path)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", response.StatusCode)
			}
			if upstream.path != route.UpstreamPath() {
				t.Fatalf("%s upstream saw %q, want %q", route.Upstream, upstream.path, route.UpstreamPath())
			}
			// The service token still goes: anonymous to the browser is not
			// anonymous to the upstream, which authenticates its caller either way.
			if got := upstream.header.Get(authorizationHeader); got != "Bearer "+token {
				t.Fatalf("Authorization = %q, want the service token", got)
			}
			// No identity header at all, rather than an empty one. An empty
			// assertion is indistinguishable from a real one to an upstream that
			// ever grows a reason to care.
			if _, present := upstream.header[principalHeader]; present {
				t.Fatalf("%s was sent for an anonymous read: %q", principalHeader, upstream.header.Get(principalHeader))
			}
		})
	}
}

func TestRealtimeWebSocketRelaysGatewayMarketUpgrade(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/market/ws" || r.URL.RawQuery != "" {
			t.Errorf("gateway websocket request=%s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if got := r.Header.Get(authorizationHeader); got != "Bearer "+testToken+"-gateway" {
			t.Errorf("gateway authorization=%q", got)
		}
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept upstream websocket: %v", err)
			return
		}
		defer connection.CloseNow()
		if err := wsjson.Write(r.Context(), connection, map[string]string{"eventType": "snapshot"}); err != nil {
			t.Errorf("write upstream websocket: %v", err)
		}
	}))
	defer upstream.Close()

	sessions := &fakeSessions{err: session.ErrInvalidSession}
	forwarder, err := New(Options{
		Trading:  Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken + "-gateway"},
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if _, err := forwarder.Register(mux); err != nil {
		t.Fatal(err)
	}
	edge := httptest.NewServer(mux)
	defer edge.Close()

	target := "ws" + strings.TrimPrefix(edge.URL, "http") + "/api/v1/market/realtime/ws"
	connection, response, err := websocket.Dial(context.Background(), target, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial auth edge websocket: %v", err)
	}
	defer connection.CloseNow()
	var event map[string]string
	if err := wsjson.Read(context.Background(), connection, &event); err != nil {
		t.Fatalf("read relayed websocket event: %v", err)
	}
	if event["eventType"] != "snapshot" {
		t.Fatalf("relayed event=%v", event)
	}
}

// A signed-in reader still asserts their identity on a public route. The
// carve-out is a floor, not a ceiling: dropping the principal here would make
// every reader anonymous to the upstream the moment a route went Public, which
// is a downgrade nobody asked for and nothing on screen would report.
func TestPublicRoutesStillCarryASignedInPrincipal(t *testing.T) {
	h := newHarness(t, nil)

	if got := h.do(t, http.MethodGet, "/api/v1/funding-board", nil, "").StatusCode; got != http.StatusOK {
		t.Fatalf("status = %d, want 200", got)
	}
	if got := h.gateway.header.Get(principalHeader); got != "browser-session" {
		t.Fatalf("%s = %q, want the session's principal", principalHeader, got)
	}
}

// A stale cookie on a public route is reported stale, not quietly downgraded.
//
// This is the third condition in the carve-out earning its place. Without it a
// reader whose session expired would keep seeing a market and silently stop
// seeing their own account — the worst failure available here, because nothing
// on the page would say why the numbers they came for went missing.
func TestPublicRoutesWithAStaleSessionAreUnauthorized(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.err = session.ErrInvalidSession

	response := h.do(t, http.MethodGet, "/api/v1/funding-board", nil, "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if got := errorCode(t, response); got != "AUTH_REQUIRED" {
		t.Fatalf("code = %q, want AUTH_REQUIRED", got)
	}
	if h.trading.path != "" || h.gateway.path != "" {
		t.Fatalf("a stale session reached an upstream: trading=%q gateway=%q", h.trading.path, h.gateway.path)
	}
}

// Public does not mean writable. No route in the table is both Public and a
// mutation — TestPublicRoutesAreReads keeps it that way — so this combination
// has to be built on purpose to be reached at all.
//
// It is still worth pinning. That clause is the one deciding whether "public"
// means readable or writable, and the table is one character away from needing
// it to hold.
func TestPublicDoesNotOpenWrites(t *testing.T) {
	upstream := &capture{}
	trading := httptest.NewServer(recorder(upstream, nil))
	defer trading.Close()

	proxy, err := New(Options{
		Trading:  Upstream{BaseURL: mustURL(t, trading.URL), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken},
		Sessions: &fakeSessions{principal: session.Principal{ID: "browser-session"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server := httptest.NewServer(proxy.handler(Route{
		Method: http.MethodPost, Path: "/api/v1/market-data", Upstream: Trading, Public: true,
	}))
	defer server.Close()

	response, err := server.Client().Post(server.URL+"/api/v1/market-data", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
	if upstream.path != "" {
		t.Fatalf("an anonymous write reached trading: %q", upstream.path)
	}
}

// TestNamelessPrincipalIsRejected stops a half-built principal from being
// forwarded. The session service can only return one of these by failing
// silently, and the upstreams cannot tell an empty principal header from a
// request that was never meant to carry one — so the edge refuses rather than
// asserting nobody.
//
// This used to check the mode attestation too, and rejected a principal whose
// mode was empty or unrecognised. There is no mode to attest now; the name is
// all a principal is.
func TestNamelessPrincipalIsRejected(t *testing.T) {
	h := newHarness(t, nil)
	h.sessions.principal = session.Principal{ID: ""}
	if got := h.do(t, http.MethodGet, "/api/v1/positions", nil, "").StatusCode; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", got)
	}
	if h.trading.path != "" {
		t.Fatalf("a nameless principal reached trading: %q", h.trading.path)
	}
}

// TestUpstreamSetCookieIsNotRelayed: an upstream has no business handing the
// browser a cookie, and relaying one would let it overwrite the session.
func TestUpstreamSetCookieIsNotRelayed(t *testing.T) {
	upstream := httptest.NewServer(recorder(&capture{}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Add("Set-Cookie", session.CookieName+"=attacker-controlled; Path=/")
		w.Header().Set("X-Upstream-Header", "kept")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusCreated)
	}))
	defer upstream.Close()

	h := newHarness(t, func(options *Options) {
		options.Trading.BaseURL = mustURL(t, upstream.URL)
	})
	response := h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", response.StatusCode)
	}
	if cookies := response.Header.Values("Set-Cookie"); len(cookies) != 0 {
		t.Fatalf("Set-Cookie relayed: %v", cookies)
	}
	if got := response.Header.Get("X-Upstream-Header"); got != "kept" {
		t.Fatalf("X-Upstream-Header = %q, want it relayed", got)
	}
}

func TestUpstreamUnavailableIsRetryableBadGateway(t *testing.T) {
	unreachable := httptest.NewServer(http.NotFoundHandler())
	address := unreachable.URL
	unreachable.Close()

	h := newHarness(t, func(options *Options) {
		options.Trading.BaseURL = mustURL(t, address)
	})
	response := h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
	if code := errorCode(t, response); code != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("code = %q", code)
	}
}

func TestLegacyRealtimeStreamIsNotForwarded(t *testing.T) {
	h := newHarness(t, nil)
	response := h.do(t, http.MethodGet, "/api/v1/realtime/stream", nil, "")
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func TestNonStreamRouteIsBounded(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	h := newHarness(t, func(options *Options) {
		options.Trading.BaseURL = mustURL(t, upstream.URL)
		options.RequestTimeout = 20 * time.Millisecond
	})
	response := h.do(t, http.MethodGet, "/api/v1/positions", nil, "")
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
}

func TestRegisterMountsEveryRoute(t *testing.T) {
	h := newHarness(t, nil)
	_ = h

	mux := http.NewServeMux()
	proxy, err := New(Options{
		Trading:  Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken},
		Sessions: &fakeSessions{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	registered, err := proxy.Register(mux)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(registered) != len(Routes()) {
		t.Fatalf("registered %d patterns, want %d", len(registered), len(Routes()))
	}
	for index, route := range Routes() {
		if registered[index] != route.Pattern() {
			t.Fatalf("pattern %d = %q, want %q", index, registered[index], route.Pattern())
		}
	}
	if _, err := proxy.Register(nil); err == nil {
		t.Fatal("Register(nil) succeeded")
	}
}

// TestRoutesAreUnique catches the duplicate that http.ServeMux would only
// report by panicking at startup.
func TestRoutesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, route := range Routes() {
		if seen[route.Pattern()] {
			t.Fatalf("duplicate route %q", route.Pattern())
		}
		seen[route.Pattern()] = true
		if route.Upstream != Trading && route.Upstream != Gateway {
			t.Fatalf("route %q names unknown upstream %q", route.Pattern(), route.Upstream)
		}
		if !strings.HasPrefix(route.Path, "/api/v1/") {
			t.Fatalf("route %q is not browser-facing", route.Pattern())
		}
	}
}

// TestRoutesIsACopy: the table decides where credential writes go, so a caller
// that could mutate it could silently redirect them.
func TestRoutesIsACopy(t *testing.T) {
	Routes()[0] = Route{Method: http.MethodGet, Path: "/api/v1/hijacked", Upstream: Gateway}
	if Routes()[0].Path == "/api/v1/hijacked" {
		t.Fatal("Routes() handed out the table itself")
	}
}

func TestNewValidatesWiring(t *testing.T) {
	valid := func() Options {
		return Options{
			Trading:  Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken},
			Gateway:  Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken},
			Sessions: &fakeSessions{},
		}
	}
	for name, mutate := range map[string]func(*Options){
		"no sessions":     func(o *Options) { o.Sessions = nil },
		"no trading url":  func(o *Options) { o.Trading.BaseURL = nil },
		"no gateway host": func(o *Options) { o.Gateway.BaseURL = mustURL(t, "http:///v1") },
		"non-http scheme": func(o *Options) { o.Trading.BaseURL = mustURL(t, "ftp://trading.invalid") },
		"short token":     func(o *Options) { o.Trading.Token = "short" },
	} {
		t.Run(name, func(t *testing.T) {
			options := valid()
			mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatal("New accepted invalid wiring")
			}
		})
	}
}

// TestServiceTokensDoNotRender keeps the one credential that unlocks both
// upstreams out of any log line that happens to print the proxy.
func TestServiceTokensDoNotRender(t *testing.T) {
	upstream := Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken}
	proxy, err := New(Options{
		Trading: upstream, Gateway: Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken},
		Sessions: &fakeSessions{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	encoded, err := json.Marshal(map[string]any{"upstream": upstream, "proxy": proxy})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, rendered := range []string{
		upstream.String(), upstream.GoString(), proxy.String(), proxy.GoString(), string(encoded),
	} {
		if strings.Contains(rendered, testToken) {
			t.Fatalf("service token leaked into %q", rendered)
		}
	}
}

// TestSessionServiceSatisfiesSessions is the real thing, not a fake: the CSRF
// token the proxy checks has to be the one the session service issued.
func TestSessionServiceSatisfiesSessions(t *testing.T) {
	hash, err := session.HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	service, err := session.New(session.Options{
		Username: "operator", PasswordHash: hash, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	result, err := service.Login(t.Context(), "operator", "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("login: %v", err)
	}

	trading := &capture{}
	upstream := httptest.NewServer(recorder(trading, nil))
	defer upstream.Close()
	proxy, err := New(Options{
		Trading:  Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken},
		Gateway:  Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken},
		Sessions: service,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mux := http.NewServeMux()
	if _, err := proxy.Register(mux); err != nil {
		t.Fatalf("Register: %v", err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	post := func(csrf string) int {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/v1/products/manual/commands", strings.NewReader(`{}`))
		request.AddCookie(&http.Cookie{Name: session.CookieName, Value: result.Session.Token})
		if csrf != "" {
			request.Header.Set(csrfHeader, csrf)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if got := post(""); got != http.StatusForbidden {
		t.Fatalf("missing csrf: status = %d, want 403", got)
	}
	if got := post("not-the-token"); got != http.StatusForbidden {
		t.Fatalf("wrong csrf: status = %d, want 403", got)
	}
	if got := post(result.Session.CSRFToken); got != http.StatusOK {
		t.Fatalf("valid csrf: status = %d, want 200", got)
	}
	if got := trading.header.Get(principalHeader); got != session.DefaultPrincipalID {
		t.Fatalf("principal = %q, want %q", got, session.DefaultPrincipalID)
	}
}

func TestRetiredProductRoutesNeverReachAnUpstream(t *testing.T) {
	h := newHarness(t, nil)
	for _, path := range []string{"/api/v1/products/arbitrage/opportunities", "/api/v1/products/arbitrage/positions", "/api/v1/products/arbitrage/commands", "/api/v1/products/arbitrage/execution-previews", "/api/v1/realtime/ws", "/api/v1/reservations", "/api/v1/reservations/old"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			response := h.do(t, method, path, nil, "")
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound {
				t.Fatalf("%s %s = %d", method, path, response.StatusCode)
			}
		}
	}
	if h.trading.path != "" || h.gateway.path != "" {
		t.Fatalf("retired route forwarded: trading=%s gateway=%s", h.trading.path, h.gateway.path)
	}
}

func TestGenericCommandWriteIsNotForwarded(t *testing.T) {
	h := newHarness(t, nil)
	response := h.do(t, http.MethodPost, "/api/v1/commands", nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusMethodNotAllowed || h.trading.path != "" || h.gateway.path != "" {
		t.Fatalf("status=%d trading=%s gateway=%s", response.StatusCode, h.trading.path, h.gateway.path)
	}
}
