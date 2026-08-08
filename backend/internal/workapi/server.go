package workapi

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	frozencontract "github.com/QuantProcessing/boltertrader/backend/internal/contract"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

const ContractVersion = "boltertrader-product-platform-upgrade/v4"

type Options struct {
	TokenDigest     [sha256.Size]byte
	Mode            string
	ContractVersion string
	Service         *work.Service
	WorkerValidator WorkerValidator
	OnReadiness     func(bool)
}

// WorkerValidator is the narrow platform/work interface consumed by workapi for
// current-worker fencing. backend/internal/platform/work owns the implementation
// semantics; workapi maps validation failures onto the frozen HTTP error surface.
type WorkerValidator interface {
	ValidateWorker(workerID string, generation int, instanceID string) error
}

type handler struct {
	tokenDigest     [sha256.Size]byte
	mode            string
	contract        frozencontract.Contract
	mux             *http.ServeMux
	service         *work.Service
	workerValidator WorkerValidator
	onReadiness     func(bool)
}

func New(options Options) (http.Handler, error) {
	if options.TokenDigest == ([sha256.Size]byte{}) {
		return nil, configErr("token digest is required")
	}
	switch options.Mode {
	case "test", "live":
	default:
		return nil, configErr("invalid mode")
	}
	if options.ContractVersion != ContractVersion {
		return nil, configErr("contract version mismatch")
	}
	contract, err := frozencontract.Load()
	if err != nil {
		return nil, err
	}
	service := options.Service
	if service == nil {
		service = work.New(ContractVersion, 15*time.Second)
	}
	workerValidator := options.WorkerValidator
	if workerValidator == nil {
		if serviceValidator, ok := any(service).(WorkerValidator); ok {
			workerValidator = serviceValidator
		}
	}
	h := &handler{tokenDigest: options.TokenDigest, mode: options.Mode, contract: contract, mux: http.NewServeMux(), service: service, workerValidator: workerValidator, onReadiness: options.OnReadiness}
	h.register()
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeError(w, http.StatusUnauthorized, "WORKER_AUTH_REQUIRED", "worker authorization required", map[string]any{"listener": "platform-funding-work"}, false, requestID(r))
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) authorized(r *http.Request) bool {
	scheme, token, ok := splitBearerAuthorization(r.Header.Get("Authorization"))
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return false
	}
	sum := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(sum[:], h.tokenDigest[:]) == 1
}

func (h *handler) register() {
	for _, listener := range h.contract.Listeners {
		if listener.ID != "platform-funding-work" {
			continue
		}
		for _, route := range listener.Routes {
			route := route
			h.mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
				h.route(w, r, route)
			})
		}
	}
}

func (h *handler) route(w http.ResponseWriter, r *http.Request, route string) {
	switch route {
	case "GET /internal/v1/funding-work/status":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Status()})
	case "POST /internal/v1/funding-work/workers/registrations":
		req, ok := decodeStrict[registrationRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if req.Mode != h.mode || req.ProductID != "funding" || req.ContractVersion != ContractVersion {
			writeError(w, http.StatusForbidden, "WORKER_SCOPE_DENIED", "worker scope denied", nil, false, requestID(r))
			return
		}
		if !requireIdempotencyHeader(w, r, "") {
			return
		}
		workRequest := work.RegistrationRequest{
			WorkerID: h.workerKey(req.WorkerID), InstanceID: req.InstanceID, ProductID: req.ProductID,
			Mode: req.Mode, ContractVersion: req.ContractVersion, IdempotencyKey: strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		}
		registration, err := h.service.RegisterResult(workRequest)
		if err != nil {
			writeWorkError(w, r, err, nil)
			return
		}
		registration.WorkerID = req.WorkerID
		writeJSON(w, http.StatusCreated, map[string]any{"meta": meta(r), "data": registration})
	case "PUT /internal/v1/funding-work/workers/{workerId}/heartbeat":
		req, ok := decodeStrict[heartbeatRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		rawWorkerID := r.PathValue("workerId")
		if !h.validateWorkerHeaders(w, r, rawWorkerID, req.WorkerGeneration, req.InstanceID) {
			return
		}
		scopedWorkerID := h.workerKey(rawWorkerID)
		registration, err := h.service.Heartbeat(scopedWorkerID, work.HeartbeatRequest(req))
		if err != nil {
			writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation is stale", nil, false, requestID(r))
			return
		}
		registration.WorkerID = rawWorkerID
		if h.onReadiness != nil {
			h.onReadiness(h.service.Readiness(scopedWorkerID).CanClaim)
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": registration})
	case "GET /internal/v1/funding-work/workers/{workerId}/readiness":
		rawWorkerID := r.PathValue("workerId")
		if !h.validateWorkerHeaders(w, r, rawWorkerID, 0, "") {
			return
		}
		scopedWorkerID := h.workerKey(rawWorkerID)
		ready := h.service.Readiness(scopedWorkerID)
		ready.WorkerID = rawWorkerID
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": ready})
	case "POST /internal/v1/funding-work/workers/{workerId}/drain":
		req, ok := decodeStrict[drainRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		rawWorkerID := r.PathValue("workerId")
		if !h.validateWorkerHeaders(w, r, rawWorkerID, req.WorkerGeneration, req.InstanceID) || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		scopedWorkerID := h.workerKey(rawWorkerID)
		registration, err := h.service.Drain(scopedWorkerID, work.DrainRequest(req))
		if err != nil {
			writeWorkError(w, r, err, nil)
			return
		}
		registration.WorkerID = rawWorkerID
		if h.onReadiness != nil {
			h.onReadiness(false)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"meta": meta(r), "data": registration})
	case "POST /internal/v1/funding-work/claims":
		req, ok := decodeStrict[claimRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if req.MaxItems != 1 || req.WaitSeconds < 0 || req.WaitSeconds > 20 {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"maxItems": "must equal 1"}, false, requestID(r))
			return
		}
		if !h.validateWorkerHeaders(w, r, req.WorkerID, req.WorkerGeneration, req.InstanceID) || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		req.WorkerID = h.workerKey(req.WorkerID)
		page, err := h.service.ClaimPage(work.ClaimRequest(req))
		if err != nil {
			writeWorkError(w, r, err, nil)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": page})
	case "GET /internal/v1/funding-work/facts":
		if !h.validateWorkerHeaders(w, r, "", 0, "") {
			return
		}
		h.writeFactPage(w, r, false)
	case "GET /internal/v1/funding-work/facts/snapshot":
		if !h.validateWorkerHeaders(w, r, "", 0, "") {
			return
		}
		h.writeFactPage(w, r, true)
	case "POST /internal/v1/funding-work/leases/{leaseId}/renew":
		req, ok := decodeStrict[leaseRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !h.validateWorkerHeaders(w, r, req.WorkerID, req.WorkerGeneration, "") || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		req.WorkerID = h.workerKey(req.WorkerID)
		data, err := h.service.RenewResult(r.PathValue("leaseId"), work.LeaseRequest(req))
		writeWorkResult(w, r, data, err, req.WorkItemID, r.PathValue("leaseId"))
	case "POST /internal/v1/funding-work/leases/{leaseId}/command-intents":
		req, ok := decodeStrict[intentRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "intent."+req.IntentKind, req.Payload) {
			return
		}
		if !h.validateWorkerHeaders(w, r, req.WorkerID, req.WorkerGeneration, "") || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		req.WorkerID = h.workerKey(req.WorkerID)
		data, err := h.service.IntentResult(r.PathValue("leaseId"), work.IntentRequest(req))
		writeWorkResult(w, r, data, err, req.WorkItemID, r.PathValue("leaseId"))
	case "POST /internal/v1/funding-work/leases/{leaseId}/complete":
		req, ok := decodeStrict[completeRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "complete.result", req.Result) {
			return
		}
		if !h.validateWorkerHeaders(w, r, req.WorkerID, req.WorkerGeneration, "") || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		req.WorkerID = h.workerKey(req.WorkerID)
		data, err := h.service.CompleteResult(r.PathValue("leaseId"), work.CompleteRequest(req))
		writeWorkResult(w, r, data, err, req.WorkItemID, r.PathValue("leaseId"))
	case "POST /internal/v1/funding-work/leases/{leaseId}/fail":
		req, ok := decodeStrict[failRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "fail.details", req.Details) {
			return
		}
		if !h.validateWorkerHeaders(w, r, req.WorkerID, req.WorkerGeneration, "") || !requireIdempotencyHeader(w, r, req.IdempotencyKey) {
			return
		}
		req.WorkerID = h.workerKey(req.WorkerID)
		data, err := h.service.FailResult(r.PathValue("leaseId"), work.FailRequest(req))
		writeWorkResult(w, r, data, err, req.WorkItemID, r.PathValue("leaseId"))
	default:
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"route": route, "scaffold": true}})
	}
}

type registrationRequest struct {
	WorkerID        string `json:"workerId"`
	InstanceID      string `json:"instanceId"`
	ProductID       string `json:"productId"`
	Mode            string `json:"mode"`
	ContractVersion string `json:"contractVersion"`
}

type heartbeatRequest struct {
	WorkerGeneration       int      `json:"workerGeneration"`
	InstanceID             string   `json:"instanceId"`
	State                  string   `json:"state"`
	ActiveLeaseIDs         []string `json:"activeLeaseIds"`
	LastAppliedFactsCursor string   `json:"lastAppliedFactsCursor"`
}

type drainRequest struct {
	WorkerGeneration int    `json:"workerGeneration"`
	InstanceID       string `json:"instanceId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	Reason           string `json:"reason"`
}

type claimRequest struct {
	WorkerID         string `json:"workerId"`
	WorkerGeneration int    `json:"workerGeneration"`
	InstanceID       string `json:"instanceId"`
	IdempotencyKey   string `json:"idempotencyKey"`
	AfterCursor      string `json:"afterCursor"`
	WaitSeconds      int    `json:"waitSeconds"`
	MaxItems         int    `json:"maxItems"`
}

type leaseRequest struct {
	WorkerID         string `json:"workerId"`
	WorkerGeneration int    `json:"workerGeneration"`
	WorkItemID       string `json:"workItemId"`
	WorkRevision     int    `json:"workRevision"`
	LeaseGeneration  int    `json:"leaseGeneration"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type intentRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	IntentKind       string          `json:"intentKind"`
	FactsCursor      string          `json:"factsCursor"`
	AccountFences    json.RawMessage `json:"accountFences"`
	Payload          json.RawMessage `json:"payload"`
}

type completeRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	FactsCursor      string          `json:"factsCursor"`
	Result           json.RawMessage `json:"result"`
}

type failRequest struct {
	WorkerID         string          `json:"workerId"`
	WorkerGeneration int             `json:"workerGeneration"`
	WorkItemID       string          `json:"workItemId"`
	WorkRevision     int             `json:"workRevision"`
	LeaseGeneration  int             `json:"leaseGeneration"`
	IdempotencyKey   string          `json:"idempotencyKey"`
	FactsCursor      string          `json:"factsCursor"`
	ErrorCode        string          `json:"errorCode"`
	Retryable        bool            `json:"retryable"`
	Details          json.RawMessage `json:"details"`
}

func decodeStrict[T any](w http.ResponseWriter, r *http.Request, contract frozencontract.Contract, route string) (T, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		var zero T
		return zero, false
	}
	if err := contract.ValidateMutation(route, normalizedBody(body)); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		var zero T
		return zero, false
	}
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	var req T
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		var zero T
		return zero, false
	}
	return req, true
}

func validateNested(w http.ResponseWriter, r *http.Request, contract frozencontract.Contract, name string, body []byte) bool {
	if err := contract.ValidateNested(name, normalizedBody(body)); err != nil {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
		return false
	}
	return true
}

func splitBearerAuthorization(header string) (scheme, token string, ok bool) {
	if header == "" || strings.TrimSpace(header) != header {
		return "", "", false
	}
	parts := strings.Split(header, " ")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func requireIdempotencyHeader(w http.ResponseWriter, r *http.Request, bodyKey string) bool {
	headerKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if headerKey == "" {
		writeError(w, http.StatusBadRequest, "IDEMPOTENCY_REQUIRED", "idempotency key is required", nil, false, requestID(r))
		return false
	}
	if bodyKey != "" && headerKey != bodyKey {
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "idempotency key mismatch", nil, false, requestID(r))
		return false
	}
	return true
}

func (h *handler) validateWorkerHeaders(w http.ResponseWriter, r *http.Request, bodyWorkerID string, bodyGeneration int, bodyInstanceID string) bool {
	headerWorkerID := strings.TrimSpace(r.Header.Get("X-Worker-Id"))
	headerGeneration := strings.TrimSpace(r.Header.Get("X-Worker-Generation"))
	headerInstanceID := strings.TrimSpace(r.Header.Get("X-Worker-Instance-Id"))
	if headerWorkerID == "" || headerGeneration == "" || headerInstanceID == "" {
		writeError(w, http.StatusBadRequest, "WORKER_FENCE_REQUIRED", "worker fencing headers are required", nil, false, requestID(r))
		return false
	}
	generation, err := strconv.Atoi(headerGeneration)
	if err != nil || generation <= 0 {
		writeError(w, http.StatusBadRequest, "WORKER_FENCE_REQUIRED", "worker generation header is invalid", nil, false, requestID(r))
		return false
	}
	if bodyWorkerID != "" && headerWorkerID != bodyWorkerID {
		writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker id header mismatch", nil, false, requestID(r))
		return false
	}
	if bodyGeneration != 0 && generation != bodyGeneration {
		writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation header mismatch", nil, false, requestID(r))
		return false
	}
	if bodyInstanceID != "" && headerInstanceID != bodyInstanceID {
		writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker instance header mismatch", nil, false, requestID(r))
		return false
	}
	if h.workerValidator != nil {
		if err := h.workerValidator.ValidateWorker(h.workerKey(headerWorkerID), generation, headerInstanceID); err != nil {
			writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation is stale", nil, false, requestID(r))
			return false
		}
	}
	return true
}

func (h *handler) workerKey(workerID string) string {
	return h.mode + "\x00" + strings.TrimSpace(workerID)
}

func (h *handler) writeFactPage(w http.ResponseWriter, r *http.Request, snapshot bool) {
	query := r.URL.Query()
	workerGeneration, generationErr := strconv.Atoi(strings.TrimSpace(r.Header.Get("X-Worker-Generation")))
	workRevision, revisionErr := strconv.Atoi(query.Get("workRevision"))
	leaseGeneration, leaseErr := strconv.Atoi(query.Get("leaseGeneration"))
	limit := 100
	if query.Get("limit") != "" {
		limit, _ = strconv.Atoi(query.Get("limit"))
	}
	request := work.FactRequest{
		WorkerID: h.workerKey(strings.TrimSpace(r.Header.Get("X-Worker-Id"))), WorkerGeneration: workerGeneration,
		WorkItemID: query.Get("workItemId"), WorkRevision: workRevision, LeaseID: query.Get("leaseId"),
		LeaseGeneration: leaseGeneration, AfterCursor: query.Get("afterCursor"), Limit: limit,
	}
	if generationErr != nil || revisionErr != nil || leaseErr != nil || request.WorkItemID == "" || request.LeaseID == "" || workRevision <= 0 || leaseGeneration <= 0 || limit <= 0 || limit > 500 {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, requestID(r))
		return
	}
	page, err := h.service.FactPage(request, snapshot)
	if err != nil {
		writeWorkError(w, r, err, map[string]any{"leaseId": request.LeaseID, "workItemId": request.WorkItemID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": page})
}

func writeWorkResult(w http.ResponseWriter, r *http.Request, data map[string]any, err error, workItemID, leaseID string) {
	if err != nil {
		writeWorkError(w, r, err, map[string]any{"leaseId": leaseID, "workItemId": workItemID})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": data})
}

func writeWorkError(w http.ResponseWriter, r *http.Request, err error, details map[string]any) {
	status, code, retryable := workHTTPError(err)
	writeError(w, status, code, err.Error(), details, retryable, requestID(r))
}

func workHTTPError(err error) (int, string, bool) {
	switch {
	case errors.Is(err, work.ErrWorkerAlreadyActive):
		return http.StatusConflict, "WORKER_ALREADY_ACTIVE", true
	case errors.Is(err, work.ErrWorkerDraining):
		return http.StatusConflict, "WORKER_DRAINING", false
	case errors.Is(err, work.ErrLeaseExpired):
		return http.StatusConflict, "WORK_LEASE_EXPIRED", false
	case errors.Is(err, work.ErrLeaseStale):
		return http.StatusConflict, "WORK_LEASE_STALE", false
	case errors.Is(err, work.ErrWorkRevisionConflict):
		return http.StatusConflict, "WORK_REVISION_CONFLICT", true
	case errors.Is(err, work.ErrAuthorizationStale):
		return http.StatusConflict, "WORK_AUTHORIZATION_STALE", true
	case errors.Is(err, work.ErrResnapshotRequired):
		return http.StatusConflict, "RESNAPSHOT_REQUIRED", true
	case errors.Is(err, work.ErrLiveWriteDisabled):
		return http.StatusForbidden, "LIVE_PRIVATE_WRITE_DISABLED", false
	case errors.Is(err, work.ErrAttemptsExhausted):
		return http.StatusConflict, "WORK_ATTEMPTS_EXHAUSTED", false
	case errors.Is(err, work.ErrIdempotencyConflict):
		return http.StatusConflict, "IDEMPOTENCY_CONFLICT", false
	case errors.Is(err, work.ErrWorkerNotRegistered), errors.Is(err, work.ErrWorkerStale):
		return http.StatusConflict, "WORKER_GENERATION_STALE", false
	case errors.Is(err, work.ErrWorkerNotReady), errors.Is(err, work.ErrDependencyUnavailable):
		return http.StatusServiceUnavailable, "WORKER_NOT_READY", true
	case errors.Is(err, work.ErrWorkNotFound):
		return http.StatusNotFound, "WORK_NOT_FOUND", false
	default:
		return http.StatusServiceUnavailable, "WORKER_NOT_READY", true
	}
}

func meta(r *http.Request) map[string]any {
	return map[string]any{"requestId": requestID(r), "contractVersion": ContractVersion, "apiVersion": "v1"}
}

func requestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	sum := sha256.Sum256([]byte(time.Now().UTC().String()))
	return "req_" + hex.EncodeToString(sum[:8])
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
