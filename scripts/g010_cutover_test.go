package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestG010Cutover(t *testing.T) {
	repo := repoRoot(t)
	driver := filepath.Join(repo, "scripts", "g010-cutover")

	t.Run("requires explicit opt in and exact Test mode", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.unset("BT_G010_CUTOVER_ACCEPT")
		h.rejects(driver, "BT_G010_CUTOVER_ACCEPT=1")

		for _, mode := range []string{"", "live", "Test", "TEST", "test "} {
			h = newG010Harness(t, repo, "baseline")
			h.set("FT_MODE", mode)
			h.rejects(driver, "FT_MODE must be exactly test")
		}
	})

	t.Run("rejects every venue write opt in", func(t *testing.T) {
		for _, flag := range []string{
			"BT_DEV_ENABLE_LIVE_PRIVATE_WRITES", "BT_LIVE_PRIVATE_WRITES_ENABLED",
			"BOLTER_ENABLE_BINANCE_WRITES", "BOLTER_ALLOW_BYBIT_WRITES",
		} {
			h := newG010Harness(t, repo, "baseline")
			h.set(flag, "1")
			h.rejects(driver, flag)
		}
	})

	t.Run("rejects non mode scoped paths and mixed mode identities", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.set("BT_G010_STATE_DIR", filepath.Join(h.runtimeDir, "state", "live"))
		h.rejects(driver, "mode-scoped")

		for _, tc := range []struct{ key, value string }{
			{"BT_G010_SOURCE_SERVICE", "funding_live_test"},
			{"BT_G010_SOURCE_SERVICE", "FUNDING_LIVE_test"},
			{"BT_G010_TARGET_DATABASE", "test_live"},
			{"BT_G010_GATEWAY_ROLE", "gateway-live-test"},
			{"BT_G010_TARGET_WRITER_ROLE", "contest_writer"},
		} {
			h = newG010Harness(t, repo, "baseline")
			h.set(tc.key, tc.value)
			h.rejects(driver, "mode-scoped Test identity")
		}
	})

	t.Run("requires loopback services and healthy Secret Store permissions", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.set("BT_GATEWAY_URL", "https://gateway.example.test")
		h.rejects(driver, "loopback")

		h = newG010Harness(t, repo, "baseline")
		key := h.env["BT_SECRET_MASTER_KEY_FILE"]
		if err := os.Chmod(key, 0o644); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "permissions must be 0600")
	})

	t.Run("rejects missing and tampered digest bound evidence", func(t *testing.T) {
		h := newG010Harness(t, repo, "restored")
		if err := os.Remove(h.backupArtifact); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "backup replay conflict: artifact changed")

		h = newG010Harness(t, repo, "restored")
		if err := os.WriteFile(filepath.Join(h.evidenceDir, "003-restore.json"), []byte("{\"status\":\"pass\",\"tampered\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "restore replay conflict: evidence changed")
	})

	t.Run("revalidates import evidence and Git provenance on restart", func(t *testing.T) {
		h := newG010Harness(t, repo, "provenance-ready")
		if err := os.WriteFile(filepath.Join(h.evidenceDir, "001-record-import.json"), []byte("{\"status\":\"pass\",\"tampered\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "record-import evidence changed")

		h = newG010Harness(t, repo, "provenance-ready")
		h.set("FAKE_GIT_PROVENANCE_FAIL", "1")
		h.rejects(driver, "git provenance rejected")
	})

	t.Run("revalidates activation evidence on restart", func(t *testing.T) {
		h := newG010Harness(t, repo, "activated")
		proof := h.env["BT_G010_ACTIVATION_PROOF"]
		if err := os.WriteFile(proof, []byte("{\"status\":\"pass\",\"tampered\":true}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.rejects(driver, "record-activation-evidence evidence changed")
	})

	t.Run("rejects post fence writers and failed browser verification", func(t *testing.T) {
		h := newG010Harness(t, repo, "fenced")
		h.set("FAKE_SOURCE_WRITER_STATE", "true|1")
		h.rejects(driver, "source writer remains active after fence")

		h = newG010Harness(t, repo, "backfilled")
		h.set("FAKE_BROWSER_FAIL", "1")
		h.rejects(driver, "real browser verification failed")

		h = newG010Harness(t, repo, "validated")
		h.set("FAKE_BROWSER_FAIL", "1")
		h.rejects(driver, "real browser verification failed")

		h = newG010Harness(t, repo, "activation-ready")
		h.set("FAKE_BROWSER_FAIL", "1")
		h.rejects(driver, "real browser verification failed")
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

	t.Run("one invocation reaches activation ready through durable phases", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.accepts(driver)
		if phase := h.phase(); phase != "activation-ready" {
			t.Fatalf("durable phase=%q, want activation-ready", phase)
		}
		h.assertMigrationAdvances(
			"record-import", "backup", "restore", "final-delta", "fence", "backfill", "validate", "prepare-activation",
		)
		h.assertValidationGateAfter("backfill")
		h.assertNoPrivateWriteCommand()
	})

	t.Run("resumes after an interrupted durable mutation", func(t *testing.T) {
		h := newG010Harness(t, repo, "baseline")
		h.set("FAKE_INTERRUPT_AFTER", "restore")
		h.rejects(driver, "migration-tool restore failed")
		if phase := h.phase(); phase != "restored" {
			t.Fatalf("phase after interruption=%q, want restored", phase)
		}

		h.accepts(driver)
		if phase := h.phase(); phase != "activation-ready" {
			t.Fatalf("phase after resume=%q, want activation-ready", phase)
		}
		h.assertMigrationAdvances(
			"record-import", "backup", "restore", "final-delta", "fence", "backfill", "validate", "prepare-activation",
		)
		h.assertNoPrivateWriteCommand()
	})

	t.Run("stops before retirement", func(t *testing.T) {
		h := newG010Harness(t, repo, "activation-ready")
		h.accepts(driver)
		if strings.Contains(h.commands(), "prepare-retirement") {
			t.Fatal("G010 issued source retirement preparation")
		}
	})
}

type g010Harness struct {
	t              *testing.T
	root           string
	runtimeDir     string
	evidenceDir    string
	backupArtifact string
	deltaArtifact  string
	phaseFile      string
	commandLog     string
	env            map[string]string
}

func newG010Harness(t *testing.T, repo, phase string) *g010Harness {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	evidenceDir := filepath.Join(runtimeDir, "migration", "test", "evidence")
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
	if err := os.WriteFile(secretKey, []byte("non-secret-test-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	commandLog := filepath.Join(root, "commands")
	phaseFile := filepath.Join(root, "phase")
	ledgerFile := filepath.Join(root, "ledger")
	interruptMarker := filepath.Join(root, "interrupted")
	migrationTool := filepath.Join(binDir, "migration-tool")
	writeExecutable(t, migrationTool, fakeMigrationTool)
	devPlatform := filepath.Join(binDir, "dev-platform")
	writeExecutable(t, devPlatform, "#!/bin/sh\nset -eu\nprintf 'dev-platform %s\\n' \"$*\" >>\"$FAKE_COMMAND_LOG\"\n")
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
	writeExecutable(t, makeShim, "#!/bin/sh\nset -eu\nprintf 'make %s\\n' \"$*\" >>\"$FAKE_COMMAND_LOG\"\n")
	npm := filepath.Join(binDir, "npm")
	writeExecutable(t, npm, "#!/bin/sh\nset -eu\nprintf 'npm %s\\n' \"$*\" >>\"$FAKE_COMMAND_LOG\"\n[ \"${FAKE_BROWSER_FAIL:-0}\" = 0 ]\n")

	manifest := filepath.Join(root, "manifest.json")
	backfill := filepath.Join(root, "account-route-v1.json")
	activationProof := filepath.Join(root, "activation-proof.json")
	if err := os.WriteFile(manifest, []byte("fake durable manifest owned by migration-tool\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backfill, []byte("{\"routes\":[]}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(activationProof, []byte("{\"schemaVersion\":1,\"operation\":\"record-activation-evidence\",\"status\":\"pass\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := &g010Harness{
		t: t, root: root, runtimeDir: runtimeDir, evidenceDir: evidenceDir,
		backupArtifact: filepath.Join(runtimeDir, "migration", "test", "backup.dump"),
		deltaArtifact:  filepath.Join(runtimeDir, "migration", "test", "final-delta.dump"),
		phaseFile:      phaseFile, commandLog: commandLog, env: map[string]string{},
	}
	for key, value := range map[string]string{
		"FT_MODE": "test", "BT_G010_CUTOVER_ACCEPT": "1", "FT_LOCAL_RUNTIME_DIR": runtimeDir,
		"BT_G010_MANIFEST_PATH": manifest, "BT_G010_MIGRATION_TOOL": migrationTool,
		"BT_G010_DEV_PLATFORM": devPlatform, "BT_G010_PSQL": psql, "BT_G010_MAKE": makeShim, "BT_G010_NPM": npm,
		"BT_G010_IMPORT_COMMIT": strings.Repeat("a", 40), "BT_G010_BACKFILL_MAPPING": backfill,
		"BT_G010_ACTIVATION_AT": "2026-08-10T01:00:00Z", "BT_G010_CUTOVER_AT": "2026-08-10T01:01:00Z", "BT_G010_ACTIVATION_PROOF": activationProof,
		"FAKE_PHASE_FILE": phaseFile, "FAKE_LEDGER_FILE": ledgerFile, "FAKE_EVIDENCE_DIR": evidenceDir,
		"FAKE_INTERRUPT_MARKER": interruptMarker, "FAKE_COMMAND_LOG": commandLog,
		"BT_GATEWAY_URL": "http://127.0.0.1:18082", "BT_PLATFORM_BROWSER_URL": "http://127.0.0.1:18091/api/v1",
		"BT_PLATFORM_WORK_URL": "http://127.0.0.1:18092", "BT_WEB_URL": "http://127.0.0.1:13001",
		"BT_SECRET_STORE_DIR": secretDir, "BT_SECRET_STORE_NAMESPACE": "boltertrader/test", "BT_SECRET_MASTER_KEY_FILE": secretKey,
		"BT_PLATFORM_DATABASE": "bolter_platform_test", "BT_PLATFORM_DATABASE_ROLE": "bolter_platform_test",
	} {
		h.set(key, value)
	}
	if err := os.WriteFile(phaseFile, []byte("baseline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ledgerFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, operation := range operationsToPhase(phase) {
		h.seed(operation)
	}
	if phase != "activated" {
		h.unset("BT_G010_ACTIVATION_AT")
		h.unset("BT_G010_CUTOVER_AT")
		h.unset("BT_G010_ACTIVATION_PROOF")
	}
	if err := os.WriteFile(commandLog, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return h
}

func operationsToPhase(phase string) []string {
	all := []string{"record-import", "backup", "restore", "final-delta", "fence", "backfill", "validate", "prepare-activation", "record-activation-evidence"}
	phases := []string{"baseline", "provenance-ready", "backed-up", "restored", "delta-captured", "fenced", "backfilled", "validated", "activation-ready", "activated"}
	for index, candidate := range phases {
		if phase == candidate {
			return all[:index]
		}
	}
	panic("unsupported test phase: " + phase)
}

func (h *g010Harness) seed(operation string) {
	h.t.Helper()
	args := []string{operation, "--manifest", h.env["BT_G010_MANIFEST_PATH"], "--evidence-dir", h.evidenceDir}
	switch operation {
	case "record-import":
		args = append(args, "--id", h.env["BT_G010_IMPORT_COMMIT"])
	case "backup", "restore":
		args = append(args, "--artifact", h.backupArtifact)
	case "final-delta":
		args = append(args, "--artifact", h.deltaArtifact)
	case "backfill":
		args = append(args, "--backfill-mapping", h.env["BT_G010_BACKFILL_MAPPING"])
	case "record-activation-evidence":
		args = append(args, "--proof", h.env["BT_G010_ACTIVATION_PROOF"], "--activation-at", h.env["BT_G010_ACTIVATION_AT"], "--cutover-at", h.env["BT_G010_CUTOVER_AT"])
	}
	cmd := exec.Command(h.env["BT_G010_MIGRATION_TOOL"], args...)
	cmd.Env = h.environ()
	if output, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("seed %s: %v\n%s", operation, err, output)
	}
}

func (h *g010Harness) set(key, value string) { h.env[key] = value }
func (h *g010Harness) unset(key string)      { delete(h.env, key) }

func (h *g010Harness) environ() []string {
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + h.root}
	for key, value := range h.env {
		env = append(env, key+"="+value)
	}
	return env
}

func (h *g010Harness) command(driver string) *exec.Cmd {
	cmd := exec.Command(driver)
	cmd.Dir = filepath.Dir(filepath.Dir(driver))
	cmd.Env = h.environ()
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

func (h *g010Harness) phase() string {
	h.t.Helper()
	data, err := os.ReadFile(h.phaseFile)
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func (h *g010Harness) commands() string {
	h.t.Helper()
	data, err := os.ReadFile(h.commandLog)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *g010Harness) assertMigrationAdvances(want ...string) {
	h.t.Helper()
	var got []string
	for _, line := range strings.Split(h.commands(), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "migration-tool" && fields[2] == "advance" {
			got = append(got, fields[1])
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		h.t.Fatalf("migration advances=%v, want %v; log:\n%s", got, want, h.commands())
	}
}

func (h *g010Harness) assertValidationGateAfter(operation string) {
	h.t.Helper()
	commands := h.commands()
	advance := strings.Index(commands, "migration-tool "+operation+" advance")
	browser := strings.Index(commands, "npm --prefix web run test:e2e:real")
	if advance < 0 || browser < advance {
		h.t.Fatalf("validation browser gate ran before %s completed:\n%s", operation, commands)
	}
	if strings.Contains(commands[:advance], "dev-platform ") || strings.Contains(commands[:advance], "npm ") {
		h.t.Fatalf("full stack gate ran during an intermediate legacy phase:\n%s", commands)
	}
}

func (h *g010Harness) assertNoPrivateWriteCommand() {
	h.t.Helper()
	lower := strings.ToLower(h.commands())
	for _, route := range []string{"/orders", "/positions", "/leverage", "/withdraw", "/transfer"} {
		if strings.Contains(lower, route) {
			h.t.Fatalf("private venue route %q found in commands:\n%s", route, h.commands())
		}
	}
}

const fakeMigrationTool = `#!/bin/sh
set -eu

hash_file() { shasum -a 256 "$1" | awk '{print $1}'; }
arg_value() {
	flag=$1
	shift
	while [ "$#" -gt 0 ]; do
		if [ "$1" = "$flag" ]; then printf '%s\n' "$2"; return 0; fi
		shift
	done
	return 1
}
phase=$(sed -n '1p' "$FAKE_PHASE_FILE")
operation=${1:-}
shift || true

if [ "$operation" = status ]; then
	expected_sequence=1
	while IFS='|' read -r sequence recorded artifact_sha proof_sha; do
		[ -n "$sequence" ] || continue
		[ "$sequence" = "$expected_sequence" ] || { printf 'status: invalid event sequence\n' >&2; exit 1; }
		case "$artifact_sha" in -|????????????????????????????????????????????????????????????????) ;; *) printf 'status: invalid artifact digest\n' >&2; exit 1 ;; esac
		case "$proof_sha" in -|????????????????????????????????????????????????????????????????) ;; *) printf 'status: invalid proof digest\n' >&2; exit 1 ;; esac
		expected_sequence=$((expected_sequence + 1))
	done <"$FAKE_LEDGER_FILE"
	printf 'migration-tool status inspect\n' >>"$FAKE_COMMAND_LOG"
	printf '{"phase":"%s","activationPrepared":false,"retirementPrepared":false}\n' "$phase"
	exit 0
fi

if [ "$operation" = verify-evidence ]; then
	printf 'migration-tool verify-evidence inspect\n' >>"$FAKE_COMMAND_LOG"
	[ "${FAKE_GIT_PROVENANCE_FAIL:-0}" = 0 ] || { printf 'git provenance rejected\n' >&2; exit 1; }
	while IFS='|' read -r sequence recorded artifact_sha proof_sha; do
		case "$recorded" in
			record-import)
				proof="$FAKE_EVIDENCE_DIR/$(printf '%03d' "$sequence")-$recorded.json"
				[ -f "$proof" ] && [ "$(hash_file "$proof")" = "$proof_sha" ] || { printf 'record-import evidence changed\n' >&2; exit 1; }
				;;
			record-activation-evidence)
				proof=$(arg_value --activation-proof "$@")
				[ -f "$proof" ] && [ "$(hash_file "$proof")" = "$proof_sha" ] || { printf 'record-activation-evidence evidence changed\n' >&2; exit 1; }
				;;
		esac
	done <"$FAKE_LEDGER_FILE"
	printf '{"phase":"%s","evidenceVerified":true}\n' "$phase"
	exit 0
fi

artifact=-
case "$operation" in
	backup|restore) artifact=$(arg_value --artifact "$@") ;;
	final-delta) artifact=$(arg_value --artifact "$@") ;;
	backfill) artifact=$(arg_value --backfill-mapping "$@") ;;
esac

record=$(awk -F '|' -v operation="$operation" '$2 == operation { print; exit }' "$FAKE_LEDGER_FILE")
if [ -n "$record" ]; then
	IFS='|' read -r sequence recorded expected_artifact_sha expected_proof_sha <<EOF
$record
EOF
	proof="$FAKE_EVIDENCE_DIR/$(printf '%03d' "$sequence")-$operation.json"
	[ -f "$proof" ] && [ "$(hash_file "$proof")" = "$expected_proof_sha" ] || { printf '%s replay conflict: evidence changed\n' "$operation" >&2; exit 1; }
	case "$artifact" in
		-) [ "$expected_artifact_sha" = - ] || exit 1 ;;
		*) [ -f "$artifact" ] && [ "$(hash_file "$artifact")" = "$expected_artifact_sha" ] || { printf '%s replay conflict: artifact changed\n' "$operation" >&2; exit 1; } ;;
	esac
	case "$(cat "$proof")" in *'"schemaVersion":1'*'"operation":"'"$operation"'"'*'"status":"pass"'*'"generatedBy":"migration-tool/executor-v1"'*) ;; *) printf '%s replay conflict: generated evidence is invalid\n' "$operation" >&2; exit 1 ;; esac
	printf 'migration-tool %s replay\n' "$operation" >>"$FAKE_COMMAND_LOG"
	printf '{"phase":"%s","replayed":true}\n' "$phase"
	exit 0
fi

case "$phase:$operation" in
	baseline:record-import) next=provenance-ready ;;
	provenance-ready:backup) next=backed-up ;;
	backed-up:restore) next=restored ;;
	restored:final-delta) next=delta-captured ;;
	delta-captured:fence) next=fenced ;;
	fenced:backfill) next=backfilled ;;
	backfilled:validate) next=validated ;;
	validated:prepare-activation) next=activation-ready ;;
	activation-ready:record-activation-evidence) next=activated ;;
	*) printf '%s is not legal from %s\n' "$operation" "$phase" >&2; exit 1 ;;
esac

if [ "$operation" = validate ]; then
	"$BT_G010_NPM" --prefix web run test:e2e:real || { printf 'real browser verification failed\n' >&2; exit 1; }
fi

case "$operation" in
	backup) mkdir -p "$(dirname "$artifact")"; printf 'backup-v1\n' >"$artifact" ;;
	final-delta) mkdir -p "$(dirname "$artifact")"; printf 'delta-v1\n' >"$artifact" ;;
esac
artifact_sha=-
[ "$artifact" = - ] || artifact_sha=$(hash_file "$artifact")
sequence=$(( $(wc -l <"$FAKE_LEDGER_FILE") + 1 ))
proof_sha=-
case "$operation" in
	prepare-activation) ;;
	record-activation-evidence)
		proof=$(arg_value --proof "$@")
		proof_sha=$(hash_file "$proof")
		;;
	*)
		proof="$FAKE_EVIDENCE_DIR/$(printf '%03d' "$sequence")-$operation.json"
		printf '{"schemaVersion":1,"operation":"%s","status":"pass","generatedBy":"migration-tool/executor-v1","artifactSha256":"%s"}\n' "$operation" "$artifact_sha" >"$proof"
		proof_sha=$(hash_file "$proof")
		;;
esac
printf '%s|%s|%s|%s\n' "$sequence" "$operation" "$artifact_sha" "$proof_sha" >>"$FAKE_LEDGER_FILE"
printf '%s\n' "$next" >"$FAKE_PHASE_FILE"
printf 'migration-tool %s advance\n' "$operation" >>"$FAKE_COMMAND_LOG"
if [ "${FAKE_INTERRUPT_AFTER:-}" = "$operation" ] && [ ! -f "$FAKE_INTERRUPT_MARKER" ]; then
	: >"$FAKE_INTERRUPT_MARKER"
	printf 'forced interruption after durable %s\n' "$operation" >&2
	exit 97
fi
printf '{"phase":"%s"}\n' "$next"
`
