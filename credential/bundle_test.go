package credential_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/credential"
)

func TestRegistryRejectsUnknownCredentialFields(t *testing.T) {
	payload := []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret","extra":"forbidden"}}`)
	_, err := credential.DefaultRegistry().Decode("binance", payload)
	if !errors.Is(err, credential.ErrInvalidBundle) {
		t.Fatalf("err=%v", err)
	}
}

func TestRegistryDecodesEveryRegisteredCredentialVariant(t *testing.T) {
	tests := []struct {
		venue    string
		kind     credential.AuthKind
		metadata string
		secrets  string
		wantType string
	}{
		{"binance", credential.AuthAPIKeySecret, `{}`, `{"apiKey":"key","apiSecret":"secret"}`, "*credential.APIKeySecret"},
		{"okx", credential.AuthAPIKeySecretPassphrase, `{}`, `{"apiKey":"key","apiSecret":"secret","passphrase":"pass"}`, "*credential.APIKeySecretPassphrase"},
		{"lighter", credential.AuthLighterSigner, `{"accountIndex":7,"keyIndex":2}`, `{"privateKey":"private"}`, "*credential.LighterSigner"},
		{"hyperliquid", credential.AuthEVMPrivateKey, `{"accountAddress":"0x1111111111111111111111111111111111111111"}`, `{"privateKey":"private"}`, "*credential.EVMPrivateKey"},
		{"aster", credential.AuthAsterAPIWallet, `{"userAddress":"0x2222222222222222222222222222222222222222","expectedSigner":"0x3333333333333333333333333333333333333333"}`, `{"apiWalletPrivateKey":"private"}`, "*credential.AsterAPIWallet"},
		{"nado", credential.AuthNadoSigner, `{"subaccount":"default"}`, `{"privateKey":"private"}`, "*credential.NadoSigner"},
	}
	for _, test := range tests {
		t.Run(test.venue, func(t *testing.T) {
			payload := []byte(fmt.Sprintf(`{"schemaVersion":1,"venue":%q,"authKind":%q,"metadata":%s,"secrets":%s}`, test.venue, test.kind, test.metadata, test.secrets))
			decoded, err := credential.DefaultRegistry().Decode(test.venue, payload)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if got := fmt.Sprintf("%T", decoded.Credentials); got != test.wantType {
				t.Fatalf("credentials type=%s want=%s", got, test.wantType)
			}
			if decoded.Venue != test.venue || decoded.AuthKind != test.kind {
				t.Fatalf("decoded identity=%+v", decoded)
			}
		})
	}
}

func TestRegistryRejectsMalformedAndOversizedBundles(t *testing.T) {
	valid := `{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`
	tests := map[string][]byte{
		"venue mismatch":       []byte(strings.Replace(valid, `"binance"`, `"bybit"`, 1)),
		"auth mismatch":        []byte(strings.Replace(valid, `"api_key_secret"`, `"evm_private_key"`, 1)),
		"duplicate nested key": []byte(strings.Replace(valid, `"apiKey":"key"`, `"apiKey":"key","apiKey":"other"`, 1)),
		"trailing value":       []byte(valid + `{}`),
		"oversized body":       []byte(strings.Repeat(" ", 32*1024) + valid),
		"oversized string":     []byte(strings.Replace(valid, `"key"`, `"`+strings.Repeat("x", 8*1024+1)+`"`, 1)),
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := credential.DefaultRegistry().Decode("binance", payload); !errors.Is(err, credential.ErrInvalidBundle) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestDecodedFormattingIsRedactedAndClearIsIdempotent(t *testing.T) {
	const key, secret = "key-canary", "secret-canary"
	payload := []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"` + key + `","apiSecret":"` + secret + `"}}`)
	decoded, err := credential.DefaultRegistry().Decode("binance", payload)
	if err != nil {
		t.Fatal(err)
	}
	formattedJSON, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{decoded.String(), fmt.Sprintf("%#v", decoded), string(formattedJSON)} {
		if !strings.Contains(formatted, "redacted") || strings.Contains(formatted, "binance") || strings.Contains(formatted, key) || strings.Contains(formatted, secret) {
			t.Fatalf("unsafe formatting %q", formatted)
		}
	}
	decoded.Clear()
	decoded.Clear()
	credentials := decoded.Credentials.(*credential.APIKeySecret)
	if credentials.APIKey != "" || credentials.APISecret != "" {
		t.Fatalf("credentials not cleared: %#v", credentials)
	}
}
