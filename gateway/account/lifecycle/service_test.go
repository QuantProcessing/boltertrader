package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

type secretWriter struct {
	ref      string
	material []byte
	deleted  []string
}

func (writer *secretWriter) Delete(_ context.Context, reference string) error {
	writer.deleted = append(writer.deleted, reference)
	return nil
}

func (writer *secretWriter) Put(_ context.Context, _ secretstore.OperationID, material []byte) (string, error) {
	writer.material = append([]byte(nil), material...)
	return writer.ref, nil
}

type auditWriter struct{ records []AuditRecord }

func (writer *auditWriter) Append(_ context.Context, record AuditRecord) error {
	writer.records = append(writer.records, record)
	return nil
}

func TestCredentialLifecycleUsesOpaqueRuntimeIdentityAndNeverSerializesSecretMaterial(t *testing.T) {
	now := time.Date(2026, 8, 5, 2, 0, 0, 0, time.UTC)
	secrets := &secretWriter{ref: "vault://test/account/1"}
	audit := &auditWriter{}
	service, err := New(Options{
		Store: NewMemoryStore(), Secrets: secrets, Audit: audit,
		Now: func() time.Time { return now }, ID: func() string { return "acct_1" },
	})
	if err != nil {
		t.Fatal(err)
	}

	const canary = "credential-canary-never-return"
	created, err := service.Create(WithRequestID(context.Background(), "req-create"), CreateRequest{
		Venue: "binance", VenueEnvironment: EnvironmentLive, DisplayName: "Primary",
		ProductIDs: []string{"manual"}, SecretMaterial: []byte(canary),
	}, "browser-session")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.RuntimeID == "" || created.DesiredStateRevision != 0 || created.RuntimeGeneration != 0 || created.CredentialVersion != 0 || created.State != StateCreating {
		t.Fatalf("created account = %#v", created)
	}
	encoded, err := json.Marshal(created.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), secrets.ref) || strings.Contains(string(encoded), "RuntimeID") {
		t.Fatalf("public account leaked internal credential/runtime data: %s", encoded)
	}
	if _, err := service.Rotate(context.Background(), created.ID, 0, []byte("replacement-secret"), "browser-session", "replace"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("credential replacement err=%v", err)
	}
	if len(audit.records) != 1 || audit.records[0].RequestID != "req-create" {
		t.Fatalf("audit=%+v", audit.records)
	}
}

func TestUpdateMetadataKeepsCanonicalProductPolicyInSync(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Store: store, Secrets: &secretWriter{ref: "opaque-ref"}, Audit: &auditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"manual"}
	updated, err := service.UpdateMetadata(context.Background(), account.ID, account.DesiredStateRevision, "Updated", want, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Get(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.ProductPolicy, want) || !slices.Equal(updated.ProductIDs, want) ||
		!slices.Equal(persisted.ProductPolicy, want) || !slices.Equal(persisted.ProductIDs, want) {
		t.Fatalf("updated=%+v persisted=%+v", updated, persisted)
	}
}

func TestMarkValidatedDoesNotAdvanceDesiredRuntimeRevision(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Store: store, Secrets: &secretWriter{ref: "opaque-ref"}, Audit: &auditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	validated, err := service.MarkValidated(context.Background(), account.ID, account.DesiredStateRevision, "principal_1")
	if err != nil {
		t.Fatal(err)
	}
	if validated.DesiredStateRevision != account.DesiredStateRevision || validated.LastValidatedAt == nil {
		t.Fatalf("validated=%+v", validated)
	}
}

func TestUpdateMetadataRejectsInvalidProductPolicyWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		policy []string
	}{
		{name: "empty"},
		{name: "duplicate", policy: []string{"manual", "manual"}},
		{name: "unknown", policy: []string{"unknown"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore()
			account := validRoutedAccount()
			account.Routes = []AccountRoute{{ID: "spot", Product: ProductSpot, RouteProfileID: "binance.spot.v1", RequestedState: RouteActive}}
			if err := store.Create(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			service, err := New(Options{Store: store, Secrets: &secretWriter{ref: "opaque-ref"}, Audit: &auditWriter{}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.UpdateMetadata(context.Background(), account.ID, account.DesiredStateRevision, "Changed", test.policy, "principal_1"); !errors.Is(err, ErrValidation) {
				t.Fatalf("err=%v", err)
			}
			persisted, err := store.Get(context.Background(), account.ID)
			if err != nil || persisted.DesiredStateRevision != account.DesiredStateRevision || persisted.DisplayName != account.DisplayName || !slices.Equal(persisted.ProductPolicy, account.ProductPolicy) {
				t.Fatalf("persisted=%+v err=%v", persisted, err)
			}
		})
	}
}

func TestRemovedLifecycleTransitionsStayUnavailable(t *testing.T) {
	store := NewMemoryStore()
	account := validRoutedAccount()
	account.State = StateActive
	if err := store.Create(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Store: store, Secrets: &secretWriter{ref: "opaque-ref"}, Audit: &auditWriter{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Drain(context.Background(), account.ID, 0, Blockers{}, "browser-session"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("drain err=%v", err)
	}
	if _, err := service.MarkDrained(context.Background(), account.ID, 0, "gateway-adapter"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("mark drained err=%v", err)
	}
}
