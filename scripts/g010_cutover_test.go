package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestG010Cutover(t *testing.T) {
	repo := repoRoot(t)
	driver := filepath.Join(repo, "scripts", "g010-cutover")
	if _, err := os.Stat(driver); err != nil {
		t.Fatalf("guarded cutover driver is missing: %v", err)
	}

	t.Run("requires explicit opt in", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.unset("BT_G010_CUTOVER_ACCEPT")
		h.rejects(driver, "BT_G010_CUTOVER_ACCEPT=1")
	})

	for _, mode := range []string{"", "live", "Test", "TEST", "test "} {
		mode := mode
		t.Run("rejects mode "+fmt.Sprintf("%q", mode), func(t *testing.T) {
			h := newG010Harness(t, repo, "baseline")
			h.set("FT_MODE", mode)
			h.rejects(driver, "FT_MODE must be exactly test")
		})
	}

	for _, flag := range []string{
		"BT_DEV_ENABLE_LIVE_PRIVATE_WRITES", "BT_LIVE_PRIVATE_WRITES_ENABLED",
		"BOLTER_ENABLE_BINANCE_WRITES", "BOLTER_ALLOW_BYBIT_WRITES",
	} {
		flag := flag
		t.Run("rejects live write flag "+flag, func(t *testing.T) {
			h := newG010Harness(t, repo, "baseline")
			h.set(flag, "1")
			h.rejects(driver, flag)
		})
	}

	t.Run("requires durable backup and restore evidence", func(t *testing.T) {
		h := newG010Harness(t, repo, "restored")
		if err := os.Remove(h.backupArtifact); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "backup evidence")

		h = newG010Harness(t, repo, "restored")
		if err := os.Remove(filepath.Join(h.evidenceDir, "003-restore.proof.json")); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "restore evidence")
	})

	t.Run("rejects non mode scoped state paths", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.set("BT_G010_STATE_DIR", filepath.Join(h.runtimeDir, "state", "live"))
		h.rejects(driver, "mode-scoped")
	})

	t.Run("requires loopback services and Test identities", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.set("BT_GATEWAY_URL", "https://gateway.example.test")
		h.rejects(driver, "loopback")

		h = newG010Harness(t, repo, "baseline")
		h.set("BT_G010_TARGET_DATABASE", "bolter_platform_live")
		h.rejects(driver, "mode-scoped Test identity")
	})

	t.Run("requires healthy Secret Store permissions", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		key := h.env["BT_SECRET_MASTER_KEY_FILE"]
		if err := os.Chmod(key, 0o644); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "permissions must be 0600")
	})

	t.Run("rejects post fence source writers", func(t *testing.T) {
		h := newG010Harness(t, repo, "fenced")
		h.set("FAKE_SOURCE_WRITER_STATE", "true|1")
		h.rejects(driver, "source writer remains active after fence")
		h.assertMigrationOperations("status")
	})

	t.Run("rejects a failed real browser check", func(t *testing.T) {
		h := newG010Harness(t, repo, "backfilled")
		h.set("FAKE_BROWSER_FAIL", "1")
		h.rejects(driver, "real browser verification failed")
		h.assertMigrationOperations("status")
	})

	t.Run("rejects private venue write route commands", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		privateCommand := filepath.Join(h.root, "api", "v1", "private", "orders")
		if err := os.MkdirAll(filepath.Dir(privateCommand), 0o700); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, privateCommand, "#!/bin/sh\nexit 0\n")
		h.set("BT_G010_DEV_PLATFORM", privateCommand)
		h.rejects(driver, "private venue write route")
	})

	t.Run("executes only the next legal phase", func(t *testing.T) {
		cases := []struct {
			phase string
			next  string
		}{
			{"baseline", "record-import"},
			{"provenance-ready", "backup"},
			{"backed-up", "restore"},
			{"restored", "final-delta"},
			{"delta-captured", "fence"},
			{"fenced", "backfill"},
			{"backfilled", "validate"},
			{"validated", "prepare-activation"},
		}
		for _, tc := range cases {
			t.Run(tc.phase, func(t *testing.T) {
				h := newG010Harness(t, repo, tc.phase)
				manifestBefore, err := os.ReadFile(h.env["BT_G010_MANIFEST_PATH"])
				if err != nil {
					t.Fatal(err)
				}
				h.accepts(driver)
				h.assertMigrationOperations("status", tc.next)
				h.assertNoPrivateWriteCommand()
				manifestAfter, err := os.ReadFile(h.env["BT_G010_MANIFEST_PATH"])
				if err != nil {
					t.Fatal(err)
				}
				if string(manifestAfter) != string(manifestBefore) {
					t.Fatal("driver edited the manifest instead of delegating evidence recording to migration-tool")
				}
			})
		}
	})

	t.Run("resumes without retirement", func(t *testing.T) {
		h := newG010Harness(t, repo, "activation-ready")
		h.accepts(driver)
		h.assertMigrationOperations("status")

		h = newG010Harness(t, repo, "activated")
		h.accepts(driver)
		h.assertMigrationOperations("status")
	})
}

type g010Harness struct {
	t              *testing.T
	root           string
	runtimeDir     string
	evidenceDir    string
	backupArtifact string
	commandLog     string
	env            map[string]string
}

func newG010Harness(t *testing.T, repo, phase string) *g010Harness {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	evidenceDir := filepath.Join(runtimeDir, "migration", "test", "evidence")
	backupArtifact := filepath.Join(runtimeDir, "migration", "test", "backup.dump")
	deltaArtifact := filepath.Join(runtimeDir, "migration", "test", "final-delta.dump")
	secretDir := filepath.Join(runtimeDir, "secret-store")
	secretKey := filepath.Join(runtimeDir, "credentials", "test", "secret-store-master.key")
	for _, dir := range []string{evidenceDir, secretDir, filepath.Dir(secretKey)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{backupArtifact, deltaArtifact, secretKey} {
		if err := os.WriteFile(path, []byte("non-secret-test-evidence\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"002-backup.proof.json", "003-restore.proof.json", "004-final-delta.proof.json", "005-fence.proof.json", "006-backfill.proof.json", "007-validate.proof.json"} {
		if err := os.WriteFile(filepath.Join(evidenceDir, name), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(root, "commands")
	migrationTool := filepath.Join(binDir, "migration-tool")
	writeExecutable(t, migrationTool, `#!/bin/sh
set -eu
printf 'migration-tool %s\n' "$*" >>"$FAKE_COMMAND_LOG"
if [ "${1:-}" = status ]; then
	printf '{"phase":"%s","activationPrepared":false,"retirementPrepared":false}\n' "$FAKE_PHASE"
	exit 0
fi
printf '{"phase":"completed","activationPrepared":false,"retirementPrepared":false}\n'
`)
	devPlatform := filepath.Join(binDir, "dev-platform")
	writeExecutable(t, devPlatform, `#!/bin/sh
set -eu
printf 'dev-platform %s\n' "$*" >>"$FAKE_COMMAND_LOG"
`)
	psql := filepath.Join(binDir, "psql")
	writeExecutable(t, psql, `#!/bin/sh
set -eu
printf 'psql %s\n' "$*" >>"$FAKE_COMMAND_LOG"
case "$*" in
	*funding_source_test*) printf '%s\n' "${FAKE_SOURCE_WRITER_STATE:-false|0}" ;;
	*platform_target_test*) printf '%s\n' "${FAKE_TARGET_WRITER_STATE:-false|0}" ;;
	*) printf 'ok\n' ;;
esac
`)
	makeShim := filepath.Join(binDir, "make")
	writeExecutable(t, makeShim, `#!/bin/sh
set -eu
printf 'make %s\n' "$*" >>"$FAKE_COMMAND_LOG"
`)
	npm := filepath.Join(binDir, "npm")
	writeExecutable(t, npm, `#!/bin/sh
set -eu
printf 'npm %s\n' "$*" >>"$FAKE_COMMAND_LOG"
[ "${FAKE_BROWSER_FAIL:-0}" = 0 ]
`)

	manifest := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifest, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &g010Harness{t: t, root: root, runtimeDir: runtimeDir, evidenceDir: evidenceDir, backupArtifact: backupArtifact, commandLog: commandLog, env: map[string]string{}}
	for key, value := range map[string]string{
		"FT_MODE": "test", "BT_G010_CUTOVER_ACCEPT": "1", "FT_LOCAL_RUNTIME_DIR": runtimeDir,
		"BT_G010_MANIFEST_PATH": manifest, "BT_G010_MIGRATION_TOOL": migrationTool,
		"BT_G010_DEV_PLATFORM": devPlatform, "BT_G010_PSQL": psql, "BT_G010_MAKE": makeShim, "BT_G010_NPM": npm,
		"BT_G010_IMPORT_COMMIT": strings.Repeat("a", 40), "FAKE_PHASE": phase, "FAKE_COMMAND_LOG": commandLog,
		"BT_GATEWAY_URL": "http://127.0.0.1:18082", "BT_PLATFORM_BROWSER_URL": "http://127.0.0.1:18091/api/v1",
		"BT_PLATFORM_WORK_URL": "http://127.0.0.1:18092", "BT_WEB_URL": "http://127.0.0.1:13001",
		"BT_SECRET_STORE_DIR": secretDir, "BT_SECRET_STORE_NAMESPACE": "boltertrader/test", "BT_SECRET_MASTER_KEY_FILE": secretKey,
		"BT_PLATFORM_DATABASE": "bolter_platform_test", "BT_PLATFORM_DATABASE_ROLE": "bolter_platform_test",
	} {
		h.set(key, value)
	}
	return h
}

func (h *g010Harness) set(key, value string) { h.env[key] = value }
func (h *g010Harness) unset(key string)      { delete(h.env, key) }

func (h *g010Harness) command(driver string) *exec.Cmd {
	h.t.Helper()
	cmd := exec.Command(driver)
	cmd.Dir = filepath.Dir(filepath.Dir(driver))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + h.root}
	for key, value := range h.env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	return cmd
}

func (h *g010Harness) rejects(driver, want string) {
	h.t.Helper()
	output, err := h.command(driver).CombinedOutput()
	if err == nil {
		h.t.Fatalf("driver succeeded; output=%s", output)
	}
	if !strings.Contains(string(output), want) {
		h.t.Fatalf("output=%q, want rejection containing %q", output, want)
	}
}

func (h *g010Harness) accepts(driver string) {
	h.t.Helper()
	output, err := h.command(driver).CombinedOutput()
	if err != nil {
		h.t.Fatalf("driver failed: %v\n%s", err, output)
	}
}

func (h *g010Harness) assertMigrationOperations(want ...string) {
	h.t.Helper()
	data, err := os.ReadFile(h.commandLog)
	if err != nil {
		h.t.Fatal(err)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "migration-tool ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			got = append(got, fields[1])
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Fatalf("migration operations=%v, want %v; log:\n%s", got, want, data)
	}
}

func (h *g010Harness) assertNoPrivateWriteCommand() {
	h.t.Helper()
	data, err := os.ReadFile(h.commandLog)
	if err != nil {
		h.t.Fatal(err)
	}
	lower := strings.ToLower(string(data))
	for _, route := range []string{"/orders", "/positions", "/leverage", "/withdraw", "/transfer"} {
		if strings.Contains(lower, route) {
			h.t.Fatalf("private venue route %q found in commands:\n%s", route, data)
		}
	}
}
