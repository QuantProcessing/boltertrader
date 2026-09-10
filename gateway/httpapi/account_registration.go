package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"io"
	"net/http"
	"strings"
)

type AccountRegistrations interface {
	List(context.Context) ([]account.Registration, error)
	Get(context.Context, domain.AccountID) (account.Registration, error)
	Add(context.Context, account.AddRegistration, string) (account.Registration, error)
	Remove(context.Context, domain.AccountID) error
}

const (
	accountPrincipalHeader   = "X-Bolter-Principal"
	accountRequestIDHeader   = "X-Request-ID"
	accountIdempotencyHeader = "Idempotency-Key"
)

func InternalAccountRoutes() []Route {
	return []Route{
		{http.MethodGet, "/internal/v1/accounts"},
		{http.MethodPost, "/internal/v1/accounts"},
		{http.MethodGet, "/internal/v1/accounts/{accountId}"},
		{http.MethodDelete, "/internal/v1/accounts/{accountId}"},
	}
}
func (handler *InternalHandler) registerAccountRegistrations() {
	handler.mux.HandleFunc("GET /internal/v1/accounts", handler.listAccountRecords)
	handler.mux.HandleFunc("POST /internal/v1/accounts", handler.createAccountRecord)
	handler.mux.HandleFunc("GET /internal/v1/accounts/{accountId}", handler.getAccountRecord)
	handler.mux.HandleFunc("DELETE /internal/v1/accounts/{accountId}", handler.removeAccountRecord)
}

type createAccountRecordRequest struct {
	Venue            string          `json:"venue"`
	DisplayName      string          `json:"displayName"`
	CredentialBundle json.RawMessage `json:"credentialBundle"`
}

// Product routes describe adapter wiring; they are derived, never granted by a user.
func accountRecordView(item account.Registration) wire.Account {
	routes := account.RegistrationRoutes(item.Venue)
	views := make([]wire.AccountRoute, 0, len(routes))
	for _, route := range routes {
		views = append(views, wire.AccountRoute{RouteID: string(route.RouteID), Product: route.Product, SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID})
	}
	return wire.Account{ID: string(item.ID), Venue: item.Venue, DisplayName: item.DisplayName,
		NativeEnvironment: "live", Status: "active", Routes: views,
		LastValidatedAt: &item.ValidatedAt, CreatedAt: item.CreatedAt, UpdatedAt: item.CreatedAt}
}
func (handler *InternalHandler) listAccountRecords(w http.ResponseWriter, r *http.Request) {
	items, err := handler.accounts.List(r.Context())
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	views := make([]wire.Account, 0, len(items))
	for _, item := range items {
		views = append(views, accountRecordView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"accounts": views}})
}
func (handler *InternalHandler) getAccountRecord(w http.ResponseWriter, r *http.Request) {
	id := domain.AccountID(r.PathValue("accountId"))
	if id.Validate() != nil {
		handler.accountFailure(w, r, account.ErrInvalidRegistration)
		return
	}
	item, err := handler.accounts.Get(r.Context(), id)
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusOK, item)
}
func (handler *InternalHandler) createAccountRecord(w http.ResponseWriter, r *http.Request) {
	var request createAccountRecordRequest
	if err := decodeStrictJSON(w, r, handler.bodyLimit, &request); err != nil {
		handler.accountFailure(w, r, account.ErrInvalidRegistration)
		return
	}
	defer clear(request.CredentialBundle)
	if validateCredentialBundle(request.CredentialBundle) != nil {
		handler.accountFailure(w, r, account.ErrInvalidRegistration)
		return
	}
	item, err := handler.accounts.Add(r.Context(), account.AddRegistration{Venue: request.Venue, DisplayName: request.DisplayName, CredentialBundle: request.CredentialBundle}, r.Header.Get(accountIdempotencyHeader))
	if err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	handler.writeAccountRecord(w, http.StatusCreated, item)
}
func (handler *InternalHandler) removeAccountRecord(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, handler.bodyLimit))
	if err != nil || (len(bytes.TrimSpace(body)) != 0 && !bytes.Equal(bytes.TrimSpace(body), []byte("{}"))) {
		handler.accountFailure(w, r, account.ErrInvalidRegistration)
		return
	}
	if err := handler.accounts.Remove(r.Context(), domain.AccountID(r.PathValue("accountId"))); err != nil {
		handler.accountFailure(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (handler *InternalHandler) writeAccountRecord(w http.ResponseWriter, status int, item account.Registration) {
	writeJSON(w, status, map[string]any{"data": map[string]any{"account": accountRecordView(item)}})
}
func (handler *InternalHandler) accountFailure(w http.ResponseWriter, r *http.Request, err error) {
	var venueErr *exchange.Error
	switch {
	case errors.Is(err, account.ErrRegistrationNotFound):
		handler.writeError(w, r, http.StatusNotFound, "ACCOUNT_NOT_FOUND", false)
	case errors.Is(err, account.ErrRegistrationConflict):
		handler.writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", false)
	case errors.Is(err, account.ErrInvalidRegistration):
		handler.writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", false)
	case errors.As(err, &venueErr):
		details := venueErr.Details()
		message := details.SafeMessage
		if message == "" {
			message = string(venueErr.Kind())
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]any{"code": "ACCOUNT_VERIFICATION_FAILED", "message": message, "venueCode": details.Code, "retryable": venueErr.Kind() == exchange.KindRateLimit || venueErr.Kind() == exchange.KindTransport, "requestId": accountRequestID(r)}})
	default:
		handler.writeError(w, r, http.StatusServiceUnavailable, "ACCOUNT_CONNECTION_FAILED", true)
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
