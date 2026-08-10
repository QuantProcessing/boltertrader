package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDatabaseBootstrapFreezesSeparatedTestAuthority(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	for _, required := range []string{
		"bolter_platform_test_admin LOGIN NOSUPERUSER CREATEDB CREATEROLE NOINHERIT",
		"bolter_funding_test_migrator",
		"bolter_platform_test_migrator",
		"bolter_gateway_test_migrator",
		"CREATE DATABASE bolter_funding_test OWNER bolter_funding_test_migrator",
		"CREATE DATABASE bolter_platform_test OWNER bolter_platform_test_migrator",
		"CREATE DATABASE bolter_gateway_test OWNER bolter_gateway_test_migrator",
		"CREATE DATABASE bolter_maintenance_test OWNER bolter_platform_test_admin",
		"ALTER DATABASE bolter_platform_test OWNER TO bolter_platform_test_migrator",
		"ALTER DATABASE bolter_gateway_test OWNER TO bolter_gateway_test_migrator",
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"GRANT pg_signal_backend TO bolter_platform_test_admin",
		"maintenance_user=bolter_platform_test_admin",
		"current_user <> session_user OR current_user <> 'bolter_platform_test_admin'",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("bootstrap missing %q", required)
		}
	}
	for _, role := range []string{"bolter_funding_test", "bolter_platform_test", "bolter_gateway_test"} {
		if !strings.Contains(script, "'"+role+"'") {
			t.Errorf("bootstrap missing runtime %s", role)
		}
	}
	for _, writer := range []string{"bolter_funding_test_writer", "bolter_platform_test_writer", "bolter_gateway_test_writer"} {
		if !strings.Contains(script, writer) {
			t.Errorf("bootstrap missing writer %s", writer)
		}
	}
}

func TestDatabaseBootstrapKeepsLiveRolesUnprivilegedAndInherited(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	for _, role := range []string{"bolter_platform_live", "bolter_gateway_live"} {
		if !strings.Contains(script, "CREATE DATABASE "+role+" OWNER "+role) {
			t.Errorf("bootstrap changed Live ownership for %s", role)
		}
	}
	if strings.Contains(script, "GRANT bolter_platform_live_writer") || strings.Contains(script, "GRANT bolter_gateway_live_writer") {
		t.Fatal("bootstrap grants a Live writer membership")
	}
}

func TestDatabaseBootstrapMaintenanceRoleUsesOnlyDelegableAlterAttributes(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	start := strings.Index(script, `PGUSER=$maintenance_user "$PSQL"`)
	if start < 0 {
		t.Fatal("bootstrap does not open an authenticated maintenance-role block")
	}
	end := strings.Index(script[start:], `PGUSER=$cluster_admin_user "$PSQL"`)
	if end < 0 {
		t.Fatal("bootstrap does not close the authenticated maintenance-role block")
	}
	maintenanceBlock := script[start : start+end]
	protectedAlter := regexp.MustCompile(`(?i)ALTER ROLE[^;\n]*(?:SUPERUSER|REPLICATION|BYPASSRLS)`)
	if match := protectedAlter.FindString(maintenanceBlock); match != "" {
		t.Fatalf("maintenance role attempts a superuser-only role alteration: %s", match)
	}
	for _, protected := range []string{"rolsuper", "rolreplication", "rolbypassrls"} {
		if !strings.Contains(maintenanceBlock, protected) {
			t.Errorf("maintenance role does not fail closed on existing %s drift", protected)
		}
	}
}

func TestDatabaseBootstrapTemporarilyDelegatesExistingRolesBeforeMaintenanceAlter(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	maintenanceSession := strings.Index(script, `PGUSER=$maintenance_user "$PSQL"`)
	if maintenanceSession < 0 {
		t.Fatal("bootstrap does not open an authenticated maintenance-role block")
	}
	clusterNormalization := strings.Index(script[maintenanceSession:], `PGUSER=$cluster_admin_user "$PSQL"`)
	if clusterNormalization < 0 {
		t.Fatal("bootstrap does not contain ordered maintenance-role boundaries")
	}
	if !strings.Contains(script[:maintenanceSession], "GRANT %I TO bolter_platform_test_admin WITH ADMIN OPTION") {
		t.Fatal("existing roles are not delegated before maintenance-role ALTER statements")
	}
	afterReset := script[maintenanceSession+clusterNormalization:]
	normalized := strings.Join(strings.Fields(afterReset), " ")
	selfGrantCleanup := strings.Index(normalized, "GRANTED BY bolter_platform_test_admin")
	adminDowngrade := strings.Index(normalized, "WITH ADMIN FALSE")
	if selfGrantCleanup < 0 || adminDowngrade < 0 || selfGrantCleanup > adminDowngrade {
		t.Fatal("bootstrap does not clean historical self-grants before removing temporary ADMIN OPTION")
	}
	for _, cleanup := range []string{
		"GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH ADMIN FALSE",
		"REVOKE bolter_funding_test FROM bolter_platform_test_admin",
		"REVOKE bolter_platform_live, bolter_gateway_live FROM bolter_platform_test_admin",
	} {
		if !strings.Contains(normalized, cleanup) {
			t.Errorf("bootstrap does not remove temporary role authority %q", cleanup)
		}
	}
}

func TestDatabaseBootstrapNormalizesMaintenanceMembershipOptions(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(payload)), " ")
	for _, required := range []string{
		"GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH SET TRUE",
		"GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH INHERIT FALSE",
		"GRANT bolter_funding_test_migrator, bolter_platform_test_migrator, bolter_gateway_test_migrator TO bolter_platform_test_admin WITH ADMIN FALSE",
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("bootstrap does not normalize final migrator membership with %q", required)
		}
	}
}

func TestDatabaseBootstrapAuthenticatesAsMaintenanceRole(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(payload)), " ")
	for _, required := range []string{
		"maintenance_user=bolter_platform_test_admin",
		`PGUSER=$maintenance_user "$PSQL"`,
		"current_user = session_user AND current_user = 'bolter_platform_test_admin'",
		`PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d "$database"`,
	} {
		if !strings.Contains(normalized, required) {
			t.Errorf("bootstrap does not authenticate the bounded maintenance session with %q", required)
		}
	}
}

func TestDatabaseBootstrapRunsDatabaseACLsThroughExactOwners(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(payload)), " ")
	for database, owner := range map[string]string{
		"bolter_funding_test":  "bolter_funding_test_migrator",
		"bolter_platform_test": "bolter_platform_test_migrator",
		"bolter_gateway_test":  "bolter_gateway_test_migrator",
		"bolter_platform_live": "bolter_platform_live",
		"bolter_gateway_live":  "bolter_gateway_live",
	} {
		pattern := regexp.MustCompile(`SET ROLE ` + owner + `;.*ALTER DATABASE ` + database + ` OWNER TO ` + owner + `;.*REVOKE CONNECT ON DATABASE ` + database + ` FROM PUBLIC;.*RESET ROLE;`)
		if !pattern.MatchString(normalized) {
			t.Errorf("database %s ownership and ACLs do not run through exact owner %s", database, owner)
		}
	}
}

func TestDatabaseBootstrapRepairsLegacyTestOwnersBeforeMaintenanceACLs(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	secondMaintenance := strings.LastIndex(script, `PGUSER=$maintenance_user "$PSQL" -v ON_ERROR_STOP=1 -d postgres`)
	if secondMaintenance < 0 {
		t.Fatal("bootstrap does not contain the database maintenance session")
	}
	normalizedRepair := strings.Join(strings.Fields(script[:secondMaintenance]), " ")
	for database, owner := range map[string]string{
		"bolter_funding_test":  "bolter_funding_test_migrator",
		"bolter_platform_test": "bolter_platform_test_migrator",
		"bolter_gateway_test":  "bolter_gateway_test_migrator",
	} {
		repair := "SELECT 'ALTER DATABASE " + database + " OWNER TO " + owner + "' WHERE EXISTS (SELECT 1 FROM pg_database WHERE datname = '" + database + "')\\gexec"
		if !strings.Contains(normalizedRepair, repair) {
			t.Errorf("bootstrap does not repair fixed database %s to owner %s before maintenance ACLs", database, owner)
		}
	}
}

func TestDatabaseBootstrapGrantsMaintenanceTargetConnect(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(string(payload)), " ")
	for _, database := range []string{"bolter_funding_test", "bolter_platform_test", "bolter_gateway_test"} {
		grant := "GRANT CONNECT ON DATABASE " + database + " TO bolter_platform_test_admin"
		if !strings.Contains(normalized, grant) {
			t.Errorf("bootstrap does not grant bounded maintenance CONNECT for %s", database)
		}
	}
	if strings.Contains(normalized, `REVOKE CONNECT ON DATABASE :"database" FROM bolter_platform_test_admin`) {
		t.Fatal("bootstrap revokes target CONNECT required by the NOLOGIN migrator service")
	}
}

func TestDatabaseBootstrapMaintenanceRoleNeverGrantsAuthorityToItself(t *testing.T) {
	payload, err := os.ReadFile("db-bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(payload)
	start := strings.Index(script, `PGUSER=$maintenance_user "$PSQL"`)
	if start < 0 {
		t.Fatal("bootstrap does not open an authenticated maintenance-role block")
	}
	end := strings.Index(script[start:], `PGUSER=$cluster_admin_user "$PSQL"`)
	if end < 0 {
		t.Fatal("bootstrap does not close the authenticated maintenance-role block")
	}
	maintenanceBlock := script[start : start+end]
	if strings.Contains(maintenanceBlock, "TO bolter_platform_test_admin") {
		t.Fatal("maintenance role grants role authority back to itself")
	}
}

func TestDatabaseBootstrapScriptsPassShellSyntaxCheck(t *testing.T) {
	paths := []string{
		"db-bootstrap.sh",
		"secret-bootstrap.sh",
		filepath.Join("..", "..", "gateway", "scripts", "db-bootstrap.sh"),
		filepath.Join("..", "..", "gateway", "scripts", "secret-bootstrap.sh"),
	}
	for _, path := range paths {
		command := exec.Command("sh", "-n", path)
		if output, err := command.CombinedOutput(); err != nil {
			t.Errorf("sh -n %s: %v\n%s", path, err, output)
		}
	}
}

func TestSecretBootstrapKeepsMasterKeysOpaqueAndModesSeparate(t *testing.T) {
	payload, err := os.ReadFile("secret-bootstrap.sh")
	if err != nil {
		t.Fatalf("read secret-bootstrap.sh: %v", err)
	}
	script := string(payload)
	for _, required := range []string{
		"boltertrader/test",
		"boltertrader/live",
		"BT_SECRET_MASTER_KEY_FILE",
		"SECRET_STORE_KEY_MISSING",
		"SECRET_STORE_KEY_VERSION_UNSUPPORTED",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("secret bootstrap missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"cat \"$BT_SECRET_MASTER_KEY_FILE\"",
		"echo \"$BT_SECRET_MASTER_KEY",
		"printf '%s' \"$BT_SECRET_MASTER_KEY",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("secret bootstrap reads or emits master-key material via %q", forbidden)
		}
	}
}

func TestSecretBootstrapRejectsKeyReuseAndWritesOnlySafeMetadata(t *testing.T) {
	runtimeDir := t.TempDir()
	testKey := filepath.Join(t.TempDir(), "test.key")
	copiedTestKey := filepath.Join(t.TempDir(), "copied-test.key")
	liveKey := filepath.Join(t.TempDir(), "live.key")
	const testCanary = "synthetic-test-master-key-canary"
	const liveCanary = "synthetic-live-master-key-canary"
	if err := os.WriteFile(testKey, []byte(testCanary), 0o600); err != nil {
		t.Fatalf("write Test key fixture: %v", err)
	}
	if err := os.WriteFile(copiedTestKey, []byte(testCanary), 0o600); err != nil {
		t.Fatalf("write copied Test key fixture: %v", err)
	}
	if err := os.WriteFile(liveKey, []byte(liveCanary), 0o600); err != nil {
		t.Fatalf("write Live key fixture: %v", err)
	}

	run := func(mode, key, version string) (string, error) {
		command := exec.Command("sh", "secret-bootstrap.sh")
		command.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"FT_MODE=" + mode,
			"FT_LOCAL_RUNTIME_DIR=" + runtimeDir,
			"BT_SECRET_MASTER_KEY_FILE=" + key,
			"BT_SECRET_MASTER_KEY_VERSION=" + version,
		}
		output, err := command.CombinedOutput()
		return string(output), err
	}

	if output, err := run("test", testKey, "v1"); err != nil {
		t.Fatalf("bootstrap Test: %v\n%s", err, output)
	}
	if output, err := run("live", testKey, "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISMATCH") {
		t.Fatalf("cross-mode key reuse = (%v, %q), want mismatch", err, output)
	}
	if output, err := run("live", copiedTestKey, "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISMATCH") {
		t.Fatalf("copied cross-mode key reuse = (%v, %q), want mismatch", err, output)
	}
	if output, err := run("live", liveKey, "v1"); err != nil {
		t.Fatalf("bootstrap Live: %v\n%s", err, output)
	}
	if output, err := run("test", testKey, "v2"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_VERSION_UNSUPPORTED") {
		t.Fatalf("unsupported version = (%v, %q)", err, output)
	}
	if output, err := run("test", filepath.Join(t.TempDir(), "missing.key"), "v1"); err == nil || !strings.Contains(output, "SECRET_STORE_KEY_MISSING") {
		t.Fatalf("missing key = (%v, %q)", err, output)
	}

	for _, mode := range []string{"test", "live"} {
		metadata, err := os.ReadFile(filepath.Join(runtimeDir, "secret-store", mode, "bootstrap.meta"))
		if err != nil {
			t.Fatalf("read %s metadata: %v", mode, err)
		}
		if strings.Contains(string(metadata), testCanary) || strings.Contains(string(metadata), liveCanary) {
			t.Fatalf("%s metadata leaked key fixture: %s", mode, metadata)
		}
		if !strings.Contains(string(metadata), "namespace=boltertrader/"+mode) {
			t.Fatalf("%s metadata has wrong namespace: %s", mode, metadata)
		}
	}
}
