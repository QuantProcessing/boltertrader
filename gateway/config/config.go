package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"gopkg.in/yaml.v3"
)

type File struct {
	Server   ServerConfig
	Postgres PostgresConfig
	Accounts []AccountConfig
}

type ServerConfig struct {
	Listen       string     `yaml:"listen" json:"listen"`
	AuthTokenRef secret.Ref `yaml:"auth_token_ref" json:"auth_token_ref"`
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
	Accounts []accountYAML  `yaml:"accounts"`
}

type accountYAML struct {
	AccountID   string    `yaml:"account_id"`
	Venue       string    `yaml:"venue"`
	Product     string    `yaml:"product"`
	Environment string    `yaml:"environment"`
	Settlement  string    `yaml:"settlement"`
	Credentials yaml.Node `yaml:"credentials"`
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
		Accounts: make([]AccountConfig, 0, len(raw.Accounts)),
	}
	for _, rawAccount := range raw.Accounts {
		account := AccountConfig{
			AccountID:   strings.TrimSpace(rawAccount.AccountID),
			Venue:       strings.ToLower(strings.TrimSpace(rawAccount.Venue)),
			Product:     strings.ToLower(strings.TrimSpace(rawAccount.Product)),
			Environment: strings.ToLower(strings.TrimSpace(rawAccount.Environment)),
			Settlement:  strings.ToUpper(strings.TrimSpace(rawAccount.Settlement)),
		}
		credentials, err := decodeCredentials(account, rawAccount.Credentials)
		if err != nil {
			return File{}, fmt.Errorf("account %q credentials: %w", account.AccountID, err)
		}
		account.Credentials = credentials
		file.Accounts = append(file.Accounts, account)
	}
	if err := file.Validate(); err != nil {
		return File{}, err
	}
	return file, nil
}

func (file File) Validate() error {
	if strings.TrimSpace(file.Server.Listen) == "" {
		return fmt.Errorf("server listen is required")
	}
	host, _, err := net.SplitHostPort(file.Server.Listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return fmt.Errorf("server listen must be a loopback host:port")
	}
	if file.Server.AuthTokenRef.IsZero() {
		return fmt.Errorf("server auth_token_ref secret reference is required")
	}
	if file.Postgres.DSNRef.IsZero() {
		return fmt.Errorf("postgres dsn_ref secret reference is required")
	}
	seen := map[string]struct{}{}
	for _, account := range file.Accounts {
		if account.AccountID == "" {
			return fmt.Errorf("account_id is required")
		}
		if _, ok := seen[account.AccountID]; ok {
			return fmt.Errorf("duplicate account_id %q", account.AccountID)
		}
		seen[account.AccountID] = struct{}{}
		if err := validateProfile(account); err != nil {
			return err
		}
		if err := validateCredentials(account.Credentials); err != nil {
			return fmt.Errorf("account %q credentials: %w", account.AccountID, err)
		}
	}
	return nil
}

func (file File) Hash() (string, error) {
	sanitized := hashFile{
		Server: hashServer{
			Listen:       file.Server.Listen,
			AuthTokenRef: file.Server.AuthTokenRef.Fingerprint(),
		},
		Postgres: hashPostgres{
			DSNRef: file.Postgres.DSNRef.Fingerprint(),
		},
		Accounts: make([]hashAccount, 0, len(file.Accounts)),
	}
	for _, account := range file.Accounts {
		credentials, err := hashCredentials(account.Credentials)
		if err != nil {
			return "", err
		}
		sanitized.Accounts = append(sanitized.Accounts, hashAccount{
			AccountID:   account.AccountID,
			Venue:       account.Venue,
			Product:     account.Product,
			Environment: account.Environment,
			Settlement:  account.Settlement,
			Credentials: credentials,
		})
	}
	payload, err := json.Marshal(sanitized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func (file File) String() string {
	return "gateway/config.File{server:configured, postgres:redacted, accounts:" + strconv.Itoa(len(file.Accounts)) + ", credentials:redacted}"
}

func (file File) GoString() string { return file.String() }

func (file File) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{
		"server": map[string]any{
			"listen":         file.Server.Listen,
			"auth_token_ref": "redacted",
		},
		"postgres": map[string]any{
			"dsn_ref": "redacted",
		},
		"accounts": file.redactedAccounts(),
	})
}

func (file File) redactedAccounts() []map[string]any {
	rows := make([]map[string]any, 0, len(file.Accounts))
	for _, account := range file.Accounts {
		rows = append(rows, map[string]any{
			"account_id":  account.AccountID,
			"venue":       account.Venue,
			"product":     account.Product,
			"environment": account.Environment,
			"settlement":  account.Settlement,
			"credentials": redactedCredentials(account.Credentials),
		})
	}
	return rows
}

func redactedCredentials(credentials any) map[string]any {
	switch value := credentials.(type) {
	case APIKeySecretCredentials:
		return map[string]any{"api_key_ref": "redacted", "api_secret_ref": "redacted"}
	case APIKeySecretPassphraseCredentials:
		return map[string]any{"api_key_ref": "redacted", "api_secret_ref": "redacted", "passphrase_ref": "redacted"}
	case LighterCredentials:
		return map[string]any{"private_key_ref": "redacted", "account_index": value.AccountIndex, "key_index": uint8(value.KeyIndex)}
	case PrivateKeyCredentials:
		return map[string]any{"private_key_ref": "redacted", "account_address": value.AccountAddress}
	case AsterCredentials:
		return map[string]any{"user_address": value.UserAddress, "api_wallet_private_key_ref": "redacted", "expected_signer": value.ExpectedSigner}
	case NadoCredentials:
		return map[string]any{"private_key_ref": "redacted", "subaccount": value.Subaccount}
	default:
		return map[string]any{"credentials": "redacted"}
	}
}

func decodeCredentials(account AccountConfig, node yaml.Node) (any, error) {
	switch account.Venue {
	case "binance", "bybit", "gate":
		var credentials APIKeySecretCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	case "okx", "bitget":
		var credentials APIKeySecretPassphraseCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	case "lighter":
		var credentials LighterCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	case "hyperliquid":
		var credentials PrivateKeyCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	case "aster":
		var credentials AsterCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	case "nado":
		var credentials NadoCredentials
		if err := decodeStrict(node, &credentials); err != nil {
			return nil, err
		}
		return credentials, nil
	default:
		return nil, fmt.Errorf("unknown venue")
	}
}

func decodeStrict(node yaml.Node, out any) error {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	if err := encoder.Encode(&node); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(&buffer)
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return normalizeYAMLError("strict credentials decode", err)
	}
	return nil
}

func normalizeYAMLError(scope string, err error) error {
	message := err.Error()
	if strings.Contains(message, "field ") && strings.Contains(message, " not found in type ") {
		return fmt.Errorf("%s: unknown field: %w", scope, err)
	}
	return fmt.Errorf("%s: %w", scope, err)
}

func validateProfile(account AccountConfig) error {
	allowed := map[string]map[string]map[string]map[string]bool{
		"binance": {
			"spot": {"": {"live": true, "demo": true}},
			"perp": {"USDT": {"live": true, "demo": true}},
		},
		"okx": {
			"spot": {"": {"live": true, "demo": true}},
			"perp": {"USDT": {"live": true, "demo": true}},
		},
		"lighter": {
			"spot": {"": {"live": true, "testnet": true}},
			"perp": {"USDC": {"live": true, "testnet": true}},
		},
		"hyperliquid": {
			"spot": {"": {"live": true, "testnet": true}},
			"perp": {"USDC": {"live": true, "testnet": true}},
		},
		"bybit": {
			"spot": {"": {"live": true, "demo": true, "testnet": true}},
			"perp": {"USDT": {"live": true, "demo": true, "testnet": true}, "USDC": {"live": true, "demo": true, "testnet": true}},
		},
		"bitget": {
			"spot": {"": {"live": true, "demo": true}},
			"perp": {"USDT": {"live": true, "demo": true}, "USDC": {"live": true, "demo": true}},
		},
		"gate": {
			"spot": {"": {"live": true, "testnet": true}},
			"perp": {"USDT": {"live": true, "testnet": true}},
		},
		"aster": {
			"spot": {"": {"live": true, "testnet": true}},
			"perp": {"USDT": {"live": true, "testnet": true}},
		},
		"nado": {
			"spot": {"USDT0": {"live": true, "testnet": true}},
			"perp": {"USDT0": {"live": true, "testnet": true}},
		},
	}
	products, ok := allowed[account.Venue]
	if !ok {
		return fmt.Errorf("unknown venue %q", account.Venue)
	}
	settlements, ok := products[account.Product]
	if !ok {
		return fmt.Errorf("unknown product %q for venue %q", account.Product, account.Venue)
	}
	environments, ok := settlements[account.Settlement]
	if !ok {
		return fmt.Errorf("incompatible profile for %s %s settlement %q", account.Venue, account.Product, account.Settlement)
	}
	if !environments[account.Environment] {
		return fmt.Errorf("unknown environment %q for profile", account.Environment)
	}
	return nil
}

func validateCredentials(credentials any) error {
	switch value := credentials.(type) {
	case APIKeySecretCredentials:
		return requireRefs(value.APIKeyRef, value.APISecretRef)
	case APIKeySecretPassphraseCredentials:
		return requireRefs(value.APIKeyRef, value.APISecretRef, value.PassphraseRef)
	case LighterCredentials:
		if err := requireRefs(value.PrivateKeyRef); err != nil {
			return err
		}
		if value.AccountIndex < 0 {
			return fmt.Errorf("account_index must be non-negative")
		}
		return nil
	case PrivateKeyCredentials:
		if err := requireRefs(value.PrivateKeyRef); err != nil {
			return err
		}
		if value.AccountAddress == "" {
			return nil
		}
		return validateEVMAddress("account_address", value.AccountAddress)
	case AsterCredentials:
		if err := requireRefs(value.APIWalletPrivateKeyRef); err != nil {
			return err
		}
		if err := validateEVMAddress("user_address", value.UserAddress); err != nil {
			return err
		}
		if value.ExpectedSigner == "" {
			return nil
		}
		return validateEVMAddress("expected_signer", value.ExpectedSigner)
	case NadoCredentials:
		if strings.TrimSpace(value.Subaccount) == "" {
			return fmt.Errorf("subaccount is required")
		}
		return requireRefs(value.PrivateKeyRef)
	default:
		return fmt.Errorf("unknown credential variant")
	}
}

func requireRefs(refs ...secret.Ref) error {
	for _, ref := range refs {
		if ref.IsZero() {
			return fmt.Errorf("secret reference is required")
		}
	}
	return nil
}

func validateEVMAddress(field, value string) error {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return fmt.Errorf("%s must be 0x followed by 40 hexadecimal characters", field)
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 20 {
		return fmt.Errorf("%s must be 0x followed by 40 hexadecimal characters", field)
	}
	allZero := true
	for _, octet := range decoded {
		if octet != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return fmt.Errorf("%s must not be the zero address", field)
	}
	return nil
}

type hashFile struct {
	Server   hashServer    `json:"server"`
	Postgres hashPostgres  `json:"postgres"`
	Accounts []hashAccount `json:"accounts"`
}
type hashServer struct {
	Listen       string `json:"listen"`
	AuthTokenRef string `json:"auth_token_ref"`
}
type hashPostgres struct {
	DSNRef string `json:"dsn_ref"`
}
type hashAccount struct {
	AccountID   string         `json:"account_id"`
	Venue       string         `json:"venue"`
	Product     string         `json:"product"`
	Environment string         `json:"environment"`
	Settlement  string         `json:"settlement"`
	Credentials map[string]any `json:"credentials"`
}

func hashCredentials(credentials any) (map[string]any, error) {
	switch value := credentials.(type) {
	case APIKeySecretCredentials:
		return map[string]any{"type": "api_key_secret", "api_key_ref": value.APIKeyRef.Fingerprint(), "api_secret_ref": value.APISecretRef.Fingerprint()}, nil
	case APIKeySecretPassphraseCredentials:
		return map[string]any{"type": "api_key_secret_passphrase", "api_key_ref": value.APIKeyRef.Fingerprint(), "api_secret_ref": value.APISecretRef.Fingerprint(), "passphrase_ref": value.PassphraseRef.Fingerprint()}, nil
	case LighterCredentials:
		return map[string]any{"type": "lighter", "private_key_ref": value.PrivateKeyRef.Fingerprint(), "account_index": value.AccountIndex, "key_index": uint8(value.KeyIndex)}, nil
	case PrivateKeyCredentials:
		return map[string]any{"type": "private_key", "private_key_ref": value.PrivateKeyRef.Fingerprint(), "account_address": value.AccountAddress}, nil
	case AsterCredentials:
		return map[string]any{"type": "aster", "user_address": value.UserAddress, "api_wallet_private_key_ref": value.APIWalletPrivateKeyRef.Fingerprint(), "expected_signer": value.ExpectedSigner}, nil
	case NadoCredentials:
		return map[string]any{"type": "nado", "private_key_ref": value.PrivateKeyRef.Fingerprint(), "subaccount": value.Subaccount}, nil
	default:
		return nil, fmt.Errorf("unknown credential variant")
	}
}
