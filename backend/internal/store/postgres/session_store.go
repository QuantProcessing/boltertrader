package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

type SessionStore struct{ db *sql.DB }

func NewSessionStore(db *sql.DB) *SessionStore { return &SessionStore{db: db} }

func (store *SessionStore) Save(ctx context.Context, stored session.StoredSession) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO platform_sessions (token_digest, principal_id, mode, username, csrf_digest, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (token_digest) DO UPDATE SET
			principal_id = EXCLUDED.principal_id, mode = EXCLUDED.mode,
			username = EXCLUDED.username, csrf_digest = EXCLUDED.csrf_digest,
			expires_at = EXCLUDED.expires_at`,
		stored.TokenDigest, stored.Principal.ID, stored.Principal.Mode, stored.Principal.Username, stored.CSRFDigest, stored.ExpiresAt)
	return err
}

func (store *SessionStore) Get(ctx context.Context, tokenDigest string) (session.StoredSession, error) {
	var stored session.StoredSession
	err := store.db.QueryRowContext(ctx, `
		SELECT token_digest, principal_id, mode, username, csrf_digest, expires_at
		FROM platform_sessions WHERE token_digest = $1`, tokenDigest).Scan(
		&stored.TokenDigest, &stored.Principal.ID, &stored.Principal.Mode,
		&stored.Principal.Username, &stored.CSRFDigest, &stored.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return session.StoredSession{}, session.ErrNotFound
	}
	return stored, err
}

func (store *SessionStore) Delete(ctx context.Context, tokenDigest string) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM platform_sessions WHERE token_digest = $1`, tokenDigest)
	return err
}

type IdentityStore struct{ db *sql.DB }

func NewIdentityStore(db *sql.DB) *IdentityStore { return &IdentityStore{db: db} }

func (store *IdentityStore) Upsert(ctx context.Context, identity session.Identity) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO platform_identities (principal_id, mode, username, password_hash)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (mode, username) DO UPDATE SET
			principal_id = EXCLUDED.principal_id, password_hash = EXCLUDED.password_hash,
			disabled_at = NULL`, identity.PrincipalID, identity.Mode, identity.Username, identity.PasswordHash)
	return err
}

func (store *IdentityStore) FindByUsername(ctx context.Context, mode, username string) (session.Identity, error) {
	var identity session.Identity
	err := store.db.QueryRowContext(ctx, `
		SELECT principal_id, username, password_hash, mode
		FROM platform_identities WHERE mode = $1 AND username = $2 AND disabled_at IS NULL`, mode, username).Scan(
		&identity.PrincipalID, &identity.Username, &identity.PasswordHash, &identity.Mode)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Identity{}, session.ErrNotFound
	}
	return identity, err
}
