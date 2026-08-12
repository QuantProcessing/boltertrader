package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	"github.com/QuantProcessing/boltertrader/internal/httpx"
)

type AuthRoutesOptions struct{ Sessions *session.Service }

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func RegisterAuthRoutes(mux *http.ServeMux, options AuthRoutesOptions) error {
	if mux == nil || options.Sessions == nil {
		return httpx.ConfigErr("auth route dependencies are required")
	}
	mux.HandleFunc("POST /api/v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var request LoginRequest
		if err := decodeStrict(r, &request); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, httpx.RequestID(r))
			return
		}
		result, err := options.Sessions.Login(r.Context(), request.Username, request.Password)
		request.Password = ""
		if err != nil {
			httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
			return
		}
		setSessionCookie(w, result.Session.Token, result.Session.ExpiresAt, options.Sessions.CookieSecure())
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": result})
	})
	mux.HandleFunc("GET /api/v1/auth/session", func(w http.ResponseWriter, r *http.Request) { writeCurrentSession(w, r, options.Sessions) })
	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) { writeCurrentSession(w, r, options.Sessions) })
	mux.HandleFunc("DELETE /api/v1/auth/session", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
			return
		}
		if err := options.Sessions.Logout(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token")); err != nil {
			status := http.StatusUnauthorized
			if errors.Is(err, session.ErrInvalidCSRF) {
				status = http.StatusForbidden
			}
			httpx.WriteError(w, status, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: options.Sessions.CookieSecure(), SameSite: http.SameSiteStrictMode})
		w.WriteHeader(http.StatusNoContent)
	})
	return nil
}

func SessionAuthenticator(sessions *session.Service) func(*http.Request) (string, error) {
	return func(r *http.Request) (string, error) {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			return "", session.ErrInvalidSession
		}
		result, err := sessions.Current(r.Context(), cookie.Value)
		if err != nil {
			return "", err
		}
		return result.Principal.ID, nil
	}
}

func SessionMutationAuthorizer(sessions *session.Service) func(*http.Request) error {
	return func(r *http.Request) error {
		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || cookie.Value == "" {
			return session.ErrInvalidSession
		}
		return sessions.RequireCSRF(r.Context(), cookie.Value, r.Header.Get("X-CSRF-Token"))
	}
}

func writeCurrentSession(w http.ResponseWriter, r *http.Request, sessions *session.Service) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	result, err := sessions.Current(r.Context(), cookie.Value)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, httpx.RequestID(r))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"meta": httpx.Meta(r), "data": result})
}

func setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: SessionCookieName, Value: token, Path: "/", Expires: expiresAt, HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode})
}
