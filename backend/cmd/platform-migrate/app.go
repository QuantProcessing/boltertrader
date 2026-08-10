package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	platformpostgres "github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	"github.com/QuantProcessing/boltertrader/migration/authority"
	_ "github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

const (
	requiredPlatformSchemaVersion = 15
	bootstrapHashFileEnv          = "BT_BOOTSTRAP_PASSWORD_HASH_FILE"
)

var migratePlatform = migrateProductionPlatform

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("platform-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	service := flags.String("service", "", "non-secret libpq Platform service")
	database := flags.String("database", "", "non-secret Platform database")
	role := flags.String("role", "", "expected Platform PostgreSQL connection role")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeIdentity(*service) || !safeIdentity(*database) || !safeIdentity(*role) {
		return fmt.Errorf("--service, --database, and --role are required")
	}
	if *service != "platform_target_test" || *database != "bolter_platform_test" || *role != "bolter_platform_test_migrator" {
		return fmt.Errorf("CROSS_MODE_DATABASE_IDENTITY")
	}
	version, err := migratePlatform(context.Background(), *service, *database, *role)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		SchemaVersion int `json:"schemaVersion"`
	}{SchemaVersion: version})
}

func migrateProductionPlatform(ctx context.Context, service, database, role string) (int, error) {
	connection, err := sql.Open("pgx", "service="+service+" dbname="+database)
	if err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_UNAVAILABLE")
	}
	defer connection.Close()
	if err := connection.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_UNAVAILABLE")
	}
	var observedDatabase, observedRole string
	if err := connection.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&observedDatabase, &observedRole); err != nil {
		return 0, fmt.Errorf("PLATFORM_CONNECTED_IDENTITY_UNAVAILABLE")
	}
	if observedDatabase != database || observedRole != role {
		return 0, fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	if err := platformpostgres.Migrate(ctx, connection); err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_BLOCKED")
	}
	if err := platformpostgres.ApplyAuthority(ctx, connection, authority.PlatformV15()); err != nil {
		return 0, fmt.Errorf("PLATFORM_AUTHORITY_BLOCKED")
	}
	if err := seedBootstrapIdentity(ctx, connection); err != nil {
		return 0, err
	}
	var version int
	var ready bool
	if err := connection.QueryRowContext(ctx, `SELECT COALESCE(max(version),0), to_regclass('platform_account_routes') IS NOT NULL AND (SELECT count(*)=2 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='platform_accounts' AND column_name IN ('mode','native_environment')) FROM platform_schema_migrations`).Scan(&version, &ready); err != nil || version != requiredPlatformSchemaVersion || !ready {
		return 0, fmt.Errorf("PLATFORM_SCHEMA_PREFLIGHT_BLOCKED")
	}
	return version, nil
}

func seedBootstrapIdentity(ctx context.Context, connection *sql.DB) error {
	username := strings.TrimSpace(os.Getenv("BT_BOOTSTRAP_USERNAME"))
	if username == "" {
		return fmt.Errorf("BT_BOOTSTRAP_USERNAME_MISSING")
	}
	hash, err := readBootstrapPasswordHash(os.Getenv(bootstrapHashFileEnv))
	if err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `
		INSERT INTO platform_identities (principal_id, mode, username, password_hash)
		VALUES ('browser-session', 'test', $1, $2)
		ON CONFLICT (mode, username) DO UPDATE SET
			principal_id=EXCLUDED.principal_id, password_hash=EXCLUDED.password_hash, disabled_at=NULL`, username, hash); err != nil {
		return fmt.Errorf("PLATFORM_BOOTSTRAP_IDENTITY_BLOCKED")
	}
	return nil
}

func readBootstrapPasswordHash(path string) (string, error) {
	if path == "" || filepath.Base(filepath.Dir(filepath.Clean(path))) != "test" {
		return "", fmt.Errorf("BOOTSTRAP_PASSWORD_HASH_FILE_SCOPE_INVALID")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("BOOTSTRAP_PASSWORD_HASH_FILE_UNSAFE")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("BOOTSTRAP_PASSWORD_HASH_FILE_UNREADABLE")
	}
	hash := strings.TrimSpace(string(payload))
	if _, err := bcrypt.Cost([]byte(hash)); err != nil {
		return "", fmt.Errorf("BOOTSTRAP_PASSWORD_HASH_INVALID")
	}
	return hash, nil
}

func safeIdentity(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 64 {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n\x00='\"")
}

func containsIdentityToken(value, want string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(char rune) bool {
		return char == '_' || char == '-' || char == '.'
	}) {
		if token == want {
			return true
		}
	}
	return false
}
