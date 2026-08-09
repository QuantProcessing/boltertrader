package credential

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxBundleBytes = 32 * 1024
	maxStringBytes = 8 * 1024
)

var ErrInvalidBundle = errors.New("invalid credential bundle")

var defaultAuthKinds = map[string]AuthKind{
	"binance":     AuthAPIKeySecret,
	"bybit":       AuthAPIKeySecret,
	"gate":        AuthAPIKeySecret,
	"okx":         AuthAPIKeySecretPassphrase,
	"bitget":      AuthAPIKeySecretPassphrase,
	"lighter":     AuthLighterSigner,
	"hyperliquid": AuthEVMPrivateKey,
	"aster":       AuthAsterAPIWallet,
	"nado":        AuthNadoSigner,
}

type Registry struct {
	authKinds map[string]AuthKind
}

func DefaultRegistry() *Registry {
	authKinds := make(map[string]AuthKind, len(defaultAuthKinds))
	for venue, kind := range defaultAuthKinds {
		authKinds[venue] = kind
	}
	return &Registry{authKinds: authKinds}
}

func (registry *Registry) Decode(venue string, payload []byte) (*Decoded, error) {
	if registry == nil || len(payload) == 0 || len(payload) > maxBundleBytes {
		return nil, invalidBundle("body size")
	}
	if err := validateJSONShape(payload); err != nil {
		return nil, invalidBundle("json shape")
	}
	var bundle BundleV1
	if err := decodeStrict(payload, &bundle); err != nil {
		return nil, invalidBundle("envelope")
	}
	normalizedVenue := strings.ToLower(strings.TrimSpace(venue))
	kind, ok := registry.authKinds[normalizedVenue]
	if !ok || venue != normalizedVenue || bundle.Venue != normalizedVenue || bundle.SchemaVersion != 1 || bundle.AuthKind != kind {
		return nil, invalidBundle("identity")
	}
	defer clear(bundle.Metadata)
	defer clear(bundle.Secrets)

	decoded := &Decoded{Venue: normalizedVenue, AuthKind: kind}
	if err := decodeCredentials(decoded, bundle.Metadata, bundle.Secrets); err != nil {
		return nil, invalidBundle("credentials")
	}
	return decoded, nil
}

func decodeCredentials(decoded *Decoded, metadata, secrets json.RawMessage) error {
	switch decoded.AuthKind {
	case AuthAPIKeySecret:
		var meta struct{}
		var secret APIKeySecret
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || secret.APIKey == "" || secret.APISecret == "" {
			return ErrInvalidBundle
		}
		decoded.Credentials = &secret
		decoded.clear = func() { secret.APIKey, secret.APISecret = "", "" }
	case AuthAPIKeySecretPassphrase:
		var meta struct{}
		var secret APIKeySecretPassphrase
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || secret.APIKey == "" || secret.APISecret == "" || secret.Passphrase == "" {
			return ErrInvalidBundle
		}
		decoded.Credentials = &secret
		decoded.clear = func() { secret.APIKey, secret.APISecret, secret.Passphrase = "", "", "" }
	case AuthLighterSigner:
		var meta struct {
			AccountIndex int64 `json:"accountIndex"`
			KeyIndex     uint8 `json:"keyIndex"`
		}
		var secret struct {
			PrivateKey string `json:"privateKey"`
		}
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || meta.AccountIndex < 0 || secret.PrivateKey == "" {
			return ErrInvalidBundle
		}
		credentials := &LighterSigner{PrivateKey: secret.PrivateKey, AccountIndex: meta.AccountIndex, KeyIndex: meta.KeyIndex}
		secret.PrivateKey = ""
		decoded.Credentials = credentials
		decoded.clear = func() { credentials.PrivateKey = "" }
	case AuthEVMPrivateKey:
		var meta struct {
			AccountAddress string `json:"accountAddress,omitempty"`
		}
		var secret struct {
			PrivateKey string `json:"privateKey"`
		}
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || secret.PrivateKey == "" || (meta.AccountAddress != "" && validateEVMAddress(meta.AccountAddress) != nil) {
			return ErrInvalidBundle
		}
		credentials := &EVMPrivateKey{PrivateKey: secret.PrivateKey, AccountAddress: meta.AccountAddress}
		secret.PrivateKey = ""
		decoded.Credentials = credentials
		decoded.clear = func() { credentials.PrivateKey, credentials.AccountAddress = "", "" }
	case AuthAsterAPIWallet:
		var meta struct {
			UserAddress    string `json:"userAddress"`
			ExpectedSigner string `json:"expectedSigner,omitempty"`
		}
		var secret struct {
			APIWalletPrivateKey string `json:"apiWalletPrivateKey"`
		}
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || secret.APIWalletPrivateKey == "" || validateEVMAddress(meta.UserAddress) != nil || (meta.ExpectedSigner != "" && validateEVMAddress(meta.ExpectedSigner) != nil) {
			return ErrInvalidBundle
		}
		credentials := &AsterAPIWallet{APIWalletPrivateKey: secret.APIWalletPrivateKey, UserAddress: meta.UserAddress, ExpectedSigner: meta.ExpectedSigner}
		secret.APIWalletPrivateKey = ""
		decoded.Credentials = credentials
		decoded.clear = func() {
			credentials.APIWalletPrivateKey, credentials.UserAddress, credentials.ExpectedSigner = "", "", ""
		}
	case AuthNadoSigner:
		var meta struct {
			Subaccount string `json:"subaccount"`
		}
		var secret struct {
			PrivateKey string `json:"privateKey"`
		}
		if err := decodeNested(metadata, &meta); err != nil || decodeNested(secrets, &secret) != nil || strings.TrimSpace(meta.Subaccount) == "" || meta.Subaccount != strings.TrimSpace(meta.Subaccount) || secret.PrivateKey == "" {
			return ErrInvalidBundle
		}
		credentials := &NadoSigner{PrivateKey: secret.PrivateKey, Subaccount: meta.Subaccount}
		secret.PrivateKey = ""
		decoded.Credentials = credentials
		decoded.clear = func() { credentials.PrivateKey, credentials.Subaccount = "", "" }
	default:
		return ErrInvalidBundle
	}
	return nil
}

func decodeNested(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || raw[0] != '{' {
		return ErrInvalidBundle
	}
	return decodeStrict(raw, destination)
}

func decodeStrict(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrInvalidBundle
	}
	return nil
}

func validateJSONShape(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return ErrInvalidBundle
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case string:
		if len(value) > maxStringBytes {
			return ErrInvalidBundle
		}
	case json.Delim:
		switch value {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok || len(key) > maxStringBytes {
					return ErrInvalidBundle
				}
				if _, duplicate := seen[key]; duplicate {
					return ErrInvalidBundle
				}
				seen[key] = struct{}{}
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return ErrInvalidBundle
			}
		case '[':
			for decoder.More() {
				if err := scanJSONValue(decoder); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return ErrInvalidBundle
			}
		default:
			return ErrInvalidBundle
		}
	}
	return nil
}

func validateEVMAddress(value string) error {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return ErrInvalidBundle
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil || len(decoded) != 20 {
		return ErrInvalidBundle
	}
	for _, octet := range decoded {
		if octet != 0 {
			return nil
		}
	}
	return ErrInvalidBundle
}

func invalidBundle(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidBundle, reason)
}
