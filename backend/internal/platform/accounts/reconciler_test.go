package accounts

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

var errCrash = errors.New("simulated crash")

type lifecycleWriter struct {
	refs        map[secretstore.OperationID]string
	putCalls    int
	lookupCalls int
	deleteCalls int
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

func (writer *lifecycleWriter) Delete(_ context.Context, _ string) error {
	writer.deleteCalls++
	return nil
}

func (writer *lifecycleWriter) LookupOperation(_ context.Context, operation secretstore.OperationID) (string, error) {
	writer.lookupCalls++
	if ref := writer.refs[operation]; ref != "" {
		return ref, nil
	}
	return "", secretstore.ErrSecretNotFound
}

type lifecycleGateway struct {
	applyCalls  int
	retireCalls int
	tombstoned  bool
}

func (gateway *lifecycleGateway) ApplyDesired(_ context.Context, apply DesiredApply) (GatewayRuntimeTruth, error) {
	gateway.applyCalls++
	return GatewayRuntimeTruth{AccountID: apply.Account.ID, DesiredStateRevision: apply.Account.DesiredStateRevision, RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion, Active: true, Tombstoned: gateway.tombstoned}, nil
}
func (gateway *lifecycleGateway) GetRuntimeTruth(context.Context, string) (GatewayRuntimeTruth, error) {
	return GatewayRuntimeTruth{}, nil
}
func (gateway *lifecycleGateway) RetireAccount(context.Context, RetireRequest) (GatewayRetirement, error) {
	gateway.retireCalls++
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
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34", "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"}
	service, _ := New(Options{Store: NewMemoryStore(), Secrets: writer, Audit: &auditWriter{}, Gateway: gateway, ID: func() string { return "acct_1" }, OperationID: func() secretstore.OperationID { id := ids[0]; ids = ids[1:]; return id }})
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
	if err != nil || rotated.State != StateActive || rotated.PreviousSecretRef != "" || writer.deleteCalls != 1 {
		t.Fatalf("rotated=%+v deletes=%d err=%v", rotated, writer.deleteCalls, err)
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
}

func TestRemoveRecoversAfterProviderDeleteResponseWasLost(t *testing.T) {
	writer := &lifecycleWriter{refs: make(map[secretstore.OperationID]string)}
	gateway := &lifecycleGateway{}
	ids := []secretstore.OperationID{"018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"}
	service, _ := New(Options{Store: NewMemoryStore(), Secrets: writer, Audit: &auditWriter{}, Gateway: gateway, ID: func() string { return "acct_2" }, OperationID: func() secretstore.OperationID { id := ids[0]; ids = ids[1:]; return id }})
	created, _ := service.Create(context.Background(), CreateRequest{Venue: "binance", VenueEnvironment: EnvironmentDemo, DisplayName: "Primary", ProductIDs: []string{"manual"}, SecretMaterial: []byte("legacy-secret")}, "principal_1")
	active, _ := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "principal_1")
	service.Hooks.AfterProviderDelete = func() error { return errCrash }
	if _, err := service.Remove(context.Background(), active.ID, active.DesiredStateRevision, "principal_1", "remove-2"); !errors.Is(err, errCrash) { t.Fatalf("remove crash=%v", err) }
	service.Hooks = LifecycleHooks{}
	removed, err := service.Remove(context.Background(), active.ID, active.DesiredStateRevision, "principal_1", "remove-2")
	if err != nil || removed.State != StateRemoved || writer.deleteCalls != 2 { t.Fatalf("removed=%+v deletes=%d err=%v", removed, writer.deleteCalls, err) }
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
