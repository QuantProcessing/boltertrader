# Docker-Free Startup Identity Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Allow a launcher-owned wrapper to `exec` its final service process before readiness without weakening strict post-readiness process ownership.

**Architecture:** Add one startup-only identity refresh helper to the POSIX shell supervisor. It preserves PID and start-time invariants, atomically updates the provisional command, and is called only from readiness polling; all normal ownership consumers remain strict.

**Tech Stack:** POSIX shell, Go `testing`, Unix process inspection through `ps`.

---

### Task 1: Reproduce The Wrapper-To-Service Exec Race

**Files:**
- Modify: `scripts/dev_platform_test.go`
- Test: `scripts/dev_platform_test.go`

- [ ] **Step 1: Write the failing lifecycle regression**

Add `TestDevPlatformAllowsOwnedCommandExecBeforeReadiness`. Create a wrapper
script that sleeps for 200 ms and then executes a final script. The final
script creates the PostgreSQL readiness marker and waits for `TERM`. Configure
the other four services with the existing stable helper and immediate custom
readiness. Assert that `up`, `status`, and `down` succeed and that
`state/test/ownership/postgres.command` names the final script rather than the
wrapper.

```go
func TestDevPlatformAllowsOwnedCommandExecBeforeReadiness(t *testing.T) {
	repo := repoRoot(t)
	runtimeDir := t.TempDir()
	ready := filepath.Join(runtimeDir, "postgres.ready")
	final := filepath.Join(runtimeDir, "final.sh")
	wrapper := filepath.Join(runtimeDir, "wrapper.sh")
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
	// Configure postgres with wrapper + marker readiness and other services
	// with the existing stable helper, then assert up/status/down and the
	// sealed command record.
}
```

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./scripts -run TestDevPlatformAllowsOwnedCommandExecBeforeReadiness -count=1 -v
```

Expected: FAIL because `postgres` becomes "not an owned running process" after
the wrapper executes the final process.

- [ ] **Step 3: Commit the regression only after observing RED**

```bash
git add scripts/dev_platform_test.go
git commit -m "test: reproduce dev platform exec startup race"
```

### Task 2: Reconcile Identity Only During Startup

**Files:**
- Modify: `scripts/dev-platform`
- Test: `scripts/dev_platform_test.go`

- [ ] **Step 1: Add an atomic command-record writer**

Add a helper that writes the observed command to a same-directory temporary
file and renames it over the provisional record:

```sh
write_command_identity() {
	service=$1
	command=$2
	command_file="$OWNERSHIP_DIR/$service.command"
	temporary="$command_file.tmp.$$"
	printf '%s\n' "$command" >"$temporary"
	mv "$temporary" "$command_file"
}
```

- [ ] **Step 2: Add the startup-only refresh**

The helper must validate the existing PID record, process liveness, exact
recorded start time, and a non-empty current command before calling
`write_command_identity`. It must never be called from `is_owned`, `status`,
port checks, drain, or shutdown.

```sh
refresh_starting_identity() {
	service=$1
	pid_file="$PID_DIR/$service.pid"
	start_file="$OWNERSHIP_DIR/$service.start"
	[ -f "$pid_file" ] && [ -f "$start_file" ] || return 1
	pid=$(sed -n '1p' "$pid_file")
	case "$pid" in *[!0-9]*|'') return 1 ;; esac
	kill -0 "$pid" 2>/dev/null || return 1
	[ "$(process_start_time "$pid")" = "$(sed -n '1p' "$start_file")" ] || return 1
	observed_command=$(process_command "$pid")
	[ -n "$observed_command" ] || return 1
	write_command_identity "$service" "$observed_command"
}
```

- [ ] **Step 3: Seal identity at readiness**

Use the atomic writer for the initial provisional command. In `wait_service`,
replace the pre-readiness strict check with `refresh_starting_identity`. When
readiness succeeds, refresh once more and immediately require `is_owned`
before returning ready.

- [ ] **Step 4: Verify GREEN and strict ownership regression**

Run:

```bash
go test ./scripts -run 'TestDevPlatform(AllowsOwnedCommandExecBeforeReadiness|WillNotStopProcessWithMismatchedOwnership)' -count=1 -v
go test ./scripts -count=3
sh -n scripts/dev-platform scripts/dev-platform-service
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 5: Commit the minimal implementation**

```bash
git add scripts/dev-platform
git commit -m "fix: reconcile dev platform startup identity"
```

### Task 3: Real-Stack And Independent Verification

**Files:**
- Verify only: `scripts/dev-platform`, `scripts/dev_platform_test.go`

- [ ] **Step 1: Launch and inspect the Docker-free Test stack**

Run the canonical non-secret environment template, then `doctor`, `up`,
`status`, and `wait`. Confirm fixed loopback listeners and service readiness.

- [ ] **Step 2: Run G009 acceptance gates**

Run the real PostgreSQL store/migration/restart suites, the attested
`make test-real` Test-mode gate, `make test-browser`, full Go tests, `go vet`,
shell syntax, formatting, and `git diff --check`. Also rerun `make test-real`
without its attestation and confirm it refuses to run.

- [ ] **Step 3: Run Team independent review**

Launch OMX Team with separate verification and review lanes. Require terminal
task state with zero failures and an explicit independent `APPROVE`, then shut
the team down and preserve its evidence for the leader-owned Ultragoal ledger.

- [ ] **Step 4: Normalize and checkpoint G009**

Preserve a backup ref, normalize G009 history using `git commit-tree` and a
compare-and-swap `git update-ref` without reset, rerun verification against the
normalized tree, obtain a fresh active Codex goal snapshot, and checkpoint
`G009-complete-docker-free-launch-and-migr` with evidence referencing
`.omx/ultragoal`.

