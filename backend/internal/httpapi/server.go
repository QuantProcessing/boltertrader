package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	"github.com/QuantProcessing/boltertrader/internal/httpx"
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
	// Downstream mounts routes this listener does not implement itself and
	// reports which ones it took, so they are not also registered as frozen
	// contract scaffolds. Today the trading service is mounted through it
	// in-process; when trading moves to its own process this becomes the
	// proxy's registration and no handler here has to change.
	Downstream     func(*http.ServeMux) ([]string, error)
	WriteAuthority interface {
		Ready(context.Context) (bool, string)
	}
}

type Server struct {
	sessions       *session.Service
	mode           string
	contract       frozencontract.Contract
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
	contract, err := frozencontract.Load()
	if err != nil {
		return nil, err
	}
	s := &Server{sessions: options.Sessions, mode: options.Mode, contract: contract, mux: http.NewServeMux(), writeAuthority: options.WriteAuthority}
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
	owned := map[string]bool{
		"POST /api/v1/auth/login": true, "GET /api/v1/auth/session": true,
		"DELETE /api/v1/auth/session": true, "GET /api/v1/me": true,
	}
	if err := RegisterAuthRoutes(s.mux, AuthRoutesOptions{Sessions: s.sessions}); err != nil {
		return err
	}
	if options.Downstream != nil {
		routes, err := options.Downstream(s.mux)
		if err != nil {
			return err
		}
		for _, route := range routes {
			owned[route] = true
		}
	}
	for _, listener := range s.contract.Listeners {
		if listener.ID != "platform-browser" {
			continue
		}
		for _, route := range listener.Routes {
			if owned[route] {
				continue
			}
			route := route
			s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
				s.route(w, r, route)
			})
		}
	}
	return nil
}

func (s *Server) route(w http.ResponseWriter, r *http.Request, route string) {
	switch route {
	case "POST /api/v1/auth/login":
		s.login(w, r, route)
	case "GET /api/v1/auth/session":
		s.session(w, r)
	case "DELETE /api/v1/auth/session":
		s.logout(w, r)
	case "GET /api/v1/me":
		s.me(w, r)
	case "GET /api/v1/system/status":
		s.systemStatus(w, r)
	case "GET /api/v1/system/readiness":
		s.systemReadiness(w, r)
	default:
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": map[string]any{"route": route, "scaffold": true}})
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, route string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, httpx.RequestID(r))
		return
	}
	if err := s.contract.ValidateMutation(route, httpx.NormalizedBody(body)); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, httpx.RequestID(r))
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, httpx.RequestID(r))
		return
	}
	result, err := s.sessions.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: result.Session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": result})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	result, err := s.currentSession(r)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": result})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	csrf := r.Header.Get("X-CSRF-Token")
	if err := s.sessions.Logout(r.Context(), cookie.Value, csrf); err != nil {
		if err == session.ErrInvalidCSRF {
			httpx.WriteError(w, http.StatusForbidden, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	result, err := s.currentSession(r)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": result})
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": readinessPayload(true)})
}

func (s *Server) systemReadiness(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusServiceUnavailable, map[string]any{"meta": httpx.Meta(r), "data": readinessPayload(false)})
}

func readinessPayload(ready bool) map[string]any {
	component := func() map[string]any {
		return map[string]any{"state": "scaffold", "ready": ready, "observedAt": time.Now().UTC().Format(time.RFC3339)}
	}
	return map[string]any{
		"ready":      ready,
		"process":    component(),
		"database":   component(),
		"gateway":    component(),
		"publicData": component(),
		"accounts":   component(),
	}
}

func (s *Server) currentSession(r *http.Request) (session.Result, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return session.Result{}, session.ErrInvalidSession
	}
	return s.sessions.Current(r.Context(), cookie.Value)
}
