// Package devsetup generates the credentials and compose variables the local
// stack needs. It is idempotent, so `make up` can run it before every
// `docker compose up` without disturbing a stack that is already running.
//
// It never reads or writes the repository's own .env. That file holds the
// operator's venue API credentials and is what internal/testenv.LoadRepoEnv
// feeds to the test harness; compose auto-loads a root .env into its own
// interpolation unless told otherwise, so the stack's variables go to .env.stack
// instead and every compose invocation names that file with --env-file. The two
// never meet.
package devsetup

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	// Mode is the only mode this stack runs in, and it is a path component as
	// well as a value: cmd/trading and cmd/authedge refuse a token file whose
	// parent directory is not named for the mode the process is running as,
	// which is what stops a live process from starting on these credentials.
	Mode = "test"

	// SecretDirName holds everything this package generates except the env
	// file, so "start over" is `rm -rf .secrets .env.stack`.
	SecretDirName = ".secrets"

	// SecretStoreDirName is the gateway's encrypted venue-credential store, and
	// it is the one directory in the stack a service writes to. It sits beside
	// the mode directory rather than inside it because that mount is read-only:
	// the tokens are given to the services, whereas this is state the gateway
	// owns.
	//
	// It is a host directory rather than a named volume because every container
	// runs as the host uid, and Docker creates a named volume owned by root —
	// which the gateway could not write to.
	SecretStoreDirName = "store"

	// EnvStackName is deliberately not ".env" — see the package comment.
	EnvStackName = ".env.stack"

	// HostServiceFileName is the host-side twin of docker/pg_service.conf. The
	// committed file names the compose service `postgres`, which resolves only
	// inside the compose network; this one names the published loopback port,
	// under the same service names, so `make doctor` and the authority
	// acceptance test can connect from the host without a second vocabulary.
	HostServiceFileName = "pg_service.host.conf"
)

// The generated secret files. The first four are opaque tokens, the fifth is a
// key of a different shape, the sixth is derived, and the last two are one
// credential in two files.
const (
	TradingServiceTokenFile = "trading_service_token"
	GatewayAuthTokenFile    = "gateway_auth_token"
	// The gateway's two planes hold two tokens. This one opens /internal/v1 —
	// accounts, credentials, catalog, runtime state — and is deliberately not the
	// token that opens /v1. Generating one file and pointing both at it would
	// work and would quietly make the split decorative.
	GatewayInternalAuthTokenFile = "gateway_internal_auth_token"
	ManualPreviewSigningKeyFile  = "manual_preview_signing_key"
	SecretMasterKeyFile          = "secret_master_key"
	GatewayPostgresDSNFile       = "gateway_postgres_dsn"
	BootstrapPasswordFile        = "bootstrap_password"
	BootstrapPasswordHashFile    = "bootstrap_password_hash"
)

// gatewayDSN names the compose service and the in-network port, neither of which
// an operator can change without editing compose.yaml, so it is rewritten on
// every run rather than preserved. Nothing in it is secret — the cluster runs
// under trust authentication — but it lives beside the tokens so that one
// directory is the whole answer to "what did make up generate".
const gatewayDSN = "postgres://bolter_gateway_test@postgres:5432/bolter_gateway_test?sslmode=disable"

// Compose defaults, restated. Each also appears as a `${NAME:-default}` in
// compose.yaml; TestStackDefaultsMatchCompose pins the two together, because the
// host-side tooling that has to agree with compose reads these rather than
// re-deriving them.
const (
	defaultPostgresPort = "15432"
	defaultGatewayPort  = "18080"
	defaultAuthEdgePort = "18091"
	defaultTradingPort  = "18092"
	defaultUsername     = "operator"
)

// Environment variable names written into .env.stack. compose.yaml reads every
// one of them; the four without a `:-` default there are required, and a
// compose run without this file fails naming them rather than starting a
// service with no token.
const (
	hostUIDVar               = "BT_HOST_UID"
	hostGIDVar               = "BT_HOST_GID"
	secretDirVar             = "BT_SECRET_DIR"
	authTokenFileVar         = "GATEWAY_AUTH_TOKEN_FILE"
	internalAuthTokenFileVar = "GATEWAY_INTERNAL_AUTH_TOKEN_FILE"
	postgresDSNVar           = "GATEWAY_POSTGRES_DSN_FILE"
	postgresPortVar          = "POSTGRES_PORT"
	gatewayPortVar           = "GATEWAY_PORT"
	authEdgePortVar          = "AUTHEDGE_PORT"
	tradingPortVar           = "TRADING_PORT"
	bootstrapUserVar         = "BT_BOOTSTRAP_USERNAME"
)

// Config is the resolved set of values written into .env.stack.
type Config struct {
	PostgresPort      string
	GatewayPort       string
	AuthEdgePort      string
	TradingPort       string
	BootstrapUsername string
}

// Result reports what a Bootstrap run did, so the caller can tell the operator
// something more useful than "ok".
type Result struct {
	Config Config

	// Root is the absolute repository root everything below is relative to.
	Root string

	// SecretDir is the mode-scoped directory holding the six secret files.
	SecretDir string

	// EnvFile and ServiceFile are the two generated non-secret artifacts.
	EnvFile     string
	ServiceFile string

	// Created names the secret files this run generated, in a stable order. An
	// empty slice means every credential already existed and was reused.
	Created []string

	// Password is the freshly generated bootstrap password, and it is non-empty
	// only on the run that created it. The operator needs it to log in and has
	// no other way to recover it, since only the bcrypt hash is kept in a form
	// the stack reads.
	Password string
}

// Bootstrap generates everything the compose stack needs and reports what it
// did. It is safe to run on every `make up`: an existing credential is reused
// untouched, because rotating a token under a running stack turns every
// service-to-service call into a 401 that reads exactly like a bug in the auth
// code.
func Bootstrap(root string) (Result, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve repository root: %w", err)
	}
	// compose's .env parser expands $VAR inside values, so a checkout path
	// containing one would reach compose mangled and the secret mounts would
	// resolve somewhere else entirely. Refusing by name beats debugging that.
	if strings.ContainsRune(root, '$') {
		return Result{}, fmt.Errorf("repository path %q contains '$', which compose's env-file parser expands; move the checkout", root)
	}

	uid, gid := os.Getuid(), os.Getgid()
	if uid < 0 || gid < 0 {
		return Result{}, fmt.Errorf("this platform reports no uid/gid; set %s and %s by hand", hostUIDVar, hostGIDVar)
	}

	config, err := resolveConfig(filepath.Join(root, EnvStackName))
	if err != nil {
		return Result{}, err
	}

	secretDir := filepath.Join(root, SecretDirName, Mode)
	if err := ensureDir(filepath.Join(root, SecretDirName)); err != nil {
		return Result{}, err
	}
	if err := ensureDir(secretDir); err != nil {
		return Result{}, err
	}
	// Created here rather than left to the gateway because Docker would create
	// the missing bind-mount source itself, owned by root, and every container
	// runs as the host uid — so the gateway's first write would fail on a
	// directory it appears to own.
	if err := ensureDir(filepath.Join(root, SecretDirName, SecretStoreDirName)); err != nil {
		return Result{}, err
	}

	result := Result{
		Config:      config,
		Root:        root,
		SecretDir:   secretDir,
		EnvFile:     filepath.Join(root, EnvStackName),
		ServiceFile: filepath.Join(root, SecretDirName, HostServiceFileName),
	}

	for _, name := range []string{TradingServiceTokenFile, GatewayAuthTokenFile, GatewayInternalAuthTokenFile, ManualPreviewSigningKeyFile} {
		created, err := ensureSecret(filepath.Join(secretDir, name), newToken)
		if err != nil {
			return Result{}, err
		}
		if created {
			result.Created = append(result.Created, name)
		}
	}

	// Separate from the loop above because its generator is different, not
	// because its lifecycle is: it is created once and then reused, and
	// regenerating it would orphan every credential already encrypted under it.
	created, err := ensureSecret(filepath.Join(secretDir, SecretMasterKeyFile), newMasterKey)
	if err != nil {
		return Result{}, err
	}
	if created {
		result.Created = append(result.Created, SecretMasterKeyFile)
	}

	// Derived, not generated: rewritten every run so it cannot drift from the
	// compose network it names.
	if err := putSecret(filepath.Join(secretDir, GatewayPostgresDSNFile), gatewayDSN); err != nil {
		return Result{}, err
	}

	password, err := ensureBootstrapCredential(secretDir)
	if err != nil {
		return Result{}, err
	}
	if password != "" {
		result.Password = password
		result.Created = append(result.Created, BootstrapPasswordFile, BootstrapPasswordHashFile)
	}

	if err := writeFile(result.EnvFile, renderEnvStack(secretDir, config, uid, gid), 0o600); err != nil {
		return Result{}, err
	}
	if err := writeFile(result.ServiceFile, renderHostServiceFile(config.PostgresPort), 0o644); err != nil {
		return Result{}, err
	}
	return result, nil
}

// ensureBootstrapCredential generates the operator's password and its bcrypt
// hash, and returns the plaintext only when it created them.
//
// The asymmetry here is deliberate. A missing hash means there is nothing for
// cmd/authedge to authenticate against, so both files are regenerated. A missing
// plaintext means the operator deleted it after storing it somewhere, which is
// the right thing to do and must not silently cost them their login — so the
// hash is left alone. Resetting a forgotten password is therefore `rm` on the
// hash, which is the file whose absence actually means something.
func ensureBootstrapCredential(secretDir string) (string, error) {
	hashPath := filepath.Join(secretDir, BootstrapPasswordHashFile)
	passwordPath := filepath.Join(secretDir, BootstrapPasswordFile)

	switch info, err := os.Lstat(hashPath); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s is not a regular file; remove it and re-run", hashPath)
		}
		if err := os.Chmod(hashPath, 0o600); err != nil {
			return "", err
		}
		if _, err := os.Lstat(passwordPath); err == nil {
			return "", os.Chmod(passwordPath, 0o600)
		}
		return "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("stat %s: %w", hashPath, err)
	}

	password, err := newPassword()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash bootstrap password: %w", err)
	}
	// The hash is written first. If the process dies between the two writes the
	// stack still starts, and the operator resets with `rm` on the hash; the
	// reverse order would leave a plaintext password that authenticates nothing.
	if err := putSecret(hashPath, string(hash)); err != nil {
		return "", err
	}
	if err := putSecret(passwordPath, password); err != nil {
		return "", err
	}
	return password, nil
}

// ensureSecret creates a secret only if it is absent, and returns whether it
// did. An existing file is left byte-for-byte alone, but its permissions are
// tightened unconditionally: readProtectedToken accepts nothing but 0600, and a
// fresh checkout or a `cp` is enough to loosen them into a startup failure whose
// message is about permissions rather than about what the operator did.
func ensureSecret(path string, generate func() (string, error)) (bool, error) {
	switch info, err := os.Lstat(path); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("%s is not a regular file; remove it and re-run", path)
		}
		return false, os.Chmod(path, 0o600)
	case !errors.Is(err, fs.ErrNotExist):
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	value, err := generate()
	if err != nil {
		return false, err
	}
	return true, putSecret(path, value)
}

// putSecret writes a secret at 0600 unconditionally. The explicit Chmod is not
// redundant with the mode passed to OpenFile: that mode applies only when the
// file is created, and is masked by the process umask even then.
func putSecret(path, value string) error {
	return writeFile(path, value, 0o600)
}

func writeFile(path, contents string, mode fs.FileMode) error {
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// newToken returns 64 characters drawn from 48 random bytes. base64url is the
// alphabet on purpose: every character it emits is printable and none is
// whitespace, which are exactly the two properties readProtectedToken checks
// after length.
func newToken() (string, error) {
	return randomString(48)
}

// newPassword is shorter than a token because a human types it. 18 random bytes
// is 144 bits — far past what bcrypt at the default cost needs to resist — and
// the identity it unlocks lives in a test database behind a loopback-only port.
func newPassword() (string, error) {
	return randomString(18)
}

// newMasterKey is hex rather than base64url, and 32 bytes rather than 48, only
// because secretstore.FileKeySource says so: it hex-decodes the file into a
// 32-byte AES-GCM key and rejects every other shape. A token-shaped value here
// is also 64 characters and also passes doctor's printable-byte rule, so the
// mistake would surface as an unexplained gateway startup failure rather than
// as a bad secret.
func newMasterKey() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func randomString(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// resolveConfig picks each value from the process environment, then from the
// .env.stack a previous run wrote, then from the compose default.
//
// The middle step is what makes `POSTGRES_PORT=15433 make up` stick. Without it
// the next plain `make up` would quietly move the stack back to 15432 while
// doctor and the migrate targets kept probing the port the operator chose, and
// the resulting "connection refused" would name neither.
func resolveConfig(envStackPath string) (Config, error) {
	previous := readEnvStack(envStackPath)
	config := Config{
		PostgresPort:      resolve(postgresPortVar, previous, defaultPostgresPort),
		GatewayPort:       resolve(gatewayPortVar, previous, defaultGatewayPort),
		AuthEdgePort:      resolve(authEdgePortVar, previous, defaultAuthEdgePort),
		TradingPort:       resolve(tradingPortVar, previous, defaultTradingPort),
		BootstrapUsername: resolve(bootstrapUserVar, previous, defaultUsername),
	}
	for _, port := range []struct {
		name  string
		value string
	}{
		{postgresPortVar, config.PostgresPort},
		{gatewayPortVar, config.GatewayPort},
		{authEdgePortVar, config.AuthEdgePort},
		{tradingPortVar, config.TradingPort},
	} {
		number, err := strconv.Atoi(port.value)
		if err != nil || number < 1 || number > 65535 {
			return Config{}, fmt.Errorf("%s must be a TCP port between 1 and 65535, got %q", port.name, port.value)
		}
	}
	// The username is written into .env.stack unquoted and interpolated by
	// compose, so the two characters that would change its meaning there are
	// refused here rather than producing an operator who cannot log in.
	if config.BootstrapUsername == "" || strings.ContainsAny(config.BootstrapUsername, "$\n") {
		return Config{}, fmt.Errorf("%s must be a non-empty name without '$' or a newline, got %q", bootstrapUserVar, config.BootstrapUsername)
	}
	return config, nil
}

func resolve(name string, previous map[string]string, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	if value := strings.TrimSpace(previous[name]); value != "" {
		return value
	}
	return fallback
}

// readEnvStack parses a .env.stack written by a previous run. It understands
// only what renderEnvStack emits — comments, blank lines, and unquoted
// KEY=VALUE — so it is not a general dotenv reader, and it deliberately performs
// no `$` expansion. A file it cannot make sense of yields no values rather than
// an error: the caller's fallback is the compose default, which is always safe.
func readEnvStack(path string) map[string]string {
	values := map[string]string{}
	contents, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return values
}

func renderEnvStack(secretDir string, config Config, uid, gid int) string {
	var out strings.Builder
	fmt.Fprintf(&out, `# Generated by 'make up' (internal/devsetup). Do not edit and do not commit.
# Every value here is re-resolved on each run from the environment, from this
# file, then from the default in compose.yaml — so an edit survives, but only
# until something else sets the same variable.
#
# This file exists so that compose never auto-loads the repository's .env, which
# holds venue API credentials that no container has any business seeing. Every
# compose invocation passes --env-file %s.

# The host identity that owns %s. Every container runs as it: the secret
# files are bind-mounted with the host's own mode and owner, and they are 0600,
# so a container running as anyone else cannot open its own token.
%s=%d
%s=%d

# Where the bind mount finds the secrets, relative to this file.
%s=./%s

# The gateway receives these three through compose's own 'secrets:' mechanism,
# which takes a host path rather than a container one. The two token paths name
# files that trading and authedge also read, through the bind mount above — the
# same bytes, two delivery mechanisms, because the gateway's shim resolves
# 'env://' refs from /run/secrets while the other services read a mode-scoped
# directory. Generating separate files for the two mechanisms would produce a 401
# between services that looks exactly like a bug in the auth code.
#
# The two tokens are different from each other, though, and deliberately so: the
# first opens /v1 (orders, fills, market data) and the second opens /internal/v1
# (accounts, credentials, runtime state).
%s=%s
%s=%s
%s=%s

# Published ports. compose binds every one of them to 127.0.0.1.
%s=%s
%s=%s
%s=%s
%s=%s

# The bootstrap operator's name. Not a secret; the password is never here in any
# form, only its bcrypt hash in %s.
%s=%s
`,
		EnvStackName,
		filepath.Join(SecretDirName, Mode),
		hostUIDVar, uid,
		hostGIDVar, gid,
		secretDirVar, SecretDirName,
		authTokenFileVar, filepath.Join(secretDir, GatewayAuthTokenFile),
		internalAuthTokenFileVar, filepath.Join(secretDir, GatewayInternalAuthTokenFile),
		postgresDSNVar, filepath.Join(secretDir, GatewayPostgresDSNFile),
		postgresPortVar, config.PostgresPort,
		gatewayPortVar, config.GatewayPort,
		authEdgePortVar, config.AuthEdgePort,
		tradingPortVar, config.TradingPort,
		filepath.Join(SecretDirName, Mode, BootstrapPasswordHashFile),
		bootstrapUserVar, config.BootstrapUsername,
	)
	return out.String()
}

// renderHostServiceFile emits the same two libpq service names as
// docker/pg_service.conf, pointed at the published loopback port instead of the
// compose service name. Keeping the names identical is the point: a caller
// selects the file with PGSERVICEFILE and never has to know which side of the
// network boundary it is on.
func renderHostServiceFile(postgresPort string) string {
	return fmt.Sprintf(`# Generated by 'make up' (internal/devsetup). Do not edit and do not commit.
#
# The host-side twin of docker/pg_service.conf. That file names the compose
# service 'postgres', which resolves only inside the compose network; this one
# names the port compose publishes to loopback, under the same service names, so
# 'make doctor' and the authority acceptance test select it with PGSERVICEFILE
# and otherwise read identically to the in-network callers.
#
# No password appears here and none is needed: the cluster runs under trust
# authentication on a port published only to 127.0.0.1.

# The gateway migrator's identity. It carries no dbname on purpose —
# cmd/gateway-migrate appends dbname=bolter_gateway_test itself and then asserts
# current_database() matches, so a dbname here would be a second place for that
# fact to live and the wrong one to win.
[gateway_test]
host=127.0.0.1
port=%s
user=bolter_gateway_test_migrator
sslmode=disable

# The gateway's serving identity, used only by the authority acceptance test:
# reading the runtime's effective privileges back as the migrator would prove
# nothing about the runtime.
[gateway_runtime_test]
host=127.0.0.1
port=%s
user=bolter_gateway_test
dbname=bolter_gateway_test
sslmode=disable
`, postgresPort, postgresPort)
}

// FindRepoRoot walks up from dir until it finds the directory holding go.mod.
//
// internal/testenv has the same walk and keeps it unexported. Copying six lines
// is cheaper than exporting it: importing testenv from here would pull godotenv
// and the whole venue-credential surface into a tool whose entire job is to
// avoid touching credentials.
func FindRepoRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found at or above %s", dir)
		}
		dir = parent
	}
}
