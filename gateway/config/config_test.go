package config_test

import (
	"encoding/json"
	"fmt"
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
	if file.Server.Mode != "test" || file.Server.Listen != "127.0.0.1:18080" {
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

func TestValidateRuntimeIdentityUsesTrustedProcessMode(t *testing.T) {
	for _, test := range []struct{ mode, venue, environment string }{
		{mode: "test", venue: "binance", environment: "live"},
		{mode: "live", venue: "binance", environment: "demo"},
		{mode: "live", venue: "bybit", environment: "testnet"},
	} {
		t.Run(test.mode+"_rejects_"+test.environment, func(t *testing.T) {
			if err := config.ValidateRuntimeIdentity(test.mode, "acct_1", test.venue, test.environment); err == nil {
				t.Fatal("cross-mode runtime identity accepted")
			}
		})
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
	if err != nil { t.Fatal(err) }
	if file.Server.StartupProfile != config.StartupProfileMigrationValidation { t.Fatalf("startup profile = %q", file.Server.StartupProfile) }
	data = strings.Replace(validYAML(), "server:\n", "server:\n  startup_profile: unsafe\n", 1)
	if _, err := config.LoadYAML([]byte(data)); err == nil { t.Fatal("unknown startup profile succeeded") }
}
