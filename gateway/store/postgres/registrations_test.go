package postgres

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestRegistrationRealPostgresEncryptsCredentialsAndDeletionPreservesOrderEvidence(t *testing.T) {
	pool := newTestPool(t)
	ctx := t.Context()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	cipher, err := secretstore.NewCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	registrations := NewRegistrationStore(pool, cipher)
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := account.RegistrationRecord{
		Registration:     account.Registration{ID: testAccountID(t), Venue: "binance", DisplayName: "Offline fixture", CreatedAt: now, ValidatedAt: now},
		CredentialBundle: []byte(`{"fixture":"offline-credential-only"}`),
	}
	if err := registrations.CreateRegistration(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, err := registrations.GetRegistration(ctx, record.ID)
	if err != nil || loaded.ID != record.ID || loaded.Venue != record.Venue || loaded.DisplayName != record.DisplayName || !loaded.CreatedAt.Equal(now) || !loaded.ValidatedAt.Equal(now) || !bytes.Equal(loaded.CredentialBundle, record.CredentialBundle) {
		t.Fatalf("encrypted registration did not round-trip: %v", err)
	}
	clear(loaded.CredentialBundle)
	var sealed []byte
	if err := pool.QueryRow(ctx, `SELECT credential_ciphertext FROM gateway_accounts WHERE account_id=$1`, record.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("offline-credential-only")) {
		t.Fatal("credential stored as plaintext")
	}
	if err := registrations.CreateRegistration(ctx, record); !errors.Is(err, account.ErrRegistrationConflict) {
		t.Fatalf("duplicate account accepted: %v", err)
	}
	commands := newStoreForTest(pool, storeOptions{identity: testIdentityGenerator()})
	request := reserveRequest(t, json.RawMessage(`{"quantity":"0.001"}`))
	first, err := commands.ReserveCommand(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := registrations.DeleteRegistration(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := registrations.GetRegistration(ctx, record.ID); !errors.Is(err, account.ErrRegistrationNotFound) {
		t.Fatalf("deleted credential remains available: %v", err)
	}
	if items, err := registrations.ListRegistrations(ctx); err != nil || len(items) != 0 {
		t.Fatalf("deleted account remains in directory: %v", err)
	}
	replay, err := commands.ReserveCommand(ctx, request)
	if err != nil || replay.New || replay.CommandID != first.CommandID || replay.GatewayOrderID != first.GatewayOrderID {
		t.Fatalf("deletion lost order idempotency: %v", err)
	}
	var orders, events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE gateway_order_id=$1`, first.GatewayOrderID).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE gateway_order_id=$1`, first.GatewayOrderID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if orders != 1 || events != 1 {
		t.Fatalf("deletion lost order evidence: orders=%d events=%d", orders, events)
	}
}
