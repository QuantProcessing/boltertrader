package httpapi

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

const (
	ContractVersion   = "boltertrader-product-platform-upgrade/v4"
	SessionCookieName = "bt_session"
)

type Options struct {
	Sessions        *session.Service
	Mode            string
	ContractVersion string
}

type Server struct {
	sessions *session.Service
	mode     string
	contract frozencontract.Contract
	mux      *http.ServeMux
}

func New(options Options) (http.Handler, error) {
	if options.Sessions == nil {
		return nil, configErr("sessions are required")
	}
	if options.ContractVersion != ContractVersion {
		return nil, configErr("contract version mismatch")
	}
	switch options.Mode {
	case "test", "live":
	default:
		return nil, configErr("invalid mode")
	}
	contract, err := frozencontract.Load()
	if err != nil {
		return nil, err
	}
	s := &Server{sessions: options.Sessions, mode: options.Mode, contract: contract, mux: http.NewServeMux()}
	s.register()
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, pattern := s.mux.Handler(r); pattern == "" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == "/api/v1/auth/login" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if _, err := s.currentSession(r); err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", map[string]any{"listener": "platform-browser"}, false, requestID(r))
		return
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) register() {
	for _, listener := range s.contract.Listeners {
		if listener.ID != "platform-browser" {
			continue
		}
		for _, route := range listener.Routes {
			route := route
			s.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
				s.route(w, r, route)
			})
		}
	}
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
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"route": route, "scaffold": true}})
	}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) login(w http.ResponseWriter, r *http.Request, route string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		return
	}
	if err := s.contract.ValidateMutation(route, normalizedBody(body)); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		return
	}
	var req loginRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		return
	}
	result, err := s.sessions.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: result.Session.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": result})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	result, err := s.currentSession(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": result})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return
	}
	csrf := r.Header.Get("X-CSRF-Token")
	if err := s.sessions.Logout(r.Context(), cookie.Value, csrf); err != nil {
		if err == session.ErrInvalidCSRF {
			writeError(w, http.StatusForbidden, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
			return
		}
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return
	}
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	result, err := s.currentSession(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": result})
}

func (s *Server) systemStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": readinessPayload(true)})
}

func (s *Server) systemReadiness(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"meta": meta(r), "data": readinessPayload(false)})
}

func readinessPayload(ready bool) map[string]any {
	component := func() map[string]any {
		return map[string]any{"state": "scaffold", "ready": ready, "observedAt": time.Now().UTC().Format(time.RFC3339)}
	}
	return map[string]any{
		"ready":         ready,
		"process":       component(),
		"database":      component(),
		"secretStore":   component(),
		"gateway":       component(),
		"publicData":    component(),
		"accounts":      component(),
		"fundingWorker": component(),
	}
}

func (s *Server) currentSession(r *http.Request) (session.Result, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		return session.Result{}, session.ErrInvalidSession
	}
	return s.sessions.Current(r.Context(), cookie.Value)
}

func meta(r *http.Request) map[string]any {
	return map[string]any{"requestId": requestID(r), "contractVersion": ContractVersion, "apiVersion": "v1"}
}

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return "req_" + hex.EncodeToString(buf)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any, retryable bool, requestID string) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details, "retryable": retryable, "requestId": requestID}})
}

func normalizedBody(body []byte) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return []byte("{}")
	}
	return body
}

func configErr(message string) error { return &configError{message: message} }

type configError struct{ message string }

func (e *configError) Error() string { return e.message }
