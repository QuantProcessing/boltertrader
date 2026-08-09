package credential

import (
	"encoding/json"
	"fmt"
)

type AuthKind string

const (
	AuthAPIKeySecret           AuthKind = "api_key_secret"
	AuthAPIKeySecretPassphrase AuthKind = "api_key_secret_passphrase"
	AuthLighterSigner          AuthKind = "lighter_signer"
	AuthEVMPrivateKey          AuthKind = "evm_private_key"
	AuthAsterAPIWallet         AuthKind = "aster_api_wallet"
	AuthNadoSigner             AuthKind = "nado_signer"
)

type BundleV1 struct {
	SchemaVersion int             `json:"schemaVersion"`
	Venue         string          `json:"venue"`
	AuthKind      AuthKind        `json:"authKind"`
	Metadata      json.RawMessage `json:"metadata"`
	Secrets       json.RawMessage `json:"secrets"`
}

type APIKeySecret struct {
	APIKey    string `json:"apiKey"`
	APISecret string `json:"apiSecret"`
}

type APIKeySecretPassphrase struct {
	APIKey     string `json:"apiKey"`
	APISecret  string `json:"apiSecret"`
	Passphrase string `json:"passphrase"`
}

type LighterSigner struct {
	PrivateKey   string `json:"privateKey"`
	AccountIndex int64  `json:"accountIndex"`
	KeyIndex     uint8  `json:"keyIndex"`
}

type EVMPrivateKey struct {
	PrivateKey     string `json:"privateKey"`
	AccountAddress string `json:"accountAddress,omitempty"`
}

type AsterAPIWallet struct {
	APIWalletPrivateKey string `json:"apiWalletPrivateKey"`
	UserAddress         string `json:"userAddress"`
	ExpectedSigner      string `json:"expectedSigner,omitempty"`
}

type NadoSigner struct {
	PrivateKey string `json:"privateKey"`
	Subaccount string `json:"subaccount"`
}

type Decoded struct {
	Venue       string
	AuthKind    AuthKind
	Credentials any
	clear       func()
}

func (decoded *Decoded) Clear() {
	if decoded != nil && decoded.clear != nil {
		decoded.clear()
		decoded.clear = nil
	}
}

func (decoded Decoded) String() string {
	return fmt.Sprintf("credential.Decoded{Type:%q Credentials:redacted}", decoded.AuthKind)
}

func (decoded Decoded) GoString() string { return decoded.String() }

func (decoded Decoded) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type        AuthKind `json:"type"`
		Credentials string   `json:"credentials"`
	}{Type: decoded.AuthKind, Credentials: "redacted"})
}
