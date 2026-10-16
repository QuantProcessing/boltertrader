package store

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func testCipher(t *testing.T, seed byte) *secretstore.Cipher {
	t.Helper()
	cipher, err := secretstore.NewCipher(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return cipher
}

func TestStoreRoundTripAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.enc")
	store, err := Open(path, testCipher(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	account := Account{ID: "acc_1", Venue: "binance", Label: "main", CreatedAt: time.Now().UTC()}
	secret := []byte(`{"apiKey":"k","apiSecret":"super-secret"}`)
	if err := store.Create(account, secret); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(account, secret); err != ErrExists {
		t.Fatalf("duplicate create: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatal("credential stored in plaintext")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	reopened, err := Open(path, testCipher(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	got, bundle, err := reopened.Get("acc_1")
	if err != nil || got.Label != "main" || !bytes.Equal(bundle, secret) {
		t.Fatalf("get = %+v %s %v", got, bundle, err)
	}
	if err := reopened.Delete("acc_1"); err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 0 {
		t.Fatal("delete did not persist in memory")
	}
	again, _ := Open(path, testCipher(t, 1))
	if len(again.List()) != 0 {
		t.Fatal("delete did not persist on disk")
	}
}

func TestStoreRejectsWrongKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.enc")
	store, _ := Open(path, testCipher(t, 1))
	if err := store.Create(Account{ID: "acc_1", Venue: "okx", CreatedAt: time.Now()}, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, testCipher(t, 2)); err == nil {
		t.Fatal("wrong key must fail to open the store")
	}
}
