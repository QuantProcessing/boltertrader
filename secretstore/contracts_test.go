package secretstore_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestOpaqueRefRejectsPathLikeValues(t *testing.T) {
	for _, raw := range []string{"../secret", "/tmp/secret", "short", strings.Repeat("a", 44)} {
		if _, err := secretstore.ParseOpaqueRef(raw); !errors.Is(err, secretstore.ErrInvalidReference) {
			t.Fatalf("raw=%q err=%v", raw, err)
		}
	}
}

func TestOpaqueRefAcceptsCanonicalRawURLBase64(t *testing.T) {
	raw := strings.Repeat("A", 42) + "E"
	ref, err := secretstore.ParseOpaqueRef(raw)
	if err != nil || ref.RawForTrustedBoundary() != raw {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	for _, invalid := range []string{strings.Repeat("A", 42) + "=", strings.Repeat("A", 42) + "/", strings.Repeat("A", 42) + " ", strings.Repeat("A", 42) + "B"} {
		if _, err := secretstore.ParseOpaqueRef(invalid); !errors.Is(err, secretstore.ErrInvalidReference) {
			t.Fatalf("invalid=%q err=%v", invalid, err)
		}
	}
}

func TestOpaqueRefExposesRawOnlyAtTrustedBoundary(t *testing.T) {
	raw := strings.Repeat("A", 43)
	ref, err := secretstore.ParseOpaqueRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := ref.RawForTrustedBoundary(); got != raw {
		t.Fatalf("raw=%q want canonical reference", got)
	}
}

func TestOpaqueRefOrdinaryFormattingAndJSONAreRedacted(t *testing.T) {
	raw := strings.Repeat("A", 43)
	ref, err := secretstore.ParseOpaqueRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	formatted := []string{
		fmt.Sprintf("%s", ref),
		fmt.Sprintf("%v", ref),
		fmt.Sprintf("%+v", ref),
		fmt.Sprintf("%#v", ref),
		string(encoded),
	}
	for _, output := range formatted {
		if strings.Contains(output, raw) || !strings.Contains(output, "redacted") {
			t.Fatalf("unsafe opaque reference formatting %q", output)
		}
	}
}

func TestOperationIDRequiresCanonicalUUID(t *testing.T) {
	const canonical = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	operation, err := secretstore.ParseOperationID(canonical)
	if err != nil || operation.String() != canonical {
		t.Fatalf("operation=%q err=%v", operation, err)
	}
	for _, invalid := range []string{"018F47A8-2578-7D3C-9D7D-5E4F4A445C33", "{018f47a8-2578-7d3c-9d7d-5e4f4a445c33}", "not-a-uuid"} {
		if _, err := secretstore.ParseOperationID(invalid); !errors.Is(err, secretstore.ErrInvalidOperationID) {
			t.Fatalf("invalid=%q err=%v", invalid, err)
		}
	}
}

func TestScopeValidationIsModeIsolated(t *testing.T) {
	valid := []secretstore.Scope{
		{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v1"},
		{Mode: secretstore.ModeLive, Namespace: "boltertrader/live", KeyVersion: "v1"},
	}
	for _, scope := range valid {
		if err := scope.Validate(); err != nil {
			t.Fatalf("scope=%+v err=%v", scope, err)
		}
	}
	for _, scope := range []secretstore.Scope{
		{Mode: secretstore.ModeTest, Namespace: "boltertrader/live", KeyVersion: "v1"},
		{Mode: secretstore.ModeLive, Namespace: "boltertrader/test", KeyVersion: "v1"},
		{Mode: secretstore.ModeTest, Namespace: "boltertrader/test", KeyVersion: "v2"},
	} {
		if err := scope.Validate(); !errors.Is(err, secretstore.ErrInvalidScope) {
			t.Fatalf("scope=%+v err=%v", scope, err)
		}
	}
}
