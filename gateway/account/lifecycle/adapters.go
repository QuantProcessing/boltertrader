package lifecycle

import (
	"context"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

// SecretStoreAdapter narrows a scoped credential store to the three calls the
// lifecycle needs. The lifecycle only ever handles the provider-facing handle,
// never the reference's scope metadata, so the adapter is where the richer
// type is dropped.
type SecretStoreAdapter struct{ Store *secret.Store }

var (
	_ SecretWriter          = SecretStoreAdapter{}
	_ SecretCleaner         = SecretStoreAdapter{}
	_ SecretOperationLookup = SecretStoreAdapter{}
)

func (adapter SecretStoreAdapter) Put(ctx context.Context, operationID secretstore.OperationID, material []byte) (string, error) {
	reference, err := adapter.Store.Put(ctx, operationID, material)
	if err != nil {
		return "", err
	}
	return reference.RawForProvider(), nil
}

func (adapter SecretStoreAdapter) LookupOperation(ctx context.Context, operationID secretstore.OperationID) (string, error) {
	reference, err := adapter.Store.LookupOperation(ctx, operationID)
	if err != nil {
		return "", err
	}
	return reference.RawForProvider(), nil
}

func (adapter SecretStoreAdapter) Delete(ctx context.Context, reference string) error {
	return adapter.Store.Delete(ctx, reference)
}
