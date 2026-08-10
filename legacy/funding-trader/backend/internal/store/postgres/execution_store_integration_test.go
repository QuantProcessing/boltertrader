package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestExecutionStorePreviewScopeAndExpiry(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	preview := testPreview(scope, "preview-1", time.Now().UTC().Add(-time.Minute))

	if err := store.SavePreview(ctx, preview, "hash-preview"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetPreview(ctx, scope, preview.PreviewID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.Expired || got.RequestHash != "hash-preview" || got.Preview.PreviewID != preview.PreviewID {
		t.Fatalf("preview=%+v", got)
	}
	expired, err := store.GetPreview(ctx, scope, preview.PreviewID, preview.ExpiresAt.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !expired.Expired {
		t.Fatal("expired preview should still be readable and marked expired")
	}
	if _, err := store.GetPreview(ctx, readmodel.Scope{PrincipalID: "principal-b", Mode: "test"}, preview.PreviewID, time.Now().UTC()); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("cross-principal err=%v", err)
	}
}

func TestExecutionStorePreviewSaveIsImmutableWithinScope(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	preview := testPreview(scope, "preview-immutable", time.Now().UTC())

	if err := store.SavePreview(ctx, preview, "hash-preview"); err != nil {
		t.Fatal(err)
	}
	if err := store.SavePreview(ctx, preview, "hash-preview"); err != nil {
		t.Fatalf("same hash and payload should replay as no-op: %v", err)
	}
	changedPayload := preview
	changedPayload.SingleLegNotional = decimal.RequireFromString("501")
	if err := store.SavePreview(ctx, changedPayload, "hash-preview"); !errors.Is(err, ErrPreviewConflict) {
		t.Fatalf("changed payload err=%v", err)
	}
	changedHash := preview
	if err := store.SavePreview(ctx, changedHash, "other-hash"); !errors.Is(err, ErrPreviewConflict) {
		t.Fatalf("changed hash err=%v", err)
	}
	got, err := store.GetPreview(ctx, scope, preview.PreviewID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestHash != "hash-preview" || !got.Preview.SingleLegNotional.Equal(preview.SingleLegNotional) {
		t.Fatalf("preview was overwritten: %+v", got)
	}
}

func TestExecutionStoreIdempotencyReplayConflictConcurrencyAndResponse(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-1")

	first, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-1", "hash-1", exec.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if first.Replayed || first.Execution.ID != exec.ID {
		t.Fatalf("first=%+v", first)
	}
	replay, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-1", "hash-1", testExecution(t, scope, "exec-2").Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.Execution.ID != exec.ID {
		t.Fatalf("replay=%+v", replay)
	}
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-1", "different", exec.Snapshot()); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict err=%v", err)
	}
	if err := store.CompleteIdempotency(ctx, scope, "POST", "/api/executions", "idem-1", 202, json.RawMessage(`{"executionId":"exec-1","accepted":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteIdempotency(ctx, scope, "POST", "/api/executions", "idem-1", 500, json.RawMessage(`{"overwritten":true}`)); err != nil {
		t.Fatal(err)
	}
	completed, err := store.GetIdempotency(ctx, scope, "POST", "/api/executions", "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if completed.ResponseCode != 202 || !jsonObjectEquals(completed.ResponseBody, `{"executionId":"exec-1","accepted":true}`) {
		t.Fatalf("completed=%+v body=%s", completed, completed.ResponseBody)
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, reserveErr := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-concurrent", "hash-c", testExecution(t, scope, "exec-concurrent").Snapshot())
			errs <- reserveErr
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := store.pool.QueryRow(ctx, "select count(*) from executions where id='exec-concurrent'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("concurrent executions=%d", count)
	}
}

func TestExecutionStoreCompleteLinkedIdempotencyCompletesOnlyInProgressRecords(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-linked-idempotency")

	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-linked-open", "hash-linked", exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.pool.Exec(ctx, `
		insert into idempotency_records(
			id, principal_id, mode, method, path, key, request_hash, execution_id, state, response_code, response_body, created_at, updated_at
		) values(
			'id-completed-existing', $1, $2, 'POST', '/api/executions/other', 'idem-completed', 'hash-completed', $3,
			'completed', 299, '{"kept":true}'::jsonb, now(), now()
		)
	`, scope.PrincipalID, scope.Mode, exec.ID); err != nil {
		t.Fatal(err)
	}

	body := json.RawMessage(`{"executionId":"exec-linked-idempotency","accepted":true}`)
	if err := store.CompleteLinkedIdempotency(ctx, scope, exec.ID, 202, body); err != nil {
		t.Fatal(err)
	}

	repaired, err := store.GetIdempotency(ctx, scope, "POST", "/api/executions", "idem-linked-open")
	if err != nil {
		t.Fatal(err)
	}
	if repaired.State != "completed" || repaired.ResponseCode != 202 || !jsonObjectEquals(repaired.ResponseBody, string(body)) {
		t.Fatalf("repaired=%+v body=%s", repaired, repaired.ResponseBody)
	}
	existing, err := store.GetIdempotency(ctx, scope, "POST", "/api/executions/other", "idem-completed")
	if err != nil {
		t.Fatal(err)
	}
	if existing.ResponseCode != 299 || !jsonObjectEquals(existing.ResponseBody, `{"kept":true}`) {
		t.Fatalf("completed record overwritten: %+v body=%s", existing, existing.ResponseBody)
	}
}

func TestExecutionStoreListsTerminalExecutionsWithIncompleteIdempotency(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	otherScope := readmodel.Scope{PrincipalID: "principal-b", Mode: "test"}
	terminal := testExecution(t, scope, "exec-terminal-incomplete")
	quantity := terminal.PlannedLegs[0].Quantity
	for _, event := range []execution.Event{
		execution.BeginSubmit(time.Now().UTC()),
		execution.LegAccepted("exec-terminal-incomplete:long", execution.LegRoleLong, "order-long", time.Now().UTC()),
		execution.LegFilledDetail("fill-long", "exec-terminal-incomplete:long", "gateway-fill-long", quantity, decimal.RequireFromString("100"), execution.Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"}, time.Now().UTC()),
		execution.LegAccepted("exec-terminal-incomplete:short", execution.LegRoleShort, "order-short", time.Now().UTC()),
		execution.LegFilledDetail("fill-short", "exec-terminal-incomplete:short", "gateway-fill-short", quantity, decimal.RequireFromString("100"), execution.Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"}, time.Now().UTC()),
	} {
		if err := terminal.Apply(event); err != nil {
			t.Fatalf("apply terminal event: %v", err)
		}
	}
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-terminal", "hash-terminal", terminal.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: terminal.Snapshot(), ExpectedVersion: 1,
		Kind: "terminal", RequestID: "req-terminal", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	completed := testExecution(t, scope, "exec-terminal-completed")
	for _, event := range []execution.Event{
		execution.BeginSubmit(time.Now().UTC()),
		execution.Event{Type: execution.EventLegRejected, GatewayID: "exec-terminal-completed:long", Role: execution.LegRoleLong, SafeError: execution.ErrorInfo{Code: "REJECTED", SafeMessage: "rejected"}, At: time.Now().UTC()},
	} {
		if err := completed.Apply(event); err != nil {
			t.Fatalf("apply completed terminal event: %v", err)
		}
	}
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-completed-terminal", "hash-completed-terminal", completed.Snapshot()); err != nil {
		t.Fatal(err)
	}
	body := json.RawMessage(`{"executionId":"exec-terminal-completed"}`)
	if err := store.CompleteLinkedIdempotency(ctx, scope, completed.ID, 202, body); err != nil {
		t.Fatal(err)
	}
	nonTerminal := testExecution(t, scope, "exec-nonterminal-incomplete")
	if err := nonTerminal.Apply(execution.BeginSubmit(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-nonterminal", "hash-nonterminal", nonTerminal.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: nonTerminal.Snapshot(), ExpectedVersion: 1,
		Kind: "begin_submit", RequestID: "req-nonterminal", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	crossScope := testExecution(t, otherScope, "exec-cross-terminal")
	for _, event := range []execution.Event{
		execution.BeginSubmit(time.Now().UTC()),
		execution.Event{Type: execution.EventLegRejected, GatewayID: "exec-cross-terminal:long", Role: execution.LegRoleLong, SafeError: execution.ErrorInfo{Code: "REJECTED", SafeMessage: "rejected"}, At: time.Now().UTC()},
	} {
		if err := crossScope.Apply(event); err != nil {
			t.Fatalf("apply cross-scope terminal event: %v", err)
		}
	}
	if _, err := store.ReserveExecution(ctx, otherScope, "POST", "/api/executions", "idem-cross-terminal", "hash-cross-terminal", crossScope.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: otherScope, Execution: crossScope.Snapshot(), ExpectedVersion: 1,
		Kind: "cross_terminal", RequestID: "req-cross", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListTerminalWithIncompleteIdempotency(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Execution.ID != terminal.ID || listed[0].Execution.State != execution.StateHedged {
		t.Fatalf("listed terminal incomplete executions = %+v", listed)
	}
}

func TestExecutionStorePersistsLineageColumnsOnReserveAndTransition(t *testing.T) {
	ctx, store, pool, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testCloseExecutionWithLineage(t, scope, "exec-close-lineage", "exec-open-lineage", "lineage-open")

	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-lineage", "hash-lineage", exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	assertExecutionLineageColumns(t, ctx, pool, exec.ID, "exec-open-lineage", "lineage-open")

	if _, err := pool.Exec(ctx, `
		update executions
		set parent_execution_id=null, lineage_id=null
		where id=$1
	`, exec.ID); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "begin_submit", RequestID: "req-lineage", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	assertExecutionLineageColumns(t, ctx, pool, exec.ID, "exec-open-lineage", "lineage-open")
}

func TestExecutionStoreRejectsImmutableLegAndFillReplayConflicts(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-conflicts")
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-conflicts", "hash-conflicts", exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventLegAccepted, Role: execution.LegRoleLong, GatewayID: "gw-1", OrderID: "order-1", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	fill := execution.Event{
		Type: execution.EventLegPartial, GatewayID: "gw-1", FillID: "fill-conflict", GatewayFillID: "gfill-conflict",
		Quantity: decimal.RequireFromString("2"), Price: decimal.RequireFromString("100"),
		Fee: execution.Money{Amount: decimal.RequireFromString("0.1"), Currency: "USDT"}, At: time.Now().UTC(),
	}
	if err := exec.Apply(fill); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "leg_partial", RequestID: "req-conflict-1", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}

	legConflict := exec.Snapshot()
	legConflict.Legs[0].RequestedQuantity = decimal.RequireFromString("6")
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: legConflict, ExpectedVersion: 2,
		Kind: "leg_conflict", RequestID: "req-leg-conflict", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionLegConflict) {
		t.Fatalf("leg conflict err=%v", err)
	}

	fillConflict := exec.Snapshot()
	fillConflict.Fills[0].Price = decimal.RequireFromString("101")
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: fillConflict, ExpectedVersion: 2,
		Kind: "fill_conflict", RequestID: "req-fill-conflict", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionFillConflict) {
		t.Fatalf("fill conflict err=%v", err)
	}
}

func TestExecutionStoreScopesGatewayFillReplayByExecution(t *testing.T) {
	ctx, store, _, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}

	first := testExecutionWithPartialFill(t, ctx, store, scope, "exec-fill-a", "shared-gateway-fill", "100")
	second := testExecutionWithPartialFill(t, ctx, store, scope, "exec-fill-b", "shared-gateway-fill", "101")

	if first.ID == second.ID || first.Fills[0].GatewayFillID != second.Fills[0].GatewayFillID {
		t.Fatalf("test setup failed first=%+v second=%+v", first.Fills, second.Fills)
	}
	conflict := first.Snapshot()
	conflict.Fills[0].Price = decimal.RequireFromString("102")
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: conflict, ExpectedVersion: 2,
		Kind: "fill_conflict", RequestID: "req-fill-conflict", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionFillConflict) {
		t.Fatalf("same execution changed fill err=%v", err)
	}
}

func TestExecutionStoreRetainsBoundedExecutionOutboxEvents(t *testing.T) {
	ctx, store, pool, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-retention")
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-retention", "hash-retention", exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= eventRetentionRows+5; i++ {
		_, err := store.PersistTransition(ctx, PersistTransitionInput{
			Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: i,
			Kind: "retention", RequestID: "req-retention", Payload: json.RawMessage(`{"safe":true}`),
			OccurredAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatalf("transition %d: %v", i, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, "select count(*) from read_model_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != eventRetentionRows {
		t.Fatalf("read_model_events retained %d rows, want exactly %d", count, eventRetentionRows)
	}
}

func TestExecutionStorePersistsTransitionsDedupeRecoveryAuditAndReadModel(t *testing.T) {
	ctx, store, pool, cleanup := newExecutionStoreTest(t)
	defer cleanup()
	scope := readmodel.Scope{PrincipalID: "principal-a", Mode: "test"}
	exec := testExecution(t, scope, "exec-transition")
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-transition", "hash-transition", exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	events, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "begin_submit", RequestID: "req-1", Payload: json.RawMessage(`{"safe":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if events.Execution.Kind != readmodel.ExecutionUpsert || events.Audit.Kind != readmodel.AuditAppend {
		t.Fatalf("events=%+v", events)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "stale", RequestID: "req-stale", Payload: json.RawMessage(`{"safe":true}`),
	}); !errors.Is(err, ErrExecutionVersionConflict) {
		t.Fatalf("stale err=%v", err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventLegAccepted, Role: execution.LegRoleLong, GatewayID: "gw-1", OrderID: "order-1", At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	fill := execution.Event{
		Type: execution.EventLegPartial, GatewayID: "gw-1", FillID: "fill-1", GatewayFillID: "gfill-1",
		Quantity: decimal.RequireFromString("2"), Price: decimal.RequireFromString("100"),
		Fee: execution.Money{Amount: decimal.RequireFromString("0.1"), Currency: "USDT"}, At: time.Now().UTC(),
	}
	if err := exec.Apply(fill); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(fill); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 2,
		Kind: "leg_partial", RequestID: "req-2", Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := store.GetExecution(ctx, scope, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := execution.RestoreExecution(reloaded.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if restored.State != execution.StateLeg1Working || len(restored.Fills) != 1 {
		t.Fatalf("restored state=%s fills=%d", restored.State, len(restored.Fills))
	}
	open, err := store.ListNonTerminal(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Execution.ID != exec.ID {
		t.Fatalf("nonterminal=%+v", open)
	}
	allExecutions, err := store.ListExecutions(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(allExecutions) != 1 || allExecutions[0].ID != exec.ID {
		t.Fatalf("all executions=%+v", allExecutions)
	}
	crossExecutions, err := store.ListExecutions(ctx, readmodel.Scope{
		PrincipalID: "principal-other", Mode: scope.Mode,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(crossExecutions) != 0 {
		t.Fatalf("cross-principal executions=%+v", crossExecutions)
	}
	var auditCount, outboxCount int
	if err := pool.QueryRow(ctx, "select count(*) from audit_events where principal_id=$1 and mode=$2 and execution_id=$3 and request_id in ('req-1','req-2')", scope.PrincipalID, scope.Mode, exec.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from read_model_events where principal_id=$1 and mode=$2 and entity_id=$3 and kind in ('execution.upsert','audit.append')", scope.PrincipalID, scope.Mode, exec.ID).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 || outboxCount < 4 {
		t.Fatalf("audit=%d outbox=%d", auditCount, outboxCount)
	}
	var longVenue, longVenueEnvironment string
	if err := pool.QueryRow(ctx, `
		select
		  max(venue) filter (where role='long'),
		  max(venue_environment) filter (where role='long')
		from execution_legs
		where execution_id=$1
	`, exec.ID).Scan(&longVenue, &longVenueEnvironment); err != nil {
		t.Fatal(err)
	}
	if longVenue != "okx" || longVenueEnvironment != "demo" {
		t.Fatalf("leg provenance long=%s/%s", longVenue, longVenueEnvironment)
	}
	var transitionPayload, auditPayload, fillPayload json.RawMessage
	if err := pool.QueryRow(ctx, `
		select payload
		from execution_transitions
		where principal_id=$1 and mode=$2 and execution_id=$3 and request_id='req-2'
	`, scope.PrincipalID, scope.Mode, exec.ID).Scan(&transitionPayload); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select payload
		from audit_events
		where principal_id=$1 and mode=$2 and execution_id=$3 and request_id='req-2'
	`, scope.PrincipalID, scope.Mode, exec.ID).Scan(&auditPayload); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		select payload
		from execution_fills
		where execution_id=$1 and gateway_fill_id='gfill-1'
	`, exec.ID).Scan(&fillPayload); err != nil {
		t.Fatal(err)
	}
	assertExecutionRoutePayload(t, transitionPayload, "test", "legs")
	assertExecutionRoutePayload(t, auditPayload, "test", "legs")
	assertExecutionRoutePayload(t, fillPayload, "", "route")
	var contractVersion string
	if err := pool.QueryRow(ctx, `
		select distinct contract_version
		from read_model_events
		where principal_id=$1 and mode=$2 and entity_id=$3 and kind='execution.upsert'
	`, scope.PrincipalID, scope.Mode, exec.ID).Scan(&contractVersion); err != nil {
		t.Fatal(err)
	}
	if contractVersion != readmodel.ContractVersion || events.Execution.ContractVersion != readmodel.ContractVersion ||
		events.Audit.ContractVersion != readmodel.ContractVersion {
		t.Fatalf("contract db=%q execution=%q audit=%q", contractVersion, events.Execution.ContractVersion, events.Audit.ContractVersion)
	}

	reader, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x44}, 32))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := reader.Execution(ctx, scope, exec.ID)
	if err != nil {
		t.Fatal(err)
	}
	var executionPayload map[string]any
	if err := json.Unmarshal(payload, &executionPayload); err != nil {
		t.Fatalf("execution payload is not json: %v", err)
	}
	if executionPayload["id"] != "exec-transition" {
		t.Fatalf("execution payload=%s", payload)
	}
	cross, err := reader.ListExecutions(ctx, readmodel.Scope{PrincipalID: "principal-a", Mode: "live"}, readmodel.ExecutionQuery{PageQuery: readmodel.PageQuery{Limit: 20}})
	if err != nil {
		t.Fatal(err)
	}
	if len(cross.Items) != 0 {
		t.Fatalf("cross env executions=%d", len(cross.Items))
	}
	snapshot, err := reader.Snapshot(ctx, scope, []readmodel.EventKind{readmodel.ExecutionUpsert})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Executions) != 1 {
		t.Fatalf("snapshot executions=%d", len(snapshot.Executions))
	}
}

func assertExecutionRoutePayload(t *testing.T, payload json.RawMessage, wantMode, routeField string) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("route payload is not json: %v payload=%s", err, payload)
	}
	if wantMode != "" && object["mode"] != wantMode {
		t.Fatalf("payload mode=%v want %s payload=%s", object["mode"], wantMode, payload)
	}
	switch routeField {
	case "legs":
		legs, ok := object["legs"].([]any)
		if !ok || len(legs) != 2 {
			t.Fatalf("payload legs=%v payload=%s", object["legs"], payload)
		}
		long, _ := legs[0].(map[string]any)
		short, _ := legs[1].(map[string]any)
		if long["role"] != "long" || long["accountId"] != "long" ||
			long["venue"] != "okx" || long["instrumentId"] != "btc" ||
			long["venueEnvironment"] != "demo" ||
			short["role"] != "short" || short["accountId"] != "short" ||
			short["venue"] != "bybit" || short["instrumentId"] != "btc" ||
			short["venueEnvironment"] != "testnet" {
			t.Fatalf("payload legs not exact/order-preserving: %v payload=%s", legs, payload)
		}
	case "route":
		route, ok := object["route"].(map[string]any)
		if !ok {
			t.Fatalf("payload route=%v payload=%s", object["route"], payload)
		}
		if route["role"] != "long" || route["accountId"] != "long" ||
			route["venue"] != "okx" || route["instrumentId"] != "btc" ||
			route["venueEnvironment"] != "demo" {
			t.Fatalf("payload route not exact: %v payload=%s", route, payload)
		}
	default:
		t.Fatalf("unknown route field %q", routeField)
	}
}

func newExecutionStoreTest(t *testing.T) (context.Context, *ExecutionStore, *pgxpool.Pool, func()) {
	t.Helper()
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control cascade"); err != nil {
		t.Fatal(err)
	}
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	return ctx, NewExecutionStore(pool, "execution-test-boot"), pool, pool.Close
}

func testPreview(scope readmodel.Scope, id string, at time.Time) execution.PreviewResult {
	return execution.PreviewResult{
		PreviewID: id, SnapshotID: "snapshot-" + id,
		PrincipalID: scope.PrincipalID, Mode: scope.Mode,
		Operation: execution.OperationOpen, OpportunityID: "opp-1",
		State: execution.StatePreviewed, PreviewedAt: at.UTC(), ExpiresAt: at.UTC().Add(5 * time.Minute),
		SingleLegNotional: decimal.RequireFromString("500"),
		GrossExposure:     decimal.RequireFromString("1000"),
		GrossProfit7d:     decimal.RequireFromString("7"),
		FeesUSDT:          decimal.RequireFromString("1"),
		SlippageUSDT:      decimal.RequireFromString("0.5"),
		NetProfit7d:       decimal.RequireFromString("5.5"),
		MaxSlippagePct:    decimal.RequireFromString("0.01"),
		CanSubmit:         true,
		PlannedLegs: []execution.PlannedLeg{
			{LegIdentity: execution.LegIdentity{AccountID: "long", Venue: "okx", InstrumentID: "btc", VenueEnvironment: "demo"}, Role: execution.LegRoleLong, Side: execution.SideBuy, Symbol: "BTC-USDT", GatewayInstrumentID: "BTC-USDT-SWAP", MarkPrice: decimal.RequireFromString("100"), Notional: decimal.RequireFromString("500"), Quantity: decimal.RequireFromString("5")},
			{LegIdentity: execution.LegIdentity{AccountID: "short", Venue: "bybit", InstrumentID: "btc", VenueEnvironment: "testnet"}, Role: execution.LegRoleShort, Side: execution.SideSell, Symbol: "BTC-USDT", GatewayInstrumentID: "BTCUSDT", MarkPrice: decimal.RequireFromString("100"), Notional: decimal.RequireFromString("500"), Quantity: decimal.RequireFromString("5")},
		},
	}
}

func testExecution(t *testing.T, scope readmodel.Scope, id string) *execution.Execution {
	t.Helper()
	exec, err := execution.NewExecution(id, testPreview(scope, "preview-"+id, time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func testCloseExecutionWithLineage(t *testing.T, scope readmodel.Scope, id, parentID, lineageID string) *execution.Execution {
	t.Helper()
	preview := testPreview(scope, "preview-"+id, time.Now().UTC())
	preview.Operation = execution.OperationClose
	preview.OpportunityID = ""
	preview.PositionID = "position-" + parentID
	preview.ParentExecutionID = parentID
	preview.LineageID = lineageID
	for i := range preview.PlannedLegs {
		preview.PlannedLegs[i].ReduceOnly = true
	}
	preview.PlannedLegs[0].Side = execution.SideSell
	preview.PlannedLegs[1].Side = execution.SideBuy
	exec, err := execution.NewExecution(id, preview)
	if err != nil {
		t.Fatal(err)
	}
	return exec
}

func assertExecutionLineageColumns(t *testing.T, ctx context.Context, pool *pgxpool.Pool, executionID, parentID, lineageID string) {
	t.Helper()
	var parentColumn, lineageColumn string
	if err := pool.QueryRow(ctx, `
		select coalesce(parent_execution_id, ''), coalesce(lineage_id, '')
		from executions
		where id=$1
	`, executionID).Scan(&parentColumn, &lineageColumn); err != nil {
		t.Fatal(err)
	}
	if parentColumn != parentID || lineageColumn != lineageID {
		t.Fatalf("execution lineage columns parent=%q lineage=%q, want parent=%q lineage=%q", parentColumn, lineageColumn, parentID, lineageID)
	}
}

func testExecutionWithPartialFill(t *testing.T, ctx context.Context, store *ExecutionStore, scope readmodel.Scope, id, gatewayFillID, price string) *execution.Execution {
	t.Helper()
	exec := testExecution(t, scope, id)
	if _, err := store.ReserveExecution(ctx, scope, "POST", "/api/executions", "idem-"+id, "hash-"+id, exec.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventBeginSubmit, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{Type: execution.EventLegAccepted, Role: execution.LegRoleLong, GatewayID: "gw-" + id, OrderID: "order-" + id, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := exec.Apply(execution.Event{
		Type: execution.EventLegPartial, GatewayID: "gw-" + id, FillID: "fill-" + id, GatewayFillID: gatewayFillID,
		Quantity: decimal.RequireFromString("2"), Price: decimal.RequireFromString(price),
		Fee: execution.Money{Amount: decimal.RequireFromString("0.1"), Currency: "USDT"}, At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PersistTransition(ctx, PersistTransitionInput{
		Scope: scope, Execution: exec.Snapshot(), ExpectedVersion: 1,
		Kind: "leg_partial", RequestID: "req-" + id, Payload: json.RawMessage(`{"safe":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	return exec
}
