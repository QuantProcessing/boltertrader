package workapi

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
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
	OnReadiness     func(bool)
}

type handler struct {
	tokenDigest [sha256.Size]byte
	mode        string
	contract    frozencontract.Contract
	mux         *http.ServeMux
	service     *work.Service
	onReadiness func(bool)
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
	h := &handler{tokenDigest: options.TokenDigest, mode: options.Mode, contract: contract, mux: http.NewServeMux(), service: service, onReadiness: options.OnReadiness}
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
	const prefix = "Bearer "
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(auth, prefix) {
		return false
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
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
		if req.Mode != h.mode {
			writeError(w, http.StatusForbidden, "WORKER_SCOPE_DENIED", "worker scope denied", nil, false, requestID(r))
			return
		}
		registration := h.service.Register(work.RegistrationRequest(req))
		if h.onReadiness != nil {
			h.onReadiness(true)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"meta": meta(r), "data": registration})
	case "PUT /internal/v1/funding-work/workers/{workerId}/heartbeat":
		req, ok := decodeStrict[heartbeatRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		registration, err := h.service.Heartbeat(r.PathValue("workerId"), work.HeartbeatRequest(req))
		if err != nil {
			writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation is stale", nil, false, requestID(r))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": registration})
	case "GET /internal/v1/funding-work/workers/{workerId}/readiness":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Readiness(r.PathValue("workerId"))})
	case "POST /internal/v1/funding-work/workers/{workerId}/drain":
		req, ok := decodeStrict[drainRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		registration, err := h.service.Drain(r.PathValue("workerId"), work.DrainRequest(req))
		if err != nil {
			writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation is stale", nil, false, requestID(r))
			return
		}
		if h.onReadiness != nil {
			h.onReadiness(false)
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": registration})
	case "POST /internal/v1/funding-work/claims":
		req, ok := decodeStrict[claimRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		items, err := h.service.Claim(work.ClaimRequest(req))
		if err != nil {
			writeError(w, http.StatusConflict, "WORKER_GENERATION_STALE", "worker generation is stale", nil, false, requestID(r))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": items}})
	case "GET /internal/v1/funding-work/facts":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Facts()})
	case "GET /internal/v1/funding-work/facts/snapshot":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Snapshot()})
	case "POST /internal/v1/funding-work/leases/{leaseId}/renew":
		req, ok := decodeStrict[leaseRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Renew(r.PathValue("leaseId"), work.LeaseRequest(req))})
	case "POST /internal/v1/funding-work/leases/{leaseId}/command-intents":
		req, ok := decodeStrict[intentRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "intent."+req.IntentKind, req.Payload) {
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"meta": meta(r), "data": h.service.Intent(r.PathValue("leaseId"), work.IntentRequest(req))})
	case "POST /internal/v1/funding-work/leases/{leaseId}/complete":
		req, ok := decodeStrict[completeRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "complete.result", req.Result) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Complete(r.PathValue("leaseId"), work.CompleteRequest(req))})
	case "POST /internal/v1/funding-work/leases/{leaseId}/fail":
		req, ok := decodeStrict[failRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if !validateNested(w, r, h.contract, "fail.details", req.Details) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": h.service.Fail(r.PathValue("leaseId"), work.FailRequest(req))})
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
