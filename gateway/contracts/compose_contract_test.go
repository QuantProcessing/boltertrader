package contracts

import (
	"os"
	"strings"
	"testing"
)

func TestPlatformOfflineGateIsCanonicalDockerFreeIntegratedSequence(t *testing.T) {
	data, err := os.ReadFile("../../scripts/test-platform-offline")
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	want := []string{
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
	previous := -1
	for _, command := range want {
		position := strings.Index(text, command)
		if position < 0 {
			t.Errorf("offline gate missing %q", command)
			continue
		}
		if position <= previous {
			t.Errorf("offline gate command out of order: %q", command)
		}
		previous = position
	}
	if strings.Contains(strings.ToLower(text), "docker") {
		t.Fatal("canonical integrated offline gate requires Docker")
	}
}

func TestPlatformOfflineGateIsDocumentedAsDefaultIntegratedGate(t *testing.T) {
	for _, path := range []string{
		"../../docs/guides/docker-free-local-platform.md",
		"../../docs/reference/testing.md",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(data)
		for _, required := range []string{
			"make test-platform-offline",
			"test-gateway-contract",
			"test-gateway-offline",
			"test-gateway-race",
			"test-gateway-security",
			"account-route-backfill",
			"go test -race ./...",
			"go vet ./...",
			"npm --prefix web run build",
			"npm --prefix web test -- --run",
			"npm --prefix web run test:e2e",
			"RUN_FULL",
			"FT_MODE=live",
			"Docker-free",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing offline-gate contract %q", path, required)
			}
		}
	}
}
