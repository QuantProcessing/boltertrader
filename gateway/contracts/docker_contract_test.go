package contracts

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultLocalPlatformArtifactsAreDockerFree(t *testing.T) {
	for _, path := range []string{
		"../../scripts/dev-platform",
		"../../scripts/dev-platform-service",
		"../../scripts/test-platform-offline",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := strings.ToLower(string(data))
		if strings.Contains(text, "docker compose") || strings.Contains(text, "docker run") {
			t.Errorf("default local artifact %s requires Docker", path)
		}
	}

	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(makeData)
	for _, target := range []string{
		"dev-platform-up", "dev-platform-down", "dev-platform-restart",
		"dev-platform-status", "dev-platform-logs", "dev-platform-reset",
		"dev-platform-doctor", "test-dev-platform", "test-browser",
		"test-platform-offline",
	} {
		block := contractMakeTargetBlock(makefile, target)
		if block == "" {
			t.Errorf("Makefile missing default local target %s", target)
		} else if strings.Contains(strings.ToLower(block), "docker") {
			t.Errorf("default local target %s requires Docker", target)
		}
	}
}

func TestHistoricalDockerArtifactsRemainOptional(t *testing.T) {
	for _, path := range []string{"../../Dockerfile", "../../compose.yaml", "../../docker"} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("historical Docker artifact %s: %v", path, err)
		}
	}
}

func contractMakeTargetBlock(makefile, target string) string {
	start := strings.Index(makefile, target+":")
	if start < 0 {
		return ""
	}
	rest := makefile[start:]
	if end := strings.Index(rest, "\n\n"); end >= 0 {
		return rest[:end]
	}
	return rest
}
