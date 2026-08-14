package httpapi

// The account lifecycle is served on the internal plane rather than the public
// one for two reasons. It accepts credential material, which must never reach a
// surface a browser can be pointed at; and the public /v1/accounts path already
// names a different resource — the runtime view of an account, with its
// readiness and reasons — which the OpenAPI contract pins. Auth-edge holds a
// service token, so reaching it here costs nothing.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
)

// AccountLifecycle is the slice of the lifecycle service this transport drives.
// It is an interface so the handler can be tested without a credential provider
// or a database behind it; the service itself is the only production
// implementation.
type AccountLifecycle interface {
	List(context.Context) ([]lifecycle.Account, error)
	Get(context.Context, string) (lifecycle.Account, error)
	Create(context.Context, lifecycle.CreateRequest, string, ...string) (lifecycle.Account, error)
	UpdateMetadata(context.Context, string, int64, string, []string, string) (lifecycle.Account, error)
	MarkValidated(context.Context, string, int64, string) (lifecycle.Account, error)
	Rotate(context.Context, string, int64, []byte, string, ...string) (lifecycle.Account, error)
	Disable(context.Context, string, int64, lifecycle.Blockers, string) (lifecycle.Account, error)
	Drain(context.Context, string, int64, lifecycle.Blockers, string) (lifecycle.Account, error)
	Remove(context.Context, string, int64, string, ...string) (lifecycle.Account, error)
}

var _ AccountLifecycle = (*lifecycle.Service)(nil)

// AccountBlockers answers what still holds an account open. Draining and
// disabling are refused while inventory exists, and the gateway cannot decide
// that alone, so the caller supplies the count.
type AccountBlockers func(context.Context, string) (lifecycle.Blockers, error)

const (
	accountPrincipalHeader   = "X-Bolter-Principal"
	accountRequestIDHeader   = "X-Request-ID"
	accountIdempotencyHeader = "Idempotency-Key"
)

var internalAccountLifecycleRoutes = []Route{
	{http.MethodGet, "/internal/v1/accounts"},
	{http.MethodPost, "/internal/v1/accounts"},
	{http.MethodGet, "/internal/v1/accounts/{accountId}"},
	{http.MethodPatch, "/internal/v1/accounts/{accountId}"},
	{http.MethodPost, "/internal/v1/accounts/{accountId}/validate"},
	{http.MethodPost, "/internal/v1/accounts/{accountId}/rotate"},
	{http.MethodPost, "/internal/v1/accounts/{accountId}/disable"},
	{http.MethodPost, "/internal/v1/accounts/{accountId}/drain"},
	{http.MethodDelete, "/internal/v1/accounts/{accountId}"},
}

// InternalAccountLifecycleRoutes reports the routes that only exist when a
// lifecycle service is configured. They are separate from InternalRoutes so a
// deployment without one does not advertise a surface it cannot serve.
func InternalAccountLifecycleRoutes() []Route {
	return append([]Route(nil), internalAccountLifecycleRoutes...)
}

func (handler *InternalHandler) registerAccountLifecycle() {
	handler.mux.HandleFunc("GET /internal/v1/accounts", handler.listAccountRecords)
	handler.mux.HandleFunc("POST /internal/v1/accounts", handler.createAccountRecord)
	handler.mux.HandleFunc("GET /internal/v1/accounts/{accountId}", handler.getAccountRecord)
	handler.mux.HandleFunc("PATCH /internal/v1/accounts/{accountId}", handler.updateAccountRecord)
	handler.mux.HandleFunc("POST /internal/v1/accounts/{accountId}/validate", handler.validateAccountRecord)
	handler.mux.HandleFunc("POST /internal/v1/accounts/{accountId}/rotate", handler.rotateAccountRecord)
	handler.mux.HandleFunc("POST /internal/v1/accounts/{accountId}/disable", handler.disableAccountRecord)
	handler.mux.HandleFunc("POST /internal/v1/accounts/{accountId}/drain", handler.drainAccountRecord)
	handler.mux.HandleFunc("DELETE /internal/v1/accounts/{accountId}", handler.removeAccountRecord)
}

type createAccountRecordRequest struct {
	Venue            string                   `json:"venue"`
	DisplayName      string                   `json:"displayName"`
	ProductPolicy    []string                 `json:"productPolicy"`
	Routes           []lifecycle.AccountRoute `json:"routes"`
	CredentialBundle json.RawMessage          `json:"credentialBundle"`
}

type updateAccountRecordRequest struct {
	DisplayName string   `json:"displayName"`
	ProductIDs  []string `json:"productIds"`
}

type rotateAccountRecordRequest struct {
	CredentialBundle json.RawMessage `json:"credentialBundle"`
}

func (handler *InternalHandler) listAccountRecords(w http.ResponseWriter, r *http.Request) {
	if _, ok := handler.accountPrincipal(w, r); !ok {
		return
	}
	accounts, err := handler.lifecycle.List(r.Context())
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	public := make([]lifecycle.PublicAccount, 0, len(accounts))
	for _, account := range accounts {
		public = append(public, account.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"accounts": public}})
}

func (handler *InternalHandler) getAccountRecord(w http.ResponseWriter, r *http.Request) {
	if _, ok := handler.accountPrincipal(w, r); !ok {
		return
	}
	accountID, ok := handler.accountRecordID(w, r)
	if !ok {
		return
	}
	account, err := handler.lifecycle.Get(r.Context(), accountID)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, account)
}

func (handler *InternalHandler) createAccountRecord(w http.ResponseWriter, r *http.Request) {
	principal, ok := handler.accountPrincipal(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := handler.accountIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request createAccountRecordRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &request); err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	defer clear(request.CredentialBundle)
	if err := validateCredentialBundle(request.CredentialBundle); err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	account, err := handler.lifecycle.Create(handler.accountContext(r), lifecycle.CreateRequest{
		Venue:            request.Venue,
		DisplayName:      request.DisplayName,
		ProductPolicy:    request.ProductPolicy,
		Routes:           request.Routes,
		CredentialBundle: request.CredentialBundle,
	}, principal, idempotencyKey)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusCreated, account)
}

func (handler *InternalHandler) updateAccountRecord(w http.ResponseWriter, r *http.Request) {
	principal, accountID, revision, ok := handler.accountRecordMutation(w, r)
	if !ok {
		return
	}
	var request updateAccountRecordRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &request); err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	account, err := handler.lifecycle.UpdateMetadata(handler.accountContext(r), accountID, revision, request.DisplayName, request.ProductIDs, principal)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, account)
}

func (handler *InternalHandler) validateAccountRecord(w http.ResponseWriter, r *http.Request) {
	principal, accountID, revision, ok := handler.accountRecordMutation(w, r)
	if !ok {
		return
	}
	if !handler.expectEmptyAccountBody(w, r) {
		return
	}
	blockers, ok := handler.accountBlockers(w, r, accountID)
	if !ok {
		return
	}
	// Validation drives a live credential probe against the venue. Doing that
	// while the account still carries inventory would let a half-checked key be
	// promoted underneath open orders.
	if blockers.Any() {
		handler.accountFailure(w, r, lifecycle.ErrAccountBlocked)
		return
	}
	account, err := handler.lifecycle.MarkValidated(handler.accountContext(r), accountID, revision, principal)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, account)
}

func (handler *InternalHandler) rotateAccountRecord(w http.ResponseWriter, r *http.Request) {
	principal, accountID, revision, ok := handler.accountRecordMutation(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := handler.accountIdempotencyKey(w, r)
	if !ok {
		return
	}
	var request rotateAccountRecordRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &request); err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	defer clear(request.CredentialBundle)
	if err := validateCredentialBundle(request.CredentialBundle); err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	account, err := handler.lifecycle.Rotate(handler.accountContext(r), accountID, revision, request.CredentialBundle, principal, idempotencyKey)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, account)
}

func (handler *InternalHandler) disableAccountRecord(w http.ResponseWriter, r *http.Request) {
	handler.blockedAccountTransition(w, r, handler.lifecycle.Disable)
}

func (handler *InternalHandler) drainAccountRecord(w http.ResponseWriter, r *http.Request) {
	handler.blockedAccountTransition(w, r, handler.lifecycle.Drain)
}

// blockedAccountTransition hands the blockers to the service instead of
// refusing here: disable and drain are how an operator responds to inventory
// they cannot clear, so the service decides which counts stop which transition.
func (handler *InternalHandler) blockedAccountTransition(w http.ResponseWriter, r *http.Request, transition func(context.Context, string, int64, lifecycle.Blockers, string) (lifecycle.Account, error)) {
	principal, accountID, revision, ok := handler.accountRecordMutation(w, r)
	if !ok {
		return
	}
	if !handler.expectEmptyAccountBody(w, r) {
		return
	}
	blockers, ok := handler.accountBlockers(w, r, accountID)
	if !ok {
		return
	}
	account, err := transition(handler.accountContext(r), accountID, revision, blockers, principal)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, account)
}

func (handler *InternalHandler) removeAccountRecord(w http.ResponseWriter, r *http.Request) {
	principal, accountID, revision, ok := handler.accountRecordMutation(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := handler.accountIdempotencyKey(w, r)
	if !ok {
		return
	}
	if !handler.expectEmptyAccountBody(w, r) {
		return
	}
	blockers, ok := handler.accountBlockers(w, r, accountID)
	if !ok {
		return
	}
	// Removal deletes the credential. Unlike drain, there is no state in which
	// it is the right answer to inventory that still exists.
	if blockers.Any() {
		handler.accountFailure(w, r, lifecycle.ErrAccountBlocked)
		return
	}
	account, err := handler.lifecycle.Remove(handler.accountContext(r), accountID, revision, principal, idempotencyKey)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	// Removal is accepted, not done: the credential is only deleted once the
	// runtime confirms the account is tombstoned.
	handler.writeAccountRecord(w, http.StatusAccepted, account)
}

func (handler *InternalHandler) accountRecordMutation(w http.ResponseWriter, r *http.Request) (string, string, int64, bool) {
	principal, ok := handler.accountPrincipal(w, r)
	if !ok {
		return "", "", 0, false
	}
	accountID, ok := handler.accountRecordID(w, r)
	if !ok {
		return "", "", 0, false
	}
	revision, err := parseDesiredETag(r.Header.Get("If-Match"))
	if err != nil {
		handler.writeError(w, r, http.StatusPreconditionRequired, "IF_MATCH_REQUIRED", false)
		return "", "", 0, false
	}
	return principal, accountID, revision, true
}

func (handler *InternalHandler) accountBlockers(w http.ResponseWriter, r *http.Request, accountID string) (lifecycle.Blockers, bool) {
	blockers, err := handler.blockers(r.Context(), accountID)
	if err != nil {
		handler.writeError(w, r, http.StatusServiceUnavailable, "ACCOUNT_BLOCKERS_UNAVAILABLE", true)
		return lifecycle.Blockers{}, false
	}
	return blockers, true
}

// accountContext carries the request id into the service so the audit trail
// names the request that caused each transition.
func (handler *InternalHandler) accountContext(r *http.Request) context.Context {
	return lifecycle.WithRequestID(r.Context(), accountRequestID(r))
}

func (handler *InternalHandler) accountPrincipal(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal := r.Header.Get(accountPrincipalHeader)
	if !safeHeaderToken(principal, 128) {
		handler.writeError(w, r, http.StatusUnauthorized, "PRINCIPAL_REQUIRED", false)
		return "", false
	}
	return principal, true
}

func (handler *InternalHandler) accountIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(accountIdempotencyHeader))
	if key == "" || len(key) > 128 {
		handler.writeError(w, r, http.StatusBadRequest, "IDEMPOTENCY_KEY_REQUIRED", false)
		return "", false
	}
	return key, true
}

func (handler *InternalHandler) accountRecordID(w http.ResponseWriter, r *http.Request) (string, bool) {
	accountID := r.PathValue("accountId")
	// "desired" and "runtime" name sibling collections under this prefix, so a
	// record with either id could never be addressed. Rejecting it is honest
	// where serving it would silently route to the wrong resource.
	if !safeHeaderToken(accountID, 128) || accountID == "desired" || accountID == "runtime" {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return "", false
	}
	return accountID, true
}

func (handler *InternalHandler) expectEmptyAccountBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, handler.bodyLimit))
	if err != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return true
	}
	handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
	return false
}

func (handler *InternalHandler) writeAccountRecord(w http.ResponseWriter, status int, account lifecycle.Account) {
	w.Header().Set("ETag", desiredETag(account.DesiredStateRevision))
	writeJSON(w, status, map[string]any{"data": map[string]any{"account": account.Public()}})
}

func (handler *InternalHandler) accountFailure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, lifecycle.ErrNotFound):
		handler.writeError(w, r, http.StatusNotFound, "ACCOUNT_NOT_FOUND", false)
	case errors.Is(err, lifecycle.ErrRevisionConflict):
		handler.writeError(w, r, http.StatusConflict, "DESIRED_STATE_REVISION_CONFLICT", false)
	case errors.Is(err, lifecycle.ErrIdempotencyConflict):
		handler.writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", false)
	case errors.Is(err, lifecycle.ErrInvalidTransition):
		handler.writeError(w, r, http.StatusConflict, "INVALID_ACCOUNT_TRANSITION", false)
	case errors.Is(err, lifecycle.ErrAccountBlocked):
		handler.writeError(w, r, http.StatusConflict, "ACCOUNT_LIFECYCLE_BLOCKED", false)
	case errors.Is(err, lifecycle.ErrValidation):
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
	default:
		handler.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", true)
	}
}

// accountRequestID prefers the caller's request id so a lifecycle transition can
// be traced back through auth-edge, but only when it is shaped like an
// identifier: the value lands in the audit trail, which rejects anything that
// reads like a captured header.
func accountRequestID(r *http.Request) string {
	if candidate := r.Header.Get(accountRequestIDHeader); safeHeaderToken(candidate, 64) {
		return candidate
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "internal"
	}
	return "req_" + hex.EncodeToString(raw[:])
}

func safeHeaderToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '.', char == '_', char == '-', char == '@', char == ':':
		default:
			return false
		}
	}
	return true
}

var errCredentialBundleShape = errors.New("credential bundle shape is invalid")

// validateCredentialBundle refuses any key the credential decoder does not
// know. Everything it rejects is reported as one opaque error so a rejection
// never echoes back which credential field the caller got wrong.
func validateCredentialBundle(raw json.RawMessage) error {
	envelope, err := decodeCaseUniqueObject(raw)
	if err != nil {
		return errCredentialBundleShape
	}
	nestedKeys := map[string]map[string]struct{}{
		"metadata": credentialKeySet("accountIndex", "keyIndex", "accountAddress", "userAddress", "expectedSigner", "subaccount"),
		"secrets":  credentialKeySet("apiKey", "apiSecret", "passphrase", "privateKey", "apiWalletPrivateKey"),
	}
	allowed := credentialKeySet("schemaVersion", "venue", "authKind", "metadata", "secrets")
	for key, child := range envelope {
		if _, exists := allowed[key]; !exists {
			return errCredentialBundleShape
		}
		keys, requiresObject := nestedKeys[key]
		if !requiresObject {
			continue
		}
		nested, err := decodeCaseUniqueObject(child)
		if err != nil {
			return errCredentialBundleShape
		}
		for nestedKey := range nested {
			if _, exists := keys[nestedKey]; !exists {
				return errCredentialBundleShape
			}
		}
	}
	return nil
}

// decodeCaseUniqueObject rejects keys that differ only by case. decodeStrictJSON
// already refuses exact duplicates, but the credential decoder folds case, so
// two spellings of the same secret would decode to whichever one it happened to
// prefer rather than the one the submitter reviewed.
func decodeCaseUniqueObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errCredentialBundleShape
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errCredentialBundleShape
	}
	folded := make(map[string]struct{}, len(object))
	for key := range object {
		lowered := strings.ToLower(key)
		if _, exists := folded[lowered]; exists {
			return nil, errCredentialBundleShape
		}
		folded[lowered] = struct{}{}
	}
	return object, nil
}

func credentialKeySet(keys ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		set[key] = struct{}{}
	}
	return set
}
