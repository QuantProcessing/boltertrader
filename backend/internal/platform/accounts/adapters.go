package accounts

import (
	"context"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/secret"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

type SecretStoreAdapter struct{ Store *secret.Store }

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

type AuditServiceAdapter struct{ Service *audit.Service }

func (adapter AuditServiceAdapter) Append(ctx context.Context, record AuditRecord) error {
	_, err := adapter.Service.Record(ctx, audit.Entry{
		PrincipalID:  record.PrincipalID,
		Action:       record.Action,
		ResourceType: "account",
		ResourceID:   record.ResourceID,
		Outcome:      record.Outcome,
		RequestID:    record.RequestID,
	})
	return err
}
