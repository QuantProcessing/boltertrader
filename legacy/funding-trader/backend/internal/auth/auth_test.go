package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBootstrapHashesPasswordAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)

	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatalf("bootstrap first: %v", err)
	}
	user, err := store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	if user.PasswordHash == "first secure password" {
		t.Fatal("password stored in plaintext")
	}
	if err := CheckPassword(user.PasswordHash, "first secure password"); err != nil {
		t.Fatalf("hash did not verify: %v", err)
	}

	originalHash := user.PasswordHash
	if err := svc.Bootstrap(ctx, "owner@example.com", "second secure password"); err != nil {
		t.Fatalf("bootstrap second: %v", err)
	}
	user, _ = store.UserByEmail(ctx, "owner@example.com")
	if user.PasswordHash != originalHash {
		t.Fatal("bootstrap overwrote existing password")
	}
}

func TestConcurrentBootstrapCreatesOneUserAndDoesNotOverwrite(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)
	const workers = 16
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- svc.Bootstrap(ctx, "owner@example.com", fmt.Sprintf("secure bootstrap password %02d", i))
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("bootstrap error: %v", err)
		}
	}
	if count, err := store.UserCount(ctx); err != nil || count != 1 {
		t.Fatalf("user count = %d err=%v", count, err)
	}
	user, err := store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	hash := user.PasswordHash
	if err := svc.Bootstrap(ctx, "owner@example.com", "secure bootstrap password replacement"); err != nil {
		t.Fatal(err)
	}
	user, err = store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash != hash {
		t.Fatal("bootstrap overwrote existing hash")
	}
}

func TestModeSwitchInvalidatesOldSessionAndCSRFWithoutReplacingBootstrapPassword(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	testSvc := NewService(store, "test", time.Hour, false)
	testSvc.SetClock(func() time.Time { return clock })
	if err := testSvc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatalf("bootstrap test: %v", err)
	}
	login, err := testSvc.Login(ctx, "owner@example.com", "first secure password", "req-test")
	if err != nil {
		t.Fatalf("login test: %v", err)
	}
	if login.Principal.Mode != "test" {
		t.Fatalf("test login principal mode=%q, want test", login.Principal.Mode)
	}
	sessions := store.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("stored sessions = %d, want 1", len(sessions))
	}
	if sessions[0].Mode != "test" {
		t.Fatalf("stored test session mode=%q, want test", sessions[0].Mode)
	}

	clock = clock.Add(time.Minute)
	live := NewService(store, "live", time.Hour, true)
	live.SetClock(func() time.Time { return clock })
	if err := live.Bootstrap(ctx, "owner@example.com", "replacement password must not win"); err != nil {
		t.Fatalf("bootstrap live: %v", err)
	}

	user, err := store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("load switched user: %v", err)
	}
	if err := CheckPassword(user.PasswordHash, "first secure password"); err != nil {
		t.Fatalf("bootstrap did not preserve original password: %v", err)
	}
	if _, err := live.Authenticate(ctx, login.Session.Token); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old test session error=%v, want ErrInvalidSession", err)
	}
	if err := live.RequireCSRF(ctx, login.Session.Token, login.Session.CSRFToken); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old test csrf error=%v, want ErrInvalidSession", err)
	}
	if err := live.Logout(ctx, login.Session.Token, login.Session.CSRFToken, "req-live-old-logout"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("old test logout error=%v, want ErrInvalidSession", err)
	}
	if _, err := live.Login(ctx, "owner@example.com", "replacement password must not win", "req-replacement"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("replacement bootstrap password error=%v, want ErrInvalidCredentials", err)
	}
	liveLogin, err := live.Login(ctx, "owner@example.com", "first secure password", "req-live")
	if err != nil {
		t.Fatalf("login with original password: %v", err)
	}
	if liveLogin.Principal.Mode != "live" {
		t.Fatalf("login principal mode=%q, want live", liveLogin.Principal.Mode)
	}
	current, err := live.CurrentSession(ctx, liveLogin.Session.Token)
	if err != nil {
		t.Fatalf("current live session: %v", err)
	}
	if current.Principal.Mode != "live" {
		t.Fatalf("current principal mode=%q, want live", current.Principal.Mode)
	}
	if err := live.RequireCSRF(ctx, liveLogin.Session.Token, liveLogin.Session.CSRFToken); err != nil {
		t.Fatalf("live csrf rejected: %v", err)
	}
	hasLiveSession := false
	for _, session := range store.Sessions() {
		if session.TokenDigest == DigestToken(liveLogin.Session.Token) {
			hasLiveSession = true
			if session.Mode != "live" {
				t.Fatalf("stored live session mode=%q, want live", session.Mode)
			}
		}
	}
	if !hasLiveSession {
		t.Fatal("live login did not persist a live-scoped session")
	}
}

func TestSessionLifecycleExpiryLogoutReplayAndCSRF(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)
	svc.SetClock(func() time.Time { return clock })
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	login, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-login")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if login.Principal.ID != DefaultPrincipalID || !login.Session.ExpiresAt.Equal(clock.Add(time.Hour)) {
		t.Fatalf("unexpected login result: %+v", login)
	}
	if _, err := svc.Authenticate(ctx, login.Session.Token); err != nil {
		t.Fatalf("authenticate fresh: %v", err)
	}
	if err := svc.RequireCSRF(ctx, login.Session.Token, "wrong"); err == nil {
		t.Fatal("wrong CSRF accepted")
	}
	if err := svc.RequireCSRF(ctx, login.Session.Token, login.Session.CSRFToken); err != nil {
		t.Fatalf("valid CSRF rejected: %v", err)
	}
	current, err := svc.CurrentSession(ctx, login.Session.Token)
	if err != nil {
		t.Fatalf("current session: %v", err)
	}
	if current.Session.CSRFToken != login.Session.CSRFToken || !current.Session.ExpiresAt.Equal(login.Session.ExpiresAt) {
		t.Fatalf("current session did not rederive metadata: %+v vs %+v", current.Session, login.Session)
	}

	if err := svc.Logout(ctx, login.Session.Token, login.Session.CSRFToken, "req-logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := svc.Authenticate(ctx, login.Session.Token); err == nil {
		t.Fatal("replayed logged-out session accepted")
	}
	if err := svc.Logout(ctx, login.Session.Token, login.Session.CSRFToken, "req-repeat"); err != nil {
		t.Fatalf("repeat logout should be idempotent: %v", err)
	}

	second, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-second")
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	clock = clock.Add(2 * time.Hour)
	if _, err := svc.Authenticate(ctx, second.Session.Token); err == nil {
		t.Fatal("expired session accepted")
	}
	if err := svc.Logout(ctx, second.Session.Token, second.Session.CSRFToken, "req-expired"); err != nil {
		t.Fatalf("expired logout should be idempotent: %v", err)
	}
}

func TestLoginStoresNoRawTokenSubstringInSessionID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	login, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-login")
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range store.Sessions() {
		if session.ID == "" {
			t.Fatal("session id is empty")
		}
		if strings.Contains(login.Session.Token, session.ID) || strings.Contains(session.ID, login.Session.Token[:12]) {
			t.Fatalf("session id contains raw bearer token material: id=%q token=%q", session.ID, login.Session.Token)
		}
	}
}

func TestLoginPropagatesInfrastructureErrorsAndDoesNotPersistPartialSession(t *testing.T) {
	ctx := context.Background()
	infraErr := errors.New("database unavailable")
	store := NewMemoryStore()
	svc := NewService(&userLookupErrorStore{MemoryStore: store, err: infraErr}, "test", time.Hour, false)
	if _, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-infra"); !errors.Is(err, infraErr) {
		t.Fatalf("login error = %v, want infra error", err)
	}

	store = NewMemoryStore()
	svc = NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	txErr := errors.New("commit failed")
	txStore := &loginTxErrorStore{MemoryStore: store, err: txErr}
	svc = NewService(txStore, "test", time.Hour, false)
	if _, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-tx"); !errors.Is(err, txErr) {
		t.Fatalf("login tx error = %v, want tx error", err)
	}
	if sessions := store.Sessions(); len(sessions) != 0 {
		t.Fatalf("partial login persisted sessions: %+v", sessions)
	}
}

func TestLogoutRequiresKnownSessionAndMatchingCSRFForReplay(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)
	svc.SetClock(func() time.Time { return clock })
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	if err := svc.Logout(ctx, "", "csrf", "req-empty"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("empty token logout error = %v, want ErrInvalidSession", err)
	}
	if err := svc.Logout(ctx, "unknown-token", "csrf", "req-unknown"); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("unknown token logout error = %v, want ErrInvalidSession", err)
	}

	active, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-login")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, active.Session.Token, "wrong", "req-wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("active wrong csrf error = %v, want ErrInvalidCSRF", err)
	}
	if _, err := svc.Authenticate(ctx, active.Session.Token); err != nil {
		t.Fatalf("wrong csrf revoked active session: %v", err)
	}
	if err := svc.Logout(ctx, active.Session.Token, active.Session.CSRFToken, "req-success"); err != nil {
		t.Fatalf("first logout: %v", err)
	}
	if err := svc.Logout(ctx, active.Session.Token, "wrong", "req-revoked-wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("revoked wrong csrf error = %v, want ErrInvalidCSRF", err)
	}
	if err := svc.Logout(ctx, active.Session.Token, active.Session.CSRFToken, "req-revoked-match"); err != nil {
		t.Fatalf("revoked matching csrf should succeed: %v", err)
	}

	expired, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-expiring")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour)
	if err := svc.Logout(ctx, expired.Session.Token, "wrong", "req-expired-wrong"); !errors.Is(err, ErrInvalidCSRF) {
		t.Fatalf("expired wrong csrf error = %v, want ErrInvalidCSRF", err)
	}
	if err := svc.Logout(ctx, expired.Session.Token, expired.Session.CSRFToken, "req-expired-match"); err != nil {
		t.Fatalf("expired matching csrf should succeed: %v", err)
	}
}

func TestAuditSecurityOutcomesDoNotPersistSecrets(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store, "test", time.Hour, false)

	if err := svc.AuditSecurityOutcome(ctx, "req-unauth", "auth.session", "unauthenticated", "session-token-secret"); err != nil {
		t.Fatal(err)
	}
	if err := svc.AuditSecurityOutcome(ctx, "req-csrf", "auth.csrf", "missing", "csrf-token-secret"); err != nil {
		t.Fatal(err)
	}
	events := store.AuditEvents()
	if len(events) != 2 {
		t.Fatalf("audit events = %d", len(events))
	}
	for _, event := range events {
		if event.Email != "" || strings.Contains(event.Action+event.Outcome+event.RequestID, "secret") {
			t.Fatalf("audit leaked secret material: %+v", event)
		}
	}
}

type userLookupErrorStore struct {
	*MemoryStore
	err error
}

func (s *userLookupErrorStore) UserByEmail(context.Context, string) (*User, error) {
	return nil, s.err
}

type loginTxErrorStore struct {
	*MemoryStore
	err error
}

func (s *loginTxErrorStore) RecordLoginSuccess(context.Context, string, StoredSession, time.Time, AuditEvent) (*User, error) {
	return nil, s.err
}
