package common

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testSignerAddress = "0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf"

func TestSecurityContextDerivesAndGuardsSigner(t *testing.T) {
	security, err := NewSecurityContext(CredentialConfig{
		User:           "0x1111111111111111111111111111111111111111",
		PrivateKey:     fmt.Sprintf("%064x", 1),
		ExpectedSigner: testSignerAddress,
	})
	if err != nil {
		t.Fatalf("NewSecurityContext: %v", err)
	}
	if security.Signer() != testSignerAddress {
		t.Fatalf("signer = %s", security.Signer())
	}
	if security.User() != "0x1111111111111111111111111111111111111111" {
		t.Fatalf("user = %s", security.User())
	}

	_, err = NewSecurityContext(CredentialConfig{
		User:           "0x1111111111111111111111111111111111111111",
		PrivateKey:     fmt.Sprintf("%064x", 1),
		ExpectedSigner: "0x2222222222222222222222222222222222222222",
	})
	if err == nil {
		t.Fatal("mismatched expected signer accepted")
	}
	if strings.Contains(err.Error(), fmt.Sprintf("%064x", 1)) {
		t.Fatal("signer mismatch leaked private key")
	}
}

func TestSecurityContextRejectsMalformedCredentials(t *testing.T) {
	tests := []CredentialConfig{
		{User: "not-an-address", PrivateKey: fmt.Sprintf("%064x", 1)},
		{User: "0x1111111111111111111111111111111111111111", PrivateKey: "not-a-key"},
		{User: "0x1111111111111111111111111111111111111111", PrivateKey: fmt.Sprintf("%064x", 1), ExpectedSigner: "bad"},
	}
	for _, cfg := range tests {
		if _, err := NewSecurityContext(cfg); err == nil {
			t.Fatalf("invalid credentials accepted: user=%q expectedSigner=%q", cfg.User, cfg.ExpectedSigner)
		}
	}
}

func TestSecurityContextMatchesIndependentEIP712Vector(t *testing.T) {
	fixed := time.UnixMicro(1_748_310_859_508_867)
	security, err := NewSecurityContext(CredentialConfig{
		User:           "0x1111111111111111111111111111111111111111",
		PrivateKey:     fmt.Sprintf("%064x", 1),
		ExpectedSigner: testSignerAddress,
	}, WithClock(ClockFunc(func() time.Time { return fixed })))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := NewProfile(EnvironmentTestnet, ProductPerp)
	if err != nil {
		t.Fatal(err)
	}
	params := url.Values{
		"symbol":      {"ASTERUSDT"},
		"type":        {"LIMIT"},
		"side":        {"BUY"},
		"timeInForce": {"GTC"},
		"quantity":    {"20"},
		"price":       {"0.5"},
	}
	signed, err := security.Sign(profile, params)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	values := signed.Values()
	signatureHex := strings.TrimPrefix(values.Get("signature"), "0x")
	signature, err := hex.DecodeString(signatureHex)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	signatureHash := sha256.Sum256(signature)
	if got, want := hex.EncodeToString(signatureHash[:]), "f00bb01aa0abe41390daa7815b3080f477ae0a9761b90cce19eef1a5b24ed4c0"; got != want {
		t.Fatalf("signature hash = %s, want %s", got, want)
	}
	if got, want := signed.Digest(), "0xa9b72c71134a5d03978f6f65757ef0f680ee553405f62829dafa92d7496afbc8"; got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
	if values.Get("user") != security.User() || values.Get("signer") != security.Signer() {
		t.Fatalf("identity params = user:%q signer:%q", values.Get("user"), values.Get("signer"))
	}
	if values.Get("nonce") != "1748310859508867" || values.Get("timestamp") != "1748310859508" {
		t.Fatalf("time params = nonce:%q timestamp:%q", values.Get("nonce"), values.Get("timestamp"))
	}
	if params.Get("nonce") != "" || params.Get("signature") != "" {
		t.Fatal("Sign mutated caller params")
	}
}

func TestSecurityContextRejectsCallerOwnedAuthFields(t *testing.T) {
	security, err := NewSecurityContext(CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := NewProfile(EnvironmentTestnet, ProductSpot)
	for _, field := range []string{"user", "signer", "nonce", "timestamp", "signature"} {
		if _, err := security.Sign(profile, url.Values{field: {"caller-value"}}); err == nil {
			t.Fatalf("reserved auth field %q accepted", field)
		}
	}
}

func TestSecurityDiagnosticsAreRedacted(t *testing.T) {
	privateKey := fmt.Sprintf("%064x", 1)
	security, err := NewSecurityContext(CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: privateKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, rendered := range []string{fmt.Sprint(security), fmt.Sprintf("%#v", security)} {
		if strings.Contains(rendered, privateKey) {
			t.Fatal("security diagnostics leaked private key")
		}
		if !strings.Contains(rendered, "<redacted>") {
			t.Fatalf("security diagnostics are not explicitly redacted: %q", rendered)
		}
	}
}

func TestNonceIsSharedAcrossSpotAndPerpSigning(t *testing.T) {
	fixed := time.UnixMicro(1_748_310_859_508_867)
	security, err := NewSecurityContext(CredentialConfig{
		User:       "0x1111111111111111111111111111111111111111",
		PrivateKey: fmt.Sprintf("%064x", 1),
	}, WithClock(ClockFunc(func() time.Time { return fixed })))
	if err != nil {
		t.Fatal(err)
	}
	spot, _ := NewProfile(EnvironmentTestnet, ProductSpot)
	perp, _ := NewProfile(EnvironmentTestnet, ProductPerp)
	profiles := []Profile{spot, perp}

	const count = 200
	values := make([]int64, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			signed, signErr := security.Sign(profiles[index%len(profiles)], url.Values{"symbol": {"ASTERUSDT"}})
			if signErr != nil {
				t.Errorf("Sign: %v", signErr)
				return
			}
			values[index], signErr = strconv.ParseInt(signed.Values().Get("nonce"), 10, 64)
			if signErr != nil {
				t.Errorf("parse nonce: %v", signErr)
			}
		}(i)
	}
	wg.Wait()
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for i, value := range values {
		want := fixed.UnixMicro() + int64(i)
		if value != want {
			t.Fatalf("nonce[%d] = %d, want %d", i, value, want)
		}
	}
}
