package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

func TestSessionSurvivesLosingEveryConnection(t *testing.T) {
	database, reopen := openIntegrationDatabase(t)
	ctx := context.Background()
	identities := NewIdentityStore(database)
	if err := identities.Upsert(ctx, session.Identity{PrincipalID: "principal-a", Username: "operator", PasswordHash: "hash-a"}); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
	stored := session.StoredSession{
		TokenDigest: "token-digest-a",
		Principal:   session.Principal{ID: "principal-a", Username: "operator"},
		CSRFDigest:  "csrf-digest-a",
		ExpiresAt:   expires,
	}
	if err := NewSessionStore(database).Save(ctx, stored); err != nil {
		t.Fatal(err)
	}

	// A session is only worth anything if it outlives the process that minted
	// it, so the read goes through a connection the write never touched.
	got, err := NewSessionStore(reopen()).Get(ctx, "token-digest-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Principal != stored.Principal || got.CSRFDigest != stored.CSRFDigest {
		t.Fatalf("got=%+v want=%+v", got, stored)
	}
	if !got.ExpiresAt.Equal(expires) {
		t.Fatalf("expires=%s want=%s", got.ExpiresAt, expires)
	}
}

func TestSessionDeletionIsFinal(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	if err := NewIdentityStore(database).Upsert(ctx, session.Identity{PrincipalID: "principal-a", Username: "operator", PasswordHash: "hash-a"}); err != nil {
		t.Fatal(err)
	}
	sessions := NewSessionStore(database)
	if err := sessions.Save(ctx, session.StoredSession{TokenDigest: "token-digest-a", Principal: session.Principal{ID: "principal-a", Username: "operator"}, CSRFDigest: "csrf-digest-a", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Delete(ctx, "token-digest-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.Get(ctx, "token-digest-a"); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("err=%v want=%v", err, session.ErrNotFound)
	}
}

// Login rejects a disabled identity the same way it rejects one that never
// existed. Anything else would let an unauthenticated caller learn which
// usernames are real.
func TestDisabledIdentityIsIndistinguishableFromAbsent(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	identities := NewIdentityStore(database)
	if err := identities.Upsert(ctx, session.Identity{PrincipalID: "principal-a", Username: "operator", PasswordHash: "hash-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE identities SET disabled_at = now() WHERE username = 'operator'`); err != nil {
		t.Fatal(err)
	}
	_, disabledErr := identities.FindByUsername(ctx, "operator")
	_, absentErr := identities.FindByUsername(ctx, "nobody")
	if !errors.Is(disabledErr, session.ErrNotFound) || !errors.Is(absentErr, session.ErrNotFound) {
		t.Fatalf("disabled=%v absent=%v", disabledErr, absentErr)
	}
}

// The username is the whole identity key now that mode is gone, so a second
// upsert of the same username is a rotation rather than a second row. The auth
// edge seeds the bootstrap operator on every start and depends on exactly that:
// a rotated hash has to displace the stored one, or the old password keeps
// working. Re-enabling is part of the same statement for the same reason — an
// operator who re-seeds an identity has said it should work.
func TestIdentityUpsertRotatesRatherThanDuplicates(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	identities := NewIdentityStore(database)
	for _, identity := range []session.Identity{
		{PrincipalID: "principal-old", Username: "operator", PasswordHash: "hash-old"},
		{PrincipalID: "principal-new", Username: "operator", PasswordHash: "hash-new"},
	} {
		if err := identities.Upsert(ctx, identity); err != nil {
			t.Fatal(err)
		}
	}
	var rows int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM identities WHERE username = 'operator'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("rows=%d want=1", rows)
	}
	found, err := identities.FindByUsername(ctx, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if found.PrincipalID != "principal-new" || found.PasswordHash != "hash-new" {
		t.Fatalf("found=%+v", found)
	}

	if _, err := database.ExecContext(ctx, `UPDATE identities SET disabled_at = now() WHERE username = 'operator'`); err != nil {
		t.Fatal(err)
	}
	if err := identities.Upsert(ctx, session.Identity{PrincipalID: "principal-new", Username: "operator", PasswordHash: "hash-new"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identities.FindByUsername(ctx, "operator"); err != nil {
		t.Fatalf("re-seeded identity: %v", err)
	}
}

func TestReapplyingMigrationsIsANoop(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	if err := Migrate(ctx, database); err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if applied != len(migrations) {
		t.Fatalf("applied=%d want=%d", applied, len(migrations))
	}
}

func TestMigrationRefusesATamperedLedger(t *testing.T) {
	database, _ := openIntegrationDatabase(t)
	ctx := context.Background()
	if _, err := database.ExecContext(ctx, `UPDATE schema_migrations SET checksum = 'tampered' WHERE version = 1`); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, database); !errors.Is(err, ErrMigrationMismatch) {
		t.Fatalf("err=%v want=%v", err, ErrMigrationMismatch)
	}
}
