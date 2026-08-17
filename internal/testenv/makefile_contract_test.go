package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRepositoryGitignoreProtectsEnvironmentCredentials(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("read repository .gitignore: %v", err)
	}

	positions := map[string]int{
		".env":          -1,
		".env.*":        -1,
		"!.env.example": -1,
	}
	for lineNumber, line := range strings.Split(string(data), "\n") {
		rule := strings.TrimSpace(line)
		if _, tracked := positions[rule]; tracked {
			positions[rule] = lineNumber
		}
	}
	for rule, position := range positions {
		if position < 0 {
			t.Errorf("repository .gitignore is missing %q", rule)
		}
	}
	if positions["!.env.example"] < positions[".env.*"] {
		t.Error("!.env.example must follow .env.* so the shareable template remains unignored")
	}
}

func TestExchangeOfflineTargetIsPhony(t *testing.T) {
	makefile := readRepoMakefile(t)
	phony := make(map[string]bool)
	for _, line := range strings.Split(makefile, "\n") {
		if !strings.HasPrefix(line, ".PHONY:") {
			continue
		}
		for _, target := range strings.Fields(strings.TrimPrefix(line, ".PHONY:")) {
			phony[target] = true
		}
	}
	if !phony["test-exchange-offline"] {
		t.Error("Makefile .PHONY declarations are missing test-exchange-offline")
	}
}

func TestExchangeOfflineRecipeRunsShortSuite(t *testing.T) {
	makefile := readRepoMakefile(t)
	const want = "test-exchange-offline:\n\tgo test -short ./exchange/... -count=1"
	if got := makeTargetBlock(t, makefile, "test-exchange-offline"); got != want {
		t.Fatalf("test-exchange-offline recipe mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestExchangeQualityRecipesRunRaceAndRedactionGates(t *testing.T) {
	makefile := readRepoMakefile(t)
	for target, want := range map[string]string{
		"test-exchange-race":      "test-exchange-race:\n\tgo test -race ./exchange/... -count=1",
		"test-exchange-redaction": "test-exchange-redaction:\n\tgo test -short ./exchange/... -run 'Test(ConfigFormattingRedactsCredentials|ConstructedClientFormattingRedactsCredentials|ExpandedConstructedClientFormattingRedactsCredentials|AllVenueQueryNormalizersRedactUnderlyingErrors|AllVenueMutationNormalizersRedactVenueMessages|NormalizedErrorKindsAndMetadata)$$' -count=1",
		"test-exchange-quality":   "test-exchange-quality: test-exchange-offline test-exchange-redaction test-exchange-race",
	} {
		t.Run(target, func(t *testing.T) {
			if got := makeTargetBlock(t, makefile, target); got != want {
				t.Fatalf("%s recipe mismatch\ngot:\n%s\nwant:\n%s", target, got, want)
			}
		})
	}
}

func TestMakefileSerializesParallelTopLevelTargets(t *testing.T) {
	makefile := readRepoMakefile(t)
	if !regexp.MustCompile(`(?m)^\.NOTPARALLEL:\s*$`).MatchString(makefile) {
		t.Fatal("Makefile must use a global .NOTPARALLEL directive so direct make -j leaf invocations cannot overlap credentialed writes")
	}

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	temp := t.TempDir()
	probe := filepath.Join(temp, "serial-probe.mk")
	lock := filepath.Join(temp, "active")
	overlap := filepath.Join(temp, "overlap")
	probeMakefile := `.PHONY: codex-serial-probe-a codex-serial-probe-b
codex-serial-probe-a codex-serial-probe-b:
	@if mkdir "$(CODEX_SERIAL_LOCK)" 2>/dev/null; then sleep 0.2; rmdir "$(CODEX_SERIAL_LOCK)"; else touch "$(CODEX_SERIAL_OVERLAP)"; exit 1; fi
`
	if err := os.WriteFile(probe, []byte(probeMakefile), 0o600); err != nil {
		t.Fatalf("write Make serialization probe: %v", err)
	}
	cmd := exec.Command(
		"make", "--no-print-directory",
		"-f", filepath.Join(root, "Makefile"),
		"-f", probe,
		"-j2",
		"codex-serial-probe-a", "codex-serial-probe-b",
		"CODEX_SERIAL_LOCK="+lock,
		"CODEX_SERIAL_OVERLAP="+overlap,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("parallel Make probe overlapped top-level targets: %v\n%s", err, output)
	}
	if _, err := os.Stat(overlap); err == nil {
		t.Fatal("parallel Make probe observed overlapping top-level targets")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat Make overlap marker: %v", err)
	}
}

func TestMakefileRecipesNeverEnableCredentialedWrites(t *testing.T) {
	makefile := readRepoMakefile(t)
	// Every credentialed write now reaches a venue's production environment, so
	// no Make recipe may enable one: `make` must stay offline and deterministic.
	// This replaces the per-venue demo/testnet timeout contract, which bounded
	// writes that were only ever cheap because they landed on a non-production
	// environment. Live validation is a deliberate, hand-run act, not a target.
	writeEnabled := regexp.MustCompile(`BOLTER_ENABLE_[A-Z0-9_]+_WRITES=1`)
	currentTarget := ""
	for _, line := range strings.Split(makefile, "\n") {
		if !strings.HasPrefix(line, "\t") {
			if name, _, ok := strings.Cut(line, ":"); ok {
				currentTarget = strings.TrimSpace(name)
			}
			continue
		}
		if match := writeEnabled.FindString(line); match != "" {
			t.Errorf("Makefile target %s enables credentialed writes with %s", currentTarget, match)
		}
	}
}

func readRepoMakefile(t *testing.T) string {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	data, err := os.ReadFile(root + "/Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	return string(data)
}

func makeTargetBlock(t *testing.T, makefile, target string) string {
	t.Helper()
	prefix := target + ":"
	start := strings.Index(makefile, prefix)
	if start < 0 {
		t.Fatalf("Makefile target %s not found", target)
	}
	rest := makefile[start:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
