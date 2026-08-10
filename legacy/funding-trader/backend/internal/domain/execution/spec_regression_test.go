package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestPreviewRequiresAndRetainsIDsAndDerivesCanSubmit(t *testing.T) {
	input := validPreviewInput(now())
	input.PreviewID = "preview-1"
	input.SnapshotID = "snapshot-1"

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if preview.PreviewID != "preview-1" || preview.SnapshotID != "snapshot-1" {
		t.Fatalf("expected ids retained, got preview=%q snapshot=%q", preview.PreviewID, preview.SnapshotID)
	}
	if !preview.CanSubmit {
		t.Fatalf("unblocked preview must derive CanSubmit=true")
	}

	input.PreviewID = ""
	input.SnapshotID = ""
	blocked, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if blocked.CanSubmit {
		t.Fatalf("blocked preview must derive CanSubmit=false")
	}
	assertFindingCodes(t, blocked.Blockers, []ReasonCode{ReasonInvalidPreviewID, ReasonInvalidSnapshotID})
}

func TestExecutionDetailCopiesPlannedLegsAndUsesSafeStructuredErrorsAndRichFills(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilledDetail("fill-1", "gw-1", "gw-fill-1", decimal.RequireFromString("5"), decimal.RequireFromString("100.25"), Money{Amount: decimal.RequireFromString("0.01"), Currency: "USDT"}, now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegRejectedCode("gw-2", ErrorInfo{Code: "NO_LIQUIDITY", SafeMessage: "no executable liquidity", Retryable: false}, now()))

	if len(exec.Legs) != 2 {
		t.Fatalf("expected two legs, got %#v", exec.Legs)
	}
	long := exec.Legs[0]
	if long.LegID == "" || long.AccountID == "" || long.InstrumentID == "" || long.GatewayOrderID != "order-1" || !long.RequestedQuantity.Equal(long.Quantity) || long.State != LegStatusFilled {
		t.Fatalf("leg detail missing planned/API fields: %#v", long)
	}
	short := exec.Legs[1]
	if short.LastError.Code != "NO_LIQUIDITY" || strings.Contains(short.LastError.SafeMessage, "secret") {
		t.Fatalf("expected safe structured error, got %#v", short.LastError)
	}
	if len(exec.Fills) != 1 {
		t.Fatalf("expected one fill, got %#v", exec.Fills)
	}
	fill := exec.Fills[0]
	if fill.FillID != "fill-1" || fill.LegID != long.LegID || fill.GatewayFillID != "gw-fill-1" || !fill.Price.Equal(decimal.RequireFromString("100.25")) || fill.Fee.Currency != "USDT" {
		t.Fatalf("fill detail missing API fields: %#v", fill)
	}
	if fill.AccountID != long.AccountID || fill.Venue != long.Venue || fill.InstrumentID != long.InstrumentID || fill.VenueEnvironment != long.VenueEnvironment {
		t.Fatalf("fill lost immutable leg identity: fill=%#v leg=%#v", fill, long)
	}
}

func TestRestoreExecutionBackfillsLegacyFillIdentityFromImmutableLeg(t *testing.T) {
	body := []byte(`{
		"id":"exec-legacy-fill",
		"principalId":"principal-1",
		"mode":"test",
		"operation":"open",
		"state":"hedged",
		"legs":[{
			"legId":"long",
			"role":"long",
			"accountId":"acct-long",
			"venue":"binance",
			"instrumentId":"binance:demo:BTC-USDT",
			"venueEnvironment":"demo",
			"gatewayId":"gw-long"
		}],
		"fills":[{
			"fillId":"fill-legacy",
			"legId":"long",
			"gatewayId":"gw-long",
			"role":"long",
			"quantity":"1",
			"price":"100",
			"fee":{"amount":"0","currency":"USDT"},
			"at":"2026-07-30T12:00:00Z",
			"occurredAt":"2026-07-30T12:00:00Z"
		}]
	}`)

	restored, err := RestoreExecution(body)
	if err != nil {
		t.Fatalf("RestoreExecution: %v", err)
	}
	fill := restored.Snapshot().Fills[0]
	if fill.AccountID != "acct-long" || fill.Venue != "binance" ||
		fill.InstrumentID != "binance:demo:BTC-USDT" ||
		fill.VenueEnvironment != "demo" {
		t.Fatalf("legacy fill identity not backfilled: %#v", fill)
	}
}

func TestCompensationUncertainSetsManualFlagButKeepsStateUncertainAndDetails(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)

	assertApply(t, exec, CompensationUncertain("gw-c", "write status unknown", LegRoleLong, decimal.RequireFromString("3"), now()))

	assertState(t, exec, StateUncertain)
	if !exec.ManualIntervention || exec.ManualInterventionReason != "compensation uncertain: gateway error" {
		t.Fatalf("expected execution manual flag with safe reason, got manual=%v reason=%q", exec.ManualIntervention, exec.ManualInterventionReason)
	}
	if exec.Compensation == nil || !exec.Compensation.ManualRequired || exec.Compensation.Error.SafeMessage != "gateway error" {
		t.Fatalf("expected compensation details preserved: %#v", exec.Compensation)
	}
	if len(exec.Legs) != 3 || exec.Legs[2].LastError.SafeMessage != "gateway error" {
		t.Fatalf("expected uncertain compensation leg retained: %#v", exec.Legs)
	}
}

func TestClosePreviewReversesDirectionsAndFailsClosedWithoutVerifiedExposure(t *testing.T) {
	input := validPreviewInput(now())
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.PreviewID = "preview-close"
	input.SnapshotID = "snapshot-close"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("5")
	input.CurrentShortQuantity = decimal.RequireFromString("5")

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if preview.Blocked() {
		t.Fatalf("expected close preview pass, blockers=%v", preview.Blockers)
	}
	if preview.PlannedLegs[0].Role != LegRoleLong || preview.PlannedLegs[0].Side != SideSell || !preview.PlannedLegs[0].ReduceOnly {
		t.Fatalf("long close leg must sell reduce-only: %#v", preview.PlannedLegs[0])
	}
	if preview.PlannedLegs[1].Role != LegRoleShort || preview.PlannedLegs[1].Side != SideBuy || !preview.PlannedLegs[1].ReduceOnly {
		t.Fatalf("short close leg must buy reduce-only: %#v", preview.PlannedLegs[1])
	}

	input.CurrentPositionVerifiable = false
	blocked, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertFindingCodes(t, blocked.Blockers, []ReasonCode{ReasonPositionUnverifiable})

	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("4.9")
	blocked, err = Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if blocked.Blocked() {
		t.Fatalf("close preview should use current exposure quantity without capital-derived exceeds blocker, got %v", blocked.Blockers)
	}
}

func TestSubmitStateGraphRejectsSkippingBeginSubmitAndIllegalStates(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	if err := exec.Apply(LegAccepted("gw-1", LegRoleLong, "order-1", now())); err == nil {
		t.Fatalf("expected leg acceptance before BeginSubmit to be illegal")
	}
	assertApply(t, exec, BeginSubmit(now()))
	assertState(t, exec, StateSubmitting)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	if err := exec.Apply(Event{Type: EventBeginSubmit, GatewayID: "second-submit", At: now()}); err == nil {
		t.Fatalf("expected different begin submit outside previewed to be illegal")
	}
	if err := exec.Apply(Event{Type: EventType("unknown")}); err == nil {
		t.Fatalf("expected unknown event rejected")
	}
}

func TestSecondLegRejectAllowsCompensationAndCompensationFillFailsExecution(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegRejectedCode("gw-2", ErrorInfo{Code: "REJECTED", SafeMessage: "rejected"}, now()))

	assertState(t, exec, StatePartiallyHedged)
	if exec.ManualIntervention {
		t.Fatalf("second leg rejection with exposure should not be manual terminal before compensation")
	}
	assertDecimal(t, exec.DirectionalExposure, "5")

	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegFilled("gw-c", decimal.RequireFromString("5"), now()))
	assertState(t, exec, StateFailed)
	if exec.DirectionalExposure.IsPositive() {
		t.Fatalf("compensation should neutralize new exposure, got %s", exec.DirectionalExposure)
	}
}

func TestRequestHashExcludesExecutionIDButChangesOnBusinessFields(t *testing.T) {
	preview := mustPreview(t)

	a := NewCommandPayload("exec-a", preview, "secret")
	b := NewCommandPayload("exec-b", preview, "secret")
	hashA, err := a.RequestHash("POST", "/api/executions")
	if err != nil {
		t.Fatalf("RequestHash returned error: %v", err)
	}
	hashB, err := b.RequestHash("POST", "/api/executions")
	if err != nil {
		t.Fatalf("RequestHash returned error: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("same logical request with different execution ids must hash equally: %s %s", hashA, hashB)
	}

	changed := preview
	changed.SingleLegNotional = decimal.RequireFromString("501")
	hashChanged, err := NewCommandPayload("exec-c", changed, "").RequestHash("POST", "/api/executions")
	if err != nil {
		t.Fatalf("RequestHash returned error: %v", err)
	}
	if hashA == hashChanged {
		t.Fatalf("business field change must alter request hash")
	}
}

func TestExecutionJSONRoundTripRestoresPlannedAndAppliedForRestartSafety(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	event := LegAccepted("gw-1", LegRoleLong, "order-1", now())
	assertApply(t, exec, event)

	body, err := json.Marshal(exec.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	restored, err := RestoreExecution(body)
	if err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}
	assertApply(t, restored, event)
	if len(restored.Legs) != 1 {
		t.Fatalf("duplicate event after restore should not duplicate leg: %#v", restored.Legs)
	}
	assertApply(t, restored, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, restored, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
}

func TestFillQuantityMustBePositiveCumulativeAndNotExceedRequested(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))

	if err := exec.Apply(LegFilled("gw-1", decimal.Zero, now())); err == nil {
		t.Fatalf("expected zero fill rejected")
	}
	if err := exec.Apply(LegFilled("gw-1", decimal.RequireFromString("5.001"), now())); err == nil {
		t.Fatalf("expected oversize cumulative fill rejected")
	}
	assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("2"), now()))
	assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("3"), now()))
	if len(exec.Fills) != 2 || !exec.Legs[0].Filled.Equal(decimal.RequireFromString("3")) {
		t.Fatalf("fills must be cumulative snapshots with deduped deltas, leg=%#v fills=%#v", exec.Legs[0], exec.Fills)
	}
	assertApply(t, exec, LegPartial("gw-1", decimal.RequireFromString("3"), now()))
	if len(exec.Fills) != 2 {
		t.Fatalf("duplicate cumulative fill must not append another fill: %#v", exec.Fills)
	}
}

func moveToPartialSecondLeg(t *testing.T, exec *Execution) {
	t.Helper()
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimal.RequireFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegPartial("gw-2", decimal.RequireFromString("2"), now()))
}

func TestRestoredBeginSubmitReplayIsNoOpBeforeStateGraphCheck(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	begin := BeginSubmit(now())
	assertApply(t, exec, begin)

	body, err := json.Marshal(exec.Snapshot())
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	restored, err := RestoreExecution(body)
	if err != nil {
		t.Fatalf("restore snapshot: %v", err)
	}

	assertApply(t, restored, begin)
	assertState(t, restored, StateSubmitting)
	if len(restored.AppliedEventIDs) != len(exec.AppliedEventIDs) {
		t.Fatalf("replayed begin_submit should not append applied event: %#v", restored.AppliedEventIDs)
	}
}

func TestUnresolvedCompensationUncertainCannotBeOverwrittenByOrdinaryFill(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)
	assertApply(t, exec, CompensationUncertain("gw-c", "write status unknown", LegRoleLong, decimal.RequireFromString("3"), now()))

	assertApply(t, exec, LegFilled("gw-2", decimal.RequireFromString("2.5"), now()))

	assertState(t, exec, StateUncertain)
	if !exec.ManualIntervention || exec.ManualInterventionReason != "compensation uncertain: gateway error" {
		t.Fatalf("unresolved compensation uncertainty must keep manual state, got manual=%v reason=%q", exec.ManualIntervention, exec.ManualInterventionReason)
	}
	if exec.Compensation == nil || exec.Compensation.Status != LegStatusUncertain || exec.Compensation.Error.SafeMessage != "gateway error" {
		t.Fatalf("compensation uncertainty details were overwritten: %#v", exec.Compensation)
	}
}

func TestAcceptedCompensationCanBecomeUncertainWithSameGatewayOrderIdentity(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	moveToPartialSecondLeg(t, exec)
	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimal.RequireFromString("3"), now()))

	assertApply(t, exec, CompensationUncertain("gw-c", "write status unknown", LegRoleLong, decimal.RequireFromString("3"), now()))

	assertState(t, exec, StateUncertain)
	if !exec.ManualIntervention || exec.Compensation == nil || !exec.Compensation.ManualRequired {
		t.Fatalf("expected accepted compensation updated to uncertain/manual: manual=%v comp=%#v", exec.ManualIntervention, exec.Compensation)
	}
	if exec.Compensation.Status != LegStatusUncertain || exec.Compensation.Error.SafeMessage != "gateway error" {
		t.Fatalf("expected compensation status/error updated: %#v", exec.Compensation)
	}
	if len(exec.Legs) != 3 {
		t.Fatalf("same compensation identity must update existing leg, got legs=%#v", exec.Legs)
	}
	if exec.Legs[2].Status != LegStatusUncertain || exec.Legs[2].LastError.SafeMessage != "gateway error" {
		t.Fatalf("expected existing compensation leg uncertain: %#v", exec.Legs[2])
	}

	if err := exec.Apply(CompensationUncertain("gw-other", "other write", LegRoleLong, decimal.RequireFromString("3"), now())); err == nil {
		t.Fatalf("different compensation identity must be rejected as second attempt")
	}
}
