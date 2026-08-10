package postgres

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreAuthServiceIntegration(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	applyProductModeMigrationsForTest(t, ctx, pool)
	store := NewStore(pool, StoreOptions{BootID: "auth-test-boot", Mode: "test"})
	broadcaster := &recordingAuditBroadcaster{}
	store.SetEventBroadcaster(broadcaster)
	svc := auth.NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	user, err := store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash == "first secure password" {
		t.Fatal("plaintext password stored")
	}
	hash := user.PasswordHash
	if err := svc.Bootstrap(ctx, "owner@example.com", "second secure password"); err != nil {
		t.Fatal(err)
	}
	user, err = store.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if user.PasswordHash != hash {
		t.Fatal("bootstrap overwrote password hash")
	}
	login, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-login")
	if err != nil {
		t.Fatal(err)
	}
	var sessionID, tokenDigest, csrfDigest, sessionMode string
	if err := pool.QueryRow(ctx, "select id, token_digest, csrf_digest, mode from sessions where user_id=$1", auth.DefaultPrincipalID).Scan(&sessionID, &tokenDigest, &csrfDigest, &sessionMode); err != nil {
		t.Fatal(err)
	}
	if sessionMode != "test" {
		t.Fatalf("session mode=%q, want test", sessionMode)
	}
	if tokenDigest == login.Session.Token || csrfDigest == login.Session.CSRFToken {
		t.Fatal("raw token material stored")
	}
	if strings.Contains(login.Session.Token, sessionID) || strings.Contains(sessionID, login.Session.Token[:12]) {
		t.Fatalf("session id contains raw bearer token material: id=%q token=%q", sessionID, login.Session.Token)
	}
	if _, err := svc.Authenticate(ctx, login.Session.Token); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if err := svc.Logout(ctx, login.Session.Token, login.Session.CSRFToken, "req-logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if err := svc.Logout(ctx, login.Session.Token, login.Session.CSRFToken, "req-repeat"); err != nil {
		t.Fatalf("repeat logout: %v", err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "select count(*) from audit_events").Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 3 {
		t.Fatalf("audit events = %d", auditCount)
	}
	var principalID, mode string
	if err := pool.QueryRow(ctx, `
		select principal_id, mode
		from audit_events
		where action='auth.login' and outcome='success'
		order by id desc
		limit 1
	`).Scan(&principalID, &mode); err != nil {
		t.Fatal(err)
	}
	if principalID != auth.DefaultPrincipalID || mode != "test" {
		t.Fatalf("audit scope principal=%q mode=%q", principalID, mode)
	}
	var replayKind string
	var replayPrincipalID *string
	var replayMode, contractVersion string
	if err := pool.QueryRow(ctx, `
		select kind, principal_id, mode, contract_version
		from read_model_events
		where kind='audit.append'
		order by id desc
		limit 1
	`).Scan(&replayKind, &replayPrincipalID, &replayMode, &contractVersion); err != nil {
		t.Fatal(err)
	}
	if replayKind != "audit.append" || replayPrincipalID == nil ||
		*replayPrincipalID != auth.DefaultPrincipalID || replayMode != "test" ||
		contractVersion != readmodel.ContractVersion {
		t.Fatalf("audit replay kind=%q principal=%v mode=%q contractVersion=%q",
			replayKind, replayPrincipalID, replayMode, contractVersion)
	}
	if len(broadcaster.events) < 3 {
		t.Fatalf("live audit broadcasts=%d", len(broadcaster.events))
	}
	for _, event := range broadcaster.events {
		if event.Kind != string(readmodel.AuditAppend) || event.Scope != "all" || event.Sequence == 0 {
			t.Fatalf("invalid audit broadcast=%+v", event)
		}
	}
	readStore, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x5d}, 32))
	if err != nil {
		t.Fatal(err)
	}
	testAudit, err := readStore.ListAuditEvents(ctx,
		readmodel.Scope{PrincipalID: auth.DefaultPrincipalID, Mode: "test"},
		readmodel.AuditQuery{PageQuery: readmodel.PageQuery{Limit: 20}},
	)
	if err != nil {
		t.Fatal(err)
	}
	liveAudit, err := readStore.ListAuditEvents(ctx,
		readmodel.Scope{PrincipalID: auth.DefaultPrincipalID, Mode: "live"},
		readmodel.AuditQuery{PageQuery: readmodel.PageQuery{Limit: 20}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(testAudit.Items) < 3 || len(liveAudit.Items) != 0 {
		t.Fatalf("test audit=%d live audit=%d", len(testAudit.Items), len(liveAudit.Items))
	}

	activeTest, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-before-live")
	if err != nil {
		t.Fatalf("login before mode switch: %v", err)
	}
	liveStore := NewStore(pool, StoreOptions{BootID: "auth-test-live-boot", Mode: "live"})
	liveSvc := auth.NewService(liveStore, "live", time.Hour, true)
	if err := liveSvc.Bootstrap(ctx, "owner@example.com", "replacement password must not win"); err != nil {
		t.Fatalf("bootstrap live: %v", err)
	}
	if _, err := liveSvc.Authenticate(ctx, activeTest.Session.Token); !errors.Is(err, auth.ErrInvalidSession) {
		t.Fatalf("old test session error=%v, want ErrInvalidSession", err)
	}
	if err := liveSvc.RequireCSRF(ctx, activeTest.Session.Token, activeTest.Session.CSRFToken); !errors.Is(err, auth.ErrInvalidSession) {
		t.Fatalf("old test csrf error=%v, want ErrInvalidSession", err)
	}
	user, err = liveStore.UserByEmail(ctx, "owner@example.com")
	if err != nil {
		t.Fatalf("load switched user: %v", err)
	}
	if user.PasswordHash != hash {
		t.Fatal("mode switch changed password hash")
	}
	if _, err := liveSvc.Login(ctx, "owner@example.com", "replacement password must not win", "req-replacement"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("replacement bootstrap password error=%v, want ErrInvalidCredentials", err)
	}
	liveLogin, err := liveSvc.Login(ctx, "owner@example.com", "first secure password", "req-live")
	if err != nil {
		t.Fatalf("login after mode switch: %v", err)
	}
	if liveLogin.Principal.Mode != "live" {
		t.Fatalf("live principal mode=%q", liveLogin.Principal.Mode)
	}
}

type recordingAuditBroadcaster struct {
	events []uistream.StoredEvent
}

func (broadcaster *recordingAuditBroadcaster) BroadcastStored(event uistream.StoredEvent) error {
	broadcaster.events = append(broadcaster.events, event)
	return nil
}

func TestStoreConcurrentLoginLeavesOneActiveSession(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	applyProductModeMigrationsForTest(t, ctx, pool)
	store := NewStore(pool)
	svc := auth.NewService(store, "test", time.Hour, false)
	if err := svc.Bootstrap(ctx, "owner@example.com", "first secure password"); err != nil {
		t.Fatal(err)
	}
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Login(ctx, "owner@example.com", "first secure password", "req-concurrent")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var active int
	if err := pool.QueryRow(ctx, "select count(*) from sessions where revoked_at is null and expires_at > now()").Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active sessions = %d, want 1", active)
	}
}
