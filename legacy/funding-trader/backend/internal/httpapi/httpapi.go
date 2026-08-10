package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/maintenance"
	"github.com/QuantProcessing/funding-trader/backend/internal/observability"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

const sessionCookieName = "ft_session"

type authService interface {
	Login(context.Context, string, string, string) (auth.LoginResult, error)
	Authenticate(context.Context, string) (auth.Principal, error)
	CurrentSession(context.Context, string) (auth.CurrentSessionResult, error)
	RequireCSRF(context.Context, string, string) error
	Logout(context.Context, string, string, string) error
	AuditSecurityOutcome(context.Context, string, string, string, string) error
	CookieSecure() bool
}

type executionAPI interface {
	Preview(context.Context, executionservice.Scope, executionservice.PreviewRequest) (execution.PreviewResult, error)
	Submit(context.Context, executionservice.Scope, executionservice.SubmitRequest, string, string) (executionservice.Result, error)
	Close(context.Context, executionservice.Scope, executionservice.SubmitRequest, string, string) (executionservice.Result, error)
	Get(context.Context, executionservice.Scope, string) (execution.Execution, error)
}

type ServerConfig struct {
	Mode             string
	Version          string
	Build            string
	MaintenanceGate  *maintenance.Gate
	Auth             authService
	ReadModel        readmodel.Reader
	Ledger           ledgerAPI
	Execution        executionAPI
	Metrics          observability.Reader
	Stream           *uistream.Service
	Readiness        readiness.Checker
	TradingReadiness tradingReadinessAPI
	Logger           *slog.Logger
}

type Server struct {
	cfg           ServerConfig
	started       time.Time
	mux           *http.ServeMux
	loginThrottle *loginThrottle
}

type principalKey struct{}
type tokenKey struct{}
type requestIDKey struct{}

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func New(cfg ServerConfig) http.Handler {
	mustValidateProductMode(cfg.Mode)
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.MaintenanceGate == nil {
		cfg.MaintenanceGate = maintenance.NewGate()
	}
	s := &Server{
		cfg: cfg, started: time.Now().UTC(), mux: http.NewServeMux(),
		loginThrottle: newLoginThrottle(nil),
	}
	s.routes()
	return s.withRequestID(cfg.MaintenanceGate.Wrap(s.mux))
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /health/live", s.live)
	s.mux.HandleFunc("GET /health/ready", s.ready)
	s.mux.Handle("GET /metrics", s.requireAuth(http.HandlerFunc(s.metrics)))
	s.mux.HandleFunc("POST /api/v1/auth/login", s.login)
	s.mux.Handle("GET /api/v1/auth/session", s.requireAuth(http.HandlerFunc(s.session)))
	s.mux.HandleFunc("DELETE /api/v1/auth/session", s.logout)
	s.mux.Handle("GET /api/v1/me", s.requireAuth(http.HandlerFunc(s.me)))
	s.mux.Handle("GET /api/v1/system/status", s.requireAuth(http.HandlerFunc(s.status)))
	s.mux.Handle("GET /api/v1/system/readiness", s.requireAuth(http.HandlerFunc(s.apiReadiness)))
	s.mux.Handle("GET /api/v1/system/verification", s.requireAuth(http.HandlerFunc(s.verification)))
	s.mux.Handle("GET /api/v1/ledger/funding-payments", s.requireAuth(http.HandlerFunc(s.listFundingPayments)))
	s.mux.Handle("GET /api/v1/ledger/portfolio", s.requireAuth(http.HandlerFunc(s.portfolioLedger)))
	s.mux.Handle("GET /api/v1/opportunities", s.requireAuth(http.HandlerFunc(s.listOpportunities)))
	s.mux.Handle("GET /api/v1/opportunities/{opportunityId}", s.requireAuth(http.HandlerFunc(s.opportunity)))
	s.mux.Handle("GET /api/v1/funding-rate-alerts", s.requireAuth(http.HandlerFunc(s.listAlerts)))
	s.mux.Handle("POST /api/v1/execution-previews", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.executionPreview))))
	s.mux.Handle("POST /api/v1/orders", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.submitExecution))))
	s.mux.Handle("GET /api/v1/executions/{executionId}", s.requireAuth(http.HandlerFunc(s.getExecution)))
	s.mux.Handle("GET /api/v1/positions", s.requireAuth(http.HandlerFunc(s.listPositions)))
	s.mux.Handle("GET /api/v1/positions/{positionId}", s.requireAuth(http.HandlerFunc(s.position)))
	s.mux.Handle("POST /api/v1/positions/{positionId}/close", s.requireAuth(s.requireCSRF(http.HandlerFunc(s.closePosition))))
	s.mux.Handle("GET /api/v1/activities", s.requireAuth(http.HandlerFunc(s.listActivities)))
	s.mux.Handle("GET /api/v1/audit-events", s.requireAuth(http.HandlerFunc(s.listAuditEvents)))
	s.mux.Handle("GET /api/v1/stream", s.requireAuth(http.HandlerFunc(s.stream)))
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Metrics == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "metrics_unavailable", "metrics are unavailable", true, nil)
		return
	}
	snapshot, err := s.cfg.Metrics.Snapshot(r.Context(), s.readScope(r))
	if err != nil {
		s.cfg.Logger.Error("metrics_snapshot_failed",
			"requestId", requestID(r),
			"errorType", fmt.Sprintf("%T", err),
		)
		s.writeError(w, r, http.StatusServiceUnavailable, "metrics_unavailable", "metrics are unavailable", true, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `# HELP funding_trader_execution_state_current Current scoped executions in a safety-critical state.
# TYPE funding_trader_execution_state_current gauge
funding_trader_execution_state_current{state="partiallyHedged"} %d
funding_trader_execution_state_current{state="compensating"} %d
funding_trader_execution_state_current{state="uncertain"} %d
funding_trader_execution_state_current{state="manualInterventionRequired"} %d
# HELP funding_trader_resnapshot_required_events Persisted scoped resnapshot-required events retained by the backend.
# TYPE funding_trader_resnapshot_required_events gauge
funding_trader_resnapshot_required_events %d
`,
		snapshot.PartiallyHedged,
		snapshot.Compensating,
		snapshot.Uncertain,
		snapshot.ManualInterventionRequired,
		snapshot.ResnapshotRequired,
	)
}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(requestID) {
			requestID = randomRequestID()
		}
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		w.Header().Set("X-Request-Id", requestID)
		w.Header().Set("Content-Type", "application/json")
		request := r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		if s.rejectBrowserModeOverride(recorder, request) {
			s.cfg.Logger.Info("http_request",
				"requestId", requestID,
				"method", r.Method,
				"path", r.URL.Path,
				"status", recorder.status,
				"duration", time.Since(start).String(),
			)
			return
		}
		next.ServeHTTP(recorder, request)
		s.cfg.Logger.Info("http_request",
			"requestId", requestID,
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(start).String(),
		)
	})
}

func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := r.Context().Value(tokenKey{}).(string)
		csrf := strings.TrimSpace(r.Header.Get("X-CSRF-Token"))
		if csrf == "" {
			_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.csrf", "missing", "")
			s.writeError(w, r, http.StatusForbidden, "csrf_invalid", "csrf token invalid", false, nil)
			return
		}
		if err := s.cfg.Auth.RequireCSRF(r.Context(), token, csrf); err != nil {
			if errors.Is(err, auth.ErrInvalidSession) {
				clearSessionCookie(w, s.cfg.Auth.CookieSecure())
				s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
				return
			}
			_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.csrf", "invalid", "")
			s.writeError(w, r, http.StatusForbidden, "csrf_invalid", "csrf token invalid", false, nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.session", "unauthenticated", "")
			s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
			return
		}
		principal, err := s.cfg.Auth.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.session", "invalid", "")
			clearSessionCookie(w, s.cfg.Auth.CookieSecure())
			s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, principal)
		ctx = context.WithValue(ctx, tokenKey{}, cookie.Value)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := dec.Decode(&body); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed login request", false, nil)
		return
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed login request", false, nil)
		return
	}
	throttleKey := loginThrottleKey(body.Email, r.RemoteAddr)
	if retryAfter := s.loginThrottle.RetryAfter(throttleKey); retryAfter > 0 {
		retryAfterSeconds := int64((retryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.FormatInt(retryAfterSeconds, 10))
		_ = s.cfg.Auth.AuditSecurityOutcome(
			r.Context(), requestID(r), "auth.login", "throttled", "",
		)
		s.writeError(
			w, r, http.StatusTooManyRequests, "rate_limited",
			"login temporarily unavailable", true,
			map[string]any{"retryAfterSeconds": retryAfterSeconds},
		)
		return
	}
	result, err := s.cfg.Auth.Login(r.Context(), body.Email, body.Password, requestID(r))
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.loginThrottle.RecordFailure(throttleKey)
			s.writeError(w, r, http.StatusUnauthorized, "invalid_credentials", "invalid email or password", false, nil)
			return
		}
		s.writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "service temporarily unavailable", true, nil)
		return
	}
	s.loginThrottle.Reset(throttleKey)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: result.Session.Token, Path: "/", HttpOnly: true, Secure: s.cfg.Auth.CookieSecure(), SameSite: http.SameSiteStrictMode, Expires: result.Session.ExpiresAt})
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"data": map[string]any{
			"session":   map[string]any{"authenticated": true, "expiresAt": result.Session.ExpiresAt.UTC().Format(time.RFC3339), "csrfToken": result.Session.CSRFToken},
			"principal": principalPayload(result.Principal),
		},
		"meta": meta(r),
	})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	token := r.Context().Value(tokenKey{}).(string)
	current, err := s.cfg.Auth.CurrentSession(r.Context(), token)
	if err != nil {
		s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": map[string]any{"session": sessionPayload(current.Session), "principal": principalPayload(current.Principal)}, "meta": meta(r)})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	principal := r.Context().Value(principalKey{}).(auth.Principal)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": map[string]any{"principal": principalPayload(principal)}, "meta": meta(r)})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.session", "unauthenticated", "")
		s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
		return
	}
	token := cookie.Value
	csrf := r.Header.Get("X-CSRF-Token")
	if csrf == "" {
		_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.csrf", "missing", "")
		s.writeError(w, r, http.StatusForbidden, "csrf_invalid", "csrf token invalid", false, nil)
		return
	}
	if err := s.cfg.Auth.Logout(r.Context(), token, csrf, requestID(r)); err != nil {
		if errors.Is(err, auth.ErrInvalidCSRF) {
			_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.csrf", "invalid", "")
			s.writeError(w, r, http.StatusForbidden, "csrf_invalid", "csrf token invalid", false, nil)
			return
		}
		_ = s.cfg.Auth.AuditSecurityOutcome(r.Context(), requestID(r), "auth.session", "invalid", "")
		clearSessionCookie(w, s.cfg.Auth.CookieSecure())
		s.writeError(w, r, http.StatusUnauthorized, "unauthenticated", "authentication required", false, nil)
		return
	}
	clearSessionCookie(w, s.cfg.Auth.CookieSecure())
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": map[string]any{"loggedOut": true}, "meta": meta(r)})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ready := s.cfg.Readiness.Check(r.Context()).Ready
	maintenanceState := s.cfg.MaintenanceGate.Snapshot()
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": map[string]any{"mode": s.cfg.Mode, "version": s.cfg.Version, "build": s.cfg.Build, "maintenance": maintenanceState.Enabled, "maintenanceReason": maintenanceState.Reason, "uptimeSeconds": int64(time.Since(s.started).Seconds()), "ready": ready}, "meta": meta(r)})
}

func (s *Server) apiReadiness(w http.ResponseWriter, r *http.Request) {
	result := s.cfg.Readiness.Check(r.Context())
	trading := readiness.TradingReadinessResult{Reason: readiness.TradingReadinessReasonNotReady}
	if s.cfg.TradingReadiness != nil {
		trading = s.cfg.TradingReadiness.CheckTrading(r.Context())
	}
	status := http.StatusOK
	if !result.Ready {
		status = http.StatusServiceUnavailable
	}
	s.writeJSON(w, r, status, map[string]any{"data": newReadinessDTO(result, trading), "meta": meta(r)})
}

func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": map[string]any{"status": "live"}, "meta": meta(r)})
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	result := s.cfg.Readiness.Check(r.Context())
	status := http.StatusOK
	if !result.Ready {
		status = http.StatusServiceUnavailable
	}
	s.writeJSON(w, r, status, map[string]any{"data": result, "meta": meta(r)})
}

func (s *Server) rejectBrowserModeOverride(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return false
	}
	query := r.URL.Query()
	for _, field := range []string{"environment", "mode"} {
		if _, ok := query[field]; ok {
			s.writeError(w, r, http.StatusBadRequest, "bad_request", "product mode is server configured and cannot be overridden", false, map[string]any{"field": field})
			return true
		}
	}
	return false
}

func (s *Server) listOpportunities(w http.ResponseWriter, r *http.Request) {
	if !s.requireReadModel(w, r, "opportunities") {
		return
	}
	pageQuery, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	sort := readmodel.OpportunitySort(strings.TrimSpace(r.URL.Query().Get("sort")))
	if sort == "" {
		sort = readmodel.NetAPRDescending
	}
	if sort != readmodel.NetAPRDescending && sort != readmodel.NetAPRAscending {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid opportunity sort", false, map[string]any{"field": "sort"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "all" {
		status = ""
	}
	if status != "" && status != "executable" && status != "attention" && status != "blocked" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid opportunity status", false, map[string]any{"field": "status"})
		return
	}
	page, err := s.cfg.ReadModel.ListOpportunities(r.Context(), s.readScope(r), readmodel.OpportunityQuery{
		PageQuery:    pageQuery,
		Sort:         sort,
		ShortVenueID: strings.TrimSpace(r.URL.Query().Get("shortVenueId")),
		Status:       status,
	})
	if err != nil {
		s.writeReadModelError(w, r, "opportunities", err)
		return
	}
	s.writePage(w, r, page)
}

func (s *Server) listAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.requireReadModel(w, r, "funding-rate-alerts") {
		return
	}
	pageQuery, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	severity := strings.TrimSpace(r.URL.Query().Get("severity"))
	switch severity {
	case "", "all", "critical", "warning", "watch":
	default:
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid alert severity", false, map[string]any{"field": "severity"})
		return
	}
	page, err := s.cfg.ReadModel.ListAlerts(r.Context(), s.readScope(r), readmodel.AlertQuery{
		PageQuery: pageQuery,
		Severity:  severity,
	})
	if err != nil {
		s.writeReadModelError(w, r, "funding-rate-alerts", err)
		return
	}
	s.writePage(w, r, page)
}

func (s *Server) executionPreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireExecution(w, r) {
		return
	}
	var body struct {
		OpportunityID string `json:"opportunityId"`
		Capital       string `json:"capital"`
		Leverage      int    `json:"leverage"`
	}
	if !s.decodeStrictBody(w, r, &body) {
		return
	}
	body.OpportunityID = strings.TrimSpace(body.OpportunityID)
	capital, ok := s.parsePositiveDecimal(w, r, "capital", body.Capital)
	if !ok {
		return
	}
	if body.OpportunityID == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "opportunity id is required", false, map[string]any{"field": "opportunityId"})
		return
	}
	if body.Leverage != 1 && body.Leverage != 2 {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "leverage must be 1 or 2", false, map[string]any{"field": "leverage"})
		return
	}
	preview, err := s.cfg.Execution.Preview(r.Context(), s.executionScope(r), executionservice.PreviewRequest{
		PreviewID: "preview-" + randomRequestID(), Operation: execution.OperationOpen,
		OpportunityID: body.OpportunityID, CapitalUSDT: capital, Leverage: body.Leverage,
	})
	if err != nil {
		s.writeExecutionError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"data": newExecutionPreviewDTO(preview),
		"meta": meta(r),
	})
}

func (s *Server) submitExecution(w http.ResponseWriter, r *http.Request) {
	receivedAt := time.Now().UTC()
	if !s.requireExecution(w, r) {
		return
	}
	idempotencyKey, ok := s.idempotencyKey(w, r)
	if !ok {
		return
	}
	var body struct {
		OpportunityID string `json:"opportunityId"`
		PreviewID     string `json:"previewId"`
		Capital       string `json:"capital"`
		Leverage      int    `json:"leverage"`
		OperationKind string `json:"operationKind"`
	}
	if !s.decodeStrictBody(w, r, &body) {
		return
	}
	body.OpportunityID = strings.TrimSpace(body.OpportunityID)
	body.PreviewID = strings.TrimSpace(body.PreviewID)
	if body.OperationKind != string(execution.OperationOpen) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "operation kind must be open", false, map[string]any{"field": "operationKind"})
		return
	}
	if body.OpportunityID == "" || body.PreviewID == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "opportunity id and preview id are required", false, map[string]any{"fields": []string{"opportunityId", "previewId"}})
		return
	}
	capital, ok := s.parsePositiveDecimal(w, r, "capital", body.Capital)
	if !ok {
		return
	}
	if body.Leverage != 1 && body.Leverage != 2 {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "leverage must be 1 or 2", false, map[string]any{"field": "leverage"})
		return
	}
	result, err := s.cfg.Execution.Submit(r.Context(), s.executionScope(r), executionservice.SubmitRequest{
		PreviewID: body.PreviewID, Operation: execution.OperationOpen,
		OpportunityID: body.OpportunityID, CapitalUSDT: capital, Leverage: body.Leverage,
		ReceivedAt: receivedAt,
	}, idempotencyKey, requestID(r))
	s.writeExecutionResultOrError(w, r, result, err)
}

func (s *Server) getExecution(w http.ResponseWriter, r *http.Request) {
	if !s.requireExecution(w, r) {
		return
	}
	id := strings.TrimSpace(r.PathValue("executionId"))
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "execution id is required", false, map[string]any{"field": "executionId"})
		return
	}
	snapshot, err := s.cfg.Execution.Get(r.Context(), s.executionScope(r), id)
	if err != nil {
		s.writeExecutionError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"data": newExecutionResponseDTO(snapshot),
		"meta": meta(r),
	})
}

func (s *Server) closePosition(w http.ResponseWriter, r *http.Request) {
	if !s.requireExecution(w, r) {
		return
	}
	idempotencyKey, ok := s.idempotencyKey(w, r)
	if !ok {
		return
	}
	if !s.decodeEmptyBody(w, r) {
		return
	}
	positionID := strings.TrimSpace(r.PathValue("positionId"))
	if positionID == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "position id is required", false, map[string]any{"field": "positionId"})
		return
	}
	result, err := s.cfg.Execution.Close(r.Context(), s.executionScope(r), executionservice.SubmitRequest{
		Operation: execution.OperationClose, PositionID: positionID,
	}, idempotencyKey, requestID(r))
	s.writeExecutionResultOrError(w, r, result, err)
}

func (s *Server) listPositions(w http.ResponseWriter, r *http.Request) {
	pageQuery, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	states := nonEmptyValues(r.URL.Query()["state"])
	if len(states) == 0 {
		states = []string{"open", "attention"}
	}
	if len(states) == 1 && states[0] == "closed" && s.cfg.Ledger != nil {
		s.closedPositions(w, r)
		return
	}
	if !s.requireReadModel(w, r, "positions") {
		return
	}
	page, err := s.cfg.ReadModel.ListPositions(r.Context(), s.readScope(r), readmodel.PositionQuery{
		PageQuery: pageQuery,
		States:    states,
	})
	if err != nil {
		s.writeReadModelError(w, r, "positions", err)
		return
	}
	s.writePage(w, r, page)
}

func (s *Server) listActivities(w http.ResponseWriter, r *http.Request) {
	if !s.requireReadModel(w, r, "activities") {
		return
	}
	pageQuery, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	from, ok := s.parseOptionalTime(w, r, "from")
	if !ok {
		return
	}
	to, ok := s.parseOptionalTime(w, r, "to")
	if !ok {
		return
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "activity time range is invalid", false, map[string]any{"fields": []string{"from", "to"}})
		return
	}
	page, err := s.cfg.ReadModel.ListActivities(r.Context(), s.readScope(r), readmodel.ActivityQuery{
		PageQuery: pageQuery,
		Kinds:     nonEmptyValues(r.URL.Query()["kind"]),
		From:      from,
		To:        to,
	})
	if err != nil {
		s.writeReadModelError(w, r, "activities", err)
		return
	}
	s.writePage(w, r, page)
}

func (s *Server) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireReadModel(w, r, "audit-events") {
		return
	}
	pageQuery, ok := s.parsePageQuery(w, r)
	if !ok {
		return
	}
	page, err := s.cfg.ReadModel.ListAuditEvents(r.Context(), s.readScope(r), readmodel.AuditQuery{PageQuery: pageQuery})
	if err != nil {
		s.writeReadModelError(w, r, "audit-events", err)
		return
	}
	s.writePage(w, r, page)
}

func (s *Server) opportunity(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ReadModel == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "read_model_unavailable", "read model service is unavailable", true, map[string]any{"resource": "opportunities"})
		return
	}
	id := strings.TrimSpace(r.PathValue("opportunityId"))
	if id == "" {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "opportunity id is required", false, map[string]any{"field": "opportunityId"})
		return
	}
	payload, err := s.cfg.ReadModel.Opportunity(r.Context(), s.readScope(r), id)
	if err != nil {
		s.writeReadModelError(w, r, "opportunities", err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": payload, "meta": meta(r)})
}

func (s *Server) requireExecution(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Execution != nil {
		return true
	}
	s.writeError(w, r, http.StatusServiceUnavailable, "execution_unavailable", "execution service is unavailable", true, nil)
	return false
}

func (s *Server) executionScope(r *http.Request) executionservice.Scope {
	principal := r.Context().Value(principalKey{}).(auth.Principal)
	return executionservice.Scope{PrincipalID: principal.ID, Mode: s.cfg.Mode}
}

func (s *Server) decodeStrictBody(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed execution request", false, nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed execution request", false, nil)
		return false
	}
	return true
}

func (s *Server) decodeEmptyBody(w http.ResponseWriter, r *http.Request) bool {
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var body struct{}
	err := decoder.Decode(&body)
	if errors.Is(err, io.EOF) {
		return true
	}
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed close request", false, nil)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "malformed close request", false, nil)
		return false
	}
	return true
}

func (s *Server) parsePositiveDecimal(
	w http.ResponseWriter,
	r *http.Request,
	field string,
	raw string,
) (decimal.Decimal, bool) {
	value, err := decimal.NewFromString(strings.TrimSpace(raw))
	if err != nil || !value.IsPositive() {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid decimal amount", false, map[string]any{"field": field})
		return decimal.Zero, false
	}
	return value, true
}

func (s *Server) idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 256 {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "valid idempotency key is required", false, map[string]any{"header": "Idempotency-Key"})
		return "", false
	}
	return key, true
}

func (s *Server) writeExecutionResult(w http.ResponseWriter, r *http.Request, result executionservice.Result) {
	status := result.ResponseCode
	if status == 0 {
		status = http.StatusAccepted
	}
	responseMeta := meta(r)
	responseMeta["replayed"] = result.Replayed
	responseMeta["degraded"] = result.Degraded
	responseMeta["recoverable"] = result.Recoverable
	s.writeJSON(w, r, status, map[string]any{
		"data": newExecutionResultDTO(result),
		"meta": responseMeta,
	})
}

func (s *Server) writeExecutionResultOrError(w http.ResponseWriter, r *http.Request, result executionservice.Result, err error) {
	if err == nil {
		s.writeExecutionResult(w, r, result)
		return
	}
	if strings.TrimSpace(newExecutionResultDTO(result).ExecutionID) == "" {
		s.writeExecutionError(w, r, err)
		return
	}
	result.ResponseCode = http.StatusAccepted
	result.Degraded = true
	result.Recoverable = true
	s.writeExecutionResult(w, r, result)
}

func (s *Server) writeExecutionError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, executionservice.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "execution_not_found", "execution was not found", false, nil)
	case errors.Is(err, executionservice.ErrIdempotencyConflict):
		s.writeError(w, r, http.StatusConflict, "idempotency_conflict", "idempotency key conflicts with the original request", false, nil)
	case errors.Is(err, executionservice.ErrPreviewConflict),
		errors.Is(err, executionservice.ErrPreviewMismatch),
		errors.Is(err, executionservice.ErrPreviewExpired):
		s.writeError(w, r, http.StatusConflict, "preview_conflict", "execution preview is no longer valid", false, nil)
	case errors.Is(err, executionservice.ErrBlockedPreview):
		s.writeError(w, r, http.StatusUnprocessableEntity, "preview_blocked", "execution preview is blocked", false, nil)
	case errors.Is(err, executionservice.ErrExecutionInProgress):
		s.writeError(w, r, http.StatusConflict, "execution_in_progress", "execution request is already in progress", true, nil)
	case errors.Is(err, executionservice.ErrNotReady),
		errors.Is(err, executionservice.ErrRecoveryInProgress),
		errors.Is(err, executionservice.ErrServiceUnavailable):
		s.writeError(w, r, http.StatusServiceUnavailable, "execution_unavailable", "execution service is unavailable", true, nil)
	default:
		s.cfg.Logger.Error("execution_request_failed",
			"requestId", requestID(r),
			"errorType", fmt.Sprintf("%T", err),
		)
		s.writeError(w, r, http.StatusServiceUnavailable, "execution_unavailable", "execution service is unavailable", true, nil)
	}
}

const maxHTTPPageLimit = 100

func (s *Server) parsePageQuery(w http.ResponseWriter, r *http.Request) (readmodel.PageQuery, bool) {
	limit := 50
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n < 1 || n > maxHTTPPageLimit {
			s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid limit", false, map[string]any{"field": "limit"})
			return readmodel.PageQuery{}, false
		}
		limit = n
	}
	return readmodel.PageQuery{Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")), Limit: limit}, true
}

func (s *Server) parseOptionalTime(w http.ResponseWriter, r *http.Request, field string) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(field))
	if raw == "" {
		return time.Time{}, true
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid RFC3339 timestamp", false, map[string]any{"field": field})
		return time.Time{}, false
	}
	return value.UTC(), true
}

func (s *Server) requireReadModel(w http.ResponseWriter, r *http.Request, resource string) bool {
	if s.cfg.ReadModel != nil {
		return true
	}
	s.writeError(w, r, http.StatusServiceUnavailable, "read_model_unavailable", "read model service is unavailable", true, map[string]any{"resource": resource})
	return false
}

func (s *Server) writeReadModelError(w http.ResponseWriter, r *http.Request, resource string, err error) {
	switch {
	case errors.Is(err, readmodel.ErrNotFound):
		s.writeError(w, r, http.StatusNotFound, "not_found", "read model item was not found", false, map[string]any{"resource": resource})
	case errors.Is(err, readmodel.ErrInvalidCursor):
		s.writeError(w, r, http.StatusBadRequest, "invalid_cursor", "cursor is invalid for this request", false, map[string]any{"resource": resource})
	default:
		s.cfg.Logger.Error("read_model_query_failed", "resource", resource, "requestId", requestID(r), "error", err)
		s.writeError(w, r, http.StatusServiceUnavailable, "read_model_error", "read model query failed", true, map[string]any{"resource": resource})
	}
}

func (s *Server) writePage(w http.ResponseWriter, r *http.Request, page readmodel.Page) {
	items := page.Items
	if items == nil {
		items = []json.RawMessage{}
	}
	responseMeta := map[string]any{
		"nextCursor": page.NextCursor,
		"hasMore":    page.NextCursor != "",
		"requestId":  requestID(r),
	}
	if !page.AsOf.IsZero() {
		responseMeta["asOf"] = page.AsOf.UTC().Format(time.RFC3339Nano)
	}
	if page.Counts != nil {
		responseMeta["counts"] = page.Counts
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"data": items, "meta": responseMeta})
}

func (s *Server) readScope(r *http.Request) readmodel.Scope {
	principal := r.Context().Value(principalKey{}).(auth.Principal)
	return readmodel.Scope{PrincipalID: principal.ID, Mode: s.cfg.Mode}
}

func nonEmptyValues(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

var streamKinds = map[string]struct{}{
	string(readmodel.SystemStatus):                      {},
	string(readmodel.OpportunityUpsert):                 {},
	string(readmodel.AlertUpsert):                       {},
	string(readmodel.ExecutionUpsert):                   {},
	string(readmodel.PositionUpsert):                    {},
	string(readmodel.ActivityAppend):                    {},
	string(readmodel.AuditAppend):                       {},
	string(readmodel.ResnapshotRequired):                {},
	string(readmodel.LedgerFundingPaymentAppend):        {},
	string(readmodel.LedgerPositionVerificationChanged): {},
	string(readmodel.LedgerPortfolioUpdated):            {},
	string(readmodel.VerificationStatus):                {},
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stream == nil {
		s.writeError(w, r, http.StatusServiceUnavailable, "stream_unavailable", "realtime stream is unavailable", true, nil)
		return
	}
	kinds := nonEmptyValues(r.URL.Query()["kind"])
	for _, kind := range kinds {
		if _, ok := streamKinds[kind]; !ok {
			s.writeError(w, r, http.StatusBadRequest, "bad_request", "invalid stream event kind", false, map[string]any{"field": "kind", "value": kind})
			return
		}
	}
	token, _ := r.Context().Value(tokenKey{}).(string)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	sink := &sseSink{writer: w, auth: s.cfg.Auth, token: token}
	err := s.cfg.Stream.Serve(r.Context(), sink, uistream.Request{
		Scope:  "all",
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
		Kinds:  kinds,
	})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, auth.ErrInvalidSession) {
		s.cfg.Logger.Error("stream_closed", "requestId", requestID(r), "error", err)
	}
}

type sseSink struct {
	writer http.ResponseWriter
	auth   authService
	token  string
}

func (sink *sseSink) Send(ctx context.Context, event uistream.StreamEvent) error {
	if _, err := sink.auth.CurrentSession(ctx, sink.token); err != nil {
		return auth.ErrInvalidSession
	}
	name := event.Name
	if name == uistream.EventData && event.Kind != "" {
		name = event.Kind
	}
	if strings.ContainsAny(name, "\r\n") || strings.ContainsAny(event.Cursor, "\r\n") {
		return errors.New("invalid SSE event metadata")
	}
	data, err := streamEventData(event)
	if err != nil {
		return err
	}
	if event.Cursor != "" {
		if _, err := fmt.Fprintf(sink.writer, "id: %s\n", event.Cursor); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(sink.writer, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	return http.NewResponseController(sink.writer).Flush()
}

func streamEventData(event uistream.StreamEvent) ([]byte, error) {
	envelope := map[string]any{"cursor": event.Cursor}
	if event.PrincipalID != "" {
		envelope["principalId"] = event.PrincipalID
	}
	if event.Mode != "" {
		envelope["mode"] = event.Mode
	}
	if event.ContractVersion != "" {
		envelope["contractVersion"] = event.ContractVersion
	}
	if !event.OccurredAt.IsZero() {
		envelope["occurredAt"] = event.OccurredAt.UTC().Format(time.RFC3339Nano)
	}
	if len(event.Data) > 0 {
		publicData, err := publicStreamPayload(event)
		if err != nil {
			return nil, err
		}
		var payload any
		if err := json.Unmarshal(publicData, &payload); err != nil {
			return nil, err
		}
		if event.Name == uistream.EventResnapshotRequired || event.Kind == uistream.EventResnapshotRequired {
			object, ok := payload.(map[string]any)
			if !ok {
				return nil, errors.New("resnapshot payload must be an object")
			}
			for key, value := range object {
				envelope[key] = value
			}
		} else {
			envelope["data"] = payload
		}
	}
	return json.Marshal(envelope)
}

func publicStreamPayload(event uistream.StreamEvent) (json.RawMessage, error) {
	switch {
	case event.Kind == string(readmodel.ExecutionUpsert):
		return publicExecutionStreamPayload(event.Data)
	case event.Name == uistream.EventSnapshot:
		return publicSnapshotStreamPayload(event.Data)
	default:
		return append(json.RawMessage(nil), event.Data...), nil
	}
}

func publicExecutionStreamPayload(payload json.RawMessage) (json.RawMessage, error) {
	var identity struct {
		ID          string `json:"id"`
		ExecutionID string `json:"executionId"`
	}
	if err := json.Unmarshal(payload, &identity); err != nil {
		return nil, err
	}
	var dto executionResponseDTO
	switch {
	case strings.TrimSpace(identity.ID) != "":
		var snapshot execution.Execution
		if err := json.Unmarshal(payload, &snapshot); err != nil {
			return nil, err
		}
		dto = newExecutionResponseDTO(snapshot)
	case strings.TrimSpace(identity.ExecutionID) != "":
		if err := json.Unmarshal(payload, &dto); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("execution stream payload is missing identity")
	}
	return json.Marshal(dto)
}

func publicSnapshotStreamPayload(payload json.RawMessage) (json.RawMessage, error) {
	var snapshot map[string]json.RawMessage
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	rawExecutions, ok := snapshot["executions"]
	if !ok || string(rawExecutions) == "null" {
		return append(json.RawMessage(nil), payload...), nil
	}
	var executions []json.RawMessage
	if err := json.Unmarshal(rawExecutions, &executions); err != nil {
		return nil, err
	}
	publicExecutions := make([]json.RawMessage, 0, len(executions))
	for _, item := range executions {
		publicItem, err := publicExecutionStreamPayload(item)
		if err != nil {
			return nil, err
		}
		publicExecutions = append(publicExecutions, publicItem)
	}
	encodedExecutions, err := json.Marshal(publicExecutions)
	if err != nil {
		return nil, err
	}
	snapshot["executions"] = encodedExecutions
	return json.Marshal(snapshot)
}

func (s *Server) writeJSON(w http.ResponseWriter, _ *http.Request, status int, body any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func mustValidateProductMode(mode string) {
	if mode != "test" && mode != "live" {
		panic("httpapi: product mode must be test or live")
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	s.writeJSON(w, r, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details, "retryable": retryable, "requestId": requestID(r)}})
}

func meta(r *http.Request) map[string]any {
	return map[string]any{"requestId": requestID(r)}
}

func requestID(r *http.Request) string {
	value, _ := r.Context().Value(requestIDKey{}).(string)
	return value
}

func principalPayload(principal auth.Principal) map[string]any {
	return map[string]any{"principalId": principal.ID, "mode": principal.Mode, "lastLoginAt": formatOptionalTime(principal.LastLoginAt)}
}

func sessionPayload(session auth.Session) map[string]any {
	return map[string]any{"authenticated": true, "expiresAt": session.ExpiresAt.UTC().Format(time.RFC3339), "csrfToken": session.CSRFToken}
}

func formatOptionalTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func clearSessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func randomRequestID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format(time.RFC3339Nano), ":", "")
	}
	return hex.EncodeToString(buf[:])
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}
