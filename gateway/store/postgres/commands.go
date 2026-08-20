package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const reserveLockKey int32 = 0x470005

func (postgresStore *Store) ReconciliationStatus(ctx context.Context, now time.Time) (int, time.Duration, error) {
	if now.IsZero() {
		return 0, 0, fmt.Errorf("reconciliation status time is required")
	}
	var count int
	var oldest time.Time
	if err := postgresStore.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(o.updated_at), $1::timestamptz)
		FROM reconciliation_tasks r
		JOIN orders o ON o.gateway_order_id = r.gateway_order_id
	`, now).Scan(&count, &oldest); err != nil {
		return 0, 0, redactErr("load reconciliation status", err)
	}
	if count == 0 || !now.After(oldest) {
		return count, 0, nil
	}
	return count, now.Sub(oldest), nil
}

func (postgresStore *Store) ReserveCommand(ctx context.Context, request store.ReserveCommandRequest) (store.ReservedCommand, error) {
	if err := request.Validate(); err != nil {
		return store.ReservedCommand{}, err
	}
	product, err := productFromInstrument(request.InstrumentID)
	if err != nil {
		return store.ReservedCommand{}, err
	}

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.ReservedCommand{}, redactErr("begin reserve command", err)
	}
	defer tx.Rollback(ctx)

	if postgresStore.beforeReserveInsert != nil {
		if err := postgresStore.beforeReserveInsert(ctx); err != nil {
			return store.ReservedCommand{}, err
		}
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, reserveLockKey, stableLockPart(string(request.PrincipalID)+"\x00"+string(request.IdempotencyKey))); err != nil {
		return store.ReservedCommand{}, redactErr("lock idempotency reservation", err)
	}

	existing, found, err := loadReservation(ctx, tx, request.PrincipalID, request.IdempotencyKey)
	if err != nil {
		return store.ReservedCommand{}, err
	}
	if found {
		existing.New = false
		existing.Conflict = reservationConflict(existing, request, product)
		return existing.ReservedCommand, nil
	}

	commandID, err := postgresStore.identity.CommandID()
	if err != nil {
		return store.ReservedCommand{}, err
	}
	gatewayOrderID, err := postgresStore.identity.GatewayOrderID()
	if err != nil {
		return store.ReservedCommand{}, err
	}
	eventID, err := postgresStore.identity.EventID()
	if err != nil {
		return store.ReservedCommand{}, err
	}
	auditID, err := postgresStore.identity.EventID()
	if err != nil {
		return store.ReservedCommand{}, err
	}
	clientID, err := allocateClientOrderID(ctx, tx, request.AccountID, product)
	if err != nil {
		return store.ReservedCommand{}, err
	}
	clientIDString := clientID.String()

	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_commands(command_id, principal_id, idempotency_key, payload_hash, request_payload, gateway_order_id, portable_client_order_id, account_id, product, state)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, commandID, request.PrincipalID, request.IdempotencyKey, request.PayloadHash, request.Payload, gatewayOrderID, uint64(clientID), request.AccountID, product, domain.StatePrepared); err != nil {
		return store.ReservedCommand{}, redactErr("insert idempotency command", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO orders(gateway_order_id, command_id, account_id, instrument_id, state, resource_version)
		VALUES ($1, $2, $3, $4, $5, 0)
	`, gatewayOrderID, commandID, request.AccountID, request.InstrumentID, domain.StatePrepared); err != nil {
		return store.ReservedCommand{}, redactErr("insert order", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO venue_correlations(gateway_order_id, account_id, product, portable_client_order_id)
		VALUES ($1, $2, $3, $4)
	`, gatewayOrderID, request.AccountID, product, uint64(clientID)); err != nil {
		return store.ReservedCommand{}, redactErr("insert venue correlation", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_events(event_id, gateway_order_id, resource_version, state)
		VALUES ($1, $2, 0, $3)
	`, eventID, gatewayOrderID, domain.StatePrepared); err != nil {
		return store.ReservedCommand{}, redactErr("insert initial event", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records(audit_id, principal_id, action, resource_id, outcome, metadata)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, auditID, request.PrincipalID, store.AuditPlaceOrder, gatewayOrderID, store.AuditPrepared, redactedMetadata(request)); err != nil {
		return store.ReservedCommand{}, redactErr("insert audit record", err)
	}
	if err := postgresStore.commit(ctx, tx); err != nil {
		return store.ReservedCommand{}, err
	}
	return store.ReservedCommand{
		CommandID:       commandID,
		GatewayOrderID:  gatewayOrderID,
		ClientOrderID:   clientIDString,
		State:           domain.StatePrepared,
		ResourceVersion: 0,
		New:             true,
	}, nil
}

func (postgresStore *Store) MarkDispatching(ctx context.Context, request store.MarkDispatchingRequest) (store.StoredCommand, error) {
	if err := request.CommandID.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	if err := request.EventID.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	return postgresStore.transition(ctx, request.CommandID, "", domain.StateDispatching, request.EventID, 0, false, nil)
}

func (postgresStore *Store) BeginOrderHandoff(ctx context.Context, request store.BeginOrderHandoffRequest) (store.OrderHandoff, error) {
	if err := request.Validate(); err != nil {
		return store.OrderHandoff{}, err
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.OrderHandoff{}, redactErr("begin order handoff", err)
	}
	defer tx.Rollback(ctx)

	existing, err := loadOrderHandoffWithQuerier(ctx, tx, request.HandoffID)
	if err == nil {
		if existing.BeginOrderHandoffRequest != request {
			return store.OrderHandoff{}, store.ErrOperationConflict
		}
		if err := postgresStore.commit(ctx, tx); err != nil {
			return store.OrderHandoff{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.OrderHandoff{}, err
	}

	var activeID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT active_snapshot_id
		FROM gateway_account_runtime_heads
		WHERE account_id=$1
		FOR KEY SHARE
	`, request.AccountID).Scan(&activeID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.OrderHandoff{}, store.ErrPromotionFenceConflict
		}
		return store.OrderHandoff{}, redactErr("lock order handoff runtime head", err)
	}
	if activeID == nil {
		return store.OrderHandoff{}, store.ErrPromotionFenceConflict
	}
	active, err := loadSnapshotByID(ctx, tx, *activeID)
	if err != nil {
		return store.OrderHandoff{}, err
	}
	if active.Fence != request.Fence {
		return store.OrderHandoff{}, store.ErrPromotionFenceConflict
	}
	var routeActive bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM gateway_runtime_snapshot_routes
			WHERE snapshot_id=$1 AND route_id=$2 AND requested_state='active'
		)
	`, *activeID, request.RouteID).Scan(&routeActive); err != nil {
		return store.OrderHandoff{}, redactErr("check order handoff active route", err)
	}
	if !routeActive {
		return store.OrderHandoff{}, store.ErrPromotionFenceConflict
	}
	var referenceRetired bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM gateway_retired_secret_refs WHERE secret_ref=$1
		)
	`, active.SecretRef.RawForTrustedBoundary()).Scan(&referenceRetired); err != nil {
		return store.OrderHandoff{}, redactErr("check order handoff active reference", err)
	}
	if referenceRetired {
		return store.OrderHandoff{}, store.ErrRetiredSecretReference
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_order_handoffs(
			handoff_id, command_id, account_id, route_id, runtime_id, state
		) VALUES ($1,$2,$3,$4,$5::uuid,'started')
		ON CONFLICT (handoff_id) DO NOTHING
	`, request.HandoffID, request.CommandID, request.AccountID, request.RouteID,
		request.Fence.RuntimeID); err != nil {
		return store.OrderHandoff{}, redactErr("begin order handoff", err)
	}
	handoff, err := loadOrderHandoffWithQuerier(ctx, tx, request.HandoffID)
	if err != nil {
		return store.OrderHandoff{}, err
	}
	if handoff.BeginOrderHandoffRequest != request {
		return store.OrderHandoff{}, store.ErrOperationConflict
	}
	if err := postgresStore.commit(ctx, tx); err != nil {
		return store.OrderHandoff{}, err
	}
	return handoff, nil
}

func (postgresStore *Store) ResolveOrderHandoff(ctx context.Context, request store.ResolveOrderHandoffRequest) (store.OrderHandoff, error) {
	if err := request.Validate(); err != nil {
		return store.OrderHandoff{}, err
	}
	command, err := postgresStore.pool.Exec(ctx, `
		UPDATE gateway_order_handoffs
		SET state=$2, resolved_at=COALESCE(resolved_at, now())
		WHERE handoff_id=$1 AND (state='started' OR state=$2)
	`, request.HandoffID, request.State)
	if err != nil {
		return store.OrderHandoff{}, redactErr("resolve order handoff", err)
	}
	if command.RowsAffected() == 0 {
		return store.OrderHandoff{}, store.ErrNotFound
	}
	return postgresStore.loadOrderHandoff(ctx, request.HandoffID)
}

func (postgresStore *Store) loadOrderHandoff(ctx context.Context, handoffID domain.EventID) (store.OrderHandoff, error) {
	return loadOrderHandoffWithQuerier(ctx, postgresStore.pool, handoffID)
}

func loadOrderHandoffWithQuerier(ctx context.Context, querier snapshotQuerier, handoffID domain.EventID) (store.OrderHandoff, error) {
	var handoff store.OrderHandoff
	var resolvedAt *time.Time
	err := querier.QueryRow(ctx, `
		SELECT handoff_id::text, command_id::text, account_id, route_id,
		       runtime_id::text,
		       state, started_at, resolved_at
		FROM gateway_order_handoffs WHERE handoff_id=$1
	`, handoffID).Scan(&handoff.HandoffID, &handoff.CommandID, &handoff.AccountID, &handoff.RouteID,
		&handoff.Fence.RuntimeID,
		&handoff.State, &handoff.StartedAt, &resolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.OrderHandoff{}, store.ErrNotFound
	}
	if err != nil {
		return store.OrderHandoff{}, redactErr("load order handoff", err)
	}
	handoff.Fence.AccountID = handoff.AccountID
	handoff.ResolvedAt = resolvedAt
	return handoff, nil
}

func (postgresStore *Store) RecordVenueResult(ctx context.Context, request store.RecordVenueResultRequest) (store.StoredCommand, error) {
	if err := request.CommandID.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	if request.NextState == "" {
		return store.StoredCommand{}, fmt.Errorf("next state is required")
	}
	if err := request.EventID.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	if err := request.ResourceVersion.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	if request.NextState == domain.StateUncertain {
		if request.Reconciliation == nil {
			return store.StoredCommand{}, fmt.Errorf("uncertain transition requires reconciliation plan")
		}
		if err := request.Reconciliation.Validate(); err != nil {
			return store.StoredCommand{}, err
		}
	} else if request.Reconciliation != nil {
		return store.StoredCommand{}, fmt.Errorf("reconciliation plan is only valid for uncertain transition")
	}
	return postgresStore.transition(ctx, request.CommandID, request.VenueOrderID, request.NextState, request.EventID, request.ResourceVersion, true, request.Reconciliation)
}

func (postgresStore *Store) LoadOrder(ctx context.Context, request store.LoadOrderRequest) (store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	row := postgresStore.pool.QueryRow(ctx, `
		SELECT c.command_id::text, o.gateway_order_id::text, c.principal_id,
		       o.account_id, o.instrument_id, c.portable_client_order_id::text,
		       o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload
		FROM orders o
		JOIN idempotency_commands c ON c.command_id = o.command_id
		WHERE o.gateway_order_id = $1 AND c.principal_id = $2
	`, request.GatewayOrderID, request.PrincipalID)
	command, err := scanStoredCommandWithPrincipal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.StoredCommand{}, ErrNotFound
	}
	return command, err
}

func (postgresStore *Store) LoadOrderByVenueIdentity(ctx context.Context, request store.LoadOrderByVenueIdentityRequest) (store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return store.StoredCommand{}, err
	}
	const projection = `
		SELECT c.command_id::text, o.gateway_order_id::text, c.principal_id,
		       o.account_id, o.instrument_id, c.portable_client_order_id::text,
		       o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload
		FROM venue_correlations v
		JOIN orders o ON o.gateway_order_id = v.gateway_order_id
		JOIN idempotency_commands c ON c.command_id = o.command_id
		WHERE v.account_id = $1 AND v.product = $2 AND `
	var row pgx.Row
	if request.ClientOrderID != "" {
		row = postgresStore.pool.QueryRow(ctx, projection+`v.portable_client_order_id = $3::bigint`, request.AccountID, request.Product, request.ClientOrderID)
	} else {
		row = postgresStore.pool.QueryRow(ctx, projection+`v.venue_order_id = $3`, request.AccountID, request.Product, request.VenueOrderID)
	}
	command, err := scanStoredCommandWithPrincipal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.StoredCommand{}, ErrNotFound
	}
	return command, err
}

func (postgresStore *Store) ListOrders(ctx context.Context, request store.ListOrdersRequest) ([]store.StoredCommand, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	rows, err := postgresStore.pool.Query(ctx, `
		SELECT c.command_id::text, o.gateway_order_id::text, c.principal_id,
		       o.account_id, o.instrument_id, c.portable_client_order_id::text,
		       o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload
		FROM orders o
		JOIN idempotency_commands c ON c.command_id = o.command_id
		WHERE c.principal_id = $1
		  AND ($2 = '' OR o.account_id = $2)
		  AND ($3 = '' OR o.instrument_id = $3)
		  AND (NOT $4 OR o.state NOT IN ('filled', 'canceled', 'rejected'))
		  AND ($5 = '' OR o.gateway_order_id::text > $5)
		ORDER BY o.gateway_order_id
		LIMIT $6
	`, request.PrincipalID, request.AccountID, request.InstrumentID, request.OpenOnly, request.Cursor, request.Limit)
	if err != nil {
		return nil, redactErr("list orders", err)
	}
	defer rows.Close()
	commands := make([]store.StoredCommand, 0)
	for rows.Next() {
		command, scanErr := scanStoredCommandWithPrincipal(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("list orders", err)
	}
	return commands, nil
}

func (postgresStore *Store) LoadRecoverableCommands(ctx context.Context, request store.LoadRecoverableCommandsRequest) ([]store.StoredCommand, error) {
	if err := request.AccountID.Validate(); err != nil {
		return nil, err
	}
	limit := request.Limit
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := postgresStore.pool.Query(ctx, `
		SELECT c.command_id::text, o.gateway_order_id::text, o.account_id, o.instrument_id,
		       c.portable_client_order_id::text, o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload
		FROM orders o
		JOIN idempotency_commands c ON c.command_id = o.command_id
		WHERE o.account_id = $1 AND o.state NOT IN ('filled', 'canceled', 'rejected')
		ORDER BY o.created_at, o.gateway_order_id
		LIMIT $2
	`, request.AccountID, limit)
	if err != nil {
		return nil, redactErr("load recoverable commands", err)
	}
	defer rows.Close()

	var commands []store.StoredCommand
	for rows.Next() {
		command, err := scanStoredCommand(rows)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("scan recoverable commands", err)
	}
	return commands, nil
}

func (postgresStore *Store) transition(ctx context.Context, commandID domain.CommandID, venueOrderID string, nextState domain.OrderState, eventID domain.EventID, expectedVersion domain.ResourceVersion, cas bool, reconciliation *store.ReconciliationPlan) (store.StoredCommand, error) {
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.StoredCommand{}, redactErr("begin command transition", err)
	}
	defer tx.Rollback(ctx)

	current, err := loadCommandForUpdate(ctx, tx, commandID)
	if err != nil {
		return store.StoredCommand{}, err
	}
	if cas && current.ResourceVersion != expectedVersion {
		return store.StoredCommand{}, ErrConflict
	}
	if !cas && current.State != domain.StatePrepared {
		return store.StoredCommand{}, ErrConflict
	}
	if err := domain.ValidateOrderTransition(current.State, nextState); err != nil {
		return store.StoredCommand{}, err
	}
	nextVersion := current.ResourceVersion + 1
	if _, err := tx.Exec(ctx, `
		UPDATE orders
		SET state = $1, resource_version = $2, venue_order_id = COALESCE(NULLIF($3, ''), venue_order_id), updated_at = now()
		WHERE command_id = $4
	`, nextState, nextVersion, venueOrderID, commandID); err != nil {
		return store.StoredCommand{}, redactErr("update order transition", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE idempotency_commands SET state = $1 WHERE command_id = $2`, nextState, commandID); err != nil {
		return store.StoredCommand{}, redactErr("update command state", err)
	}
	if venueOrderID != "" {
		if _, err := tx.Exec(ctx, `UPDATE venue_correlations SET venue_order_id = $1 WHERE gateway_order_id = $2`, venueOrderID, current.GatewayOrderID); err != nil {
			return store.StoredCommand{}, redactErr("update venue correlation", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO order_events(event_id, gateway_order_id, resource_version, state)
		VALUES ($1, $2, $3, $4)
	`, eventID, current.GatewayOrderID, nextVersion, nextState); err != nil {
		return store.StoredCommand{}, redactErr("insert transition event", err)
	}
	handoffState := store.OrderHandoffState("")
	switch nextState {
	case domain.StateAccepted:
		handoffState = store.OrderHandoffAccepted
	case domain.StateRejected:
		handoffState = store.OrderHandoffRejected
	case domain.StateUncertain:
		handoffState = store.OrderHandoffUncertain
	}
	if handoffState != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE gateway_order_handoffs SET state=$2, resolved_at=now()
			WHERE command_id=$1 AND state='started'
		`, commandID, handoffState); err != nil {
			return store.StoredCommand{}, redactErr("resolve durable order handoff", err)
		}
	}
	if nextState == domain.StateUncertain {
		if _, err := tx.Exec(ctx, `
			INSERT INTO reconciliation_tasks(task_id, gateway_order_id, reason, evidence_summary, next_attempt_at, attempts)
			VALUES ($1, $2, $3, jsonb_build_object('reason', $3::text), $4, 0)
			ON CONFLICT (gateway_order_id) DO UPDATE
			SET reason = EXCLUDED.reason,
			    evidence_summary = EXCLUDED.evidence_summary,
			    next_attempt_at = EXCLUDED.next_attempt_at
		`, reconciliation.TaskID, current.GatewayOrderID, reconciliation.Reason, reconciliation.NextAttemptAt); err != nil {
			return store.StoredCommand{}, redactErr("schedule reconciliation task", err)
		}
	} else {
		if _, err := tx.Exec(ctx, `DELETE FROM reconciliation_tasks WHERE gateway_order_id = $1`, current.GatewayOrderID); err != nil {
			return store.StoredCommand{}, redactErr("clear reconciliation task", err)
		}
	}
	if err := postgresStore.commit(ctx, tx); err != nil {
		return store.StoredCommand{}, err
	}

	current.State = nextState
	current.ResourceVersion = nextVersion
	current.EventID = eventID
	if venueOrderID != "" {
		current.VenueOrderID = venueOrderID
	}
	return current, nil
}

func (postgresStore *Store) RescheduleReconciliation(ctx context.Context, request store.RescheduleReconciliationRequest) (store.ReconciliationTask, error) {
	if err := request.Validate(); err != nil {
		return store.ReconciliationTask{}, err
	}
	row := postgresStore.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE reconciliation_tasks
			SET reason = $2,
			    evidence_summary = jsonb_build_object('reason', $2::text),
			    next_attempt_at = $3
			WHERE gateway_order_id = $1
			RETURNING task_id::text, gateway_order_id, reason, next_attempt_at, attempts
		)
		SELECT u.task_id, c.command_id::text, o.gateway_order_id::text, o.account_id, o.instrument_id,
		       c.portable_client_order_id::text, o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload, u.reason, u.next_attempt_at, u.attempts
		FROM updated u
		JOIN orders o ON o.gateway_order_id = u.gateway_order_id
		JOIN idempotency_commands c ON c.command_id = o.command_id
	`, request.GatewayOrderID, request.Plan.Reason, request.Plan.NextAttemptAt)
	task, err := scanReconciliationTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ReconciliationTask{}, ErrNotFound
	}
	return task, err
}

func (postgresStore *Store) ClaimDueReconciliationTasks(ctx context.Context, request store.ClaimDueReconciliationTasksRequest) ([]store.ReconciliationTask, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, redactErr("begin reconciliation claim", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		WITH due AS (
			SELECT r.task_id
			FROM reconciliation_tasks r
			JOIN orders o ON o.gateway_order_id = r.gateway_order_id
			WHERE o.account_id = $1
			  AND r.next_attempt_at <= $2
			  AND r.attempts < $3
			ORDER BY r.next_attempt_at, r.task_id
			FOR UPDATE OF r SKIP LOCKED
			LIMIT $4
		), claimed AS (
			UPDATE reconciliation_tasks r
			SET attempts = r.attempts + 1,
			    next_attempt_at = $5
			FROM due
			WHERE r.task_id = due.task_id
			RETURNING r.task_id::text, r.gateway_order_id, r.reason, r.next_attempt_at, r.attempts
		)
		SELECT claimed.task_id, c.command_id::text, o.gateway_order_id::text, o.account_id, o.instrument_id,
		       c.portable_client_order_id::text, o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload, claimed.reason, claimed.next_attempt_at, claimed.attempts
		FROM claimed
		JOIN orders o ON o.gateway_order_id = claimed.gateway_order_id
		JOIN idempotency_commands c ON c.command_id = o.command_id
		ORDER BY claimed.next_attempt_at, claimed.task_id
	`, request.AccountID, request.Now, request.MaxAttempts, request.Limit, request.LeaseUntil)
	if err != nil {
		return nil, redactErr("claim reconciliation tasks", err)
	}
	defer rows.Close()
	tasks := make([]store.ReconciliationTask, 0)
	for rows.Next() {
		task, scanErr := scanReconciliationTask(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("scan reconciliation tasks", err)
	}
	if err := postgresStore.commit(ctx, tx); err != nil {
		return nil, err
	}
	return tasks, nil
}

func scanReconciliationTask(row rowScanner) (store.ReconciliationTask, error) {
	var task store.ReconciliationTask
	if err := row.Scan(
		&task.TaskID,
		&task.Command.CommandID, &task.Command.GatewayOrderID, &task.Command.AccountID, &task.Command.InstrumentID,
		&task.Command.ClientOrderID, &task.Command.State, &task.Command.ResourceVersion, &task.Command.VenueOrderID,
		&task.Command.EventID, &task.Command.Payload,
		&task.Reason, &task.NextAttemptAt, &task.Attempts,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.ReconciliationTask{}, pgx.ErrNoRows
		}
		return store.ReconciliationTask{}, redactErr("scan reconciliation task", err)
	}
	return task, nil
}

func (postgresStore *Store) commit(ctx context.Context, tx pgx.Tx) error {
	if postgresStore.beforeCommit != nil {
		if err := postgresStore.beforeCommit(ctx); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit postgres transaction", err)
	}
	return nil
}

func loadReservation(ctx context.Context, tx pgx.Tx, principalID domain.PrincipalID, key domain.IdempotencyKey) (reservationRecord, bool, error) {
	var command store.StoredCommand
	var payloadHash string
	var product string
	err := tx.QueryRow(ctx, `
		SELECT c.command_id::text, o.gateway_order_id::text, o.account_id, o.instrument_id,
		       c.portable_client_order_id::text, o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.payload_hash, c.product
		FROM idempotency_commands c
		JOIN orders o ON o.command_id = c.command_id
		WHERE c.principal_id = $1 AND c.idempotency_key = $2
		FOR UPDATE OF c
	`, principalID, key).Scan(
		&command.CommandID, &command.GatewayOrderID, &command.AccountID, &command.InstrumentID,
		&command.ClientOrderID, &command.State, &command.ResourceVersion, &command.VenueOrderID,
		&command.EventID, &payloadHash, &product,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return reservationRecord{}, false, nil
	}
	if err != nil {
		return reservationRecord{}, false, redactErr("load idempotency reservation", err)
	}
	return reservationRecord{
		ReservedCommand: store.ReservedCommand{
			CommandID:       command.CommandID,
			GatewayOrderID:  command.GatewayOrderID,
			ClientOrderID:   command.ClientOrderID,
			State:           command.State,
			ResourceVersion: command.ResourceVersion,
			VenueOrderID:    command.VenueOrderID,
		},
		accountID:    command.AccountID,
		instrumentID: command.InstrumentID,
		product:      product,
		payloadHash:  payloadHash,
	}, true, nil
}

type reservationRecord struct {
	store.ReservedCommand
	accountID    domain.AccountID
	instrumentID domain.InstrumentID
	product      string
	payloadHash  string
}

func reservationConflict(existing reservationRecord, request store.ReserveCommandRequest, product string) store.IdempotencyConflict {
	switch {
	case existing.accountID != request.AccountID:
		return store.ConflictAccount
	case existing.instrumentID != request.InstrumentID || existing.product != product:
		return store.ConflictInstrument
	case existing.payloadHash != request.PayloadHash:
		return store.ConflictPayload
	default:
		return store.ConflictNone
	}
}

func loadCommandForUpdate(ctx context.Context, tx pgx.Tx, commandID domain.CommandID) (store.StoredCommand, error) {
	row := tx.QueryRow(ctx, `
		SELECT c.command_id::text, o.gateway_order_id::text, c.principal_id, o.account_id, o.instrument_id,
		       c.portable_client_order_id::text, o.state, o.resource_version, COALESCE(o.venue_order_id, ''),
		       COALESCE((SELECT e.event_id::text FROM order_events e WHERE e.gateway_order_id = o.gateway_order_id ORDER BY e.resource_version DESC LIMIT 1), ''),
		       c.request_payload
		FROM orders o
		JOIN idempotency_commands c ON c.command_id = o.command_id
		WHERE c.command_id = $1
		FOR UPDATE OF o
	`, commandID)
	command, err := scanStoredCommandWithPrincipal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.StoredCommand{}, ErrNotFound
	}
	return command, err
}

type rowScanner interface {
	Scan(...any) error
}

func scanStoredCommand(row rowScanner) (store.StoredCommand, error) {
	var command store.StoredCommand
	if err := row.Scan(
		&command.CommandID, &command.GatewayOrderID, &command.AccountID, &command.InstrumentID,
		&command.ClientOrderID, &command.State, &command.ResourceVersion, &command.VenueOrderID,
		&command.EventID, &command.Payload,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.StoredCommand{}, pgx.ErrNoRows
		}
		return store.StoredCommand{}, redactErr("scan stored command", err)
	}
	return command, nil
}

func scanStoredCommandWithPrincipal(row rowScanner) (store.StoredCommand, error) {
	var command store.StoredCommand
	if err := row.Scan(
		&command.CommandID, &command.GatewayOrderID, &command.PrincipalID,
		&command.AccountID, &command.InstrumentID, &command.ClientOrderID,
		&command.State, &command.ResourceVersion, &command.VenueOrderID,
		&command.EventID, &command.Payload,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.StoredCommand{}, pgx.ErrNoRows
		}
		return store.StoredCommand{}, redactErr("scan stored order", err)
	}
	return command, nil
}

func allocateClientOrderID(ctx context.Context, tx pgx.Tx, accountID domain.AccountID, product string) (domain.ClientOrderID, error) {
	var value uint64
	err := tx.QueryRow(ctx, `
		INSERT INTO next_client_order_ids(account_id, product, next_value)
		VALUES ($1, $2, 2)
		ON CONFLICT (account_id, product)
		DO UPDATE SET next_value = next_client_order_ids.next_value + 1
		RETURNING next_value - 1
	`, accountID, product).Scan(&value)
	if err != nil {
		return 0, redactErr("allocate client order id", err)
	}
	if value == 0 || value > (1<<48-1) {
		return 0, fmt.Errorf("%w: client order id exhausted", ErrConflict)
	}
	return domain.ClientOrderID(value), nil
}

func redactedMetadata(request store.ReserveCommandRequest) []byte {
	data, _ := json.Marshal(map[string]string{
		"correlation_id": string(request.CorrelationID),
		"account_id":     string(request.AccountID),
		"instrument_id":  string(request.InstrumentID),
	})
	return data
}

func productFromInstrument(instrumentID domain.InstrumentID) (string, error) {
	if err := instrumentID.Validate(); err != nil {
		return "", err
	}
	parts := strings.Split(string(instrumentID), ":")
	if len(parts) < 4 {
		return "", fmt.Errorf("instrument id product is missing")
	}
	return parts[2], nil
}
