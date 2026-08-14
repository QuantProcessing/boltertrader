package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

type scopedCredentialWriter struct {
	ref       secretstore.OpaqueRef
	err       error
	deleted   string
	lookupRef secretstore.OpaqueRef
}

func (writer *scopedCredentialWriter) Check(context.Context, secretstore.Scope) error {
	return writer.err
}

func (writer *scopedCredentialWriter) Put(context.Context, secretstore.Scope, secretstore.OperationID, []byte) (secretstore.OpaqueRef, error) {
	return writer.ref, writer.err
}

func (writer *scopedCredentialWriter) LookupOperation(context.Context, secretstore.Scope, secretstore.OperationID) (secretstore.OpaqueRef, error) {
	return writer.lookupRef, writer.err
}

func (writer *scopedCredentialWriter) Delete(_ context.Context, _ secretstore.Scope, ref secretstore.OpaqueRef) error {
	writer.deleted = ref.RawForTrustedBoundary()
	return writer.err
}

// The lifecycle stores whatever string the adapter returns as the account's
// secret reference, so handing back a redacted rendering instead of the
// provider handle would silently orphan the credential.
func TestSecretStoreAdapterCarriesTheProviderHandleAndDropsTheScope(t *testing.T) {
	raw := strings.Repeat("A", 43)
	writer := &scopedCredentialWriter{ref: secretstore.OpaqueRef(raw), lookupRef: secretstore.OpaqueRef(raw)}
	store, err := secret.NewStore(secret.Config{Mode: secret.ModeTest, Namespace: secret.TestNamespace, KeyVersion: "v1"}, writer)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	adapter := SecretStoreAdapter{Store: store}
	ctx := t.Context()
	const operationID secretstore.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"

	stored, err := adapter.Put(ctx, operationID, []byte("material"))
	if err != nil || stored != raw {
		t.Fatalf("Put = %q / %v", stored, err)
	}
	looked, err := adapter.LookupOperation(ctx, operationID)
	if err != nil || looked != raw {
		t.Fatalf("LookupOperation = %q / %v", looked, err)
	}
	if err := adapter.Delete(ctx, stored); err != nil || writer.deleted != raw {
		t.Fatalf("Delete = %q / %v", writer.deleted, err)
	}

	writer.err = errors.New("provider unavailable")
	if stored, err := adapter.Put(ctx, operationID, []byte("material")); err == nil || stored != "" {
		t.Fatalf("failed Put = %q / %v", stored, err)
	}
	if looked, err := adapter.LookupOperation(ctx, operationID); err == nil || looked != "" {
		t.Fatalf("failed LookupOperation = %q / %v", looked, err)
	}
}
