package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestDefinitePlacementRejectionBeforeAcceptancePersistsExactPlannedLeg(t *testing.T) {
	t.Run("first leg", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		assertApply(t, exec, BeginSubmit(now()))

		event := LegRejectedBeforeAcceptance(
			"gw-long-attempt",
			LegRoleLong,
			ErrorInfo{Code: "ORDER_REJECTED", SafeMessage: "order rejected"},
			now(),
		)
		assertApply(t, exec, event)

		assertState(t, exec, StateFailed)
		if len(exec.Legs) != 1 {
			t.Fatalf("expected one rejected leg, got %#v", exec.Legs)
		}
		leg := exec.Legs[0]
		plan := exec.PlannedLegs[0]
		if leg.Role != LegRoleLong ||
			leg.Status != LegStatusRejected ||
			leg.AccountID != plan.AccountID ||
			leg.InstrumentID != plan.InstrumentID ||
			leg.Side != plan.Side ||
			!leg.RequestedQuantity.Equal(plan.Quantity) ||
			leg.ReduceOnly != plan.ReduceOnly ||
			leg.LastError.Code != "ORDER_REJECTED" {
			t.Fatalf("rejected leg did not preserve planned identity: %#v", leg)
		}

		body, err := json.Marshal(exec.Snapshot())
		if err != nil {
			t.Fatalf("marshal execution: %v", err)
		}
		if strings.Contains(string(body), "raw gateway") {
			t.Fatalf("rejected execution exposed a raw gateway error: %s", body)
		}
	})

	t.Run("second leg after first fill", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		assertApply(t, exec, BeginSubmit(now()))
		assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
		assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))

		assertApply(t, exec, LegRejectedBeforeAcceptance(
			"gw-short-attempt",
			LegRoleShort,
			ErrorInfo{Code: "NO_LIQUIDITY", SafeMessage: "no executable liquidity"},
			now(),
		))

		assertState(t, exec, StatePartiallyHedged)
		if len(exec.Legs) != 2 || exec.Legs[1].Role != LegRoleShort ||
			exec.Legs[1].Status != LegStatusRejected {
			t.Fatalf("expected exact rejected short leg, got %#v", exec.Legs)
		}
		assertDecimal(t, exec.DirectionalExposure, "5")
	})

	t.Run("ordering remains strict", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		assertApply(t, exec, BeginSubmit(now()))
		if err := exec.Apply(LegRejectedBeforeAcceptance(
			"gw-short-attempt",
			LegRoleShort,
			ErrorInfo{Code: "REJECTED", SafeMessage: "rejected"},
			now(),
		)); err == nil {
			t.Fatalf("expected a short rejection before the first long attempt to be invalid")
		}
	})
}

func TestCanceledRemainderIsVisibleAndNeverCollapsesExposureToSuccess(t *testing.T) {
	t.Run("partial normal leg", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		assertApply(t, exec, BeginSubmit(now()))
		assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
		assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("2"), now()))

		assertApply(t, exec, LegCanceledCode(
			"gw-1",
			ErrorInfo{Code: "REMAINDER_CANCELED", SafeMessage: "unfilled remainder canceled"},
			now(),
		))

		assertState(t, exec, StatePartiallyHedged)
		if exec.Legs[0].Status != LegStatusCanceled ||
			exec.Legs[0].LastError.Code != "REMAINDER_CANCELED" {
			t.Fatalf("expected canceled partial leg to remain visible: %#v", exec.Legs[0])
		}
		assertDecimal(t, exec.DirectionalExposure, "2")
	})

	t.Run("unfilled first leg", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		assertApply(t, exec, BeginSubmit(now()))
		assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
		assertApply(t, exec, LegCanceled("gw-1", now()))

		assertState(t, exec, StateFailed)
		if exec.Legs[0].Status != LegStatusCanceled {
			t.Fatalf("expected canceled leg, got %#v", exec.Legs[0])
		}
	})

	t.Run("compensation cancellation requires manual intervention", func(t *testing.T) {
		exec := newPreviewedExecution(OperationOpen)
		moveToPartialSecondLeg(t, exec)
		assertApply(t, exec, CompensationAccepted(
			"gw-c",
			"comp-1",
			LegRoleLong,
			decimal.RequireFromString("3"),
			now(),
		))

		assertApply(t, exec, LegCanceledCode(
			"gw-c",
			ErrorInfo{Code: "COMPENSATION_CANCELED", SafeMessage: "compensation did not complete"},
			now(),
		))

		assertState(t, exec, StateManualInterventionRequired)
		if exec.Compensation == nil ||
			exec.Compensation.Status != LegStatusCanceled ||
			!exec.Compensation.ManualRequired ||
			exec.Compensation.Error.Code != "COMPENSATION_CANCELED" {
			t.Fatalf("expected canceled compensation to require manual action: %#v", exec.Compensation)
		}
	})
}

func TestCompensationLegCarriesExactTargetAccountAndInstrument(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)

	assertApply(t, exec, CompensationAccepted(
		"gw-c",
		"comp-1",
		LegRoleLong,
		decimal.RequireFromString("3"),
		now(),
	))

	target := exec.PlannedLegs[0]
	compensation := exec.Legs[2]
	if compensation.AccountID != target.AccountID ||
		compensation.InstrumentID != target.InstrumentID ||
		compensation.Side != oppositeSide(target.Side) ||
		!compensation.ReduceOnly {
		t.Fatalf("compensation identity must match its target route: target=%#v compensation=%#v", target, compensation)
	}
}

func TestDefiniteCompensationRejectionBeforeAcceptanceRequiresManualIntervention(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)

	assertApply(t, exec, CompensationRejectedBeforeAcceptance(
		"gw-c",
		"comp-order-1",
		LegRoleLong,
		decimal.RequireFromString("3"),
		ErrorInfo{Code: "REDUCE_REJECTED", SafeMessage: "reduce-only order rejected"},
		now(),
	))

	assertState(t, exec, StateManualInterventionRequired)
	if exec.Compensation == nil ||
		!exec.Compensation.Attempted ||
		exec.Compensation.Status != LegStatusRejected ||
		!exec.Compensation.ManualRequired ||
		exec.Compensation.OrderID != "comp-order-1" ||
		exec.Compensation.Error.Code != "REDUCE_REJECTED" {
		t.Fatalf("expected exact rejected compensation state: %#v", exec.Compensation)
	}
	if len(exec.Legs) != 3 {
		t.Fatalf("expected exact rejected compensation leg: %#v", exec.Legs)
	}
	target := exec.PlannedLegs[0]
	leg := exec.Legs[2]
	if leg.Role != LegRoleCompensation ||
		leg.Status != LegStatusRejected ||
		leg.AccountID != target.AccountID ||
		leg.InstrumentID != target.InstrumentID ||
		leg.GatewayOrderID != "comp-order-1" ||
		!leg.ReduceOnly {
		t.Fatalf("rejected compensation leg lost immutable identity: %#v", leg)
	}
}

func TestDetailedFillPreservesCumulativeStateAndExactVenueFillDeltas(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))

	first := LegPartialFillDetail(
		"fill-1",
		"gw-1",
		"gateway-fill-1",
		decimal.RequireFromString("2"),
		decimal.RequireFromString("2"),
		decimal.RequireFromString("100"),
		Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"},
		now(),
	)
	second := LegFilledDeltaDetail(
		"fill-2",
		"gw-1",
		"gateway-fill-2",
		decimal.RequireFromString("5"),
		decimal.RequireFromString("3"),
		decimal.RequireFromString("101"),
		Money{Amount: decimal.RequireFromString("0.02"), Currency: "USDT"},
		now(),
	)
	assertApply(t, exec, first)
	assertApply(t, exec, second)

	if !exec.Legs[0].Filled.Equal(decimal.RequireFromString("5")) {
		t.Fatalf("leg cumulative filled quantity = %s, want 5", exec.Legs[0].Filled)
	}
	if len(exec.Fills) != 2 ||
		!exec.Fills[0].Quantity.Equal(decimal.RequireFromString("2")) ||
		!exec.Fills[1].Quantity.Equal(decimal.RequireFromString("3")) {
		t.Fatalf("venue fill deltas were not preserved exactly: %#v", exec.Fills)
	}

	body, err := json.Marshal(exec.Snapshot())
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}
	restored, err := RestoreExecution(body)
	if err != nil {
		t.Fatalf("restore execution: %v", err)
	}
	assertApply(t, restored, second)
	if len(restored.Fills) != 2 {
		t.Fatalf("replayed detailed fill duplicated after restore: %#v", restored.Fills)
	}

	for name, mutate := range map[string]func(*Event){
		"gateway fill id": func(event *Event) {
			event.GatewayFillID = "gateway-fill-corrupted"
		},
		"price": func(event *Event) {
			event.Price = decimal.RequireFromString("999")
		},
		"fee": func(event *Event) {
			event.Fee = Money{Amount: decimal.RequireFromString("9"), Currency: "USDT"}
		},
	} {
		t.Run("changed "+name+" conflicts after restore", func(t *testing.T) {
			restarted, restoreErr := RestoreExecution(body)
			if restoreErr != nil {
				t.Fatalf("restore execution: %v", restoreErr)
			}
			changed := second
			mutate(&changed)
			if applyErr := restarted.Apply(changed); applyErr == nil {
				t.Fatalf("changed immutable %s was silently treated as a replay", name)
			}
		})
	}

	bad := LegPartialFillDetail(
		"fill-3",
		"gw-1",
		"gateway-fill-3",
		decimal.RequireFromString("5"),
		decimal.RequireFromString("1"),
		decimal.RequireFromString("101"),
		Money{},
		now(),
	)
	if err := restored.Apply(bad); err == nil {
		t.Fatalf("expected an exact fill delta without cumulative progress to be rejected")
	}
}
