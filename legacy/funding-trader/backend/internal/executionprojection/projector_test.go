package executionprojection

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

func TestProjectOpenHedgedExecutionCreatesStableVisiblePositionAndIdempotentActivity(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")
	exec := openExecution("exec-open-1", execution.StateHedged)

	if err := projector.Project(ctx, scope, exec); err != nil {
		t.Fatalf("Project returned error: %v", err)
	}
	if err := projector.Project(ctx, scope, exec); err != nil {
		t.Fatalf("second Project returned error: %v", err)
	}

	position := store.position("position-exec-open-1")
	if position.Status != "open" || position.Scope != scope || position.Symbol != "BTC-USDT" {
		t.Fatalf("position metadata = %#v", position)
	}
	var payload positionPayload
	decodePayload(t, position.Payload, &payload)
	if payload.PositionID != "position-exec-open-1" ||
		payload.OpeningExecutionID != "exec-open-1" ||
		payload.LineageID != "exec-open-1" ||
		payload.OpportunityID != "opp-btc-carry" ||
		payload.Status != "open" ||
		payload.State != string(execution.StateHedged) {
		t.Fatalf("unexpected position identity/status payload: %#v", payload)
	}
	assertDecimal(t, payload.CapitalUSDT, "100")
	assertDecimal(t, payload.SingleLegNotional, "300")
	assertDecimal(t, payload.ExpectedNetAprPct, "18.5")
	if payload.Leverage != 3 {
		t.Fatalf("leverage = %d", payload.Leverage)
	}
	if payload.LongLeg.AccountID != "acct-long" || payload.LongLeg.Venue != "okx" ||
		payload.LongLeg.InstrumentID != "okx-btc" ||
		payload.LongLeg.VenueEnvironment != "demo" ||
		payload.LongLeg.GatewayInstrumentID != "BTC-USDT-SWAP" {
		t.Fatalf("long leg identity lost: %#v", payload.LongLeg)
	}
	if payload.ShortLeg.AccountID != "acct-short" || payload.ShortLeg.Venue != "bybit" ||
		payload.ShortLeg.InstrumentID != "bybit-btc" ||
		payload.ShortLeg.VenueEnvironment != "testnet" ||
		payload.ShortLeg.GatewayInstrumentID != "BTCUSDT" {
		t.Fatalf("short leg identity lost: %#v", payload.ShortLeg)
	}
	assertDecimal(t, payload.LongLeg.InitialQuantity, "0.12")
	assertDecimal(t, payload.ShortLeg.InitialQuantity, "0.12")
	assertDecimal(t, payload.LongLeg.CurrentQuantity, "0.12")
	assertDecimal(t, payload.ShortLeg.CurrentQuantity, "0.12")
	if raw := string(position.Payload); strings.Contains(raw, "unrealizedPnl") || strings.Contains(raw, "liquidation") {
		t.Fatalf("projection fabricated risk/PnL fields: %s", raw)
	}

	if len(store.activities) != 1 {
		t.Fatalf("activities = %d, want idempotent single append", len(store.activities))
	}
	activity := store.onlyActivity()
	if activity.Kind != "position.opened" || activity.PositionID != payload.PositionID || activity.ExecutionID != exec.ID {
		t.Fatalf("activity metadata = %#v", activity)
	}
	var activityPayload activityPayload
	decodePayload(t, activity.Payload, &activityPayload)
	if activityPayload.ActivityID != activity.ID ||
		activityPayload.State != string(execution.StateHedged) ||
		activityPayload.Status != "open" {
		t.Fatalf("activity payload = %#v", activityPayload)
	}
}

func TestProjectCloseExecutionUpdatesExistingPositionClosedWithZeroCurrentQuantities(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")

	open := openExecution("exec-open-1", execution.StateHedged)
	if err := projector.Project(ctx, scope, open); err != nil {
		t.Fatalf("project open: %v", err)
	}
	close := closeExecution("exec-close-1", open, execution.StateClosed, "0.12", "0.12")
	if err := projector.Project(ctx, scope, close); err != nil {
		t.Fatalf("project close: %v", err)
	}

	position := store.position("position-exec-open-1")
	if position.Status != "closed" {
		t.Fatalf("position status = %q", position.Status)
	}
	var payload positionPayload
	decodePayload(t, position.Payload, &payload)
	if payload.ClosingExecutionID != "exec-close-1" ||
		payload.OpeningExecutionID != "exec-open-1" ||
		payload.ParentExecutionID != "exec-open-1" ||
		payload.Status != "closed" ||
		payload.State != string(execution.StateClosed) {
		t.Fatalf("close payload identity/status = %#v", payload)
	}
	assertDecimal(t, payload.LongLeg.CurrentQuantity, "0")
	assertDecimal(t, payload.ShortLeg.CurrentQuantity, "0")

	if len(store.activities) != 2 {
		t.Fatalf("activities = %d, want open and closed", len(store.activities))
	}
	if !store.hasActivityKind("position.closed") {
		t.Fatalf("missing position.closed activity: %#v", store.activities)
	}
}

func TestProjectPartialOrManualExecutionKeepsPositionVisibleAsAttention(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")
	exec := openExecution("exec-open-1", execution.StatePartiallyHedged)
	exec.Legs[0].Filled = decimal.RequireFromString("0.12")
	exec.Legs[1].Filled = decimal.RequireFromString("0.04")
	exec.DirectionalExposure = decimal.RequireFromString("0.08")

	if err := projector.Project(ctx, scope, exec); err != nil {
		t.Fatalf("Project returned error: %v", err)
	}

	position := store.position("position-exec-open-1")
	if position.Status != "attention" {
		t.Fatalf("position status = %q", position.Status)
	}
	var payload positionPayload
	decodePayload(t, position.Payload, &payload)
	if payload.Status != "attention" || !payload.RequiresAttention {
		t.Fatalf("attention flags = %#v", payload)
	}
	assertDecimal(t, payload.LongLeg.CurrentQuantity, "0.12")
	assertDecimal(t, payload.ShortLeg.CurrentQuantity, "0.04")
	if !store.hasActivityKind("position.attention_required") {
		t.Fatalf("missing attention activity: %#v", store.activities)
	}
}

func TestProjectRejectsNonProductMode(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")
	exec := openExecution("exec-open-1", execution.StateHedged)
	exec.Mode = "paper"

	err := projector.Project(ctx, readmodel.Scope{PrincipalID: exec.PrincipalID, Mode: "paper"}, exec)
	if err == nil {
		t.Fatal("expected projector to reject non-product mode")
	}
}

func TestProjectSuccessfulCompensationClosesFlatPositionWithoutFalseExposure(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")
	exec := openExecution("exec-open-1", execution.StateFailed)
	exec.Legs[0].Filled = decimal.RequireFromString("0.12")
	exec.Legs[1].Filled = decimal.Zero
	exec.Legs = append(exec.Legs, execution.Leg{
		Role: execution.LegRoleCompensation, Status: execution.LegStatusFilled,
		Filled: decimal.RequireFromString("0.12"),
	})
	exec.Compensation = &execution.CompensationState{
		Attempted: true, TargetRole: execution.LegRoleLong,
		Quantity: decimal.RequireFromString("0.12"), Status: execution.LegStatusFilled,
	}
	exec.DirectionalExposure = decimal.Zero

	if err := projector.Project(ctx, scope, exec); err != nil {
		t.Fatalf("Project returned error: %v", err)
	}
	position := store.position("position-exec-open-1")
	var payload positionPayload
	decodePayload(t, position.Payload, &payload)
	if position.Status != "closed" || payload.Status != "closed" {
		t.Fatalf("flat compensated position remained open: metadata=%#v payload=%#v", position, payload)
	}
	assertDecimal(t, payload.LongLeg.CurrentQuantity, "0")
	assertDecimal(t, payload.ShortLeg.CurrentQuantity, "0")
	assertDecimal(t, payload.SingleLegNotional, "300")
}

func TestRebuildProjectsStoredExecutionsInUpdatedOrder(t *testing.T) {
	ctx := context.Background()
	scope := readmodel.Scope{PrincipalID: "principal-1", Mode: "test"}
	store := newMemoryStore()
	projector := NewProjector(store, "boot-g004")
	open := openExecution("exec-open-1", execution.StateHedged)
	close := closeExecution("exec-close-1", open, execution.StateClosed, "0.12", "0.12")
	store.executions = []execution.Execution{close, open}

	if err := projector.Rebuild(ctx, scope); err != nil {
		t.Fatalf("Rebuild returned error: %v", err)
	}

	position := store.position("position-exec-open-1")
	var payload positionPayload
	decodePayload(t, position.Payload, &payload)
	if position.Status != "closed" || payload.ClosingExecutionID != "exec-close-1" {
		t.Fatalf("rebuild did not preserve final close state: status=%q payload=%#v", position.Status, payload)
	}
}

type positionPayload struct {
	PositionID         string          `json:"positionId"`
	OpeningExecutionID string          `json:"openingExecutionId"`
	ClosingExecutionID string          `json:"closingExecutionId,omitempty"`
	ParentExecutionID  string          `json:"parentExecutionId,omitempty"`
	LineageID          string          `json:"lineageId"`
	OpportunityID      string          `json:"opportunityId,omitempty"`
	Symbol             string          `json:"symbol"`
	Status             string          `json:"status"`
	State              string          `json:"state"`
	RequiresAttention  bool            `json:"requiresAttention"`
	CapitalUSDT        decimal.Decimal `json:"capitalUsdt"`
	SingleLegNotional  decimal.Decimal `json:"singleLegNotional"`
	Leverage           int             `json:"leverage"`
	ExpectedNetAprPct  decimal.Decimal `json:"expectedNetAprPct"`
	LongLeg            legPayload      `json:"longLeg"`
	ShortLeg           legPayload      `json:"shortLeg"`
}

type legPayload struct {
	AccountID           string          `json:"accountId"`
	Venue               string          `json:"venue"`
	Symbol              string          `json:"symbol"`
	InstrumentID        string          `json:"instrumentId"`
	VenueEnvironment    string          `json:"venueEnvironment"`
	GatewayInstrumentID string          `json:"gatewayInstrumentId"`
	InitialQuantity     decimal.Decimal `json:"initialQuantity"`
	CurrentQuantity     decimal.Decimal `json:"currentQuantity"`
}

type activityPayload struct {
	ActivityID  string `json:"activityId"`
	PositionID  string `json:"positionId"`
	ExecutionID string `json:"executionId"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	State       string `json:"state"`
}

func openExecution(id string, state execution.State) execution.Execution {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	return execution.Execution{
		ID:                       id,
		PrincipalID:              "principal-1",
		Mode:                     "test",
		Operation:                execution.OperationOpen,
		OpportunityID:            "opp-btc-carry",
		LineageID:                id,
		State:                    state,
		CreatedAt:                at,
		UpdatedAt:                at.Add(time.Minute),
		CapitalUSDT:              decimal.RequireFromString("100"),
		Leverage:                 3,
		ExpectedNetAprPct:        decimal.RequireFromString("18.5"),
		InitialLongQuantity:      decimal.RequireFromString("0.12"),
		InitialShortQuantity:     decimal.RequireFromString("0.12"),
		DirectionalExposure:      decimal.Zero,
		PlannedLegs:              plannedLegs(),
		Legs:                     filledLegs("0.12", "0.12", false),
		ManualIntervention:       state == execution.StateManualInterventionRequired,
		ManualInterventionReason: "manual review required",
	}
}

func closeExecution(id string, open execution.Execution, state execution.State, longFilled, shortFilled string) execution.Execution {
	at := open.UpdatedAt.Add(time.Minute)
	return execution.Execution{
		ID:                   id,
		PrincipalID:          open.PrincipalID,
		Mode:                 open.Mode,
		Operation:            execution.OperationClose,
		PositionID:           "position-" + open.ID,
		ParentExecutionID:    open.ID,
		LineageID:            open.LineageID,
		State:                state,
		CreatedAt:            at,
		UpdatedAt:            at.Add(time.Minute),
		CapitalUSDT:          open.CapitalUSDT,
		Leverage:             open.Leverage,
		ExpectedNetAprPct:    open.ExpectedNetAprPct,
		InitialLongQuantity:  open.InitialLongQuantity,
		InitialShortQuantity: open.InitialShortQuantity,
		PlannedLegs: []execution.PlannedLeg{
			plannedLeg(execution.LegRoleLong, execution.SideSell, "acct-long", "okx", "okx-btc", "BTC-USDT-SWAP", true),
			plannedLeg(execution.LegRoleShort, execution.SideBuy, "acct-short", "bybit", "bybit-btc", "BTCUSDT", true),
		},
		Legs: filledLegs(longFilled, shortFilled, true),
	}
}

func plannedLegs() []execution.PlannedLeg {
	return []execution.PlannedLeg{
		plannedLeg(execution.LegRoleLong, execution.SideBuy, "acct-long", "okx", "okx-btc", "BTC-USDT-SWAP", false),
		plannedLeg(execution.LegRoleShort, execution.SideSell, "acct-short", "bybit", "bybit-btc", "BTCUSDT", false),
	}
}

func plannedLeg(role execution.LegRole, side execution.Side, accountID, venue, instrumentID, gatewayInstrumentID string, reduceOnly bool) execution.PlannedLeg {
	venueEnvironment := "demo"
	if role == execution.LegRoleShort {
		venueEnvironment = "testnet"
	}
	return execution.PlannedLeg{
		LegIdentity: execution.LegIdentity{
			AccountID: accountID, Venue: venue,
			InstrumentID: instrumentID, VenueEnvironment: venueEnvironment,
		},
		Role:                role,
		Side:                side,
		Symbol:              "BTC-USDT",
		GatewayInstrumentID: gatewayInstrumentID,
		MarkPrice:           decimal.RequireFromString("2500"),
		Notional:            decimal.RequireFromString("300"),
		Quantity:            decimal.RequireFromString("0.12"),
		ReduceOnly:          reduceOnly,
	}
}

func filledLegs(longFilled, shortFilled string, reduceOnly bool) []execution.Leg {
	return []execution.Leg{
		{LegIdentity: execution.LegIdentity{AccountID: "acct-long", Venue: "okx", InstrumentID: "okx-btc", VenueEnvironment: "demo"}, Role: execution.LegRoleLong, Status: execution.LegStatusFilled, Side: execution.SideBuy, Quantity: decimal.RequireFromString("0.12"), RequestedQuantity: decimal.RequireFromString("0.12"), Filled: decimal.RequireFromString(longFilled), ReduceOnly: reduceOnly},
		{LegIdentity: execution.LegIdentity{AccountID: "acct-short", Venue: "bybit", InstrumentID: "bybit-btc", VenueEnvironment: "testnet"}, Role: execution.LegRoleShort, Status: execution.LegStatusFilled, Side: execution.SideSell, Quantity: decimal.RequireFromString("0.12"), RequestedQuantity: decimal.RequireFromString("0.12"), Filled: decimal.RequireFromString(shortFilled), ReduceOnly: reduceOnly},
	}
}

type memoryStore struct {
	executions []execution.Execution
	positions  map[string]readmodel.PositionProjection
	activities map[string]readmodel.ActivityProjection
}

func newMemoryStore() *memoryStore {
	return &memoryStore{positions: make(map[string]readmodel.PositionProjection), activities: make(map[string]readmodel.ActivityProjection)}
}

func (store *memoryStore) ListExecutions(_ context.Context, _ readmodel.Scope) ([]execution.Execution, error) {
	return append([]execution.Execution(nil), store.executions...), nil
}

func (store *memoryStore) ApplyExecutionProjection(
	_ context.Context,
	_ string,
	position readmodel.PositionProjection,
	activity readmodel.ActivityProjection,
) ([]readmodel.Event, error) {
	store.positions[position.ID] = position
	events := []readmodel.Event{{
		Kind: readmodel.PositionUpsert, EntityID: position.ID,
	}}
	if _, ok := store.activities[activity.ID]; ok {
		return events, nil
	}
	store.activities[activity.ID] = activity
	return append(events, readmodel.Event{
		Kind: readmodel.ActivityAppend, EntityID: activity.ID,
	}), nil
}

func (store *memoryStore) position(id string) readmodel.PositionProjection {
	position, ok := store.positions[id]
	if !ok {
		panic("missing position " + id)
	}
	return position
}

func (store *memoryStore) onlyActivity() readmodel.ActivityProjection {
	if len(store.activities) != 1 {
		panic("expected one activity")
	}
	for _, activity := range store.activities {
		return activity
	}
	panic("unreachable")
}

func (store *memoryStore) hasActivityKind(kind string) bool {
	for _, activity := range store.activities {
		if activity.Kind == kind {
			return true
		}
	}
	return false
}

func decodePayload(t *testing.T, raw json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}

func assertDecimal(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Fatalf("decimal = %s, want %s", got, want)
	}
}
