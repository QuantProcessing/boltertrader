package postgres

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/cutover"
)

func TestDisposableBackupCutoverAndExactLegacyRestoreRehearsal(t *testing.T) {
	if os.Getenv("RUN_CUTOVER_BACKUP_REHEARSAL") != "1" {
		t.Skip("RUN_CUTOVER_BACKUP_REHEARSAL is not enabled")
	}
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Fatal("guarded TEST_DATABASE_URL is required")
	}
	const (
		container    = "funding-trader-postgres-local"
		database     = "funding_trader_test"
		databaseUser = "funding_trader_local"
		archive      = "/tmp/funding-trader-product-mode-cutover.dump"
	)
	ctx := context.Background()
	control := openNamedTestPool(t, ctx, dsn, "funding-trader-cutover-test-control")
	resetCutoverDatabase(t, ctx, control)
	applyLegacyMigrationsForTest(t, ctx, control)
	seedSuccessfulCutover(t, ctx, control)
	control.Close()

	runDocker(t, ctx, "exec", container,
		"pg_dump", "-Fc", "-U", databaseUser, "-d", database, "-f", archive)
	hashOutput := runDocker(t, ctx, "exec", container, "sha256sum", archive)
	fields := strings.Fields(hashOutput)
	if len(fields) == 0 || len(fields[0]) != 64 {
		t.Fatalf("invalid backup checksum output %q", hashOutput)
	}
	proof := cutover.Proof{
		BackupID:            "disposable-rehearsal-1",
		BackupSHA256:        fields[0],
		BackupVerifiedAt:    time.Now().UTC(),
		OldServiceStopped:   true,
		AutoRestartDisabled: true,
	}

	cutoverPool, err := OpenCutoverPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewCutoverRunner(cutoverPool, proof).Run(ctx); err != nil {
		cutoverPool.Close()
		t.Fatal(err)
	}
	stateStore := NewCutoverStateStore(cutoverPool)
	if err := stateStore.AuthorizeRestore(ctx, proof.BackupID); err != nil {
		cutoverPool.Close()
		t.Fatalf("restore boundary rejected verified pre-reopen backup: %v", err)
	}
	cutoverPool.Close()

	runDocker(t, ctx, "exec", container,
		"dropdb", "--if-exists", "-U", databaseUser, database)
	runDocker(t, ctx, "exec", container,
		"createdb", "-U", databaseUser, database)
	runDocker(t, ctx, "exec", container,
		"pg_restore", "-U", databaseUser, "-d", database, archive)
	t.Cleanup(func() {
		_ = exec.Command("docker", "exec", container, "rm", "-f", archive).Run()
	})

	restored := openNamedTestPool(t, ctx, dsn, "funding-trader-restore-verifier")
	defer restored.Close()
	assertColumnPresent(t, ctx, restored, "executions", "environment")
	assertColumnAbsent(t, ctx, restored, "executions", "mode")
	var maxVersion int
	var requestHash string
	if err := restored.QueryRow(ctx, `
		select
		  (select max(version) from schema_migrations),
		  (select request_hash from idempotency_records where id='idem-1')
	`).Scan(&maxVersion, &requestHash); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 3 || requestHash != "request-hash-1" {
		t.Fatalf("restored maxVersion=%d requestHash=%q", maxVersion, requestHash)
	}
}

func runDocker(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
