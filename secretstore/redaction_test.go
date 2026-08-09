package secretstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestProviderFailuresDoNotExposeStorageOrCredentialMaterial(t *testing.T) {
	provider := newTestProvider(t, secretstore.ModeTest)
	scope := secretstore.Scope{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"}
	op, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	ref, err := provider.Writer().Put(context.Background(), scope, op, []byte("plaintext-canary"))
	if err != nil {
		t.Fatal(err)
	}
	_, conflict := provider.Writer().Put(context.Background(), scope, op, []byte("different-canary"))
	if !errors.Is(conflict, secretstore.ErrOperationConflict) {
		t.Fatalf("conflict=%v", conflict)
	}
	metadata := filepath.Join(provider.StateDir(), "test", "metadata.json")
	if err := os.WriteFile(metadata, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	readiness := provider.Writer().Check(context.Background(), scope)
	if !errors.Is(readiness, secretstore.ErrIntegrity) {
		t.Fatalf("readiness=%v", readiness)
	}
	for _, failure := range []error{conflict, readiness} {
		output := failure.Error()
		for _, forbidden := range []string{"plaintext-canary", "different-canary", ref.RawForTrustedBoundary(), provider.StateDir(), metadata} {
			if strings.Contains(output, forbidden) {
				t.Fatalf("error %q leaked %q", output, forbidden)
			}
		}
	}
}
