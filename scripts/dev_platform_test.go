package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	gatewayconfig "github.com/QuantProcessing/boltertrader/gateway/config"
)

func TestDevPlatformLifecycleIsOrderedAndDrainFirst(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	events := filepath.Join(runtimeDir, "events")
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
set -eu
name=$1
events=$2
printf 'start:%s\n' "$name" >>"$events"
touch "$events.$name.ready"
trap 'printf "stop:%s\n" "$name" >>"$events"; exit 0' TERM INT
while :; do sleep 1; done
`)

	env := launcherEnv(runtimeDir)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		name := strings.ToLower(strings.ReplaceAll(service, "_", "-"))
		env = append(env,
			"BT_DEV_"+service+"_CMD="+helper+" "+name+" "+events,
			"BT_DEV_"+service+"_READY_CMD=test -f "+events+"."+name+".ready",
		)
	}
	env = append(env, "BT_DEV_FUNDING_WORKER_DRAIN_CMD=printf 'drain:funding-worker\\n' >>"+events)
	env = append(env,
		"BT_DEV_DB_BOOTSTRAP_CMD=printf 'bootstrap:database\\n' >>"+events,
		"BT_DEV_SECRET_BOOTSTRAP_CMD=printf 'bootstrap:secret-store\\n' >>"+events,
	)

	runLauncher(t, repo, env, "up")
	manualKeyPath := filepath.Join(runtimeDir, "credentials", "test", "manual-preview-signing.key")
	manualKeyInfo, err := os.Lstat(manualKeyPath)
	if err != nil {
		t.Fatalf("Manual preview signing key was not created: %v", err)
	}
	if !manualKeyInfo.Mode().IsRegular() || manualKeyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("Manual preview signing key mode=%v, want regular 0600", manualKeyInfo.Mode())
	}
	runLauncher(t, repo, env, "status")
	runLauncher(t, repo, env, "down")

	got := readLines(t, events)
	want := []string{
		"start:postgres", "bootstrap:database", "bootstrap:secret-store", "start:gateway", "start:platform", "start:funding-worker", "start:web",
		"stop:web", "drain:funding-worker", "stop:funding-worker", "stop:platform", "stop:gateway", "stop:postgres",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lifecycle events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDevPlatformUpReusesReadyServices(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	events := filepath.Join(runtimeDir, "events")
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
set -eu
name=$1
events=$2
printf 'start:%s\n' "$name" >>"$events"
touch "$events.$name.ready"
trap 'printf "stop:%s\n" "$name" >>"$events"; exit 0' TERM INT
while :; do sleep 1; done
`)

	env := launcherEnv(runtimeDir)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		name := strings.ToLower(strings.ReplaceAll(service, "_", "-"))
		env = append(env,
			"BT_DEV_"+service+"_CMD="+helper+" "+name+" "+events,
			"BT_DEV_"+service+"_READY_CMD=test -f "+events+"."+name+".ready",
		)
	}
	env = append(env,
		"BT_DEV_FUNDING_WORKER_DRAIN_CMD=true",
		"BT_DEV_DB_BOOTSTRAP_CMD=printf 'bootstrap:database\\n' >>"+events,
		"BT_DEV_SECRET_BOOTSTRAP_CMD=printf 'bootstrap:secret-store\\n' >>"+events,
	)
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
		cmd.Dir, cmd.Env = repo, env
		_, _ = cmd.CombinedOutput()
	})

	runLauncher(t, repo, env, "up")
	initialPIDs := make(map[string]string)
	for _, service := range []string{"postgres", "gateway", "platform", "funding-worker", "web"} {
		payload, err := os.ReadFile(filepath.Join(runtimeDir, "state", "test", "pids", service+".pid"))
		if err != nil {
			t.Fatal(err)
		}
		initialPIDs[service] = strings.TrimSpace(string(payload))
	}

	runLauncher(t, repo, env, "up")
	for service, initialPID := range initialPIDs {
		payload, err := os.ReadFile(filepath.Join(runtimeDir, "state", "test", "pids", service+".pid"))
		if err != nil {
			t.Fatalf("second up stopped %s: %v", service, err)
		}
		if got := strings.TrimSpace(string(payload)); got != initialPID {
			t.Errorf("second up restarted %s: pid=%s, want %s", service, got, initialPID)
		}
	}

	got := readLines(t, events)
	want := []string{
		"start:postgres", "bootstrap:database", "bootstrap:secret-store", "start:gateway", "start:platform", "start:funding-worker", "start:web",
		"bootstrap:database", "bootstrap:secret-store",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events after repeated up:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDevPlatformAllowsOwnedCommandExecBeforeReadiness(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	ready := filepath.Join(runtimeDir, "postgres.ready")
	final := filepath.Join(runtimeDir, "final.sh")
	wrapper := filepath.Join(runtimeDir, "wrapper.sh")
	stable := filepath.Join(runtimeDir, "stable.sh")
	writeExecutable(t, final, `#!/bin/sh
set -eu
touch "$1"
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)
	writeExecutable(t, wrapper, `#!/bin/sh
set -eu
sleep 0.2
exec "$1" "$2"
`)
	writeExecutable(t, stable, `#!/bin/sh
set -eu
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)

	env := append(launcherEnv(runtimeDir),
		"BT_DEV_POSTGRES_CMD="+wrapper+" "+final+" "+ready,
		"BT_DEV_POSTGRES_READY_CMD=test -f "+ready,
		"BT_DEV_FUNDING_WORKER_DRAIN_CMD=true",
	)
	for _, service := range []string{"GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		env = append(env,
			"BT_DEV_"+service+"_CMD="+stable,
			"BT_DEV_"+service+"_READY_CMD=true",
		)
	}
	postgresPIDFile := filepath.Join(runtimeDir, "state", "test", "pids", "postgres.pid")
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
		cmd.Dir, cmd.Env = repo, env
		_, _ = cmd.CombinedOutput()
		payload, err := os.ReadFile(postgresPIDFile)
		if err != nil {
			return
		}
		var pid int
		if _, err := fmt.Sscanf(string(payload), "%d", &pid); err == nil && pid > 0 {
			if process, err := os.FindProcess(pid); err == nil {
				_ = process.Kill()
			}
		}
	})

	runLauncher(t, repo, env, "up")
	commandPath := filepath.Join(runtimeDir, "state", "test", "ownership", "postgres.command")
	command, err := os.ReadFile(commandPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(command), final) || strings.Contains(string(command), wrapper) {
		t.Fatalf("sealed postgres command %q does not identify final process %q", strings.TrimSpace(string(command)), final)
	}
	runLauncher(t, repo, env, "status")
	runLauncher(t, repo, env, "down")
}

func TestDevPlatformDoesNotRewriteIdentityWhenStartTimeChangesAfterCommandSample(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	process := exec.Command("sleep", "60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _, _ = process.Process.Wait() })

	startOutput, err := exec.Command("ps", "-p", fmt.Sprint(process.Process.Pid), "-o", "lstart=").Output()
	if err != nil {
		t.Fatal(err)
	}
	start := strings.TrimSpace(string(startOutput))
	modeDir := filepath.Join(runtimeDir, "state", "test")
	pidDir := filepath.Join(modeDir, "pids")
	ownershipDir := filepath.Join(modeDir, "ownership")
	for _, dir := range []string{pidDir, ownershipDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pidDir, "postgres.pid"), []byte(fmt.Sprintf("%d\n", process.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "postgres.start"), []byte(start+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(ownershipDir, "postgres.command")
	if err := os.WriteFile(commandPath, []byte("trusted-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	shimDir := filepath.Join(runtimeDir, "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	trigger := filepath.Join(runtimeDir, "command-sampled")
	emitted := filepath.Join(runtimeDir, "changed-start-emitted")
	psPath, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(shimDir, "ps"), fmt.Sprintf(`#!/bin/sh
set -eu
if [ "${1:-}" = "-p" ] && [ "${2:-}" = "%d" ] && [ "${3:-}" = "-o" ] && [ "${4:-}" = "command=" ]; then
	printf 'replacement-command\n'
	: >%q
	exit 0
fi
if [ "${1:-}" = "-p" ] && [ "${2:-}" = "%d" ] && [ "${3:-}" = "-o" ] && [ "${4:-}" = "lstart=" ] && [ -f %q ] && [ ! -f %q ]; then
	: >%q
	printf 'changed-process-start-time\n'
	exit 0
fi
exec %q "$@"
`, process.Process.Pid, trigger, process.Process.Pid, trigger, emitted, emitted, psPath))

	env := launcherEnv(runtimeDir)
	for index := range env {
		if strings.HasPrefix(env[index], "PATH=") {
			env[index] = "PATH=" + shimDir + ":" + strings.TrimPrefix(env[index], "PATH=")
		}
	}
	env = append(env, "BT_DEV_POSTGRES_READY_CMD=true")
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "wait")
	cmd.Dir, cmd.Env = repo, env
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("wait accepted a changed process identity: %s", output)
	}
	command, err := os.ReadFile(commandPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(command)); got != "trusted-command" {
		t.Fatalf("changed process rewrote command identity to %q", got)
	}
}

func TestDevPlatformWaitDoesNotRewriteSealedCommandIdentity(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	process := exec.Command("sleep", "60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Process.Kill(); _, _ = process.Process.Wait() })

	startOutput, err := exec.Command("ps", "-p", fmt.Sprint(process.Process.Pid), "-o", "lstart=").Output()
	if err != nil {
		t.Fatal(err)
	}
	modeDir := filepath.Join(runtimeDir, "state", "test")
	pidDir := filepath.Join(modeDir, "pids")
	ownershipDir := filepath.Join(modeDir, "ownership")
	for _, dir := range []string{pidDir, ownershipDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pidDir, "postgres.pid"), []byte(fmt.Sprintf("%d\n", process.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "postgres.start"), []byte(strings.TrimSpace(string(startOutput))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(ownershipDir, "postgres.command")
	if err := os.WriteFile(commandPath, []byte("sealed-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	shimDir := filepath.Join(runtimeDir, "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		t.Fatal(err)
	}
	psPath, err := exec.LookPath("ps")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(shimDir, "ps"), fmt.Sprintf(`#!/bin/sh
set -eu
if [ "${1:-}" = "-p" ] && [ "${2:-}" = "%d" ] && [ "${3:-}" = "-o" ] && [ "${4:-}" = "command=" ]; then
	printf 'post-readiness-command\n'
	exit 0
fi
exec %q "$@"
`, process.Process.Pid, psPath))

	env := launcherEnv(runtimeDir)
	for index := range env {
		if strings.HasPrefix(env[index], "PATH=") {
			env[index] = "PATH=" + shimDir + ":" + strings.TrimPrefix(env[index], "PATH=")
		}
	}
	env = append(env, "BT_DEV_POSTGRES_READY_CMD=true")
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "wait")
	cmd.Dir, cmd.Env = repo, env
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("wait accepted a changed sealed identity: %s", output)
	}
	command, err := os.ReadFile(commandPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(command)); got != "sealed-command" {
		t.Fatalf("public wait rewrote sealed command identity to %q", got)
	}
}

func TestDevPlatformDownStopsOwnedStartingProcessAfterCommandExec(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	process := exec.Command("sleep", "60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}

	waited := make(chan error, 1)
	go func() {
		_, err := process.Process.Wait()
		waited <- err
	}()
	t.Cleanup(func() { _ = process.Process.Kill() })
	startOutput, err := exec.Command("ps", "-p", fmt.Sprint(process.Process.Pid), "-o", "lstart=").Output()
	if err != nil {
		t.Fatal(err)
	}
	modeDir := filepath.Join(runtimeDir, "state", "test")
	pidDir := filepath.Join(modeDir, "pids")
	ownershipDir := filepath.Join(modeDir, "ownership")
	for _, dir := range []string{pidDir, ownershipDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pidDir, "postgres.pid"), []byte(fmt.Sprintf("%d\n", process.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "postgres.start"), []byte(strings.TrimSpace(string(startOutput))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "postgres.command"), []byte("pre-exec-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "postgres.starting"), []byte("starting\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runLauncher(t, repo, launcherEnv(runtimeDir), "down")
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		_ = process.Process.Kill()
		<-waited
		t.Fatal("owned starting process survived rollback cleanup")
	}
}

func TestDevPlatformBootstrapTargetsManagedPostgres(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	observed := filepath.Join(runtimeDir, "bootstrap-postgres-target")
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
set -eu
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)

	env := append(launcherEnv(runtimeDir),
		"PGHOST=ambient.invalid",
		"PGPORT=6543",
		`BT_DEV_DB_BOOTSTRAP_CMD=printf '%s:%s\n' "$PGHOST" "$PGPORT" >`+observed,
		"BT_DEV_FUNDING_WORKER_DRAIN_CMD=true",
	)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		env = append(env,
			"BT_DEV_"+service+"_CMD="+helper,
			"BT_DEV_"+service+"_READY_CMD=true",
		)
	}
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
		cmd.Dir, cmd.Env = repo, env
		_, _ = cmd.CombinedOutput()
	})

	runLauncher(t, repo, env, "up")
	target, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(target), "127.0.0.1:15432\n"; got != want {
		t.Fatalf("database bootstrap target = %q, want %q", got, want)
	}
	runLauncher(t, repo, env, "down")
}

func TestDevPlatformBuiltinFundingWorkerReadinessUsesWorkerFenceAndFailClosedHTTP(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	credentialDir := filepath.Join(runtimeDir, "credentials", "test")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const fundingToken = "synthetic-funding-worker-readiness-token"
	const gatewayToken = "synthetic-gateway-adapter-readiness-token"
	if err := os.WriteFile(filepath.Join(credentialDir, "funding-worker.token"), []byte(fundingToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(credentialDir, "gateway-adapter.token"), []byte(gatewayToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(runtimeDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(runtimeDir, "curl.requests")
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
set -eu
config=$(cat)
printf 'request=%s\nconfig=%s\n' "$*" "$config" >>"$BT_TEST_CURL_REQUESTS"
case "$*" in
  *--fail*) ;;
  *) exit 22 ;;
esac
case "$*" in
  *'/internal/v1/funding-work/workers/registrations'*) printf '{"data":{"workerGeneration":7}}\n' ;;
esac
`)
	writeExecutable(t, filepath.Join(binDir, "pg_isready"), "#!/bin/sh\nexit 0\n")
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)

	env := append(launcherEnv(runtimeDir),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BT_TEST_CURL_REQUESTS="+requests,
		"BT_DEV_FUNDING_WORKER_DRAIN_CMD=true",
		"BT_DEV_START_TIMEOUT_SECONDS=1",
	)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		env = append(env, "BT_DEV_"+service+"_CMD="+helper, "BT_DEV_"+service+"_READY_CMD=")
	}
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
		cmd.Dir = repo
		cmd.Env = env
		_, _ = cmd.CombinedOutput()
	})

	runLauncher(t, repo, env, "up")
	requestLog, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"/internal/v1/funding-work/workers/registrations",
		"/internal/v1/funding-work/workers/funding-default-test/readiness",
		`header = "X-Worker-Id: funding-default-test"`,
		`header = "X-Worker-Generation: 7"`,
		`header = "X-Worker-Instance-Id: `,
	} {
		if !strings.Contains(string(requestLog), required) {
			t.Errorf("supervisor did not use worker-fenced readiness value %q:\n%s", required, requestLog)
		}
	}
	for _, required := range []string{
		"request=--fail --silent --show-error --config - http://127.0.0.1:18082/internal/v1/runtime/status\nconfig=oauth2-bearer = \"" + gatewayToken + "\"",
		"request=--fail --silent --show-error --config - http://127.0.0.1:18082/internal/v1/runtime/readiness\nconfig=oauth2-bearer = \"" + gatewayToken + "\"",
		"oauth2-bearer = \"" + fundingToken + "\"",
	} {
		if !strings.Contains(string(requestLog), required) {
			t.Errorf("supervisor readiness used the wrong bearer token; missing %q:\n%s", required, requestLog)
		}
	}
}

func TestDevPlatformWillNotStopProcessWithMismatchedOwnership(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })

	modeDir := filepath.Join(runtimeDir, "state", "test")
	for _, dir := range []string{"pids", "ownership"} {
		if err := os.MkdirAll(filepath.Join(modeDir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(modeDir, "pids", "web.pid"), []byte(fmt.Sprintf("%d\n", cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "ownership", "web.start"), []byte("not-the-process-start-time\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modeDir, "ownership", "web.command"), []byte("not-the-process-command\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	runLauncher(t, repo, launcherEnv(runtimeDir), "down")
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unowned process was stopped: %v", err)
	}
}

func TestDevPlatformRollsBackPartialStartup(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)
	env := launcherEnv(runtimeDir)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		env = append(env,
			"BT_DEV_"+service+"_CMD="+helper,
			"BT_DEV_"+service+"_READY_CMD=true",
		)
	}
	env = append(env,
		"BT_DEV_GATEWAY_READY_CMD=false",
		"BT_DEV_FUNDING_WORKER_DRAIN_CMD=true",
		"BT_DEV_START_TIMEOUT_SECONDS=1",
	)
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "up")
	cmd.Dir = repo
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("partial startup unexpectedly succeeded: %s", out)
	}
	pidFile := filepath.Join(runtimeDir, "state", "test", "pids", "postgres.pid")
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Fatalf("partial startup left postgres ownership behind: %v", err)
	}
}

func TestDevPlatformDrainTimeoutIsBoundedAndStillStopsOwnedProcesses(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	helper := filepath.Join(runtimeDir, "service.sh")
	drain := filepath.Join(runtimeDir, "drain.sh")
	writeExecutable(t, helper, `#!/bin/sh
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)
	writeExecutable(t, drain, `#!/bin/sh
trap '' TERM
while :; do sleep 1; done
`)
	env := launcherEnv(runtimeDir)
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		env = append(env, "BT_DEV_"+service+"_CMD="+helper, "BT_DEV_"+service+"_READY_CMD=true")
	}
	env = append(env, "BT_DEV_FUNDING_WORKER_DRAIN_CMD=true")
	runLauncher(t, repo, env, "up")

	downEnv := append(env, "BT_DEV_FUNDING_WORKER_DRAIN_CMD="+drain, "BT_DEV_DRAIN_TIMEOUT_SECONDS=1")
	started := time.Now()
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
	cmd.Dir = repo
	cmd.Env = downEnv
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("timed-out drain unexpectedly reported success: %s", out)
	}
	if elapsed := time.Since(started); elapsed > 8*time.Second {
		t.Fatalf("drain timeout was not bounded: %s", elapsed)
	}
	for _, service := range []string{"postgres", "gateway", "platform", "funding-worker", "web"} {
		pidFile := filepath.Join(runtimeDir, "state", "test", "pids", service+".pid")
		if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
			t.Errorf("%s pid remained after drain timeout: %v", service, err)
		}
	}
}

func TestDevPlatformContractAndExamples(t *testing.T) {
	repo := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repo, "scripts", "dev-platform"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	for _, required := range []string{
		"15432", "18082", "18091", "18092", "13001",
		"postgres gateway platform funding-worker web", "web funding-worker platform gateway postgres",
		"./backend/scripts/db-bootstrap.sh", "./backend/scripts/secret-bootstrap.sh",
		"http://127.0.0.1:18082/internal/v1/runtime/readiness",
		"http://127.0.0.1:18091/api/v1/system/readiness",
		"http://127.0.0.1:18092/internal/v1/funding-work/status",
		"oauth2-bearer",
		`BT_PLATFORM_WORK_URL="${BT_PLATFORM_WORK_URL:-http://127.0.0.1:18092}"`,
		`WORKER_ID=${BT_FUNDING_WORKER_ID:-funding-default-$MODE}`,
		`BT_MANUAL_PREVIEW_SIGNING_KEY_FILE`,
		`BT_SECRET_KEY_SOURCE`, `BT_SECRET_STORE_DIR`, `BT_SECRET_STORE_NAMESPACE`,
		`BT_SECRET_MASTER_KEY_VERSION`, `boltertrader-product-platform-upgrade/v5`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("launcher is missing frozen contract value %q", required)
		}
	}

	for _, name := range []string{"backend/.env.example", "gateway/.env.example", "web/.env.example"} {
		contents, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(contents))
		for _, forbidden := range []string{"password=", "secret=", "token=", "private_key="} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("%s contains inline secret-shaped assignment %q", name, forbidden)
			}
		}
	}
}

func TestCanonicalDevPlatformCommandsAreCompleteAndNonSecret(t *testing.T) {
	repo := repoRoot(t)
	templatePath := filepath.Join(repo, "scripts", "dev-platform.env.example")
	payload, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	template := string(payload)
	for _, name := range []string{
		"BT_DEV_POSTGRES_CMD", "BT_DEV_GATEWAY_CMD", "BT_DEV_PLATFORM_CMD",
		"BT_DEV_FUNDING_WORKER_CMD", "BT_DEV_WEB_CMD", "BT_DEV_FUNDING_WORKER_DRAIN_CMD",
		"BT_SECRET_KEY_SOURCE", "BT_SECRET_STORE_DIR", "BT_SECRET_STORE_NAMESPACE",
		"BT_SECRET_MASTER_KEY_VERSION", "BT_SECRET_MASTER_KEY_FILE",
	} {
		if !strings.Contains(template, "export "+name+"=") {
			t.Errorf("canonical template is missing %s", name)
		}
	}
	lower := strings.ToLower(template)
	for _, forbidden := range []string{"docker compose", "docker run", "docker build", "password=", "secret=", "token=", "private_key=", "bolter_enable_", "live_private_writes"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("canonical template contains forbidden value %q", forbidden)
		}
	}

	launcher, err := os.ReadFile(filepath.Join(repo, "scripts", "dev-platform-service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entrypoint := range []string{"./cmd/exchange-gateway", "cd backend", "./cmd/platform-api", "./cmd/funding-worker", "web/server.mjs"} {
		if !strings.Contains(string(launcher), entrypoint) {
			t.Errorf("service launcher is missing real entrypoint %q", entrypoint)
		}
	}

	gatewayPayload, err := os.ReadFile(filepath.Join(repo, "gateway", "dev-platform.test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	gatewayFile, err := gatewayconfig.LoadYAML(gatewayPayload)
	if err != nil {
		t.Fatalf("canonical Gateway config is invalid: %v", err)
	}
	if gatewayFile.Server.Mode != "test" || gatewayFile.Server.InternalListen != "127.0.0.1:18082" {
		t.Fatalf("canonical Gateway config is not a write-free Test config: %+v", gatewayFile)
	}
	if strings.Contains(string(gatewayPayload), "accounts:") {
		t.Fatal("canonical Gateway config contains removed static account inventory")
	}
}

func TestProductPlatformMakeGatesAreExplicitAndPhony(t *testing.T) {
	repo := repoRoot(t)
	payload, err := os.ReadFile(filepath.Join(repo, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(payload)
	for _, required := range []string{
		"test-browser test-real",
		"test-browser:\n\tnpm --prefix web ci\n\tnpm --prefix web run build\n\tnpm --prefix web test\n\tnpm --prefix web run test:e2e",
		"test-real:\n\t./scripts/test-real-platform",
	} {
		if !strings.Contains(makefile, required) {
			t.Errorf("Makefile is missing gate contract %q", required)
		}
	}
}

func TestRealPlatformGateFailsClosedBeforeReadiness(t *testing.T) {
	repo := repoRoot(t)
	script := filepath.Join(repo, "scripts", "test-real-platform")
	for name, env := range map[string][]string{
		"missing attestation": append(os.Environ(), "FT_MODE=test"),
		"wrong mode":          append(os.Environ(), "BT_DEV_REAL_STACK_ACCEPTANCE=1", "FT_MODE=live"),
		"venue writes":        append(os.Environ(), "BT_DEV_REAL_STACK_ACCEPTANCE=1", "FT_MODE=test", "BOLTER_ENABLE_BINANCE_DEMO_WRITES=1"),
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(script)
			cmd.Dir = repo
			cmd.Env = env
			if output, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("gate unexpectedly passed: %s", output)
			}
		})
	}
}

func TestCanonicalDrainUsesWorkerFenceAndAuthenticatedRequest(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	credentialDir := filepath.Join(runtimeDir, "credentials", "test")
	ownershipDir := filepath.Join(runtimeDir, "state", "test", "ownership")
	binDir := filepath.Join(runtimeDir, "bin")
	for _, dir := range []string{credentialDir, ownershipDir, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(credentialDir, "funding-worker.token"), []byte("synthetic-drain-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownershipDir, "funding-worker.instance"), []byte("instance-drain\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(runtimeDir, "requests")
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
set -eu
config=$(cat)
printf 'args=%s\nconfig=%s\n' "$*" "$config" >>"$BT_TEST_REQUESTS"
case "$*" in
  *'/workers/registrations'*) printf '{"data":{"workerGeneration":9}}\n' ;;
esac
`)
	env := append(launcherEnv(runtimeDir),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BT_TEST_REQUESTS="+requests,
	)
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "drain-funding-worker")
	cmd.Dir, cmd.Env = repo, env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("drain command failed: %v\n%s", err, output)
	}
	payload, err := os.ReadFile(requests)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, required := range []string{
		"/workers/registrations", "/workers/funding-default-test/drain",
		"Idempotency-Key: supervisor-drain:funding-default-test:instance-drain:9",
		"X-Worker-Generation: 9", `oauth2-bearer = "synthetic-drain-token"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("drain request is missing %q:\n%s", required, text)
		}
	}
}

func TestDockerFreeOperatorGuideIsLinkedAndPreservesSafetyBoundary(t *testing.T) {
	repo := repoRoot(t)
	index, err := os.ReadFile(filepath.Join(repo, "docs", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "guides/docker-free-local-platform.md") {
		t.Fatal("documentation index does not link the Docker-free operator guide")
	}
	guide, err := os.ReadFile(filepath.Join(repo, "docs", "guides", "docker-free-local-platform.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"dev-platform.env.example", "make test-browser", "make test-real", "does not activate migration", "no venue accounts", "G010", "G011", "zero-account inventory", "Secret Store"} {
		if !strings.Contains(string(guide), required) {
			t.Errorf("operator guide is missing %q", required)
		}
	}
}

func TestLauncherHelpAndRealGateDocumentExactG010Sequence(t *testing.T) {
	repo := repoRoot(t)
	launcher, err := os.ReadFile(filepath.Join(repo, "scripts", "dev-platform"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"help)", ". ./scripts/dev-platform.env.example", "make dev-platform-doctor", "make dev-platform-up", "make dev-platform-status", "BT_DEV_REAL_STACK_ACCEPTANCE=1 make test-real", "G010 no-trade", "G011 Test-write"} {
		if !strings.Contains(string(launcher), want) {
			t.Errorf("launcher help missing %q", want)
		}
	}
	realGate, err := os.ReadFile(filepath.Join(repo, "scripts", "test-real-platform"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(realGate)
	if strings.Count(text, "http://127.0.0.1:18091/api/v1/system/readiness") < 2 || strings.Count(text, "http://127.0.0.1:13001/") < 2 {
		t.Fatalf("real gate does not repeat readiness/browser reads: %s", text)
	}
	for _, want := range []string{"G010", "no-trade", "G011", "Test-write", "no venue writes"} {
		if !strings.Contains(text, want) {
			t.Errorf("real gate missing %q", want)
		}
	}
}

func TestDevPlatformPassesCompletePlatformEnvironment(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	credentialDir := filepath.Join(runtimeDir, "credentials", "test")
	if err := os.MkdirAll(credentialDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const secretKeyCanary = "abababababababababababababababababababababababababababababababab"
	if err := os.WriteFile(filepath.Join(credentialDir, "secret-store-master.key"), []byte(secretKeyCanary+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output := runLauncher(t, repo, launcherEnv(runtimeDir), "doctor"); strings.Contains(output, secretKeyCanary) {
		t.Fatal("doctor output contains Secret Store key material")
	}
	environment := filepath.Join(runtimeDir, "platform.env")
	helper := filepath.Join(runtimeDir, "service.sh")
	writeExecutable(t, helper, `#!/bin/sh
set -eu
name=$1
if [ "$name" = platform ]; then env | sort >"$BT_TEST_PLATFORM_ENV"; fi
trap 'exit 0' TERM INT
while :; do sleep 1; done
`)
	env := append(launcherEnv(runtimeDir), "BT_TEST_PLATFORM_ENV="+environment, "BT_DEV_FUNDING_WORKER_DRAIN_CMD=true")
	for _, service := range []string{"POSTGRES", "GATEWAY", "PLATFORM", "FUNDING_WORKER", "WEB"} {
		name := strings.ToLower(strings.ReplaceAll(service, "_", "-"))
		ready := "true"
		if service == "PLATFORM" {
			ready = "test -f " + environment
		}
		env = append(env, "BT_DEV_"+service+"_CMD="+helper+" "+name, "BT_DEV_"+service+"_READY_CMD="+ready)
	}
	t.Cleanup(func() {
		cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "down")
		cmd.Dir, cmd.Env = repo, env
		_, _ = cmd.CombinedOutput()
	})
	runLauncher(t, repo, env, "up")
	contents, err := os.ReadFile(environment)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		"BT_MODE=test", "FT_MODE=test", "BT_PLATFORM_DATABASE=bolter_platform_test", "BT_PLATFORM_DATABASE_ROLE=bolter_platform_test",
		"BT_MANUAL_PREVIEW_SIGNING_KEY_FILE=" + filepath.Join(runtimeDir, "credentials", "test", "manual-preview-signing.key"),
		"BT_SECRET_KEY_SOURCE=file", "BT_SECRET_STORE_DIR=" + filepath.Join(runtimeDir, "secret-store"),
		"BT_SECRET_STORE_NAMESPACE=boltertrader/test", "BT_SECRET_MASTER_KEY_VERSION=v1",
		"BT_SECRET_MASTER_KEY_FILE=" + filepath.Join(runtimeDir, "credentials", "test", "secret-store-master.key"),
	} {
		if !strings.Contains(text, required+"\n") {
			t.Errorf("Platform environment missing %q", required)
		}
	}
	if strings.Contains(text, secretKeyCanary) {
		t.Fatal("Platform process environment contains Secret Store key material")
	}
}

func TestDevPlatformResetRemovesOnlyCurrentModeSecretStoreState(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	testState := filepath.Join(runtimeDir, "secret-store", "test", "objects")
	liveState := filepath.Join(runtimeDir, "secret-store", "live", "objects")
	for _, dir := range []string{testState, liveState} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runLauncher(t, repo, launcherEnv(runtimeDir), "reset")
	if _, err := os.Stat(filepath.Join(runtimeDir, "secret-store", "test")); !os.IsNotExist(err) {
		t.Fatalf("Test Secret Store state survived reset: %v", err)
	}
	if _, err := os.Stat(liveState); err != nil {
		t.Fatalf("Test reset removed Live Secret Store state: %v", err)
	}
}

func TestDevPlatformTestAndLiveStateAreIsolated(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	testEnv := launcherEnv(runtimeDir)
	output := runLauncher(t, repo, testEnv, "doctor")
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "funding-worker.token")) {
		t.Fatalf("test token path missing from doctor output: %s", output)
	}
	if !strings.Contains(output, "worker-id=funding-default-test") {
		t.Fatalf("test worker ID is not mode-scoped: %s", output)
	}
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "gateway-adapter.token")) || !strings.Contains(output, "gateway-url=http://127.0.0.1:18082") {
		t.Fatalf("Gateway adapter contract missing from doctor output: %s", output)
	}
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "manual-preview-signing.key")) {
		t.Fatalf("test Manual preview signing key path missing from doctor output: %s", output)
	}

	liveEnvFile := filepath.Join(runtimeDir, "live.env")
	if err := os.WriteFile(liveEnvFile, []byte("BT_LIVE_PRIVATE_WRITES_ENABLED=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	liveEnv := append(launcherEnv(runtimeDir), "FT_MODE=live", "BT_DEV_LIVE_ENV_FILE="+liveEnvFile)
	output = runLauncher(t, repo, liveEnv, "doctor")
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "live", "funding-worker.token")) {
		t.Fatalf("live token path missing from doctor output: %s", output)
	}
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "live", "manual-preview-signing.key")) || strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "manual-preview-signing.key")) {
		t.Fatalf("live Manual preview signing key is not mode-scoped: %s", output)
	}
	if strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "funding-worker.token")) {
		t.Fatalf("doctor reused test token in live mode: %s", output)
	}
	if !strings.Contains(output, "worker-id=funding-default-live") {
		t.Fatalf("live worker ID is not mode-scoped: %s", output)
	}
	if !strings.Contains(output, "live-private-writes=0") {
		t.Fatalf("live private writes were not default-denied: %s", output)
	}

	sharedTokenEnv := append(liveEnv, "BT_FUNDING_WORKER_TOKEN_FILE="+filepath.Join(runtimeDir, "credentials", "test", "funding-worker.token"))
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "doctor")
	cmd.Dir = repo
	cmd.Env = sharedTokenEnv
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("live mode accepted the test token path: %s", out)
	}

	crossModeManualKeyEnv := append(liveEnv, "BT_MANUAL_PREVIEW_SIGNING_KEY_FILE="+filepath.Join(runtimeDir, "credentials", "test", "manual-preview-signing.key"))
	cmd = exec.Command(filepath.Join(repo, "scripts", "dev-platform"), "doctor")
	cmd.Dir = repo
	cmd.Env = crossModeManualKeyEnv
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("live mode accepted the test Manual preview signing key path: %s", out)
	}
}

func launcherEnv(runtimeDir string) []string {
	return append(os.Environ(),
		"FT_LOCAL_RUNTIME_DIR="+runtimeDir,
		"FT_MODE=test",
		"BT_DEV_START_TIMEOUT_SECONDS=10",
		"BT_DEV_STOP_TIMEOUT_SECONDS=3",
		"BT_DEV_DRAIN_TIMEOUT_SECONDS=3",
		"BT_DEV_PORT_CHECK_CMD=true",
		"BT_DEV_TEST_HOOKS=1",
		"BT_DEV_DB_BOOTSTRAP_CMD=true",
		"BT_DEV_SECRET_BOOTSTRAP_CMD=true",
	)
}

func runLauncher(t *testing.T, repo string, env []string, arg string) string {
	t.Helper()
	cmd := exec.Command(filepath.Join(repo, "scripts", "dev-platform"), arg)
	cmd.Dir = repo
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dev-platform %s: %v\n%s", arg, err, out)
	}
	return string(out)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := os.ReadFile(path)
		if err == nil {
			return strings.Fields(string(contents))
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
