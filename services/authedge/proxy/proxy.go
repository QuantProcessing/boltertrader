// Package proxy is the forwarding half of the auth edge.
//
// The auth edge terminates the browser session and nothing else. Every other
// capability lives in a service that has no idea what a cookie is: the trading
// service and the gateway both authenticate a *caller* with a service token
// and then trust the identity that caller asserts in a header. This package is
// the piece that turns the first arrangement into the second.
//
// That translation is the security boundary, and it runs in one direction
// only. Identity headers and the Authorization header are stripped from every
// inbound request before anything is injected, so a browser that sends
// `X-Bolter-Principal: someone-else` gets it deleted rather than honoured. The
// session cookie is stripped in the other direction for the same reason: the
// upstreams have no use for the raw session token, and forwarding it would
// spread the one credential the auth edge exists to contain.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

const (
	authorizationHeader = "Authorization"
	principalHeader     = "X-Bolter-Principal"
	csrfHeader          = "X-CSRF-Token"

	// The two upstreams each read a mode attestation under their own header
	// name — trading X-Bolter-Principal-Mode, the gateway X-Bolter-Mode — and
	// reconciling that was this proxy's job. Neither header exists now, but
	// both stay on the forgeable list: an inbound request carrying one is
	// still someone probing, and it must not survive to be read as authority
	// if an upstream ever grows a name back.
	tradingModeHeader = "X-Bolter-Principal-Mode"
	gatewayModeHeader = "X-Bolter-Mode"
)

// forgeableHeaders are deleted from every inbound request. The first two are
// how the auth edge asserts identity downstream, so a request that arrives
// carrying one is either a misconfigured proxy or someone probing; Cookie goes
// with them because the session token stops here.
var forgeableHeaders = []string{
	authorizationHeader, principalHeader, tradingModeHeader, gatewayModeHeader, "Cookie",
}

// forwardedHeaders is an allowlist rather than a denylist, and it holds exactly
// what the upstreams actually read plus Accept. On a boundary this narrow, a
// header nobody reads is a header nobody has reasoned about.
var forwardedHeaders = []string{
	"Accept", "Content-Type", "Idempotency-Key", "If-Match", "Last-Event-ID", "X-Request-Id",
}

// hopByHopHeaders belong to a single connection and must not be relayed onto
// the next one, in either direction.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Sessions is the slice of the session service this package needs: who is
// calling, and whether they may mutate.
type Sessions interface {
	Current(ctx context.Context, token string) (session.Result, error)
	RequireCSRF(ctx context.Context, token string, csrf string) error
}

var _ Sessions = (*session.Service)(nil)

// Upstream is one service the auth edge forwards to.
type Upstream struct {
	// BaseURL is the service's origin. A path on it is honoured as a mount
	// prefix, so the proxy still works behind a reverse proxy that nests the
	// service under a subpath.
	BaseURL *url.URL
	// Token is the shared service token proving the caller is the auth edge.
	// Everything downstream rests on this being unreachable from a browser.
	Token string
}

// String, GoString and MarshalJSON keep the service token out of any log line
// or error that happens to render an upstream.
func (upstream Upstream) String() string {
	host := ""
	if upstream.BaseURL != nil {
		host = upstream.BaseURL.Host
	}
	return "proxy.Upstream{host:" + host + ",token:redacted}"
}

func (upstream Upstream) GoString() string { return upstream.String() }

func (upstream Upstream) MarshalJSON() ([]byte, error) {
	return []byte(`{"token":"redacted"}`), nil
}

// Options is everything the proxy needs to forward a request.
type Options struct {
	Trading  Upstream
	Gateway  Upstream
	Sessions Sessions
	// Client is optional. The default has no timeout of its own because a
	// client-wide deadline would kill event streams; bounded requests get
	// their deadline from RequestTimeout instead.
	Client *http.Client
	// RequestTimeout bounds a non-streaming forward. Zero means one minute.
	RequestTimeout time.Duration
}

// Proxy forwards the browser's requests to the services that answer them.
type Proxy struct {
	upstreams map[UpstreamName]Upstream
	sessions  Sessions
	client    *http.Client
	timeout   time.Duration
}

func New(options Options) (*Proxy, error) {
	if options.Sessions == nil {
		return nil, httpx.ConfigErr("proxy requires a session service")
	}
	upstreams := map[UpstreamName]Upstream{Trading: options.Trading, Gateway: options.Gateway}
	for name, upstream := range upstreams {
		if err := validateUpstream(name, upstream); err != nil {
			return nil, err
		}
	}
	client := options.Client
	if client == nil {
		client = &http.Client{}
	}
	timeout := options.RequestTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	return &Proxy{upstreams: upstreams, sessions: options.Sessions, client: client, timeout: timeout}, nil
}

func validateUpstream(name UpstreamName, upstream Upstream) error {
	if upstream.BaseURL == nil || upstream.BaseURL.Host == "" {
		return httpx.ConfigErr(fmt.Sprintf("%s upstream URL is required", name))
	}
	if upstream.BaseURL.Scheme != "http" && upstream.BaseURL.Scheme != "https" {
		return httpx.ConfigErr(fmt.Sprintf("%s upstream URL must be http or https", name))
	}
	// A token this short is not a service token, it is a password someone
	// typed — and this credential is never typed. The same floor the trading
	// service enforces on the receiving end.
	if len(upstream.Token) < 32 {
		return httpx.ConfigErr(fmt.Sprintf("%s service token must be at least 32 bytes", name))
	}
	return nil
}

// Register mounts every proxied route on mux and reports which patterns it
// took. The signature matches the auth edge's downstream hook, so mounting the
// trading service over HTTP is the same wiring as mounting it in-process was.
func (proxy *Proxy) Register(mux *http.ServeMux) ([]string, error) {
	if mux == nil {
		return nil, httpx.ConfigErr("proxy requires a mux")
	}
	registered := make([]string, 0, len(routes))
	for _, route := range routes {
		if _, ok := proxy.upstreams[route.Upstream]; !ok {
			return nil, httpx.ConfigErr(fmt.Sprintf("route %s names unknown upstream %s", route.Pattern(), route.Upstream))
		}
		mux.Handle(route.Pattern(), proxy.handler(route))
		registered = append(registered, route.Pattern())
	}
	return registered, nil
}

func (proxy *Proxy) handler(route Route) http.Handler {
	upstream := proxy.upstreams[route.Upstream]
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := proxy.authorize(w, r)
		if !ok {
			return
		}
		outbound, ok := proxy.build(w, r, route, upstream, principal)
		if !ok {
			return
		}
		proxy.forward(w, r, route, outbound)
	})
}

// authorize resolves the session and, for a mutation, checks the CSRF token.
// Both upstreams delegate that second decision here on purpose: the CSRF token
// is bound to a browser session neither of them can see.
func (proxy *Proxy) authorize(w http.ResponseWriter, r *http.Request) (session.Principal, bool) {
	cookie, err := r.Cookie(session.CookieName)
	if err != nil || cookie.Value == "" {
		writeAuthRequired(w, r)
		return session.Principal{}, false
	}
	result, err := proxy.sessions.Current(r.Context(), cookie.Value)
	if err != nil {
		writeAuthRequired(w, r)
		return session.Principal{}, false
	}
	if result.Principal.ID == "" {
		writeAuthRequired(w, r)
		return session.Principal{}, false
	}
	if !safeMethod(r.Method) {
		if err := proxy.sessions.RequireCSRF(r.Context(), cookie.Value, r.Header.Get(csrfHeader)); err != nil {
			if errors.Is(err, session.ErrInvalidCSRF) {
				httpx.WriteError(w, http.StatusForbidden, "CSRF_REQUIRED", "request is not authorised", nil, false, httpx.RequestID(r))
				return session.Principal{}, false
			}
			writeAuthRequired(w, r)
			return session.Principal{}, false
		}
	}
	return result.Principal, true
}

func (proxy *Proxy) build(w http.ResponseWriter, r *http.Request, route Route, upstream Upstream, principal session.Principal) (*http.Request, bool) {
	path, escaped, ok := targetPath(upstream.BaseURL, route.UpstreamPath(), r)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
		return nil, false
	}
	target := *upstream.BaseURL
	target.Path, target.RawPath, target.RawQuery = path, escaped, r.URL.RawQuery

	// The body streams rather than being read into memory: an order payload is
	// small, but buffering is the kind of thing that quietly becomes a limit.
	outbound, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
		return nil, false
	}
	outbound.ContentLength = r.ContentLength

	for _, name := range forwardedHeaders {
		if value := r.Header.Get(name); value != "" {
			outbound.Header.Set(name, value)
		}
	}
	// Nothing above can have set an identity header — the allowlist does not
	// contain one — but deleting them is what makes that a property of the
	// code rather than of the list happening to be right today.
	for _, name := range forgeableHeaders {
		outbound.Header.Del(name)
	}
	outbound.Header.Set(authorizationHeader, "Bearer "+upstream.Token)
	outbound.Header.Set(principalHeader, principal.ID)
	if outbound.Header.Get("X-Request-Id") == "" {
		outbound.Header.Set("X-Request-Id", httpx.RequestID(r))
	}
	return outbound, true
}

func (proxy *Proxy) forward(w http.ResponseWriter, r *http.Request, route Route, outbound *http.Request) {
	if !route.Stream {
		// A deadline here and not on the client: the client is shared with the
		// event stream, which must be allowed to run for as long as the
		// browser keeps listening.
		ctx, cancel := context.WithTimeout(outbound.Context(), proxy.timeout)
		defer cancel()
		outbound = outbound.WithContext(ctx)
	}
	response, err := proxy.client.Do(outbound)
	if err != nil {
		// A cancelled request means the browser hung up. There is nobody left
		// to answer, and writing a status now would only corrupt the log.
		if r.Context().Err() != nil {
			return
		}
		httpx.WriteError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", "upstream service is unavailable", nil, true, httpx.RequestID(r))
		return
	}
	defer response.Body.Close()

	header := w.Header()
	for name, values := range response.Header {
		if isHopByHop(name) || strings.EqualFold(name, "Set-Cookie") {
			// An upstream has no business setting a browser cookie, and
			// relaying one would let it overwrite the session.
			continue
		}
		for _, value := range values {
			header.Add(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	stream(w, response.Body)
}

// stream copies the upstream body through unbuffered. Server-sent events only
// work if each event reaches the browser as it arrives, so this flushes after
// every read rather than after every full buffer.
func stream(w http.ResponseWriter, body io.Reader) {
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		read, readErr := body.Read(buffer)
		if read > 0 {
			if _, writeErr := w.Write(buffer[:read]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
	}
}

// targetPath rebuilds the upstream path from the route's template and the
// values the mux already matched, instead of rewriting the raw URL.
//
// Rebuilding is what stops a caller smuggling a path upstream: every segment
// is either a literal from the route table or exactly one matched value, so a
// `..`, an extra slash, or an encoded separator cannot change which upstream
// resource is addressed. It returns both the decoded path and its escaping,
// because a matched value may legitimately contain a character that has to
// stay escaped on the way out.
func targetPath(base *url.URL, template string, r *http.Request) (string, string, bool) {
	var decoded, escaped strings.Builder
	decoded.WriteString(strings.TrimSuffix(base.Path, "/"))
	escaped.WriteString(strings.TrimSuffix(base.EscapedPath(), "/"))
	for segment := range strings.SplitSeq(strings.TrimPrefix(template, "/"), "/") {
		value := segment
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			value = r.PathValue(strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}"))
			if value == "" {
				return "", "", false
			}
			decoded.WriteString("/" + value)
			escaped.WriteString("/" + url.PathEscape(value))
			continue
		}
		decoded.WriteString("/" + segment)
		escaped.WriteString("/" + segment)
	}
	return decoded.String(), escaped.String(), true
}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func isHopByHop(name string) bool {
	for _, header := range hopByHopHeaders {
		if strings.EqualFold(name, header) {
			return true
		}
	}
	return false
}

func writeAuthRequired(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
}

// String, GoString and MarshalJSON keep the service tokens out of any log line
// or error that happens to render the proxy.
func (proxy *Proxy) String() string { return "proxy.Proxy{tokens:redacted}" }

func (proxy *Proxy) GoString() string { return proxy.String() }

func (proxy *Proxy) MarshalJSON() ([]byte, error) { return []byte(`{"tokens":"redacted"}`), nil }
