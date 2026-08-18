package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

type SessionStore struct{ database *sql.DB }

func NewSessionStore(database *sql.DB) *SessionStore { return &SessionStore{database: database} }

func (store *SessionStore) Save(ctx context.Context, stored session.StoredSession) error {
	_, err := store.database.ExecContext(ctx, `
		INSERT INTO sessions (token_digest, principal_id, username, csrf_digest, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (token_digest) DO UPDATE SET
			principal_id = EXCLUDED.principal_id,
			username = EXCLUDED.username, csrf_digest = EXCLUDED.csrf_digest,
			expires_at = EXCLUDED.expires_at`,
		stored.TokenDigest, stored.Principal.ID, stored.Principal.Username, stored.CSRFDigest, stored.ExpiresAt)
	return err
}

func (store *SessionStore) Get(ctx context.Context, tokenDigest string) (session.StoredSession, error) {
	var stored session.StoredSession
	err := store.database.QueryRowContext(ctx, `
		SELECT token_digest, principal_id, username, csrf_digest, expires_at
		FROM sessions WHERE token_digest = $1`, tokenDigest).Scan(
		&stored.TokenDigest, &stored.Principal.ID,
		&stored.Principal.Username, &stored.CSRFDigest, &stored.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return session.StoredSession{}, session.ErrNotFound
	}
	return stored, err
}

func (store *SessionStore) Delete(ctx context.Context, tokenDigest string) error {
	_, err := store.database.ExecContext(ctx, `DELETE FROM sessions WHERE token_digest = $1`, tokenDigest)
	return err
}

type IdentityStore struct{ database *sql.DB }

func NewIdentityStore(database *sql.DB) *IdentityStore { return &IdentityStore{database: database} }

func (store *IdentityStore) Upsert(ctx context.Context, identity session.Identity) error {
	_, err := store.database.ExecContext(ctx, `
		INSERT INTO identities (principal_id, username, password_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (username) DO UPDATE SET
			principal_id = EXCLUDED.principal_id, password_hash = EXCLUDED.password_hash,
			disabled_at = NULL`, identity.PrincipalID, identity.Username, identity.PasswordHash)
	return err
}

// FindByUsername ignores disabled identities rather than reporting them as
// disabled: the caller turns any lookup failure into the same generic login
// rejection, and distinguishing them here would only create a way to probe
// which usernames exist.
func (store *IdentityStore) FindByUsername(ctx context.Context, username string) (session.Identity, error) {
	var identity session.Identity
	err := store.database.QueryRowContext(ctx, `
		SELECT principal_id, username, password_hash
		FROM identities WHERE username = $1 AND disabled_at IS NULL`, username).Scan(
		&identity.PrincipalID, &identity.Username, &identity.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return session.Identity{}, session.ErrNotFound
	}
	return identity, err
}
