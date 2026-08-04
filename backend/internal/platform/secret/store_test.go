package secret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

type recordingProvider struct {
	readinessErr error
	putRef       string
	putErr       error
	mode         Mode
	namespace    string
	keyVersion   string
	secret       []byte
}

func (provider *recordingProvider) Check(_ context.Context, scope Scope) error {
	provider.mode = scope.Mode
	provider.namespace = scope.Namespace
	provider.keyVersion = scope.KeyVersion
	return provider.readinessErr
}

func (provider *recordingProvider) Put(_ context.Context, scope Scope, value []byte) (string, error) {
	provider.mode = scope.Mode
	provider.namespace = scope.Namespace
	provider.keyVersion = scope.KeyVersion
	provider.secret = append([]byte(nil), value...)
	return provider.putRef, provider.putErr
}

func TestNewStoreFreezesModeNamespaceAndKeyVersion(t *testing.T) {
	provider := &recordingProvider{}
	store, err := NewStore(Config{
		Mode:       ModeTest,
		Namespace:  "boltertrader/test",
		KeyVersion: "v1",
	}, provider)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Readiness(context.Background()); err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	if provider.mode != ModeTest || provider.namespace != "boltertrader/test" || provider.keyVersion != "v1" {
		t.Fatalf("provider scope = (%q, %q, %q)", provider.mode, provider.namespace, provider.keyVersion)
	}
}

func TestNewStoreRejectsCrossModeNamespaceAndUnsupportedKeyVersion(t *testing.T) {
	provider := &recordingProvider{}
	tests := []struct {
		name   string
		config Config
		code   Code
	}{
		{name: "test with live namespace", config: Config{Mode: ModeTest, Namespace: "boltertrader/live", KeyVersion: "v1"}, code: CodeKeyMismatch},
		{name: "live with test namespace", config: Config{Mode: ModeLive, Namespace: "boltertrader/test", KeyVersion: "v1"}, code: CodeKeyMismatch},
		{name: "missing version", config: Config{Mode: ModeTest, Namespace: "boltertrader/test"}, code: CodeKeyVersionUnsupported},
		{name: "unsupported version", config: Config{Mode: ModeLive, Namespace: "boltertrader/live", KeyVersion: "v2"}, code: CodeKeyVersionUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewStore(test.config, provider)
			if !IsCode(err, test.code) {
				t.Fatalf("NewStore error = %v, want %s", err, test.code)
			}
		})
	}
}

func TestStoreReturnsOnlyOpaqueReferenceAndRedactsProviderErrors(t *testing.T) {
	const canary = "secret-canary-do-not-log"
	provider := &recordingProvider{putRef: "provider-internal-id-42"}
	store, err := NewStore(Config{Mode: ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}, provider)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ref, err := store.Put(context.Background(), []byte(canary))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(provider.secret) != canary {
		t.Fatal("provider did not receive the submitted credential")
	}
	if strings.Contains(fmt.Sprintf("%+v", ref), canary) || strings.Contains(fmt.Sprintf("%+v", ref), provider.putRef) {
		t.Fatalf("reference formatting exposed sensitive material: %+v", ref)
	}
	if ref.Namespace() != "boltertrader/test" || ref.KeyVersion() != "v1" || ref.IsZero() {
		t.Fatalf("reference metadata = %+v", ref)
	}

	provider.putErr = errors.New("provider failed while storing " + canary)
	if _, err := store.Put(context.Background(), []byte(canary)); err == nil {
		t.Fatal("Put accepted provider failure")
	} else if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), provider.putRef) {
		t.Fatalf("Put leaked provider material: %v", err)
	}
}

func TestReadinessMapsStableFailureCodesWithoutProviderDetails(t *testing.T) {
	const canary = "secret-provider-detail"
	tests := []struct {
		providerErr error
		code        Code
	}{
		{providerErr: ErrKeyMissing, code: CodeKeyMissing},
		{providerErr: ErrKeyMismatch, code: CodeKeyMismatch},
		{providerErr: ErrKeyVersionUnsupported, code: CodeKeyVersionUnsupported},
		{providerErr: errors.New(canary), code: CodeUnavailable},
	}
	for _, test := range tests {
		provider := &recordingProvider{readinessErr: test.providerErr}
		store, err := NewStore(Config{Mode: ModeLive, Namespace: "boltertrader/live", KeyVersion: "v1"}, provider)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		err = store.Readiness(context.Background())
		if !IsCode(err, test.code) {
			t.Fatalf("Readiness error = %v, want %s", err, test.code)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("Readiness leaked provider detail: %v", err)
		}
	}
}
