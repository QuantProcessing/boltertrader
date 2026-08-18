package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestAccountRejectsDuplicateRouteID(t *testing.T) {
	account := validRoutedAccount()
	account.Routes = append(account.Routes, account.Routes[0])
	if err := account.Validate(); err == nil {
		t.Fatal("duplicate route accepted")
	}
}

func TestAccountStoreCreatesDraftAndCredentialOperationAtomically(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.SecretRef = ""
	operation := validCredentialOperation(account.ID)
	if err := store.CreateDraft(context.Background(), account, operation); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetCredentialOperation(context.Background(), operation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != OperationAwaitingMaterial || got.AccountID != account.ID {
		t.Fatalf("operation=%+v", got)
	}
	if err := store.CreateDraft(context.Background(), account, operation); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate err=%v", err)
	}
}

func TestBindCredentialReferenceTransitionsStoredAtomically(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.SecretRef = ""
	operation := validCredentialOperation(account.ID)
	if err := store.CreateDraft(context.Background(), account, operation); err != nil {
		t.Fatal(err)
	}
	boundAccount, boundOperation, err := store.BindCredentialReference(context.Background(), operation.ID, "opaque-ref")
	if err != nil {
		t.Fatal(err)
	}
	if boundAccount.SecretRef != "opaque-ref" || boundOperation.SecretRef != "opaque-ref" || boundOperation.State != OperationStored {
		t.Fatalf("account=%+v operation=%+v", boundAccount, boundOperation)
	}
}

func validRoutedAccount() Account {
	return Account{
		ID: "acct_1", Venue: "binance",
		NativeEnvironment: EnvironmentLive, DisplayName: "Primary",
		ProductPolicy: []string{"manual"}, ProductIDs: []string{"manual"},
		Routes:               []AccountRoute{{ID: "perp-usdt", Product: ProductPerp, SettlementAsset: "USDT", RouteProfileID: "binance.perp.usdt.v1", RequestedState: RouteActive}},
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
		State: StateCreating, UpdatedAt: time.Unix(1, 0).UTC(),
	}
}

func validCredentialOperation(accountID string) CredentialOperation {
	return CredentialOperation{
		ID:          secretstore.OperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33"),
		PrincipalID: "principal_1", AccountID: accountID,
		Kind: OperationCreate, State: OperationAwaitingMaterial,
		IdempotencyKeyDigest: "idempotency-digest", RequestDigest: "request-digest",
		TargetDesiredStateRevision: 1, TargetRuntimeGeneration: 1, TargetCredentialVersion: 1,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC(),
	}
}
