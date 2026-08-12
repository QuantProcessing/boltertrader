package httpapi

import (
	"context"
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
	Mode            string
	ContractVersion string
	// Downstream mounts every route this listener does not serve itself and
	// reports which ones it took. The auth edge owns four routes; everything
	// else on the browser surface belongs to a service behind this hook.
	Downstream     func(*http.ServeMux) ([]string, error)
	WriteAuthority interface {
		Ready(context.Context) (bool, string)
	}
}

type Server struct {
	sessions       *session.Service
	mode           string
	mux            *http.ServeMux
	writeAuthority interface {
		Ready(context.Context) (bool, string)
	}
}

func New(options Options) (http.Handler, error) {
	if options.Sessions == nil {
		return nil, httpx.ConfigErr("sessions are required")
	}
	if options.ContractVersion != ContractVersion {
		return nil, httpx.ConfigErr("contract version mismatch")
	}
	switch options.Mode {
	case "test", "live":
	default:
		return nil, httpx.ConfigErr("invalid mode")
	}
	s := &Server{sessions: options.Sessions, mode: options.Mode, mux: http.NewServeMux(), writeAuthority: options.WriteAuthority}
	if err := s.register(options); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("X-Request-Id")) == "" {
		r.Header.Set("X-Request-Id", httpx.RequestID(r))
	}
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if s.canonicalMutationBlocked(r) {
		httpx.WriteError(w, http.StatusServiceUnavailable, "CANONICAL_WRITES_NOT_READY", "canonical writes are not ready", nil, true, httpx.RequestID(r))
		return
	}
	if r.URL.Path == "/api/v1/auth/login" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if _, err := s.currentSession(r); err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", map[string]any{"listener": "platform-browser"}, false, httpx.RequestID(r))
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) canonicalMutationBlocked(r *http.Request) bool {
	if s.writeAuthority == nil || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return false
	}
	if (r.Method == http.MethodPost && r.URL.Path == "/api/v1/auth/login") ||
		(r.Method == http.MethodDelete && r.URL.Path == "/api/v1/auth/session") {
		return false
	}
	ready, _ := s.writeAuthority.Ready(r.Context())
	return !ready
}

func (s *Server) register(options Options) error {
	if err := RegisterAuthRoutes(s.mux, AuthRoutesOptions{Sessions: s.sessions}); err != nil {
		return err
	}
	if options.Downstream != nil {
		if _, err := options.Downstream(s.mux); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) currentSession(r *http.Request) (session.Result, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return session.Result{}, session.ErrInvalidSession
	}
	return s.sessions.Current(r.Context(), cookie.Value)
}
