package migration

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresValidationSnapshotRequest struct {
	PlatformService, PlatformDatabase string
	GatewayService, GatewayDatabase   string
}

func CapturePostgresValidationSnapshot(ctx context.Context, request PostgresValidationSnapshotRequest) (ValidationSnapshot, error) {
	platform, err := pgxpool.New(ctx, "service="+request.PlatformService+" dbname="+request.PlatformDatabase)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("connect Platform snapshot source: %w", err)
	}
	defer platform.Close()
	gateway, err := pgxpool.New(ctx, "service="+request.GatewayService+" dbname="+request.GatewayDatabase)
	if err != nil {
		return ValidationSnapshot{}, fmt.Errorf("connect Gateway snapshot source: %w", err)
	}
	defer gateway.Close()
	source := &postgresValidationSnapshotSource{platform: platform, gateway: gateway}
	return CaptureValidationSnapshot(ctx, source, authority.PlatformV15(), authority.GatewayV6())
}

type postgresValidationSnapshotSource struct{ platform, gateway *pgxpool.Pool }

func (source *postgresValidationSnapshotSource) AuthorityRows(ctx context.Context, matrix authority.Matrix, object authority.Object) ([]SnapshotRow, error) {
	pool := source.platform
	if matrix.Name == authority.GatewayV6().Name {
		pool = source.gateway
	}
	identifier := pgx.Identifier{object.Schema, object.Name}.Sanitize()
	query, args := "SELECT * FROM "+identifier, []any(nil)
	switch object.Kind {
	case authority.Sequence:
		query = "SELECT last_value,is_called FROM " + identifier
	case authority.Routine:
		query, args = "SELECT pg_get_functiondef($1::regprocedure)", []any{object.Schema + "." + object.Name}
	}
	return querySnapshotRows(ctx, pool, query, args...)
}

func (source *postgresValidationSnapshotSource) GatewayActiveRows(ctx context.Context) ([]SnapshotRow, error) {
	return querySnapshotRows(ctx, source.gateway, `SELECT head.account_id,head.active_snapshot_id,snapshot.operation_id,snapshot.desired_state_revision,snapshot.runtime_generation,snapshot.credential_version,snapshot.request_digest FROM gateway_account_runtime_heads head LEFT JOIN gateway_runtime_snapshots snapshot ON snapshot.snapshot_id=head.active_snapshot_id WHERE head.active_snapshot_id IS NOT NULL`)
}

func (source *postgresValidationSnapshotSource) AuthSessionRows(ctx context.Context) (map[string]SnapshotRow, error) {
	rows, err := source.platform.Query(ctx, `SELECT * FROM platform_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]SnapshotRow)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("platform session row has no identity")
		}
		id, ok := values[0].(string)
		if !ok || id == "" {
			return nil, fmt.Errorf("platform session identity is invalid")
		}
		result[id], err = typedSnapshotRow(rows.FieldDescriptions(), values)
		if err != nil {
			return nil, err
		}
	}
	return result, rows.Err()
}

func querySnapshotRows(ctx context.Context, pool *pgxpool.Pool, query string, args ...any) ([]SnapshotRow, error) {
	rows, err := pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []SnapshotRow
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row, err := typedSnapshotRow(rows.FieldDescriptions(), values)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func typedSnapshotRow(fields []pgconn.FieldDescription, values []any) (SnapshotRow, error) {
	if len(fields) != len(values) {
		return nil, fmt.Errorf("snapshot field/value mismatch")
	}
	row := make(SnapshotRow, len(values))
	for index, value := range values {
		row[index].Type = fmt.Sprintf("oid:%d", fields[index].DataTypeOID)
		if value == nil {
			row[index].Null = true
			continue
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode snapshot cell: %w", err)
		}
		row[index].Value = string(encoded)
	}
	return row, nil
}
