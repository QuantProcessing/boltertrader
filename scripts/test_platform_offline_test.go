package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var platformOfflineCommands = []string{
	"make test-gateway-contract",
	"make test-gateway-offline",
	"make test-gateway-race",
	"make test-gateway-security",
	"go test ./cmd/account-route-backfill ./scripts -count=1",
	"(cd backend && go test ./... && go test -race ./... && go vet ./...)",
	"npm --prefix web run build",
	"npm --prefix web test -- --run",
	"npm --prefix web run test:e2e",
}

func TestPlatformOfflineScriptHasFrozenCommandOrderAndNoEnvironmentSourcing(t *testing.T) {
	repo := platformOfflineRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repo, "scripts", "test-platform-offline"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#!/bin/sh\nset -eu\n") {
		t.Fatalf("script must begin with the POSIX shell and set -eu:\n%s", text)
	}
	for _, forbidden := range []string{"source ", ". .env", ". ./scripts/", "dev-platform.env"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("script sources environment through %q", forbidden)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ". ") {
			t.Errorf("script contains a shell source command: %q", line)
		}
	}
	previous := -1
	for _, command := range platformOfflineCommands {
		if count := strings.Count(text, command); count != 1 {
			t.Errorf("command %q occurs %d times, want once", command, count)
			continue
		}
		position := strings.Index(text, command)
		if position <= previous {
			t.Errorf("command %q is out of order", command)
		}
		previous = position
	}
}

func TestPlatformOfflineScriptExecutesFrozenCommands(t *testing.T) {
	logPath, output, err := runPlatformOffline(t, nil)
	if err != nil {
		t.Fatalf("offline gate failed: %v\n%s", err, output)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"make test-gateway-contract",
		"make test-gateway-offline",
		"make test-gateway-race",
		"make test-gateway-security",
		"go test ./cmd/account-route-backfill ./scripts -count=1",
		"go[backend] test ./...",
		"go[backend] test -race ./...",
		"go[backend] vet ./...",
		"npm --prefix web run build",
		"npm --prefix web test -- --run",
		"npm --prefix web run test:e2e",
	}
	if got := strings.TrimSpace(string(data)); got != strings.Join(want, "\n") {
		t.Fatalf("commands:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestPlatformOfflineScriptRejectsUnsafeEnvironmentBeforeCommands(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"RUN_FULL", "1"},
		{"RUN_FULL", ""},
		{"FT_MODE", "live"},
		{"BT_FUNDING_LIVE_ENABLED", "1"},
		{"BOLTER_ENABLE_LIVE_READ_TESTS", "1"},
		{"BOLTER_ENABLE_BINANCE_DEMO_WRITES", "1"},
		{"BT_DEV_REAL_STACK_ACCEPTANCE", "1"},
		{"BT_DEV_ENABLE_LIVE_PRIVATE_WRITES", "1"},
		{"BT_LIVE_PRIVATE_WRITES_ENABLED", "1"},
		{"BINANCE_ENABLE_LIVE_WRITE_TESTS", "1"},
		{"TESTENV_ENABLE_LIVE_WRITE", "1"},
		{"FUTURE_LIVE_WRITE_OPT_IN", "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			logPath, output, err := runPlatformOffline(t, []string{test.name + "=" + test.value})
			if err == nil {
				t.Fatalf("offline gate accepted %s: %s", test.name, output)
			}
			if !strings.Contains(output, test.name) {
				t.Fatalf("offline gate did not identify %s: %s", test.name, output)
			}
			if data, readErr := os.ReadFile(logPath); readErr == nil && len(data) != 0 {
				t.Fatalf("offline gate ran commands before rejecting %s:\n%s", test.name, data)
			}
		})
	}
}

func TestPlatformOfflineMakeTargetInvokesOnlyTheScript(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(platformOfflineRepoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !phonyTargetDeclared(text, "test-platform-offline") {
		t.Fatal("test-platform-offline is not declared phony")
	}
	if block := makeTargetBlock(text, "test-platform-offline"); block != "test-platform-offline:\n\t./scripts/test-platform-offline" {
		t.Fatalf("test-platform-offline target = %q", block)
	}
}

func runPlatformOffline(t *testing.T, extraEnv []string) (string, string, error) {
	t.Helper()
	repo := platformOfflineRepoRoot(t)
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "commands.log")
	helper := `#!/bin/sh
set -eu
name=${0##*/}
if [ "$name" = go ] && [ "${PWD##*/}" = backend ]; then name='go[backend]'; fi
printf '%s %s\n' "$name" "$*" >>"$PLATFORM_OFFLINE_TEST_LOG"
`
	for _, name := range []string{"make", "go", "npm"} {
		writePlatformOfflineExecutable(t, filepath.Join(binDir, name), helper)
	}
	command := exec.Command(filepath.Join(repo, "scripts", "test-platform-offline"))
	command.Dir = repo
	command.Env = append([]string{
		"PATH=" + binDir + ":/usr/bin:/bin",
		"PLATFORM_OFFLINE_TEST_LOG=" + logPath,
	}, extraEnv...)
	output, err := command.CombinedOutput()
	return logPath, string(output), err
}

func phonyTargetDeclared(makefile, target string) bool {
	for _, line := range strings.Split(makefile, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == ".PHONY:" {
			for _, candidate := range fields[1:] {
				if candidate == target {
					return true
				}
			}
		}
	}
	return false
}

func platformOfflineRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(dir)
}

func writePlatformOfflineExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func makeTargetBlock(makefile, target string) string {
	start := strings.Index(makefile, target+":")
	if start < 0 {
		return ""
	}
	rest := makefile[start:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		return strings.TrimSpace(rest[:end])
	}
	return strings.TrimSpace(rest)
}
