package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"

	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

type routeSecretWriter struct {
	ref  string
	puts int
}

func (writer *routeSecretWriter) Put(context.Context, secretstore.OperationID, []byte) (string, error) {
	writer.puts++
	return writer.ref, nil
}

type routeAuditWriter struct{}

func (routeAuditWriter) Append(context.Context, platformaccounts.AuditRecord) error { return nil }

func TestAccountRoutesAreStrictCASFencedAndRedacted(t *testing.T) {
	secrets := &routeSecretWriter{ref: "vault://must-not-return"}
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: secrets, Audit: routeAuditWriter{}, ID: func() string { return "acct_http" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	err = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "browser-session", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	validBody := `{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"credential-canary","apiSecret":"credential-secret-canary"}}}`
	invalidCreates := map[string]string{
		"unknown field":        strings.Replace(validBody, `"displayName":"Primary"`, `"displayName":"Primary","extra":true`, 1),
		"productIds":           strings.Replace(validBody, `"productPolicy"`, `"productIds"`, 1),
		"venueEnvironment":     strings.Replace(validBody, `"venue":"binance"`, `"venue":"binance","venueEnvironment":"demo"`, 1),
		"secretMaterial":       strings.Replace(validBody, `"credentialBundle":`, `"secretMaterial":{"apiKey":"legacy"},"credentialBundle":`, 1),
		"multiple JSON values": validBody + `{}`,
		"oversized body":       strings.Repeat(" ", accountMutationBodyLimit+1),
	}
	for name, body := range invalidCreates {
		t.Run("rejects create "+name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(body))
			request.Header.Set("Idempotency-Key", "invalid-"+name)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || secrets.puts != 0 {
				t.Fatalf("status=%d puts=%d body=%s", response.Code, secrets.puts, response.Body.String())
			}
		})
	}

	valid := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(validBody))
	valid.Header.Set("Idempotency-Key", "create-2")
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, valid)
	if created.Code != http.StatusCreated || secrets.puts != 1 {
		t.Fatalf("create status=%d puts=%d body=%s", created.Code, secrets.puts, created.Body.String())
	}
	if strings.Contains(created.Body.String(), "credential-canary") || strings.Contains(created.Body.String(), "vault://") || strings.Contains(created.Body.String(), "secretRef") {
		t.Fatalf("create leaked secret: %s", created.Body.String())
	}
	if created.Header().Get("ETag") != AccountETag(1) {
		t.Fatalf("etag=%q", created.Header().Get("ETag"))
	}

	rotate := httptest.NewRequest(http.MethodPost, "/api/v1/accounts/acct_http/rotate", strings.NewReader(`{"secretMaterial":{"apiKey":"replacement"}}`))
	rotate.Header.Set("If-Match", AccountETag(1))
	rotated := httptest.NewRecorder()
	mux.ServeHTTP(rotated, rotate)
	if rotated.Code != http.StatusBadRequest || secrets.puts != 1 {
		t.Fatalf("legacy rotate status=%d puts=%d body=%s", rotated.Code, secrets.puts, rotated.Body.String())
	}
}

func TestAccountRoutesRequireAuthHooks(t *testing.T) {
	service, _ := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: &routeSecretWriter{ref: "ref"}, Audit: routeAuditWriter{}})
	mux := http.NewServeMux()
	_ = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "", errors.New("denied") }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestTypedAccountCreateReplaysIdempotentlyAndRejectsChangedBody(t *testing.T) {
	service, _ := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: &routeSecretWriter{ref: "opaque-ref"}, Audit: routeAuditWriter{}, ID: func() string { return "acct_typed" }})
	mux := http.NewServeMux()
	_ = RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}})
	body := `{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key-canary","apiSecret":"secret-canary"}}}`
	request := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(payload))
		req.Header.Set("Idempotency-Key", " typed-create ")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		return response
	}
	for index := 0; index < 2; index++ {
		response := request(body)
		if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "canary") || strings.Contains(response.Body.String(), "opaque-ref") {
			t.Fatalf("replay %d status=%d body=%s", index, response.Code, response.Body.String())
		}
	}
	changed := strings.Replace(body, "Primary", "Changed", 1)
	response := request(changed)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "IDEMPOTENCY_CONFLICT") {
		t.Fatalf("changed status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTypedAccountCreateValidationFailureIsStableBadRequest(t *testing.T) {
	secrets := &routeSecretWriter{ref: "opaque-ref"}
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: secrets, Audit: routeAuditWriter{}, ID: func() string { return "acct_invalid" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"venue":"binance","displayName":" ","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}}`))
	request.Header.Set("Idempotency-Key", "invalid-display")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"VALIDATION_ERROR"`) || strings.Contains(response.Body.String(), "INTERNAL_ERROR") || secrets.puts != 0 {
		t.Fatalf("status=%d puts=%d body=%s", response.Code, secrets.puts, response.Body.String())
	}
}

func TestAccountResponsesExposeCanonicalBrowserIdentityAndPATCHRejectsLegacyFields(t *testing.T) {
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: &routeSecretWriter{ref: "opaque-ref"}, Audit: routeAuditWriter{}, ID: func() string { return "acct_contract" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}, Mode: "test"}); err != nil {
		t.Fatal(err)
	}
	create := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(`{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}}`))
	create.Header.Set("Idempotency-Key", "create-contract")
	created := httptest.NewRecorder()
	mux.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	body := created.Body.String()
	for _, required := range []string{`"mode":"test"`, `"nativeEnvironment":"demo"`, `"productPolicy":["manual"]`} {
		if !strings.Contains(body, required) {
			t.Errorf("response missing %s: %s", required, body)
		}
	}
	if strings.Contains(body, `"productIds"`) || strings.Contains(body, `"venueEnvironment"`) || strings.Contains(body, "opaque-ref") {
		t.Fatalf("response leaked legacy/private field: %s", body)
	}

	patch := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/acct_contract", strings.NewReader(`{"displayName":"Updated","productPolicy":["manual"],"routes":[]}`))
	patch.Header.Set("If-Match", AccountETag(1))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, patch)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("legacy PATCH status=%d body=%s", response.Code, response.Body.String())
	}

	patch = httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/acct_contract", strings.NewReader(`{"displayName":"Updated","productIds":["funding"]}`))
	patch.Header.Set("If-Match", AccountETag(1))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, patch)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"productPolicy":["funding"]`) || strings.Contains(response.Body.String(), `"productIds"`) {
		t.Fatalf("canonical PATCH status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAccountPatchRejectsRouteIncompatibleProductPolicyWithoutMutation(t *testing.T) {
	store := platformaccounts.NewMemoryStore()
	account := platformaccounts.Account{
		ID: "acct_patch_policy", Venue: "binance", Mode: "test", NativeEnvironment: platformaccounts.EnvironmentDemo,
		DisplayName: "Spot only", ProductIDs: []string{"manual"}, ProductPolicy: []string{"manual"},
		Routes:               []platformaccounts.AccountRoute{{ID: "spot", Product: platformaccounts.ProductSpot, RouteProfileID: "binance.spot.demo.v1", RequestedState: platformaccounts.RouteActive}},
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: platformaccounts.StateActive,
	}
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service, err := platformaccounts.New(platformaccounts.Options{Store: store, Secrets: &routeSecretWriter{ref: "opaque-ref"}, Audit: routeAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPatch, "/api/v1/accounts/"+account.ID, strings.NewReader(`{"displayName":"Changed","productIds":["funding"]}`))
	request.Header.Set("If-Match", AccountETag(account.DesiredStateRevision))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"VALIDATION_ERROR"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	persisted, err := store.Get(context.Background(), account.ID)
	if err != nil || persisted.DesiredStateRevision != account.DesiredStateRevision || persisted.DisplayName != account.DisplayName || !slices.Equal(persisted.ProductPolicy, account.ProductPolicy) {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
}

func TestAccountMutationBodiesRejectDuplicateKeysBeforeSideEffects(t *testing.T) {
	secrets := &routeSecretWriter{ref: "opaque-ref"}
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: secrets, Audit: routeAuditWriter{}, ID: func() string { return "acct_duplicates" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}, Mode: "test"}); err != nil {
		t.Fatal(err)
	}
	validBody := `{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}}`
	valid := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(validBody))
	valid.Header.Set("Idempotency-Key", "valid")
	validResponse := httptest.NewRecorder()
	mux.ServeHTTP(validResponse, valid)
	if validResponse.Code != http.StatusCreated || secrets.puts != 1 {
		t.Fatalf("valid create status=%d puts=%d body=%s", validResponse.Code, secrets.puts, validResponse.Body.String())
	}

	tests := []struct {
		name, method, path, body string
		idempotency              string
		wantError                string
	}{
		{name: "create wrong top-level case", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "wrong-top-case", wantError: "non-canonical", body: strings.Replace(validBody, `"venue":"binance"`, `"Venue":"binance"`, 1)},
		{name: "create case-fold top-level duplicate", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "folded-top", body: strings.Replace(validBody, `"venue":"binance"`, `"venue":"binance","Venue":"bybit"`, 1)},
		{name: "create wrong nested route case", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "wrong-route-case", wantError: "non-canonical", body: strings.Replace(validBody, `"routeId":"perp-usdt"`, `"RouteId":"perp-usdt"`, 1)},
		{name: "create wrong nested credential case", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "wrong-credential-case", wantError: "credential bundle shape", body: strings.Replace(validBody, `"authKind":"api_key_secret"`, `"AuthKind":"api_key_secret"`, 1)},
		{name: "create case-fold nested secret duplicate", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "folded-secret", wantError: "credential bundle shape", body: strings.Replace(validBody, `"apiKey":"key"`, `"apiKey":"key","APIKEY":"other"`, 1)},
		{name: "create top level", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "duplicate-top", body: strings.Replace(validBody, `"venue":"binance"`, `"venue":"binance","venue":"bybit"`, 1)},
		{name: "create nested route", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "duplicate-route", body: strings.Replace(validBody, `"routeId":"perp-usdt"`, `"routeId":"perp-usdt","routeId":"spot-usdt"`, 1)},
		{name: "create nested credential", method: http.MethodPost, path: "/api/v1/accounts", idempotency: "duplicate-credential", wantError: "credential bundle shape", body: strings.Replace(validBody, `"authKind":"api_key_secret"`, `"authKind":"api_key_secret","authKind":"evm_private_key"`, 1)},
		{name: "patch top level", method: http.MethodPatch, path: "/api/v1/accounts/acct_duplicates", body: `{"displayName":"First","displayName":"Second","productIds":["manual"]}`},
		{name: "rotate nested secret", method: http.MethodPost, path: "/api/v1/accounts/acct_duplicates/rotate", idempotency: "rotate-duplicate", wantError: "credential bundle shape", body: `{"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"one","apiKey":"two","apiSecret":"secret"}}}`},
		{name: "delete body", method: http.MethodDelete, path: "/api/v1/accounts/acct_duplicates", idempotency: "delete-duplicate", body: `{"reason":"one","reason":"two"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("If-Match", AccountETag(1))
			if test.idempotency != "" {
				request.Header.Set("Idempotency-Key", test.idempotency)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			wantError := test.wantError
			if wantError == "" {
				wantError = "duplicate"
			}
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), wantError) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			account, getErr := service.Get(context.Background(), "acct_duplicates")
			if getErr != nil || account.DesiredStateRevision != 1 || account.CredentialVersion != 1 || secrets.puts != 1 {
				t.Fatalf("side effect account=%+v err=%v puts=%d", account, getErr, secrets.puts)
			}
		})
	}
}

func TestCredentialBundleShapeErrorsNeverEchoSubmittedKeyNames(t *testing.T) {
	secrets := &routeSecretWriter{ref: "opaque-ref"}
	service, err := platformaccounts.New(platformaccounts.Options{Store: platformaccounts.NewMemoryStore(), Secrets: secrets, Audit: routeAuditWriter{}, ID: func() string { return "acct_shape" }})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil }, AuthorizeMutation: func(*http.Request) error { return nil }, Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
		return platformaccounts.Blockers{}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	base := `{"venue":"binance","displayName":"Primary","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}}`
	tests := []struct {
		name, canary, body string
	}{
		{name: "unknown envelope", canary: "EnvelopeShapeCanary", body: strings.Replace(base, `"authKind":"api_key_secret"`, `"authKind":"api_key_secret","EnvelopeShapeCanary":true`, 1)},
		{name: "wrong case", canary: "AuthKindShapeCanary", body: strings.Replace(base, `"authKind":"api_key_secret"`, `"AuthKindShapeCanary":"api_key_secret"`, 1)},
		{name: "case folded duplicate", canary: "DuplicateShapeCanary", body: strings.Replace(base, `"metadata":{}`, `"metadata":{"DuplicateShapeCanary":1,"DUPLICATESHAPECANARY":2}`, 1)},
		{name: "unknown nested secret", canary: "SecretShapeCanary", body: strings.Replace(base, `"apiKey":"key"`, `"apiKey":"key","SecretShapeCanary":"attacker-material"`, 1)},
		{name: "mixed case bundle nested duplicate", canary: "AliasNestedCanary", body: strings.Replace(strings.Replace(base, `"credentialBundle"`, `"CrEdEnTiAlBuNdLe"`, 1), `"metadata":{}`, `"metadata":{"AliasNestedCanary":1,"ALIASNESTEDCANARY":2}`, 1)},
		{name: "mixed case bundle nested unknown", canary: "AliasUnknownCanary", body: strings.Replace(strings.Replace(base, `"credentialBundle"`, `"CrEdEnTiAlBuNdLe"`, 1), `"metadata":{}`, `"metadata":{"AliasUnknownCanary":1}`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/v1/accounts", strings.NewReader(test.body))
			request.Header.Set("Idempotency-Key", "shape-"+test.name)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			body := response.Body.String()
			if response.Code != http.StatusBadRequest || !strings.Contains(body, `"code":"VALIDATION_ERROR"`) || !strings.Contains(body, "credential bundle shape is invalid") {
				t.Fatalf("status=%d body=%s", response.Code, body)
			}
			for _, forbidden := range []string{test.canary, strings.ToUpper(test.canary), "attacker-material"} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("response echoed submitted credential shape %q: %s", forbidden, body)
				}
			}
			if secrets.puts != 0 {
				t.Fatalf("puts=%d", secrets.puts)
			}
		})
	}
}

func TestRotateAndDeleteRequireIdempotencyKeyBeforeBodyDecodeOrEffects(t *testing.T) {
	secrets := &routeSecretWriter{ref: "opaque-ref"}
	store := platformaccounts.NewMemoryStore()
	account := platformaccounts.Account{
		ID: "acct_idempotency", Venue: "binance", Mode: "test", NativeEnvironment: platformaccounts.EnvironmentDemo,
		DisplayName: "Primary", ProductIDs: []string{"manual"}, ProductPolicy: []string{"manual"},
		Routes:               []platformaccounts.AccountRoute{{ID: "perp-usdt", Product: platformaccounts.ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: platformaccounts.RouteActive}},
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, State: platformaccounts.StateActive,
	}
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service, err := platformaccounts.New(platformaccounts.Options{Store: store, Secrets: secrets, Audit: routeAuditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	blockerCalls := 0
	mux := http.NewServeMux()
	if err := RegisterAccountRoutes(mux, AccountRoutesOptions{
		Service: service, Authenticate: func(*http.Request) (string, error) { return "principal", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
		Blockers: func(context.Context, string) (platformaccounts.Blockers, error) {
			blockerCalls++
			return platformaccounts.Blockers{}, nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, method, path, key string
	}{
		{name: "rotate missing", method: http.MethodPost, path: "/api/v1/accounts/acct_idempotency/rotate"},
		{name: "rotate blank", method: http.MethodPost, path: "/api/v1/accounts/acct_idempotency/rotate", key: " \t "},
		{name: "delete missing", method: http.MethodDelete, path: "/api/v1/accounts/acct_idempotency"},
		{name: "delete blank", method: http.MethodDelete, path: "/api/v1/accounts/acct_idempotency", key: " \t "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader("not-json"))
			request.Header.Set("If-Match", AccountETag(1))
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Idempotency-Key is required") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			persisted, getErr := store.Get(context.Background(), account.ID)
			if getErr != nil || persisted.DesiredStateRevision != 1 || secrets.puts != 0 || blockerCalls != 0 {
				t.Fatalf("account=%+v err=%v puts=%d blockerCalls=%d", persisted, getErr, secrets.puts, blockerCalls)
			}
		})
	}
}
