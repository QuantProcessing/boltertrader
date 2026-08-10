package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5"
)

func (store *ReadModelStore) ApplyPosition(
	ctx context.Context,
	bootID string,
	projection readmodel.PositionProjection,
) (readmodel.Event, error) {
	if err := validatePositionProjection(bootID, projection); err != nil {
		return readmodel.Event{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return readmodel.Event{}, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		insert into positions(
			id, symbol, payload, principal_id, mode, status,
			created_at, updated_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$7)
		on conflict(id) do update set
			symbol=excluded.symbol,
			payload=excluded.payload,
			status=excluded.status,
			updated_at=excluded.updated_at
		where positions.principal_id=excluded.principal_id
		  and positions.mode=excluded.mode
	`,
		projection.ID,
		projection.Symbol,
		projection.Payload,
		projection.Scope.PrincipalID,
		projection.Scope.Mode,
		projection.Status,
		projection.UpdatedAt.UTC(),
	)
	if err != nil {
		return readmodel.Event{}, err
	}
	if tag.RowsAffected() != 1 {
		return readmodel.Event{}, fmt.Errorf("position identity conflicts across projection scopes")
	}
	event, err := appendEventTx(ctx, tx, readmodel.Event{
		BootID: bootID, PrincipalID: projection.Scope.PrincipalID,
		Mode:       projection.Scope.Mode,
		Kind:       readmodel.PositionUpsert,
		Scope:      []string{"positions"},
		EntityID:   projection.ID,
		Payload:    projection.Payload,
		OccurredAt: projection.UpdatedAt.UTC(),
	})
	if err != nil {
		return readmodel.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return readmodel.Event{}, err
	}
	return event, nil
}

func (store *ReadModelStore) AppendActivity(
	ctx context.Context,
	bootID string,
	projection readmodel.ActivityProjection,
) (readmodel.Event, error) {
	if err := validateActivityProjection(bootID, projection); err != nil {
		return readmodel.Event{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return readmodel.Event{}, err
	}
	defer tx.Rollback(ctx)

	var insertedID string
	err = tx.QueryRow(ctx, `
		insert into activities(
			id, type, payload, principal_id, mode, position_id,
			execution_id, occurred_at, created_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$8,$8)
		on conflict(id) do nothing
		returning id
	`,
		projection.ID,
		projection.Kind,
		projection.Payload,
		projection.Scope.PrincipalID,
		projection.Scope.Mode,
		nullableString(projection.PositionID),
		nullableString(projection.ExecutionID),
		projection.OccurredAt.UTC(),
	).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var matchesExisting bool
		if lookupErr := tx.QueryRow(ctx, `
			select principal_id=$2
			   and mode=$3
			   and type=$4
			   and coalesce(position_id, '')=$5
			   and coalesce(execution_id, '')=$6
			   and occurred_at=$7
			   and payload=$8::jsonb
			from activities
			where id=$1
		`,
			projection.ID,
			projection.Scope.PrincipalID,
			projection.Scope.Mode,
			projection.Kind,
			projection.PositionID,
			projection.ExecutionID,
			projection.OccurredAt.UTC(),
			projection.Payload,
		).Scan(&matchesExisting); lookupErr != nil {
			return readmodel.Event{}, lookupErr
		}
		if !matchesExisting {
			return readmodel.Event{}, fmt.Errorf("activity identity conflicts with existing projection")
		}
		return readmodel.Event{}, nil
	}
	if err != nil {
		return readmodel.Event{}, err
	}
	event, err := appendEventTx(ctx, tx, readmodel.Event{
		BootID: bootID, PrincipalID: projection.Scope.PrincipalID,
		Mode:       projection.Scope.Mode,
		Kind:       readmodel.ActivityAppend,
		Scope:      []string{"activities"},
		EntityID:   insertedID,
		Payload:    projection.Payload,
		OccurredAt: projection.OccurredAt.UTC(),
	})
	if err != nil {
		return readmodel.Event{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return readmodel.Event{}, err
	}
	return event, nil
}

// ApplyExecutionProjection commits the position, activity, and their outbox
// events as one unit. This prevents a durable position from advancing without
// its corresponding user-visible activity.
func (store *ReadModelStore) ApplyExecutionProjection(
	ctx context.Context,
	bootID string,
	position readmodel.PositionProjection,
	activity readmodel.ActivityProjection,
) ([]readmodel.Event, error) {
	if err := validatePositionProjection(bootID, position); err != nil {
		return nil, err
	}
	if err := validateActivityProjection(bootID, activity); err != nil {
		return nil, err
	}
	if position.Scope != activity.Scope ||
		position.ID != activity.PositionID ||
		!position.UpdatedAt.UTC().Equal(activity.OccurredAt.UTC()) {
		return nil, fmt.Errorf("execution projection identity mismatch")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		insert into positions(
			id, symbol, payload, principal_id, mode, status,
			created_at, updated_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$7)
		on conflict(id) do update set
			symbol=excluded.symbol,
			payload=excluded.payload,
			status=excluded.status,
			updated_at=excluded.updated_at
		where positions.principal_id=excluded.principal_id
		  and positions.mode=excluded.mode
	`,
		position.ID,
		position.Symbol,
		position.Payload,
		position.Scope.PrincipalID,
		position.Scope.Mode,
		position.Status,
		position.UpdatedAt.UTC(),
	)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, fmt.Errorf("position identity conflicts across projection scopes")
	}
	positionEvent, err := appendEventTx(ctx, tx, readmodel.Event{
		BootID: bootID, PrincipalID: position.Scope.PrincipalID,
		Mode:       position.Scope.Mode,
		Kind:       readmodel.PositionUpsert,
		Scope:      []string{"positions"},
		EntityID:   position.ID,
		Payload:    position.Payload,
		OccurredAt: position.UpdatedAt.UTC(),
	})
	if err != nil {
		return nil, err
	}

	var insertedID string
	err = tx.QueryRow(ctx, `
		insert into activities(
			id, type, payload, principal_id, mode, position_id,
			execution_id, occurred_at, created_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$8,$8)
		on conflict(id) do nothing
		returning id
	`,
		activity.ID,
		activity.Kind,
		activity.Payload,
		activity.Scope.PrincipalID,
		activity.Scope.Mode,
		nullableString(activity.PositionID),
		nullableString(activity.ExecutionID),
		activity.OccurredAt.UTC(),
	).Scan(&insertedID)
	events := []readmodel.Event{positionEvent}
	if errors.Is(err, pgx.ErrNoRows) {
		var matchesExisting bool
		if lookupErr := tx.QueryRow(ctx, `
			select principal_id=$2
			   and mode=$3
			   and type=$4
			   and coalesce(position_id, '')=$5
			   and coalesce(execution_id, '')=$6
			   and occurred_at=$7
			   and payload=$8::jsonb
			from activities
			where id=$1
		`,
			activity.ID,
			activity.Scope.PrincipalID,
			activity.Scope.Mode,
			activity.Kind,
			activity.PositionID,
			activity.ExecutionID,
			activity.OccurredAt.UTC(),
			activity.Payload,
		).Scan(&matchesExisting); lookupErr != nil {
			return nil, lookupErr
		}
		if !matchesExisting {
			return nil, fmt.Errorf("activity identity conflicts with existing projection")
		}
	} else if err != nil {
		return nil, err
	} else {
		activityEvent, appendErr := appendEventTx(ctx, tx, readmodel.Event{
			BootID: bootID, PrincipalID: activity.Scope.PrincipalID,
			Mode:       activity.Scope.Mode,
			Kind:       readmodel.ActivityAppend,
			Scope:      []string{"activities"},
			EntityID:   insertedID,
			Payload:    activity.Payload,
			OccurredAt: activity.OccurredAt.UTC(),
		})
		if appendErr != nil {
			return nil, appendErr
		}
		events = append(events, activityEvent)
	}
	if err := pruneReadModelEventsTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return events, nil
}

func validatePositionProjection(bootID string, projection readmodel.PositionProjection) error {
	if strings.TrimSpace(bootID) == "" ||
		strings.TrimSpace(projection.ID) == "" ||
		strings.TrimSpace(projection.Scope.PrincipalID) == "" ||
		!isProductMode(projection.Scope.Mode) ||
		strings.TrimSpace(projection.Symbol) == "" ||
		strings.TrimSpace(projection.Status) == "" ||
		projection.UpdatedAt.IsZero() ||
		!json.Valid(projection.Payload) {
		return fmt.Errorf("invalid position projection")
	}
	return nil
}

func validateActivityProjection(bootID string, projection readmodel.ActivityProjection) error {
	if strings.TrimSpace(bootID) == "" ||
		strings.TrimSpace(projection.ID) == "" ||
		strings.TrimSpace(projection.Scope.PrincipalID) == "" ||
		!isProductMode(projection.Scope.Mode) ||
		strings.TrimSpace(projection.Kind) == "" ||
		projection.OccurredAt.IsZero() ||
		!json.Valid(projection.Payload) {
		return fmt.Errorf("invalid activity projection")
	}
	return nil
}
