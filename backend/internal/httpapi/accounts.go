package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

type AccountRoutesOptions struct {
	Service           *platformaccounts.Service
	Authenticate      func(*http.Request) (string, error)
	AuthorizeMutation func(*http.Request) error
	Blockers          func(context.Context, string) (platformaccounts.Blockers, error)
}

func RegisterAccountRoutes(mux *http.ServeMux, options AccountRoutesOptions) error {
	if mux == nil || options.Service == nil || options.Authenticate == nil || options.AuthorizeMutation == nil || options.Blockers == nil {
		return configErr("account route dependencies are required")
	}
	routes := &accountRoutes{options: options}
	mux.HandleFunc("GET /api/v1/accounts", routes.list)
	mux.HandleFunc("POST /api/v1/accounts", routes.create)
	mux.HandleFunc("GET /api/v1/accounts/{accountId}", routes.get)
	mux.HandleFunc("PATCH /api/v1/accounts/{accountId}", routes.update)
	mux.HandleFunc("POST /api/v1/accounts/{accountId}/validate", routes.validate)
	mux.HandleFunc("POST /api/v1/accounts/{accountId}/rotate", routes.rotate)
	mux.HandleFunc("POST /api/v1/accounts/{accountId}/disable", routes.disable)
	mux.HandleFunc("POST /api/v1/accounts/{accountId}/drain", routes.drain)
	mux.HandleFunc("DELETE /api/v1/accounts/{accountId}", routes.remove)
	return nil
}

type accountRoutes struct{ options AccountRoutesOptions }

type createAccountRequest struct {
	Venue            string                       `json:"venue"`
	VenueEnvironment platformaccounts.Environment `json:"venueEnvironment"`
	DisplayName      string                       `json:"displayName"`
	ProductIDs       []string                     `json:"productIds"`
	SecretMaterial   json.RawMessage              `json:"secretMaterial"`
}

type updateAccountRequest struct {
	DisplayName string   `json:"displayName"`
	ProductIDs  []string `json:"productIds"`
}

type rotateAccountRequest struct {
	SecretMaterial json.RawMessage `json:"secretMaterial"`
}

func (routes *accountRoutes) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := routes.principal(w, r); !ok {
		return
	}
	accounts, err := routes.options.Service.List(r.Context())
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	public := make([]platformaccounts.PublicAccount, 0, len(accounts))
	for _, account := range accounts {
		public = append(public, account.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"meta": meta(r), "data": map[string]any{"accounts": public}})
}

func (routes *accountRoutes) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := routes.principal(w, r); !ok {
		return
	}
	account, err := routes.options.Service.Get(r.Context(), r.PathValue("accountId"))
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := routes.mutationPrincipal(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key is required", nil, false, requestID(r))
		return
	}
	var request createAccountRequest
	if err := decodeStrict(r, &request); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	account, err := routes.options.Service.Create(r.Context(), platformaccounts.CreateRequest{Venue: request.Venue, VenueEnvironment: request.VenueEnvironment, DisplayName: request.DisplayName, ProductIDs: request.ProductIDs, SecretMaterial: request.SecretMaterial}, principal)
	clear(request.SecretMaterial)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusCreated, account)
}

func (routes *accountRoutes) update(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	var request updateAccountRequest
	if err := decodeStrict(r, &request); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	account, err := routes.options.Service.UpdateMetadata(r.Context(), r.PathValue("accountId"), revision, request.DisplayName, request.ProductIDs, principal)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) validate(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	if err := decodeEmpty(r); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	blockers, err := routes.options.Blockers(r.Context(), r.PathValue("accountId"))
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	if blockers.Any() {
		routes.failure(w, r, platformaccounts.ErrAccountBlocked)
		return
	}
	account, err := routes.options.Service.MarkValidated(r.Context(), r.PathValue("accountId"), revision, principal)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) rotate(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	var request rotateAccountRequest
	if err := decodeStrict(r, &request); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	account, err := routes.options.Service.Rotate(r.Context(), r.PathValue("accountId"), revision, request.SecretMaterial, principal)
	clear(request.SecretMaterial)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) disable(w http.ResponseWriter, r *http.Request) {
	routes.blockedTransition(w, r, "disable")
}
func (routes *accountRoutes) drain(w http.ResponseWriter, r *http.Request) {
	routes.blockedTransition(w, r, "drain")
}

func (routes *accountRoutes) blockedTransition(w http.ResponseWriter, r *http.Request, action string) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	if err := decodeEmpty(r); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	blockers, err := routes.options.Blockers(r.Context(), r.PathValue("accountId"))
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	var account platformaccounts.Account
	if action == "disable" {
		account, err = routes.options.Service.Disable(r.Context(), r.PathValue("accountId"), revision, blockers, principal)
	} else {
		account, err = routes.options.Service.Drain(r.Context(), r.PathValue("accountId"), revision, blockers, principal)
	}
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) remove(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	if err := decodeEmpty(r); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	blockers, err := routes.options.Blockers(r.Context(), r.PathValue("accountId"))
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	if blockers.Any() {
		routes.failure(w, r, platformaccounts.ErrAccountBlocked)
		return
	}
	account, err := routes.options.Service.Remove(r.Context(), r.PathValue("accountId"), revision, principal)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.writeAccount(w, r, http.StatusAccepted, account)
}

func (routes *accountRoutes) principal(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, err := routes.options.Authenticate(r)
	if err != nil || principal == "" {
		writeError(w, http.StatusUnauthorized, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return "", false
	}
	return principal, true
}

func (routes *accountRoutes) mutationPrincipal(w http.ResponseWriter, r *http.Request) (string, bool) {
	principal, ok := routes.principal(w, r)
	if !ok {
		return "", false
	}
	if err := routes.options.AuthorizeMutation(r); err != nil {
		writeError(w, http.StatusForbidden, "AUTH_REQUIRED", "authentication required", nil, false, requestID(r))
		return "", false
	}
	*r = *r.WithContext(platformaccounts.WithRequestID(r.Context(), requestID(r)))
	return principal, true
}

func (routes *accountRoutes) existingMutation(w http.ResponseWriter, r *http.Request) (string, int64, bool) {
	principal, ok := routes.mutationPrincipal(w, r)
	if !ok {
		return "", 0, false
	}
	revision, err := ParseAccountETag(r.Header.Get("If-Match"))
	if err != nil {
		routes.validationFailure(w, r, err)
		return "", 0, false
	}
	return principal, revision, true
}

func AccountETag(revision int64) string { return fmt.Sprintf(`"desired-state-revision-%d"`, revision) }

func ParseAccountETag(value string) (int64, error) {
	value = strings.Trim(value, `"`)
	const prefix = "desired-state-revision-"
	if !strings.HasPrefix(value, prefix) {
		return 0, errors.New("If-Match is required")
	}
	revision, err := strconv.ParseInt(strings.TrimPrefix(value, prefix), 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("If-Match is invalid")
	}
	return revision, nil
}

func (routes *accountRoutes) writeAccount(w http.ResponseWriter, r *http.Request, status int, account platformaccounts.Account) {
	w.Header().Set("ETag", AccountETag(account.DesiredStateRevision))
	writeJSON(w, status, map[string]any{"meta": meta(r), "data": map[string]any{"account": account.Public()}})
}

func (routes *accountRoutes) validationFailure(w http.ResponseWriter, r *http.Request, err error) {
	writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", map[string]any{"body": err.Error()}, false, requestID(r))
}

func (routes *accountRoutes) failure(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, platformaccounts.ErrNotFound):
		writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "account not found", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrRevisionConflict):
		writeError(w, http.StatusConflict, "DESIRED_STATE_REVISION_CONFLICT", "account revision conflict", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "INVALID_ACCOUNT_TRANSITION", "account transition is invalid", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrAccountBlocked):
		writeError(w, http.StatusConflict, "ACCOUNT_LIFECYCLE_BLOCKED", "account lifecycle is blocked", nil, false, requestID(r))
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "request failed", nil, true, requestID(r))
	}
}

func decodeStrict(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func decodeEmpty(r *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	if len(value) != 0 {
		return errors.New("request body must be empty")
	}
	return nil
}
