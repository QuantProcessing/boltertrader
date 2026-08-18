package config_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/config"
)

func TestLoadStrictYAMLAcceptsOperationalOnlyConfig(t *testing.T) {
	file, err := config.LoadYAML([]byte(validYAML()))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	if file.Server.Listen != "127.0.0.1:18080" {
		t.Fatalf("server = %+v", file.Server)
	}
	if file.Server.AuthTokenRef.IsZero() || file.Postgres.DSNRef.IsZero() {
		t.Fatal("operational secret references were not decoded")
	}
}

func TestLoadStrictYAMLRejectsTopLevelAccounts(t *testing.T) {
	_, err := config.LoadYAML([]byte(validYAML() + "accounts: []\n"))
	if err == nil {
		t.Fatal("LoadYAML accepted static account inventory")
	}
	message := strings.ToLower(err.Error())
	if !strings.Contains(message, "unknown") || !strings.Contains(message, "accounts") {
		t.Fatalf("error = %v, want strict unknown accounts field", err)
	}
}

func TestSecretRefsContainOnlyOperationalAuthAndDSN(t *testing.T) {
	file, err := config.LoadYAML([]byte(strings.Replace(
		validYAML(),
		"  auth_token_ref: env://TOKEN",
		"  auth_token_ref: env://TOKEN\n  internal_listen: 127.0.0.1:18082\n  internal_auth_token_ref: env://INTERNAL_TOKEN",
		1,
	)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ref := range file.SecretRefs() {
		got = append(got, ref.RawForProvider())
	}
	want := []string{"env://TOKEN", "env://POSTGRES_DSN", "env://INTERNAL_TOKEN"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SecretRefs = %v, want %v", got, want)
	}
}

func TestHashAndFormattingContainNoAccountOrCredentialInventory(t *testing.T) {
	file, err := config.LoadYAML([]byte(validYAML()))
	if err != nil {
		t.Fatal(err)
	}
	hashA, err := file.Hash()
	if err != nil {
		t.Fatal(err)
	}
	changed, err := config.LoadYAML([]byte(strings.Replace(validYAML(), "127.0.0.1:18080", "127.0.0.1:18081", 1)))
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := changed.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if hashA == hashB {
		t.Fatal("hash ignored operational listen change")
	}
	payload, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{string(payload), fmt.Sprint(file), fmt.Sprintf("%#v", file)} {
		lower := strings.ToLower(output)
		if strings.Contains(lower, "account") || strings.Contains(lower, "credential") || strings.Contains(output, "env://") {
			t.Fatalf("config output contains removed inventory/reference material: %s", output)
		}
		if !strings.Contains(lower, "redacted") {
			t.Fatalf("config output is not visibly redacted: %s", output)
		}
	}
}

// TestValidateRuntimeIdentityRejectsUnreachableEndpoints keeps the guard that
// used to be a cross-mode check.
//
// It once proved a test-mode process would not adopt a live account and vice
// versa. Modes are gone, but the half of that check which was never about modes
// still matters: only live endpoints exist, so an account row naming demo or
// testnet describes a venue nothing can dial. Such a row is written by an older
// binary and read back by this one, so it has to be refused here rather than
// surface later as an unreachable exchange.
func TestValidateRuntimeIdentityRejectsUnreachableEndpoints(t *testing.T) {
	for _, test := range []struct{ name, venue, environment string }{
		{name: "demo", venue: "binance", environment: "demo"},
		{name: "testnet", venue: "bybit", environment: "testnet"},
		{name: "empty", venue: "binance", environment: ""},
		{name: "unknown venue", venue: "notavenue", environment: "live"},
	} {
		t.Run("rejects_"+test.name, func(t *testing.T) {
			if err := config.ValidateRuntimeIdentity("acct_1", test.venue, test.environment); err == nil {
				t.Fatal("unreachable runtime identity accepted")
			}
		})
	}
	if err := config.ValidateRuntimeIdentity("acct_1", "binance", "live"); err != nil {
		t.Fatalf("live runtime identity rejected: %v", err)
	}
}

func TestNonLoopbackListenRequiresExplicitOptIn(t *testing.T) {
	nonLoopback := strings.Replace(validYAML(), "127.0.0.1:18080", "0.0.0.0:18080", 1)
	if _, err := config.LoadYAML([]byte(nonLoopback)); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("non-loopback error = %v", err)
	}
	optedIn := strings.Replace(nonLoopback, "  listen: 0.0.0.0:18080", "  listen: 0.0.0.0:18080\n  allow_non_loopback_listen: true", 1)
	if _, err := config.LoadYAML([]byte(optedIn)); err != nil {
		t.Fatalf("opted-in config: %v", err)
	}
}

// TestNonLoopbackInternalListenRequiresItsOwnOptIn pins the property that makes
// the two opt-ins worth having separately: widening the capability plane must
// not widen the authority plane. A single flag governing both would pass the
// first half of this test and silently publish accounts and credentials.
func TestNonLoopbackInternalListenRequiresItsOwnOptIn(t *testing.T) {
	withInternal := strings.Replace(validYAML(), "  auth_token_ref: env://TOKEN\n",
		"  auth_token_ref: env://TOKEN\n  internal_listen: 0.0.0.0:18082\n  internal_auth_token_ref: env://INTERNAL_TOKEN\n", 1)
	if _, err := config.LoadYAML([]byte(withInternal)); err == nil || !strings.Contains(err.Error(), "allow_non_loopback_internal_listen") {
		t.Fatalf("non-loopback internal_listen error = %v", err)
	}
	// The public opt-in alone must not unlock it.
	publicOnly := strings.Replace(withInternal, "  internal_listen:", "  allow_non_loopback_listen: true\n  internal_listen:", 1)
	if _, err := config.LoadYAML([]byte(publicOnly)); err == nil || !strings.Contains(err.Error(), "allow_non_loopback_internal_listen") {
		t.Fatalf("public opt-in widened the authority plane: %v", err)
	}
	optedIn := strings.Replace(publicOnly, "  internal_listen: 0.0.0.0:18082", "  internal_listen: 0.0.0.0:18082\n  allow_non_loopback_internal_listen: true", 1)
	file, err := config.LoadYAML([]byte(optedIn))
	if err != nil {
		t.Fatalf("opted-in internal config: %v", err)
	}
	if !file.Server.AllowNonLoopbackInternalListen || file.Server.InternalListen != "0.0.0.0:18082" {
		t.Fatalf("internal listener not carried through: %+v", file.Server)
	}
	// The opt-in has to reach the hash, or a stack that flipped it would
	// resume against a ledger entry recorded under the narrower configuration.
	loopback := strings.Replace(optedIn, "  allow_non_loopback_internal_listen: true\n", "", 1)
	loopback = strings.Replace(loopback, "internal_listen: 0.0.0.0:18082", "internal_listen: 127.0.0.1:18082", 1)
	narrower, err := config.LoadYAML([]byte(loopback))
	if err != nil {
		t.Fatalf("loopback internal config: %v", err)
	}
	wide, err := file.Hash()
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := narrower.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if wide == narrow {
		t.Fatal("hash does not distinguish a published authority plane from a loopback one")
	}
}

func validYAML() string {
	return `server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
`
}

func TestMigrationValidationStartupProfile(t *testing.T) {
	data := strings.Replace(validYAML(), "server:\n", "server:\n  startup_profile: migration-validation\n", 1)
	file, err := config.LoadYAML([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if file.Server.StartupProfile != config.StartupProfileMigrationValidation {
		t.Fatalf("startup profile = %q", file.Server.StartupProfile)
	}
	data = strings.Replace(validYAML(), "server:\n", "server:\n  startup_profile: unsafe\n", 1)
	if _, err := config.LoadYAML([]byte(data)); err == nil {
		t.Fatal("unknown startup profile succeeded")
	}
}

func TestDevPlatformConfigsDeclareNormalAndMigrationValidationProfiles(t *testing.T) {
	for name, want := range map[string]config.StartupProfile{
		"dev-platform.test.yaml":            config.StartupProfileNormal,
		"dev-platform.validation.test.yaml": config.StartupProfileMigrationValidation,
	} {
		data, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		file, err := config.LoadYAML(data)
		if err != nil {
			t.Fatal(err)
		}
		if file.Server.StartupProfile != want {
			t.Fatalf("%s server=%+v", name, file.Server)
		}
	}
}
