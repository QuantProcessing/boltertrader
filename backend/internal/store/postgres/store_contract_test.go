package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
)

func TestStoresImplementPlatformContracts(t *testing.T) {
	var _ session.Store = (*SessionStore)(nil)
	var _ session.IdentityStore = (*IdentityStore)(nil)
	var _ accounts.Store = (*AccountStore)(nil)
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
