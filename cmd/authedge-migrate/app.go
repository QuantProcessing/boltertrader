// Command authedge-migrate brings an auth edge database up to the schema this
// binary embeds, then reports what it applied.
//
// It is a separate binary from the service on purpose. Migrating is a
// privileged act performed once by an operator; serving is an unprivileged act
// performed continuously by a process that should never hold the rights to
// alter its own tables. Splitting them is what lets those two connections use
// two different roles — which matters more here than anywhere else, since the
// tables this touches hold the password hashes and live sessions of everyone
// who can reach the platform.
//
// Errors are opaque codes rather than sentences. The failure modes here are
// about database identity and privilege, and a message that quoted the
// connection back would put deployment topology into whatever collects stderr.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"

	"github.com/QuantProcessing/boltertrader/services/authedge/store/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// result is what an operator (or the compose bring-up) reads to confirm the
// migration landed. The digest is the ledger of applied versions and their
// checksums, so two deployments claiming the same schemaVersion can still be
// told apart if one of them applied a different file under that number.
type result struct {
	Database      string `json:"database"`
	SchemaVersion int    `json:"schemaVersion"`
	LedgerDigest  string `json:"ledgerDigest"`
}

// migrate is replaceable so the flag and identity handling can be tested
// without a database.
var migrate = migrateDatabase

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("authedge-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	database := flags.String("database", "", "non-secret auth edge database name")
	role := flags.String("role", "", "expected PostgreSQL connection role")
	host := flags.String("host", "127.0.0.1:15432", "auth edge database host:port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeIdentity(*database) || !safeIdentity(*role) || !safeIdentity(*host) {
		return fmt.Errorf("AUTHEDGE_MIGRATION_IDENTITY_INVALID")
	}
	applied, err := migrate(context.Background(), *host, *database, *role)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(applied)
}

func migrateDatabase(ctx context.Context, host, database, role string) (result, error) {
	// No password is assembled here. The connection authenticates by peer or by
	// whatever ~/.pgpass and the environment already provide, which keeps this
	// binary from ever holding a credential it could log.
	connectionURL := &url.URL{Scheme: "postgres", User: url.User(role), Host: host, Path: database, RawQuery: "sslmode=disable"}
	handle, err := sql.Open("pgx", connectionURL.String())
	if err != nil {
		return result{}, fmt.Errorf("AUTHEDGE_MIGRATION_UNAVAILABLE")
	}
	defer handle.Close()

	// The connection is asked who it actually is rather than trusted to be who
	// the flags said. A migrator pointed at the live database by a stale
	// environment would otherwise apply the schema there and report success —
	// and these tables decide who may sign in.
	var observedDatabase, observedRole string
	if err := handle.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&observedDatabase, &observedRole); err != nil {
		return result{}, fmt.Errorf("AUTHEDGE_CONNECTED_IDENTITY_UNAVAILABLE")
	}
	if observedDatabase != database || observedRole != role {
		return result{}, fmt.Errorf("AUTHEDGE_CONNECTED_IDENTITY_MISMATCH")
	}
	if err := postgres.Migrate(ctx, handle); err != nil {
		return result{}, fmt.Errorf("AUTHEDGE_MIGRATION_BLOCKED")
	}
	version, digest, err := postgres.LedgerDigest(ctx, handle)
	if err != nil {
		return result{}, fmt.Errorf("AUTHEDGE_SCHEMA_PREFLIGHT_BLOCKED")
	}
	return result{Database: database, SchemaVersion: version, LedgerDigest: digest}, nil
}

// safeIdentity keeps flag values out of connection-string interpretation. They
// are interpolated into a URL, so a value carrying a space, a quote or an equals
// sign could change what the connection means.
func safeIdentity(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character <= ' ' || character > '~' || character == '=' || character == '\'' || character == '"' || character == '\\' {
			return false
		}
	}
	return true
}
