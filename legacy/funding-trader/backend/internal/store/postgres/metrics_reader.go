package postgres

import (
	"context"
	"errors"

	"github.com/QuantProcessing/funding-trader/backend/internal/observability"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5/pgxpool"
)

type MetricsReader struct {
	pool *pgxpool.Pool
}

func NewMetricsReader(pool *pgxpool.Pool) (*MetricsReader, error) {
	if pool == nil {
		return nil, errors.New("metrics pool is required")
	}
	return &MetricsReader{pool: pool}, nil
}

func (reader *MetricsReader) Snapshot(ctx context.Context, scope readmodel.Scope) (observability.Snapshot, error) {
	if err := validateReadModelScope(scope); err != nil {
		return observability.Snapshot{}, err
	}
	var snapshot observability.Snapshot
	err := reader.pool.QueryRow(ctx, `
		select
			count(*) filter (where status='partially_hedged'),
			count(*) filter (where status='compensating'),
			count(*) filter (where status='uncertain'),
			count(*) filter (
				where status='manual_intervention_required' or manual_intervention
			),
			(
				select count(*)
				from read_model_events
				where mode=$2
				  and contract_version=$3
				  and (principal_id is null or principal_id=$1)
				  and kind='resnapshot.required'
			)
		from executions
		where principal_id=$1 and mode=$2
	`, scope.PrincipalID, scope.Mode, readmodel.ContractVersion).Scan(
		&snapshot.PartiallyHedged,
		&snapshot.Compensating,
		&snapshot.Uncertain,
		&snapshot.ManualInterventionRequired,
		&snapshot.ResnapshotRequired,
	)
	if err != nil {
		return observability.Snapshot{}, err
	}
	return snapshot, nil
}
