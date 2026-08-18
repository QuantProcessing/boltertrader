package devsetup

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

// secretFiles is every file Bootstrap puts in .secrets/stack/. It is written
// out here rather than derived from the package's own constants so that deleting
// one from Bootstrap fails this test instead of quietly shrinking it.
var secretFiles = []string{
	"trading_service_token",
	"gateway_auth_token",
	"manual_preview_signing_key",
	"secret_master_key",
	"gateway_postgres_dsn",
	"bootstrap_password",
	"bootstrap_password_hash",
}

// protectedTokenFiles is the subset that reaches readProtectedToken in
// cmd/trading or cmd/authedge, and so has to satisfy its length and character
// rules on top of the permissions every file here gets.
//
// The three excluded files are excluded for three different reasons, and none is
// an oversight. gateway_postgres_dsn is read by the gateway shim's readSecret,
// which requires only that the file be non-empty — no length, no charset, no
// permission check. bootstrap_password is read by nothing at all: it exists so
// the operator can look up the password `make up` printed once, and holding it
// to a 32-byte minimum would mean generating a longer password than a human
// wants to type in order to satisfy a rule that never applies to it.
// secret_master_key would pass these rules and still be wrong, so it is held to
// its own, stricter one below.
var protectedTokenFiles = []string{
	"trading_service_token",
	"gateway_auth_token",
	"manual_preview_signing_key",
	"bootstrap_password_hash",
}

// bootstrapInto runs Bootstrap against a scratch root with the stack's variables
// cleared, so an ambient POSTGRES_PORT in the developer's shell cannot change
// what any test observes.
func bootstrapInto(t *testing.T, root string) Result {
	t.Helper()
	for _, name := range []string{postgresPortVar, gatewayPortVar, authEdgePortVar, tradingPortVar, webPortVar, bootstrapUserVar} {
		t.Setenv(name, "")
	}
	result, err := Bootstrap(root)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return result
}

func readAll(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(contents)
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// parseEnvFile is deliberately a second implementation of readEnvStack. Reading
// the generated file back with the same parser that wrote it would prove only
// that the two agree, not that the file says what compose will read.
func parseEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for _, line := range strings.Split(readAll(t, path), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			t.Fatalf("unparseable line in %s: %q", path, line)
		}
		values[key] = value
	}
	return values
}

// Every property checked here is checked again by readProtectedToken in
// cmd/trading and cmd/authedge before either process will start, so a generator
// that gets any of them wrong produces a stack that fails at boot with a message
// about file permissions rather than about what was actually generated.
func TestBootstrapGeneratesCredentialsTheServicesWillAccept(t *testing.T) {
	root := t.TempDir()
	result := bootstrapInto(t, root)

	if got := modeOf(t, result.SecretDir); got != 0o700 {
		t.Errorf("secret directory mode = %o, want 700", got)
	}

	// The store must be a sibling of this directory, never inside it. Trading
	// and the auth edge bind-mount SecretDir read-only to get their tokens, so a
	// store nested under it would hand both services a view of every registered
	// venue credential — which neither has any business seeing.
	//
	// This level used to be named for BT_MODE, and the assertion here was that
	// it carried that name. Nothing distinguishes one mode from another now, but
	// the level itself still separates the two mounts, so the check moved to the
	// property that survived.
	store := filepath.Join(result.Root, SecretDirName, SecretStoreDirName)
	if got := modeOf(t, store); got != 0o700 {
		t.Errorf("secret store mode = %o, want 700", got)
	}
	if strings.HasPrefix(store+string(filepath.Separator), result.SecretDir+string(filepath.Separator)) {
		t.Errorf("secret store %q sits inside the read-only mount %q", store, result.SecretDir)
	}

	// 0600 and no padding hold for every file, including the two no service
	// reads through readProtectedToken: the mode because loosening any of them
	// is the failure this generator exists to prevent, and the padding because
	// the gateway shim trims exactly one trailing newline and nothing else, so
	// stray whitespace would survive into a DSN.
	for _, name := range secretFiles {
		path := filepath.Join(result.SecretDir, name)
		if got := modeOf(t, path); got != 0o600 {
			t.Errorf("%s mode = %o, want 600", name, got)
		}
		if contents := readAll(t, path); strings.TrimSpace(contents) != contents {
			t.Errorf("%s is padded with whitespace", name)
		}
	}

	for _, name := range protectedTokenFiles {
		contents := readAll(t, filepath.Join(result.SecretDir, name))
		if len(contents) < 32 {
			t.Errorf("%s is %d bytes, under the 32 readProtectedToken requires", name, len(contents))
		}
		for index, value := range []byte(contents) {
			if value < 0x21 || value > 0x7e {
				t.Fatalf("%s byte %d is %#x, outside the printable range readProtectedToken accepts", name, index, value)
			}
		}
	}
}

// The master key is asserted by loading it, not by re-checking its shape. Every
// other file here is consumed by a package main that cannot be imported, so its
// rules have to be restated; this one is consumed by secretstore, so the test
// can put the real reader in the loop. The failure it guards is specific: a
// base64url token is also 64 characters, also passes every rule above, and
// leaves the gateway refusing to start with a message that names no file.
func TestBootstrapMasterKeyLoadsThroughTheGatewaysOwnKeySource(t *testing.T) {
	result := bootstrapInto(t, t.TempDir())

	key, err := secretstore.FileKeySource{Path: filepath.Join(result.SecretDir, "secret_master_key")}.Load(t.Context())
	if err != nil {
		t.Fatalf("the gateway's key source rejects the key bootstrap generated: %v", err)
	}
	if len(key) != 32 {
		t.Errorf("loaded key is %d bytes, want the 32 AES-GCM takes", len(key))
	}
}

func TestBootstrapHashAuthenticatesThePasswordItPrints(t *testing.T) {
	root := t.TempDir()
	result := bootstrapInto(t, root)

	if result.Password == "" {
		t.Fatal("expected the first run to report the generated password; it is the operator's only readable copy")
	}
	hash := readAll(t, filepath.Join(result.SecretDir, BootstrapPasswordHashFile))
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(result.Password)); err != nil {
		t.Fatalf("the generated hash does not authenticate the reported password: %v", err)
	}
	stored := readAll(t, filepath.Join(result.SecretDir, BootstrapPasswordFile))
	if stored != result.Password {
		t.Fatalf("stored password %q does not match the reported one", stored)
	}
}

// Rotating a token under a running stack turns every service-to-service call
// into a 401, which reads exactly like a bug in the auth code. `make up` runs
// this generator on every invocation, so reuse is not an optimisation.
func TestBootstrapReusesEveryExistingCredential(t *testing.T) {
	root := t.TempDir()
	first := bootstrapInto(t, root)

	before := map[string]string{}
	for _, name := range secretFiles {
		before[name] = readAll(t, filepath.Join(first.SecretDir, name))
	}

	second := bootstrapInto(t, root)
	if len(second.Created) != 0 {
		t.Errorf("second run created %v, want nothing", second.Created)
	}
	if second.Password != "" {
		t.Error("second run reported a password; only the run that generates one may print it")
	}
	for _, name := range secretFiles {
		if after := readAll(t, filepath.Join(second.SecretDir, name)); after != before[name] {
			t.Errorf("%s changed across runs", name)
		}
	}
}

// A checkout, a `cp`, or an editor is enough to loosen 0600 into a startup
// failure. Tightening is unambiguous — it can only ever reduce access — so it
// happens without asking, and without touching the credential itself.
func TestBootstrapTightensLoosenedPermissionsWithoutRotating(t *testing.T) {
	root := t.TempDir()
	first := bootstrapInto(t, root)

	path := filepath.Join(first.SecretDir, GatewayAuthTokenFile)
	before := readAll(t, path)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("loosen permissions: %v", err)
	}

	second := bootstrapInto(t, root)
	if got := modeOf(t, path); got != 0o600 {
		t.Errorf("mode after re-run = %o, want 600", got)
	}
	if after := readAll(t, path); after != before {
		t.Error("tightening permissions must not rotate the token")
	}
	if len(second.Created) != 0 {
		t.Errorf("re-run created %v, want nothing", second.Created)
	}
}

// Deleting the plaintext after storing it elsewhere is the right thing for an
// operator to do, and it must not cost them their login. The hash is the file
// whose absence means something.
func TestBootstrapKeepsTheHashWhenOnlyThePlaintextIsDeleted(t *testing.T) {
	root := t.TempDir()
	first := bootstrapInto(t, root)
	hashPath := filepath.Join(first.SecretDir, BootstrapPasswordHashFile)
	passwordPath := filepath.Join(first.SecretDir, BootstrapPasswordFile)
	hash := readAll(t, hashPath)

	if err := os.Remove(passwordPath); err != nil {
		t.Fatalf("remove password: %v", err)
	}

	second := bootstrapInto(t, root)
	if after := readAll(t, hashPath); after != hash {
		t.Fatal("the hash was rotated, invalidating a login the operator had already stored")
	}
	if second.Password != "" {
		t.Error("no password was generated, so none may be reported")
	}
	if _, err := os.Stat(passwordPath); err == nil {
		t.Error("the plaintext was recreated; it cannot be, since only the hash survives")
	}
}

// The documented reset. Removing the hash leaves nothing for cmd/authedge to
// authenticate against, so both halves are regenerated together and the new
// password is printed.
func TestBootstrapRegeneratesBothHalvesWhenTheHashIsDeleted(t *testing.T) {
	root := t.TempDir()
	first := bootstrapInto(t, root)
	hashPath := filepath.Join(first.SecretDir, BootstrapPasswordHashFile)

	if err := os.Remove(hashPath); err != nil {
		t.Fatalf("remove hash: %v", err)
	}

	second := bootstrapInto(t, root)
	if second.Password == "" {
		t.Fatal("expected a fresh password to be reported after the reset")
	}
	if second.Password == first.Password {
		t.Fatal("the reset produced the same password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(readAll(t, hashPath)), []byte(second.Password)); err != nil {
		t.Fatalf("the regenerated pair does not match: %v", err)
	}
}

// One token reaches the gateway through compose's `secrets:` mechanism and the
// other two services through the bind mount. They must be the same file:
// generating a second one would authenticate the gateway against a token nobody
// else holds, and the resulting 401 names neither file.
func TestEnvStackPointsAtTheSameTokenTheBindMountServes(t *testing.T) {
	root := t.TempDir()
	result := bootstrapInto(t, root)
	values := parseEnvFile(t, result.EnvFile)

	want := filepath.Join(result.SecretDir, GatewayAuthTokenFile)
	if values[authTokenFileVar] != want {
		t.Errorf("%s = %q, want %q", authTokenFileVar, values[authTokenFileVar], want)
	}
	if values[postgresDSNVar] != filepath.Join(result.SecretDir, GatewayPostgresDSNFile) {
		t.Errorf("%s = %q, want the generated DSN file", postgresDSNVar, values[postgresDSNVar])
	}
	if _, err := os.Stat(values[authTokenFileVar]); err != nil {
		t.Errorf("%s names a file compose cannot mount: %v", authTokenFileVar, err)
	}
}

// compose expands $VAR inside env-file values. Nothing this generator emits may
// contain one, or compose will substitute it — silently, and to empty.
func TestEnvStackSurvivesComposeInterpolation(t *testing.T) {
	root := t.TempDir()
	result := bootstrapInto(t, root)

	for key, value := range parseEnvFile(t, result.EnvFile) {
		if strings.ContainsRune(value, '$') {
			t.Errorf("%s=%q contains '$', which compose would expand", key, value)
		}
	}
}

// The variables compose declares with `:?` have no default: a compose run
// without them fails outright, including on metadata-only subcommands like
// `docker compose port`. Every one of them has to come out of this generator.
func TestEnvStackSatisfiesEveryRequiredComposeVariable(t *testing.T) {
	root := t.TempDir()
	result := bootstrapInto(t, root)
	values := parseEnvFile(t, result.EnvFile)

	required := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*):\?`)
	compose := readAll(t, filepath.Join(repoRoot(t), "compose.yaml"))
	matches := required.FindAllStringSubmatch(compose, -1)
	if len(matches) == 0 {
		t.Fatal("found no required variables in compose.yaml; the pattern has drifted")
	}
	for _, match := range matches {
		if _, ok := values[match[1]]; !ok {
			t.Errorf("compose.yaml requires %s but .env.stack does not set it", match[1])
		}
	}
}

// The defaults below are restated from compose.yaml so host-side tooling reads
// one value instead of re-deriving it. This is the guard that keeps the two
// copies honest.
func TestStackDefaultsMatchCompose(t *testing.T) {
	optional := regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*):-([^}]*)\}`)
	compose := readAll(t, filepath.Join(repoRoot(t), "compose.yaml"))

	defaults := map[string]string{}
	for _, match := range optional.FindAllStringSubmatch(compose, -1) {
		defaults[match[1]] = match[2]
	}
	for name, want := range map[string]string{
		postgresPortVar:  defaultPostgresPort,
		gatewayPortVar:   defaultGatewayPort,
		authEdgePortVar:  defaultAuthEdgePort,
		tradingPortVar:   defaultTradingPort,
		webPortVar:       defaultWebPort,
		bootstrapUserVar: defaultUsername,
		secretDirVar:     "./" + SecretDirName,
	} {
		got, ok := defaults[name]
		if !ok {
			t.Errorf("compose.yaml no longer defaults %s", name)
			continue
		}
		if got != want {
			t.Errorf("compose.yaml defaults %s to %q, this package uses %q", name, got, want)
		}
	}
}

// Without this, a plain `make up` after `POSTGRES_PORT=15433 make up` would move
// the stack back to 15432 while doctor and the migrate targets kept probing the
// port the operator chose, and the "connection refused" would name neither.
func TestBootstrapRemembersAPortChosenOnce(t *testing.T) {
	root := t.TempDir()

	t.Setenv(postgresPortVar, "15433")
	if _, err := Bootstrap(root); err != nil {
		t.Fatalf("bootstrap with an explicit port: %v", err)
	}

	second := bootstrapInto(t, root)
	if second.Config.PostgresPort != "15433" {
		t.Errorf("port after a plain re-run = %q, want the 15433 chosen earlier", second.Config.PostgresPort)
	}
	if !strings.Contains(readAll(t, second.ServiceFile), "port=15433") {
		t.Error("the generated libpq service file still names the default port")
	}
}

func TestBootstrapRejectsAPortItCannotUse(t *testing.T) {
	for _, port := range []string{"0", "70000", "5432/tcp", "abc"} {
		t.Setenv(postgresPortVar, port)
		_, err := Bootstrap(t.TempDir())
		if err == nil {
			t.Fatalf("port %q was accepted", port)
		}
		if !strings.Contains(err.Error(), postgresPortVar) {
			t.Fatalf("error for %q must name the variable, got: %v", port, err)
		}
	}
}

// The whole reason .env.stack exists rather than .env: that file holds the
// operator's venue API credentials, compose would load it into its own
// interpolation, and internal/testenv reads it as the test harness's input.
func TestBootstrapLeavesTheRepositoryDotEnvAlone(t *testing.T) {
	root := t.TempDir()
	dotEnv := filepath.Join(root, ".env")
	const contents = "BINANCE_API_KEY=not-a-real-key\n"
	if err := os.WriteFile(dotEnv, []byte(contents), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	result := bootstrapInto(t, root)
	if after := readAll(t, dotEnv); after != contents {
		t.Fatal(".env was modified")
	}
	if result.EnvFile == dotEnv {
		t.Fatal("the generated env file is .env itself")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := FindRepoRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	return root
}
