package accounts

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
}

type ambiguousApplyError struct{ ambiguous bool }

func (err ambiguousApplyError) Error() string                { return "gateway apply failed" }
func (err ambiguousApplyError) ResultMayHaveCommitted() bool { return err.ambiguous }

func (gateway *lifecycleGateway) ApplyDesired(_ context.Context, apply DesiredApply) (GatewayRuntimeTruth, error) {
	gateway.applyCalls++
	if err := gateway.applyErrByAccount[apply.Account.ID]; err != nil {
		return GatewayRuntimeTruth{}, err
	}
	if gateway.applyErr != nil {
		return GatewayRuntimeTruth{}, gateway.applyErr
	}
	if gateway.truth.AccountID != "" {
		return gateway.truth, nil
	}
	routes := make([]GatewayRuntimeRoute, 0, len(apply.Account.Routes))
	for _, route := range apply.Account.Routes {
		routes = append(routes, GatewayRuntimeRoute{RouteID: route.ID, Product: route.Product, SettlementAsset: route.SettlementAsset, RouteProfileID: route.RouteProfileID, RequestedState: route.RequestedState, Ready: route.RequestedState == RouteActive})
	}
	return GatewayRuntimeTruth{OperationID: apply.OperationID, AccountID: apply.Account.ID, DesiredStateRevision: apply.Account.DesiredStateRevision, RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion, RequestedState: apply.Account.State, Routes: routes, Active: true, Tombstoned: gateway.tombstoned}, nil
}
func (gateway *lifecycleGateway) GetRuntimeTruth(_ context.Context, accountID string) (GatewayRuntimeTruth, error) {
	gateway.getCalls++
	if err := gateway.getErrByAccount[accountID]; err != nil {
		return GatewayRuntimeTruth{}, err
	}
	return gateway.truth, nil
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
		Routes: []GatewayRuntimeRoute{{RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive, Ready: true}},
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
		Routes: []GatewayRuntimeRoute{{RouteID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive, Ready: true}},
	}
	if !exactGatewayActiveTruth(operationID, account, base) {
		t.Fatal("exact runtime truth rejected")
	}
	for name, mutate := range map[string]func(*GatewayRuntimeTruth){
		"different operation": func(truth *GatewayRuntimeTruth) { truth.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c34" },
		"missing route":       func(truth *GatewayRuntimeTruth) { truth.Routes = nil },
		"extra route": func(truth *GatewayRuntimeTruth) {
			truth.Routes = append(truth.Routes, GatewayRuntimeRoute{RouteID: "extra", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteDisabled})
		},
		"duplicate route":  func(truth *GatewayRuntimeTruth) { truth.Routes = append(truth.Routes, truth.Routes[0]) },
		"field mismatch":   func(truth *GatewayRuntimeTruth) { truth.Routes[0].RouteProfileID = "different" },
		"active not ready": func(truth *GatewayRuntimeTruth) { truth.Routes[0].Ready = false },
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

func TestRotateAndRemoveRecoverCleanupWindows(t *testing.T) {
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{tombstoned: true}
	store := NewMemoryStore()
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34", "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"}
	service, _ := New(Options{Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway, ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { id := ids[0]; ids = ids[1:]; return id }})
	created, err := service.Create(context.Background(), CreateRequest{Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary", ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret")}, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	bundle := durableCreateRequest().CredentialBundle
	rotated, err := service.Rotate(context.Background(), active.ID, active.DesiredStateRevision, bundle, "principal_1", "rotate-1")
	if err != nil || rotated.State != StateActive || rotated.PreviousSecretRef == "" || writer.deleteCalls != 0 {
		t.Fatalf("rotated=%+v deletes=%d err=%v", rotated, writer.deleteCalls, err)
	}
	reconciler, err := NewReconciler(store, writer, gateway, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.RunOnce(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	rotated, err = store.Get(context.Background(), rotated.ID)
	if err != nil || rotated.State != StateActive || rotated.PreviousSecretRef != "" || writer.deleteCalls != 1 {
		t.Fatalf("reconciled rotation=%+v deletes=%d err=%v", rotated, writer.deleteCalls, err)
	}
	service.Hooks.AfterGatewayTombstone = func() error { return errCrash }
	if _, err := service.Remove(context.Background(), rotated.ID, rotated.DesiredStateRevision, "principal_1", "remove-1"); !errors.Is(err, errCrash) {
		t.Fatalf("remove crash=%v", err)
	}
	service.Hooks = LifecycleHooks{}
	removed, err := service.Remove(context.Background(), rotated.ID, rotated.DesiredStateRevision, "principal_1", "remove-1")
	if err != nil || removed.State != StateRemoved || removed.SecretRef != "" || gateway.retireCalls != 2 {
		t.Fatalf("removed=%+v retires=%d err=%v", removed, gateway.retireCalls, err)
	}
	if gateway.lastRetire.DesiredStateRevision != rotated.DesiredStateRevision+1 || gateway.lastRetire.RuntimeGeneration != rotated.RuntimeGeneration || gateway.lastRetire.CredentialVersion != rotated.CredentialVersion {
		t.Fatalf("retire fence=%+v removed=%+v", gateway.lastRetire, removed)
	}
}

func TestRotateReplayFromCleanupPendingReturnsCurrentAccountWithoutReapplying(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_rotation_replay" },
		OperationID: func() secretstore.OperationID {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{
		Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary",
		ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret"),
	}, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	originalRevision := active.DesiredStateRevision
	bundle := durableCreateRequest().CredentialBundle
	first, err := service.Rotate(context.Background(), active.ID, originalRevision, bundle, "principal_1", "rotate-replay")
	if err != nil || first.PreviousSecretRef == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	replayed, err := service.Rotate(context.Background(), active.ID, originalRevision, bundle, "principal_1", "rotate-replay")
	if err != nil || replayed.DesiredStateRevision != first.DesiredStateRevision || replayed.PreviousSecretRef != first.PreviousSecretRef {
		t.Fatalf("replayed=%+v first=%+v err=%v", replayed, first, err)
	}
	if writer.putCalls != 2 || gateway.applyCalls != 1 {
		t.Fatalf("putCalls=%d applyCalls=%d", writer.putCalls, gateway.applyCalls)
	}
}

func TestRotationCleanupBlocksSecondRotation(t *testing.T) {
	store := NewMemoryStore()
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34", "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"}
	service, err := New(Options{
		Store: store, Secrets: writer, Audit: &auditWriter{}, Gateway: gateway,
		ID: func() string { return "acct_double_rotation" },
		OperationID: func() secretstore.OperationID {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(context.Background(), CreateRequest{
		Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary",
		ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret"),
	}, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	active, err := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Rotate(context.Background(), active.ID, active.DesiredStateRevision, durableCreateRequest().CredentialBundle, "principal_1", "rotate-first")
	if err != nil || first.PreviousSecretRef == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	secondBundle := []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"other-key","apiSecret":"other-secret"}}`)
	if _, err := service.Rotate(context.Background(), active.ID, first.DesiredStateRevision, secondBundle, "principal_1", "rotate-second"); !errors.Is(err, ErrAccountBlocked) {
		t.Fatalf("second rotation err=%v", err)
	}
	current, err := store.Get(context.Background(), active.ID)
	if err != nil || current.PreviousSecretRef != first.PreviousSecretRef || current.SecretRef != first.SecretRef || current.DesiredStateRevision != first.DesiredStateRevision {
		t.Fatalf("current=%+v first=%+v err=%v", current, first, err)
	}
	if writer.putCalls != 2 || gateway.applyCalls != 1 {
		t.Fatalf("putCalls=%d applyCalls=%d", writer.putCalls, gateway.applyCalls)
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
	created, _ := service.Create(context.Background(), CreateRequest{Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary", ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret")}, "principal_1")
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
		Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary",
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
	return CreateRequest{Venue: "binance", DisplayName: "Primary", ProductPolicy: []string{"manual"}, Routes: []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive}}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`)}
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
	request := CreateRequest{Venue: "binance", DisplayName: "Primary", ProductPolicy: []string{"manual"}, Routes: []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.demo.v1", RequestedState: RouteActive}}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`)}
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
