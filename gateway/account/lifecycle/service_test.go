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

func TestCredentialLifecycleUsesCASAndNeverSerializesSecretMaterial(t *testing.T) {
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
	ctx := WithRequestID(context.Background(), "req-create")
	created, err := service.Create(ctx, CreateRequest{
		Venue: "binance", VenueEnvironment: EnvironmentTestnet, DisplayName: "Primary", ProductIDs: []string{"manual"}, SecretMaterial: []byte(canary),
	}, "browser-session")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.DesiredStateRevision != 1 || created.RuntimeGeneration != 1 || created.CredentialVersion != 1 || created.State != StateCreating {
		t.Fatalf("created account = %#v", created)
	}
	if string(secrets.material) != canary {
		t.Fatal("secret writer did not receive credential")
	}
	encoded, err := json.Marshal(created.Public())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), canary) || strings.Contains(string(encoded), secrets.ref) {
		t.Fatalf("public account leaked secret material/reference: %s", encoded)
	}

	if _, err := service.Activate(context.Background(), created.ID, 0, "gateway-adapter"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale activate error = %v", err)
	}
	active, err := service.Activate(context.Background(), created.ID, 1, "gateway-adapter")
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if active.State != StateActive || active.DesiredStateRevision != 2 {
		t.Fatalf("active account = %#v", active)
	}

	secrets.ref = "vault://test/account/2"
	if _, err := service.rotateLegacy(context.Background(), created.ID, 1, []byte("orphan-candidate"), "browser-session"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale rotate error = %v", err)
	}
	if len(secrets.deleted) != 1 || secrets.deleted[0] != secrets.ref {
		t.Fatalf("stale rotation cleanup = %#v", secrets.deleted)
	}
	rotating, err := service.rotateLegacy(context.Background(), created.ID, active.DesiredStateRevision, []byte("replacement-secret"), "browser-session")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotating.State != StateRotating || rotating.RuntimeGeneration != 1 || rotating.CredentialVersion != 1 || rotating.Rotation == nil || rotating.Rotation.ToRuntimeGeneration != 2 || rotating.Rotation.ToCredentialVersion != 2 {
		t.Fatalf("rotating account = %#v", rotating)
	}
	promoted, err := service.ActivateRotation(context.Background(), created.ID, rotating.DesiredStateRevision, "gateway-adapter")
	if err != nil {
		t.Fatalf("ActivateRotation: %v", err)
	}
	if promoted.State != StateActive || promoted.RuntimeGeneration != 2 || promoted.CredentialVersion != 2 || promoted.Rotation != nil {
		t.Fatalf("promoted account = %#v", promoted)
	}
	if len(audit.records) != 4 {
		t.Fatalf("audit records = %d, want 4", len(audit.records))
	}
	if audit.records[0].RequestID != "req-create" {
		t.Fatalf("create audit request id = %q", audit.records[0].RequestID)
	}
	for _, record := range audit.records {
		body, _ := json.Marshal(record)
		if strings.Contains(string(body), canary) || strings.Contains(string(body), "replacement-secret") || strings.Contains(string(body), "vault://") {
			t.Fatalf("audit leaked secret data: %s", body)
		}
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
			account.Routes = []AccountRoute{{ID: "spot", Product: ProductSpot, RouteProfileID: "binance.spot.demo.v1", RequestedState: RouteActive}}
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

func TestDrainDisableRemoveTransitionsFailClosed(t *testing.T) {
	service, _ := New(Options{
		Store: NewMemoryStore(), Secrets: &secretWriter{ref: "opaque-ref"}, Audit: &auditWriter{},
		ID: func() string { return "acct_2" },
	})
	created, _ := service.Create(context.Background(), CreateRequest{Venue: "okx", VenueEnvironment: EnvironmentDemo, DisplayName: "Demo", ProductIDs: []string{"manual"}, SecretMaterial: []byte("secret")}, "browser-session")
	active, _ := service.Activate(context.Background(), created.ID, created.DesiredStateRevision, "gateway-adapter")

	if _, err := service.removeLegacy(context.Background(), active.ID, active.DesiredStateRevision, "browser-session"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("remove active error = %v", err)
	}
	draining, err := service.Drain(context.Background(), active.ID, active.DesiredStateRevision, Blockers{OpenOrders: 1}, "browser-session")
	if !errors.Is(err, ErrAccountBlocked) || draining.ID != "" {
		t.Fatalf("blocked drain = %#v, %v", draining, err)
	}
	draining, err = service.Drain(context.Background(), active.ID, active.DesiredStateRevision, Blockers{}, "browser-session")
	if err != nil || draining.State != StateDraining {
		t.Fatalf("drain = %#v, %v", draining, err)
	}
	drained, err := service.MarkDrained(context.Background(), active.ID, draining.DesiredStateRevision, "gateway-adapter")
	if err != nil || drained.State != StateDrained {
		t.Fatalf("mark drained = %#v, %v", drained, err)
	}
	disabled, err := service.Disable(context.Background(), active.ID, drained.DesiredStateRevision, Blockers{}, "browser-session")
	if err != nil || disabled.State != StateDisabled {
		t.Fatalf("disable = %#v, %v", disabled, err)
	}
	removing, err := service.removeLegacy(context.Background(), active.ID, disabled.DesiredStateRevision, "browser-session")
	if err != nil || removing.State != StateRemoving {
		t.Fatalf("remove = %#v, %v", removing, err)
	}
	removed, err := service.MarkRemoved(context.Background(), active.ID, removing.DesiredStateRevision, "gateway-adapter")
	if err != nil || removed.State != StateRemoved {
		t.Fatalf("mark removed = %#v, %v", removed, err)
	}
}
