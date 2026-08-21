package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/account"
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

func (fake *fakeAccountLifecycle) Disable(ctx context.Context, _ string, _ int64, blockers lifecycle.Blockers, principal string) (lifecycle.Account, error) {
	fake.record(ctx, "Disable", principal)
	fake.blockers = blockers
	return fake.account, fake.err
}

func (fake *fakeAccountLifecycle) Remove(ctx context.Context, _ string, _ int64, principal string, keys ...string) (lifecycle.Account, error) {
	fake.record(ctx, "Remove", principal, keys...)
	return fake.account, fake.err
}

const validCredentialBundle = `{"schemaVersion":1,"venue":"binance","authKind":"hmac","metadata":{"subaccount":"main"},"secrets":{"apiKey":"key","apiSecret":"secret"}}`

func newLifecycleHandler(t *testing.T, fake *fakeAccountLifecycle, blockers lifecycle.Blockers, blockersErr error) *InternalHandler {
	t.Helper()
	handler, err := NewInternalHandler(InternalOptions{
		Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil },
		Lifecycle: fake,
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
	request.Header.Set(accountIdempotencyHeader, "idem-1")
	return request
}

func lifecycleAccount() lifecycle.Account {
	return lifecycle.Account{ID: "acct_1", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11", Venue: "binance", State: lifecycle.StateActive}
}

func TestInternalHandlerRefusesAccountLifecycleWithoutABlockersSource(t *testing.T) {
	_, err := NewInternalHandler(InternalOptions{
		Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil },
		Lifecycle: &fakeAccountLifecycle{},
	})
	if err == nil {
		t.Fatal("NewInternalHandler accepted a lifecycle with no blockers source")
	}
}

func TestAccountLifecycleRoutesAreAbsentWithoutALifecycle(t *testing.T) {
	handler, err := NewInternalHandler(InternalOptions{
		Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodGet, "/internal/v1/accounts", ""))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestAccountLifecycleRoutesMatchImmutableCredentialSurface(t *testing.T) {
	got := make(map[string]bool)
	for _, route := range InternalAccountLifecycleRoutes() {
		got[route.Method+" "+route.Pattern] = true
	}
	want := []string{
		"GET /internal/v1/accounts",
		"POST /internal/v1/accounts",
		"GET /internal/v1/accounts/{accountId}",
		"POST /internal/v1/accounts/{accountId}/disable",
		"DELETE /internal/v1/accounts/{accountId}",
	}
	if len(got) != len(want) {
		t.Fatalf("routes=%v", got)
	}
	for _, route := range want {
		if !got[route] {
			t.Fatalf("missing route %q", route)
		}
	}
}

func TestAccountLifecycleRoutesReachTheServiceWithoutVersionHeaders(t *testing.T) {
	createBody := `{"venue":"binance","displayName":"Main","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.v1","requestedState":"active"}],"credentialBundle":` + validCredentialBundle + `}`
	for _, test := range []struct {
		name, method, path, body, call string
		status                         int
	}{
		{"list", http.MethodGet, "/internal/v1/accounts", "", "List", http.StatusOK},
		{"get", http.MethodGet, "/internal/v1/accounts/acct_1", "", "Get", http.StatusOK},
		{"create", http.MethodPost, "/internal/v1/accounts", createBody, "Create", http.StatusCreated},
		{"disable", http.MethodPost, "/internal/v1/accounts/acct_1/disable", "", "Disable", http.StatusOK},
		{"remove", http.MethodDelete, "/internal/v1/accounts/acct_1", "", "Remove", http.StatusAccepted},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeAccountLifecycle{account: lifecycleAccount()}
			handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, lifecycleRequest(test.method, test.path, test.body))
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			wantCalls := test.call
			if test.call == "Remove" {
				wantCalls = "Get,Remove"
			}
			if strings.Join(fake.calls, ",") != wantCalls {
				t.Fatalf("calls=%v want=%s", fake.calls, wantCalls)
			}
			if response.Header().Get("ETag") != "" {
				t.Fatalf("version ETag leaked: %q", response.Header().Get("ETag"))
			}
		})
	}
}

func TestRemovedAccountMutationRoutesStayAbsent(t *testing.T) {
	for _, test := range []struct{ method, path string }{
		{http.MethodPatch, "/internal/v1/accounts/acct_1"},
		{http.MethodPost, "/internal/v1/accounts/acct_1/validate"},
		{http.MethodPost, "/internal/v1/accounts/acct_1/rotate"},
		{http.MethodPost, "/internal/v1/accounts/acct_1/drain"},
	} {
		fake := &fakeAccountLifecycle{account: lifecycleAccount()}
		response := httptest.NewRecorder()
		newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, lifecycleRequest(test.method, test.path, "{}"))
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s %s status=%d", test.method, test.path, response.Code)
		}
		if len(fake.calls) != 0 {
			t.Fatalf("removed route reached service: %v", fake.calls)
		}
	}
}

func TestAccountLifecycleListHidesRemovedAuditRows(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	fake.account.State = lifecycle.StateRemoved
	response := httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, lifecycleRequest(http.MethodGet, "/internal/v1/accounts", ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"accounts":[]`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
}

func TestAccountLifecycleCreateClearsCredentialBundleAndRejectsUnknownKeys(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	handler := newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil)
	body := `{"venue":"binance","displayName":"Main","productPolicy":["manual"],"routes":[],"credentialBundle":` + validCredentialBundle + `}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts", body))
	if !strings.Contains(fake.bundle, "apiSecret") || strings.Contains(response.Body.String(), "secret") {
		t.Fatalf("bundle=%q response=%s", fake.bundle, response.Body.String())
	}

	fake = &fakeAccountLifecycle{account: lifecycleAccount()}
	response = httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts",
		`{"venue":"binance","displayName":"Main","credentialBundle":{"venue":"binance","secrets":{"apiKey":"k","sshKey":"leak"}}}`))
	if response.Code != http.StatusBadRequest || len(fake.calls) != 0 || strings.Contains(response.Body.String(), "sshKey") {
		t.Fatalf("status=%d calls=%v body=%s", response.Code, fake.calls, response.Body.String())
	}
}

func TestAccountLifecycleRequiresIdempotencyForCreateAndRemove(t *testing.T) {
	for _, test := range []struct{ method, path, body string }{
		{http.MethodPost, "/internal/v1/accounts", `{"venue":"binance","credentialBundle":` + validCredentialBundle + `}`},
		{http.MethodDelete, "/internal/v1/accounts/acct_1", ""},
	} {
		fake := &fakeAccountLifecycle{account: lifecycleAccount()}
		request := lifecycleRequest(test.method, test.path, test.body)
		request.Header.Del(accountIdempotencyHeader)
		response := httptest.NewRecorder()
		newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || len(fake.calls) != 0 {
			t.Fatalf("status=%d calls=%v", response.Code, fake.calls)
		}
	}
}

func TestAccountLifecycleUsesBlockersForDisableAndRemoval(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	response := httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{OpenOrders: 2}, nil).ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/disable", ""))
	if response.Code != http.StatusOK || fake.blockers.OpenOrders != 2 {
		t.Fatalf("disable status=%d blockers=%+v", response.Code, fake.blockers)
	}

	fake = &fakeAccountLifecycle{account: lifecycleAccount()}
	response = httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{OwnedPositions: 1}, nil).ServeHTTP(response, lifecycleRequest(http.MethodDelete, "/internal/v1/accounts/acct_1", ""))
	if response.Code != http.StatusConflict || strings.Join(fake.calls, ",") != "Get" {
		t.Fatalf("remove status=%d calls=%v", response.Code, fake.calls)
	}
}

func TestFailedCreatingAccountCanBeRemovedWithoutAnActiveRuntime(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	fake.account.State = lifecycle.StateCreating
	response := httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, account.ErrUnknownAccount).ServeHTTP(
		response,
		lifecycleRequest(http.MethodDelete, "/internal/v1/accounts/acct_1", ""),
	)

	if response.Code != http.StatusAccepted || strings.Join(fake.calls, ",") != "Get,Remove" {
		t.Fatalf("remove status=%d calls=%v body=%s", response.Code, fake.calls, response.Body.String())
	}
}

func TestActiveAccountStillFailsClosedWhenRuntimeBlockersAreUnavailable(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	response := httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, account.ErrUnknownAccount).ServeHTTP(
		response,
		lifecycleRequest(http.MethodDelete, "/internal/v1/accounts/acct_1", ""),
	)

	if response.Code != http.StatusServiceUnavailable || strings.Join(fake.calls, ",") != "Get" ||
		!strings.Contains(response.Body.String(), "ACCOUNT_BLOCKERS_UNAVAILABLE") {
		t.Fatalf("remove status=%d calls=%v body=%s", response.Code, fake.calls, response.Body.String())
	}
}

func TestAccountLifecycleRequiresWellFormedPrincipalAndSanitizesRequestID(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	request := lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/disable", "")
	request.Header.Set(accountPrincipalHeader, "operator one")
	response := httptest.NewRecorder()
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || len(fake.calls) != 0 {
		t.Fatalf("status=%d calls=%v", response.Code, fake.calls)
	}

	fake = &fakeAccountLifecycle{account: lifecycleAccount()}
	request = lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/disable", "")
	request.Header.Set(accountRequestIDHeader, "Authorization=Bearer secret")
	newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(httptest.NewRecorder(), request)
	if !strings.HasPrefix(fake.requestID, "req_") || strings.Contains(fake.requestID, "secret") {
		t.Fatalf("requestID=%q", fake.requestID)
	}
}

func TestAccountLifecycleMapsStateConflictsWithoutVersionLanguage(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{lifecycle.ErrNotFound, http.StatusNotFound, "ACCOUNT_NOT_FOUND"},
		{lifecycle.ErrRevisionConflict, http.StatusConflict, "ACCOUNT_STATE_CONFLICT"},
		{lifecycle.ErrInvalidTransition, http.StatusConflict, "INVALID_ACCOUNT_TRANSITION"},
		{errors.New("postgres unavailable"), http.StatusInternalServerError, "INTERNAL_ERROR"},
	} {
		fake := &fakeAccountLifecycle{account: lifecycleAccount(), err: test.err}
		response := httptest.NewRecorder()
		newLifecycleHandler(t, fake, lifecycle.Blockers{}, nil).ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/disable", ""))
		if response.Code != test.status {
			t.Fatalf("err=%v status=%d", test.err, response.Code)
		}
		var body struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Error.Code != test.code {
			t.Fatalf("body=%s err=%v", response.Body.String(), err)
		}
	}
}

func TestAccountLifecycleMutationsWaitForWriteReadiness(t *testing.T) {
	fake := &fakeAccountLifecycle{account: lifecycleAccount()}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil },
		WriteReady: func() bool { return false }, Lifecycle: fake,
		Blockers: func(context.Context, string) (lifecycle.Blockers, error) { return lifecycle.Blockers{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, lifecycleRequest(http.MethodPost, "/internal/v1/accounts/acct_1/disable", ""))
	if response.Code != http.StatusServiceUnavailable || len(fake.calls) != 0 {
		t.Fatalf("status=%d calls=%v", response.Code, fake.calls)
	}
}
