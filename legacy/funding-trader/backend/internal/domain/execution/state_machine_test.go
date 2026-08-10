package execution

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestExecutionCompletesOpenAndCloseThroughAcceptedWorkingFilledLegs(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)

	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertState(t, exec, StateLeg1Working)
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertState(t, exec, StateLeg2Working)
	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("2.5"), now()))
	assertState(t, exec, StatePartiallyHedged)

	closeExec := newPreviewedExecution(OperationClose)
	assertApply(t, closeExec, BeginSubmit(now()))
	assertApply(t, closeExec, LegAccepted("gw-1", LegRoleLong, "close-1", now()))
	assertApply(t, closeExec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, closeExec, LegAccepted("gw-2", LegRoleShort, "close-2", now()))
	assertApply(t, closeExec, LegFilled("gw-2", decimal.RequireFromString("2.5"), now()))
	assertState(t, closeExec, StatePartiallyHedged)
	for _, leg := range closeExec.Legs {
		if !leg.ReduceOnly {
			t.Fatalf("close leg must be reduce-only: %#v", leg)
		}
	}
}

func TestOpenWithDifferentPricesAndIncrementsHedgesWhenCommonQuantityFilled(t *testing.T) {
	input := validPreviewInput(now())
	input.Long.MarkPrice = decimal.NewFromInt(100)
	input.Short.MarkPrice = decimal.NewFromInt(120)
	input.Long.QuantityIncrement = decimal.RequireFromString("0.003")
	input.Short.QuantityIncrement = decimal.RequireFromString("0.002")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-common", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("4.164"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("4.164"), now()))
	assertState(t, exec, StateHedged)
	assertDecimal(t, exec.DirectionalExposure, "0")
}

func TestCloseWithUnequalInitialQuantitiesClosesWhenBothInitialQuantitiesFilled(t *testing.T) {
	input := validPreviewInput(now())
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("5")
	input.CurrentShortQuantity = decimal.RequireFromString("2.5")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-close", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "close-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "close-2", now()))
	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("2.5"), now()))
	assertState(t, exec, StateClosed)
	assertDecimal(t, exec.DirectionalExposure, "0")
}

func TestAuthoritativeDetailedFillCorrectsRejectedCloseLeg(t *testing.T) {
	input := validPreviewInput(now())
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("5")
	input.CurrentShortQuantity = decimal.RequireFromString("2.5")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-close-late-fill", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-long", LegRoleLong, "order-long", now()))
	assertApply(t, exec, LegFilled("gw-long", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegRejectedBeforeAcceptance(
		"gw-short",
		LegRoleShort,
		ErrorInfo{Code: "GATEWAY_WRITE_REJECTED", SafeMessage: "gateway write was rejected"},
		now(),
	))
	assertState(t, exec, StatePartiallyHedged)

	if err := exec.Apply(LegFilled("gw-short", decimal.RequireFromString("2.5"), now())); err == nil {
		t.Fatal("rejected leg accepted a status-only fill without authoritative fill evidence")
	}
	lateFill := LegFilledDeltaDetail(
		"fill-short-1",
		"gw-short",
		"gateway-fill-short-1",
		decimal.RequireFromString("2.5"),
		decimal.RequireFromString("2.5"),
		decimal.RequireFromString("100"),
		Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"},
		now().Add(time.Second),
	)
	if err := exec.Apply(lateFill); err == nil {
		t.Fatal("rejected leg accepted a detailed fill without the replayed gateway order identity")
	}
	lateFill.OrderID = "order-short"
	assertApply(t, exec, lateFill)

	assertState(t, exec, StateClosed)
	assertDecimal(t, exec.DirectionalExposure, "0")
	if len(exec.Fills) != 2 || exec.Fills[1].GatewayFillID != "gateway-fill-short-1" {
		t.Fatalf("late authoritative fill was not retained: %#v", exec.Fills)
	}
	if exec.Legs[1].Status != LegStatusFilled || exec.Legs[1].LastError.Code != "" || exec.Legs[1].Error != "" {
		t.Fatalf("rejected leg evidence was not corrected: %#v", exec.Legs[1])
	}
}

func TestRecoveredDetailedFillDoesNotMoveExecutionUpdatedAtBackward(t *testing.T) {
	base := time.Date(2026, 8, 3, 5, 35, 48, 0, time.UTC)
	input := validPreviewInput(base)
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("5")
	input.CurrentShortQuantity = decimal.RequireFromString("2.5")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-close-recovered-fill", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}

	assertApply(t, exec, BeginSubmit(base.Add(time.Second)))
	assertApply(t, exec, LegAccepted("gw-long", LegRoleLong, "order-long", base.Add(2*time.Second)))
	assertApply(t, exec, LegFilled("gw-long", decimal.RequireFromString("5"), base.Add(3*time.Second)))
	acceptedAt := base.Add(10 * time.Second)
	assertApply(t, exec, LegAccepted("gw-short", LegRoleShort, "order-short", acceptedAt))

	fillOccurredAt := base.Add(5 * time.Second)
	recoveredFill := LegFilledDeltaDetail(
		"fill-short-1",
		"gw-short",
		"gateway-fill-short-1",
		decimal.RequireFromString("2.5"),
		decimal.RequireFromString("2.5"),
		decimal.RequireFromString("100"),
		Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"},
		fillOccurredAt,
	)
	assertApply(t, exec, recoveredFill)

	assertState(t, exec, StateClosed)
	if len(exec.Fills) != 2 || !exec.Fills[1].OccurredAt.Equal(fillOccurredAt) {
		t.Fatalf("recovered venue fill time = %v, want %v", exec.Fills, fillOccurredAt)
	}
	if !exec.UpdatedAt.Equal(acceptedAt) {
		t.Fatalf("execution updatedAt = %v, want monotonic %v", exec.UpdatedAt, acceptedAt)
	}
}

func TestCloseCompensationIsBoundedToCommandCreatedResidualExposure(t *testing.T) {
	input := validPreviewInput(now())
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("5")
	input.CurrentShortQuantity = decimal.RequireFromString("2.5")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-close", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "close-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "close-2", now()))
	assertApply(t, exec, LegPartial("gw-2", decimal.RequireFromString("1"), now()))
	assertDecimal(t, exec.DirectionalExposure, "1.5")
	if err := exec.Apply(CompensationAccepted("gw-c-over", "comp-over", LegRoleLong, decimal.RequireFromString("1.501"), now())); err == nil {
		t.Fatalf("expected over compensation to be rejected")
	}
	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("1.5"), now()))
	assertApply(t, exec, LegFilled("gw-c", decimal.RequireFromString("1.5"), now()))
	assertDecimal(t, exec.DirectionalExposure, "0")
}

func TestExecutionCompensatesPartialSecondLegWithinExposureOnce(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegPartial("gw-2", decimal.RequireFromString("2"), now()))
	assertState(t, exec, StatePartiallyHedged)

	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("3"), now()))
	assertState(t, exec, StateCompensating)
	assertApply(t, exec, LegFilled("gw-c", decimal.RequireFromString("3"), now()))

	assertState(t, exec, StateFailed)
	if exec.Compensation == nil || !exec.Compensation.Attempted || exec.Compensation.ManualRequired {
		t.Fatalf("expected completed compensation state, got %#v", exec.Compensation)
	}
}

func TestExecutionRejectsSecondLegWithExposureVisibleAndManualRequired(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegRejected("gw-2", "no liquidity", now()))

	assertState(t, exec, StatePartiallyHedged)
	if exec.ManualIntervention {
		t.Fatalf("second leg rejection with exposure must allow compensation before manual terminal")
	}
	assertDecimal(t, exec.DirectionalExposure, "5")
	if exec.Legs[1].Status != LegStatusRejected || exec.Legs[1].Error != "no liquidity" {
		t.Fatalf("expected exact rejected leg/error preserved: %#v", exec.Legs[1])
	}
}

func TestExecutionAmbiguousWritesBecomeUncertainWithoutSuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []Event
	}{
		{name: "initial", events: []Event{
			BeginSubmit(now()),
			LegUncertain("gw-1", LegRoleLong, "timeout", now()),
		}},
		{name: "second", events: []Event{
			BeginSubmit(now()),
			LegAccepted("gw-1", LegRoleLong, "order-1", now()),
			LegFilled("gw-1", decimal.RequireFromString("5"), now()),
			LegAccepted("gw-2", LegRoleShort, "order-2", now()),
			LegUncertain("gw-2", LegRoleShort, "timeout", now()),
		}},
		{name: "compensation", events: []Event{
			BeginSubmit(now()),
			LegAccepted("gw-1", LegRoleLong, "order-1", now()),
			LegFilled("gw-1", decimal.RequireFromString("5"), now()),
			LegAccepted("gw-2", LegRoleShort, "order-2", now()),
			LegPartial("gw-2", decimal.RequireFromString("2"), now()),
			CompensationUncertain("gw-c", "timeout", LegRoleLong, decimal.RequireFromString("3"), now()),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := newPreviewedExecution(OperationOpen)
			for _, event := range tc.events {
				assertApply(t, exec, event)
			}
			assertState(t, exec, StateUncertain)
			if tc.name == "compensation" && (exec.Compensation == nil || !exec.Compensation.ManualRequired || exec.Compensation.Error.SafeMessage != "gateway error") {
				t.Fatalf("expected exact compensation uncertainty preserved: %#v", exec.Compensation)
			}
		})
	}
}

func TestLegUncertainPreservesOrderIdentity(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))

	assertApply(t, exec, Event{Type: EventLegUncertain, GatewayID: "exec-1:long", Role: LegRoleLong, OrderID: "real-gateway-uuid", Error: "timeout", At: now()})

	assertState(t, exec, StateUncertain)
	if len(exec.Legs) != 1 || exec.Legs[0].OrderID != "real-gateway-uuid" || exec.Legs[0].GatewayOrderID != "real-gateway-uuid" {
		t.Fatalf("uncertain leg identity not preserved: %#v", exec.Legs)
	}
}

func TestExistingLegUncertainFillsEmptyOrderIdentityWithoutOverwritingDifferentIdentity(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("exec-1:long", LegRoleLong, "", now()))

	assertApply(t, exec, Event{Type: EventLegUncertain, GatewayID: "exec-1:long", Role: LegRoleLong, OrderID: "real-gateway-uuid", Error: "timeout", At: now()})

	if exec.Legs[0].OrderID != "real-gateway-uuid" || exec.Legs[0].GatewayOrderID != "real-gateway-uuid" {
		t.Fatalf("existing uncertain leg identity not backfilled: %#v", exec.Legs[0])
	}

	assertApply(t, exec, Event{Type: EventLegUncertain, GatewayID: "exec-1:long", Role: LegRoleLong, OrderID: "different-id", Error: "still timeout", At: now()})
	if exec.Legs[0].OrderID != "real-gateway-uuid" || exec.Legs[0].GatewayOrderID != "real-gateway-uuid" {
		t.Fatalf("existing non-empty identity was overwritten: %#v", exec.Legs[0])
	}
}

func TestExecutionCompensationRejectAndOverCompensationRequireManualOrReject(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegPartial("gw-2", decimal.RequireFromString("2"), now()))

	if err := exec.Apply(CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("3.001"), now())); err == nil {
		t.Fatalf("expected over-compensation to be rejected")
	}
	assertState(t, exec, StatePartiallyHedged)

	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("3"), now()))
	assertApply(t, exec, LegRejected("gw-c", "reduce failed", now()))
	assertState(t, exec, StateManualInterventionRequired)
	if exec.Compensation == nil || !exec.Compensation.ManualRequired || exec.Compensation.Error.SafeMessage != "gateway error" {
		t.Fatalf("expected exact compensation rejection preserved: %#v", exec.Compensation)
	}
	if err := exec.Apply(CompensationAccepted("gw-c2", "comp-2", LegRoleLong, decimal.RequireFromString("1"), now())); err == nil {
		t.Fatalf("expected second automatic compensation attempt to be rejected")
	}
}

func TestExecutionRejectsDuplicateIllegalAndTerminalTransitions(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	event := LegAccepted("gw-1", LegRoleLong, "order-1", now())
	assertApply(t, exec, event)
	assertApply(t, exec, event)
	if len(exec.Legs) != 1 {
		t.Fatalf("duplicate event should not add a leg, got %d", len(exec.Legs))
	}
	if err := exec.Apply(LegFilled("gw-missing", decimal.RequireFromString("1"), now())); err == nil {
		t.Fatalf("expected fill for unknown leg to be illegal")
	}
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("2.5"), now()))
	assertState(t, exec, StatePartiallyHedged)
	if err := exec.Apply(LegAccepted("gw-3", LegRoleCompensation, "late", now())); err == nil {
		t.Fatalf("expected terminal transition to be rejected")
	}
}

func newPreviewedExecution(operation Operation) *Execution {
	input := validPreviewInput(now())
	input.Short.MarkPrice = decimal.RequireFromString("100")
	input.Operation = operation
	if operation == OperationClose {
		input.OpportunityID = ""
		input.PositionID = "position-1"
		input.CurrentPositionVerifiable = true
		input.CurrentLongQuantity = decimal.RequireFromString("5")
		input.CurrentShortQuantity = decimal.RequireFromString("5")
	}
	preview, err := Preview(input)
	if err != nil {
		panic(err)
	}
	exec, err := NewExecution("exec-1", preview)
	if err != nil {
		panic(err)
	}
	return exec
}

func now() time.Time {
	return time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
}

func assertApply(t *testing.T, exec *Execution, event Event) {
	t.Helper()
	if err := exec.Apply(event); err != nil {
		t.Fatalf("Apply(%#v) returned error: %v", event, err)
	}
}

func assertState(t *testing.T, exec *Execution, want State) {
	t.Helper()
	if exec.State != want {
		t.Fatalf("expected state %s, got %s", want, exec.State)
	}
}
