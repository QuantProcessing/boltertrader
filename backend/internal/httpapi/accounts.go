package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

type AccountRoutesOptions struct {
	Service           *platformaccounts.Service
	Authenticate      func(*http.Request) (string, error)
	AuthorizeMutation func(*http.Request) error
	Blockers          func(context.Context, string) (platformaccounts.Blockers, error)
	Activity          ActivityPublisher
	Mode              string
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
	Venue            string                          `json:"venue"`
	DisplayName      string                          `json:"displayName"`
	ProductPolicy    []string                        `json:"productPolicy"`
	Routes           []platformaccounts.AccountRoute `json:"routes"`
	CredentialBundle json.RawMessage                 `json:"credentialBundle"`
}

type updateAccountRequest struct {
	DisplayName string   `json:"displayName"`
	ProductIDs  []string `json:"productIds"`
}

type rotateAccountRequest struct {
	CredentialBundle json.RawMessage `json:"credentialBundle"`
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
	account, err := routes.options.Service.Create(r.Context(), platformaccounts.CreateRequest{Venue: request.Venue, DisplayName: request.DisplayName, ProductIDs: request.ProductPolicy, Routes: request.Routes, CredentialBundle: request.CredentialBundle}, principal, strings.TrimSpace(r.Header.Get("Idempotency-Key")))
	clear(request.CredentialBundle)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.publish(principal, "account.created", account)
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
	routes.publish(principal, "account.updated", account)
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
	routes.publish(principal, "account.validated", account)
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) rotate(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key is required", nil, false, requestID(r))
		return
	}
	var request rotateAccountRequest
	if err := decodeStrict(r, &request); err != nil {
		routes.validationFailure(w, r, err)
		return
	}
	account, err := routes.options.Service.Rotate(r.Context(), r.PathValue("accountId"), revision, request.CredentialBundle, principal, idempotencyKey)
	clear(request.CredentialBundle)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.publish(principal, "account.rotated", account)
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
	kind := "account.draining"
	if action == "disable" {
		kind = "account.disabled"
	}
	routes.publish(principal, kind, account)
	routes.writeAccount(w, r, http.StatusOK, account)
}

func (routes *accountRoutes) remove(w http.ResponseWriter, r *http.Request) {
	principal, revision, ok := routes.existingMutation(w, r)
	if !ok {
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if idempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "Idempotency-Key is required", nil, false, requestID(r))
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
	account, err := routes.options.Service.Remove(r.Context(), r.PathValue("accountId"), revision, principal, idempotencyKey)
	if err != nil {
		routes.failure(w, r, err)
		return
	}
	routes.publish(principal, "account.removed", account)
	routes.writeAccount(w, r, http.StatusAccepted, account)
}

func (routes *accountRoutes) publish(principal, kind string, account platformaccounts.Account) {
	if routes.options.Activity == nil {
		return
	}
	_, _ = routes.options.Activity.Publish(ActivityRecord{PrincipalID: principal, Mode: routes.options.Mode, Kind: kind, AccountID: account.ID, ResourceID: account.ID, Payload: account.Public()})
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
	case errors.Is(err, platformaccounts.ErrIdempotencyConflict):
		writeError(w, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "idempotency key conflicts with a different request", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrInvalidTransition):
		writeError(w, http.StatusConflict, "INVALID_ACCOUNT_TRANSITION", "account transition is invalid", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrAccountBlocked):
		writeError(w, http.StatusConflict, "ACCOUNT_LIFECYCLE_BLOCKED", "account lifecycle is blocked", nil, false, requestID(r))
	case errors.Is(err, platformaccounts.ErrValidation):
		writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "request is invalid", nil, false, requestID(r))
	default:
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "request failed", nil, true, requestID(r))
	}
}

const accountMutationBodyLimit = 1 << 20

var errCredentialBundleShape = errors.New("credential bundle shape is invalid")

func decodeStrict(r *http.Request, target any) error {
	body, err := readAccountMutationBody(r)
	if err != nil {
		return err
	}
	if err := validateExactJSONKeys(body, target); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func validateExactJSONKeys(body []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	targetType := reflect.TypeOf(target)
	if targetType == nil || targetType.Kind() != reflect.Pointer {
		return errors.New("request body target is invalid")
	}
	return validateJSONValueKeys(value, targetType.Elem())
}

func validateJSONValueKeys(value any, targetType reflect.Type) error {
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	switch targetType.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		fields := make(map[string]reflect.StructField, targetType.NumField())
		for index := 0; index < targetType.NumField(); index++ {
			field := targetType.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if field.PkgPath == "" && name != "" && name != "-" {
				fields[name] = field
			}
		}
		for key, child := range object {
			field, exists := fields[key]
			if !exists {
				if strings.EqualFold(key, "credentialBundle") {
					return errCredentialBundleShape
				}
				return fmt.Errorf("request body contains non-canonical field %q", key)
			}
			if field.Type == reflect.TypeOf(json.RawMessage{}) && key == "credentialBundle" {
				if err := validateCredentialBundleKeys(child); err != nil {
					return err
				}
				continue
			}
			if err := validateJSONValueKeys(child, field.Type); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for _, item := range items {
			if err := validateJSONValueKeys(item, targetType.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCredentialBundleKeys(value any) error {
	object, ok := value.(map[string]any)
	if !ok {
		return errCredentialBundleShape
	}
	allowed := map[string]map[string]struct{}{
		"metadata": credentialKeySet("accountIndex", "keyIndex", "accountAddress", "userAddress", "expectedSigner", "subaccount"),
		"secrets":  credentialKeySet("apiKey", "apiSecret", "passphrase", "privateKey", "apiWalletPrivateKey"),
	}
	envelope := credentialKeySet("schemaVersion", "venue", "authKind", "metadata", "secrets")
	for key, child := range object {
		if _, exists := envelope[key]; !exists {
			return errCredentialBundleShape
		}
		nested, nestedObject := child.(map[string]any)
		keys, requiresObject := allowed[key]
		if !requiresObject {
			continue
		}
		if !nestedObject {
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

func credentialKeySet(keys ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		result[key] = struct{}{}
	}
	return result
}

func decodeEmpty(r *http.Request) error {
	body, err := readAccountMutationBody(r)
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

func readAccountMutationBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, accountMutationBodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(body) > accountMutationBodyLimit {
		return nil, errors.New("request body exceeds limit")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return body, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := scanUniqueJSONObjectKeys(decoder, false); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("request body must contain one JSON value")
	}
	return body, nil
}

func scanUniqueJSONObjectKeys(decoder *json.Decoder, credentialScope bool) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("request body contains invalid object key")
			}
			folded := strings.ToLower(key)
			if _, exists := seen[folded]; exists {
				if credentialScope {
					return errCredentialBundleShape
				}
				return fmt.Errorf("request body contains duplicate key %q", key)
			}
			seen[folded] = struct{}{}
			if err := scanUniqueJSONObjectKeys(decoder, credentialScope || strings.EqualFold(key, "credentialBundle")); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("request body contains invalid object")
		}
	case '[':
		for decoder.More() {
			if err := scanUniqueJSONObjectKeys(decoder, credentialScope); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("request body contains invalid array")
		}
	default:
		return errors.New("request body contains invalid JSON delimiter")
	}
	return nil
}
