// Package httpapi is the auth edge's HTTP surface.
//
// It serves four routes — login, session read, logout, and the current
// principal — and mounts everything else through a downstream hook. That is the
// whole listener: the browser session is established and destroyed here, and
// every other capability belongs to a service that has never heard of a cookie.
package httpapi

import (
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/internal/httpx"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

const (
	// ContractVersion is re-exported so callers wiring this listener do not
	// have to import httpx just to name the version they are pinning to.
	ContractVersion   = httpx.ContractVersion
	SessionCookieName = session.CookieName
)

type Options struct {
	Sessions        *session.Service
	ContractVersion string
	// Downstream mounts every route this listener does not serve itself and
	// reports which ones it took.
	//
	// Authorizing those routes is the downstream's job, not this listener's.
	// Whatever mounts them has to resolve the caller anyway — the proxy needs a
	// principal to assert upstream, an in-process mount needs one to attribute
	// the write — so a check here could only duplicate that decision or
	// contradict it. It would also be the weaker of the two: this listener sees
	// a cookie, not a CSRF token bound to it.
	Downstream func(*http.ServeMux) ([]string, error)
}

type Server struct {
	sessions *session.Service
	mux      *http.ServeMux
}

func New(options Options) (http.Handler, error) {
	if options.Sessions == nil {
		return nil, httpx.ConfigErr("sessions are required")
	}
	if options.ContractVersion != ContractVersion {
		return nil, httpx.ConfigErr("contract version mismatch")
	}
	s := &Server{sessions: options.Sessions, mux: http.NewServeMux()}
	if err := RegisterAuthRoutes(s.mux, AuthRoutesOptions{Sessions: s.sessions}); err != nil {
		return nil, err
	}
	if options.Downstream != nil {
		if _, err := options.Downstream(s.mux); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Minting the request id here rather than per-handler is what makes it
	// present on the 404s too, and what lets a forwarded request carry the same
	// id the browser will see in the response.
	if strings.TrimSpace(r.Header.Get("X-Request-Id")) == "" {
		r.Header.Set("X-Request-Id", httpx.RequestID(r))
	}
	s.mux.ServeHTTP(w, r)
}
