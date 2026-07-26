package contracts

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestGatewayDockerfileFreezesMinimalNonRootPID1Runtime(t *testing.T) {
	data, err := os.ReadFile("../../Dockerfile")
	if err != nil {
		t.Fatalf("read Dockerfile: %v", err)
	}
	text := string(data)
	for _, required := range []string{
		"FROM golang:1.26.0-alpine3.23@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5 AS build",
		"FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6",
		"CGO_ENABLED=0",
		"go build -trimpath",
		"./cmd/exchange-gateway",
		"./cmd/exchange-gateway-docker-support",
		"COPY --from=build",
		"/usr/local/bin/exchange-gateway",
		"/usr/local/bin/exchange-gateway-docker-support",
		"USER nonroot:nonroot",
		`ENTRYPOINT ["/usr/local/bin/exchange-gateway-docker-support"]`,
		`CMD ["run", "-config", "/etc/boltertrader/gateway.yaml"]`,
		`HEALTHCHECK`,
		`"health", "-config", "/etc/boltertrader/gateway.yaml", "-kind", "ready"`,
	} {
		if !strings.Contains(text, required) {
			t.Errorf("Dockerfile missing %q", required)
		}
	}
	for _, forbidden := range []string{
		":latest",
		"ENTRYPOINT [\"/bin/sh\"",
		"ENTRYPOINT [\"sh\"",
		"ENTRYPOINT [\"bash\"",
		"USER root",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("Dockerfile contains forbidden runtime contract %q", forbidden)
		}
	}

	stages := regexp.MustCompile(`(?m)^FROM `).FindAllStringIndex(text, -1)
	if len(stages) != 2 {
		t.Fatalf("Dockerfile stages = %d, want exactly build and runtime", len(stages))
	}
	runtimeStage := text[stages[1][0]:]
	for _, forbidden := range []string{"COPY .", "COPY gateway", "COPY cmd", "RUN ", "go.mod", ".go "} {
		if strings.Contains(runtimeStage, forbidden) {
			t.Errorf("runtime stage contains build/source material %q", forbidden)
		}
	}
}

func TestGatewayDockerignoreExcludesRepositoryStateTestsAndSecretFiles(t *testing.T) {
	data, err := os.ReadFile("../../.dockerignore")
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	lines := make(map[string]struct{})
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines[line] = struct{}{}
		}
	}
	for _, required := range []string{
		".git",
		".omx",
		".env",
		".env.*",
		"**/.env",
		"**/.env.*",
		"**/*_test.go",
		"docs",
		"docker",
		"*.pem",
		"*.key",
		"*.p12",
		"coverage.out",
	} {
		if _, ok := lines[required]; !ok {
			t.Errorf(".dockerignore missing %q", required)
		}
	}
	for _, dangerous := range []string{"*secret*", "**/secret", "gateway/secret"} {
		if _, ok := lines[dangerous]; ok {
			t.Errorf(".dockerignore would remove the required secret package with %q", dangerous)
		}
	}
}
