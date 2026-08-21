package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

var errCrash = errors.New("simulated crash")

type lifecycleWriter struct {
	refs        map[secretstore.OperationID]string
	putCalls    int
	lookupCalls int
	deleteCalls int
	deleteErr   error
	onDelete    func(string) error
}

func (writer *lifecycleWriter) Put(_ context.Context, operation secretstore.OperationID, _ []byte) (string, error) {
	writer.putCalls++
	if ref := writer.refs[operation]; ref != "" {
		return ref, nil
	}
	ref := fmt.Sprintf("opaque-reference-%d", writer.putCalls)
	writer.refs[operation] = ref
	return ref, nil
}

func (writer *lifecycleWriter) Delete(_ context.Context, reference string) error {
	writer.deleteCalls++
	if writer.onDelete != nil {
		if err := writer.onDelete(reference); err != nil {
			return err
		}
	}
	return writer.deleteErr
}

func (writer *lifecycleWriter) LookupOperation(_ context.Context, operation secretstore.OperationID) (string, error) {
	writer.lookupCalls++
	if ref := writer.refs[operation]; ref != "" {
		return ref, nil
	}
	return "", secretstore.ErrSecretNotFound
}

type lifecycleGateway struct {
	applyCalls        int
	getCalls          int
	retireCalls       int
	finalizeCalls     int
	tombstoned        bool
	lastRetire        RetireRequest
	lastFinalize      RotationCleanupRequest
	applyErr          error
	finalizeErr       error
	applyErrByAccount map[string]error
	getErrByAccount   map[string]error
	truth             GatewayRuntimeTruth
	finalization      RotationCleanupProof
	lastRuntimeID     string
}

type createEffectStore struct {
	Store
	findCalls  int
	draftCalls int
}

func (store *createEffectStore) FindCredentialOperation(ctx context.Context, lookup OperationLookup) (CredentialOperation, error) {
	store.findCalls++
	return store.Store.FindCredentialOperation(ctx, lookup)
}

func (store *createEffectStore) CreateDraft(ctx context.Context, account Account, operation CredentialOperation) error {
	store.draftCalls++
	return store.Store.CreateDraft(ctx, account, operation)
}

type ambiguousApplyError struct{ ambiguous bool }

func (err ambiguousApplyError) Error() string                { return "gateway apply failed" }
func (err ambiguousApplyError) ResultMayHaveCommitted() bool { return err.ambiguous }

type codedApplyError struct{ code string }

func (err codedApplyError) Error() string             { return err.code }
func (err codedApplyError) StableFailureCode() string { return err.code }

func (gateway *lifecycleGateway) ApplyDesired(_ context.Context, apply DesiredApply) (GatewayRuntimeTruth, error) {
	gateway.applyCalls++
	gateway.lastRuntimeID = apply.Account.RuntimeID
	if err := gateway.applyErrByAccount[apply.Account.ID]; err != nil {
		return GatewayRuntimeTruth{}, err
	}
	if gateway.applyErr != nil {
		return GatewayRuntimeTruth{}, gateway.applyErr
	}
	if gateway.truth.AccountID != "" {
		if gateway.truth.RuntimeID == "" {
			gateway.truth.RuntimeID = apply.Account.RuntimeID
		}
		return gateway.truth, nil
	}
	routes := make([]GatewayRuntimeRoute, 0, len(apply.Account.Routes))
	for _, route := range apply.Account.Routes {
		routes = append(routes, GatewayRuntimeRoute{RouteID: route.ID, Product: route.Product, SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: route.RequestedState, Ready: route.RequestedState == RouteActive})
	}
	return GatewayRuntimeTruth{OperationID: apply.OperationID, AccountID: apply.Account.ID, RuntimeID: apply.Account.RuntimeID, DesiredStateRevision: apply.Account.DesiredStateRevision, RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion, RequestedState: apply.Account.State, Routes: routes, Active: true, Tombstoned: gateway.tombstoned}, nil
}
func (gateway *lifecycleGateway) GetRuntimeTruth(_ context.Context, accountID string) (GatewayRuntimeTruth, error) {
	gateway.getCalls++
	if err := gateway.getErrByAccount[accountID]; err != nil {
		return GatewayRuntimeTruth{}, err
	}
	truth := gateway.truth
	if truth.RuntimeID == "" {
		truth.RuntimeID = gateway.lastRuntimeID
	}
	return truth, nil
}

func (gateway *lifecycleGateway) FinalizeRotation(_ context.Context, request RotationCleanupRequest) (RotationCleanupProof, error) {
	gateway.finalizeCalls++
	gateway.lastFinalize = request
	if gateway.finalizeErr != nil {
		return RotationCleanupProof{}, gateway.finalizeErr
	}
	if gateway.finalization.OperationID != "" {
		return gateway.finalization, nil
	}
	return exactRotationCleanupProof(request), nil
}

func TestReconcilerCompletesCASFromExactGatewayTruthAfterLostApplyResponse(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33" },
		Hooks: LifecycleHooks{AfterGatewayApply: func() error { return errCrash }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-lost-response"); !errors.Is(err, errCrash) {
		t.Fatalf("create err=%v", err)
	}
	account, err := store.Get(context.Background(), "acct_1")
	if err != nil {
		t.Fatal(err)
	}
	gateway.applyErr = ambiguousApplyError{ambiguous: true}
	gateway.truth = GatewayRuntimeTruth{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: account.ID,
		DesiredStateRevision: account.DesiredStateRevision, RuntimeGeneration: account.RuntimeGeneration,
		CredentialVersion: account.CredentialVersion, RequestedState: account.State, Active: true,
		Routes: []GatewayRuntimeRoute{{RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive, Ready: true}},
	}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Get(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil || recovered.State != StateActive || operation.State != OperationActive || gateway.getCalls != 1 {
		t.Fatalf("account=%+v operation=%+v getCalls=%d err=%v", recovered, operation, gateway.getCalls, err)
	}
}

func TestExactGatewayActiveTruthRejectsOperationAndRouteMismatch(t *testing.T) {
	account := validRoutedAccount()
	operationID := secretstore.OperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	base := GatewayRuntimeTruth{
		OperationID: operationID, AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
		RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		RequestedState: account.State, Active: true,
		Routes: []GatewayRuntimeRoute{{RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive, Ready: true}},
	}
	if !exactGatewayActiveTruth(operationID, account, base) {
		t.Fatal("exact runtime truth rejected")
	}
	for name, mutate := range map[string]func(*GatewayRuntimeTruth){
		"different operation": func(truth *GatewayRuntimeTruth) { truth.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c34" },
		"missing route":       func(truth *GatewayRuntimeTruth) { truth.Routes = nil },
		"extra route": func(truth *GatewayRuntimeTruth) {
			truth.Routes = append(truth.Routes, GatewayRuntimeRoute{RouteID: "extra", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteDisabled})
		},
		"duplicate route": func(truth *GatewayRuntimeTruth) { truth.Routes = append(truth.Routes, truth.Routes[0]) },
		"field mismatch":  func(truth *GatewayRuntimeTruth) { truth.Routes[0].RouteProfileID = "different" },
	} {
		t.Run(name, func(t *testing.T) {
			truth := base
			truth.Routes = append([]GatewayRuntimeRoute(nil), base.Routes...)
			mutate(&truth)
			if exactGatewayActiveTruth(operationID, account, truth) {
				t.Fatal("mismatched runtime truth accepted")
			}
		})
	}
	notReady := base
	notReady.Routes = append([]GatewayRuntimeRoute(nil), base.Routes...)
	notReady.Routes[0].Ready = false
	if !exactGatewayActiveTruth(operationID, account, notReady) {
		t.Fatal("applied account fence was coupled to route readiness")
	}
}

func TestDurableCreateActivatesAccountWhileRuntimeRoutesWarm(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{truth: GatewayRuntimeTruth{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: "acct_1",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
		RequestedState: StateCreating, Active: true,
		Routes: []GatewayRuntimeRoute{{
			RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT",
			RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive, Ready: false,
		}},
	}}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" },
		OperationID: func() secretstore.OperationID {
			return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-warming")
	if err != nil || created.ID != "acct_1" || created.State != StateActive {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil || operation.State != OperationActive {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func TestDurableCreateReturnsAcceptedAccountAfterCandidateProbeFailure(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{applyErr: errors.New("runtime candidate failed")}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" },
		OperationID: func() secretstore.OperationID {
			return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-probe-failed")
	if err != nil || created.ID != "acct_1" || created.State != StateCreating {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil || operation.State != OperationApplying {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func TestFailedCreatingAccountCanBeRemoved(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{applyErr: errors.New("runtime candidate failed")}
	operationIDs := []secretstore.OperationID{
		"018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
		"018f47a8-2578-7d3c-9d7d-5e4f4a445c34",
	}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" },
		OperationID: func() secretstore.OperationID {
			id := operationIDs[0]
			operationIDs = operationIDs[1:]
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-failed")
	if err != nil || created.State != StateCreating {
		t.Fatalf("created=%+v err=%v", created, err)
	}

	removed, err := service.Remove(context.Background(), created.ID, created.DesiredStateRevision, "principal_1", "remove-failed-create")
	if err != nil || removed.State != StateRemoved || removed.SecretRef != "" {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
	createOperation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil || createOperation.State != OperationAbandoned || createOperation.StableErrorCode != "ACCOUNT_REMOVED_DURING_CREATION" {
		t.Fatalf("create operation=%+v err=%v", createOperation, err)
	}
	retireOperation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	if err != nil || retireOperation.State != OperationActive {
		t.Fatalf("retire operation=%+v err=%v", retireOperation, err)
	}
	if gateway.retireCalls != 1 || gateway.lastRetire.AccountID != created.ID || gateway.lastRetire.RuntimeID != created.RuntimeID || writer.deleteCalls != 1 {
		t.Fatalf("retire=%+v calls=%d secret deletes=%d", gateway.lastRetire, gateway.retireCalls, writer.deleteCalls)
	}
}

func TestReconcilerDiscardsLegacyRevisionDriftOnAppliedCreate(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{applyErr: errors.New("initial response lost")}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" },
		OperationID: func() secretstore.OperationID {
			return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-revision-drift")
	if err != nil || created.State != StateCreating {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	drifted := created
	drifted.DesiredStateRevision = 3
	drifted.UpdatedAt = time.Now().UTC()
	if err := store.CompareAndSwap(context.Background(), created.ID, created.DesiredStateRevision, drifted); err != nil {
		t.Fatal(err)
	}

	gateway.applyErr = nil
	gateway.truth = GatewayRuntimeTruth{
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: created.ID,
		RequestedState: StateCreating, Active: true,
		Routes: []GatewayRuntimeRoute{{
			RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT",
			RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive, Ready: false,
		}},
	}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Get(context.Background(), created.ID)
	if err != nil || recovered.State != StateActive || recovered.DesiredStateRevision != 0 || recovered.LastValidatedAt == nil {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func TestReconcilerKeepsDriftedRevisionWhenTargetReplayStillFails(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{applyErr: errors.New("initial response lost")}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" },
		OperationID: func() secretstore.OperationID {
			return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "create-revision-failure")
	if err != nil {
		t.Fatal(err)
	}
	drifted := created
	drifted.DesiredStateRevision = 3
	if err := store.CompareAndSwap(context.Background(), created.ID, created.DesiredStateRevision, drifted); err != nil {
		t.Fatal(err)
	}
	gateway.applyErr = codedApplyError{code: "CREDENTIAL_INVALID"}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 10); err == nil {
		t.Fatal("failed replay reported success")
	}
	persisted, err := store.Get(context.Background(), created.ID)
	if err != nil || persisted.DesiredStateRevision != 3 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil || operation.StableErrorCode != "CREDENTIAL_INVALID" {
		t.Fatalf("operation=%+v err=%v", operation, err)
	}
}

func TestStableApplyFailureCodePreservesActionableGatewayCode(t *testing.T) {
	if got := stableApplyFailureCode(codedApplyError{code: "CREDENTIAL_EXPIRED"}); got != "CREDENTIAL_EXPIRED" {
		t.Fatalf("stableApplyFailureCode = %q", got)
	}
	if got := stableApplyFailureCode(errors.New("opaque")); got != "GATEWAY_APPLY_FAILED" {
		t.Fatalf("opaque stableApplyFailureCode = %q", got)
	}
}

func TestReconcilerDoesNotQueryTruthAfterDeterministicApplyFailure(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33" },
		Hooks: LifecycleHooks{AfterGatewayApply: func() error { return errCrash }},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", "deterministic-apply-failure"); !errors.Is(err, errCrash) {
		t.Fatalf("create err=%v", err)
	}
	gateway.applyErr = ambiguousApplyError{ambiguous: false}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 1); err == nil || gateway.getCalls != 0 {
		t.Fatalf("err=%v getCalls=%d", err, gateway.getCalls)
	}
}

func TestReconcilerContinuesBatchAfterOperationFailure(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	accountIDs := []string{"acct_1", "acct_2", "acct_3"}
	operationIDs := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34", "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { id := accountIDs[0]; accountIDs = accountIDs[1:]; return id },
		OperationID: func() secretstore.OperationID {
			id := operationIDs[0]
			operationIDs = operationIDs[1:]
			return id
		},
		Hooks: LifecycleHooks{AfterGatewayApply: func() error { return errCrash }},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"create-1", "create-2", "create-3"} {
		if _, err := service.Create(context.Background(), durableCreateRequest(), "principal_1", key); !errors.Is(err, errCrash) {
			t.Fatalf("create %s err=%v", key, err)
		}
	}

	gateway.applyErrByAccount = map[string]error{
		"acct_1": ambiguousApplyError{ambiguous: true},
		"acct_2": ambiguousApplyError{ambiguous: true},
	}
	gateway.getErrByAccount = map[string]error{"acct_1": errors.New("runtime truth unavailable")}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	runErr := reconciler.RunOnce(context.Background(), 3)
	if runErr == nil {
		t.Fatal("reconciler accepted failed operation")
	}
	joined, ok := runErr.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("error type=%T, want aggregate", runErr)
	}
	failures := joined.Unwrap()
	if len(failures) != 2 || failures[0].Error() != "GATEWAY_APPLY_FAILED" || failures[1].Error() != "GATEWAY_FENCE_MISMATCH" {
		t.Fatalf("failures=%v", failures)
	}
	first, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	if err != nil {
		t.Fatal(err)
	}
	third, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c35")
	if err != nil {
		t.Fatal(err)
	}
	if first.StableErrorCode != "GATEWAY_APPLY_FAILED" || second.StableErrorCode != "GATEWAY_FENCE_MISMATCH" || third.State != OperationActive {
		t.Fatalf("first=%+v second=%+v third=%+v", first, second, third)
	}
}

func TestReconcilerReturnsSingleOperationErrorUnchanged(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.State = StateRemoving
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	operation := validCredentialOperation(account.ID)
	operation.Kind = OperationRetire
	operation.State = OperationCleanupPending
	if err := store.BeginCredentialOperation(context.Background(), account.DesiredStateRevision, account, operation); err != nil {
		t.Fatal(err)
	}
	reconciler, err := NewReconciler(store, &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}, &lifecycleGateway{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reconciler.SetHooks(LifecycleHooks{AfterGatewayTombstone: func() error { return errCrash }})
	if err := reconciler.RunOnce(context.Background(), 1); err != errCrash {
		t.Fatalf("error=%v type=%T, want original %T", err, err, errCrash)
	}
}

func (gateway *lifecycleGateway) RetireAccount(_ context.Context, request RetireRequest) (GatewayRetirement, error) {
	gateway.retireCalls++
	gateway.lastRetire = request
	return GatewayRetirement{Tombstoned: true}, nil
}

func TestCreateRecoversEveryDurableCrashWindow(t *testing.T) {
	for name, selectHook := range map[string]func(*LifecycleHooks){
		"draft":                 func(hooks *LifecycleHooks) { hooks.AfterDraftCommit = func() error { return errCrash } },
		"reference binding":     func(hooks *LifecycleHooks) { hooks.AfterReferenceBinding = func() error { return errCrash } },
		"gateway apply":         func(hooks *LifecycleHooks) { hooks.AfterGatewayApply = func() error { return errCrash } },
		"promotion observation": func(hooks *LifecycleHooks) { hooks.AfterPromotionObservation = func() error { return errCrash } },
	} {
		t.Run(name, func(t *testing.T) {
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			gateway := &lifecycleGateway{}
			hooks := LifecycleHooks{}
			selectHook(&hooks)
			service, _ := New(Options{Store: NewMemoryStore(), Secrets: writer, Audit: &auditWriter{}, Gateway: gateway, ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33" }, Hooks: hooks})
			request := durableCreateRequest()
			if _, err := service.Create(context.Background(), request, "principal_1", "create-1"); !errors.Is(err, errCrash) {
				t.Fatalf("crash err=%v", err)
			}
			service.Hooks = LifecycleHooks{}
			account, err := service.Create(context.Background(), request, "principal_1", "create-1")
			if err != nil || account.State != StateActive {
				t.Fatalf("account=%+v err=%v", account, err)
			}
		})
	}
}

func TestDurableCreateValidatesGeneratedIdentityBeforeEffects(t *testing.T) {
	tests := []struct {
		name        string
		accountID   string
		operationID secretstore.OperationID
		principal   string
	}{
		{name: "account id", accountID: " acct_1 ", operationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", principal: "principal_1"},
		{name: "operation id", accountID: "acct_1", operationID: "not-a-uuid", principal: "principal_1"},
		{name: "principal", accountID: "acct_1", operationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", principal: " principal_1 "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			gateway := &lifecycleGateway{}
			service, err := New(Options{
				Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
				ID: func() string { return test.accountID }, OperationID: func() secretstore.OperationID { return test.operationID },
			})
			if err != nil {
				t.Fatal(err)
			}
			if account, err := service.Create(context.Background(), durableCreateRequest(), test.principal, "create-invalid"); err == nil {
				t.Fatalf("invalid create returned account=%+v", account)
			}
			accounts, err := store.List(context.Background())
			if err != nil || len(accounts) != 0 || writer.putCalls != 0 || gateway.applyCalls != 0 {
				t.Fatalf("accounts=%+v err=%v puts=%d applies=%d", accounts, err, writer.putCalls, gateway.applyCalls)
			}
		})
	}
}

func TestCredentialReplacementRequiresDeleteAndRecreate(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.State = StateActive
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	service, err := New(Options{Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Rotate(context.Background(), account.ID, 0, durableCreateRequest().CredentialBundle, "principal_1", "rotate")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("credential replacement err=%v", err)
	}
	persisted, err := store.Get(context.Background(), account.ID)
	if err != nil || persisted.SecretRef != account.SecretRef || writer.putCalls != 0 || gateway.applyCalls != 0 {
		t.Fatalf("persisted=%+v err=%v puts=%d applies=%d", persisted, err, writer.putCalls, gateway.applyCalls)
	}
}

func TestRotationCleanupBlocksFenceMutatingMetadataUpdate(t *testing.T) {
	store := NewMemoryStore()
	account, _ := seedRotationCleanup(t, store, OperationCleanupPending)
	service, err := New(Options{Store: store, Secrets: &lifecycleWriter{}, Audit: &auditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateMetadata(context.Background(), account.ID, account.DesiredStateRevision, "Renamed", []string{"manual"}, "principal_1"); !errors.Is(err, ErrAccountBlocked) {
		t.Fatalf("metadata update err=%v", err)
	}
	current, err := store.Get(context.Background(), account.ID)
	if err != nil || current.DisplayName != account.DisplayName || current.DesiredStateRevision != account.DesiredStateRevision {
		t.Fatalf("current=%+v account=%+v err=%v", current, account, err)
	}
}

func TestMemoryStoreRejectsSecondRecoverableCredentialOperationForAccount(t *testing.T) {
	store := NewMemoryStore()
	account, _ := seedRotationCleanup(t, store, OperationCleanupPending)
	operation := validCredentialOperation(account.ID)
	operation.ID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"
	operation.IdempotencyKeyDigest = "second-idempotency-digest"
	operation.State = OperationAwaitingMaterial
	if err := store.BeginCredentialOperation(context.Background(), account.DesiredStateRevision, account, operation); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("second recoverable operation err=%v", err)
	}
}

func TestRemoveRecoversAfterProviderDeleteResponseWasLost(t *testing.T) {
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"}
	service, _ := New(Options{Store: NewMemoryStore(), Secrets: writer, Audit: &auditWriter{}, Gateway: gateway, ID: func() string { return "acct_2" }, OperationID: func() secretstore.OperationID { id := ids[0]; ids = ids[1:]; return id }})
	created, _ := service.Create(context.Background(), CreateRequest{Venue: "binance", VenueEnvironment: EnvironmentLive, DisplayName: "Primary", ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret")}, "principal_1")
	active, _ := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	service.Hooks.AfterProviderDelete = func() error { return errCrash }
	if _, err := service.Remove(context.Background(), active.ID, active.DesiredStateRevision, "principal_1", "remove-2"); !errors.Is(err, errCrash) {
		t.Fatalf("remove crash=%v", err)
	}
	service.Hooks = LifecycleHooks{}
	removed, err := service.Remove(context.Background(), active.ID, active.DesiredStateRevision, "principal_1", "remove-2")
	if err != nil || removed.State != StateRemoved || writer.deleteCalls != 2 {
		t.Fatalf("removed=%+v deletes=%d err=%v", removed, writer.deleteCalls, err)
	}
}

func TestReconcilerKeepsRetirementPendingWhenSecretDeletionFails(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_3" }, OperationID: func() secretstore.OperationID { id := ids[0]; ids = ids[1:]; return id },
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{
		Venue: "binance", VenueEnvironment: EnvironmentLive, DisplayName: "Primary",
		ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret"),
	}, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	service.Hooks.AfterGatewayTombstone = func() error { return errCrash }
	if _, err := service.Remove(context.Background(), active.ID, active.DesiredStateRevision, "principal_1", "remove-3"); !errors.Is(err, errCrash) {
		t.Fatalf("remove crash=%v", err)
	}

	writer.deleteErr = errors.New("provider delete failed")
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 10); err == nil {
		t.Fatal("reconciler accepted failed credential deletion")
	}
	pending, err := store.Get(context.Background(), active.ID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), "018f47a8-2578-7d3c-9d7d-5e4f4a445c34")
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != StateRemoving || pending.SecretRef == "" || operation.State != OperationCleanupPending || operation.StableErrorCode != "SECRET_DELETE_FAILED" {
		t.Fatalf("account=%+v operation=%+v", pending, operation)
	}

	writer.deleteErr = nil
	if err := reconciler.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	removed, err := store.Get(context.Background(), active.ID)
	if err != nil || removed.State != StateRemoved || removed.SecretRef != "" {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
}

type rotationProofStoreFailure struct {
	Store
	err error
}

func (store rotationProofStoreFailure) RecordRotationCleanupProof(context.Context, RotationCleanupProof) (CredentialOperation, error) {
	return CredentialOperation{}, store.err
}

func TestRotationCleanupPersistsExactProofBeforeDeletingPreviousSecret(t *testing.T) {
	store := NewMemoryStore()
	account, operation := seedRotationCleanup(t, store, OperationCleanupPending)
	gateway := &lifecycleGateway{}
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	writer.onDelete = func(reference string) error {
		if reference != account.PreviousSecretRef {
			return fmt.Errorf("deleted reference %q", reference)
		}
		persisted, err := store.GetCredentialOperation(context.Background(), operation.ID)
		if err != nil {
			return err
		}
		if persisted.RotationCleanupTombstoneRevision != account.DesiredStateRevision || persisted.RotationCleanupCompletedAt.IsZero() {
			return errors.New("provider delete ran before durable rotation cleanup proof")
		}
		return nil
	}
	reconciler, err := NewReconciler(store, writer, gateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	assertRotationCleanupComplete(t, store, account.ID, operation.ID)
	if gateway.finalizeCalls != 1 || writer.deleteCalls != 1 {
		t.Fatalf("finalizeCalls=%d deleteCalls=%d", gateway.finalizeCalls, writer.deleteCalls)
	}
}

func TestRotationCleanupFailuresRetainPendingOperationAndPreviousSecret(t *testing.T) {
	tests := map[string]func(*MemoryStore, *lifecycleWriter, *lifecycleGateway) Store{
		"gateway finalization": func(store *MemoryStore, _ *lifecycleWriter, gateway *lifecycleGateway) Store {
			gateway.finalizeErr = errors.New("finalization unavailable")
			return store
		},
		"proof persistence": func(store *MemoryStore, _ *lifecycleWriter, _ *lifecycleGateway) Store {
			return rotationProofStoreFailure{Store: store, err: errors.New("proof persistence failed")}
		},
		"provider deletion": func(store *MemoryStore, writer *lifecycleWriter, _ *lifecycleGateway) Store {
			writer.deleteErr = errors.New("provider deletion failed")
			return store
		},
	}
	for name, arrange := range tests {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			account, operation := seedRotationCleanup(t, store, OperationCleanupPending)
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			gateway := &lifecycleGateway{}
			reconciler, err := NewReconciler(arrange(store, writer, gateway), writer, gateway, fixedRotationCleanupTime)
			if err != nil {
				t.Fatal(err)
			}
			if err := reconciler.RunOnce(context.Background(), 1); err == nil {
				t.Fatal("rotation cleanup failure was accepted")
			}
			assertRotationCleanupPending(t, store, account.ID, operation.ID, account.PreviousSecretRef)
			if name != "provider deletion" && writer.deleteCalls != 0 {
				t.Fatalf("deleteCalls=%d", writer.deleteCalls)
			}
		})
	}
}

func TestRotationCleanupRejectsMismatchedOrNonTerminalGatewayProof(t *testing.T) {
	mutations := map[string]func(*RotationCleanupProof){
		"active fence":           func(proof *RotationCleanupProof) { proof.ActiveFence.RuntimeGeneration++ },
		"retiring fence":         func(proof *RotationCleanupProof) { proof.RetiringFence.CredentialVersion++ },
		"tombstone revision":     func(proof *RotationCleanupProof) { proof.TombstoneRevision++ },
		"retiring state":         func(proof *RotationCleanupProof) { proof.RetiringState = "draining" },
		"secret reference state": func(proof *RotationCleanupProof) { proof.SecretReferenceState = "active" },
		"completion time":        func(proof *RotationCleanupProof) { proof.CompletedAt = time.Time{} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			store := NewMemoryStore()
			account, operation := seedRotationCleanup(t, store, OperationCleanupPending)
			request := rotationCleanupRequest(account, operation)
			proof := exactRotationCleanupProof(request)
			mutate(&proof)
			gateway := &lifecycleGateway{finalization: proof}
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			reconciler, err := NewReconciler(store, writer, gateway, fixedRotationCleanupTime)
			if err != nil {
				t.Fatal(err)
			}
			if err := reconciler.RunOnce(context.Background(), 1); err == nil {
				t.Fatal("invalid rotation cleanup proof was accepted")
			}
			assertRotationCleanupPending(t, store, account.ID, operation.ID, account.PreviousSecretRef)
			persisted, err := store.GetCredentialOperation(context.Background(), operation.ID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.RotationCleanupTombstoneRevision != 0 || !persisted.RotationCleanupCompletedAt.IsZero() || writer.deleteCalls != 0 {
				t.Fatalf("operation=%+v deleteCalls=%d", persisted, writer.deleteCalls)
			}
		})
	}
}

func TestRotationCleanupResumesFromDurableProofAfterRestart(t *testing.T) {
	store := NewMemoryStore()
	account, operation := seedRotationCleanup(t, store, OperationCleanupPending)
	firstGateway := &lifecycleGateway{}
	firstWriter := &lifecycleWriter{refs: make(map[secretstore.OperationID]string), deleteErr: errors.New("provider unavailable")}
	first, err := NewReconciler(store, firstWriter, firstGateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.RunOnce(context.Background(), 1); err == nil {
		t.Fatal("failed provider delete was accepted")
	}
	persisted, err := store.GetCredentialOperation(context.Background(), operation.ID)
	if err != nil || persisted.RotationCleanupTombstoneRevision == 0 || persisted.RotationCleanupCompletedAt.IsZero() {
		t.Fatalf("operation=%+v err=%v", persisted, err)
	}

	secondGateway := &lifecycleGateway{finalizeErr: errors.New("Gateway must not be called after proof persistence")}
	secondWriter := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	second, err := NewReconciler(store, secondWriter, secondGateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.RunOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	assertRotationCleanupComplete(t, store, account.ID, operation.ID)
	if firstGateway.finalizeCalls != 1 || secondGateway.finalizeCalls != 0 || secondWriter.deleteCalls != 1 {
		t.Fatalf("firstFinalize=%d secondFinalize=%d secondDelete=%d", firstGateway.finalizeCalls, secondGateway.finalizeCalls, secondWriter.deleteCalls)
	}
}

func TestRotationCleanupReplaysIdempotentDeleteAfterLostResponse(t *testing.T) {
	store := NewMemoryStore()
	account, operation := seedRotationCleanup(t, store, OperationCleanupPending)
	gateway := &lifecycleGateway{}
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	first, err := NewReconciler(store, writer, gateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	first.SetHooks(LifecycleHooks{AfterProviderDelete: func() error { return errCrash }})
	if err := first.RunOnce(context.Background(), 1); !errors.Is(err, errCrash) {
		t.Fatalf("first run err=%v", err)
	}
	assertRotationCleanupPending(t, store, account.ID, operation.ID, account.PreviousSecretRef)

	second, err := NewReconciler(store, writer, gateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.RunOnce(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	assertRotationCleanupComplete(t, store, account.ID, operation.ID)
	if gateway.finalizeCalls != 1 || writer.deleteCalls != 2 {
		t.Fatalf("finalizeCalls=%d deleteCalls=%d", gateway.finalizeCalls, writer.deleteCalls)
	}
}

func TestRecoveredRotationApplyDoesNotTreatRuntimeTombstoneAsCleanupAuthorization(t *testing.T) {
	store := NewMemoryStore()
	account, operation := seedRotationCleanup(t, store, OperationApplying)
	account.State = StateRotating
	if err := store.CompareAndSwap(context.Background(), account.ID, account.DesiredStateRevision, account); err != nil {
		t.Fatal(err)
	}
	gateway := &lifecycleGateway{tombstoned: true, finalizeErr: errors.New("exact finalization proof unavailable")}
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	reconciler, err := NewReconciler(store, writer, gateway, fixedRotationCleanupTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 1); err == nil {
		t.Fatal("runtime tombstone authorized rotation cleanup")
	}
	assertRotationCleanupPending(t, store, account.ID, operation.ID, account.PreviousSecretRef)
	if writer.deleteCalls != 0 || gateway.finalizeCalls != 1 {
		t.Fatalf("deleteCalls=%d finalizeCalls=%d", writer.deleteCalls, gateway.finalizeCalls)
	}
}

func seedRotationCleanup(t *testing.T, store *MemoryStore, state OperationState) (Account, CredentialOperation) {
	t.Helper()
	now := fixedRotationCleanupTime()
	account := validRoutedAccount()
	account.SecretRef = "new-opaque-reference"
	account.PreviousSecretRef = "old-opaque-reference"
	account.DesiredStateRevision = 2
	account.RuntimeGeneration = 2
	account.CredentialVersion = 2
	account.State = StateActive
	account.Rotation = &RotationFence{
		ID: "rotation-1", State: "promoted", FromCredentialVersion: 1, ToCredentialVersion: 2,
		FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: now.Add(-time.Minute),
	}
	account.UpdatedAt = now.Add(-time.Second)
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	operation := validCredentialOperation(account.ID)
	operation.Kind = OperationRotate
	operation.State = state
	operation.SecretRef = account.SecretRef
	operation.TargetDesiredStateRevision = account.DesiredStateRevision
	operation.TargetRuntimeGeneration = account.RuntimeGeneration
	operation.TargetCredentialVersion = account.CredentialVersion
	operation.UpdatedAt = account.UpdatedAt
	if err := store.BeginCredentialOperation(context.Background(), account.DesiredStateRevision, account, operation); err != nil {
		t.Fatal(err)
	}
	return account, operation
}

func rotationCleanupRequest(account Account, operation CredentialOperation) RotationCleanupRequest {
	return RotationCleanupRequest{
		OperationID: operation.ID,
		AccountID:   account.ID,
		ExpectedActiveFence: AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision,
			RuntimeGeneration: account.RuntimeGeneration, CredentialVersion: account.CredentialVersion,
		},
		ExpectedRetiringFence: AccountFence{
			AccountID: account.ID, DesiredStateRevision: account.DesiredStateRevision - 1,
			RuntimeGeneration: account.RuntimeGeneration - 1, CredentialVersion: account.CredentialVersion - 1,
		},
	}
}

func exactRotationCleanupProof(request RotationCleanupRequest) RotationCleanupProof {
	return RotationCleanupProof{
		OperationID: request.OperationID, AccountID: request.AccountID,
		ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
		TombstoneRevision: request.ExpectedActiveFence.DesiredStateRevision,
		RetiringState:     "retired", SecretReferenceState: "tombstoned",
		CompletedAt: fixedRotationCleanupTime(),
	}
}

func fixedRotationCleanupTime() time.Time {
	return time.Date(2026, 8, 9, 8, 9, 10, 0, time.UTC)
}

func assertRotationCleanupPending(t *testing.T, store *MemoryStore, accountID string, operationID secretstore.OperationID, previousRef string) {
	t.Helper()
	account, err := store.Get(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	if account.PreviousSecretRef != previousRef || operation.State != OperationCleanupPending {
		t.Fatalf("account=%+v operation=%+v", account, operation)
	}
}

func assertRotationCleanupComplete(t *testing.T, store *MemoryStore, accountID string, operationID secretstore.OperationID) {
	t.Helper()
	account, err := store.Get(context.Background(), accountID)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := store.GetCredentialOperation(context.Background(), operationID)
	if err != nil {
		t.Fatal(err)
	}
	if account.State != StateActive || account.PreviousSecretRef != "" || operation.State != OperationActive || operation.RotationCleanupTombstoneRevision == 0 || operation.RotationCleanupCompletedAt.IsZero() {
		t.Fatalf("account=%+v operation=%+v", account, operation)
	}
}

func durableCreateRequest() CreateRequest {
	return CreateRequest{Venue: "binance", DisplayName: "Primary", ProductPolicy: []string{"manual"}, Routes: []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive}}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`)}
}

func TestRotateAndRemoveRejectMissingIdempotencyKeysWithoutEffects(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.State = StateActive
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	service, err := New(Options{Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []struct {
		name string
		keys []string
	}{{name: "missing"}, {name: "blank", keys: []string{" \t "}}} {
		t.Run("rotate "+key.name, func(t *testing.T) {
			_, err := service.Rotate(context.Background(), account.ID, account.DesiredStateRevision, durableCreateRequest().CredentialBundle, "principal_1", key.keys...)
			if !errors.Is(err, ErrInvalidTransition) {
				t.Fatalf("err=%v", err)
			}
		})
		t.Run("remove "+key.name, func(t *testing.T) {
			_, err := service.Remove(context.Background(), account.ID, account.DesiredStateRevision, "principal_1", key.keys...)
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	persisted, err := store.Get(context.Background(), account.ID)
	if err != nil || persisted.DesiredStateRevision != account.DesiredStateRevision || writer.putCalls != 0 || writer.deleteCalls != 0 || gateway.applyCalls != 0 || gateway.retireCalls != 0 {
		t.Fatalf("account=%+v err=%v puts=%d deletes=%d apply=%d retire=%d", persisted, err, writer.putCalls, writer.deleteCalls, gateway.applyCalls, gateway.retireCalls)
	}
}

func TestDurableCreateRejectsInvalidPolicyAndRoutesBeforeEffects(t *testing.T) {
	thirtyThreeRoutes := make([]AccountRoute, 33)
	for index := range thirtyThreeRoutes {
		thirtyThreeRoutes[index] = AccountRoute{ID: fmt.Sprintf("perp-usdt-%02d", index), Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive}
	}
	tests := []struct {
		name   string
		mutate func(*CreateRequest)
	}{
		{name: "blank display name", mutate: func(request *CreateRequest) { request.DisplayName = " \t " }},
		{name: "empty product policy", mutate: func(request *CreateRequest) { request.ProductPolicy = nil }},
		{name: "duplicate product policy", mutate: func(request *CreateRequest) { request.ProductPolicy = []string{"manual", "manual"} }},
		{name: "unknown product policy", mutate: func(request *CreateRequest) { request.ProductPolicy = []string{"unknown"} }},
		{name: "too many routes", mutate: func(request *CreateRequest) { request.Routes = thirtyThreeRoutes }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &createEffectStore{Store: NewMemoryStore()}
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			gateway := &lifecycleGateway{}
			service, err := New(Options{
				Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
				ID: func() string { return "acct_valid" }, OperationID: func() secretstore.OperationID { return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33" },
			})
			if err != nil {
				t.Fatal(err)
			}
			request := durableCreateRequest()
			test.mutate(&request)
			if _, err := service.Create(context.Background(), request, "principal_1", "create-invalid"); !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
			if store.findCalls != 0 || store.draftCalls != 0 || writer.putCalls != 0 || gateway.applyCalls != 0 {
				t.Fatalf("find=%d draft=%d put=%d apply=%d", store.findCalls, store.draftCalls, writer.putCalls, gateway.applyCalls)
			}
		})
	}
}

func TestDurableCreateValidatesGeneratedIdentityBeforeOperationLookup(t *testing.T) {
	tests := []struct {
		name        string
		accountID   string
		operationID secretstore.OperationID
		principal   string
	}{
		{name: "account id", accountID: " invalid ", operationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", principal: "principal_1"},
		{name: "operation id", accountID: "acct_valid", operationID: "not-a-uuid", principal: "principal_1"},
		{name: "principal", accountID: "acct_valid", operationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", principal: " principal_1 "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &createEffectStore{Store: NewMemoryStore()}
			writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
			gateway := &lifecycleGateway{}
			service, err := New(Options{
				Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
				ID: func() string { return test.accountID }, OperationID: func() secretstore.OperationID { return test.operationID },
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Create(context.Background(), durableCreateRequest(), test.principal, "create-invalid-identity"); !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
			if store.findCalls != 0 || store.draftCalls != 0 || writer.putCalls != 0 || gateway.applyCalls != 0 {
				t.Fatalf("find=%d draft=%d put=%d apply=%d", store.findCalls, store.draftCalls, writer.putCalls, gateway.applyCalls)
			}
		})
	}
}

func TestCreateRecoversReferenceAfterPutResponseWasLost(t *testing.T) {
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	service, err := New(Options{
		Store: NewMemoryStore(), Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { return "018f47a8-2578-7d3c-9d7d-5e4f4a445c33" },
		Hooks: LifecycleHooks{AfterProviderPut: func() error { return errCrash }},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := CreateRequest{Venue: "binance", DisplayName: "Primary", ProductPolicy: []string{"manual"}, Routes: []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive}}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`)}
	_, err = service.Create(context.Background(), request, "principal_1", "create-1")
	if !errors.Is(err, errCrash) {
		t.Fatalf("err=%v", err)
	}
	service.Hooks.AfterProviderPut = nil
	account, err := service.Create(context.Background(), request, "principal_1", "create-1")
	if err != nil {
		t.Fatal(err)
	}
	if writer.putCalls != 1 || writer.lookupCalls != 1 || gateway.applyCalls != 1 {
		t.Fatalf("put=%d lookup=%d apply=%d", writer.putCalls, writer.lookupCalls, gateway.applyCalls)
	}
	if account.State != StateActive {
		t.Fatalf("state=%s", account.State)
	}
	changed := request
	changed.DisplayName = "Changed"
	if _, err := service.Create(context.Background(), changed, "principal_1", "create-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed replay err=%v", err)
	}
	if writer.putCalls != 1 || gateway.applyCalls != 1 {
		t.Fatal("changed replay caused side effects")
	}
}
