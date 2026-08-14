package secret_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

type recordingCredentialWriter struct {
	readinessErr error
	putRef       secretstore.OpaqueRef
	putErr       error
	mode         secretstore.Mode
	namespace    string
	keyVersion   string
	secret       []byte
	deleteRef    string
	deleteErr    error
}

func (writer *recordingCredentialWriter) Check(_ context.Context, scope secretstore.Scope) error {
	writer.record(scope)
	return writer.readinessErr
}

func (writer *recordingCredentialWriter) Put(_ context.Context, scope secretstore.Scope, _ secretstore.OperationID, value []byte) (secretstore.OpaqueRef, error) {
	writer.record(scope)
	writer.secret = append([]byte(nil), value...)
	return writer.putRef, writer.putErr
}

func (writer *recordingCredentialWriter) LookupOperation(_ context.Context, scope secretstore.Scope, _ secretstore.OperationID) (secretstore.OpaqueRef, error) {
	writer.record(scope)
	return writer.putRef, writer.putErr
}

func (writer *recordingCredentialWriter) Delete(_ context.Context, scope secretstore.Scope, reference secretstore.OpaqueRef) error {
	writer.record(scope)
	writer.deleteRef = reference.RawForTrustedBoundary()
	return writer.deleteErr
}

func (writer *recordingCredentialWriter) record(scope secretstore.Scope) {
	writer.mode = scope.Mode
	writer.namespace = scope.Namespace
	writer.keyVersion = scope.KeyVersion
}

const credentialOperationID secretstore.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"

func TestCredentialStoreFreezesModeNamespaceAndKeyVersion(t *testing.T) {
	writer := &recordingCredentialWriter{}
	store, err := secret.NewStore(secret.Config{
		Mode:       secret.ModeTest,
		Namespace:  secret.TestNamespace,
		KeyVersion: "v1",
	}, writer)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	if err := store.Readiness(context.Background()); err != nil {
		t.Fatalf("Readiness: %v", err)
	}
	if writer.mode != secret.ModeTest || writer.namespace != secret.TestNamespace || writer.keyVersion != "v1" {
		t.Fatalf("provider scope = (%q, %q, %q)", writer.mode, writer.namespace, writer.keyVersion)
	}
}

// A test-mode store addressing live material is the failure that loses real
// money, so the scope mismatch has to be rejected at construction rather than
// at the first write.
func TestCredentialStoreRejectsCrossModeNamespaceAndUnsupportedKeyVersion(t *testing.T) {
	writer := &recordingCredentialWriter{}
	tests := []struct {
		name   string
		config secret.Config
		code   secret.Code
	}{
		{name: "test with live namespace", config: secret.Config{Mode: secret.ModeTest, Namespace: secret.LiveNamespace, KeyVersion: "v1"}, code: secret.CodeKeyMismatch},
		{name: "live with test namespace", config: secret.Config{Mode: secret.ModeLive, Namespace: secret.TestNamespace, KeyVersion: "v1"}, code: secret.CodeKeyMismatch},
		{name: "missing version", config: secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace}, code: secret.CodeKeyVersionUnsupported},
		{name: "unsupported version", config: secret.Config{Mode: secret.ModeLive, Namespace: secret.LiveNamespace, KeyVersion: "v2"}, code: secret.CodeKeyVersionUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := secret.NewStore(test.config, writer); !secret.IsCode(err, test.code) {
				t.Fatalf("NewStore error = %v, want %s", err, test.code)
			}
		})
	}
	if _, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, nil); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("NewStore without a provider = %v", err)
	}
}

func TestCredentialStoreReturnsOnlyOpaqueReferenceAndRedactsProviderErrors(t *testing.T) {
	const canary = "secret-canary-do-not-log"
	writer := &recordingCredentialWriter{putRef: secretstore.OpaqueRef(strings.Repeat("A", 43))}
	store, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, writer)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ref, err := store.Put(context.Background(), credentialOperationID, []byte(canary))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if string(writer.secret) != canary {
		t.Fatal("provider did not receive the submitted credential")
	}
	rendered := fmt.Sprintf("%+v %#v %s", ref, ref, ref)
	if strings.Contains(rendered, canary) || strings.Contains(rendered, writer.putRef.RawForTrustedBoundary()) {
		t.Fatalf("reference formatting exposed sensitive material: %s", rendered)
	}
	if ref.Namespace() != secret.TestNamespace || ref.KeyVersion() != "v1" || ref.IsZero() {
		t.Fatalf("reference metadata = %+v", ref)
	}
	if ref.RawForProvider() != writer.putRef.RawForTrustedBoundary() {
		t.Fatal("reference lost the provider-facing value")
	}

	// The provider is free to quote the material it failed on, so its error must
	// not survive the boundary.
	writer.putErr = errors.New("provider failed while storing " + canary)
	if _, err := store.Put(context.Background(), credentialOperationID, []byte(canary)); err == nil {
		t.Fatal("Put accepted provider failure")
	} else if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), writer.putRef.RawForTrustedBoundary()) {
		t.Fatalf("Put leaked provider material: %v", err)
	}

	// A provider that reports neither a reference nor an error has still failed:
	// an empty reference would otherwise be bound to the account as if it named
	// stored material.
	writer.putErr, writer.putRef = nil, ""
	if _, err := store.Put(context.Background(), credentialOperationID, []byte(canary)); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("Put with an empty reference = %v", err)
	}
	if _, err := store.Put(context.Background(), credentialOperationID, nil); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("Put without material = %v", err)
	}
	if _, err := store.Put(context.Background(), "", []byte(canary)); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("Put without an operation = %v", err)
	}
}

func TestCredentialStoreLooksUpOperationsForReplay(t *testing.T) {
	writer := &recordingCredentialWriter{putRef: secretstore.OpaqueRef(strings.Repeat("B", 43))}
	store, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, writer)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	ref, err := store.LookupOperation(context.Background(), credentialOperationID)
	if err != nil {
		t.Fatalf("LookupOperation: %v", err)
	}
	if ref.RawForProvider() != writer.putRef.RawForTrustedBoundary() || ref.KeyVersion() != "v1" {
		t.Fatalf("lookup reference = %+v", ref)
	}

	// An unwritten operation is indistinguishable from an unavailable provider
	// at this layer: both mean the caller may not proceed on a reference.
	writer.putRef = ""
	if _, err := store.LookupOperation(context.Background(), credentialOperationID); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("missing operation = %v", err)
	}
}

func TestCredentialStoreReadinessMapsStableFailureCodesWithoutProviderDetails(t *testing.T) {
	const canary = "secret-provider-detail"
	tests := []struct {
		providerErr error
		code        secret.Code
	}{
		{providerErr: secret.ErrKeyMissing, code: secret.CodeKeyMissing},
		{providerErr: secret.ErrKeyMismatch, code: secret.CodeKeyMismatch},
		{providerErr: secretstore.ErrInvalidScope, code: secret.CodeKeyMismatch},
		{providerErr: secret.ErrKeyVersionUnsupported, code: secret.CodeKeyVersionUnsupported},
		{providerErr: errors.New(canary), code: secret.CodeUnavailable},
	}
	for _, test := range tests {
		writer := &recordingCredentialWriter{readinessErr: test.providerErr}
		store, err := secret.NewStore(secret.Config{Mode: secret.ModeLive, Namespace: secret.LiveNamespace, KeyVersion: "v1"}, writer)
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		err = store.Readiness(context.Background())
		if !secret.IsCode(err, test.code) {
			t.Fatalf("Readiness error = %v, want %s", err, test.code)
		}
		if strings.Contains(err.Error(), canary) {
			t.Fatalf("Readiness leaked provider detail: %v", err)
		}
	}
}

func TestCredentialStoreDeleteUsesOpaqueProviderReferenceAndRedactsFailure(t *testing.T) {
	writer := &recordingCredentialWriter{}
	store, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, writer)
	if err != nil {
		t.Fatal(err)
	}
	rawRef := strings.Repeat("A", 43)
	if err := store.Delete(context.Background(), rawRef); err != nil {
		t.Fatal(err)
	}
	if writer.deleteRef != rawRef {
		t.Fatalf("delete ref = %q", writer.deleteRef)
	}
	writer.deleteErr = errors.New("delete provider-ref-1 with secret-canary")
	if err := store.Delete(context.Background(), rawRef); err == nil || strings.Contains(err.Error(), "provider-ref-1") || strings.Contains(err.Error(), "secret-canary") {
		t.Fatalf("Delete error = %v", err)
	}

	// A reference that is not a well-formed opaque handle never reaches the
	// provider, so a malformed value cannot address someone else's material.
	writer.deleteRef = ""
	if err := store.Delete(context.Background(), "not-an-opaque-reference"); !secret.IsCode(err, secret.CodeUnavailable) {
		t.Fatalf("malformed reference = %v", err)
	}
	if writer.deleteRef != "" {
		t.Fatalf("malformed reference reached the provider as %q", writer.deleteRef)
	}
}
