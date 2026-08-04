package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

func TestAppStartsAndStops(t *testing.T) {
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BrowserListenAddr: "127.0.0.1:18191", WorkListenAddr: "127.0.0.1:18192",
		BootstrapUsername: "operator", BootstrapPasswordHash: passwordHash,
		FundingWorkerTokenFile:   "/protected/test-worker.token",
		FundingWorkerTokenDigest: sha256.Sum256([]byte("test-worker-token-with-at-least-32-bytes")),
		Mode:                     "test",
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Start(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
	}
}

func TestAppWiresOwnedRoutesAndKeepsLiveWritesDisabled(t *testing.T) {
	for _, mode := range []string{"test", "live"} {
		t.Run(mode, func(t *testing.T) {
			app, err := New(testConfig(t, mode))
			if err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"correct horse battery staple"}`))
			loginResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(loginResponse, login)
			if loginResponse.Code != http.StatusOK {
				t.Fatalf("login=%d body=%s", loginResponse.Code, loginResponse.Body.String())
			}
			cookie := loginResponse.Result().Cookies()[0]
			var loginBody struct {
				Data struct {
					Session struct {
						CSRFToken string `json:"csrfToken"`
					} `json:"session"`
				} `json:"data"`
			}
			if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
				t.Fatal(err)
			}

			accountsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
			accountsRequest.AddCookie(cookie)
			accountsResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(accountsResponse, accountsRequest)
			if accountsResponse.Code != http.StatusOK || strings.Contains(accountsResponse.Body.String(), `"scaffold":true`) {
				t.Fatalf("accounts=%d body=%s", accountsResponse.Code, accountsResponse.Body.String())
			}
			readinessRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
			readinessRequest.AddCookie(cookie)
			readinessResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(readinessResponse, readinessRequest)
			if readinessResponse.Code != http.StatusServiceUnavailable || strings.Contains(readinessResponse.Body.String(), "not_reported") {
				t.Fatalf("readiness=%d body=%s", readinessResponse.Code, readinessResponse.Body.String())
			}
			for _, component := range []string{"process", "database", "secretStore", "gateway", "publicData", "accounts", "fundingWorker"} {
				if !strings.Contains(readinessResponse.Body.String(), `"`+component+`"`) {
					t.Fatalf("readiness missing %s: %s", component, readinessResponse.Body.String())
				}
			}

			if mode == "live" {
				command := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(`{"accountId":"account-1","productId":"manual","lineageId":"lineage-1","reservationId":"","idempotencyKey":"command-1","commandKind":"place","mode":"live","payload":{}}`))
				command.AddCookie(cookie)
				command.Header.Set("Idempotency-Key", "command-1")
				command.Header.Set("X-CSRF-Token", loginBody.Data.Session.CSRFToken)
				response := httptest.NewRecorder()
				app.browser.Handler.ServeHTTP(response, command)
				if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "LIVE_PRIVATE_WRITE_DISABLED") {
					t.Fatalf("live command=%d body=%s", response.Code, response.Body.String())
				}
			}
		})
	}

	app, err := New(testConfig(t, "test"))
	if err != nil {
		t.Fatal(err)
	}
	registration := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`{"workerId":"worker-1","instanceId":"instance-1","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	registration.Header.Set("Authorization", "Bearer test-worker-token-with-at-least-32-bytes")
	response := httptest.NewRecorder()
	app.work.Handler.ServeHTTP(response, registration)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"scaffold":true`) {
		t.Fatalf("registration=%d body=%s", response.Code, response.Body.String())
	}
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"correct horse battery staple"}`))
	loginResponse := httptest.NewRecorder()
	app.browser.Handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	readinessRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	readinessRequest.AddCookie(loginResponse.Result().Cookies()[0])
	readinessResponse := httptest.NewRecorder()
	app.browser.Handler.ServeHTTP(readinessResponse, readinessRequest)
	var readinessBody struct {
		Data struct {
			Components map[string]struct {
				Ready bool `json:"ready"`
			} `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal(readinessResponse.Body.Bytes(), &readinessBody); err != nil {
		t.Fatal(err)
	}
	if !readinessBody.Data.Components["fundingWorker"].Ready {
		t.Fatalf("funding worker not ready after registration: %s", readinessResponse.Body.String())
	}
}

type recordingIdentitySeeder struct {
	identity session.Identity
}

func (store *recordingIdentitySeeder) Upsert(_ context.Context, identity session.Identity) error {
	store.identity = identity
	return nil
}

func TestSeedBootstrapIdentityUsesConfiguredModeAndDigest(t *testing.T) {
	cfg := testConfig(t, "live")
	store := &recordingIdentitySeeder{}
	if err := seedBootstrapIdentity(context.Background(), store, cfg); err != nil {
		t.Fatal(err)
	}
	if store.identity.PrincipalID != session.DefaultPrincipalID || store.identity.Username != cfg.BootstrapUsername || store.identity.PasswordHash != cfg.BootstrapPasswordHash || store.identity.Mode != "live" {
		t.Fatalf("seeded identity=%+v", store.identity)
	}
}

func testConfig(t *testing.T, mode string) config.Config {
	t.Helper()
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{BrowserListenAddr: "127.0.0.1:18191", WorkListenAddr: "127.0.0.1:18192", BootstrapUsername: "operator", BootstrapPasswordHash: passwordHash, FundingWorkerTokenFile: "/protected/worker.token", FundingWorkerTokenDigest: sha256.Sum256([]byte("test-worker-token-with-at-least-32-bytes")), Mode: mode}
}
