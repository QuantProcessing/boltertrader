package devsetup

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Status is the outcome of one check.
type Status string

const (
	// Pass means the check ran and the property holds.
	Pass Status = "pass"
	// Fail means the check ran and the property does not hold.
	Fail Status = "fail"
	// Skip means the check could not run because something it depends on
	// already failed. It is reported rather than omitted, because "postgres is
	// down so nothing downstream was tested" is a different answer from "the
	// downstream checks passed".
	Skip Status = "skip"
)

// Check is one diagnosis. Fix is what to do about it, and is the reason this
// exists as a struct rather than a bool: an operator who runs doctor is already
// stuck, and a check that reports a failure without naming the next command has
// only told them what they knew.
type Check struct {
	Name   string
	Status Status
	Detail string
	Fix    string
}

// Diagnose reports on every part of the local stack, in dependency order, and
// never stops early. A run that finds five problems should say so once rather
// than be re-run five times.
//
// It reads the same .env.stack that compose does, so it always probes the ports
// the running stack actually published rather than the defaults — which is the
// entire point of `POSTGRES_PORT=15433 make up` being remembered.
func Diagnose(ctx context.Context, root string) ([]Check, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}

	var checks []Check
	add := func(check Check) { checks = append(checks, check) }

	add(checkDocker(ctx))

	envPath := filepath.Join(root, EnvStackName)
	values := readEnvStack(envPath)
	if len(values) == 0 {
		add(Check{
			Name:   EnvStackName,
			Status: Fail,
			Detail: "missing or empty",
			Fix:    "run `make up`, which generates it",
		})
		// Without the env file there are no ports to probe and no secret
		// directory to inspect, so every remaining check would fail for the
		// same single reason and bury it.
		for _, name := range []string{"credentials", "credential store", "postgres", "gateway", "trading", "auth edge", "web"} {
			add(Check{Name: name, Status: Skip, Detail: "no " + EnvStackName + " to read the port from"})
		}
		return checks, nil
	}
	add(Check{Name: EnvStackName, Status: Pass, Detail: envPath})

	checks = append(checks, checkCredentials(filepath.Join(root, SecretDirName, Mode))...)
	add(checkSecretStore(filepath.Join(root, SecretDirName, SecretStoreDirName)))

	postgresPort := valueOr(values, postgresPortVar, defaultPostgresPort)
	postgresUp := probeTCP("postgres", postgresPort, &checks)
	if postgresUp {
		add(checkClusterInventory(ctx, postgresPort))
	} else {
		add(Check{Name: "postgres roles", Status: Skip, Detail: "postgres is not reachable"})
	}

	add(checkGateway(ctx, valueOr(values, gatewayPortVar, defaultGatewayPort), filepath.Join(root, SecretDirName, Mode, GatewayAuthTokenFile)))
	add(checkTrading(ctx, valueOr(values, tradingPortVar, defaultTradingPort), filepath.Join(root, SecretDirName, Mode, TradingServiceTokenFile)))
	add(checkAuthEdge(ctx, valueOr(values, authEdgePortVar, defaultAuthEdgePort)))
	add(checkWeb(ctx, valueOr(values, webPortVar, defaultWebPort)))
	return checks, nil
}

// Failed reports whether any check failed. Skips do not count: a skip is always
// downstream of a failure that is already being reported.
func Failed(checks []Check) bool {
	for _, check := range checks {
		if check.Status == Fail {
			return true
		}
	}
	return false
}

func checkDocker(ctx context.Context) Check {
	// `compose version` rather than `docker version`: the compose plugin is a
	// separate installation from the engine, and the failure of an operator who
	// has docker but not compose is otherwise reported as a syntax error.
	command := exec.CommandContext(ctx, "docker", "compose", "version", "--short")
	output, err := command.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Check{
				Name:   "docker",
				Status: Fail,
				Detail: "not on PATH",
				Fix:    "install Docker Desktop or OrbStack, then re-run",
			}
		}
		return Check{
			Name:   "docker",
			Status: Fail,
			Detail: "`docker compose version` failed: " + firstLine(err.Error()),
			Fix:    "start the Docker daemon, or install the compose plugin",
		}
	}
	return Check{Name: "docker", Status: Pass, Detail: "compose " + strings.TrimSpace(string(output))}
}

// checkCredentials re-derives the rules the services apply to these files at
// startup — readProtectedToken in cmd/trading and cmd/authedge, FileKeySource in
// secretstore — rather than calling into them: two are package main and cannot
// be imported. Restating them here is the point of the check. A loosened mode or
// a truncated token otherwise surfaces as a container that exits during startup
// with a message about a file, which reads like a missing mount rather than like
// a permission an editor changed.
func checkCredentials(secretDir string) []Check {
	info, err := os.Stat(secretDir)
	if err != nil {
		return []Check{{
			Name:   "credentials",
			Status: Fail,
			Detail: secretDir + " is missing",
			Fix:    "run `make up`, which generates it",
		}}
	}
	var checks []Check
	if mode := info.Mode().Perm(); mode != 0o700 {
		checks = append(checks, Check{
			Name:   "credentials",
			Status: Fail,
			Detail: fmt.Sprintf("%s is mode %o, want 700", secretDir, mode),
			Fix:    "run `make up`, which tightens it",
		})
	}

	shapes := map[string]secretShape{
		TradingServiceTokenFile:     shapeOpaque,
		GatewayAuthTokenFile:        shapeOpaque,
		ManualPreviewSigningKeyFile: shapeOpaque,
		SecretMasterKeyFile:         shapeHexKey,
		BootstrapPasswordHashFile:   shapeOpaque,
	}
	var problems []string
	for _, name := range []string{
		TradingServiceTokenFile, GatewayAuthTokenFile, ManualPreviewSigningKeyFile,
		SecretMasterKeyFile, GatewayPostgresDSNFile, BootstrapPasswordFile,
		BootstrapPasswordHashFile,
	} {
		if problem := inspectSecret(filepath.Join(secretDir, name), shapes[name]); problem != "" {
			problems = append(problems, name+": "+problem)
		}
	}
	if len(problems) > 0 {
		return append(checks, Check{
			Name:   "credentials",
			Status: Fail,
			Detail: strings.Join(problems, "; "),
			Fix:    "run `make up` to regenerate what is missing and tighten what is loose",
		})
	}
	if len(checks) > 0 {
		return checks
	}
	return []Check{{Name: "credentials", Status: Pass, Detail: "7 files, 0600, in " + secretDir}}
}

// checkSecretStore inspects the one path in the stack a service writes to: the
// gateway's encrypted venue credentials. It is a bind mount, and Docker creates
// a missing bind-mount source itself, owned by root — so the interesting failure
// is not "absent" but "present and not writable by the uid the gateway runs as",
// which the gateway reports as a startup error naming no path at all.
func checkSecretStore(storeDir string) Check {
	info, err := os.Stat(storeDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Check{
			Name:   "credential store",
			Status: Fail,
			Detail: storeDir + " is missing",
			Fix:    "run `make up`, which creates it before compose can",
		}
	case err != nil:
		return Check{Name: "credential store", Status: Fail, Detail: err.Error()}
	case !info.IsDir():
		return Check{
			Name:   "credential store",
			Status: Fail,
			Detail: storeDir + " is not a directory",
			Fix:    "remove it and re-run `make up`",
		}
	}
	// Probed rather than deduced from the owner, because the question is only
	// ever whether a write succeeds, and the doctor runs as the same uid the
	// containers do.
	probe, err := os.CreateTemp(storeDir, ".doctor-*")
	if err != nil {
		return Check{
			Name:   "credential store",
			Status: Fail,
			Detail: storeDir + " is not writable by this user",
			Fix:    "`sudo chown -R $(id -u):$(id -g) " + storeDir + "`, or remove it and re-run `make up`",
		}
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return Check{Name: "credential store", Status: Pass, Detail: "writable, at " + storeDir}
}

// secretShape is what a file's contents must look like beyond being present and
// non-empty at 0600. Each value re-states a rule enforced elsewhere at startup,
// so that a violation is reported here as a named file rather than there as a
// service that will not come up.
type secretShape int

const (
	// shapeFree is read by postgres or by a human — a DSN, a plaintext
	// password. Non-empty is the whole contract.
	shapeFree secretShape = iota
	// shapeOpaque is compared byte-for-byte by a service. readProtectedToken
	// rejects anything under 32 bytes or holding a byte outside printable
	// ASCII.
	shapeOpaque
	// shapeHexKey is the gateway's master key. secretstore.FileKeySource
	// hex-decodes it into a 32-byte AES-GCM key and accepts no other length.
	shapeHexKey
)

func inspectSecret(path string, shape secretShape) string {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "missing"
	case err != nil:
		return err.Error()
	case !info.Mode().IsRegular():
		return "not a regular file"
	case info.Mode().Perm() != 0o600:
		return fmt.Sprintf("mode %o, want 600", info.Mode().Perm())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return err.Error()
	}
	// The contents are inspected but never reported. Every string this function
	// returns describes the file, not what is in it.
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "empty"
	}
	switch shape {
	case shapeOpaque:
		if len(token) < 32 {
			return fmt.Sprintf("%d bytes, under the 32 the services require", len(token))
		}
		for _, value := range []byte(token) {
			if value < 0x21 || value > 0x7e {
				return "contains a byte the services reject as non-opaque"
			}
		}
	case shapeHexKey:
		// Deliberately not the trimmed token: FileKeySource strips one trailing
		// newline and nothing else, so a file with stray whitespace would pass
		// a check written against `token` and still fail the gateway.
		raw := strings.TrimSuffix(string(contents), "\n")
		if _, err := hex.DecodeString(raw); err != nil || len(raw) != 64 {
			return "not the 64 hex characters the gateway's key source decodes"
		}
	}
	return ""
}

// probeTCP reports whether something is listening, and appends the check. It
// returns the answer as well so the caller can skip what depends on it.
func probeTCP(name, port string, checks *[]Check) bool {
	address := net.JoinHostPort("127.0.0.1", port)
	connection, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		*checks = append(*checks, Check{
			Name:   name,
			Status: Fail,
			Detail: "nothing accepting connections on " + address,
			Fix:    "run `make up`; if it is already up, `make logs` will say why " + name + " exited",
		})
		return false
	}
	_ = connection.Close()
	*checks = append(*checks, Check{Name: name, Status: Pass, Detail: "listening on " + address})
	return true
}

// checkClusterInventory is the check that earns doctor its keep. The official
// postgres image runs the initdb scripts exactly once, triggered by an empty
// data directory rather than by any version of their contents — so an edit to
// the role script with the named volume still around means the script never
// runs, the cluster starts perfectly, and the stack fails deep inside a
// migration with a message about a role.
func checkClusterInventory(ctx context.Context, port string) Check {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// The bootstrap superuser, because it is the one identity that can read the
	// whole catalog and the only one guaranteed to exist when the roles under
	// test do not. It carries no password: the cluster runs under trust
	// authentication on a loopback-only port.
	connection, err := pgx.Connect(ctx, "postgres://bolter_bootstrap@127.0.0.1:"+port+"/bolter_bootstrap?sslmode=disable")
	if err != nil {
		return Check{
			Name:   "postgres roles",
			Status: Fail,
			Detail: "cannot connect as bolter_bootstrap: " + firstLine(err.Error()),
			Fix:    "something other than this stack may hold the port" + heldBy(port),
		}
	}
	defer func() { _ = connection.Close(ctx) }()

	var missing []string
	for _, role := range []string{
		"bolter_platform_test_admin", "bolter_gateway_test_writer", "bolter_gateway_test_migrator",
		"bolter_gateway_test", "bolter_trading_test", "bolter_authedge_test",
	} {
		var present bool
		if err := connection.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&present); err != nil {
			return Check{Name: "postgres roles", Status: Fail, Detail: "querying pg_roles: " + firstLine(err.Error())}
		}
		if !present {
			missing = append(missing, "role "+role)
		}
	}
	for _, database := range []string{"bolter_gateway_test", "bolter_trading_test", "bolter_authedge_test"} {
		var present bool
		if err := connection.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", database).Scan(&present); err != nil {
			return Check{Name: "postgres roles", Status: Fail, Detail: "querying pg_database: " + firstLine(err.Error())}
		}
		if !present {
			missing = append(missing, "database "+database)
		}
	}
	if len(missing) == 0 {
		// The membership, not just the two roles. The gateway's serving identity
		// reaches its tables only through bolter_gateway_test_writer, so a grant
		// that did not happen produces two roles that both exist and a runtime
		// that is denied on every write.
		var member bool
		if err := connection.QueryRow(ctx, "SELECT pg_has_role('bolter_gateway_test', 'bolter_gateway_test_writer', 'MEMBER')").Scan(&member); err != nil {
			return Check{Name: "postgres roles", Status: Fail, Detail: "checking role membership: " + firstLine(err.Error())}
		}
		if !member {
			missing = append(missing, "bolter_gateway_test's membership in bolter_gateway_test_writer")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Check{
			Name:   "postgres roles",
			Status: Fail,
			Detail: "missing " + strings.Join(missing, ", "),
			Fix:    "the initdb script did not run against this volume; `make down-hard` then `make up` recreates it",
		}
	}
	return Check{Name: "postgres roles", Status: Pass, Detail: "6 roles, 3 databases, writer membership granted"}
}

// gatewayModeHeader is the attestation the Gateway requires on every route. It
// is restated here rather than imported because the Gateway declares it in a
// package main; api/exchange-gateway/v1/internal-openapi.yaml is the document
// that keeps the copies honest.
const gatewayModeHeader = "X-Bolter-Mode"

// checkGateway probes the same readiness endpoint the container's own
// healthcheck calls, and needs two things on the request rather than one.
//
// The mode attestation is the obvious one: the Gateway wraps every route in that
// check and answers 409 without it, health included. Mode is this package's
// constant rather than a read of the Gateway's YAML because it is already the
// value `make up` writes the whole stack around — the secret directory's name,
// BT_MODE in compose, and the Gateway's own default all come out of it.
//
// The bearer token is the less obvious one, and its absence is not a quiet
// degradation: readiness sits behind the same auth as every other /v1 route, so
// an unauthenticated probe gets a flat 401 from a Gateway that is perfectly
// healthy, and this check reported that as a stack failure. The token is the
// capability plane's, because /v1/health/ready is a capability-plane route —
// the authority plane's port is deliberately not published to this host at all.
func checkGateway(ctx context.Context, port, tokenPath string) Check {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return Check{Name: "gateway", Status: Skip, Detail: "no auth token to authenticate with", Fix: "run `make up`"}
	}
	status, err := probeHTTP(ctx, "http://127.0.0.1:"+port+"/v1/health/ready", map[string]string{
		gatewayModeHeader: Mode,
		"Authorization":   "Bearer " + strings.TrimSpace(string(token)),
	})
	if err != nil {
		return Check{
			Name:   "gateway",
			Status: Fail,
			Detail: firstLine(err.Error()),
			Fix:    "`make logs` will say why it exited" + heldBy(port),
		}
	}
	switch status {
	case http.StatusOK:
		return Check{Name: "gateway", Status: Pass, Detail: "authenticated /v1/health/ready is 200 on 127.0.0.1:" + port}
	case http.StatusUnauthorized:
		// Serving, but not with this token — the same regenerated-secret case
		// checkTrading describes below.
		return Check{
			Name:   "gateway",
			Status: Fail,
			Detail: "rejected the token in " + filepath.Base(tokenPath),
			Fix:    "the running container holds an older token; `make down` then `make up`",
		}
	default:
		return Check{Name: "gateway", Status: Fail, Detail: fmt.Sprintf("/v1/health/ready returned %d", status), Fix: "`make logs` has the reason"}
	}
}

// checkTrading is why this runs on the host rather than as a container
// healthcheck. cmd/trading wraps its whole mux in the service-token
// authenticator, readiness included, so the probe has to carry the token — and
// the image is distroless, with no shell to read the file with.
func checkTrading(ctx context.Context, port, tokenPath string) Check {
	token, err := os.ReadFile(tokenPath)
	if err != nil {
		return Check{Name: "trading", Status: Skip, Detail: "no service token to authenticate with", Fix: "run `make up`"}
	}
	status, err := probeHTTP(ctx, "http://127.0.0.1:"+port+"/api/v1/system/readiness", map[string]string{
		"Authorization": "Bearer " + strings.TrimSpace(string(token)),
	})
	if err != nil {
		return Check{
			Name:   "trading",
			Status: Fail,
			Detail: firstLine(err.Error()),
			Fix:    "`make logs` will say why it exited" + heldBy(port),
		}
	}
	switch status {
	case http.StatusOK:
		return Check{Name: "trading", Status: Pass, Detail: "authenticated readiness is 200 on 127.0.0.1:" + port}
	case http.StatusUnauthorized:
		// The process is serving, so this is not a liveness problem: the file
		// on disk is not the token the process loaded. That happens when the
		// secret is regenerated under a running stack.
		return Check{
			Name:   "trading",
			Status: Fail,
			Detail: "rejected the token in " + filepath.Base(tokenPath),
			Fix:    "the running container holds an older token; `make down` then `make up`",
		}
	default:
		return Check{Name: "trading", Status: Fail, Detail: fmt.Sprintf("readiness returned %d", status), Fix: "`make logs` has the reason"}
	}
}

// checkAuthEdge expects a 401. Every route this service exposes either
// terminates a session or requires one, so 401 from /api/v1/me is the healthy
// answer for a request carrying no cookie — it proves the process is serving
// and routing, which is all this check claims.
func checkAuthEdge(ctx context.Context, port string) Check {
	status, err := probeHTTP(ctx, "http://127.0.0.1:"+port+"/api/v1/me", nil)
	if err != nil {
		return Check{
			Name:   "auth edge",
			Status: Fail,
			Detail: firstLine(err.Error()),
			Fix:    "`make logs` will say why it exited" + heldBy(port),
		}
	}
	if status != http.StatusUnauthorized && status != http.StatusOK {
		return Check{Name: "auth edge", Status: Fail, Detail: fmt.Sprintf("/api/v1/me returned %d, want 401 without a session", status), Fix: "`make logs` has the reason"}
	}
	return Check{Name: "auth edge", Status: Pass, Detail: "serving on http://127.0.0.1:" + port}
}

// checkWeb is the one check an operator can reproduce by opening a browser, and
// it asks for the same thing a browser does: the document at /. A 200 proves
// both halves of that container — Caddy started, and the build stage actually
// produced a bundle for it to serve, which inspecting the container cannot tell
// apart from an empty /srv.
//
// It deliberately does not probe through the /api proxy. That path's health is
// the auth edge's, reported above on its own line, and folding the two together
// would blame the frontend for an upstream that had not finished starting.
func checkWeb(ctx context.Context, port string) Check {
	status, err := probeHTTP(ctx, "http://127.0.0.1:"+port+"/", nil)
	if err != nil {
		return Check{
			Name:   "web",
			Status: Fail,
			Detail: firstLine(err.Error()),
			Fix:    "`make logs` will say why it exited" + heldBy(port),
		}
	}
	if status != http.StatusOK {
		return Check{
			Name:   "web",
			Status: Fail,
			Detail: fmt.Sprintf("/ returned %d, want 200", status),
			Fix:    "the image may hold no bundle; rebuild it with `make up`",
		}
	}
	return Check{Name: "web", Status: Pass, Detail: "open http://127.0.0.1:" + port}
}

func probeHTTP(ctx context.Context, url string, headers map[string]string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode, nil
}

// heldBy names whatever is listening on a port, when the operating system will
// say. "connection refused" and "held by a postgres from last month" are very
// different problems that otherwise read identically.
//
// It returns "" for anything it cannot answer — lsof is not installed
// everywhere, and a diagnosis that fails to diagnose should be silent rather
// than add a line about itself.
func heldBy(port string) string {
	output, err := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN").Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return ""
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 2 {
		return ""
	}
	return fmt.Sprintf("; port %s is held by %s (pid %s)", port, fields[0], fields[1])
}

func valueOr(values map[string]string, name, fallback string) string {
	if value := strings.TrimSpace(values[name]); value != "" {
		return value
	}
	return fallback
}

// firstLine keeps a multi-line driver or exec error from turning one check into
// a paragraph. The full text is never the useful part in a report that is meant
// to be scanned.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return strings.TrimSpace(line)
}
