package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLoginBurstIsThrottledByNormalizedEmailAndDirectSource(t *testing.T) {
	handler, store := newTestServerWithStore(t)
	const (
		source   = "203.0.113.10:43120"
		password = "definitely-wrong"
	)
	for attempt := 0; attempt < 5; attempt++ {
		response := loginAttempt(t, handler, source, " OWNER@EXAMPLE.COM ", password)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", attempt+1, response.Code, response.Body.String())
		}
	}

	blocked := loginAttempt(t, handler, source, "owner@example.com", password)
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("blocked status=%d body=%s", blocked.Code, blocked.Body.String())
	}
	if blocked.Header().Get("Retry-After") == "" {
		t.Fatal("throttled login omitted Retry-After")
	}
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	if err := json.Unmarshal(blocked.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "rate_limited" || !envelope.Error.Retryable {
		t.Fatalf("throttle envelope=%+v", envelope)
	}
	if body := blocked.Body.String(); strings.Contains(body, "owner@example.com") ||
		strings.Contains(body, password) {
		t.Fatalf("throttle response leaked login material: %s", body)
	}

	otherSource := loginAttempt(t, handler, "198.51.100.20:51234",
		"owner@example.com", "first secure password")
	if otherSource.Code != http.StatusOK {
		t.Fatalf("independent source login status=%d body=%s",
			otherSource.Code, otherSource.Body.String())
	}

	foundThrottleAudit := false
	for _, event := range store.AuditEvents() {
		if event.Action == "auth.login" && event.Outcome == "throttled" {
			foundThrottleAudit = true
			if event.Email != "" {
				t.Fatalf("throttle audit retained email: %+v", event)
			}
		}
	}
	if !foundThrottleAudit {
		t.Fatalf("missing throttled login audit: %+v", store.AuditEvents())
	}
}

func TestSuccessfulLoginResetsPriorFailuresForTheSameSource(t *testing.T) {
	handler := newTestServer(t)
	const source = "203.0.113.11:43121"
	for attempt := 0; attempt < 4; attempt++ {
		response := loginAttempt(t, handler, source, "owner@example.com", "wrong-before-success")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("pre-success attempt %d status=%d", attempt+1, response.Code)
		}
	}
	success := loginAttempt(t, handler, source,
		"owner@example.com", "first secure password")
	if success.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", success.Code, success.Body.String())
	}
	for attempt := 0; attempt < 5; attempt++ {
		response := loginAttempt(t, handler, source, "owner@example.com", "wrong-after-success")
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("post-success attempt %d status=%d body=%s",
				attempt+1, response.Code, response.Body.String())
		}
	}
	blocked := loginAttempt(t, handler, source, "owner@example.com", "wrong-after-success")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("post-reset throttle status=%d body=%s", blocked.Code, blocked.Body.String())
	}
}

func TestLoginThrottleNormalizesSourcePortExpiresAndStaysBounded(t *testing.T) {
	key := loginThrottleKey(" OWNER@EXAMPLE.COM ", "203.0.113.12:40000")
	if key != loginThrottleKey("owner@example.com", "203.0.113.12:50000") {
		t.Fatal("source port or email normalization changed the throttle key")
	}
	if key == loginThrottleKey("owner@example.com", "203.0.113.13:40000") {
		t.Fatal("different direct source shared a throttle key")
	}

	clock := time.Date(2026, 7, 30, 6, 0, 0, 0, time.UTC)
	throttle := newLoginThrottle(func() time.Time { return clock })
	for attempt := 0; attempt < loginFailureLimit; attempt++ {
		throttle.RecordFailure(key)
	}
	if retry := throttle.RetryAfter(key); retry != loginLockDuration {
		t.Fatalf("retry=%s want=%s", retry, loginLockDuration)
	}
	clock = clock.Add(loginLockDuration)
	if retry := throttle.RetryAfter(key); retry != 0 {
		t.Fatalf("expired throttle retry=%s", retry)
	}

	for index := 0; index <= loginThrottleMaxEntries; index++ {
		entryKey := loginThrottleKey(
			fmt.Sprintf("owner-%d@example.com", index),
			"198.51.100.50:40000",
		)
		throttle.RecordFailure(entryKey)
	}
	if got := len(throttle.entries); got != loginThrottleMaxEntries {
		t.Fatalf("throttle entries=%d want=%d", got, loginThrottleMaxEntries)
	}
}

func loginAttempt(
	t *testing.T,
	handler http.Handler,
	remoteAddress string,
	email string,
	password string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/login",
		strings.NewReader(`{"email":`+quotedJSON(email)+`,"password":`+quotedJSON(password)+`}`),
	)
	request.RemoteAddr = remoteAddress
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func quotedJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
