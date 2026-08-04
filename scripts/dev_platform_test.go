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

func TestDevPlatformTestAndLiveStateAreIsolated(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	testEnv := launcherEnv(runtimeDir)
	output := runLauncher(t, repo, testEnv, "doctor")
	if !strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "funding-worker.token")) {
		t.Fatalf("test token path missing from doctor output: %s", output)
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
	if strings.Contains(output, filepath.Join(runtimeDir, "credentials", "test", "funding-worker.token")) {
		t.Fatalf("doctor reused test token in live mode: %s", output)
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
}

func launcherEnv(runtimeDir string) []string {
	return append(os.Environ(),
		"FT_LOCAL_RUNTIME_DIR="+runtimeDir,
		"FT_MODE=test",
		"BT_DEV_START_TIMEOUT_SECONDS=3",
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
