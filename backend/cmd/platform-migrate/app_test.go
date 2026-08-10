package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestRunAppliesRepositoryPlatformMigrations(t *testing.T) {
	prior := migratePlatform
	migratePlatform = func(_ context.Context, service, database, role string) (int, error) {
		if service != "platform_target_test" || database != "bolter_platform_test" || role != "bolter_platform_test_migrator" {
			t.Fatalf("service=%q database=%q role=%q", service, database, role)
		}
		return 15, nil
	}
	t.Cleanup(func() { migratePlatform = prior })
	var stdout bytes.Buffer
	if err := run([]string{"--service", "platform_target_test", "--database", "bolter_platform_test", "--role", "bolter_platform_test_migrator"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != `{"schemaVersion":15}` {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunRejectsRuntimeOrLiveIdentity(t *testing.T) {
	for _, args := range [][]string{
		{"--service", "platform_target_test", "--database", "bolter_platform_test", "--role", "bolter_platform_test"},
		{"--service", "platform_target_live", "--database", "bolter_platform_live", "--role", "bolter_platform_live_migrator"},
	} {
		if err := run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "CROSS_MODE_DATABASE_IDENTITY") {
			t.Fatalf("args=%v err=%v", args, err)
		}
	}
}

func TestReadBootstrapPasswordHashRequiresModeScoped0600File(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "bootstrap.hash")
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, hash, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readBootstrapPasswordHash(path)
	if err != nil || got != string(hash) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBootstrapPasswordHash(path); err == nil {
		t.Fatal("accepted unsafe password hash file mode")
	}
}
