package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"gopkg.in/yaml.v3"
)

type File struct {
	Server   ServerConfig
	Postgres PostgresConfig
}

type ServerConfig struct {
	Mode                   string     `yaml:"mode" json:"mode"`
	Listen                 string     `yaml:"listen" json:"listen"`
	AllowNonLoopbackListen bool       `yaml:"allow_non_loopback_listen" json:"allow_non_loopback_listen"`
	AuthTokenRef           secret.Ref `yaml:"auth_token_ref" json:"auth_token_ref"`
	InternalListen         string     `yaml:"internal_listen,omitempty" json:"internal_listen,omitempty"`
	InternalAuthTokenRef   secret.Ref `yaml:"internal_auth_token_ref,omitempty" json:"internal_auth_token_ref,omitempty"`
}

type PostgresConfig struct {
	DSNRef secret.Ref `yaml:"dsn_ref" json:"dsn_ref"`
}

type AccountConfig struct {
	AccountID   string
	Venue       string
	Product     string
	Environment string
	Settlement  string
	Credentials any
}

// ValidateRuntimeIdentity validates the product-agnostic portion of a dynamic
// account request. Product and settlement stay runtime-discovered facts.
func ValidateRuntimeIdentity(processMode, accountID, venue, environment string) error {
	if strings.TrimSpace(accountID) == "" {
		return fmt.Errorf("account_id is required")
	}
	mode := routeprofile.Mode(strings.ToLower(strings.TrimSpace(processMode)))
	if mode != routeprofile.ModeTest && mode != routeprofile.ModeLive {
		return fmt.Errorf("process mode must be test or live")
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	environment = strings.ToLower(strings.TrimSpace(environment))
	registry := routeprofile.DefaultRegistry()
	if !registry.KnowsVenue(venue) {
		return fmt.Errorf("unknown venue %q", venue)
	}
	if registry.SupportsEnvironment(mode, venue, routeprofile.NativeEnvironment(environment)) {
		return nil
	}
	return fmt.Errorf("unknown environment %q for venue %q", environment, venue)
}

type APIKeySecretCredentials struct {
	APIKeyRef    secret.Ref `yaml:"api_key_ref" json:"api_key_ref"`
	APISecretRef secret.Ref `yaml:"api_secret_ref" json:"api_secret_ref"`
}

type APIKeySecretPassphraseCredentials struct {
	APIKeyRef     secret.Ref `yaml:"api_key_ref" json:"api_key_ref"`
	APISecretRef  secret.Ref `yaml:"api_secret_ref" json:"api_secret_ref"`
	PassphraseRef secret.Ref `yaml:"passphrase_ref" json:"passphrase_ref"`
}

type LighterCredentials struct {
	PrivateKeyRef secret.Ref `yaml:"private_key_ref" json:"private_key_ref"`
	AccountIndex  int64      `yaml:"account_index" json:"account_index"`
	KeyIndex      KeyIndex   `yaml:"key_index" json:"key_index"`
}

type KeyIndex uint8

func (index *KeyIndex) UnmarshalYAML(unmarshal func(any) error) error {
	var value int
	if err := unmarshal(&value); err != nil {
		return err
	}
	if value < 0 || value > 255 {
		return fmt.Errorf("key_index must fit uint8")
	}
	*index = KeyIndex(value)
	return nil
}

func (index KeyIndex) MarshalJSON() ([]byte, error) {
	return json.Marshal(uint8(index))
}

type PrivateKeyCredentials struct {
	PrivateKeyRef  secret.Ref `yaml:"private_key_ref" json:"private_key_ref"`
	AccountAddress string     `yaml:"account_address" json:"account_address,omitempty"`
}

type AsterCredentials struct {
	UserAddress            string     `yaml:"user_address" json:"user_address"`
	APIWalletPrivateKeyRef secret.Ref `yaml:"api_wallet_private_key_ref" json:"api_wallet_private_key_ref"`
	ExpectedSigner         string     `yaml:"expected_signer" json:"expected_signer,omitempty"`
}

type NadoCredentials struct {
	PrivateKeyRef secret.Ref `yaml:"private_key_ref" json:"private_key_ref"`
	Subaccount    string     `yaml:"subaccount" json:"subaccount"`
}

type fileYAML struct {
	Server   ServerConfig   `yaml:"server"`
	Postgres PostgresConfig `yaml:"postgres"`
}

func LoadYAML(data []byte) (File, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var raw fileYAML
	if err := decoder.Decode(&raw); err != nil {
		return File{}, normalizeYAMLError("strict config decode", err)
	}
	file := File{
		Server:   raw.Server,
		Postgres: raw.Postgres,
	}
	file.Server.Mode = strings.ToLower(strings.TrimSpace(file.Server.Mode))
	if file.Server.Mode == "" {
		file.Server.Mode = "test"
	}
	if err := file.Validate(); err != nil {
		return File{}, err
	}
	return file, nil
}

func (file File) Validate() error {
	if file.Server.Mode != "" && file.Server.Mode != "test" && file.Server.Mode != "live" {
		return fmt.Errorf("server mode must be test or live")
	}
	if strings.TrimSpace(file.Server.Listen) == "" {
		return fmt.Errorf("server listen is required")
	}
	host, _, err := net.SplitHostPort(file.Server.Listen)
	address := net.ParseIP(host)
	if err != nil || address == nil {
		return fmt.Errorf("server listen must be an IP host:port")
	}
	if !address.IsLoopback() && !file.Server.AllowNonLoopbackListen {
		return fmt.Errorf("server non-loopback listen requires allow_non_loopback_listen")
	}
	if file.Server.AuthTokenRef.IsZero() {
		return fmt.Errorf("server auth_token_ref secret reference is required")
	}
	if (file.Server.InternalListen == "") != file.Server.InternalAuthTokenRef.IsZero() {
		return fmt.Errorf("server internal listener and auth token must be configured together")
	}
	if file.Server.InternalListen != "" {
		internalHost, _, internalErr := net.SplitHostPort(file.Server.InternalListen)
		internalAddress := net.ParseIP(internalHost)
		if internalErr != nil || internalAddress == nil || !internalAddress.IsLoopback() {
			return fmt.Errorf("server internal listener must be a loopback IP host:port")
		}
	}
	if file.Postgres.DSNRef.IsZero() {
		return fmt.Errorf("postgres dsn_ref secret reference is required")
	}
	return nil
}

func (file File) Hash() (string, error) {
	sanitized := hashFile{
		Server: hashServer{
			Mode:                   file.Server.Mode,
			Listen:                 file.Server.Listen,
			AllowNonLoopbackListen: file.Server.AllowNonLoopbackListen,
			AuthTokenRef:           file.Server.AuthTokenRef.Fingerprint(),
			InternalListen:         file.Server.InternalListen,
			InternalAuthTokenRef:   file.Server.InternalAuthTokenRef.Fingerprint(),
		},
		Postgres: hashPostgres{
			DSNRef: file.Postgres.DSNRef.Fingerprint(),
		},
	}
	payload, err := json.Marshal(sanitized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (file File) String() string {
	return fmt.Sprintf("gateway/config.File{server:configured, allow_non_loopback_listen:%t, postgres:redacted}", file.Server.AllowNonLoopbackListen)
}

func (file File) GoString() string { return file.String() }

func (file File) MarshalJSON() ([]byte, error) {
	server := map[string]any{
		"mode":                      file.Server.Mode,
		"listen":                    file.Server.Listen,
		"allow_non_loopback_listen": file.Server.AllowNonLoopbackListen,
		"auth_token_ref":            "redacted",
	}
	if file.Server.InternalListen != "" {
		server["internal_listen"] = file.Server.InternalListen
		server["internal_auth_token_ref"] = "redacted"
	}
	return json.Marshal(map[string]any{
		"server": server,
		"postgres": map[string]any{
			"dsn_ref": "redacted",
		},
	})
}

func (file File) SecretRefs() []secret.Ref {
	refs := []secret.Ref{file.Server.AuthTokenRef, file.Postgres.DSNRef}
	if !file.Server.InternalAuthTokenRef.IsZero() {
		refs = append(refs, file.Server.InternalAuthTokenRef)
	}
	return refs
}

func normalizeYAMLError(scope string, err error) error {
	message := err.Error()
	if strings.Contains(message, "field ") && strings.Contains(message, " not found in type ") {
		return fmt.Errorf("%s: unknown field: %w", scope, err)
	}
	return fmt.Errorf("%s: %w", scope, err)
}

type hashFile struct {
	Server   hashServer   `json:"server"`
	Postgres hashPostgres `json:"postgres"`
}
type hashServer struct {
	Mode                   string `json:"mode"`
	Listen                 string `json:"listen"`
	AllowNonLoopbackListen bool   `json:"allow_non_loopback_listen"`
	AuthTokenRef           string `json:"auth_token_ref"`
	InternalListen         string `json:"internal_listen,omitempty"`
	InternalAuthTokenRef   string `json:"internal_auth_token_ref,omitempty"`
}
type hashPostgres struct {
	DSNRef string `json:"dsn_ref"`
}
