package secretstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestFilesystemProviderRoundTripReplayAndConflict(t *testing.T) {
	provider := newTestProvider(t, secretstore.ModeTest)
	scope := secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}
	op, err := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":1}`))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":1}`))
	if err != nil || replay != ref {
		t.Fatalf("replay=%q err=%v", replay, err)
	}
	if _, err := provider.Writer().Put(context.Background(), scope, op, []byte(`{"schemaVersion":2}`)); !errors.Is(err, secretstore.ErrOperationConflict) {
		t.Fatalf("err=%v", err)
	}
	lookup, err := provider.Writer().LookupOperation(context.Background(), scope, op)
	if err != nil || lookup != ref {
		t.Fatalf("lookup=%q err=%v", lookup, err)
	}
	resolved, err := provider.Resolver().Resolve(context.Background(), scope, ref)
	if err != nil || string(resolved) != `{"schemaVersion":1}` {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	clear(resolved)
	if err := provider.Writer().Delete(context.Background(), scope, ref); err != nil {
		t.Fatal(err)
	}
	if err := provider.Writer().Delete(context.Background(), scope, ref); err != nil {
		t.Fatalf("idempotent delete: %v", err)
	}
	if _, err := provider.Resolver().Resolve(context.Background(), scope, ref); !errors.Is(err, secretstore.ErrSecretNotFound) {
		t.Fatalf("resolve deleted err=%v", err)
	}
}

func TestFilesystemProviderRejectsTamperingAndCrossScope(t *testing.T) {
	provider := newTestProvider(t, secretstore.ModeTest)
	scope := secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}
	op, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	ref, err := provider.Writer().Put(context.Background(), scope, op, []byte("credential-canary"))
	if err != nil {
		t.Fatal(err)
	}
	objectName := fmt.Sprintf("%x", sha256.Sum256([]byte(ref.RawForTrustedBoundary())))
	objectPath := filepath.Join(provider.StateDir(), "test", "objects", objectName+".json")
	data, err := os.ReadFile(objectPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 1
	if err := os.WriteFile(objectPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Resolver().Resolve(context.Background(), scope, ref); !errors.Is(err, secretstore.ErrIntegrity) {
		t.Fatalf("tamper err=%v", err)
	} else if strings.Contains(err.Error(), "credential-canary") || strings.Contains(err.Error(), ref.RawForTrustedBoundary()) || strings.Contains(err.Error(), objectPath) {
		t.Fatalf("error leaked provider detail: %v", err)
	}
	live := secretstore.Scope{Mode: secretstore.ModeLive, Namespace: "boltertrader/live", KeyVersion: "v1"}
	if _, err := provider.Resolver().Resolve(context.Background(), live, ref); !errors.Is(err, secretstore.ErrInvalidScope) {
		t.Fatalf("cross-scope err=%v", err)
	}
}

func newTestProvider(t *testing.T, mode secretstore.Mode) *secretstore.FilesystemProvider {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	provider, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{
		StateDir: t.TempDir(),
		Mode:     mode,
		KeySource: secretstore.StaticKeySource{
			Key: key,
		},
	})
	clear(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	return provider
}

func TestFileKeySourceRequiresSecureCanonicalKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "master.key")
	encoded := hex.EncodeToString(make([]byte, 32)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := (secretstore.FileKeySource{Path: path}).Load(context.Background())
	if err != nil || len(key) != 32 {
		t.Fatalf("key length=%d err=%v", len(key), err)
	}
	clear(key)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (secretstore.FileKeySource{Path: path}).Load(context.Background()); !errors.Is(err, secretstore.ErrKeyPermissions) {
		t.Fatalf("permissions err=%v", err)
	}
	relative := secretstore.FileKeySource{Path: "master.key"}
	if _, err := relative.Load(context.Background()); !errors.Is(err, secretstore.ErrInvalidKeySource) {
		t.Fatalf("relative err=%v", err)
	}
}
