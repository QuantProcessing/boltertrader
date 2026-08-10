package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool   *pgxpool.Pool
	bootID string
	mode   string

	broadcasterMu sync.RWMutex
	broadcaster   eventBroadcaster
}

type StoreOptions struct {
	BootID string
	Mode   string
}

type eventBroadcaster interface {
	BroadcastStored(uistream.StoredEvent) error
}

func NewStore(pool *pgxpool.Pool, options ...StoreOptions) *Store {
	store := &Store{pool: pool}
	if len(options) > 0 {
		store.bootID = strings.TrimSpace(options[0].BootID)
		store.mode = strings.TrimSpace(options[0].Mode)
	}
	return store
}

func (s *Store) SetEventBroadcaster(broadcaster eventBroadcaster) {
	s.broadcasterMu.Lock()
	defer s.broadcasterMu.Unlock()
	s.broadcaster = broadcaster
}

func (s *Store) UserByEmail(ctx context.Context, email string) (*auth.User, error) {
	var user auth.User
	var lastLogin *time.Time
	err := s.pool.QueryRow(ctx, `select id, email, password_hash, last_login_at, created_at from users where email=$1`, email).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &lastLogin, &user.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrInvalidCredentials
		}
		return nil, err
	}
	user.LastLoginAt = lastLogin
	return &user, nil
}

func (s *Store) UserByID(ctx context.Context, id string) (*auth.User, error) {
	var user auth.User
	var lastLogin *time.Time
	err := s.pool.QueryRow(ctx, `select id, email, password_hash, last_login_at, created_at from users where id=$1`, id).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &lastLogin, &user.CreatedAt)
	if err != nil {
		return nil, err
	}
	user.LastLoginAt = lastLogin
	return &user, nil
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var count int
	err := s.pool.QueryRow(ctx, `select count(*) from users`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(ctx context.Context, user auth.User) error {
	_, err := s.pool.Exec(ctx, `insert into users(id, email, password_hash, created_at) values($1,$2,$3,$4)`, user.ID, user.Email, user.PasswordHash, user.CreatedAt)
	return err
}

func (s *Store) EnsureBootstrapUser(ctx context.Context, user auth.User) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtext('funding-trader.bootstrap-user'))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		insert into users(id, email, password_hash, created_at)
		select $1, $2, $3, $4
		where not exists (select 1 from users)
		on conflict do nothing
	`, user.ID, user.Email, user.PasswordHash, user.CreatedAt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) UpdateLastLogin(ctx context.Context, userID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `update users set last_login_at=$2 where id=$1`, userID, at)
	return err
}

func (s *Store) RevokeSessionsForUser(ctx context.Context, userID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `update sessions set revoked_at=$2 where user_id=$1 and revoked_at is null`, userID, at)
	return err
}

func (s *Store) CreateSession(ctx context.Context, session auth.StoredSession) error {
	if !validProductMode(session.Mode) {
		return errors.New("session mode must be test or live")
	}
	_, err := s.pool.Exec(ctx, `insert into sessions(id, user_id, token_digest, csrf_digest, mode, expires_at, revoked_at, created_at, last_authenticated_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		session.ID, session.UserID, session.TokenDigest, session.CSRFDigest, session.Mode, session.ExpiresAt, session.RevokedAt, session.CreatedAt, session.LastAuthenticated)
	return err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, userID string, session auth.StoredSession, lastLogin time.Time, audit auth.AuditEvent) (*auth.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var user auth.User
	var lastLoginAt *time.Time
	if err := tx.QueryRow(ctx, `select id, email, password_hash, last_login_at, created_at from users where id=$1 for update`, userID).
		Scan(&user.ID, &user.Email, &user.PasswordHash, &lastLoginAt, &user.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, auth.ErrInvalidCredentials
		}
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update sessions set revoked_at=$2 where user_id=$1 and revoked_at is null`, userID, lastLogin); err != nil {
		return nil, err
	}
	if !validProductMode(session.Mode) {
		return nil, errors.New("session mode must be test or live")
	}
	if _, err := tx.Exec(ctx, `insert into sessions(id, user_id, token_digest, csrf_digest, mode, expires_at, revoked_at, created_at, last_authenticated_at) values($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		session.ID, session.UserID, session.TokenDigest, session.CSRFDigest, session.Mode, session.ExpiresAt, session.RevokedAt, session.CreatedAt, session.LastAuthenticated); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `update users set last_login_at=$2 where id=$1`, userID, lastLogin); err != nil {
		return nil, err
	}
	auditEvent, err := s.insertAudit(ctx, tx, audit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.broadcast(auditEvent)
	value := lastLogin
	user.LastLoginAt = &value
	return &user, nil
}

func (s *Store) SessionByTokenDigest(ctx context.Context, digest string) (*auth.StoredSession, error) {
	var session auth.StoredSession
	err := s.pool.QueryRow(ctx, `select id, user_id, token_digest, csrf_digest, mode, expires_at, revoked_at, created_at, last_authenticated_at from sessions where token_digest=$1`, digest).
		Scan(&session.ID, &session.UserID, &session.TokenDigest, &session.CSRFDigest, &session.Mode, &session.ExpiresAt, &session.RevokedAt, &session.CreatedAt, &session.LastAuthenticated)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, auth.ErrInvalidSession
		}
		return nil, err
	}
	return &session, nil
}

func (s *Store) RevokeSession(ctx context.Context, digest string, at time.Time) error {
	tag, err := s.pool.Exec(ctx, `update sessions set revoked_at=$2 where token_digest=$1 and revoked_at is null`, digest, at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrInvalidSession
	}
	return nil
}

func (s *Store) AppendAudit(ctx context.Context, event auth.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	auditEvent, err := s.insertAudit(ctx, tx, event)
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.broadcast(auditEvent)
	return nil
}

func (s *Store) insertAudit(ctx context.Context, tx pgx.Tx, event auth.AuditEvent) (readmodel.Event, error) {
	mode := strings.TrimSpace(event.Mode)
	if mode == "" {
		mode = s.mode
	}
	if !validProductMode(mode) {
		return readmodel.Event{}, errors.New("audit mode must be test or live")
	}
	occurredAt := event.CreatedAt.UTC()
	var id int64
	err := tx.QueryRow(ctx, `
		insert into audit_events(
			request_id, principal_id, mode, action, outcome, email,
			target_id, execution_id, payload, created_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$8,'{}'::jsonb,$9)
		returning id
	`,
		event.RequestID,
		nullableText(event.PrincipalID),
		mode,
		event.Action,
		event.Outcome,
		nullableText(event.Email),
		nullableText(event.TargetID),
		nullableText(event.ExecutionID),
		occurredAt,
	).Scan(&id)
	if err != nil {
		return readmodel.Event{}, err
	}
	if s.bootID == "" {
		return readmodel.Event{}, nil
	}
	payload, err := json.Marshal(map[string]any{
		"auditId":     strconv.FormatInt(id, 10),
		"time":        occurredAt.Format(time.RFC3339Nano),
		"principalId": nullableText(event.PrincipalID),
		"kind":        event.Action,
		"targetId":    nullableText(event.TargetID),
		"outcome":     event.Outcome,
		"requestId":   event.RequestID,
		"executionId": nullableText(event.ExecutionID),
	})
	if err != nil {
		return readmodel.Event{}, err
	}
	var eventID int64
	err = tx.QueryRow(ctx, `
		insert into read_model_events(
			boot_id, principal_id, mode, contract_version, kind, scope,
			entity_id, payload, occurred_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9)
		returning id
	`,
		s.bootID,
		nullableText(event.PrincipalID),
		mode,
		readmodel.ContractVersion,
		string(readmodel.AuditAppend),
		[]string{"audit"},
		strconv.FormatInt(id, 10),
		payload,
		occurredAt,
	).Scan(&eventID)
	if err != nil {
		return readmodel.Event{}, err
	}
	return readmodel.Event{
		ID: eventID, BootID: s.bootID, PrincipalID: event.PrincipalID,
		Mode: mode, ContractVersion: readmodel.ContractVersion,
		Kind: readmodel.AuditAppend, Scope: []string{"audit"},
		EntityID: strconv.FormatInt(id, 10), Payload: payload, OccurredAt: occurredAt,
	}, nil
}

func validProductMode(mode string) bool {
	return mode == "test" || mode == "live"
}

func nullableText(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *Store) broadcast(event readmodel.Event) {
	if event.ID == 0 {
		return
	}
	s.broadcasterMu.RLock()
	broadcaster := s.broadcaster
	s.broadcasterMu.RUnlock()
	if broadcaster == nil {
		return
	}
	_ = broadcaster.BroadcastStored(uistream.StoredEvent{
		Sequence: uint64(event.ID), Scope: "all",
		ContractVersion: event.ContractVersion, Kind: string(event.Kind),
		Data: event.Payload, OccurredAt: event.OccurredAt,
	})
}
