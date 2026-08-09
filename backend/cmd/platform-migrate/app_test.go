package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunAppliesRepositoryPlatformMigrations(t *testing.T) {
	prior := migratePlatform
	migratePlatform = func(_ context.Context, service, database, role string) (int, error) {
		if service != "platform-test" || database != "platform_test" || role != "platform_test_migrator" {
			t.Fatalf("service=%q database=%q role=%q", service, database, role)
		}
		return 15, nil
	}
	t.Cleanup(func() { migratePlatform = prior })
	var stdout bytes.Buffer
	if err := run([]string{"--service", "platform-test", "--database", "platform_test", "--role", "platform_test_migrator"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != `{"schemaVersion":15}` {
		t.Fatalf("stdout=%q", stdout.String())
	}
}
