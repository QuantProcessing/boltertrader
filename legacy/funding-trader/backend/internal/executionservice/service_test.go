package executionservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/shopspring/decimal"
)

var (
	testScope = Scope{PrincipalID: "principal-1", Mode: "test"}
	testNow   = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
)

func TestExecutionServiceRejectsNonProductModeScopeBeforeDependencies(t *testing.T) {
	store := newFakeStore()
	source := &fakeSource{now: testNow, riskVerifiable: true}
	gateway := &fakeGateway{}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	for _, mode := range []string{"", "demo", "testnet", "mainnet"} {
		t.Run(mode, func(t *testing.T) {
			scope := Scope{PrincipalID: "principal-1", Mode: mode}
			if _, err := service.Preview(context.Background(), scope, PreviewRequest{}); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("Preview error=%v, want ErrInvalidScope", err)
			}
			if _, err := service.Submit(context.Background(), scope, SubmitRequest{Operation: execution.OperationOpen}, "key", "request"); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("Submit error=%v, want ErrInvalidScope", err)
			}
			if _, err := service.Recover(context.Background(), scope, "request"); !errors.Is(err, ErrInvalidScope) {
				t.Fatalf("Recover error=%v, want ErrInvalidScope", err)
			}
		})
	}
	if source.calls != 0 || len(gateway.places) != 0 {
		t.Fatalf("invalid scope reached dependencies: sourceCalls=%d gatewayWrites=%d", source.calls, len(gateway.places))
	}
}

func TestPreviewUsesFreshSourceEveryTimeAndPersistsRiskWarning(t *testing.T) {
	source := &fakeSource{now: testNow, riskVerifiable: false}
	store := newFakeStore()
	service := newTestService(t, store, source, &fakeGateway{}, fixedClock{at: testNow}, true)

	first, err := service.Preview(context.Background(), testScope, previewRequest("preview-1"))
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	second, err := service.Preview(context.Background(), testScope, previewRequest("preview-2"))
	if err != nil {
		t.Fatalf("preview again: %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("fresh source calls = %d, want 2", source.calls)
	}
	if !first.CanSubmit || len(first.Blockers) != 0 {
		t.Fatalf("Gateway V1 risk-limit gap must not block user-controlled risk: canSubmit=%v blockers=%v", first.CanSubmit, first.Blockers)
	}
	if len(first.Warnings) != 1 || first.Warnings[0].Code != execution.ReasonRiskUnverifiable {
		t.Fatalf("risk warning not visible: warnings=%v", first.Warnings)
	}
	if second.PreviewID != "preview-2" {
		t.Fatalf("second preview id = %q", second.PreviewID)
	}
	if len(store.previews) != 2 {
		t.Fatalf("persisted previews = %d, want 2", len(store.previews))
	}
}

func TestSubmitFullSuccessPersistsBeginBeforeGatewayAndPlacesLongThenShort(t *testing.T) {
	log := []string{}
	store := newFakeStore()
	store.log = &log
	gateway := &fakeGateway{log: &log}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged", result.Execution.State)
	}
	wantLog := []string{"persist:begin_submit", "place:long:exec-1:long", "persist:leg_accepted", "persist:leg_fill", "place:short:exec-1:short"}
	for i, want := range wantLog {
		if i >= len(log) || log[i] != want {
			t.Fatalf("log[%d] = %q, want %q; full log=%v", i, logAt(log, i), want, log)
		}
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want 2", len(gateway.places))
	}
	if gateway.places[0].Role != execution.LegRoleLong || gateway.places[1].Role != execution.LegRoleShort {
		t.Fatalf("roles = %s,%s, want long,short", gateway.places[0].Role, gateway.places[1].Role)
	}
	if gateway.places[0].IdempotencyKey == gateway.places[1].IdempotencyKey {
		t.Fatalf("leg idempotency keys must be distinct")
	}
}

func TestSubmitEvaluatesStoredPreviewExpiryAtRequestReceiptBeforeSlowFreshValidation(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	clock := &mutableClock{at: testNow}
	service := newTestService(t, store, source, gateway, clock, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	request := submitRequest("preview-1")
	request.ReceivedAt = preview.ExpiresAt.Add(-time.Second)
	clock.at = preview.ExpiresAt.Add(90 * time.Second)
	source.now = clock.at

	result, err := service.Submit(context.Background(), testScope, request, "idem-1", "request-1")
	if err != nil {
		t.Fatalf("timely submit after slow fresh validation: %v", err)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged", result.Execution.State)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want 2", len(gateway.places))
	}
}

func TestSubmitRejectsPreviewAlreadyExpiredWhenRequestWasReceived(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	clock := &mutableClock{at: testNow}
	service := newTestService(t, store, source, gateway, clock, true)
	preview := savePreview(t, service, "preview-1")

	request := submitRequest("preview-1")
	request.ReceivedAt = preview.ExpiresAt
	clock.at = preview.ExpiresAt.Add(90 * time.Second)
	source.now = clock.at

	_, err := service.Submit(context.Background(), testScope, request, "idem-1", "request-1")
	if !errors.Is(err, ErrPreviewExpired) {
		t.Fatalf("submit error = %v, want preview expired", err)
	}
	if len(gateway.places) != 0 || len(store.execs) != 0 {
		t.Fatalf("expired preview wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
	}
	if source.calls != 1 {
		t.Fatalf("fresh source calls = %d, want no submit revalidation for an already expired preview", source.calls)
	}
}

func TestSubmitObservesAcceptedLegByRealGatewayOrderIDButPersistsLogicalGatewayID(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity

	gateway.acceptFullByLookup("exec-1:long", "real-order-long", "real-order-long", quantity)
	gateway.acceptFullByLookup("exec-1:short", "real-order-short", "real-order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged", result.Execution.State)
	}
	if len(gateway.gets) < 2 || gateway.gets[0] != "real-order-long" || gateway.gets[1] != "real-order-short" {
		t.Fatalf("order observations used wrong lookup ids: gets=%v", gateway.gets)
	}
	if len(gateway.fillsCalls) < 2 || gateway.fillsCalls[0] != "real-order-long" || gateway.fillsCalls[1] != "real-order-short" {
		t.Fatalf("fill observations used wrong lookup ids: fills=%v", gateway.fillsCalls)
	}
	var longLeg execution.Leg
	for _, leg := range result.Execution.Legs {
		if leg.Role == execution.LegRoleLong {
			longLeg = leg
			break
		}
	}
	if longLeg.GatewayID != "exec-1:long" || longLeg.OrderID != "real-order-long" || longLeg.GatewayOrderID != "real-order-long" {
		t.Fatalf("leg identity not split into logical gateway id + real order id: %+v", longLeg)
	}
	if !longLeg.Filled.Equal(quantity) {
		t.Fatalf("fills from real order id lookup were not applied to logical leg: %+v", longLeg)
	}
}

func TestSubmitAcceptedPlacementReconcileUncertainPersistsRealOrderIDAndStopsBeforeSecondLeg(t *testing.T) {
	for _, tc := range []struct {
		name    string
		getErr  error
		fillErr error
	}{
		{name: "get order error", getErr: ErrServiceUnavailable},
		{name: "fills error", fillErr: ErrServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			gateway := &fakeGateway{getErr: tc.getErr, fillsErr: tc.fillErr}
			source := &fakeSource{now: testNow, riskVerifiable: true}
			service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
			savePreview(t, service, "preview-1")
			gateway.acceptWorking("exec-1:long", "real-order-long")

			result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if result.Execution.State != execution.StateUncertain {
				t.Fatalf("state = %s, want uncertain", result.Execution.State)
			}
			if len(gateway.places) != 1 || gateway.places[0].Role != execution.LegRoleLong {
				t.Fatalf("gateway places = %#v, want only first leg", gateway.places)
			}
			if len(gateway.gets) == 0 || gateway.gets[0] != "real-order-long" {
				t.Fatalf("GetOrder lookups = %v, want real order id", gateway.gets)
			}
			if tc.fillErr != nil && (len(gateway.fillsCalls) == 0 || gateway.fillsCalls[0] != "real-order-long") {
				t.Fatalf("Fills lookups = %v, want real order id", gateway.fillsCalls)
			}
			if len(result.Execution.Legs) != 1 {
				t.Fatalf("legs = %#v, want one uncertain long leg", result.Execution.Legs)
			}
			longLeg := result.Execution.Legs[0]
			if longLeg.Role != execution.LegRoleLong || longLeg.Status != execution.LegStatusUncertain ||
				longLeg.GatewayID != "exec-1:long" || longLeg.OrderID != "real-order-long" ||
				longLeg.GatewayOrderID != "real-order-long" {
				t.Fatalf("uncertain long leg lost real identity: %#v", longLeg)
			}
		})
	}
}

func TestSubmitAcceptedPlacementWithoutOrderIDKeepsObservedOrderIDWhenFillsFail(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{fillsErr: ErrServiceUnavailable}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.ensure()
	gateway.outcomes["exec-1:long"] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: "exec-1:long"}
	gateway.orders["exec-1:long"] = OrderObservation{Found: true, GatewayID: "exec-1:long", OrderID: "real-order-long", Status: execution.LegStatusWorking}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateUncertain {
		t.Fatalf("state = %s, want uncertain", result.Execution.State)
	}
	if len(gateway.places) != 1 || gateway.places[0].Role != execution.LegRoleLong {
		t.Fatalf("gateway places = %#v, want only first leg", gateway.places)
	}
	if len(gateway.gets) == 0 || gateway.gets[0] != "exec-1:long" {
		t.Fatalf("GetOrder lookups = %v, want logical id when placement omitted order id", gateway.gets)
	}
	if len(gateway.fillsCalls) == 0 || gateway.fillsCalls[0] != "exec-1:long" {
		t.Fatalf("Fills lookups = %v, want same lookup attempted before uncertainty", gateway.fillsCalls)
	}
	if len(result.Execution.Legs) != 1 {
		t.Fatalf("legs = %#v, want one uncertain long leg", result.Execution.Legs)
	}
	longLeg := result.Execution.Legs[0]
	if longLeg.Role != execution.LegRoleLong || longLeg.Status != execution.LegStatusUncertain ||
		longLeg.GatewayID != "exec-1:long" || longLeg.OrderID != "real-order-long" ||
		longLeg.GatewayOrderID != "real-order-long" {
		t.Fatalf("uncertain long leg lost observed identity: %#v", longLeg)
	}
}

func TestSubmitRejectsPerLegVenueEnvironmentDriftBeforeGatewayWrite(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)

	savePreview(t, service, "preview-1")
	source.shortVenueEnvironment = "demo"

	_, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("submit error = %v, want preview mismatch", err)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("gateway writes = %d, want 0", len(gateway.places))
	}
	if len(store.execs) != 0 {
		t.Fatalf("persisted executions = %d, want 0", len(store.execs))
	}
}

func TestSubmitAllowsSafeFreshMarkEconomicsAndSnapshotDriftButExecutesStoredPreview(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	source.snapshotID = "snapshot-route-fresh"
	source.grossAprPct = decimal.NewFromInt(730)
	source.capacityNotional = decimal.NewFromInt(20000)
	source.longMarkPrice = decimal.RequireFromString("99.9")
	source.shortMarkPrice = decimal.RequireFromString("99.9")
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit with safe drift: %v", err)
	}
	if !result.Execution.ExpectedNetAprPct.Equal(preview.ExpectedNetAprPct) {
		t.Fatalf("execution economics drifted to fresh value: got=%s stored=%s",
			result.Execution.ExpectedNetAprPct, preview.ExpectedNetAprPct)
	}
	if len(result.Execution.PlannedLegs) != 2 ||
		!result.Execution.PlannedLegs[0].MarkPrice.Equal(preview.PlannedLegs[0].MarkPrice) ||
		!result.Execution.PlannedLegs[1].MarkPrice.Equal(preview.PlannedLegs[1].MarkPrice) {
		t.Fatalf("execution used fresh planned marks: execution=%+v stored=%+v",
			result.Execution.PlannedLegs, preview.PlannedLegs)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway writes=%d want 2", len(gateway.places))
	}
	if !gateway.places[0].Quantity.Equal(preview.PlannedLegs[0].Quantity) ||
		!gateway.places[1].Quantity.Equal(preview.PlannedLegs[1].Quantity) {
		t.Fatalf("gateway used fresh quantities: places=%+v stored=%+v", gateway.places, preview.PlannedLegs)
	}
}

func TestSubmitRejectsAdverseFreshMarkDriftBeforeGatewayWrite(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	source.longMarkPrice = decimal.RequireFromString("100.3")
	source.shortMarkPrice = decimal.RequireFromString("100.3")

	_, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("submit error=%v want preview mismatch", err)
	}
	if len(gateway.places) != 0 || len(store.execs) != 0 {
		t.Fatalf("adverse drift wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
	}
}

func TestSubmitRejectsFreshQuantityRouteEnvironmentAndBlockerChanges(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*fakeSource)
		wantErr error
	}{
		{
			name: "quantity",
			mutate: func(source *fakeSource) {
				source.longMarkPrice = decimal.NewFromInt(101)
				source.shortMarkPrice = decimal.NewFromInt(101)
			},
			wantErr: ErrPreviewMismatch,
		},
		{
			name: "route",
			mutate: func(source *fakeSource) {
				source.longAccountID = "other-long-account"
			},
			wantErr: ErrPreviewMismatch,
		},
		{
			name: "environment",
			mutate: func(source *fakeSource) {
				source.shortVenueEnvironment = "demo"
			},
			wantErr: ErrPreviewMismatch,
		},
		{
			name: "blocker",
			mutate: func(source *fakeSource) {
				source.originalBlockers = []execution.Finding{{Code: execution.ReasonOriginalBlocker, Message: "fresh route blocked"}}
			},
			wantErr: ErrBlockedPreview,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			gateway := &fakeGateway{}
			source := &fakeSource{now: testNow, riskVerifiable: true}
			service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
			savePreview(t, service, "preview-1")
			tc.mutate(source)

			_, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("submit error=%v want %v", err, tc.wantErr)
			}
			if len(gateway.places) != 0 || len(store.execs) != 0 {
				t.Fatalf("unsafe fresh change wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
			}
		})
	}
}

func TestSubmitRejectsTamperedStoredPreviewHashBeforeGatewayWrite(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	key := testScope.PrincipalID + "/" + testScope.Mode + "/preview-1"
	stored := store.previews[key]
	stored.Preview.CapitalUSDT = decimal.NewFromInt(200)
	store.previews[key] = stored

	_, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("submit error=%v want preview mismatch", err)
	}
	if len(gateway.places) != 0 || len(store.execs) != 0 {
		t.Fatalf("tampered preview wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
	}
}

func TestSubmitRejectsStoredBlockedPreviewAndNewFreshWarnings(t *testing.T) {
	t.Run("stored blocked", func(t *testing.T) {
		store := newFakeStore()
		gateway := &fakeGateway{}
		source := &fakeSource{now: testNow, riskVerifiable: true}
		service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
		preview, err := service.Preview(context.Background(), testScope, previewRequest("preview-1"))
		if err != nil {
			t.Fatalf("preview: %v", err)
		}
		key := testScope.PrincipalID + "/" + testScope.Mode + "/preview-1"
		preview.Blockers = append(preview.Blockers, execution.Finding{Code: execution.ReasonOriginalBlocker, Message: "stored blocked"})
		preview.CanSubmit = false
		hash, err := stablePreviewHash(preview)
		if err != nil {
			t.Fatalf("hash: %v", err)
		}
		store.previews[key] = StoredPreview{Preview: preview, RequestHash: hash}

		_, err = service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
		if !errors.Is(err, ErrBlockedPreview) {
			t.Fatalf("submit error=%v want blocked preview", err)
		}
		if len(gateway.places) != 0 || len(store.execs) != 0 {
			t.Fatalf("stored blocked preview wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
		}
	})

	t.Run("new fresh warning", func(t *testing.T) {
		store := newFakeStore()
		gateway := &fakeGateway{}
		source := &fakeSource{now: testNow, riskVerifiable: true}
		service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
		savePreview(t, service, "preview-1")
		source.recentReversal = true

		_, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
		if !errors.Is(err, ErrPreviewMismatch) {
			t.Fatalf("submit error=%v want preview mismatch", err)
		}
		if len(gateway.places) != 0 || len(store.execs) != 0 {
			t.Fatalf("new warning wrote state: places=%d execs=%d", len(gateway.places), len(store.execs))
		}
	})
}

func TestSubmitAndPrivateEventRecoveryAreSerialized(t *testing.T) {
	store := newFakeStore()
	store.listEntered = make(chan struct{}, 1)
	gateway := &fakeGateway{
		placeEntered: make(chan struct{}),
		placeBlock:   make(chan struct{}),
	}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-serialized")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	submitDone := make(chan error, 1)
	go func() {
		_, err := service.Submit(
			context.Background(),
			testScope,
			submitRequest("preview-serialized"),
			"idem-serialized",
			"request-submit",
		)
		submitDone <- err
	}()
	<-gateway.placeEntered

	recoverDone := make(chan error, 1)
	go func() {
		_, err := service.Recover(context.Background(), testScope, "request-recover")
		recoverDone <- err
	}()
	select {
	case <-store.listEntered:
		t.Fatal("recovery entered persistence while submit still owned execution coordination")
	case <-time.After(25 * time.Millisecond):
	}

	close(gateway.placeBlock)
	if err := <-submitDone; err != nil {
		t.Fatalf("submit: %v", err)
	}
	select {
	case <-store.listEntered:
	case <-time.After(time.Second):
		t.Fatal("recovery did not run after submit completed")
	}
	if err := <-recoverDone; err != nil {
		t.Fatalf("recover: %v", err)
	}
}

func TestSubmitIdempotencyExactReplayAndConflict(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	first, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}
	placesAfterFirst := len(gateway.places)
	replay, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-2")
	if err != nil {
		t.Fatalf("replay submit: %v", err)
	}
	if !replay.Replayed || replay.Execution.ID != first.Execution.ID {
		t.Fatalf("replay = %+v, first id=%s", replay, first.Execution.ID)
	}
	if string(replay.ResponseBody) != string(first.ResponseBody) {
		t.Fatalf("replay body changed: first=%s replay=%s", first.ResponseBody, replay.ResponseBody)
	}
	var firstResponse execution.Execution
	if err := json.Unmarshal(first.ResponseBody, &firstResponse); err != nil {
		t.Fatalf("decode persisted first response: %v", err)
	}
	if firstResponse.ID != first.Execution.ID || firstResponse.State != first.Execution.State {
		t.Fatalf("persisted first response=%+v execution=%+v", firstResponse, first.Execution)
	}
	if len(gateway.places) != placesAfterFirst {
		t.Fatalf("replay wrote gateway orders: before=%d after=%d", placesAfterFirst, len(gateway.places))
	}
	source.snapshotID = "changed-snapshot"
	replayAfterMarketChange, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-3")
	if err != nil {
		t.Fatalf("changed-source replay submit: %v", err)
	}
	if !replayAfterMarketChange.Replayed || replayAfterMarketChange.Execution.ID != first.Execution.ID {
		t.Fatalf("changed-source replay = %+v, first id=%s", replayAfterMarketChange, first.Execution.ID)
	}
	savePreview(t, service, "preview-2")
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-2"), "idem-1", "request-4"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error = %v", err)
	}
	if _, ok := store.records[testScope.PrincipalID+"/"+testScope.Mode+"/POST//api/v1/orders/idem-1"]; !ok {
		t.Fatalf("idempotency was not recorded under POST /api/v1/orders")
	}
}

func TestCloseIdempotencyIsIsolatedByPositionPath(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	closePreview := savePreviewRequest(t, service, closePreviewRequest("preview-close", "position-1"))
	quantity := closePreview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-close-long", quantity)
	gateway.acceptFull("exec-1:short", "order-close-short", quantity)

	closeResult, err := service.Close(context.Background(), testScope, closeSubmitRequest("preview-close", "position-1"), "same-key", "request-close")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closeResult.Execution.PositionID != "position-1" {
		t.Fatalf("close execution position = %q", closeResult.Execution.PositionID)
	}
	if _, ok := store.records[testScope.PrincipalID+"/"+testScope.Mode+"/POST//api/v1/positions/position-1/close/same-key"]; !ok {
		t.Fatalf("close idempotency was not recorded under position close path")
	}

	openPreview := savePreview(t, service, "preview-open")
	openQuantity := openPreview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-open-long", openQuantity)
	gateway.acceptFull("exec-1:short", "order-open-short", openQuantity)
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-open"), "same-key", "request-open"); err != nil {
		t.Fatalf("open with same key should be isolated from close path: %v", err)
	}

	otherClosePreview := savePreviewRequest(t, service, closePreviewRequest("preview-close-2", "position/2"))
	otherQuantity := otherClosePreview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-close2-long", otherQuantity)
	gateway.acceptFull("exec-1:short", "order-close2-short", otherQuantity)
	if _, err := service.Close(context.Background(), testScope, closeSubmitRequest("preview-close-2", "position/2"), "same-key", "request-close-2"); err != nil {
		t.Fatalf("different position close with same key should be isolated: %v", err)
	}
	if _, ok := store.records[testScope.PrincipalID+"/"+testScope.Mode+"/POST//api/v1/positions/position%2F2/close/same-key"]; !ok {
		t.Fatalf("close path did not escape position id")
	}
}

func TestCloseIdempotencyConflictsWithinSamePosition(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreviewRequest(t, service, closePreviewRequest("preview-close", "position-1"))
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-close-long", quantity)
	gateway.acceptFull("exec-1:short", "order-close-short", quantity)
	if _, err := service.Close(context.Background(), testScope, closeSubmitRequest("preview-close", "position-1"), "same-key", "request-close"); err != nil {
		t.Fatalf("close: %v", err)
	}
	savePreviewRequest(t, service, closePreviewRequest("preview-close-other", "position-1"))
	if _, err := service.Close(context.Background(), testScope, closeSubmitRequest("preview-close-other", "position-1"), "same-key", "request-close-conflict"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("same position close conflict error = %v", err)
	}
}

func TestCloseRequestWithoutEconomicsPreservesSourceEconomicsAndReplays(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{
		now: testNow, riskVerifiable: true,
		capitalUSDT: decimal.NewFromInt(250), leverage: 2,
	}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	previewRequest := closePreviewRequestWithoutEconomics("preview-close", "position-1")
	closePreview := savePreviewRequest(t, service, previewRequest)
	if !closePreview.CapitalUSDT.Equal(decimal.NewFromInt(250)) || closePreview.Leverage != 2 {
		t.Fatalf("preview lost source economics: capital=%s leverage=%d", closePreview.CapitalUSDT, closePreview.Leverage)
	}
	quantity := closePreview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-close-long", quantity)
	gateway.acceptFull("exec-1:short", "order-close-short", quantity)

	request := closeSubmitRequestWithoutEconomics("preview-close", "position-1")
	result, err := service.Close(context.Background(), testScope, request, "same-key", "request-close")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if !result.Execution.CapitalUSDT.Equal(decimal.NewFromInt(250)) || result.Execution.Leverage != 2 {
		t.Fatalf("execution lost source economics: capital=%s leverage=%d", result.Execution.CapitalUSDT, result.Execution.Leverage)
	}
	writes := len(gateway.places)
	replay, err := service.Close(context.Background(), testScope, request, "same-key", "request-replay")
	if err != nil {
		t.Fatalf("replay close: %v", err)
	}
	if !replay.Replayed || replay.Execution.ID != result.Execution.ID {
		t.Fatalf("replay result = %+v, original=%s", replay, result.Execution.ID)
	}
	if len(gateway.places) != writes {
		t.Fatalf("replay duplicated gateway writes: before=%d after=%d", writes, len(gateway.places))
	}
}

func TestCloseWithoutExplicitPreviewCreatesFreshPreviewAndReplaysBeforeRefreshing(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{
		now: testNow, riskVerifiable: true,
		capitalUSDT: decimal.NewFromInt(250), leverage: 2,
	}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	gateway.acceptFull("exec-1:long", "order-close-long", decimal.NewFromInt(10))
	gateway.acceptFull("exec-1:short", "order-close-short", decimal.NewFromInt(10))
	request := SubmitRequest{Operation: execution.OperationClose, PositionID: "position-1"}

	result, err := service.Close(context.Background(), testScope, request, "same-key", "request-close")
	if err != nil {
		t.Fatalf("close without explicit preview: %v", err)
	}
	if result.Execution.PositionID != "position-1" ||
		!result.Execution.CapitalUSDT.Equal(decimal.NewFromInt(250)) ||
		result.Execution.Leverage != 2 {
		t.Fatalf("close lost source identity/economics: %+v", result.Execution)
	}
	if source.calls != 1 {
		t.Fatalf("fresh preview calls=%d, want 1", source.calls)
	}
	if len(store.previews) != 1 {
		t.Fatalf("auto-saved previews=%d, want 1", len(store.previews))
	}
	writes := len(gateway.places)

	source.snapshotID = "market-changed-after-first-response"
	replay, err := service.Close(context.Background(), testScope, request, "same-key", "request-replay")
	if err != nil {
		t.Fatalf("close replay: %v", err)
	}
	if !replay.Replayed || replay.Execution.ID != result.Execution.ID {
		t.Fatalf("close replay=%+v, original=%s", replay, result.Execution.ID)
	}
	if source.calls != 1 {
		t.Fatalf("replay refreshed current state: calls=%d", source.calls)
	}
	if len(gateway.places) != writes {
		t.Fatalf("replay duplicated gateway writes: before=%d after=%d", writes, len(gateway.places))
	}
}

func TestFirstPartialCancelsRemainderAndAttemptsBoundedCompensation(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.acceptPartial("exec-1:long", "order-long", decimal.RequireFromString("0.4"))
	gateway.acceptFull("exec-1:compensation", "order-comp", decimal.RequireFromString("0.4"))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want first leg + compensation", len(gateway.places))
	}
	if gateway.places[1].Role != execution.LegRoleLong || !gateway.places[1].ReduceOnly {
		t.Fatalf("compensation request = %+v", gateway.places[1])
	}
	if !gateway.places[1].Quantity.Equal(decimal.RequireFromString("0.4")) {
		t.Fatalf("compensation quantity = %s, want 0.4", gateway.places[1].Quantity)
	}
	if result.Execution.Compensation == nil || !result.Execution.Compensation.Attempted {
		t.Fatalf("compensation not recorded: %+v", result.Execution.Compensation)
	}
}

func TestSecondRejectTriggersSingleReduceOnlyCompensation(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.reject("exec-1:short", "order-short")
	gateway.acceptFull("exec-1:compensation", "order-comp", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.places) != 3 {
		t.Fatalf("gateway places = %d, want long, short, compensation", len(gateway.places))
	}
	if gateway.places[2].GatewayID != "exec-1:compensation" || !gateway.places[2].ReduceOnly {
		t.Fatalf("compensation place = %+v", gateway.places[2])
	}
	if result.Execution.Compensation == nil || !result.Execution.Compensation.Attempted {
		t.Fatalf("compensation missing: %+v", result.Execution.Compensation)
	}
	var second execution.Leg
	for _, leg := range result.Execution.Legs {
		if leg.Role == execution.LegRoleShort {
			second = leg
		}
	}
	if second.OrderID != "order-short" || second.GatewayOrderID != "order-short" {
		t.Fatalf("pre-accept rejection did not preserve order id: %+v", second)
	}
}

func TestSecondPartialCancelsRemainderBeforeCompensation(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptPartial("exec-1:short", "order-short", decimal.RequireFromString("0.4"))
	gateway.acceptFull("exec-1:compensation", "order-comp", decimal.RequireFromString("0.6"))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 1 || gateway.cancels[0].GatewayID != "order-short" {
		t.Fatalf("second partial remainder was not canceled: %+v", gateway.cancels)
	}
	if result.Execution.Compensation == nil || !result.Execution.Compensation.Quantity.Equal(decimal.RequireFromString("0.6")) {
		t.Fatalf("compensation not bounded to residual exposure: %+v", result.Execution.Compensation)
	}
}

func TestSecondPartialCancelsRemainderByRealGatewayOrderIDAndKeepsLogicalLegID(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "real-order-long", quantity)
	gateway.acceptPartialByLookup("exec-1:short", "real-order-short", "real-order-short", decimal.RequireFromString("0.4"))
	gateway.acceptFull("exec-1:compensation", "real-order-comp", decimal.RequireFromString("0.6"))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 1 || gateway.cancels[0].GatewayID != "real-order-short" {
		t.Fatalf("cancel did not use real gateway order id: %+v", gateway.cancels)
	}
	var shortLeg execution.Leg
	for _, leg := range result.Execution.Legs {
		if leg.Role == execution.LegRoleShort {
			shortLeg = leg
			break
		}
	}
	if shortLeg.GatewayID != "exec-1:short" || shortLeg.GatewayOrderID != "real-order-short" || shortLeg.Status != execution.LegStatusCanceled {
		t.Fatalf("short leg did not retain logical id while canceling real order: %+v", shortLeg)
	}
}

func TestFilledObservationWithoutFillsUsesPlannedQuantityAndContinuesToSecondLeg(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFullWithoutFills("exec-1:long", "order-long")
	gateway.acceptFullWithoutFills("exec-1:short", "order-short")

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged", result.Execution.State)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want both legs despite empty fills", len(gateway.places))
	}
	if len(gateway.cancels) != 0 {
		t.Fatalf("filled order with empty fills must not be canceled: %+v", gateway.cancels)
	}
	for _, leg := range result.Execution.Legs {
		if leg.Status != execution.LegStatusFilled || !leg.Filled.Equal(quantity) {
			t.Fatalf("leg = %+v, want filled with planned quantity %s", leg, quantity)
		}
	}
}

func TestFilledObservationWithShortFillsStopsUncertainWithoutSecondLeg(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	planned := preview.PlannedLegs[0].Quantity
	visible := planned.Div(decimal.NewFromInt(2))
	gateway.acceptFull("exec-1:long", "order-long", visible)
	gateway.acceptFull("exec-1:short", "order-short", planned)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateUncertain {
		t.Fatalf("state = %s, want uncertain", result.Execution.State)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("gateway places = %d, want no second leg with incomplete fill evidence", len(gateway.places))
	}
	if len(result.Execution.Legs) != 1 || result.Execution.Legs[0].Status != execution.LegStatusUncertain {
		t.Fatalf("leg = %+v, want uncertain first leg", result.Execution.Legs)
	}
	if !result.Execution.Legs[0].Filled.IsZero() {
		t.Fatalf("short visible fills must not be persisted as filled quantity: %+v", result.Execution.Legs[0])
	}
}

func TestPartialObservationWithoutFillsStopsUncertainWithoutGuessingQuantity(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.acceptPartialWithoutFills("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", decimal.NewFromInt(1))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateUncertain {
		t.Fatalf("state = %s, want uncertain", result.Execution.State)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("gateway places = %d, want no second leg when partial quantity is unknown", len(gateway.places))
	}
	if len(result.Execution.Legs) != 1 || !result.Execution.Legs[0].Filled.IsZero() {
		t.Fatalf("partial without fills guessed quantity: %+v", result.Execution.Legs)
	}
}

func TestAcceptedOrderNotObservedAfterAcceptanceIsUncertainWithoutFakeCancel(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.acceptFull("exec-1:short", "order-short", decimal.NewFromInt(1))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateUncertain {
		t.Fatalf("state = %s, want uncertain", result.Execution.State)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("gateway places = %d, want no second leg after unobserved accepted order", len(gateway.places))
	}
	if len(gateway.cancels) != 0 {
		t.Fatalf("unobserved accepted order must not be fake-canceled: %+v", gateway.cancels)
	}
	if len(result.Execution.Legs) != 1 || result.Execution.Legs[0].LastError.Code != "ORDER_NOT_OBSERVED_AFTER_ACCEPTANCE" {
		t.Fatalf("leg = %+v, want ORDER_NOT_OBSERVED_AFTER_ACCEPTANCE uncertainty", result.Execution.Legs)
	}
}

func TestCancelAcceptedReobservesAndAppliesFilledOrderInsteadOfCanceled(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptWorking("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", quantity)
	gateway.getHook = func(g *fakeGateway, gatewayID string) {
		if gatewayID == "order-long" && len(g.cancels) > 0 {
			g.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: "order-long", Status: execution.LegStatusFilled}
			g.fills[gatewayID] = []FillObservation{{FillID: "fill-order-long", GatewayFillID: "gfill-order-long", Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
		}
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 1 {
		t.Fatalf("gateway cancels = %d, want one attempted cancel", len(gateway.cancels))
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want second leg after cancel re-observed filled first leg", len(gateway.places))
	}
	var longLeg execution.Leg
	for _, leg := range result.Execution.Legs {
		if leg.Role == execution.LegRoleLong {
			longLeg = leg
		}
	}
	if longLeg.Status != execution.LegStatusFilled || !longLeg.Filled.Equal(quantity) {
		t.Fatalf("long leg after cancel re-observe = %+v, want filled quantity %s", longLeg, quantity)
	}
}

func TestAcceptedWorkingMarketOrderIsReobservedBeforeCancelingRemainder(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptWorking("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", quantity)
	observations := 0
	gateway.getHook = func(g *fakeGateway, gatewayID string) {
		if gatewayID != "order-long" {
			return
		}
		observations++
		if observations < 2 {
			return
		}
		g.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: "order-long", Status: execution.LegStatusFilled}
		g.fills[gatewayID] = []FillObservation{{FillID: "fill-order-long", GatewayFillID: "gfill-order-long", Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 0 {
		t.Fatalf("market order was canceled before re-observing fill: %+v", gateway.cancels)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway places = %d, want short leg after re-observed fill", len(gateway.places))
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged after re-observed fill", result.Execution.State)
	}
}

func TestAcceptedWorkingMarketOrderWaitsForLaterFillBeforeCancelingRemainder(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptWorking("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", quantity)
	observations := 0
	gateway.getHook = func(g *fakeGateway, gatewayID string) {
		if gatewayID != "order-long" {
			return
		}
		observations++
		if observations < 3 {
			return
		}
		g.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: "order-long", Status: execution.LegStatusFilled}
		g.fills[gatewayID] = []FillObservation{{FillID: "fill-order-long", GatewayFillID: "gfill-order-long", Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 0 {
		t.Fatalf("market order was canceled before bounded re-observation found its fill: %+v", gateway.cancels)
	}
	if observations < 3 {
		t.Fatalf("gateway observations = %d, want at least three before cancel", observations)
	}
	if len(gateway.places) != 2 || result.Execution.State != execution.StateHedged {
		t.Fatalf("later visible fill did not continue the hedge: places=%d state=%s", len(gateway.places), result.Execution.State)
	}
}

func TestCancelRejectedReobservesFilledMarketOrderBeforeManualIntervention(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{
		cancelResult: GatewayOrderResult{
			Outcome: GatewayRejected,
			Error:   execution.ErrorInfo{Code: "GATEWAY_WRITE_REJECTED", SafeMessage: "gateway write is not permitted"},
		},
	}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptWorking("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", quantity)
	gateway.getHook = func(g *fakeGateway, gatewayID string) {
		if gatewayID != "order-long" || len(g.cancels) == 0 {
			return
		}
		g.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: "order-long", Status: execution.LegStatusFilled}
		g.fills[gatewayID] = []FillObservation{{FillID: "fill-order-long", GatewayFillID: "gfill-order-long", Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.cancels) != 1 {
		t.Fatalf("gateway cancels = %d, want one attempted cancel", len(gateway.cancels))
	}
	if result.Execution.ManualIntervention || result.Execution.State != execution.StateHedged {
		t.Fatalf("execution did not recover filled order after cancel rejection: state=%s manual=%v reason=%s", result.Execution.State, result.Execution.ManualIntervention, result.Execution.ManualInterventionReason)
	}
}

func TestCancelFailureBlocksCompensationAndLeavesUncertainManual(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{cancelErr: errors.New("cancel uncertain")}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.acceptPartial("exec-1:long", "order-long", decimal.RequireFromString("0.4"))
	gateway.acceptFull("exec-1:compensation", "order-comp", decimal.RequireFromString("0.4"))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("compensation proceeded after cancel failure: places=%+v", gateway.places)
	}
	if result.Execution.State != execution.StateManualInterventionRequired || !result.Execution.ManualIntervention {
		t.Fatalf("cancel failure state = %s manual=%v", result.Execution.State, result.Execution.ManualIntervention)
	}
}

func TestAmbiguousLegDoesNotRetryWithDistinctGatewayKey(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.ambiguous("exec-1:long")

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Execution.State != execution.StateUncertain {
		t.Fatalf("state = %s, want uncertain", result.Execution.State)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("ambiguous submit places = %d, want one stable-key write", len(gateway.places))
	}
	if gateway.places[0].IdempotencyKey != "exec-1:long" {
		t.Fatalf("idempotency key = %q", gateway.places[0].IdempotencyKey)
	}
}

func TestAmbiguousLegPersistsSpecificSafeGatewayError(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.ensure()
	gateway.outcomes["exec-1:long"] = GatewayOrderResult{
		Outcome:   GatewayAmbiguous,
		GatewayID: "exec-1:long",
		Error: execution.ErrorInfo{
			Code:        "gateway_upstream_timeout",
			SafeMessage: "gateway write result is uncertain",
			Retryable:   true,
		},
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(result.Execution.Legs) != 1 {
		t.Fatalf("legs = %+v, want one uncertain leg", result.Execution.Legs)
	}
	last := result.Execution.Legs[0].LastError
	if last.Code != "gateway_upstream_timeout" || last.SafeMessage != "gateway write result is uncertain" || !last.Retryable {
		t.Fatalf("ambiguous safe error was not preserved: %#v", last)
	}
	if strings.Contains(last.SafeMessage, "sk-test") {
		t.Fatalf("unsafe raw text leaked into safe error: %#v", last)
	}
}

func TestFirstLegWorkingZeroFillDoesNotPlaceSecondLeg(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.acceptWorking("exec-1:long", "order-long")
	gateway.acceptFull("exec-1:short", "order-short", decimal.NewFromInt(1))

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("placed leg 2 before leg 1 fully filled: %+v", gateway.places)
	}
	if result.Execution.State != execution.StateFailed {
		t.Fatalf("state = %s, want failed after zero-fill cancel", result.Execution.State)
	}
}

func TestCompensationRejectAndUncertainRequireManualWithoutSecondCompensation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeGateway, decimal.Decimal)
	}{
		{name: "reject", setup: func(g *fakeGateway, q decimal.Decimal) { g.reject("exec-1:compensation", "order-comp") }},
		{name: "uncertain", setup: func(g *fakeGateway, q decimal.Decimal) { g.ambiguous("exec-1:compensation") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			gateway := &fakeGateway{}
			source := &fakeSource{now: testNow, riskVerifiable: true}
			service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
			preview := savePreview(t, service, "preview-1")
			quantity := preview.PlannedLegs[0].Quantity
			gateway.acceptFull("exec-1:long", "order-long", quantity)
			gateway.reject("exec-1:short", "order-short")
			tc.setup(gateway, quantity)

			result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			if len(gateway.places) != 3 {
				t.Fatalf("gateway places = %d, want exactly one compensation attempt", len(gateway.places))
			}
			if !result.Execution.ManualIntervention {
				t.Fatalf("manual intervention not set: %+v", result.Execution)
			}
			if tc.name == "reject" {
				comp := result.Execution.Compensation
				if comp == nil || comp.Status != execution.LegStatusRejected || comp.Error.SafeMessage != "order rejected" {
					t.Fatalf("compensation rejection not persisted as rejected safe error: %+v", comp)
				}
			}
		})
	}
}

func TestPublisherFailureDoesNotRollbackOrDuplicateGatewayWrites(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	publisher := &fakePublisher{err: errors.New("publisher unavailable")}
	service := newTestServiceWithPublisher(t, store, source, gateway, publisher, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.Degraded || !result.Recoverable {
		t.Fatalf("publisher failure not surfaced as degraded/recoverable: %+v", result)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("state = %s, want hedged despite publisher failure", result.Execution.State)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway writes = %d, want 2", len(gateway.places))
	}
}

func TestAuthoritativeRecoveryClearsPublisherDegradedLatch(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	publisher := &fakePublisher{err: errors.New("publisher unavailable")}
	repairer := &fakeRecoveryRepairer{}
	service, err := New(Options{
		Store: store, Source: source, Gateway: gateway, Publisher: publisher,
		RecoveryRepairer: repairer,
		Clock:            fixedClock{at: testNow}, IDs: fixedIDs{},
		Wait: func(context.Context, time.Duration) error { return nil }, Ready: true,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.Degraded || !service.Readiness().Degraded {
		t.Fatalf("publisher failure did not latch degraded: result=%+v readiness=%+v", result, service.Readiness())
	}

	publisher.err = nil
	if _, err := service.Recover(context.Background(), testScope, "recover-1"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if repairer.calls != 1 || repairer.scopes[0] != testScope {
		t.Fatalf("authoritative repairs = %d scopes=%+v, want one scoped repair", repairer.calls, repairer.scopes)
	}
	if readiness := service.Readiness(); !readiness.Ready || readiness.Recovering || readiness.Degraded {
		t.Fatalf("readiness after authoritative recovery = %+v", readiness)
	}
}

func TestAuthoritativeRecoveryClearsDegradedLatchAfterTerminalIdempotencyRepair(t *testing.T) {
	store := newFakeStore()
	store.completeErr = errors.New("idempotency store unavailable")
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	publisher := &fakePublisher{err: errors.New("publisher unavailable")}
	repairer := &fakeRecoveryRepairer{}
	service, err := New(Options{
		Store: store, Source: source, Gateway: gateway, Publisher: publisher,
		RecoveryRepairer: repairer,
		Clock:            fixedClock{at: testNow}, IDs: fixedIDs{},
		Wait: func(context.Context, time.Duration) error { return nil }, Ready: true,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !result.Degraded || !service.Readiness().Degraded {
		t.Fatalf("failures did not latch degraded: result=%+v readiness=%+v", result, service.Readiness())
	}

	publisher.err = nil
	store.completeErr = nil
	if _, err := service.Recover(context.Background(), testScope, "recover-1"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if repairer.calls != 1 {
		t.Fatalf("authoritative repairs = %d, want 1", repairer.calls)
	}
	if readiness := service.Readiness(); !readiness.Ready || readiness.Recovering || readiness.Degraded {
		t.Fatalf("readiness after terminal idempotency repair = %+v", readiness)
	}
}

func TestAuthoritativeRecoveryFailureKeepsWritesBlocked(t *testing.T) {
	service, err := New(Options{
		Store: newFakeStore(), Source: &fakeSource{now: testNow, riskVerifiable: true}, Gateway: &fakeGateway{},
		RecoveryRepairer: &fakeRecoveryRepairer{err: errors.New("projection rebuild failed")},
		Clock:            fixedClock{at: testNow}, IDs: fixedIDs{},
		Wait: func(context.Context, time.Duration) error { return nil }, Ready: false,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	if _, err := service.Recover(context.Background(), testScope, "recover-1"); !errors.Is(err, ErrServiceUnavailable) {
		t.Fatalf("recover error = %v, want service unavailable", err)
	}
	if readiness := service.Readiness(); readiness.Ready || readiness.Recovering || !readiness.Degraded {
		t.Fatalf("readiness after failed authoritative repair = %+v", readiness)
	}
}

func TestHealthyRuntimeRecoverySkipsAuthoritativeRepair(t *testing.T) {
	repairer := &fakeRecoveryRepairer{}
	service, err := New(Options{
		Store: newFakeStore(), Source: &fakeSource{now: testNow, riskVerifiable: true}, Gateway: &fakeGateway{},
		RecoveryRepairer: repairer,
		Clock:            fixedClock{at: testNow}, IDs: fixedIDs{},
		Wait: func(context.Context, time.Duration) error { return nil }, Ready: true,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	if _, err := service.Recover(context.Background(), testScope, "recover-1"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if repairer.calls != 0 {
		t.Fatalf("healthy runtime recovery performed %d authoritative repairs, want 0", repairer.calls)
	}
}

func TestCompleteIdempotencyFailureSurfacesWithoutDuplicateWrites(t *testing.T) {
	store := newFakeStore()
	store.completeErr = errors.New("idempotency store unavailable")
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit error = %v, want degraded terminal success", err)
	}
	if !result.Degraded || !result.Recoverable || result.Execution.State != execution.StateHedged ||
		result.ResponseCode != 202 || len(result.ResponseBody) == 0 ||
		result.SafeError.Code != "IDEMPOTENCY_COMPLETION_FAILED" {
		t.Fatalf("completion failure result = %+v", result)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("gateway writes = %d, want first execution only", len(gateway.places))
	}
	retry, retryErr := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-2")
	if retryErr != nil {
		t.Fatalf("retry error = %v, want degraded terminal replay", retryErr)
	}
	if !retry.Replayed || !retry.Degraded || !retry.Recoverable ||
		retry.Execution.ID != result.Execution.ID ||
		retry.Execution.State != execution.StateHedged ||
		retry.ResponseCode != 202 || len(retry.ResponseBody) == 0 ||
		retry.SafeError.Code != "IDEMPOTENCY_COMPLETION_FAILED" {
		t.Fatalf("retry result = %+v", retry)
	}
	if len(gateway.places) != 2 {
		t.Fatalf("retry duplicated gateway writes: %d", len(gateway.places))
	}
}

func TestConcurrentTerminalPersistedDuringSubmitReturnsTerminalReplayWithoutDuplicateWrites(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.acceptFull("exec-1:short", "order-short", quantity)
	store.beforePersist = func(input PersistTransitionInput) {
		if input.Kind != "leg_accepted" {
			return
		}
		terminal, err := execution.NewExecution("exec-1", preview)
		if err != nil {
			t.Fatalf("new terminal execution: %v", err)
		}
		for _, event := range []execution.Event{
			execution.BeginSubmit(testNow),
			execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
			execution.LegFilled("exec-1:long", quantity, testNow),
			execution.LegAccepted("exec-1:short", execution.LegRoleShort, "order-short", testNow),
			execution.LegFilled("exec-1:short", quantity, testNow),
		} {
			if err := terminal.Apply(event); err != nil {
				t.Fatalf("apply terminal event: %v", err)
			}
		}
		store.execs["exec-1"] = StoredExecution{Execution: terminal.Snapshot(), Version: input.ExpectedVersion + 1}
	}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("submit error = %v, want degraded terminal replay", err)
	}
	if !result.Degraded || !result.Recoverable || result.Execution.State != execution.StateHedged ||
		result.ResponseCode != 202 || len(result.ResponseBody) == 0 ||
		result.SafeError.Code != "IDEMPOTENCY_COMPLETION_FAILED" {
		t.Fatalf("concurrent terminal result = %+v", result)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("gateway writes = %d, want only the synchronous first leg before replay", len(gateway.places))
	}

	retry, retryErr := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-2")
	if retryErr != nil {
		t.Fatalf("retry error = %v", retryErr)
	}
	if !retry.Replayed || retry.Execution.State != execution.StateHedged || retry.ResponseCode != 202 {
		t.Fatalf("retry result = %+v", retry)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("retry duplicated gateway writes: %d", len(gateway.places))
	}
}

func TestReserveReplayInProgressDoesNotReturnInvalidReplayOrDuplicateWrites(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	requestHash, err := browserRequestHash(testScope, submitMethod, submitPath, previewRequest("preview-1"))
	if err != nil {
		t.Fatalf("browser hash: %v", err)
	}
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST//api/v1/orders/idem-1"
	store.records[recordKey] = idempotencyRecord{requestHash: requestHash, executionID: exec.ID, state: "in_progress"}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 1}
	store.idempotencyMissOnce = true

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if !errors.Is(err, ErrExecutionInProgress) {
		t.Fatalf("submit error = %v, want execution in progress", err)
	}
	if !result.Recoverable || result.Execution.ID != exec.ID || result.ResponseCode != 0 || len(result.ResponseBody) != 0 {
		t.Fatalf("in-progress result = %+v", result)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("in-progress reservation duplicated gateway writes: %+v", gateway.places)
	}
}

func TestReserveReplayInProgressTerminalExecutionReturnsSuccessWithoutDuplicateWrites(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	quantity := preview.PlannedLegs[0].Quantity
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilled("exec-1:long", quantity, testNow),
		execution.LegAccepted("exec-1:short", execution.LegRoleShort, "order-short", testNow),
		execution.LegFilled("exec-1:short", quantity, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply terminal event: %v", err)
		}
	}
	requestHash, err := browserRequestHash(testScope, submitMethod, submitPath, previewRequest("preview-1"))
	if err != nil {
		t.Fatalf("browser hash: %v", err)
	}
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST//api/v1/orders/idem-1"
	store.records[recordKey] = idempotencyRecord{requestHash: requestHash, executionID: exec.ID, state: "in_progress"}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 6}
	store.idempotencyMissOnce = true

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("terminal replay error = %v", err)
	}
	if !result.Replayed || !result.Degraded || !result.Recoverable ||
		result.Execution.State != execution.StateHedged ||
		result.ResponseCode != 202 || len(result.ResponseBody) == 0 ||
		result.SafeError.Code != "IDEMPOTENCY_COMPLETION_FAILED" {
		t.Fatalf("terminal replay result = %+v", result)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("terminal replay duplicated gateway writes: %+v", gateway.places)
	}
	record := store.records[recordKey]
	if record.state != "completed" || record.code != 202 || len(record.body) == 0 {
		t.Fatalf("terminal replay did not repair idempotency record: %+v body=%s", record, record.body)
	}
}

func TestGetIdempotencyInProgressTerminalExecutionRepairsLinkedRecord(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	quantity := preview.PlannedLegs[0].Quantity
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilled("exec-1:long", quantity, testNow),
		execution.LegAccepted("exec-1:short", execution.LegRoleShort, "order-short", testNow),
		execution.LegFilled("exec-1:short", quantity, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply terminal event: %v", err)
		}
	}
	requestHash, err := browserRequestHash(testScope, submitMethod, submitPath, previewRequest("preview-1"))
	if err != nil {
		t.Fatalf("browser hash: %v", err)
	}
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST//api/v1/orders/idem-1"
	store.records[recordKey] = idempotencyRecord{requestHash: requestHash, executionID: exec.ID, state: "in_progress"}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 6}

	result, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1")
	if err != nil {
		t.Fatalf("terminal replay error = %v", err)
	}
	if !result.Replayed || result.Execution.State != execution.StateHedged || result.ResponseCode != 202 || len(result.ResponseBody) == 0 {
		t.Fatalf("terminal replay result = %+v", result)
	}
	record := store.records[recordKey]
	if record.state != "completed" || record.code != 202 || len(record.body) == 0 {
		t.Fatalf("terminal replay did not repair direct idempotency record: %+v body=%s", record, record.body)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("terminal replay duplicated gateway writes: %+v", gateway.places)
	}
}

func TestRawUpstreamErrorsAreSanitized(t *testing.T) {
	secretErr := errors.New("upstream failed with api_secret=super-secret")
	service := newTestService(t, newFakeStore(), &fakeSource{now: testNow, riskVerifiable: true, err: secretErr}, &fakeGateway{}, fixedClock{at: testNow}, true)

	if _, err := service.Preview(context.Background(), testScope, previewRequest("preview-1")); !errors.Is(err, ErrServiceUnavailable) || errors.Is(err, secretErr) || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("raw upstream error leaked: %v", err)
	}
}

func TestRecoveryBlocksWritesAndDoesNotCreateNewNormalOrders(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	savePreview(t, service, "preview-1")
	gateway.ambiguous("exec-1:long")
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	placesBeforeRecovery := len(gateway.places)

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	if _, err := restarted.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-2", "request-2"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("submit before recovery error = %v, want not ready", err)
	}
	if _, err := restarted.Recover(context.Background(), testScope, "recover-1"); err == nil {
		t.Fatalf("recover unexpectedly succeeded")
	}
	if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded {
		t.Fatalf("service should stay blocked after unresolved recovery: %+v", readiness)
	}
	if len(gateway.places) != placesBeforeRecovery {
		t.Fatalf("recovery created gateway writes: before=%d after=%d", placesBeforeRecovery, len(gateway.places))
	}
	if len(gateway.gets) == 0 || len(gateway.fillsCalls) != 0 {
		t.Fatalf("recovery must observe the missing order without querying its fills: gets=%v fills=%v", gateway.gets, gateway.fillsCalls)
	}
}

func TestRecoverRepairsTerminalLinkedIdempotencyWithoutGatewayWrites(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	quantity := preview.PlannedLegs[0].Quantity
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilled("exec-1:long", quantity, testNow),
		execution.LegAccepted("exec-1:short", execution.LegRoleShort, "order-short", testNow),
		execution.LegFilled("exec-1:short", quantity, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply terminal event: %v", err)
		}
	}
	requestHash, err := browserRequestHash(testScope, submitMethod, submitPath, previewRequest("preview-1"))
	if err != nil {
		t.Fatalf("browser hash: %v", err)
	}
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST//api/v1/orders/idem-terminal"
	store.records[recordKey] = idempotencyRecord{requestHash: requestHash, executionID: exec.ID, state: "in_progress"}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 6}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-terminal")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.ID != exec.ID || result.Execution.State != execution.StateHedged {
		t.Fatalf("terminal repair result = %+v", result)
	}
	record := store.records[recordKey]
	if record.state != "completed" || record.code != 202 || len(record.body) == 0 {
		t.Fatalf("terminal startup repair did not complete idempotency record: %+v body=%s", record, record.body)
	}
	if len(gateway.places) != 0 || len(gateway.gets) != 0 || len(gateway.fillsCalls) != 0 || len(gateway.cancels) != 0 {
		t.Fatalf("terminal startup repair touched gateway: places=%+v gets=%+v fills=%+v cancels=%+v", gateway.places, gateway.gets, gateway.fillsCalls, gateway.cancels)
	}
	if readiness := restarted.Readiness(); !readiness.Ready || readiness.Degraded {
		t.Fatalf("readiness after terminal-only repair = %+v", readiness)
	}
}

func TestRecoverUnsubmittedExecutionBecomesManualAndKeepsWritesBlocked(t *testing.T) {
	store := newFakeStore()
	source := &fakeSource{now: testNow, riskVerifiable: true}
	gateway := &fakeGateway{}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, false)
	preview := savePreviewRequest(t, service, previewRequest("preview-1"))
	exec, err := execution.NewExecution("exec-unsubmitted", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	if err := exec.Apply(execution.BeginSubmit(testNow)); err != nil {
		t.Fatalf("begin submit: %v", err)
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 1}

	if _, err := service.Recover(context.Background(), testScope, "recover-1"); err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("recovery created gateway writes: %+v", gateway.places)
	}
	readiness := service.Readiness()
	if readiness.Ready || !readiness.Degraded {
		t.Fatalf("readiness after unresolved recovery = %+v", readiness)
	}
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "new-key", "request-2"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("write after unresolved recovery = %v, want not ready", err)
	}
	stored, err := store.GetExecution(context.Background(), testScope, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if stored.Execution.State != execution.StateManualInterventionRequired || !stored.Execution.ManualIntervention {
		t.Fatalf("recovered execution = %+v", stored.Execution)
	}
}

func TestRecoveryMapsRejectedCanceledAndUncertainWithoutNewOrders(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.ambiguous("exec-1:short")
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	before := len(gateway.places)
	gateway.orders["exec-1:short"] = OrderObservation{Found: true, GatewayID: "exec-1:short", OrderID: "order-short", Status: execution.LegStatusRejected, Error: execution.ErrorInfo{Code: "REJECTED", SafeMessage: "order rejected"}}

	result, err := service.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if len(gateway.places) != before {
		t.Fatalf("recovery created new orders: before=%d after=%d", before, len(gateway.places))
	}
	stored, err := store.GetExecution(context.Background(), testScope, "exec-1")
	if err != nil {
		t.Fatalf("get stored execution: %v", err)
	}
	var short execution.Leg
	var long execution.Leg
	for _, leg := range stored.Execution.Legs {
		if leg.Role == execution.LegRoleLong {
			long = leg
		}
		if leg.Role == execution.LegRoleShort {
			short = leg
		}
	}
	if short.Status != execution.LegStatusRejected || stored.Execution.State != execution.StatePartiallyHedged || result.Execution.State != execution.StatePartiallyHedged {
		t.Fatalf("recovery did not map rejection: short=%+v state=%s", short, stored.Execution.State)
	}
	if long.VenueEnvironment != "demo" || short.VenueEnvironment != "testnet" {
		t.Fatalf("recovery lost immutable per-leg environments: long=%+v short=%+v", long, short)
	}
	if readiness := service.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "manual recovery required" {
		t.Fatalf("partially hedged recovery must fail closed: %+v", readiness)
	}
	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-after-recover", "request-2"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("write after partially hedged recovery = %v, want not ready", err)
	}
}

func TestRecoverAppliesAuthoritativeFillToRejectedCloseLeg(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreviewRequest(t, service, closePreviewRequest("preview-close", "position-1"))
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilledDeltaDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
		{
			Type: execution.EventLegRejected, GatewayID: "exec-1:short", Role: execution.LegRoleShort,
			Error:     "gateway write was rejected",
			SafeError: execution.ErrorInfo{Code: "GATEWAY_WRITE_REJECTED", SafeMessage: "gateway write was rejected"}, At: testNow,
		},
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 5}
	path := closePath("position-1")
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST/" + path + "/idem-close"
	store.records[recordKey] = idempotencyRecord{requestHash: "close-request", executionID: exec.ID, state: "in_progress"}

	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.outcomes["exec-1:short"] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: "exec-1:short", OrderID: "order-short"}
	gateway.gatewayReplays = map[string]bool{"exec-1:short": true}
	gateway.missingFillsErr = map[string]bool{"exec-1:short": true}
	gateway.orders["order-short"] = OrderObservation{Found: true, GatewayID: "order-short", OrderID: "order-short", Status: execution.LegStatusFilled}
	gateway.fills["order-short"] = []FillObservation{{
		FillID: "fill-short-1", GatewayFillID: "gateway-fill-short-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow.Add(time.Second),
	}}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(2 * time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.State != execution.StateClosed {
		t.Fatalf("recovered state = %s, want closed: %+v", result.Execution.State, result.Execution)
	}
	if len(result.Execution.Fills) != 2 || result.Execution.Legs[1].Status != execution.LegStatusFilled || result.Execution.Legs[1].LastError.Code != "" {
		t.Fatalf("authoritative short fill did not replace rejected evidence: legs=%+v fills=%+v", result.Execution.Legs, result.Execution.Fills)
	}
	if len(gateway.places) != 1 || gateway.places[0].GatewayID != "exec-1:short" || !gateway.places[0].ReduceOnly {
		t.Fatalf("recovery did not replay the exact rejected close command: places=%+v", gateway.places)
	}
	if len(gateway.venueWrites) != 0 || len(gateway.cancels) != 0 {
		t.Fatalf("idempotent fill reconciliation performed a venue write: venueWrites=%+v cancels=%+v", gateway.venueWrites, gateway.cancels)
	}
	for _, lookupID := range gateway.fillsCalls {
		if lookupID == "exec-1:short" {
			t.Fatalf("recovery queried fills for a missing deterministic order: fillsCalls=%+v", gateway.fillsCalls)
		}
	}
	if record := store.records[recordKey]; record.state != "completed" || record.code != http.StatusAccepted || len(record.body) == 0 {
		t.Fatalf("recovered close idempotency was not completed: %+v", record)
	}
	if readiness := restarted.Readiness(); !readiness.Ready || readiness.Degraded {
		t.Fatalf("readiness after authoritative close recovery = %+v", readiness)
	}
}

func TestRecoverFailsClosedWhenWorkingLegCannotBeResolved(t *testing.T) {
	cases := []struct {
		name           string
		configure      func(*fakeGateway)
		wantFillsCalls int
	}{
		{
			name: "get order error",
			configure: func(gateway *fakeGateway) {
				gateway.getErr = errors.New("gateway read unavailable")
			},
		},
		{
			name: "order missing",
			configure: func(gateway *fakeGateway) {
				delete(gateway.orders, "order-long")
			},
		},
		{
			name: "fills error",
			configure: func(gateway *fakeGateway) {
				gateway.fillsErr = errors.New("fills unavailable")
			},
			wantFillsCalls: 1,
		},
		{
			name: "positions fallback cannot mask missing order",
			configure: func(gateway *fakeGateway) {
				delete(gateway.orders, "order-long")
				gateway.positionsErr = errors.New("positions unavailable")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			gateway := &fakeGateway{}
			source := &fakeSource{now: testNow, riskVerifiable: true}
			service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
			preview := savePreview(t, service, "preview-1")
			exec, err := execution.NewExecution("exec-1", preview)
			if err != nil {
				t.Fatalf("new execution: %v", err)
			}
			if err := exec.Apply(execution.BeginSubmit(testNow)); err != nil {
				t.Fatalf("begin submit: %v", err)
			}
			if err := exec.Apply(execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow)); err != nil {
				t.Fatalf("leg accepted: %v", err)
			}
			store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 2}
			gateway.ensure()
			gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "exec-1:long", OrderID: "order-long", Status: execution.LegStatusWorking}
			placesBeforeRecovery := len(gateway.places)
			getsBeforeRecovery := len(gateway.gets)
			fillsBeforeRecovery := len(gateway.fillsCalls)
			positionsBeforeRecovery := len(gateway.positionsCalls)
			tc.configure(gateway)

			restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
			if _, err := restarted.Recover(context.Background(), testScope, "recover-1"); err == nil {
				t.Fatalf("recover unexpectedly succeeded")
			}
			readiness := restarted.Readiness()
			if readiness.Ready || !readiness.Degraded || readiness.Reason != "recovery failed" {
				t.Fatalf("readiness after failed recovery = %+v", readiness)
			}
			if len(gateway.places) != placesBeforeRecovery {
				t.Fatalf("recovery created gateway writes: before=%d after=%d", placesBeforeRecovery, len(gateway.places))
			}
			if _, err := restarted.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-new", "request-2"); !errors.Is(err, ErrNotReady) {
				t.Fatalf("write after failed recovery = %v, want not ready", err)
			}
			if len(gateway.gets) != getsBeforeRecovery+1 {
				t.Fatalf("recovery get calls = %d after baseline %d", len(gateway.gets), getsBeforeRecovery)
			}
			if len(gateway.fillsCalls) != fillsBeforeRecovery+tc.wantFillsCalls {
				t.Fatalf("recovery fills calls = %d after baseline %d", len(gateway.fillsCalls), fillsBeforeRecovery)
			}
			if len(gateway.positionsCalls) != positionsBeforeRecovery {
				t.Fatalf("recovery used positions fallback: before=%d after=%d", positionsBeforeRecovery, len(gateway.positionsCalls))
			}
		})
	}
}

func TestRecoverFailsClosedWhenObservedStatusCannotApply(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	gateway.acceptFull("exec-1:long", "order-long", preview.PlannedLegs[0].Quantity)
	gateway.acceptWorking("exec-1:short", "order-short")

	if _, err := service.Submit(context.Background(), testScope, submitRequest("preview-1"), "idem-1", "request-1"); err != nil {
		t.Fatalf("submit: %v", err)
	}
	placesBeforeRecovery := len(gateway.places)
	gateway.orders["order-long"] = OrderObservation{
		Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusCanceled,
	}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	if _, err := restarted.Recover(context.Background(), testScope, "recover-1"); err == nil {
		t.Fatal("recover unexpectedly accepted an invalid filled-to-canceled transition")
	}
	if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "recovery failed" {
		t.Fatalf("readiness after invalid recovery transition = %+v", readiness)
	}
	if len(gateway.places) != placesBeforeRecovery {
		t.Fatalf("recovery created gateway writes: before=%d after=%d", placesBeforeRecovery, len(gateway.places))
	}
}

func TestReconcilePromotesPartialToFilledWhenCumulativeQuantityUnchanged(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegPartialFillDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}

	result, err := service.Reconcile(context.Background(), testScope, "exec-1", "reconcile-1")
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Execution.Legs[0].Status != execution.LegStatusFilled {
		t.Fatalf("leg status = %s, want filled", result.Execution.Legs[0].Status)
	}
	if len(result.Execution.Fills) != 1 {
		t.Fatalf("status-only filled duplicated fills: %+v", result.Execution.Fills)
	}
	stored := store.execs["exec-1"]
	if stored.Execution.Legs[0].Status != execution.LegStatusFilled || len(stored.Execution.Fills) != 1 {
		t.Fatalf("stored execution = %+v", stored.Execution)
	}
	if len(gateway.places) != 0 {
		t.Fatalf("reconcile created gateway writes: %+v", gateway.places)
	}
}

func TestRecoverContinuesMissingCloseLegByGatewayIdempotencyWithoutRepeatingFirst(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreviewRequest(t, service, closePreviewRequest("preview-close", "position-1"))
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegPartialFillDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	request := closeSubmitRequest("preview-close", "position-1")
	path := closePath("position-1")
	requestHash, err := browserRequestHash(testScope, submitMethod, path, PreviewRequest{
		PreviewID: request.PreviewID, Operation: request.Operation, OpportunityID: request.OpportunityID,
		PositionID: request.PositionID, CapitalUSDT: request.CapitalUSDT, Leverage: request.Leverage,
	})
	if err != nil {
		t.Fatalf("browser hash: %v", err)
	}
	store.records[testScope.PrincipalID+"/"+testScope.Mode+"/POST/"+path+"/idem-close"] = idempotencyRecord{
		requestHash: requestHash, executionID: exec.ID, state: "in_progress",
	}
	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.outcomes["exec-1:short"] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: "exec-1:short", OrderID: "order-short"}
	gateway.gatewayReplays = map[string]bool{"exec-1:short": true}
	gateway.missingFillsErr = map[string]bool{"exec-1:short": true}
	gateway.orders["order-short"] = OrderObservation{Found: true, GatewayID: "order-short", OrderID: "order-short", Status: execution.LegStatusFilled}
	gateway.fills["order-short"] = []FillObservation{{
		FillID: "fill-short-1", GatewayFillID: "gateway-fill-short-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.State != execution.StateClosed {
		t.Fatalf("recovered state = %s, want closed: %+v", result.Execution.State, result.Execution)
	}
	if len(result.Execution.Legs) != 2 ||
		result.Execution.Legs[0].Role != execution.LegRoleLong ||
		result.Execution.Legs[1].Role != execution.LegRoleShort ||
		result.Execution.Legs[1].GatewayID != "exec-1:short" ||
		result.Execution.Legs[1].Status != execution.LegStatusFilled {
		t.Fatalf("recovered legs = %+v", result.Execution.Legs)
	}
	if result.Execution.Compensation != nil {
		t.Fatalf("recovery used compensation: %+v", result.Execution.Compensation)
	}
	if len(gateway.places) != 1 ||
		gateway.places[0].GatewayID != "exec-1:short" ||
		gateway.places[0].Role != execution.LegRoleShort ||
		!gateway.places[0].ReduceOnly {
		t.Fatalf("recovery gateway idempotency replay = %+v, want only missing reduce-only short", gateway.places)
	}
	if len(gateway.venueWrites) != 0 || len(gateway.cancels) != 0 {
		t.Fatalf("recovery performed venue write/cancel: venueWrites=%+v cancels=%+v", gateway.venueWrites, gateway.cancels)
	}
	if len(gateway.gets) != 3 || gateway.gets[0] != "order-long" || gateway.gets[1] != "exec-1:short" || gateway.gets[2] != "order-short" {
		t.Fatalf("gateway gets = %+v, want existing long, deterministic miss, replayed order lookup", gateway.gets)
	}
	if len(gateway.fillsCalls) != 2 || gateway.fillsCalls[0] != "order-long" || gateway.fillsCalls[1] != "order-short" {
		t.Fatalf("fills calls = %+v, want no fills call for missing deterministic probe", gateway.fillsCalls)
	}
	if readiness := restarted.Readiness(); !readiness.Ready || readiness.Degraded {
		t.Fatalf("readiness after terminal recovery = %+v", readiness)
	}
	recordKey := testScope.PrincipalID + "/" + testScope.Mode + "/POST/" + path + "/idem-close"
	record := store.records[recordKey]
	if record.state != "completed" || record.code != 202 || len(record.body) == 0 {
		t.Fatalf("terminal recovery did not repair idempotency record: %+v body=%s", record, record.body)
	}

	replay, replayErr := restarted.Close(context.Background(), testScope, request, "idem-close", "request-replay")
	if replayErr != nil {
		t.Fatalf("same-key close replay after recovery: %v", replayErr)
	}
	if !replay.Replayed || replay.Execution.State != execution.StateClosed || replay.ResponseCode != 202 || len(replay.ResponseBody) == 0 {
		t.Fatalf("terminal replay = %+v", replay)
	}
	if len(gateway.places) != 1 {
		t.Fatalf("terminal replay duplicated gateway writes: %+v", gateway.places)
	}
}

func TestRecoverContinuesMissingOpenLegByGatewayIdempotencyWithoutRepeatingFirst(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilledDeltaDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	store.records[testScope.PrincipalID+"/"+testScope.Mode+"/POST/"+submitPath+"/idem-open"] = idempotencyRecord{
		requestHash: "open-request", executionID: exec.ID, state: "in_progress",
	}

	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.outcomes["exec-1:short"] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: "exec-1:short", OrderID: "order-short"}
	gateway.orders["order-short"] = OrderObservation{Found: true, GatewayID: "order-short", OrderID: "order-short", Status: execution.LegStatusFilled}
	gateway.fills["order-short"] = []FillObservation{{
		FillID: "fill-short-1", GatewayFillID: "gateway-fill-short-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.State != execution.StateHedged {
		t.Fatalf("recovered state = %s, want hedged: %+v", result.Execution.State, result.Execution)
	}
	if len(result.Execution.Legs) != 2 || result.Execution.Legs[1].Role != execution.LegRoleShort || result.Execution.Legs[1].Status != execution.LegStatusFilled {
		t.Fatalf("recovered legs = %+v", result.Execution.Legs)
	}
	if len(gateway.places) != 1 || gateway.places[0].GatewayID != "exec-1:short" || gateway.places[0].ReduceOnly {
		t.Fatalf("recovery replayed unexpected orders: %+v", gateway.places)
	}
	if len(gateway.venueWrites) != 1 || gateway.venueWrites[0].GatewayID != "exec-1:short" {
		t.Fatalf("missing open leg was not submitted exactly once: %+v", gateway.venueWrites)
	}
	if len(gateway.gets) != 3 || gateway.gets[0] != "order-long" || gateway.gets[1] != "exec-1:short" || gateway.gets[2] != "order-short" {
		t.Fatalf("gateway gets = %+v, want existing long, deterministic miss, replayed order lookup", gateway.gets)
	}
	if result.Execution.Compensation != nil {
		t.Fatalf("successful open recovery used compensation: %+v", result.Execution.Compensation)
	}
	if readiness := restarted.Readiness(); !readiness.Ready || readiness.Degraded {
		t.Fatalf("readiness after hedged recovery = %+v", readiness)
	}
}

func TestRecoverCompensatesOnceWhenMissingOpenLegReplayIsRejected(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilledDeltaDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.outcomes["exec-1:short"] = GatewayOrderResult{
		Outcome: GatewayRejected, GatewayID: "exec-1:short", Error: execution.ErrorInfo{Code: "REJECTED", SafeMessage: "order rejected"},
	}
	gateway.acceptFull("exec-1:compensation", "order-compensation", quantity)

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.State != execution.StateFailed || result.Execution.Compensation == nil || !result.Execution.Compensation.Attempted {
		t.Fatalf("rejected missing leg was not safely compensated: %+v", result.Execution)
	}
	if len(gateway.places) != 2 || gateway.places[0].GatewayID != "exec-1:short" || gateway.places[1].GatewayID != "exec-1:compensation" || !gateway.places[1].ReduceOnly {
		t.Fatalf("recovery writes = %+v, want short replay then one reduce-only compensation", gateway.places)
	}
	if !result.Execution.DirectionalExposure.IsZero() {
		t.Fatalf("compensated execution exposure = %s, want zero", result.Execution.DirectionalExposure)
	}
	if readiness := restarted.Readiness(); !readiness.Ready || readiness.Degraded {
		t.Fatalf("readiness after compensated terminal recovery = %+v", readiness)
	}
}

func TestRecoverCancelsPartialMissingOpenLegBeforeCompensation(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilledDeltaDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	partial := decimal.RequireFromString("0.4")
	gateway.outcomes["exec-1:short"] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: "exec-1:short", OrderID: "order-short"}
	gateway.orders["order-short"] = OrderObservation{Found: true, GatewayID: "order-short", OrderID: "order-short", Status: execution.LegStatusPartial}
	gateway.fills["order-short"] = []FillObservation{{
		FillID: "fill-short-1", GatewayFillID: "gateway-fill-short-1",
		Quantity: partial, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.acceptFull("exec-1:compensation", "order-compensation", quantity.Sub(partial))

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if result.Execution.State != execution.StateFailed || result.Execution.Compensation == nil || !result.Execution.Compensation.Quantity.Equal(quantity.Sub(partial)) {
		t.Fatalf("partial missing leg was not safely compensated: %+v", result.Execution)
	}
	if len(gateway.cancels) != 1 || gateway.cancels[0].GatewayID != "order-short" {
		t.Fatalf("partial replay remainder was not canceled first: %+v", gateway.cancels)
	}
	if len(gateway.places) != 2 || gateway.places[1].GatewayID != "exec-1:compensation" || !gateway.places[1].ReduceOnly {
		t.Fatalf("partial recovery writes = %+v", gateway.places)
	}
	if !result.Execution.DirectionalExposure.IsZero() {
		t.Fatalf("compensated partial recovery exposure = %s, want zero", result.Execution.DirectionalExposure)
	}
}

func TestRecoverDoesNotCompensateWhenMissingOpenLegReplayIsAmbiguous(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilledDeltaDetail("fill-long-1", "exec-1:long", "gateway-fill-long-1", quantity, quantity, decimal.NewFromInt(100), execution.Money{}, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 4}
	gateway.ensure()
	gateway.orders["order-long"] = OrderObservation{Found: true, GatewayID: "order-long", OrderID: "order-long", Status: execution.LegStatusFilled}
	gateway.fills["order-long"] = []FillObservation{{
		FillID: "fill-long-1", GatewayFillID: "gateway-fill-long-1",
		Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow,
	}}
	gateway.ambiguous("exec-1:short")

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover should preserve ambiguous state without transport error: %v", err)
	}
	if result.Execution.State != execution.StateUncertain || result.Execution.Compensation != nil {
		t.Fatalf("ambiguous missing leg was compensated or marked terminal: %+v", result.Execution)
	}
	if len(gateway.places) != 1 || gateway.places[0].GatewayID != "exec-1:short" {
		t.Fatalf("ambiguous recovery writes = %+v", gateway.places)
	}
	if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "manual recovery required" {
		t.Fatalf("readiness after ambiguous recovery = %+v", readiness)
	}
}

func TestRecoverFailsClosedWhenMissingCloseLegGatewayReplayIsUnresolved(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreviewRequest(t, service, closePreviewRequest("preview-close", "position-1"))
	quantity := preview.PlannedLegs[0].Quantity
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegFilled("exec-1:long", quantity, testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 3}
	gateway.acceptFull("exec-1:long", "order-long", quantity)
	gateway.ambiguous("exec-1:short")

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
	result, err := restarted.Recover(context.Background(), testScope, "recover-1")
	if err != nil {
		t.Fatalf("recover should fail closed without transport error: %v", err)
	}
	if result.Execution.State == execution.StateClosed || len(result.Execution.Legs) != 2 ||
		result.Execution.Legs[1].Role != execution.LegRoleShort ||
		result.Execution.Legs[1].Status != execution.LegStatusUncertain {
		t.Fatalf("unresolved deterministic replay was incorrectly recovered: %+v", result.Execution)
	}
	if len(gateway.places) != 1 || gateway.places[0].GatewayID != "exec-1:short" {
		t.Fatalf("recovery should try only missing short replay: places=%+v", gateway.places)
	}
	if len(gateway.venueWrites) != 1 || gateway.venueWrites[0].GatewayID != "exec-1:short" {
		t.Fatalf("non-replay unresolved attempt should be visible as venue write in fake: %+v", gateway.venueWrites)
	}
	if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "manual recovery required" {
		t.Fatalf("readiness after absent deterministic leg = %+v", readiness)
	}
}

func TestRecoverFailsClosedOnInvalidFillEvidence(t *testing.T) {
	store := newFakeStore()
	gateway := &fakeGateway{}
	source := &fakeSource{now: testNow, riskVerifiable: true}
	service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
	preview := savePreview(t, service, "preview-1")
	exec, err := execution.NewExecution("exec-1", preview)
	if err != nil {
		t.Fatalf("new execution: %v", err)
	}
	for _, event := range []execution.Event{
		execution.BeginSubmit(testNow),
		execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
		execution.LegAccepted("exec-1:short", execution.LegRoleShort, "order-short", testNow),
	} {
		if err := exec.Apply(event); err != nil {
			t.Fatalf("apply precondition event %s: %v", event.Type, err)
		}
	}
	store.execs[exec.ID] = StoredExecution{Execution: exec.Snapshot(), Version: 3}
	gateway.ensure()
	gateway.orders["exec-1:short"] = OrderObservation{
		Found: true, GatewayID: "exec-1:short", OrderID: "order-short", Status: execution.LegStatusWorking,
	}
	placesBeforeRecovery := len(gateway.places)
	gateway.orders["exec-1:long"] = OrderObservation{
		Found: true, GatewayID: "exec-1:long", OrderID: "order-long", Status: execution.LegStatusPartial,
	}
	gateway.fills["exec-1:long"] = []FillObservation{{
		FillID: "fill-oversized", GatewayFillID: "gateway-fill-oversized",
		Quantity: preview.PlannedLegs[0].Quantity.Add(decimal.NewFromInt(1)),
		Price:    decimal.NewFromInt(100), OccurredAt: testNow.Add(time.Second),
	}}
	storedBeforeRecovery := store.execs["exec-1"]
	if storedBeforeRecovery.Execution.State != execution.StateLeg2Working {
		t.Fatalf("precondition execution state = %s, want leg 2 working", storedBeforeRecovery.Execution.State)
	}

	restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(2 * time.Second)}, false)
	if result, err := restarted.Recover(context.Background(), testScope, "recover-1"); err == nil {
		t.Fatalf("recover unexpectedly discarded invalid fill evidence: result=%+v stored=%+v", result.Execution, store.execs["exec-1"].Execution)
	}
	if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "recovery failed" {
		t.Fatalf("readiness after invalid fill evidence = %+v", readiness)
	}
	if len(gateway.places) != placesBeforeRecovery {
		t.Fatalf("recovery created gateway writes: before=%d after=%d", placesBeforeRecovery, len(gateway.places))
	}
}

func TestRecoverFailsClosedWhenPersistedLegIdentityIsIncomplete(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*execution.Execution)
	}{
		{
			name: "missing planned route",
			mutate: func(exec *execution.Execution) {
				exec.PlannedLegs = exec.PlannedLegs[1:]
			},
		},
		{
			name: "missing gateway id",
			mutate: func(exec *execution.Execution) {
				exec.Legs[0].GatewayID = ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeStore()
			gateway := &fakeGateway{}
			source := &fakeSource{now: testNow, riskVerifiable: true}
			service := newTestService(t, store, source, gateway, fixedClock{at: testNow}, true)
			preview := savePreview(t, service, "preview-1")
			exec, err := execution.NewExecution("exec-1", preview)
			if err != nil {
				t.Fatalf("new execution: %v", err)
			}
			for _, event := range []execution.Event{
				execution.BeginSubmit(testNow),
				execution.LegAccepted("exec-1:long", execution.LegRoleLong, "order-long", testNow),
			} {
				if err := exec.Apply(event); err != nil {
					t.Fatalf("apply precondition event %s: %v", event.Type, err)
				}
			}
			snapshot := exec.Snapshot()
			tt.mutate(&snapshot)
			store.execs[exec.ID] = StoredExecution{Execution: snapshot, Version: 2}

			restarted := newTestService(t, store, source, gateway, fixedClock{at: testNow.Add(time.Second)}, false)
			if _, err := restarted.Recover(context.Background(), testScope, "recover-1"); err == nil {
				t.Fatal("recover unexpectedly accepted incomplete persisted leg identity")
			}
			if readiness := restarted.Readiness(); readiness.Ready || !readiness.Degraded || readiness.Reason != "recovery failed" {
				t.Fatalf("readiness after incomplete identity = %+v", readiness)
			}
			if len(gateway.places) != 0 || len(gateway.gets) != 0 || len(gateway.fillsCalls) != 0 {
				t.Fatalf("incomplete identity reached Gateway: places=%d gets=%d fills=%d", len(gateway.places), len(gateway.gets), len(gateway.fillsCalls))
			}
		})
	}
}

func TestRecoverFailureStaysBlockedUntilSuccessfulRetry(t *testing.T) {
	store := newFakeStore()
	store.listErr = errors.New("db unavailable")
	service := newTestService(t, store, &fakeSource{now: testNow, riskVerifiable: true}, &fakeGateway{}, fixedClock{at: testNow}, false)

	if _, err := service.Recover(context.Background(), testScope, "recover-1"); err == nil {
		t.Fatalf("recover unexpectedly succeeded")
	}
	if readiness := service.Readiness(); readiness.Ready || readiness.Reason != "recovery failed" {
		t.Fatalf("readiness after failed recovery = %+v", readiness)
	}
	store.listErr = nil
	if _, err := service.Recover(context.Background(), testScope, "recover-2"); err != nil {
		t.Fatalf("retry recover: %v", err)
	}
	if readiness := service.Readiness(); !readiness.Ready {
		t.Fatalf("readiness after successful retry = %+v", readiness)
	}
}

func TestRecoverIsSingleFlight(t *testing.T) {
	store := newFakeStore()
	store.listBlock = make(chan struct{})
	store.listEntered = make(chan struct{}, 1)
	service := newTestService(t, store, &fakeSource{now: testNow, riskVerifiable: true}, &fakeGateway{}, fixedClock{at: testNow}, false)

	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr error
	go func() {
		defer wg.Done()
		_, firstErr = service.Recover(context.Background(), testScope, "recover-1")
	}()
	<-store.listEntered

	_, secondErr := service.Recover(context.Background(), testScope, "recover-2")
	if !errors.Is(secondErr, ErrRecoveryInProgress) {
		t.Fatalf("second recover error = %v, want recovery in progress", secondErr)
	}
	close(store.listBlock)
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("first recover: %v", firstErr)
	}
	if store.listCalls != 1 {
		t.Fatalf("list calls = %d, want only first recover", store.listCalls)
	}
	if readiness := service.Readiness(); !readiness.Ready || readiness.Recovering || readiness.Degraded {
		t.Fatalf("readiness after first success = %+v", readiness)
	}
}

func TestPreviewRejectsSourceScopeAndRequestOverride(t *testing.T) {
	source := &fakeSource{now: testNow, riskVerifiable: true, principalID: "other", operation: execution.OperationClose}
	service := newTestService(t, newFakeStore(), source, &fakeGateway{}, fixedClock{at: testNow}, true)

	if _, err := service.Preview(context.Background(), testScope, previewRequest("preview-1")); !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("preview override error = %v, want preview mismatch", err)
	}
}

func TestPreviewRejectsSourcePositionMismatch(t *testing.T) {
	source := &fakeSource{now: testNow, riskVerifiable: true, positionID: "other-position"}
	service := newTestService(t, newFakeStore(), source, &fakeGateway{}, fixedClock{at: testNow}, true)

	if _, err := service.Preview(context.Background(), testScope, closePreviewRequest("preview-close", "position-1")); !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("position override error = %v, want preview mismatch", err)
	}
}

func newTestService(t *testing.T, store *fakeStore, source *fakeSource, gateway *fakeGateway, clock Clock, ready bool) *Service {
	t.Helper()
	return newTestServiceWithPublisher(t, store, source, gateway, &fakePublisher{}, clock, ready)
}

func newTestServiceWithPublisher(t *testing.T, store *fakeStore, source *fakeSource, gateway *fakeGateway, publisher *fakePublisher, clock Clock, ready bool) *Service {
	t.Helper()
	service, err := New(Options{
		Store: store, Source: source, Gateway: gateway, Publisher: publisher,
		Clock: clock, IDs: fixedIDs{}, Wait: func(context.Context, time.Duration) error { return nil }, Ready: ready,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return service
}

func previewRequest(id string) PreviewRequest {
	return PreviewRequest{
		PreviewID: id, Operation: execution.OperationOpen, OpportunityID: "opp-1",
		CapitalUSDT: decimal.NewFromInt(100), Leverage: 1,
	}
}

func closePreviewRequest(id, positionID string) PreviewRequest {
	return PreviewRequest{
		PreviewID: id, Operation: execution.OperationClose, PositionID: positionID,
		CapitalUSDT: decimal.NewFromInt(100), Leverage: 1,
	}
}

func closePreviewRequestWithoutEconomics(id, positionID string) PreviewRequest {
	return PreviewRequest{
		PreviewID: id, Operation: execution.OperationClose, PositionID: positionID,
	}
}

func submitRequest(id string) SubmitRequest {
	return SubmitRequest{
		PreviewID: id, Operation: execution.OperationOpen, OpportunityID: "opp-1",
		CapitalUSDT: decimal.NewFromInt(100), Leverage: 1,
	}
}

func closeSubmitRequest(id, positionID string) SubmitRequest {
	return SubmitRequest{
		PreviewID: id, Operation: execution.OperationClose, PositionID: positionID,
		CapitalUSDT: decimal.NewFromInt(100), Leverage: 1,
	}
}

func closeSubmitRequestWithoutEconomics(id, positionID string) SubmitRequest {
	return SubmitRequest{
		PreviewID: id, Operation: execution.OperationClose, PositionID: positionID,
	}
}

func savePreview(t *testing.T, service *Service, id string) execution.PreviewResult {
	t.Helper()
	return savePreviewRequest(t, service, previewRequest(id))
}

func savePreviewRequest(t *testing.T, service *Service, request PreviewRequest) execution.PreviewResult {
	t.Helper()
	preview, err := service.Preview(context.Background(), testScope, request)
	if err != nil {
		t.Fatalf("save preview: %v", err)
	}
	if !preview.CanSubmit {
		t.Fatalf("preview cannot submit: blockers=%v", preview.Blockers)
	}
	return preview
}

type fakeSource struct {
	now                   time.Time
	calls                 int
	riskVerifiable        bool
	snapshotID            string
	principalID           string
	operation             execution.Operation
	positionID            string
	capitalUSDT           decimal.Decimal
	leverage              int
	grossAprPct           decimal.Decimal
	capacityNotional      decimal.Decimal
	longMarkPrice         decimal.Decimal
	shortMarkPrice        decimal.Decimal
	longAccountID         string
	shortVenueEnvironment string
	originalBlockers      []execution.Finding
	recentReversal        bool
	err                   error
}

func (source *fakeSource) FreshPreview(_ context.Context, scope Scope, request PreviewRequest) (execution.PreviewInput, error) {
	source.calls++
	if source.err != nil {
		return execution.PreviewInput{}, source.err
	}
	snapshotID := source.snapshotID
	if snapshotID == "" {
		snapshotID = "snapshot-1"
	}
	principalID := scope.PrincipalID
	if source.principalID != "" {
		principalID = source.principalID
	}
	operation := request.Operation
	if source.operation != "" {
		operation = source.operation
	}
	positionID := request.PositionID
	if source.positionID != "" {
		positionID = source.positionID
	}
	capitalUSDT := request.CapitalUSDT
	if !capitalUSDT.IsPositive() && source.capitalUSDT.IsPositive() {
		capitalUSDT = source.capitalUSDT
	}
	leverage := request.Leverage
	if leverage == 0 && source.leverage != 0 {
		leverage = source.leverage
	}
	shortVenueEnvironment := source.shortVenueEnvironment
	if shortVenueEnvironment == "" {
		shortVenueEnvironment = "testnet"
	}
	grossAprPct := source.grossAprPct
	if grossAprPct.IsZero() {
		grossAprPct = decimal.NewFromInt(365)
	}
	capacityNotional := source.capacityNotional
	if capacityNotional.IsZero() {
		capacityNotional = decimal.NewFromInt(10000)
	}
	longMarkPrice := source.longMarkPrice
	if longMarkPrice.IsZero() {
		longMarkPrice = decimal.NewFromInt(100)
	}
	shortMarkPrice := source.shortMarkPrice
	if shortMarkPrice.IsZero() {
		shortMarkPrice = decimal.NewFromInt(100)
	}
	longAccountID := source.longAccountID
	if longAccountID == "" {
		longAccountID = "long-account"
	}
	currentPositionVerifiable := request.Operation == execution.OperationClose
	currentQuantity := decimal.Zero
	if currentPositionVerifiable {
		currentQuantity = decimal.NewFromInt(10)
	}
	return execution.PreviewInput{
		PreviewID: request.PreviewID, SnapshotID: snapshotID, PrincipalID: principalID,
		Mode: scope.Mode, Operation: operation, OpportunityID: request.OpportunityID, PositionID: positionID,
		AvailableCapitalUSDT: decimal.NewFromInt(1000), CapitalUSDT: capitalUSDT, Leverage: leverage,
		GrossAprPct: grossAprPct, DataObservedAt: source.now.Add(-time.Second), PreviewedAt: source.now,
		CapacityNotional: capacityNotional, LiquidationBufferPct: decimal.NewFromInt(80), StabilityPct: decimal.NewFromInt(90),
		RecentReversal: source.recentReversal,
		RiskVerifiable: source.riskVerifiable, AccountVerifiable: true, RulesVerifiable: true,
		OriginalBlockers:          source.originalBlockers,
		CurrentPositionVerifiable: currentPositionVerifiable, CurrentLongQuantity: currentQuantity, CurrentShortQuantity: currentQuantity,
		Long: execution.PreviewLegInput{
			LegIdentity: execution.LegIdentity{
				AccountID: longAccountID, Venue: "binance", InstrumentID: "long-inst", VenueEnvironment: "demo",
			},
			Symbol: "BTCUSDT", GatewayInstrumentID: "long-gw-inst",
			MarkPrice: longMarkPrice, QuantityIncrement: decimal.RequireFromString("0.1"), MinQuantity: decimal.RequireFromString("0.1"), MinNotional: decimal.NewFromInt(10),
		},
		Short: execution.PreviewLegInput{
			LegIdentity: execution.LegIdentity{
				AccountID: "short-account", Venue: "bybit", InstrumentID: "short-inst", VenueEnvironment: shortVenueEnvironment,
			},
			Symbol: "BTCUSDT", GatewayInstrumentID: "short-gw-inst",
			MarkPrice: shortMarkPrice, QuantityIncrement: decimal.RequireFromString("0.1"), MinQuantity: decimal.RequireFromString("0.1"), MinNotional: decimal.NewFromInt(10),
		},
	}, nil
}

type fakeStore struct {
	previews            map[string]StoredPreview
	records             map[string]idempotencyRecord
	execs               map[string]StoredExecution
	log                 *[]string
	listErr             error
	completeErr         error
	beforePersist       func(PersistTransitionInput)
	idempotencyMissOnce bool
	listBlock           chan struct{}
	listEntered         chan struct{}
	listCalls           int
}

type idempotencyRecord struct {
	requestHash string
	executionID string
	state       string
	code        int
	body        json.RawMessage
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		previews: make(map[string]StoredPreview),
		records:  make(map[string]idempotencyRecord),
		execs:    make(map[string]StoredExecution),
	}
}

func (store *fakeStore) SavePreview(_ context.Context, preview execution.PreviewResult, requestHash string) error {
	key := preview.PrincipalID + "/" + preview.Mode + "/" + preview.PreviewID
	if existing, ok := store.previews[key]; ok && existing.RequestHash != requestHash {
		return ErrPreviewConflict
	}
	store.previews[key] = StoredPreview{Preview: preview, RequestHash: requestHash}
	return nil
}

func (store *fakeStore) GetPreview(_ context.Context, scope Scope, id string, now time.Time) (StoredPreview, error) {
	key := scope.PrincipalID + "/" + scope.Mode + "/" + id
	preview, ok := store.previews[key]
	if !ok {
		return StoredPreview{}, ErrNotFound
	}
	preview.Expired = !now.Before(preview.Preview.ExpiresAt)
	return preview, nil
}

func (store *fakeStore) GetIdempotency(_ context.Context, scope Scope, method, path, key string) (IdempotencyRecord, error) {
	if store.idempotencyMissOnce {
		store.idempotencyMissOnce = false
		return IdempotencyRecord{}, ErrNotFound
	}
	recordKey := scope.PrincipalID + "/" + scope.Mode + "/" + method + "/" + path + "/" + key
	record, ok := store.records[recordKey]
	if !ok {
		return IdempotencyRecord{}, ErrNotFound
	}
	return IdempotencyRecord{
		ExecutionID: record.executionID, RequestHash: record.requestHash, State: record.state,
		ResponseCode: record.code, ResponseBody: append(json.RawMessage(nil), record.body...),
	}, nil
}

func (store *fakeStore) ReserveExecution(_ context.Context, scope Scope, method, path, key, requestHash string, snapshot execution.Execution) (IdempotencyReservation, error) {
	recordKey := scope.PrincipalID + "/" + scope.Mode + "/" + method + "/" + path + "/" + key
	if record, ok := store.records[recordKey]; ok {
		if record.requestHash != requestHash {
			return IdempotencyReservation{}, ErrIdempotencyConflict
		}
		stored := store.execs[record.executionID]
		return IdempotencyReservation{Execution: stored.Execution, Version: stored.Version, Replayed: true, State: record.state, ResponseCode: record.code, ResponseBody: record.body}, nil
	}
	store.records[recordKey] = idempotencyRecord{requestHash: requestHash, executionID: snapshot.ID, state: "in_progress"}
	store.execs[snapshot.ID] = StoredExecution{Execution: snapshot.Snapshot(), Version: 1}
	return IdempotencyReservation{Execution: snapshot.Snapshot(), Version: 1}, nil
}

func (store *fakeStore) CompleteIdempotency(_ context.Context, scope Scope, method, path, key string, code int, body json.RawMessage) error {
	if store.completeErr != nil {
		return store.completeErr
	}
	recordKey := scope.PrincipalID + "/" + scope.Mode + "/" + method + "/" + path + "/" + key
	record := store.records[recordKey]
	record.state = "completed"
	record.code = code
	record.body = append(json.RawMessage(nil), body...)
	store.records[recordKey] = record
	return nil
}

func (store *fakeStore) CompleteLinkedIdempotency(_ context.Context, scope Scope, executionID string, code int, body json.RawMessage) error {
	if store.completeErr != nil {
		return store.completeErr
	}
	for key, record := range store.records {
		prefix := scope.PrincipalID + "/" + scope.Mode + "/"
		if !strings.HasPrefix(key, prefix) || record.executionID != executionID || record.state == "completed" {
			continue
		}
		record.state = "completed"
		record.code = code
		record.body = append(json.RawMessage(nil), body...)
		store.records[key] = record
	}
	return nil
}

func (store *fakeStore) GetExecution(_ context.Context, _ Scope, id string) (StoredExecution, error) {
	stored, ok := store.execs[id]
	if !ok {
		return StoredExecution{}, ErrNotFound
	}
	return StoredExecution{Execution: stored.Execution.Snapshot(), Version: stored.Version}, nil
}

func (store *fakeStore) ListNonTerminal(_ context.Context, _ Scope) ([]StoredExecution, error) {
	store.listCalls++
	if store.listEntered != nil {
		store.listEntered <- struct{}{}
	}
	if store.listBlock != nil {
		<-store.listBlock
	}
	if store.listErr != nil {
		return nil, store.listErr
	}
	var result []StoredExecution
	for _, stored := range store.execs {
		switch stored.Execution.State {
		case execution.StateSubmitting, execution.StateLeg1Working, execution.StateLeg2Working, execution.StatePartiallyHedged, execution.StateCompensating, execution.StateUncertain, execution.StateManualInterventionRequired:
			result = append(result, StoredExecution{Execution: stored.Execution.Snapshot(), Version: stored.Version})
		}
	}
	return result, nil
}

func (store *fakeStore) ListTerminalWithIncompleteIdempotency(_ context.Context, scope Scope) ([]StoredExecution, error) {
	seen := make(map[string]struct{})
	var result []StoredExecution
	prefix := scope.PrincipalID + "/" + scope.Mode + "/"
	for recordKey, record := range store.records {
		if !strings.HasPrefix(recordKey, prefix) || record.state == "completed" {
			continue
		}
		if _, ok := seen[record.executionID]; ok {
			continue
		}
		stored, ok := store.execs[record.executionID]
		if !ok || !recoveryTerminal(stored.Execution) {
			continue
		}
		seen[record.executionID] = struct{}{}
		result = append(result, StoredExecution{Execution: stored.Execution.Snapshot(), Version: stored.Version})
	}
	return result, nil
}

func (store *fakeStore) PersistTransition(_ context.Context, input PersistTransitionInput) ([]CommittedEvent, error) {
	if store.beforePersist != nil {
		store.beforePersist(input)
	}
	stored := store.execs[input.Execution.ID]
	if stored.Version != input.ExpectedVersion {
		return nil, fmt.Errorf("version mismatch got %d want %d", input.ExpectedVersion, stored.Version)
	}
	stored.Execution = input.Execution.Snapshot()
	stored.Version++
	store.execs[input.Execution.ID] = stored
	if store.log != nil {
		*store.log = append(*store.log, "persist:"+input.Kind)
	}
	body, _ := json.Marshal(input.Execution)
	return []CommittedEvent{{ID: fmt.Sprintf("event-%d", stored.Version), Kind: input.Kind, EntityID: input.Execution.ID, Payload: body, Occurred: input.OccurredAt}}, nil
}

type fakeGateway struct {
	places          []GatewayOrderRequest
	venueWrites     []GatewayOrderRequest
	cancels         []GatewayCancelRequest
	gets            []string
	fillsCalls      []string
	positionsCalls  []string
	outcomes        map[string]GatewayOrderResult
	orders          map[string]OrderObservation
	fills           map[string][]FillObservation
	gatewayReplays  map[string]bool
	missingFillsErr map[string]bool
	cancelErr       error
	cancelResult    GatewayOrderResult
	getErr          error
	fillsErr        error
	positionsErr    error
	log             *[]string
	placeEntered    chan struct{}
	placeBlock      chan struct{}
	placeOnce       sync.Once
	getHook         func(*fakeGateway, string)
}

func (gateway *fakeGateway) ensure() {
	if gateway.outcomes == nil {
		gateway.outcomes = make(map[string]GatewayOrderResult)
	}
	if gateway.orders == nil {
		gateway.orders = make(map[string]OrderObservation)
	}
	if gateway.fills == nil {
		gateway.fills = make(map[string][]FillObservation)
	}
}

func (gateway *fakeGateway) acceptFull(gatewayID, orderID string, quantity decimal.Decimal) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusFilled}
	gateway.fills[gatewayID] = []FillObservation{{FillID: "fill-" + gatewayID, GatewayFillID: "gfill-" + gatewayID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusFilled}
		gateway.fills[orderID] = []FillObservation{{FillID: "fill-" + orderID, GatewayFillID: "gfill-" + orderID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	}
}

func (gateway *fakeGateway) acceptFullByLookup(gatewayID, orderID, lookupID string, quantity decimal.Decimal) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[lookupID] = OrderObservation{Found: true, GatewayID: lookupID, OrderID: orderID, Status: execution.LegStatusFilled}
	gateway.fills[lookupID] = []FillObservation{{FillID: "fill-" + lookupID, GatewayFillID: "gfill-" + lookupID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
}

func (gateway *fakeGateway) acceptFullWithoutFills(gatewayID, orderID string) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusFilled}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusFilled}
	}
}

func (gateway *fakeGateway) acceptPartial(gatewayID, orderID string, quantity decimal.Decimal) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusPartial}
	gateway.fills[gatewayID] = []FillObservation{{FillID: "fill-" + gatewayID, GatewayFillID: "gfill-" + gatewayID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusPartial}
		gateway.fills[orderID] = []FillObservation{{FillID: "fill-" + orderID, GatewayFillID: "gfill-" + orderID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
	}
}

func (gateway *fakeGateway) acceptPartialByLookup(gatewayID, orderID, lookupID string, quantity decimal.Decimal) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[lookupID] = OrderObservation{Found: true, GatewayID: lookupID, OrderID: orderID, Status: execution.LegStatusPartial}
	gateway.fills[lookupID] = []FillObservation{{FillID: "fill-" + lookupID, GatewayFillID: "gfill-" + lookupID, Quantity: quantity, Price: decimal.NewFromInt(100), OccurredAt: testNow}}
}

func (gateway *fakeGateway) acceptPartialWithoutFills(gatewayID, orderID string) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusPartial}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusPartial}
	}
}

func (gateway *fakeGateway) acceptWorking(gatewayID, orderID string) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: gatewayID, OrderID: orderID}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusWorking}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusWorking}
	}
}

func (gateway *fakeGateway) reject(gatewayID, orderID string) {
	gateway.ensure()
	err := execution.ErrorInfo{Code: "REJECTED", SafeMessage: "order rejected"}
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayRejected, GatewayID: gatewayID, OrderID: orderID, Error: err}
	gateway.orders[gatewayID] = OrderObservation{Found: true, GatewayID: gatewayID, OrderID: orderID, Status: execution.LegStatusRejected, Error: err}
	if strings.TrimSpace(orderID) != "" && orderID != gatewayID {
		gateway.orders[orderID] = OrderObservation{Found: true, GatewayID: orderID, OrderID: orderID, Status: execution.LegStatusRejected, Error: err}
	}
}

func (gateway *fakeGateway) ambiguous(gatewayID string) {
	gateway.ensure()
	gateway.outcomes[gatewayID] = GatewayOrderResult{Outcome: GatewayAmbiguous, GatewayID: gatewayID, Error: execution.ErrorInfo{Code: "UNCERTAIN", SafeMessage: "gateway write uncertain", Retryable: true}}
}

func (gateway *fakeGateway) PlaceOrder(_ context.Context, request GatewayOrderRequest) (GatewayOrderResult, error) {
	gateway.ensure()
	gateway.places = append(gateway.places, request)
	if gateway.placeEntered != nil {
		gateway.placeOnce.Do(func() { close(gateway.placeEntered) })
	}
	if gateway.placeBlock != nil {
		<-gateway.placeBlock
	}
	if gateway.log != nil {
		*gateway.log = append(*gateway.log, "place:"+string(request.Role)+":"+request.GatewayID)
	}
	if !gateway.gatewayReplays[request.GatewayID] {
		gateway.venueWrites = append(gateway.venueWrites, request)
	}
	outcome, ok := gateway.outcomes[request.GatewayID]
	if !ok {
		return GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: request.GatewayID, OrderID: "order-" + request.GatewayID}, nil
	}
	return outcome, nil
}

func (gateway *fakeGateway) CancelOrder(_ context.Context, request GatewayCancelRequest) (GatewayOrderResult, error) {
	gateway.ensure()
	gateway.cancels = append(gateway.cancels, request)
	if gateway.cancelErr != nil {
		return GatewayOrderResult{}, gateway.cancelErr
	}
	if gateway.cancelResult.Outcome != "" {
		result := gateway.cancelResult
		if result.GatewayID == "" {
			result.GatewayID = request.GatewayID
		}
		return result, nil
	}
	order := gateway.orders[request.GatewayID]
	if !order.Found {
		order = OrderObservation{Found: true, GatewayID: request.GatewayID, OrderID: request.GatewayID}
	}
	order.Status = execution.LegStatusCanceled
	gateway.orders[request.GatewayID] = order
	return GatewayOrderResult{Outcome: GatewayAccepted, GatewayID: request.GatewayID}, nil
}

func (gateway *fakeGateway) GetOrder(_ context.Context, _ string, gatewayID string) (OrderObservation, error) {
	gateway.ensure()
	gateway.gets = append(gateway.gets, gatewayID)
	if gateway.getHook != nil {
		gateway.getHook(gateway, gatewayID)
	}
	if gateway.getErr != nil {
		return OrderObservation{}, gateway.getErr
	}
	order, ok := gateway.orders[gatewayID]
	if !ok {
		return OrderObservation{Found: false, GatewayID: gatewayID}, nil
	}
	return order, nil
}

func (gateway *fakeGateway) Fills(_ context.Context, _, _, gatewayID string) ([]FillObservation, error) {
	gateway.ensure()
	gateway.fillsCalls = append(gateway.fillsCalls, gatewayID)
	if gateway.fillsErr != nil {
		return nil, gateway.fillsErr
	}
	if gateway.missingFillsErr[gatewayID] {
		return nil, ErrServiceUnavailable
	}
	return append([]FillObservation(nil), gateway.fills[gatewayID]...), nil
}

func (gateway *fakeGateway) Positions(_ context.Context, accountID, instrumentID string) ([]PositionObservation, error) {
	gateway.positionsCalls = append(gateway.positionsCalls, accountID+"/"+instrumentID)
	if gateway.positionsErr != nil {
		return nil, gateway.positionsErr
	}
	return []PositionObservation{{AccountID: accountID, InstrumentID: instrumentID}}, nil
}

type fakePublisher struct {
	err error
}

func (publisher *fakePublisher) Publish(context.Context, []CommittedEvent) error {
	return publisher.err
}

type fakeRecoveryRepairer struct {
	err    error
	calls  int
	scopes []Scope
}

func (repairer *fakeRecoveryRepairer) Repair(_ context.Context, scope Scope) error {
	repairer.calls++
	repairer.scopes = append(repairer.scopes, scope)
	return repairer.err
}

type fixedClock struct {
	at time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.at
}

type mutableClock struct {
	at time.Time
}

func (clock *mutableClock) Now() time.Time {
	return clock.at
}

type fixedIDs struct{}

func (fixedIDs) NewExecutionID(Scope, string, execution.PreviewResult) string {
	return "exec-1"
}

func logAt(log []string, index int) string {
	if index >= len(log) {
		return "<missing>"
	}
	return log[index]
}
