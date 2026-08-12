package postgres

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/QuantProcessing/boltertrader/services/trading/audit"
)

type AuditStore struct{ db *sql.DB }

func NewAuditStore(db *sql.DB) *AuditStore { return &AuditStore{db: db} }

func (store *AuditStore) Append(ctx context.Context, record audit.Record) error {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO platform_audit
		(audit_id, principal_id, action, resource_type, resource_id, outcome, request_id, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, record.ID, record.PrincipalID, record.Action,
		record.ResourceType, record.ResourceID, record.Outcome, record.RequestID, record.OccurredAt)
	return err
}

func (store *AuditStore) List(ctx context.Context, filter audit.Filter) ([]audit.Record, error) {
	query := `SELECT audit_id, principal_id, action, resource_type, resource_id, outcome, request_id, occurred_at FROM platform_audit`
	args := []any{}
	if filter.ResourceID != "" {
		query += ` WHERE resource_id = $1`
		args = append(args, filter.ResourceID)
	}
	args = append(args, filter.Limit)
	query += ` ORDER BY occurred_at DESC, audit_id DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []audit.Record
	for rows.Next() {
		var record audit.Record
		if err := rows.Scan(&record.ID, &record.PrincipalID, &record.Action, &record.ResourceType, &record.ResourceID, &record.Outcome, &record.RequestID, &record.OccurredAt); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}
