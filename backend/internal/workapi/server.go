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
)

const ContractVersion = "boltertrader-product-platform-upgrade/v4"

type Options struct {
	TokenDigest     [sha256.Size]byte
	Mode            string
	ContractVersion string
}

type handler struct {
	tokenDigest [sha256.Size]byte
	mode        string
	contract    frozencontract.Contract
	mux         *http.ServeMux
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
	h := &handler{tokenDigest: options.TokenDigest, mode: options.Mode, contract: contract, mux: http.NewServeMux()}
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
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"mode": h.mode, "state": "scaffold"}})
	case "POST /internal/v1/funding-work/workers/registrations":
		req, ok := decodeStrict[registrationRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		if req.Mode != h.mode {
			writeError(w, http.StatusForbidden, "WORKER_SCOPE_DENIED", "worker scope denied", nil, false, requestID(r))
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"meta": meta(r), "data": map[string]any{"accepted": false, "reason": "scaffold"}})
	case "PUT /internal/v1/funding-work/workers/{workerId}/heartbeat":
		req, ok := decodeStrict[heartbeatRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"workerId": r.PathValue("workerId"), "workerGeneration": req.WorkerGeneration, "state": req.State}})
	case "GET /internal/v1/funding-work/workers/{workerId}/readiness":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"workerId": r.PathValue("workerId"), "canClaim": false, "state": "scaffold", "observedAt": time.Now().UTC().Format(time.RFC3339)}})
	case "POST /internal/v1/funding-work/workers/{workerId}/drain":
		_, ok := decodeStrict[drainRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"draining": true}})
	case "POST /internal/v1/funding-work/claims":
		_, ok := decodeStrict[claimRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": []any{}}})
	case "GET /internal/v1/funding-work/facts":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": []any{}, "cursor": "cursor_0001"}})
	case "GET /internal/v1/funding-work/facts/snapshot":
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"items": []any{}, "cursor": "cursor_0001", "snapshot": true}})
	case "POST /internal/v1/funding-work/leases/{leaseId}/renew":
		_, ok := decodeStrict[leaseRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"leaseId": r.PathValue("leaseId"), "renewed": true}})
	case "POST /internal/v1/funding-work/leases/{leaseId}/command-intents":
		_, ok := decodeStrict[intentRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"meta": meta(r), "data": map[string]any{"leaseId": r.PathValue("leaseId"), "accepted": true}})
	case "POST /internal/v1/funding-work/leases/{leaseId}/complete":
		_, ok := decodeStrict[completeRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"leaseId": r.PathValue("leaseId"), "completed": true}})
	case "POST /internal/v1/funding-work/leases/{leaseId}/fail":
		_, ok := decodeStrict[failRequest](w, r, h.contract, route)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"leaseId": r.PathValue("leaseId"), "failed": true}})
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
