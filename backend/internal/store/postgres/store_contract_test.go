package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

func TestStoresImplementPlatformContracts(t *testing.T) {
	var _ session.Store = (*SessionStore)(nil)
	var _ session.IdentityStore = (*IdentityStore)(nil)
	var _ audit.Store = (*AuditStore)(nil)
}

func TestIdentityAccountAuditMigrationIsFencedAndSecretSafe(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "0002_identity_accounts_audit.sql")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(payload)
	for _, required := range []string{"platform_identities", "platform_sessions", "platform_accounts", "platform_audit", "desired_state_revision", "runtime_generation", "credential_version", "secret_ref", "CHECK"} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"secret_material", "api_key", "private_key", "passphrase", "raw_secret"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("migration contains forbidden secret column %q", forbidden)
		}
	}
}

func TestAccountRouteCredentialMigrationHasAtomicRecoverySchema(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "0014_account_routes_credentials.sql")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(payload))
	for _, required := range []string{"platform_account_routes", "platform_credential_operations", "idempotency_key_digest", "request_digest", "cleanup_pending", "platform_credential_operations_recovery", "previous_secret_ref", "alter column secret_ref drop not null"} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"credential_bundle", "secret_material", "api_secret", "private_key", "passphrase"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains secret-bearing column %q", forbidden)
		}
	}
}

func TestRotationCleanupProofMigrationStoresOnlyDurableTerminalEvidence(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "0015_rotation_cleanup_proof.sql")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(payload))
	for _, required := range []string{
		"alter table platform_credential_operations",
		"rotation_cleanup_tombstone_revision",
		"rotation_cleanup_completed_at",
		"platform_credential_operations_one_recoverable_per_account",
		"where state in ('awaiting_material','stored','applying','cleanup_pending')",
		"bigint",
		"timestamptz",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"secret_ref", "credential_bundle", "secret_material", "api_secret", "private_key", "passphrase", "namespace", "provider", "key_version"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains forbidden credential detail %q", forbidden)
		}
	}
}
