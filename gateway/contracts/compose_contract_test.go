package contracts

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/config"
	"gopkg.in/yaml.v3"
)

func TestGatewayComposeReferenceStackIsSingleHostHardenedAndPersistent(t *testing.T) {
	root := readYAMLMap(t, "../../compose.yaml")
	services := mapValue(t, root, "services")
	if len(services) != 2 {
		t.Fatalf("services = %v, want exactly gateway and postgres", mapKeys(services))
	}
	gateway := mapValue(t, services, "gateway")
	postgres := mapValue(t, services, "postgres")

	build := mapValue(t, gateway, "build")
	assertScalar(t, build, "context", ".")
	assertScalar(t, build, "dockerfile", "Dockerfile")
	assertSliceContains(t, gateway, "ports", "127.0.0.1:${GATEWAY_PORT:-18080}:18080")
	if _, published := postgres["ports"]; published {
		t.Fatal("production PostgreSQL must not publish a host port")
	}

	dependsOn := mapValue(t, gateway, "depends_on")
	postgresDependency := mapValue(t, dependsOn, "postgres")
	assertScalar(t, postgresDependency, "condition", "service_healthy")
	assertScalar(t, gateway, "read_only", true)
	assertSliceContains(t, gateway, "cap_drop", "ALL")
	assertSliceContains(t, gateway, "security_opt", "no-new-privileges:true")
	assertSlicePrefix(t, gateway, "tmpfs", "/tmp:")
	grace, err := time.ParseDuration(stringValue(t, gateway, "stop_grace_period"))
	if err != nil || grace <= 10*time.Second {
		t.Fatalf("stop_grace_period = %v, want greater than 10s", gateway["stop_grace_period"])
	}
	assertSliceContains(t, gateway, "secrets", "GATEWAY_AUTH_TOKEN")
	assertSliceContains(t, gateway, "secrets", "GATEWAY_POSTGRES_DSN")

	gatewayHealth := mapValue(t, gateway, "healthcheck")
	healthCommand := strings.Join(dockerStringSlice(t, gatewayHealth, "test"), " ")
	for _, required := range []string{"exchange-gateway-docker-support", "health", "/v1/health"} {
		if required == "/v1/health" {
			continue
		}
		if !strings.Contains(healthCommand, required) {
			t.Errorf("Gateway healthcheck missing %q: %s", required, healthCommand)
		}
	}
	if !strings.Contains(healthCommand, "-kind ready") {
		t.Errorf("Gateway healthcheck is not readiness: %s", healthCommand)
	}

	if image := stringValue(t, postgres, "image"); !strings.Contains(image, "postgres:16-alpine@sha256:") {
		t.Fatalf("PostgreSQL image is not digest-pinned: %q", image)
	}
	assertSliceContains(t, postgres, "secrets", "POSTGRES_PASSWORD")
	assertSlicePrefix(t, postgres, "volumes", "gateway_postgres_data:")
	if _, ok := mapValue(t, root, "volumes")["gateway_postgres_data"]; !ok {
		t.Fatal("named gateway_postgres_data volume is missing")
	}
	postgresHealth := mapValue(t, postgres, "healthcheck")
	if !strings.Contains(strings.Join(dockerStringSlice(t, postgresHealth, "test"), " "), "pg_isready") {
		t.Fatal("PostgreSQL healthcheck does not use pg_isready")
	}

	secrets := mapValue(t, root, "secrets")
	for _, name := range []string{"GATEWAY_AUTH_TOKEN", "GATEWAY_POSTGRES_DSN", "POSTGRES_PASSWORD"} {
		entry := mapValue(t, secrets, name)
		if path := stringValue(t, entry, "file"); !strings.Contains(path, "_FILE") {
			t.Errorf("secret %s is not sourced from an explicit file variable: %q", name, path)
		}
	}
}

func TestGatewayComposeTestOverridePublishesOnlyRandomLoopbackPostgresPort(t *testing.T) {
	root := readYAMLMap(t, "../../docker/compose.test.yaml")
	services := mapValue(t, root, "services")
	if len(services) != 1 {
		t.Fatalf("override services = %v, want postgres only", mapKeys(services))
	}
	postgres := mapValue(t, services, "postgres")
	assertSliceContains(t, postgres, "ports", "127.0.0.1::5432")
}

func TestDockerGatewayConfigAndEnvTemplateAreSafeAndValid(t *testing.T) {
	data, err := os.ReadFile("../../docker/gateway.docker.yaml")
	if err != nil {
		t.Fatalf("read Docker Gateway config: %v", err)
	}
	file, err := config.LoadYAML(data)
	if err != nil {
		t.Fatalf("load Docker Gateway config: %v", err)
	}
	if file.Server.Listen != "0.0.0.0:18080" || !file.Server.AllowNonLoopbackListen {
		t.Fatalf("Docker listen contract = %+v", file.Server)
	}
	if len(file.Accounts) != 0 {
		t.Fatalf("safe reference config accounts = %d, want zero", len(file.Accounts))
	}
	if file.Server.AuthTokenRef.RawForProvider() != "env://GATEWAY_AUTH_TOKEN" ||
		file.Postgres.DSNRef.RawForProvider() != "env://GATEWAY_POSTGRES_DSN" {
		t.Fatal("Docker config secret refs do not match Compose secret names")
	}

	envData, err := os.ReadFile("../../docker/gateway.env.example")
	if err != nil {
		t.Fatalf("read env template: %v", err)
	}
	envText := string(envData)
	for _, required := range []string{
		"GATEWAY_IMAGE=",
		"GATEWAY_CONFIG_FILE=",
		"GATEWAY_PORT=",
		"GATEWAY_AUTH_TOKEN_FILE=",
		"GATEWAY_POSTGRES_DSN_FILE=",
		"POSTGRES_PASSWORD_FILE=",
		"POSTGRES_USER=",
		"POSTGRES_DB=",
	} {
		if !strings.Contains(envText, required) {
			t.Errorf("env template missing %q", required)
		}
	}
	for _, forbidden := range []string{"postgres://", "sk-", "secret-canary", "password="} {
		if strings.Contains(strings.ToLower(envText), strings.ToLower(forbidden)) {
			t.Errorf("env template contains secret-like value %q", forbidden)
		}
	}
}

func TestGatewayDockerMakeTargetsAndOperatorDocsAreComplete(t *testing.T) {
	makeData, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	makefile := string(makeData)
	targets := []string{
		"gateway-compose-dsn",
		"test-gateway-docker-config",
		"test-gateway-docker-build",
		"test-gateway-docker-security",
		"test-gateway-docker-smoke",
		"test-gateway-docker-persistence",
		"test-gateway-docker-postgres",
		"test-gateway-docker",
	}
	for _, target := range targets {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(target) + `(?:[: ])`).MatchString(makefile) {
			t.Errorf("Makefile missing target %s", target)
		}
	}
	dsnBlock := dockerMakeTargetBlock(t, makefile, "gateway-compose-dsn")
	for _, required := range []string{
		"GATEWAY_COMPOSE_PROJECT",
		`docker compose -p "$$GATEWAY_COMPOSE_PROJECT"`,
		"-f compose.yaml -f docker/compose.test.yaml",
		"port postgres 5432",
		"127.0.0.1",
	} {
		if !strings.Contains(dsnBlock, required) {
			t.Errorf("gateway-compose-dsn missing %q", required)
		}
	}
	for _, target := range []string{
		"test-gateway-docker-smoke",
		"test-gateway-docker-persistence",
		"test-gateway-docker-postgres",
	} {
		block := dockerMakeTargetBlock(t, makefile, target)
		for _, required := range []string{
			`project="boltertrader-gateway-$@-$$$$"`,
			`docker compose -p "$$project"`,
			"down -v --remove-orphans",
		} {
			if !strings.Contains(block, required) {
				t.Errorf("%s missing %q", target, required)
			}
		}
		if !strings.Contains(block, "trap - EXIT INT TERM") {
			t.Errorf("%s does not require successful explicit teardown", target)
		}
	}
	if block := dockerMakeTargetBlock(t, makefile, "test-gateway-docker-security"); !strings.Contains(block, "trap - EXIT INT TERM") {
		t.Error("Docker image security target does not require successful explicit teardown")
	}
	if block := dockerMakeTargetBlock(t, makefile, "test-gateway-docker-persistence"); !strings.Contains(block, "sentinel") || !strings.Contains(block, "--force-recreate") {
		t.Error("persistence target lacks a sentinel and container recreation proof")
	}
	if block := dockerMakeTargetBlock(t, makefile, "test-gateway-docker-postgres"); !strings.Contains(block, "test-gateway-postgres") || !strings.Contains(block, "test-gateway-restart") {
		t.Error("Docker PostgreSQL target does not run both existing database gates")
	}
	if !strings.Contains(makefile, "test-gateway-docker: test-gateway-docker-config test-gateway-docker-build test-gateway-docker-security test-gateway-docker-smoke test-gateway-docker-persistence test-gateway-docker-postgres") {
		t.Error("Docker aggregate target does not preserve the approved serial order")
	}

	docs := []string{
		"../../docs/guides/exchange-gateway.md",
		"../../docs/zh-CN/guides/exchange-gateway.md",
	}
	for _, path := range docs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read %s: %v", path, err)
			continue
		}
		text := string(data)
		for _, required := range []string{
			"Docker", "Docker secrets", "health", "logs", "SIGTERM",
			"persistence", "external PostgreSQL", "ingress", "TLS",
			"single-host", "test-gateway-docker",
		} {
			if !strings.Contains(text, required) {
				t.Errorf("%s missing Docker deployment topic %q", path, required)
			}
		}
		for _, forbidden := range []string{"Kubernetes is supported", "high availability is provided", "registry publishing is included"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s claims excluded scope %q", path, forbidden)
			}
		}
	}
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var value map[string]any
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return value
}

func mapValue(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want mapping", key, parent[key])
	}
	return value
}

func stringValue(t *testing.T, parent map[string]any, key string) string {
	t.Helper()
	value, ok := parent[key].(string)
	if !ok {
		t.Fatalf("%s = %#v, want string", key, parent[key])
	}
	return value
}

func dockerStringSlice(t *testing.T, parent map[string]any, key string) []string {
	t.Helper()
	raw, ok := parent[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want sequence", key, parent[key])
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		value, ok := item.(string)
		if !ok {
			t.Fatalf("%s item = %#v, want string", key, item)
		}
		values = append(values, value)
	}
	return values
}

func assertScalar(t *testing.T, parent map[string]any, key string, want any) {
	t.Helper()
	if got := parent[key]; got != want {
		t.Errorf("%s = %#v, want %#v", key, got, want)
	}
}

func assertSliceContains(t *testing.T, parent map[string]any, key, want string) {
	t.Helper()
	for _, value := range dockerStringSlice(t, parent, key) {
		if value == want {
			return
		}
	}
	t.Errorf("%s does not contain %q", key, want)
}

func assertSlicePrefix(t *testing.T, parent map[string]any, key, want string) {
	t.Helper()
	for _, value := range dockerStringSlice(t, parent, key) {
		if strings.HasPrefix(value, want) {
			return
		}
	}
	t.Errorf("%s does not contain prefix %q", key, want)
}

func mapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func dockerMakeTargetBlock(t *testing.T, makefile, target string) string {
	t.Helper()
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
