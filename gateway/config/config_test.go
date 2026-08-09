package config_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/config"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

const (
	hyperliquidAccountAddress = "0x1111111111111111111111111111111111111111"
	asterUserAddress          = "0x3333333333333333333333333333333333333333"
	asterExpectedSigner       = "0x4444444444444444444444444444444444444444"
)

func TestLoadStrictYAMLDecodesTypedVenueCredentialVariantsAndTwentyProfiles(t *testing.T) {
	file, err := config.LoadYAML([]byte(validTwentyAccountYAML()))
	if err != nil {
		t.Fatalf("LoadYAML valid twenty rows: %v", err)
	}
	if got := len(file.Accounts); got != 20 {
		t.Fatalf("accounts = %d, want 20", got)
	}

	assertCredType[config.APIKeySecretCredentials](t, file, "binance-spot")
	assertCredType[config.APIKeySecretCredentials](t, file, "binance-usdt-perp")
	assertCredType[config.APIKeySecretPassphraseCredentials](t, file, "okx-spot")
	assertCredType[config.APIKeySecretPassphraseCredentials](t, file, "okx-usdt-perp")
	assertCredType[config.LighterCredentials](t, file, "lighter-spot")
	assertCredType[config.LighterCredentials](t, file, "lighter-usdc-perp")
	assertCredType[config.PrivateKeyCredentials](t, file, "hyperliquid-spot")
	assertCredType[config.PrivateKeyCredentials](t, file, "hyperliquid-usdc-perp")
	assertCredType[config.APIKeySecretCredentials](t, file, "bybit-spot")
	assertCredType[config.APIKeySecretCredentials](t, file, "bybit-usdt-perp")
	assertCredType[config.APIKeySecretCredentials](t, file, "bybit-usdc-perp")
	assertCredType[config.APIKeySecretPassphraseCredentials](t, file, "bitget-spot")
	assertCredType[config.APIKeySecretPassphraseCredentials](t, file, "bitget-usdt-perp")
	assertCredType[config.APIKeySecretPassphraseCredentials](t, file, "bitget-usdc-perp")
	assertCredType[config.APIKeySecretCredentials](t, file, "gate-spot")
	assertCredType[config.APIKeySecretCredentials](t, file, "gate-usdt-perp")
	assertCredType[config.AsterCredentials](t, file, "aster-spot")
	assertCredType[config.AsterCredentials](t, file, "aster-usdt-perp")
	assertCredType[config.NadoCredentials](t, file, "nado-spot")
	assertCredType[config.NadoCredentials](t, file, "nado-usdt0-perp")

	hyperliquidSpot := credentialsFor[config.PrivateKeyCredentials](t, file, "hyperliquid-spot")
	if hyperliquidSpot.AccountAddress != hyperliquidAccountAddress {
		t.Fatalf("hyperliquid account address = %q, want %q", hyperliquidSpot.AccountAddress, hyperliquidAccountAddress)
	}
	hyperliquidPerp := credentialsFor[config.PrivateKeyCredentials](t, file, "hyperliquid-usdc-perp")
	if hyperliquidPerp.AccountAddress != "" {
		t.Fatalf("optional hyperliquid account address = %q, want empty", hyperliquidPerp.AccountAddress)
	}
	asterSpot := credentialsFor[config.AsterCredentials](t, file, "aster-spot")
	if asterSpot.UserAddress != asterUserAddress || asterSpot.ExpectedSigner != asterExpectedSigner {
		t.Fatalf("aster public identities = user %q signer %q", asterSpot.UserAddress, asterSpot.ExpectedSigner)
	}
	asterPerp := credentialsFor[config.AsterCredentials](t, file, "aster-usdt-perp")
	if asterPerp.ExpectedSigner != "" {
		t.Fatalf("optional aster expected signer = %q, want empty", asterPerp.ExpectedSigner)
	}

	if file.Server.Listen != "127.0.0.1:18080" {
		t.Fatalf("listen = %q", file.Server.Listen)
	}
	if file.Postgres.DSNRef.IsZero() || file.Server.AuthTokenRef.IsZero() {
		t.Fatalf("server/postgres secret refs were not decoded")
	}
}

func TestLoadStrictYAMLRejectsUnknownFieldsLiteralsDuplicatesAndBadProfiles(t *testing.T) {
	tests := []struct {
		name    string
		patch   string
		wantErr string
	}{
		{
			name: "unknown top-level field",
			patch: `
unexpected: true
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
accounts: []
`,
			wantErr: "unknown",
		},
		{
			name:    "literal auth token rejected",
			patch:   strings.Replace(validOneAccountYAML(), "env://TOKEN", "plain-token", 1),
			wantErr: "secret reference",
		},
		{
			name:    "duplicate account ids rejected",
			patch:   strings.Replace(validTwentyAccountYAML(), "account_id: binance-usdt-perp", "account_id: binance-spot", 1),
			wantErr: "duplicate account_id",
		},
		{
			name:    "unknown environment rejected",
			patch:   strings.Replace(validOneAccountYAML(), "environment: demo", "environment: paper", 1),
			wantErr: "environment",
		},
		{
			name:    "incompatible settlement rejected",
			patch:   strings.Replace(validOneAccountYAML(), "product: spot", "product: spot\n    settlement: USDC", 1),
			wantErr: "profile",
		},
		{
			name:    "unknown credentials field rejected",
			patch:   strings.Replace(validOneAccountYAML(), "api_secret_ref: env://BINANCE_SECRET", "api_secret_ref: env://BINANCE_SECRET\n      raw_secret: leaked", 1),
			wantErr: "unknown",
		},
		{
			name:    "key index overflow rejected",
			patch:   strings.Replace(validLighterYAML(), "key_index: 2", "key_index: 256", 1),
			wantErr: "key_index",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := config.LoadYAML([]byte(test.patch))
			if err == nil {
				t.Fatal("LoadYAML unexpectedly succeeded")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestValidateLegacyEmptyModeDefaultsGloballyToTest(t *testing.T) {
	base, err := config.LoadYAML([]byte(validOneAccountYAML()))
	if err != nil {
		t.Fatal(err)
	}
	base.Server.Mode = ""
	if err := base.Validate(); err != nil {
		t.Fatalf("empty mode with demo account: %v", err)
	}

	live := base
	live.Accounts = append([]config.AccountConfig(nil), base.Accounts...)
	live.Accounts[0].Environment = "live"
	if err := live.Validate(); err == nil {
		t.Fatal("empty mode accepted live profile instead of defaulting globally to Test")
	}

	live.Server.Mode = "test"
	if err := live.Validate(); err == nil {
		t.Fatal("explicit Test mode accepted live profile")
	}
	live.Server.Mode = "live"
	if err := live.Validate(); err != nil {
		t.Fatalf("explicit Live mode with live account: %v", err)
	}
}

func TestValidateRuntimeIdentityUsesTrustedProcessMode(t *testing.T) {
	for _, test := range []struct {
		mode, venue, environment string
	}{
		{mode: "test", venue: "binance", environment: "live"},
		{mode: "live", venue: "binance", environment: "demo"},
		{mode: "live", venue: "bybit", environment: "testnet"},
	} {
		t.Run(test.mode+"_rejects_"+test.environment, func(t *testing.T) {
			derivedMode := "test"
			if test.environment == "live" {
				derivedMode = "live"
			}
			if err := config.ValidateRuntimeIdentity(derivedMode, "acct_1", test.venue, test.environment); err != nil {
				t.Fatalf("old environment-derived mode precondition is invalid: %v", err)
			}
			if err := config.ValidateRuntimeIdentity(test.mode, "acct_1", test.venue, test.environment); err == nil {
				t.Fatal("cross-mode runtime identity accepted")
			}
		})
	}
}

func TestInternalAuthorityListenerRequiresPairedLoopbackAuthConfiguration(t *testing.T) {
	configured := strings.Replace(validOneAccountYAML(),
		"  auth_token_ref: env://TOKEN",
		"  auth_token_ref: env://TOKEN\n  internal_listen: 127.0.0.1:18082\n  internal_auth_token_ref: env://GATEWAY_ADAPTER_TOKEN", 1)
	file, err := config.LoadYAML([]byte(configured))
	if err != nil {
		t.Fatalf("LoadYAML configured internal authority: %v", err)
	}
	if file.Server.InternalListen != "127.0.0.1:18082" || file.Server.InternalAuthTokenRef.IsZero() {
		t.Fatalf("internal server config=%+v", file.Server)
	}
	if got := len(file.SecretRefs()); got != len(configuredSecretRefs(file))+1 {
		t.Fatalf("SecretRefs count=%d, want internal token included", got)
	}

	for name, yaml := range map[string]string{
		"missing token":    strings.Replace(validOneAccountYAML(), "  auth_token_ref: env://TOKEN", "  auth_token_ref: env://TOKEN\n  internal_listen: 127.0.0.1:18082", 1),
		"missing listener": strings.Replace(validOneAccountYAML(), "  auth_token_ref: env://TOKEN", "  auth_token_ref: env://TOKEN\n  internal_auth_token_ref: env://GATEWAY_ADAPTER_TOKEN", 1),
		"non-loopback":     strings.Replace(validOneAccountYAML(), "  auth_token_ref: env://TOKEN", "  auth_token_ref: env://TOKEN\n  internal_listen: 0.0.0.0:18082\n  internal_auth_token_ref: env://GATEWAY_ADAPTER_TOKEN", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.LoadYAML([]byte(yaml)); err == nil {
				t.Fatal("LoadYAML accepted unsafe internal authority config")
			}
		})
	}
}

func configuredSecretRefs(file config.File) []secret.Ref {
	refs := file.SecretRefs()
	result := make([]secret.Ref, 0, len(refs))
	for _, ref := range refs {
		if ref.Fingerprint() != file.Server.InternalAuthTokenRef.Fingerprint() {
			result = append(result, ref)
		}
	}
	return result
}

func TestLoadStrictYAMLValidatesPublicEVMIdentityAddresses(t *testing.T) {
	zeroAddress := "0x0000000000000000000000000000000000000000"
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "hyperliquid account address has exact EVM shape",
			config:  strings.Replace(validTwentyAccountYAML(), hyperliquidAccountAddress, "0x1234", 1),
			wantErr: "account_address",
		},
		{
			name:    "hyperliquid account address rejects non-hex",
			config:  strings.Replace(validTwentyAccountYAML(), hyperliquidAccountAddress, "0xgggggggggggggggggggggggggggggggggggggggg", 1),
			wantErr: "account_address",
		},
		{
			name:    "hyperliquid account address rejects zero",
			config:  strings.Replace(validTwentyAccountYAML(), hyperliquidAccountAddress, zeroAddress, 1),
			wantErr: "zero",
		},
		{
			name:    "aster user address rejects zero",
			config:  strings.Replace(validTwentyAccountYAML(), asterUserAddress, zeroAddress, 1),
			wantErr: "zero",
		},
		{
			name:    "aster expected signer has exact EVM shape",
			config:  strings.Replace(validTwentyAccountYAML(), asterExpectedSigner, "0X4444444444444444444444444444444444444444", 1),
			wantErr: "expected_signer",
		},
		{
			name: "legacy hyperliquid secret-ref field is rejected",
			config: strings.Replace(
				validTwentyAccountYAML(),
				"account_address: \""+hyperliquidAccountAddress+"\"",
				"account_address_ref: env://HLS_ACCOUNT",
				1,
			),
			wantErr: "unknown",
		},
		{
			name: "legacy aster secret-ref fields are rejected",
			config: strings.Replace(
				validTwentyAccountYAML(),
				"user_address: \""+asterUserAddress+"\"",
				"user_address_ref: env://ASS_USER",
				1,
			),
			wantErr: "unknown",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := config.LoadYAML([]byte(test.config))
			if err == nil {
				t.Fatal("LoadYAML unexpectedly accepted invalid public identity")
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.wantErr)) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}

func TestConfigHashIncludesOperationalMetadataButNeverResolvedSecrets(t *testing.T) {
	file, err := config.LoadYAML([]byte(validOneAccountYAML()))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	provider := secret.StaticProvider{
		"env://TOKEN":          "token-secret-canary",
		"env://POSTGRES_DSN":   "postgres-secret-canary",
		"env://BINANCE_KEY":    "binance-key-secret-canary",
		"env://BINANCE_SECRET": "binance-secret-canary",
	}
	hashA, err := file.Hash()
	if err != nil {
		t.Fatalf("Hash A: %v", err)
	}
	if strings.Contains(hashA, "secret") || strings.Contains(hashA, "BINANCE") {
		t.Fatalf("hash leaked secret material or env names: %q", hashA)
	}
	if _, err := provider.Resolve(t.Context(), mustRef(t, "env://BINANCE_SECRET")); err != nil {
		t.Fatalf("provider sanity: %v", err)
	}
	hashB, err := file.Hash()
	if err != nil {
		t.Fatalf("Hash B: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("hash changed after resolving secret through provider: %s != %s", hashA, hashB)
	}

	changed, err := config.LoadYAML([]byte(strings.Replace(validOneAccountYAML(), "127.0.0.1:18080", "127.0.0.1:18081", 1)))
	if err != nil {
		t.Fatalf("Load changed: %v", err)
	}
	hashC, err := changed.Hash()
	if err != nil {
		t.Fatalf("Hash C: %v", err)
	}
	if hashA == hashC {
		t.Fatal("hash did not change when operational listen metadata changed")
	}
}

func TestNonLoopbackListenRequiresExplicitOptIn(t *testing.T) {
	nonLoopback := strings.Replace(validOneAccountYAML(), "127.0.0.1:18080", "0.0.0.0:18080", 1)
	if _, err := config.LoadYAML([]byte(nonLoopback)); err == nil || !strings.Contains(err.Error(), "non-loopback") {
		t.Fatalf("non-loopback listen without opt-in error = %v, want explicit rejection", err)
	}

	optedIn := strings.Replace(
		nonLoopback,
		"  listen: 0.0.0.0:18080",
		"  listen: 0.0.0.0:18080\n  allow_non_loopback_listen: true",
		1,
	)
	file, err := config.LoadYAML([]byte(optedIn))
	if err != nil {
		t.Fatalf("LoadYAML opted-in Docker listen: %v", err)
	}
	if !file.Server.AllowNonLoopbackListen {
		t.Fatal("allow_non_loopback_listen was not decoded")
	}

	loopbackWithOptIn := strings.Replace(
		validOneAccountYAML(),
		"  listen: 127.0.0.1:18080",
		"  listen: 127.0.0.1:18080\n  allow_non_loopback_listen: true",
		1,
	)
	if _, err := config.LoadYAML([]byte(loopbackWithOptIn)); err != nil {
		t.Fatalf("loopback listen with explicit opt-in: %v", err)
	}
}

func TestNonLoopbackOptInParticipatesInHashAndRedactedJSON(t *testing.T) {
	base, err := config.LoadYAML([]byte(validOneAccountYAML()))
	if err != nil {
		t.Fatalf("LoadYAML base: %v", err)
	}
	optedInYAML := strings.Replace(
		validOneAccountYAML(),
		"  listen: 127.0.0.1:18080",
		"  listen: 127.0.0.1:18080\n  allow_non_loopback_listen: true",
		1,
	)
	optedIn, err := config.LoadYAML([]byte(optedInYAML))
	if err != nil {
		t.Fatalf("LoadYAML opted in: %v", err)
	}
	baseHash, err := base.Hash()
	if err != nil {
		t.Fatalf("base Hash: %v", err)
	}
	optedInHash, err := optedIn.Hash()
	if err != nil {
		t.Fatalf("opted-in Hash: %v", err)
	}
	if baseHash == optedInHash {
		t.Fatal("hash did not change when allow_non_loopback_listen changed")
	}

	payload, err := json.Marshal(optedIn)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	output := string(payload)
	if !strings.Contains(output, `"allow_non_loopback_listen":true`) {
		t.Fatalf("redacted JSON omitted security-visible opt-in: %s", output)
	}
	for _, secretCanary := range []string{"env://TOKEN", "env://POSTGRES_DSN", "BINANCE_SECRET"} {
		if strings.Contains(output, secretCanary) {
			t.Fatalf("redacted JSON leaked %q: %s", secretCanary, output)
		}
	}
}

func TestConfigHashTracksPublicOperationalIdentityValues(t *testing.T) {
	base, err := config.LoadYAML([]byte(validTwentyAccountYAML()))
	if err != nil {
		t.Fatalf("Load base: %v", err)
	}
	baseHash, err := base.Hash()
	if err != nil {
		t.Fatalf("Hash base: %v", err)
	}

	for _, test := range []struct {
		name string
		old  string
		new  string
	}{
		{name: "hyperliquid account address", old: hyperliquidAccountAddress, new: "0x2222222222222222222222222222222222222222"},
		{name: "aster user address", old: asterUserAddress, new: "0x5555555555555555555555555555555555555555"},
		{name: "aster expected signer", old: asterExpectedSigner, new: "0x6666666666666666666666666666666666666666"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed, err := config.LoadYAML([]byte(strings.Replace(validTwentyAccountYAML(), test.old, test.new, 1)))
			if err != nil {
				t.Fatalf("Load changed identity: %v", err)
			}
			changedHash, err := changed.Hash()
			if err != nil {
				t.Fatalf("Hash changed identity: %v", err)
			}
			if changedHash == baseHash {
				t.Fatal("hash did not change with public operational identity")
			}
		})
	}
}

func TestConfigFormattingAndJSONNeverExposeRefsOrResolvedSecretCanaries(t *testing.T) {
	file, err := config.LoadYAML([]byte(validOneAccountYAML()))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	canaries := []string{
		"env://BINANCE_SECRET",
		"BINANCE_SECRET",
		"sk_live_{\"unicode\":\"雪\"}",
		"line\\nsecond",
	}

	outputs := []string{
		fmt.Sprint(file),
		fmt.Sprintf("%+v", file),
		fmt.Sprintf("%#v", file),
	}
	payload, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("json marshal config: %v", err)
	}
	outputs = append(outputs, string(payload))

	for _, output := range outputs {
		for _, canary := range canaries {
			if strings.Contains(output, canary) {
				t.Fatalf("formatted config leaked %q in %s", canary, output)
			}
		}
		if !strings.Contains(output, "redacted") {
			t.Fatalf("formatted config is not visibly redacted: %s", output)
		}
	}
}

func TestPublicIdentityFormattingIsVisibleWhileSecretReferencesStayRedacted(t *testing.T) {
	file, err := config.LoadYAML([]byte(validTwentyAccountYAML()))
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	hyperliquid := credentialsFor[config.PrivateKeyCredentials](t, file, "hyperliquid-spot")
	aster := credentialsFor[config.AsterCredentials](t, file, "aster-spot")

	payloads := make([]string, 0, 3)
	for _, value := range []any{file, hyperliquid, aster} {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("json marshal %T: %v", value, err)
		}
		payloads = append(payloads, string(payload))
	}
	payloads = append(payloads, fmt.Sprintf("%+v", hyperliquid), fmt.Sprintf("%+v", aster))

	for _, payload := range payloads {
		for _, secretCanary := range []string{"HLS_PRIVATE_KEY", "ASS_PRIVATE_KEY", "env://"} {
			if strings.Contains(payload, secretCanary) {
				t.Fatalf("public identity output leaked secret reference %q: %s", secretCanary, payload)
			}
		}
	}
	fileJSON := payloads[0]
	for _, address := range []string{hyperliquidAccountAddress, asterUserAddress, asterExpectedSigner} {
		if !strings.Contains(fileJSON, address) {
			t.Fatalf("redacted config JSON omitted public operational identity %q: %s", address, fileJSON)
		}
	}
}

func assertCredType[T any](t *testing.T, file config.File, accountID string) {
	t.Helper()
	for _, account := range file.Accounts {
		if account.AccountID != accountID {
			continue
		}
		if _, ok := account.Credentials.(T); !ok {
			t.Fatalf("%s credentials type = %T, want %T", accountID, account.Credentials, *new(T))
		}
		return
	}
	t.Fatalf("missing account %s", accountID)
}

func credentialsFor[T any](t *testing.T, file config.File, accountID string) T {
	t.Helper()
	for _, account := range file.Accounts {
		if account.AccountID != accountID {
			continue
		}
		credentials, ok := account.Credentials.(T)
		if !ok {
			t.Fatalf("%s credentials type = %T, want %T", accountID, account.Credentials, *new(T))
		}
		return credentials
	}
	t.Fatalf("missing account %s", accountID)
	return *new(T)
}

func mustRef(t *testing.T, value string) secret.Ref {
	t.Helper()
	ref, err := secret.ParseRef(value)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", value, err)
	}
	return ref
}

func validOneAccountYAML() string {
	return `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
accounts:
  - account_id: binance-spot
    venue: binance
    product: spot
    environment: demo
    credentials:
      api_key_ref: env://BINANCE_KEY
      api_secret_ref: env://BINANCE_SECRET
`
}

func validLighterYAML() string {
	return `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
accounts:
  - account_id: lighter-usdc-perp
    venue: lighter
    product: perp
    environment: testnet
    settlement: USDC
    credentials:
      private_key_ref: env://LIGHTER_PRIVATE_KEY
      account_index: 7
      key_index: 2
`
}

func validTwentyAccountYAML() string {
	return `
server:
  listen: 127.0.0.1:18080
  auth_token_ref: env://TOKEN
postgres:
  dsn_ref: env://POSTGRES_DSN
accounts:
  - account_id: binance-spot
    venue: binance
    product: spot
    environment: demo
    credentials: {api_key_ref: env://BNS_KEY, api_secret_ref: env://BNS_SECRET}
  - account_id: binance-usdt-perp
    venue: binance
    product: perp
    settlement: USDT
    environment: demo
    credentials: {api_key_ref: env://BNP_KEY, api_secret_ref: env://BNP_SECRET}
  - account_id: okx-spot
    venue: okx
    product: spot
    environment: demo
    credentials: {api_key_ref: env://OXS_KEY, api_secret_ref: env://OXS_SECRET, passphrase_ref: env://OXS_PASS}
  - account_id: okx-usdt-perp
    venue: okx
    product: perp
    settlement: USDT
    environment: demo
    credentials: {api_key_ref: env://OXP_KEY, api_secret_ref: env://OXP_SECRET, passphrase_ref: env://OXP_PASS}
  - account_id: lighter-spot
    venue: lighter
    product: spot
    environment: testnet
    credentials: {private_key_ref: env://LIS_PRIVATE_KEY, account_index: 7, key_index: 2}
  - account_id: lighter-usdc-perp
    venue: lighter
    product: perp
    settlement: USDC
    environment: testnet
    credentials: {private_key_ref: env://LIP_PRIVATE_KEY, account_index: 7, key_index: 2}
  - account_id: hyperliquid-spot
    venue: hyperliquid
    product: spot
    environment: testnet
    credentials: {private_key_ref: env://HLS_PRIVATE_KEY, account_address: "0x1111111111111111111111111111111111111111"}
  - account_id: hyperliquid-usdc-perp
    venue: hyperliquid
    product: perp
    settlement: USDC
    environment: testnet
    credentials: {private_key_ref: env://HLP_PRIVATE_KEY}
  - account_id: bybit-spot
    venue: bybit
    product: spot
    environment: testnet
    credentials: {api_key_ref: env://BYS_KEY, api_secret_ref: env://BYS_SECRET}
  - account_id: bybit-usdt-perp
    venue: bybit
    product: perp
    settlement: USDT
    environment: testnet
    credentials: {api_key_ref: env://BYP_KEY, api_secret_ref: env://BYP_SECRET}
  - account_id: bybit-usdc-perp
    venue: bybit
    product: perp
    settlement: USDC
    environment: testnet
    credentials: {api_key_ref: env://BYPC_KEY, api_secret_ref: env://BYPC_SECRET}
  - account_id: bitget-spot
    venue: bitget
    product: spot
    environment: demo
    credentials: {api_key_ref: env://BGS_KEY, api_secret_ref: env://BGS_SECRET, passphrase_ref: env://BGS_PASS}
  - account_id: bitget-usdt-perp
    venue: bitget
    product: perp
    settlement: USDT
    environment: demo
    credentials: {api_key_ref: env://BGP_KEY, api_secret_ref: env://BGP_SECRET, passphrase_ref: env://BGP_PASS}
  - account_id: bitget-usdc-perp
    venue: bitget
    product: perp
    settlement: USDC
    environment: demo
    credentials: {api_key_ref: env://BGPC_KEY, api_secret_ref: env://BGPC_SECRET, passphrase_ref: env://BGPC_PASS}
  - account_id: gate-spot
    venue: gate
    product: spot
    environment: testnet
    credentials: {api_key_ref: env://GTS_KEY, api_secret_ref: env://GTS_SECRET}
  - account_id: gate-usdt-perp
    venue: gate
    product: perp
    settlement: USDT
    environment: testnet
    credentials: {api_key_ref: env://GTP_KEY, api_secret_ref: env://GTP_SECRET}
  - account_id: aster-spot
    venue: aster
    product: spot
    environment: testnet
    credentials: {user_address: "0x3333333333333333333333333333333333333333", api_wallet_private_key_ref: env://ASS_PRIVATE_KEY, expected_signer: "0x4444444444444444444444444444444444444444"}
  - account_id: aster-usdt-perp
    venue: aster
    product: perp
    settlement: USDT
    environment: testnet
    credentials: {user_address: "0x5555555555555555555555555555555555555555", api_wallet_private_key_ref: env://ASP_PRIVATE_KEY}
  - account_id: nado-spot
    venue: nado
    product: spot
    settlement: USDT0
    environment: testnet
    credentials: {private_key_ref: env://NAS_PRIVATE_KEY, subaccount: default}
  - account_id: nado-usdt0-perp
    venue: nado
    product: perp
    settlement: USDT0
    environment: testnet
    credentials: {private_key_ref: env://NAP_PRIVATE_KEY, subaccount: default}
`
}
