package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNormalizedCommandHashIsStableAndDetectsBusinessConflicts(t *testing.T) {
	preview := mustPreview(t)

	commandA := NewCommandPayload("exec-1", preview, "client-secret-token")
	commandB := NewCommandPayload("exec-1", preview, "different-secret")

	hashA, err := commandA.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	hashB, err := commandB.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("same logical input must hash the same, got %s and %s", hashA, hashB)
	}
	encoded, err := json.Marshal(commandA.Normalized())
	if err != nil {
		t.Fatalf("marshal normalized command: %v", err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("normalized command leaked secret: %s", encoded)
	}

	changed := preview
	changed.SingleLegNotional = decimal.RequireFromString("251")
	commandC := NewCommandPayload("exec-1", changed, "")
	hashC, err := commandC.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if hashA == hashC {
		t.Fatalf("business field change must alter hash")
	}

	changedPosition := preview
	changedPosition.PositionID = "position-1"
	commandD := NewCommandPayload("exec-1", changedPosition, "")
	hashD, err := commandD.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if hashA == hashD {
		t.Fatalf("position id change must alter hash")
	}

	changedEconomics := preview
	changedEconomics.CapitalUSDT = decimal.RequireFromString("300")
	commandE := NewCommandPayload("exec-1", changedEconomics, "")
	hashE, err := commandE.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	if hashA == hashE {
		t.Fatalf("capital change must alter hash")
	}
}

func TestCommandHashAndExecutionRoundTripIncludePerLegVenueEnvironment(t *testing.T) {
	preview := mustPreview(t)
	if len(preview.PlannedLegs) != 2 ||
		preview.PlannedLegs[0].VenueEnvironment != "demo" ||
		preview.PlannedLegs[1].VenueEnvironment != "testnet" {
		t.Fatalf("mixed leg identity missing from preview: %+v", preview.PlannedLegs)
	}

	originalHash, err := NewCommandPayload("exec-identity", preview, "").Hash()
	if err != nil {
		t.Fatal(err)
	}
	changed := preview
	changed.PlannedLegs = append([]PlannedLeg(nil), preview.PlannedLegs...)
	changed.PlannedLegs[1].VenueEnvironment = "demo"
	changedHash, err := NewCommandPayload("exec-identity", changed, "").Hash()
	if err != nil {
		t.Fatal(err)
	}
	if originalHash == changedHash {
		t.Fatal("changing only one leg's native venue environment must change the command hash")
	}

	execution, err := NewExecution("exec-identity", preview)
	if err != nil {
		t.Fatal(err)
	}
	assertApply(t, execution, BeginSubmit(now()))
	assertApply(t, execution, LegAccepted("gateway-long", LegRoleLong, "order-long", now()))
	assertApply(t, execution, LegFilled("gateway-long", preview.PlannedLegs[0].Quantity, now()))
	assertApply(t, execution, LegAccepted("gateway-short", LegRoleShort, "order-short", now()))
	body, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Execution
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Legs) != 2 ||
		decoded.Legs[0].VenueEnvironment != "demo" ||
		decoded.Legs[1].VenueEnvironment != "testnet" {
		t.Fatalf("execution round trip lost native identity: %s", body)
	}
}

func TestNewExecutionCopiesPositionID(t *testing.T) {
	openPreview := mustPreview(t)
	openExec, err := NewExecution("exec-open", openPreview)
	if err != nil {
		t.Fatalf("NewExecution open returned error: %v", err)
	}
	if openPreview.PositionID != "" {
		t.Fatalf("open preview must not expose user supplied position id, got %q", openPreview.PositionID)
	}
	if openExec.PositionID != "position-exec-open" || openExec.LineageID != "exec-open" {
		t.Fatalf("open execution ids = position:%q lineage:%q", openExec.PositionID, openExec.LineageID)
	}

	input := validPreviewInput(now())
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimalFromString("5")
	input.CurrentShortQuantity = decimalFromString("5")
	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err := NewExecution("exec-close", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	if exec.PositionID != "position-1" || exec.LineageID != "exec-close" {
		t.Fatalf("execution ids = position:%q lineage:%q", exec.PositionID, exec.LineageID)
	}

	input.ParentExecutionID = "exec-open"
	input.LineageID = "lineage-open"
	preview, err = Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	exec, err = NewExecution("exec-close-2", preview)
	if err != nil {
		t.Fatalf("NewExecution returned error: %v", err)
	}
	if exec.ParentExecutionID != "exec-open" || exec.LineageID != "lineage-open" {
		t.Fatalf("close did not inherit lineage: parent=%q lineage=%q", exec.ParentExecutionID, exec.LineageID)
	}
}

func TestExecutionJSONRoundTripKeepsAPIDTOFields(t *testing.T) {
	exec := newPreviewedExecution(OperationOpen)
	assertApply(t, exec, BeginSubmit(now()))
	assertApply(t, exec, LegAccepted("gw-1", LegRoleLong, "order-1", now()))
	assertApply(t, exec, LegFilled("gw-1", decimalFromString("5"), now()))
	assertApply(t, exec, LegAccepted("gw-2", LegRoleShort, "order-2", now()))
	assertApply(t, exec, LegPartial("gw-2", decimalFromString("2"), now()))
	assertApply(t, exec, CompensationAccepted("gw-c", "comp-1", LegRoleLong, decimalFromString("3"), now()))

	body, err := json.Marshal(exec)
	if err != nil {
		t.Fatalf("marshal execution: %v", err)
	}
	for _, field := range []string{
		`"id":"exec-1"`,
		`"principalId":"user-1"`,
		`"mode":"test"`,
		`"operation":"open"`,
		`"opportunityId":"opp-1"`,
		`"state":"compensating"`,
		`"gatewayIds":["gw-1","gw-2","gw-c"]`,
		`"compensation"`,
		`"manualIntervention":false`,
		`"legs"`,
		`"fills"`,
	} {
		if !strings.Contains(string(body), field) {
			t.Fatalf("expected JSON field %s in %s", field, body)
		}
	}

	var decoded Execution
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal execution: %v", err)
	}
	if decoded.ID != exec.ID || decoded.State != exec.State || len(decoded.Legs) != len(exec.Legs) || len(decoded.Fills) != len(exec.Fills) {
		t.Fatalf("roundtrip lost execution fields: %#v", decoded)
	}
	if !decoded.CapitalUSDT.Equal(exec.CapitalUSDT) || decoded.Leverage != exec.Leverage ||
		!decoded.InitialLongQuantity.Equal(exec.InitialLongQuantity) ||
		!decoded.InitialShortQuantity.Equal(exec.InitialShortQuantity) {
		t.Fatalf("roundtrip lost immutable economics: %#v", decoded)
	}
}

func mustPreview(t *testing.T) PreviewResult {
	t.Helper()
	preview, err := Preview(validPreviewInput(now()))
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	return preview
}

func decimalFromString(value string) decimal.Decimal {
	return decimal.RequireFromString(value)
}
