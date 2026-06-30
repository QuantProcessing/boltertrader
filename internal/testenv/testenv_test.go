package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRequireFullSkipsWithoutRunFull(t *testing.T) {
	t.Setenv("RUN_FULL", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireFull(t)
		t.Fatalf("expected RequireFull to skip without RUN_FULL=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireFullSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("RUN_FULL", "1")
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireFull(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireFull to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveCredentialsSkipsWhenRequiredEnvMissing(t *testing.T) {
	t.Setenv("TESTENV_REQUIRED_VAR", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveCredentials(t, "TESTENV_REQUIRED_VAR")
		t.Fatalf("expected RequireLiveCredentials to skip when required env is missing")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireLiveWriteSkipsWithoutEnableFlag(t *testing.T) {
	t.Setenv("TESTENV_ENABLE_LIVE_WRITE", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireLiveWrite(t, "TESTENV_ENABLE_LIVE_WRITE")
		t.Fatalf("expected RequireLiveWrite to skip without enable flag")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestRequireSoakSkipsWithoutRunSoak(t *testing.T) {
	t.Setenv("RUN_SOAK", "")

	skipped := false
	t.Run("skip", func(t *testing.T) {
		defer func() {
			skipped = t.Skipped()
		}()
		RequireSoak(t)
		t.Fatalf("expected RequireSoak to skip without RUN_SOAK=1")
	})

	if !skipped {
		t.Fatalf("expected subtest to skip")
	}
}

func TestIsTransientLiveNetworkError(t *testing.T) {
	cases := []error{
		errors.New("Get https://api.example.test: context deadline exceeded (Client.Timeout exceeded while awaiting headers)"),
		errors.New("EOF"),
		errors.New("tls handshake timeout"),
	}
	for _, err := range cases {
		if !IsTransientLiveNetworkError(err) {
			t.Fatalf("expected transient live network error: %v", err)
		}
	}
	if IsTransientLiveNetworkError(errors.New("invalid signature")) {
		t.Fatal("semantic exchange errors should not be treated as transient network errors")
	}
}

func TestLoadRepoEnvDoesNotOverrideExistingEnv(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("TESTENV_OVERRIDE=file\nTESTENV_FROM_FILE=present\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}

	t.Setenv("TESTENV_OVERRIDE", "shell")
	if err := os.Unsetenv("TESTENV_FROM_FILE"); err != nil {
		t.Fatalf("unset TESTENV_FROM_FILE: %v", err)
	}

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}

	if got := os.Getenv("TESTENV_OVERRIDE"); got != "shell" {
		t.Fatalf("expected shell env to win, got %q", got)
	}
	if got := os.Getenv("TESTENV_FROM_FILE"); got != "present" {
		t.Fatalf("expected missing env to load from file, got %q", got)
	}
}

func TestLoadRepoEnvAppliesLegacyAliases(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origWD)
	})

	if err := os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module testenv\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("OKX_SECRET_KEY=legacy-secret\nNADO_SUB_ACCOUNT_NAME=legacy-sub\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.Chdir(filepath.Join(tmp, "nested")); err != nil {
		t.Fatalf("chdir nested: %v", err)
	}
	if err := os.Unsetenv("OKX_API_SECRET"); err != nil {
		t.Fatalf("unset OKX_API_SECRET: %v", err)
	}
	if err := os.Unsetenv("OKX_SECRET_KEY"); err != nil {
		t.Fatalf("unset OKX_SECRET_KEY: %v", err)
	}
	if err := os.Unsetenv("NADO_SUBACCOUNT_NAME"); err != nil {
		t.Fatalf("unset NADO_SUBACCOUNT_NAME: %v", err)
	}
	if err := os.Unsetenv("NADO_SUB_ACCOUNT_NAME"); err != nil {
		t.Fatalf("unset NADO_SUB_ACCOUNT_NAME: %v", err)
	}

	if err := LoadRepoEnv(); err != nil {
		t.Fatalf("LoadRepoEnv: %v", err)
	}

	if got := os.Getenv("OKX_API_SECRET"); got != "legacy-secret" {
		t.Fatalf("expected legacy OKX secret alias to populate canonical env, got %q", got)
	}
	if got := os.Getenv("NADO_SUBACCOUNT_NAME"); got != "legacy-sub" {
		t.Fatalf("expected legacy Nado sub-account alias to populate canonical env, got %q", got)
	}
}
