package httpapi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type AuthorityOperation string

const (
	AuthorityRuntimeStatus       AuthorityOperation = "runtime.status"
	AuthorityRuntimeReadiness    AuthorityOperation = "runtime.readiness"
	AuthorityRuntimeOpenOrders   AuthorityOperation = "runtime.open_orders"
	AuthorityRuntimePositions    AuthorityOperation = "runtime.positions"
	AuthorityRuntimeUncertain    AuthorityOperation = "runtime.uncertain_commands"
	AuthorityListDesired         AuthorityOperation = "accounts.desired.list"
	AuthorityCreateDesired       AuthorityOperation = "accounts.desired.create"
	AuthorityGetDesired          AuthorityOperation = "accounts.desired.get"
	AuthorityUpdateDesired       AuthorityOperation = "accounts.desired.update"
	AuthorityValidateDesired     AuthorityOperation = "accounts.desired.validate"
	AuthorityRotateDesired       AuthorityOperation = "accounts.desired.rotate"
	AuthorityDisableDesired      AuthorityOperation = "accounts.desired.disable"
	AuthorityDrainDesired        AuthorityOperation = "accounts.desired.drain"
	AuthorityDeleteDesired       AuthorityOperation = "accounts.desired.delete"
	AuthorityListRuntimeAccounts AuthorityOperation = "accounts.runtime.list"
	AuthorityGetRuntimeAccount   AuthorityOperation = "accounts.runtime.get"
	AuthorityRuntimeEvents       AuthorityOperation = "accounts.runtime.events"
	AuthorityCatalogVenues       AuthorityOperation = "catalog.venues"
	AuthorityCatalogInstruments  AuthorityOperation = "catalog.instruments"
	AuthorityMarketData          AuthorityOperation = "market_data"
	AuthorityMarketDataReadiness AuthorityOperation = "market_data.readiness"
	AuthorityPrivateEvents       AuthorityOperation = "private_events"
)

type RotationFence struct {
	RotationFenceID       string `json:"rotationFenceId"`
	State                 string `json:"state"`
	FromCredentialVersion int64  `json:"fromCredentialVersion"`
	ToCredentialVersion   int64  `json:"toCredentialVersion"`
	FromRuntimeGeneration int64  `json:"fromRuntimeGeneration"`
	ToRuntimeGeneration   int64  `json:"toRuntimeGeneration"`
}

type DesiredAccount struct {
	AccountID            string         `json:"accountId"`
	Venue                string         `json:"venue"`
	VenueEnvironment     string         `json:"venueEnvironment"`
	SecretRef            string         `json:"secretRef"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       string         `json:"requestedState"`
	RotationFence        *RotationFence `json:"rotationFence,omitempty"`
}

type DesiredMutation struct {
	SecretRef            string         `json:"secretRef,omitempty"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       string         `json:"requestedState,omitempty"`
	RotationFence        *RotationFence `json:"rotationFence,omitempty"`
}

type AuthorityRequest struct {
	Operation        AuthorityOperation
	AccountID        string
	ExpectedRevision int64
	Desired          *DesiredAccount
	Mutation         *DesiredMutation
	Query            url.Values
}

type AuthorityResponse struct {
	Body                 any
	DesiredStateRevision int64
}

type RuntimeReadiness struct {
	Ready bool `json:"ready"`
}

type InternalAuthority interface {
	Execute(context.Context, AuthorityRequest) (AuthorityResponse, error)
}

type AuthorityError struct {
	Status    int
	Code      string
	Retryable bool
}

func (err AuthorityError) Error() string { return err.Code }

type InternalOptions struct {
	Authority    InternalAuthority
	Authenticate func(*http.Request) error
	MaxBodyBytes int64
}

type InternalHandler struct {
	authority    InternalAuthority
	authenticate func(*http.Request) error
	bodyLimit    int64
	mux          *http.ServeMux
}

var internalRoutes = []Route{
	{http.MethodGet, "/internal/v1/runtime/status"},
	{http.MethodGet, "/internal/v1/runtime/readiness"},
	{http.MethodGet, "/internal/v1/runtime/open-orders"},
	{http.MethodGet, "/internal/v1/runtime/positions"},
	{http.MethodGet, "/internal/v1/runtime/uncertain-commands"},
	{http.MethodGet, "/internal/v1/accounts/desired"},
	{http.MethodPost, "/internal/v1/accounts/desired"},
	{http.MethodGet, "/internal/v1/accounts/desired/{accountId}"},
	{http.MethodPut, "/internal/v1/accounts/desired/{accountId}"},
	{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/validate"},
	{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/rotate"},
	{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/disable"},
	{http.MethodPost, "/internal/v1/accounts/desired/{accountId}/drain"},
	{http.MethodDelete, "/internal/v1/accounts/desired/{accountId}"},
	{http.MethodGet, "/internal/v1/accounts/runtime"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
	{http.MethodGet, "/internal/v1/catalog/venues"},
	{http.MethodGet, "/internal/v1/catalog/instruments"},
	{http.MethodGet, "/internal/v1/market-data"},
	{http.MethodGet, "/internal/v1/market-data/readiness"},
	{http.MethodGet, "/internal/v1/private-events"},
}

func InternalRoutes() []Route { return append([]Route(nil), internalRoutes...) }

func NewInternalHandler(options InternalOptions) (*InternalHandler, error) {
	if options.Authority == nil || options.Authenticate == nil {
		return nil, fmt.Errorf("internal HTTP API dependencies are incomplete")
	}
	if options.MaxBodyBytes <= 0 {
		options.MaxBodyBytes = defaultBodyLimit
	}
	handler := &InternalHandler{authority: options.Authority, authenticate: options.Authenticate, bodyLimit: options.MaxBodyBytes, mux: http.NewServeMux()}
	handler.register()
	return handler, nil
}

func (handler *InternalHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if handler.authenticate(r) != nil {
		handler.writeError(w, r, http.StatusUnauthorized, "GATEWAY_ADAPTER_AUTH_REQUIRED", false)
		return
	}
	handler.mux.ServeHTTP(w, r)
}

func (handler *InternalHandler) register() {
	handler.mux.HandleFunc("GET /internal/v1/runtime/status", handler.read(AuthorityRuntimeStatus))
	handler.mux.HandleFunc("GET /internal/v1/runtime/readiness", handler.read(AuthorityRuntimeReadiness))
	handler.mux.HandleFunc("GET /internal/v1/runtime/open-orders", handler.read(AuthorityRuntimeOpenOrders))
	handler.mux.HandleFunc("GET /internal/v1/runtime/positions", handler.read(AuthorityRuntimePositions))
	handler.mux.HandleFunc("GET /internal/v1/runtime/uncertain-commands", handler.read(AuthorityRuntimeUncertain))
	handler.mux.HandleFunc("GET /internal/v1/accounts/desired", handler.read(AuthorityListDesired))
	handler.mux.HandleFunc("POST /internal/v1/accounts/desired", handler.createDesired)
	handler.mux.HandleFunc("GET /internal/v1/accounts/desired/{accountId}", handler.accountRead(AuthorityGetDesired))
	handler.mux.HandleFunc("PUT /internal/v1/accounts/desired/{accountId}", handler.mutateDesired(AuthorityUpdateDesired))
	handler.mux.HandleFunc("POST /internal/v1/accounts/desired/{accountId}/validate", handler.mutateDesired(AuthorityValidateDesired))
	handler.mux.HandleFunc("POST /internal/v1/accounts/desired/{accountId}/rotate", handler.mutateDesired(AuthorityRotateDesired))
	handler.mux.HandleFunc("POST /internal/v1/accounts/desired/{accountId}/disable", handler.mutateDesired(AuthorityDisableDesired))
	handler.mux.HandleFunc("POST /internal/v1/accounts/desired/{accountId}/drain", handler.mutateDesired(AuthorityDrainDesired))
	handler.mux.HandleFunc("DELETE /internal/v1/accounts/desired/{accountId}", handler.deleteDesired)
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime", handler.read(AuthorityListRuntimeAccounts))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}", handler.accountRead(AuthorityGetRuntimeAccount))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}/events", handler.accountRead(AuthorityRuntimeEvents))
	handler.mux.HandleFunc("GET /internal/v1/catalog/venues", handler.read(AuthorityCatalogVenues))
	handler.mux.HandleFunc("GET /internal/v1/catalog/instruments", handler.read(AuthorityCatalogInstruments))
	handler.mux.HandleFunc("GET /internal/v1/market-data", handler.read(AuthorityMarketData))
	handler.mux.HandleFunc("GET /internal/v1/market-data/readiness", handler.read(AuthorityMarketDataReadiness))
	handler.mux.HandleFunc("GET /internal/v1/private-events", handler.read(AuthorityPrivateEvents))
}

func (handler *InternalHandler) createDesired(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-None-Match") != "*" {
		handler.writeError(w, r, http.StatusPreconditionRequired, "IF_NONE_MATCH_REQUIRED", false)
		return
	}
	var desired DesiredAccount
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &desired); err != nil || desired.validateCreate() != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	handler.execute(w, r, http.StatusCreated, AuthorityRequest{Operation: AuthorityCreateDesired, Desired: &desired})
}

func (handler *InternalHandler) mutateDesired(operation AuthorityOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		revision, err := parseDesiredETag(r.Header.Get("If-Match"))
		if err != nil {
			handler.writeError(w, r, http.StatusPreconditionRequired, "IF_MATCH_REQUIRED", false)
			return
		}
		var mutation DesiredMutation
		if err := decodeStrictJSON(w, r, handler.bodyLimit, &mutation); err != nil || mutation.validate(operation) != nil {
			handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
			return
		}
		handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: operation, AccountID: r.PathValue("accountId"), ExpectedRevision: revision, Mutation: &mutation})
	}
}

func (handler *InternalHandler) deleteDesired(w http.ResponseWriter, r *http.Request) {
	revision, err := parseDesiredETag(r.Header.Get("If-Match"))
	if err != nil {
		handler.writeError(w, r, http.StatusPreconditionRequired, "IF_MATCH_REQUIRED", false)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, handler.bodyLimit))
	if err != nil || len(bytes.TrimSpace(body)) != 0 {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	handler.execute(w, r, http.StatusNoContent, AuthorityRequest{Operation: AuthorityDeleteDesired, AccountID: r.PathValue("accountId"), ExpectedRevision: revision})
}

func (handler *InternalHandler) read(operation AuthorityOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: operation, Query: r.URL.Query()})
	}
}

func (handler *InternalHandler) accountRead(operation AuthorityOperation) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		handler.execute(w, r, http.StatusOK, AuthorityRequest{Operation: operation, AccountID: r.PathValue("accountId"), Query: r.URL.Query()})
	}
}

func (handler *InternalHandler) execute(w http.ResponseWriter, r *http.Request, status int, request AuthorityRequest) {
	response, err := handler.authority.Execute(r.Context(), request)
	if err != nil {
		var authorityErr AuthorityError
		if errors.As(err, &authorityErr) && authorityErr.Status >= 400 && authorityErr.Code != "" {
			handler.writeError(w, r, authorityErr.Status, authorityErr.Code, authorityErr.Retryable)
			return
		}
		var authorityErrPointer *AuthorityError
		if errors.As(err, &authorityErrPointer) && authorityErrPointer.Status >= 400 && authorityErrPointer.Code != "" {
			handler.writeError(w, r, authorityErrPointer.Status, authorityErrPointer.Code, authorityErrPointer.Retryable)
			return
		}
		handler.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", true)
		return
	}
	if response.DesiredStateRevision > 0 {
		w.Header().Set("ETag", desiredETag(response.DesiredStateRevision))
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]any{"data": response.Body})
}

func (handler *InternalHandler) writeError(w http.ResponseWriter, r *http.Request, status int, code string, retryable bool) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"code": code, "message": "request denied", "requestId": r.Header.Get("X-Request-ID"), "retryable": retryable,
	}})
}

func desiredETag(revision int64) string { return fmt.Sprintf(`"desired-state-revision-%d"`, revision) }

func parseDesiredETag(value string) (int64, error) {
	if value == "" || strings.Contains(value, ",") || strings.HasPrefix(value, "W/") || len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, errors.New("invalid If-Match")
	}
	const prefix = "desired-state-revision-"
	inner := value[1 : len(value)-1]
	if !strings.HasPrefix(inner, prefix) {
		return 0, errors.New("invalid If-Match")
	}
	revision, err := strconv.ParseInt(strings.TrimPrefix(inner, prefix), 10, 64)
	if err != nil || revision <= 0 {
		return 0, errors.New("invalid If-Match")
	}
	return revision, nil
}

func (desired DesiredAccount) validateCreate() error {
	if desired.AccountID == "" || desired.Venue == "" || desired.SecretRef == "" || desired.DesiredStateRevision != 1 || desired.RuntimeGeneration != 1 || desired.CredentialVersion != 1 {
		return errors.New("invalid desired account")
	}
	if desired.VenueEnvironment != "demo" && desired.VenueEnvironment != "testnet" && desired.VenueEnvironment != "live" {
		return errors.New("invalid venue environment")
	}
	if desired.RequestedState != "creating" && desired.RequestedState != "active" {
		return errors.New("invalid requested state")
	}
	return nil
}

func (mutation DesiredMutation) validate(operation AuthorityOperation) error {
	if mutation.DesiredStateRevision <= 0 || mutation.RuntimeGeneration <= 0 || mutation.CredentialVersion <= 0 {
		return errors.New("invalid desired mutation")
	}
	switch operation {
	case AuthorityValidateDesired:
		if mutation.SecretRef != "" || mutation.RequestedState != "" || mutation.RotationFence != nil {
			return errors.New("invalid validate mutation")
		}
	case AuthorityRotateDesired:
		if mutation.SecretRef == "" || mutation.RotationFence == nil || mutation.RequestedState != "" ||
			mutation.RotationFence.validate(mutation.RuntimeGeneration, mutation.CredentialVersion) != nil {
			return errors.New("invalid rotate mutation")
		}
	case AuthorityDisableDesired:
		if mutation.SecretRef != "" || mutation.RequestedState != "disabled" || mutation.RotationFence != nil {
			return errors.New("invalid lifecycle mutation")
		}
	case AuthorityDrainDesired:
		if mutation.SecretRef != "" || mutation.RequestedState != "draining" || mutation.RotationFence != nil {
			return errors.New("invalid lifecycle mutation")
		}
	case AuthorityUpdateDesired:
		if mutation.SecretRef == "" || !validRequestedState(mutation.RequestedState) ||
			(mutation.RotationFence != nil && mutation.RotationFence.validate(mutation.RuntimeGeneration, mutation.CredentialVersion) != nil) {
			return errors.New("invalid desired update")
		}
	default:
		return errors.New("invalid desired operation")
	}
	return nil
}

func (fence RotationFence) validate(runtimeGeneration, credentialVersion int64) error {
	if fence.RotationFenceID == "" || fence.State == "" ||
		fence.FromRuntimeGeneration != runtimeGeneration || fence.ToRuntimeGeneration != runtimeGeneration+1 ||
		fence.FromCredentialVersion != credentialVersion || fence.ToCredentialVersion != credentialVersion+1 {
		return errors.New("invalid rotation fence")
	}
	switch fence.State {
	case "armed", "replacement_ready", "released", "expired", "blocked":
		return nil
	default:
		return errors.New("invalid rotation fence state")
	}
}

func validRequestedState(state string) bool {
	switch state {
	case "creating", "active", "rotating", "draining", "drained", "disabled", "removing", "removed":
		return true
	default:
		return false
	}
}
