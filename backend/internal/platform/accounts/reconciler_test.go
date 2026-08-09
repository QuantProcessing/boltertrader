package accounts

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

var errCrash = errors.New("simulated crash")

type lifecycleWriter struct {
	refs        map[secretstore.OperationID]string
	putCalls    int
	lookupCalls int
}

func (writer *lifecycleWriter) Put(_ context.Context, operation secretstore.OperationID, _ []byte) (string, error) {
	writer.putCalls++
	if ref := writer.refs[operation]; ref != "" {
		return ref, nil
	}
	ref := "opaque-reference"
	writer.refs[operation] = ref
	return ref, nil
}

func (writer *lifecycleWriter) LookupOperation(_ context.Context, operation secretstore.OperationID) (string, error) {
	writer.lookupCalls++
	if ref := writer.refs[operation]; ref != "" {
		return ref, nil
	}
	return "", secretstore.ErrSecretNotFound
}

type lifecycleGateway struct{ applyCalls int }

func (gateway *lifecycleGateway) ApplyDesired(_ context.Context, apply DesiredApply) (GatewayRuntimeTruth, error) {
	gateway.applyCalls++
	return GatewayRuntimeTruth{AccountID: apply.Account.ID, DesiredStateRevision: apply.Account.DesiredStateRevision, RuntimeGeneration: apply.Account.RuntimeGeneration, CredentialVersion: apply.Account.CredentialVersion, Active: true}, nil
}
func (gateway *lifecycleGateway) GetRuntimeTruth(context.Context, string) (GatewayRuntimeTruth, error) {
	return GatewayRuntimeTruth{}, nil
}
func (gateway *lifecycleGateway) RetireAccount(context.Context, RetireRequest) (GatewayRetirement, error) {
	return GatewayRetirement{Tombstoned: true}, nil
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
