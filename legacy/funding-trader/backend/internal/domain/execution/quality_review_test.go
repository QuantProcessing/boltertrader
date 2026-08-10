package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCloseCompensationUsesOppositeOfTargetLegActualSide(t *testing.T) {
	exec := newPreviewedExecution(OperationClose)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "close-long", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "close-short", now()))
	assertApply(t, exec, LegPartial("gw-2", decimal.RequireFromString("2"), now()))

	assertApply(t, exec, CompensationAccepted("gw-c", "comp-close", LegRoleLong, decimal.RequireFromString("3"), now()))

	if exec.Legs[2].Side != SideBuy {
		t.Fatalf("close compensation must buy opposite of long close sell side, got %#v", exec.Legs[2])
	}
}

func TestFilledLegsCannotReachSuccessWhenDirectionalExposureRemains(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("2"), now()))

	if exec.State == StateHedged || exec.State == StateClosed {
		t.Fatalf("non-zero directional exposure must not be successful, state=%s exposure=%s", exec.State, exec.DirectionalExposure)
	}
	assertState(t, exec, StatePartiallyHedged)
	assertDecimal(t, exec.DirectionalExposure, "3")
}

func TestFirstLegPartialCanBecomeFullAndThenAcceptSecondLeg(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("2"), now()))
	assertState(t, exec, StateLeg1Working)

	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertState(t, exec, StateLeg1Working)
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertState(t, exec, StateLeg2Working)
}

func TestSameCumulativeQuantityCanUpgradePartialToFilled(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("5"), now()))
	if exec.Legs[0].Status != LegStatusPartial {
		t.Fatalf("expected first event to preserve partial status, got %#v", exec.Legs[0])
	}

	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	if exec.Legs[0].Status != LegStatusFilled {
		t.Fatalf("same cumulative quantity with filled status must upgrade leg, got %#v", exec.Legs[0])
	}
	if len(exec.Fills) != 1 {
		t.Fatalf("status-only upgrade must not append duplicate fill, got %#v", exec.Fills)
	}
}

func TestRawGatewayErrorIsNotJSONExposedAndFallbackIsGeneric(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegRejected("gw-1", "secret-token raw gateway body", now()))

	body, err := json.Marshal(exec.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(body), "secret-token") || strings.Contains(string(body), "raw gateway body") {
		t.Fatalf("raw gateway error leaked into JSON: %s", body)
	}
	if exec.Legs[0].LastError.SafeMessage != "gateway error" {
		t.Fatalf("fallback safe error must be generic, got %#v", exec.Legs[0].LastError)
	}
}

func TestSnapshotDeepCopiesCompensationState(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)
	assertApply(t, exec, CompensationUncertain("gw-c", "write status unknown", LegRoleLong, decimal.RequireFromString("3"), now()))

	snapshot := exec.Snapshot()
	snapshot.Compensation.Status = LegStatusFilled
	snapshot.Compensation.Error.SafeMessage = "mutated"

	if exec.Compensation.Status != LegStatusUncertain || exec.Compensation.Error.SafeMessage != "gateway error" {
		t.Fatalf("snapshot compensation aliases original: original=%#v snapshot=%#v", exec.Compensation, snapshot.Compensation)
	}
}

func TestCompensationStatusOnlyFilledUpgradeRunsTerminalFailurePath(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)
	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("3"), now()))
	assertApply(t, exec, LegPartial("gw-c", decimal.RequireFromString("3"), now()))

	assertApply(t, exec, LegFilled("gw-c", decimal.RequireFromString("3"), now()))

	assertState(t, exec, StateFailed)
	if exec.Compensation == nil || exec.Compensation.Status != LegStatusFilled {
		t.Fatalf("expected compensation status filled, got %#v", exec.Compensation)
	}
	if exec.DirectionalExposure.IsPositive() {
		t.Fatalf("expected compensation fill to recompute flat exposure, got %s", exec.DirectionalExposure)
	}
}

func TestCompensationUncertainRawHelperUsesGenericSafeErrorAndCodeHelperPreservesSafeError(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)
	assertApply(t, exec, CompensationUncertain("gw-c", "secret-token raw gateway body", LegRoleLong, decimal.RequireFromString("3"), now()))

	body, err := json.Marshal(exec.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(body), "secret-token") || strings.Contains(exec.ManualInterventionReason, "secret-token") {
		t.Fatalf("raw compensation uncertainty leaked: reason=%q json=%s", exec.ManualInterventionReason, body)
	}
	if exec.Compensation == nil || exec.Compensation.Error.SafeMessage != "gateway error" || exec.ManualInterventionReason != "compensation uncertain: gateway error" {
		t.Fatalf("raw helper must use generic fallback, got reason=%q comp=%#v", exec.ManualInterventionReason, exec.Compensation)
	}

	explicit := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, explicit)
	assertApply(t, explicit, CompensationUncertainCode("gw-c", ErrorInfo{Code: "TIMEOUT", SafeMessage: "write status unknown"}, LegRoleLong, decimal.RequireFromString("3"), now()))
	if explicit.Compensation == nil || explicit.Compensation.Error.SafeMessage != "write status unknown" || explicit.ManualInterventionReason != "compensation uncertain: write status unknown" {
		t.Fatalf("explicit safe helper should preserve sanitized message, reason=%q comp=%#v", explicit.ManualInterventionReason, explicit.Compensation)
	}
}
