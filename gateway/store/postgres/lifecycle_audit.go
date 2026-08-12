package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountLifecycleAuditStore writes the account lifecycle's audit trail into
// the same audit_records table the order path uses. It is a separate type from
// Store's AppendAudit because the order path's record is correlation-scoped:
// it requires a command correlation id that an account transition has no
// equivalent of, and its action set is frozen to the order verbs.
type AccountLifecycleAuditStore struct {
	pool     *pgxpool.Pool
	identity *domain.IdentityGenerator
}

func NewAccountLifecycleAuditStore(pool *pgxpool.Pool) *AccountLifecycleAuditStore {
	return &AccountLifecycleAuditStore{pool: pool, identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})}
}

func (store *AccountLifecycleAuditStore) Append(ctx context.Context, record lifecycle.AuditRecord) error {
	if err := validateLifecycleAuditRecord(record); err != nil {
		return err
	}
	auditID, err := store.identity.EventID()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]string{
		"resource_type": "account",
		"request_id":    record.RequestID,
	})
	if err != nil {
		return err
	}
	if _, err := store.pool.Exec(ctx, `
		INSERT INTO audit_records(audit_id, principal_id, action, resource_id, outcome, metadata, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, auditID, record.PrincipalID, record.Action, record.ResourceID, record.Outcome, metadata, record.OccurredAt.UTC()); err != nil {
		return redactErr("append account lifecycle audit record", err)
	}
	return nil
}

var errInvalidLifecycleAuditRecord = errors.New("account lifecycle audit record is invalid")

// The audit trail is one of the few places an operator reads raw strings back
// out, so a record that names credential material is rejected rather than
// stored: the trail must stay safe to display.
func validateLifecycleAuditRecord(record lifecycle.AuditRecord) error {
	values := []string{record.PrincipalID, record.Action, record.ResourceID, record.Outcome, record.RequestID}
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 256 {
			return errInvalidLifecycleAuditRecord
		}
	}
	if record.OccurredAt.IsZero() {
		return errInvalidLifecycleAuditRecord
	}
	combined := strings.ToLower(fmt.Sprint(values))
	for _, sensitive := range []string{"secretmaterial", "secretref", "password", "authorization"} {
		if strings.Contains(combined, sensitive) {
			return fmt.Errorf("%w: sensitive field", errInvalidLifecycleAuditRecord)
		}
	}
	return nil
}
