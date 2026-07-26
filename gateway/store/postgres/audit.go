package postgres

import (
	"context"
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func (postgresStore *Store) AppendAudit(ctx context.Context, record store.AuditRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	auditID, err := postgresStore.identity.EventID()
	if err != nil {
		return err
	}
	metadata, err := auditMetadata(record.CorrelationID, record.AccountID, record.InstrumentID)
	if err != nil {
		return err
	}
	if _, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO audit_records(audit_id, principal_id, action, resource_id, outcome, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, auditID, record.PrincipalID, record.Action, record.ResourceID, record.Outcome, metadata); err != nil {
		return redactErr("append audit record", err)
	}
	return nil
}

func auditMetadata(correlationID domain.EventID, accountID domain.AccountID, instrumentID domain.InstrumentID) ([]byte, error) {
	return json.Marshal(map[string]string{
		"correlation_id": string(correlationID),
		"account_id":     string(accountID),
		"instrument_id":  string(instrumentID),
	})
}
