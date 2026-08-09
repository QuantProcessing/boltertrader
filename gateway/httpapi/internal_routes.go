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
	"time"

	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/QuantProcessing/boltertrader/secretstore"
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
	AuthorityApplyDesired        AuthorityOperation = "accounts.desired.apply"
	AuthorityRetireDesired       AuthorityOperation = "accounts.desired.retire"
	AuthorityFinalizeRotation    AuthorityOperation = "accounts.desired.rotation_finalize"
	AuthorityListRuntimeAccounts AuthorityOperation = "accounts.runtime.list"
	AuthorityGetRuntimeAccount   AuthorityOperation = "accounts.runtime.get"
	AuthorityRuntimeEvents       AuthorityOperation = "accounts.runtime.events"
	AuthorityCatalogVenues       AuthorityOperation = "catalog.venues"
	AuthorityCatalogInstruments  AuthorityOperation = "catalog.instruments"
	AuthorityMarketData          AuthorityOperation = "market_data"
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

type DesiredRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
}

type DesiredApplyRequest struct {
	OperationID          string         `json:"operationId"`
	AccountID            string         `json:"accountId"`
	Venue                string         `json:"venue"`
	NativeEnvironment    string         `json:"nativeEnvironment"`
	SecretRef            string         `json:"secretRef"`
	DesiredStateRevision int64          `json:"desiredStateRevision"`
	RuntimeGeneration    int64          `json:"runtimeGeneration"`
	CredentialVersion    int64          `json:"credentialVersion"`
	RequestedState       string         `json:"requestedState"`
	Routes               []DesiredRoute `json:"routes"`
}

type RetireRequest struct {
	OperationID          string `json:"operationId"`
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

type AccountFence struct {
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int64  `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
}

type RotationFinalizationRequest struct {
	OperationID           string       `json:"operationId"`
	AccountID             string       `json:"accountId"`
	ExpectedActiveFence   AccountFence `json:"expectedActiveFence"`
	ExpectedRetiringFence AccountFence `json:"expectedRetiringFence"`
}

type RotationFinalizationProof struct {
	OperationID          string       `json:"operationId"`
	AccountID            string       `json:"accountId"`
	ActiveFence          AccountFence `json:"activeFence"`
	RetiringFence        AccountFence `json:"retiringFence"`
	TombstoneRevision    int64        `json:"tombstoneRevision"`
	RetiringState        string       `json:"retiringState"`
	SecretReferenceState string       `json:"secretReferenceState"`
	CompletedAt          time.Time    `json:"completedAt"`
}

type RuntimeRouteView struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
	RequestedState  string `json:"requestedState"`
	Ready           bool   `json:"ready"`
	Reason          string `json:"reason,omitempty"`
}

type RuntimeSnapshotView struct {
	OperationID          string             `json:"operationId"`
	DesiredStateRevision int64              `json:"desiredStateRevision"`
	RuntimeGeneration    int64              `json:"runtimeGeneration"`
	CredentialVersion    int64              `json:"credentialVersion"`
	RequestedState       string             `json:"requestedState"`
	Routes               []RuntimeRouteView `json:"routes"`
}

type PendingSnapshotView struct {
	RuntimeSnapshotView
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

type RuntimeView struct {
	AccountID  string                `json:"accountId"`
	Active     *RuntimeSnapshotView  `json:"active,omitempty"`
	Pending    *PendingSnapshotView  `json:"pending,omitempty"`
	Retiring   []RuntimeSnapshotView `json:"retiring"`
	Tombstoned bool                  `json:"tombstoned"`
}

type AuthorityRequest struct {
	Operation            AuthorityOperation
	Mode                 string
	AccountID            string
	OperationID          string
	ExpectedRevision     int64
	Desired              *DesiredAccount
	Mutation             *DesiredMutation
	DesiredApply         *DesiredApplyRequest
	Retirement           *RetireRequest
	RotationFinalization *RotationFinalizationRequest
	Query                url.Values
}

type AuthorityResponse struct {
	Body                 any
	DesiredStateRevision int64
	Status               int
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
	{http.MethodGet, "/internal/v1/accounts/desired/{accountId}"},
	{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/operations/{operationId}"},
	{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/retirements/{operationId}"},
	{http.MethodPut, "/internal/v1/accounts/desired/{accountId}/rotation-finalizations/{operationId}"},
	{http.MethodGet, "/internal/v1/accounts/runtime"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}"},
	{http.MethodGet, "/internal/v1/accounts/runtime/{accountId}/events"},
	{http.MethodGet, "/internal/v1/catalog/venues"},
	{http.MethodGet, "/internal/v1/catalog/instruments"},
	{http.MethodGet, "/internal/v1/market-data"},
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
	handler.mux.HandleFunc("GET /internal/v1/accounts/desired/{accountId}", handler.accountRead(AuthorityGetDesired))
	handler.mux.HandleFunc("PUT /internal/v1/accounts/desired/{accountId}/operations/{operationId}", handler.applyDesired)
	handler.mux.HandleFunc("PUT /internal/v1/accounts/desired/{accountId}/retirements/{operationId}", handler.retireDesired)
	handler.mux.HandleFunc("PUT /internal/v1/accounts/desired/{accountId}/rotation-finalizations/{operationId}", handler.finalizeRotation)
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime", handler.read(AuthorityListRuntimeAccounts))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}", handler.accountRead(AuthorityGetRuntimeAccount))
	handler.mux.HandleFunc("GET /internal/v1/accounts/runtime/{accountId}/events", handler.accountRead(AuthorityRuntimeEvents))
	handler.mux.HandleFunc("GET /internal/v1/catalog/venues", handler.read(AuthorityCatalogVenues))
	handler.mux.HandleFunc("GET /internal/v1/catalog/instruments", handler.read(AuthorityCatalogInstruments))
	handler.mux.HandleFunc("GET /internal/v1/market-data", handler.read(AuthorityMarketData))
	handler.mux.HandleFunc("GET /internal/v1/private-events", handler.read(AuthorityPrivateEvents))
}

func (handler *InternalHandler) applyDesired(w http.ResponseWriter, r *http.Request) {
	mode := r.Header.Get("X-Bolter-Mode")
	if mode != "test" && mode != "live" {
		handler.writeError(w, r, http.StatusBadRequest, "MODE_ATTESTATION_REQUIRED", false)
		return
	}
	var desired DesiredApplyRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &desired); err != nil || desired.validate(mode, r.PathValue("accountId"), r.PathValue("operationId")) != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	handler.execute(w, r, http.StatusAccepted, AuthorityRequest{
		Operation: AuthorityApplyDesired, Mode: mode, AccountID: desired.AccountID,
		OperationID: desired.OperationID, DesiredApply: &desired,
	})
}

func (handler *InternalHandler) retireDesired(w http.ResponseWriter, r *http.Request) {
	mode := r.Header.Get("X-Bolter-Mode")
	if mode != "test" && mode != "live" {
		handler.writeError(w, r, http.StatusBadRequest, "MODE_ATTESTATION_REQUIRED", false)
		return
	}
	var retirement RetireRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &retirement); err != nil || retirement.validate(r.PathValue("accountId"), r.PathValue("operationId")) != nil {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	handler.execute(w, r, http.StatusAccepted, AuthorityRequest{
		Operation: AuthorityRetireDesired, Mode: mode, AccountID: retirement.AccountID,
		OperationID: retirement.OperationID, Retirement: &retirement,
	})
}

func (handler *InternalHandler) finalizeRotation(w http.ResponseWriter, r *http.Request) {
	mode := r.Header.Get("X-Bolter-Mode")
	if mode != "test" && mode != "live" {
		handler.writeError(w, r, http.StatusBadRequest, "MODE_ATTESTATION_REQUIRED", false)
		return
	}
	var finalization RotationFinalizationRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &finalization); err != nil ||
		finalization.validate(r.PathValue("accountId"), r.PathValue("operationId")) != nil || len(r.URL.Query()) != 0 {
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
		return
	}
	handler.execute(w, r, http.StatusOK, AuthorityRequest{
		Operation: AuthorityFinalizeRotation, Mode: mode, AccountID: finalization.AccountID,
		OperationID: finalization.OperationID, RotationFinalization: &finalization,
	})
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
	if response.Status != 0 {
		if response.Status != http.StatusOK && response.Status != http.StatusAccepted && response.Status != http.StatusCreated && response.Status != http.StatusNoContent {
			handler.writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", true)
			return
		}
		status = response.Status
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, map[string]any{"data": response.Body})
}

func (desired DesiredApplyRequest) validate(mode, accountID, operationID string) error {
	parsedOperation, err := secretstore.ParseOperationID(desired.OperationID)
	if err != nil || string(parsedOperation) != operationID || desired.AccountID != accountID || strings.TrimSpace(accountID) == "" {
		return errors.New("invalid desired operation identity")
	}
	if _, err := secretstore.ParseOpaqueRef(desired.SecretRef); err != nil {
		return errors.New("invalid desired secret reference")
	}
	if strings.TrimSpace(desired.Venue) == "" || desired.Venue != strings.TrimSpace(desired.Venue) || len(desired.Venue) > 64 || desired.DesiredStateRevision < 1 || desired.RuntimeGeneration < 1 || desired.CredentialVersion < 1 || !validRequestedState(desired.RequestedState) || len(desired.Routes) < 1 || len(desired.Routes) > 32 {
		return errors.New("invalid desired account")
	}
	seen := make(map[string]struct{}, len(desired.Routes))
	active := 0
	for _, route := range desired.Routes {
		if strings.TrimSpace(route.RouteID) == "" || route.RouteID != strings.TrimSpace(route.RouteID) || route.RouteProfileID == "" {
			return errors.New("invalid desired route")
		}
		if _, exists := seen[route.RouteID]; exists {
			return errors.New("duplicate desired route")
		}
		seen[route.RouteID] = struct{}{}
		profile, err := routeprofile.DefaultRegistry().Resolve(routeprofile.Mode(mode), desired.Venue, route.RouteProfileID)
		if err != nil || string(profile.NativeEnvironment) != desired.NativeEnvironment || string(profile.Product) != route.Product || profile.SettlementAsset != route.SettlementAsset {
			return errors.New("desired route profile conflict")
		}
		switch route.RequestedState {
		case "active":
			active++
		case "disabled":
		default:
			return errors.New("invalid desired route state")
		}
	}
	if (desired.RequestedState == "creating" || desired.RequestedState == "active" || desired.RequestedState == "rotating") && active == 0 {
		return errors.New("desired account requires an active route")
	}
	return nil
}

func (retirement RetireRequest) validate(accountID, operationID string) error {
	parsedOperation, err := secretstore.ParseOperationID(retirement.OperationID)
	if err != nil || string(parsedOperation) != operationID || retirement.AccountID != accountID || strings.TrimSpace(accountID) == "" || retirement.DesiredStateRevision < 2 || retirement.RuntimeGeneration < 1 || retirement.CredentialVersion < 1 {
		return errors.New("invalid retirement request")
	}
	return nil
}

func (request RotationFinalizationRequest) validate(accountID, operationID string) error {
	parsedOperation, err := secretstore.ParseOperationID(request.OperationID)
	if err != nil || string(parsedOperation) != operationID || request.AccountID != accountID || strings.TrimSpace(accountID) == "" {
		return errors.New("invalid rotation finalization identity")
	}
	if request.ExpectedActiveFence.validate(accountID) != nil || request.ExpectedRetiringFence.validate(accountID) != nil {
		return errors.New("invalid rotation finalization fence")
	}
	active, retiring := request.ExpectedActiveFence, request.ExpectedRetiringFence
	if active.DesiredStateRevision <= 1 || active.RuntimeGeneration <= 1 || active.CredentialVersion <= 1 ||
		active.DesiredStateRevision-1 != retiring.DesiredStateRevision ||
		active.RuntimeGeneration-1 != retiring.RuntimeGeneration ||
		active.CredentialVersion-1 != retiring.CredentialVersion {
		return errors.New("invalid rotation finalization adjacency")
	}
	return nil
}

func (fence AccountFence) validate(accountID string) error {
	if fence.AccountID != accountID || fence.DesiredStateRevision <= 0 || fence.RuntimeGeneration <= 0 || fence.CredentialVersion <= 0 {
		return errors.New("invalid account fence")
	}
	return nil
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
