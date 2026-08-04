package accounts

import (
	"context"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/audit"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/secret"
)

type SecretStoreAdapter struct{ Store *secret.Store }

func (adapter SecretStoreAdapter) Put(ctx context.Context, material []byte) (string, error) {
	reference, err := adapter.Store.Put(ctx, material)
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
