package secretstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestFilesystemProviderRoundTripReplayAndConflict(t *testing.T) {
	provider := newTestProvider(t)
	scope := testScope()
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

func TestFilesystemProviderRejectsTamperingAndForeignScope(t *testing.T) {
	provider := newTestProvider(t)
	scope := testScope()
	op, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	ref, err := provider.Writer().Put(context.Background(), scope, op, []byte("credential-canary"))
	if err != nil {
		t.Fatal(err)
	}
	objectName := fmt.Sprintf("%x", sha256.Sum256([]byte(ref.RawForTrustedBoundary())))
	objectPath := filepath.Join(provider.StateDir(), "objects", objectName+".json")
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
	// The foreign scope used to be the other mode: a test-mode provider handed a
	// live scope refused it, and the two namespaces were "boltertrader/test" and
	// "boltertrader/live". One namespace is left, so the scope a provider will not
	// serve is any scope that does not name it — which is what a reference minted
	// against different storage would arrive carrying.
	foreign := secretstore.Scope{Namespace: "boltertrader/other", KeyVersion: "v1"}
	if _, err := provider.Resolver().Resolve(context.Background(), foreign, ref); !errors.Is(err, secretstore.ErrInvalidScope) {
		t.Fatalf("foreign-scope err=%v", err)
	}
}

func TestFilesystemProviderRejectsEachAuthenticatedEnvelopeMutation(t *testing.T) {
	mutations := map[string]func(map[string]any){
		"nonce":      func(value map[string]any) { value["nonce"] = strings.Repeat("A", 16) },
		"ciphertext": func(value map[string]any) { value["ciphertext"] = strings.Repeat("A", 32) },
		"aad digest": func(value map[string]any) { value["materialDigest"] = strings.Repeat("A", 43) },
		"version":    func(value map[string]any) { value["version"] = float64(2) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			provider := newTestProvider(t)
			scope := testScope()
			op, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
			ref, err := provider.Writer().Put(context.Background(), scope, op, []byte("credential-canary"))
			if err != nil {
				t.Fatal(err)
			}
			objectName := fmt.Sprintf("%x", sha256.Sum256([]byte(ref.RawForTrustedBoundary())))
			path := filepath.Join(provider.StateDir(), "objects", objectName+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if json.Unmarshal(data, &value) != nil {
				t.Fatal("decode envelope")
			}
			mutate(value)
			data, _ = json.Marshal(value)
			if os.WriteFile(path, data, 0o600) != nil {
				t.Fatal("write envelope")
			}
			if _, err := provider.Resolver().Resolve(context.Background(), scope, ref); !errors.Is(err, secretstore.ErrIntegrity) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestFilesystemProviderRevalidatesCanonicalOperationID(t *testing.T) {
	provider := newTestProvider(t)
	scope := testScope()
	invalid := secretstore.OperationID("018F47A8-2578-7D3C-9D7D-5E4F4A445C33")
	if _, err := provider.Writer().Put(context.Background(), scope, invalid, []byte("value")); !errors.Is(err, secretstore.ErrInvalidOperationID) {
		t.Fatalf("Put err=%v", err)
	}
	if _, err := provider.Writer().LookupOperation(context.Background(), scope, invalid); !errors.Is(err, secretstore.ErrInvalidOperationID) {
		t.Fatalf("LookupOperation err=%v", err)
	}
}

// A provider that cannot reconcile the metadata already sitting in its state
// directory must refuse to start rather than write its own over the top. The
// directory is where credential material lives; overwriting the marker would
// leave every existing envelope unopenable and report nothing.
//
// This used to be aimed one directory sideways. State was split per mode, so
// bootstrap read the *other* mode's metadata: malformed meant ErrIntegrity, and
// a check that matched this provider's key meant ErrModeKeyReuse — one key
// encrypting both modes' credentials. There is one mode and one directory now,
// so there is no sibling to read and no reuse to detect; what is left is the
// provider's own marker, and both of its failure shapes still have to close.
func TestFilesystemProviderFailsClosedOnUnreconcilableMetadata(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	defer clear(key)

	for name, metadata := range map[string]string{
		"malformed":   `{"version":2}`,
		"foreign key": `{"version":1,"check":"` + strings.Repeat("a", 64) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(stateDir, "metadata.json"), []byte(metadata), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{StateDir: stateDir, KeySource: secretstore.StaticKeySource{Key: key}})
			if !errors.Is(err, secretstore.ErrIntegrity) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func newTestProvider(t *testing.T) *secretstore.FilesystemProvider {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	provider, err := secretstore.NewFilesystemProvider(secretstore.FilesystemConfig{
		StateDir: t.TempDir(),
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

// testScope is the only scope any provider here will serve. It used to be one
// of two, paired with the mode the provider was built in; the namespace is a
// constant now, and Validate refuses anything that is not it.
func testScope() secretstore.Scope {
	return secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"}
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
	for name, prepare := range map[string]func(string) error{
		"directory":   func(path string) error { return os.Mkdir(path, 0o700) },
		"invalid hex": func(path string) error { return os.WriteFile(path, []byte(strings.Repeat("z", 64)), 0o600) },
		"empty":       func(path string) error { return os.WriteFile(path, nil, 0o600) },
		"symlink":     func(path string) error { return os.Symlink(filepath.Join(dir, "master.key"), path) },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := filepath.Join(dir, strings.ReplaceAll(name, " ", "-"))
			if err := prepare(candidate); err != nil {
				t.Fatal(err)
			}
			if _, err := (secretstore.FileKeySource{Path: candidate}).Load(context.Background()); err == nil {
				t.Fatal("invalid key source accepted")
			}
		})
	}
}
