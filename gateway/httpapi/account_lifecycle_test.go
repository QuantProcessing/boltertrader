package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
)

type fakeAccountLifecycle struct {
	account   lifecycle.Account
	err       error
	principal string
	requestID string
	created   lifecycle.CreateRequest
	bundle    string
	blockers  lifecycle.Blockers
	keys      []string
	calls     []string
}

func (fake *fakeAccountLifecycle) record(ctx context.Context, call, principal string, keys ...string) {
	fake.calls = append(fake.calls, call)
	fake.principal = principal
	fake.requestID = lifecycle.RequestIDFromContext(ctx)
	fake.keys = keys
}

func (fake *fakeAccountLifecycle) List(context.Context) ([]lifecycle.Account, error) {
	fake.calls = append(fake.calls, "List")
	return []lifecycle.Account{fake.account}, fake.err
}

func (fake *fakeAccountLifecycle) Get(context.Context, string) (lifecycle.Account, error) {
	fake.calls = append(fake.calls, "Get")
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Create(ctx context.Context, request lifecycle.CreateRequest, principal string, keys ...string) (lifecycle.Account, error) {
	fake.record(ctx, "Create", principal, keys...)
	fake.created = request
	fake.bundle = string(request.CredentialBundle)
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) UpdateMetadata(ctx context.Context, _ string, _ int64, _ string, _ []string, principal string) (lifecycle.Account, error) {
	fake.record(ctx, "UpdateMetadata", principal)
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) MarkValidated(ctx context.Context, _ string, _ int64, principal string) (lifecycle.Account, error) {
	fake.record(ctx, "MarkValidated", principal)
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Rotate(ctx context.Context, _ string, _ int64, material []byte, principal string, keys ...string) (lifecycle.Account, error) {
	fake.record(ctx, "Rotate", principal, keys...)
	fake.bundle = string(material)
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Disable(ctx context.Context, _ string, _ int64, blockers lifecycle.Blockers, principal string) (lifecycle.Account, error) {
	fake.record(ctx, "Disable", principal)
	fake.blockers = blockers
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Drain(ctx context.Context, _ string, _ int64, blockers lifecycle.Blockers, principal string) (lifecycle.Account, error) {
	fake.record(ctx, "Drain", principal)
	fake.blockers = blockers
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Remove(ctx context.Context, _ string, _ int64, principal string, keys ...string) (lifecycle.Account, error) {
	fake.record(ctx, "Remove", principal)
	fake.keys = keys
	return fake.account, fake.err
}

const validCredentialBundle = `{"schemaVersion":1,"venue":"binance","authKind":"hmac","metadata":{"subaccount":"main"},"secrets":{"apiKey":"key","apiSecret":"secret"}}`

func newLifecycleHandler(t *testing.T, fake *fakeAccountLifecycle, blockers lifecycle.Blockers, blockersErr error) *InternalHandler {
	t.Helper()
	handler, err := NewInternalHandler(InternalOptions{
		Authority:    &fakeInternalAuthority{},
		Authenticate: func(*http.Request) error { return nil },
		Lifecycle:    fake,
		Blockers: func(context.Context, string) (lifecycle.Blockers, error) {
			return blockers, blockersErr
		},
	})
	if err != nil {
		t.Fatalf("NewInternalHandler: %v", err)
	}
	return handler
}

func lifecycleRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(accountPrincipalHeader, "operator-1")
	request.Header.Set("Idempotency-Key", "idem-1")
	request.Header.Set("If-Match", `"desired-state-revision-4"`)
	return request
}

func lifecycleAccount() lifecycle.Account {
	return lifecycle.Account{ID: "acct_1", Venue: "binance", DesiredStateRevision: 7, State: lifecycle.StateActive}
}

// A gateway that cannot count open orders must not be able to disable or drain
// an account: it would answer "nothing is blocking" for an account that still
// holds inventory.
func TestInternalHandlerRefusesAccountLifecycleWithoutABlockersSource(t *testing.T) {
	_, err := NewInternalHandler(InternalOptions{
		Authority:    &fakeInternalAuthority{},
		Authenticate: func(*http.Request) error { return nil },
		Lifecycle:    &fakeAccountLifecycle{},
	})
	if err == nil {
		t.Fatal("NewInternalHandler accepted a lifecycle with no blockers source")
	}
}

// The lifecycle routes only exist when a lifecycle is configured, so a gateway
// without one must 404 rather than answer with an empty account list.
func TestAccountLifecycleRoutesAreAbsentWithoutALifecycle(t *testing.T) {
	handler, err := NewInternalHandler(InternalOptions{
		Authority:    &fakeInternalAuthority{},
		Authenticate: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewInternalHandler: %v", err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodGet, "/internal/v1/accounts", ""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestAccountLifecycleRoutesMatchTheDeclaredSurface(t *testing.T) {
	declared := map[string]struct{}{}
	for _, route := range InternalAccountLifecycleRoutes() {
		declared[route.Method+" "+route.Pattern] = struct{}{}
	}
	expected := []string{
		"GET /internal/v1/accounts",
		"POST /internal/v1/accounts",
		"GET /internal/v1/accounts/{accountId}",
		"PATCH /internal/v1/accounts/{accountId}",
		"POST /internal/v1/accounts/{accountId}/validate",
		"POST /internal/v1/accounts/{accountId}/rotate",
		"POST /internal/v1/accounts/{accountId}/disable",
		"POST /internal/v1/accounts/{accountId}/drain",
		"DELETE /internal/v1/accounts/{accountId}",
	}
	if len(declared) != len(expected) {
		t.Fatalf("declared %d routes, want %d", len(declared), len(expected))
	}
	for _, route := range expected {
		if _, ok := declared[route]; !ok {
			t.Fatalf("route %q is not declared", route)
		}
	}
	// The lifecycle routes are deliberately kept out of InternalRoutes so a
	// deployment without a lifecycle does not advertise a surface it 404s on.
	for _, route := range InternalRoutes() {
		if _, ok := declared[route.Method+" "+route.Pattern]; ok {
			t.Fatalf("lifecycle route %q leaked into InternalRoutes", route.Pattern)
		}
	}
}

func TestAccountLifecycleRoutesReachTheService(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		body   string
		status int
		call   string
	}{
		{"list", http.MethodGet, "/internal/v1/accounts", "", http.StatusOK, "List"},
		{"get", http.MethodGet, "/internal/v1/accounts/acct_1", "", http.StatusOK, "Get"},
		{"create", http.MethodPost, "/internal/v1/accounts", `{"venue":"binance","displayName":"Main","productPolicy":["perp"],"routes":[{"routeId":"perp-usdt","product":"perp","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":` + validCredentialBundle + `}`, http.StatusCreated, "Create"},
		{"update", http.MethodPatch, "/internal/v1/accounts/acct_1", `{"displayName":"Renamed","productIds":["perp"]}`, http.StatusOK, "UpdateMetadata"},
		{"validate", http.MethodPost, "/internal/v1/accounts/acct_1/validate", "", http.StatusOK, "MarkValidated"},
		{"rotate", http.MethodPost, "/internal/v1/accounts/acct_1/rotate", `{"credentialBundle":` + validCredentialBundle + `}`, http.StatusOK, "Rotate"},
		{"disable", http.MethodPost, "/internal/v1/accounts/acct_1/disable", "", http.StatusOK, "Disable"},
		{"drain", http.MethodPost, "/internal/v1/accounts/acct_1/drain", "", http.StatusOK, "Drain"},
		// Removal reports 202: the credential is only deleted once the runtime
		// confirms the account is tombstoned.
		{"remove", http.MethodDelete, "/internal/v1/accounts/acct_1", "", http.StatusAccepted, "Remove"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(testCase.method, testCase.path, testCase.body))

			if response.Code != testCase.status {
				t.Fatalf("status = %d (%s), want %d", response.Code, response.Body.String(), testCase.status)
			}
			if len(fake.calls) != 1 || fake.calls[0] != testCase.call {
				t.Fatalf("calls = %v, want [%s]", fake.calls, testCase.call)
			}
			if testCase.call != "List" && testCase.call != "Get" {
				if fake.principal != "operator-1" {
					t.Fatalf("principal = %q, want operator-1", fake.principal)
				}
				// Every transition must be traceable to the request that caused
				// it, because the audit record is written under this id.
				if fake.requestID == "" {
					t.Fatal("request id was not carried into the service")
				}
			}
			if testCase.call == "List" {
				return
			}
			// Concurrent editors rely on the ETag to detect that the account
			// moved underneath them, so it must ride every account response.
			if etag := response.Header().Get("ETag"); etag != `"desired-state-revision-7"` {
				t.Fatalf("ETag = %q", etag)
			}
		})
	}
}

// The credential bundle must never survive the request: it is cleared in place,
// so what the service receives is the last chance to observe it.
func TestAccountLifecycleClearsTheCredentialBundleAndKeepsItOutOfResponses(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{"create", http.MethodPost, "/internal/v1/accounts", `{"venue":"binance","displayName":"Main","productPolicy":["perp"],"routes":[],"credentialBundle":` + validCredentialBundle + `}`},
		{"rotate", http.MethodPost, "/internal/v1/accounts/acct_1/rotate", `{"credentialBundle":` + validCredentialBundle + `}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(testCase.method, testCase.path, testCase.body))

			if !strings.Contains(fake.bundle, "apiSecret") {
				t.Fatalf("service never received the bundle: %q", fake.bundle)
			}
			if strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("response echoed credential material: %s", response.Body.String())
			}
		})
	}
}

// A key the credential decoder does not know is a typo or an injection attempt;
// either way the account must not be created with material nobody validated.
func TestAccountLifecycleRejectsUnknownCredentialKeysWithoutNamingThem(t *testing.T) {
	for name, bundle := range map[string]string{
		"unknown envelope key": `{"schemaVersion":1,"venue":"binance","attackerField":"x"}`,
		"unknown secret key":   `{"venue":"binance","secrets":{"apiKey":"k","sshKey":"leak"}}`,
		"unknown metadata key": `{"venue":"binance","metadata":{"privilege":"root"}}`,
		// The credential decoder folds case, so two spellings of one secret
		// would resolve to whichever it happened to prefer.
		"case-variant duplicate": `{"venue":"binance","secrets":{"apiKey":"k","APIKEY":"other"}}`,
		"non-object secrets":     `{"venue":"binance","secrets":"not-an-object"}`,
		"not an object":          `"a string"`,
		"absent":                 `null`,
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts", `{"venue":"binance","displayName":"Main","credentialBundle":`+bundle+`}`))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d (%s), want 400", response.Code, response.Body.String())
			}
			if len(fake.calls) != 0 {
				t.Fatalf("service was called with an unvalidated bundle: %v", fake.calls)
			}
			for _, leaked := range []string{"attackerField", "sshKey", "privilege", "APIKEY"} {
				if strings.Contains(response.Body.String(), leaked) {
					t.Fatalf("response named the rejected key: %s", response.Body.String())
				}
			}
		})
	}
}

// Create, rotate and remove are the operations a caller retries after a
// timeout. Without a key, a retry would mint a second credential or delete an
// account that was recreated in between.
func TestAccountLifecycleRequiresAnIdempotencyKeyOnCredentialOperations(t *testing.T) {
	for name, testCase := range map[string]struct {
		method string
		path   string
		body   string
	}{
		"create": {http.MethodPost, "/internal/v1/accounts", `{"venue":"binance","credentialBundle":` + validCredentialBundle + `}`},
		"rotate": {http.MethodPost, "/internal/v1/accounts/acct_1/rotate", `{"credentialBundle":` + validCredentialBundle + `}`},
		"remove": {http.MethodDelete, "/internal/v1/accounts/acct_1", ""},
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			request := lifecycleRequest(testCase.method, testCase.path, testCase.body)
			request.Header.Del("Idempotency-Key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("service was called without an idempotency key: %v", fake.calls)
			}
		})
	}
}

// Without If-Match a stale client would overwrite a decision it never saw.
func TestAccountLifecycleRequiresIfMatchOnEveryMutation(t *testing.T) {
	for name, testCase := range map[string]struct {
		method string
		path   string
	}{
		"update":   {http.MethodPatch, "/internal/v1/accounts/acct_1"},
		"validate": {http.MethodPost, "/internal/v1/accounts/acct_1/validate"},
		"rotate":   {http.MethodPost, "/internal/v1/accounts/acct_1/rotate"},
		"disable":  {http.MethodPost, "/internal/v1/accounts/acct_1/disable"},
		"drain":    {http.MethodPost, "/internal/v1/accounts/acct_1/drain"},
		"remove":   {http.MethodDelete, "/internal/v1/accounts/acct_1"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, header := range []string{"", "garbage", `"desired-state-revision-0"`, `W/"desired-state-revision-4"`} {
				fake := &fakeAccountLifecycle{account: lifecycleAccount()}
				handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
				request := lifecycleRequest(testCase.method, testCase.path, "")
				request.Header.Set("If-Match", header)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)

				if response.Code != http.StatusPreconditionRequired {
					t.Fatalf("If-Match %q: status = %d, want 428", header, response.Code)
				}
				if len(fake.calls) != 0 {
					t.Fatalf("If-Match %q: service was called: %v", header, fake.calls)
				}
			}
		})
	}
}

// Auth-edge names the human behind the request. Accepting a blank or
// header-shaped principal would write an unattributable audit record.
func TestAccountLifecycleRequiresAWellFormedPrincipal(t *testing.T) {
	for name, principal := range map[string]string{
		"absent":       "",
		"with spaces":  "operator one",
		"header-shape": "Authorization=Bearer token",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			request := lifecycleRequest(http.MethodGet, "/internal/v1/accounts", "")
			request.Header.Set(accountPrincipalHeader, principal)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", response.Code)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("service was called without a principal: %v", fake.calls)
			}
		})
	}
}

// The audit store rejects records carrying header-shaped text, which would turn
// a valid mutation into a 500 long after the transition committed.
func TestAccountLifecycleReplacesAnUnsafeRequestID(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
	request := lifecycleRequest(http.MethodPatch, "/internal/v1/accounts/acct_1", `{"displayName":"Renamed"}`)
	request.Header.Set("X-Request-ID", "Authorization=Bearer secret-token")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if strings.Contains(fake.requestID, "secret-token") {
		t.Fatalf("request id carried header text: %q", fake.requestID)
	}
	if !strings.HasPrefix(fake.requestID, "req_") {
		t.Fatalf("request id = %q, want a generated req_ id", fake.requestID)
	}

	fake.requestID = ""
	safe := lifecycleRequest(http.MethodPatch, "/internal/v1/accounts/acct_1", `{"displayName":"Renamed"}`)
	safe.Header.Set("X-Request-ID", "req-from-auth-edge")
	handler.ServeHTTP(httptest.NewRecorder(), safe)
	if fake.requestID != "req-from-auth-edge" {
		t.Fatalf("request id = %q, want the caller's", fake.requestID)
	}
}

// Validation probes the venue with the credential and removal deletes it;
// neither is safe while the account still holds inventory.
func TestAccountLifecycleRefusesValidateAndRemoveWhileBlocked(t *testing.T) {
	for name, path := range map[string]string{
		"validate": "/internal/v1/accounts/acct_1/validate",
		"remove":   "/internal/v1/accounts/acct_1",
	} {
		t.Run(name, func(t *testing.T) {
			method := http.MethodPost
			if name == "remove" {
				method = http.MethodDelete
			}
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{OpenOrders: 2}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(method, path, ""))

			if response.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", response.Code)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("service was called while blocked: %v", fake.calls)
			}
		})
	}
}

// Disable and drain are how an operator responds to inventory they cannot
// clear, so the counts go to the service rather than stopping the request.
func TestAccountLifecyclePassesBlockersIntoDisableAndDrain(t *testing.T) {
	for name, path := range map[string]string{
		"disable": "/internal/v1/accounts/acct_1/disable",
		"drain":   "/internal/v1/accounts/acct_1/drain",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{OpenOrders: 3, OwnedPositions: 1}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, path, ""))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d (%s), want 200", response.Code, response.Body.String())
			}
			if fake.blockers.OpenOrders != 3 || fake.blockers.OwnedPositions != 1 {
				t.Fatalf("blockers = %+v", fake.blockers)
			}
		})
	}
}

// An unanswerable blockers query must not read as "nothing is blocking".
func TestAccountLifecycleRefusesTransitionsWhenBlockersCannotBeCounted(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, errors.New("inventory unavailable"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/drain", ""))

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("service was called with uncounted blockers: %v", fake.calls)
	}
}

func TestAccountLifecycleMapsServiceFailuresOntoStableCodes(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		status int
		code   string
	}{
		{lifecycle.ErrNotFound, http.StatusNotFound, "ACCOUNT_NOT_FOUND"},
		{lifecycle.ErrRevisionConflict, http.StatusConflict, "DESIRED_STATE_REVISION_CONFLICT"},
		{lifecycle.ErrIdempotencyConflict, http.StatusConflict, "IDEMPOTENCY_CONFLICT"},
		{lifecycle.ErrInvalidTransition, http.StatusConflict, "INVALID_ACCOUNT_TRANSITION"},
		{lifecycle.ErrAccountBlocked, http.StatusConflict, "ACCOUNT_LIFECYCLE_BLOCKED"},
		{fmt.Errorf("%w: display name is invalid", lifecycle.ErrValidation), http.StatusBadRequest, "VALIDATION_ERROR"},
		{errors.New("postgres connection refused"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		t.Run(testCase.code, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount(), err: testCase.err}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(http.MethodPatch, "/internal/v1/accounts/acct_1", `{"displayName":"Renamed"}`))

			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d", response.Code, testCase.status)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Code != testCase.code {
				t.Fatalf("code = %q, want %q", body.Error.Code, testCase.code)
			}
			// The internal plane never echoes the underlying error, so a
			// database DSN or a credential in a driver message cannot escape.
			if strings.Contains(body.Error.Message, "postgres") {
				t.Fatalf("message leaked the underlying error: %q", body.Error.Message)
			}
		})
	}
}

// "desired" and "runtime" name sibling collections under this prefix, so an
// account with either id could never be addressed by its own route.
func TestAccountLifecycleRejectsIdsThatCollideWithSiblingCollections(t *testing.T) {
	for _, accountID := range []string{"desired", "runtime", "acct%201", strings.Repeat("a", 129)} {
		fake := &fakeAccountLifecycle{account: lifecycleAccount()}
		handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, lifecycleRequest(http.MethodPatch, "/internal/v1/accounts/"+accountID, `{"displayName":"x"}`))

		if response.Code == http.StatusOK {
			t.Fatalf("account id %q was accepted", accountID)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("account id %q reached the service: %v", accountID, fake.calls)
		}
	}
}

// The desired-state PUT routes must keep resolving to the authority now that a
// wildcard sibling shares their prefix.
func TestAccountLifecycleDoesNotShadowTheDesiredStateRoutes(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	authority := &fakeInternalAuthority{response: AuthorityResponse{Status: http.StatusOK, Body: map[string]any{"accounts": []any{}}}}
	handler, err := NewInternalHandler(InternalOptions{
		Authority:    authority,
		Authenticate: func(*http.Request) error { return nil },
		Lifecycle:    fake,
		Blockers: func(context.Context, string) (lifecycle.Blockers, error) {
			return lifecycle.Blockers{}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewInternalHandler: %v", err)
	}
	handler.ServeHTTP(httptest.NewRecorder(), lifecycleRequest(http.MethodGet, "/internal/v1/accounts/desired", ""))

	if len(authority.requests) != 1 || authority.requests[0].Operation != AuthorityListDesired {
		t.Fatalf("authority requests = %+v", authority.requests)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("lifecycle handled a desired-state route: %v", fake.calls)
	}
}

// The write gate exists so a gateway that has not yet reconciled cannot be
// steered. Lifecycle mutations are writes like any other.
func TestAccountLifecycleMutationsAreHeldBackUntilWritesAreReady(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	handler, err := NewInternalHandler(InternalOptions{
		Authority:    &fakeInternalAuthority{},
		Authenticate: func(*http.Request) error { return nil },
		WriteReady:   func() bool { return false },
		Lifecycle:    fake,
		Blockers: func(context.Context, string) (lifecycle.Blockers, error) {
			return lifecycle.Blockers{}, nil
		},
	})
	if err != nil {
		t.Fatalf("NewInternalHandler: %v", err)
	}
	for _, testCase := range []struct{ method, path string }{
		{http.MethodPost, "/internal/v1/accounts"},
		{http.MethodPatch, "/internal/v1/accounts/acct_1"},
		{http.MethodDelete, "/internal/v1/accounts/acct_1"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, lifecycleRequest(testCase.method, testCase.path, "{}"))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: status = %d, want 503", testCase.method, testCase.path, response.Code)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatalf("service was called while writes were not ready: %v", fake.calls)
	}

	// Reads stay available: an operator needs to see the account to know why.
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodGet, "/internal/v1/accounts", ""))
	if response.Code != http.StatusOK {
		t.Fatalf("read status = %d, want 200", response.Code)
	}
}

// Bodyless transitions carry their intent in the path. Accepting a body would
// let a caller believe fields it sent were honoured.
func TestAccountLifecycleRejectsBodiesOnBodylessTransitions(t *testing.T) {
	for name, path := range map[string]string{
		"validate": "/internal/v1/accounts/acct_1/validate",
		"disable":  "/internal/v1/accounts/acct_1/disable",
		"drain":    "/internal/v1/accounts/acct_1/drain",
	} {
		t.Run(name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, path, `{"force":true}`))

			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.Code)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("service was called with an unread body: %v", fake.calls)
			}
		})
	}
}
