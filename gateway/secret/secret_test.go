package secret_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestRefRejectsLiteralValuesAndRedactsEveryFormattingPath(t *testing.T) {
	canary := "sk_live_{\"unicode\":\"雪\",\"url\":\"a%2Fb\",\"b64\":\"c2VjcmV0\",\"line\":\"first\\nsecond\"}"
	for _, value := range []string{"literal", "BINANCE_KEY", canary, "env://"} {
		if _, err := secret.ParseRef(value); err == nil {
			t.Fatalf("ParseRef(%q) accepted literal or empty reference", value)
		}
	}

	ref, err := secret.ParseRef("env://BINANCE_SECRET_KEY")
	if err != nil {
		t.Fatalf("ParseRef env: %v", err)
	}
	if _, err := secret.ParseRef("vault://trading/lighter/private-key"); err != nil {
		t.Fatalf("ParseRef vault path: %v", err)
	}
	if _, err := secret.ParseRef("kms://ring/key-id"); err != nil {
		t.Fatalf("ParseRef kms path: %v", err)
	}

	for _, formatted := range []string{
		fmt.Sprint(ref),
		fmt.Sprintf("%s", ref),
		fmt.Sprintf("%+v", ref),
		fmt.Sprintf("%#v", ref),
	} {
		if strings.Contains(formatted, "BINANCE_SECRET_KEY") || strings.Contains(formatted, canary) {
			t.Fatalf("formatted ref leaked sensitive content: %q", formatted)
		}
		if !strings.Contains(formatted, "redacted") {
			t.Fatalf("formatted ref is not explicitly redacted: %q", formatted)
		}
	}

	payload, err := json.Marshal(struct {
		Ref secret.Ref `json:"ref"`
	}{Ref: ref})
	if err != nil {
		t.Fatalf("json marshal ref: %v", err)
	}
	if strings.Contains(string(payload), "BINANCE_SECRET_KEY") || strings.Contains(string(payload), "env://") {
		t.Fatalf("json ref leaked ref material: %s", payload)
	}
	if !strings.Contains(string(payload), "redacted") {
		t.Fatalf("json ref did not render as redacted: %s", payload)
	}
}

type recordingCredentialResolver struct {
	scope secretstore.Scope
	ref   secretstore.OpaqueRef
}

func (resolver *recordingCredentialResolver) Check(_ context.Context, scope secretstore.Scope) error {
	resolver.scope = scope
	return nil
}

func (resolver *recordingCredentialResolver) Resolve(_ context.Context, scope secretstore.Scope, ref secretstore.OpaqueRef) ([]byte, error) {
	resolver.scope, resolver.ref = scope, ref
	return []byte("credential-canary"), nil
}

// A resolver is built around one scope and must present that scope to the
// provider unchanged: a reference minted under different storage would
// otherwise be resolved against this provider's key.
//
// The scope used to carry the mode as well, and the name recorded that the
// references were mode-scoped. The mode is gone; the scoping is not.
func TestScopedCredentialResolverUsesOnlyScopedOpaqueReferences(t *testing.T) {
	underlying := &recordingCredentialResolver{}
	scope := secretstore.Scope{Namespace: secretstore.Namespace, KeyVersion: "v1"}
	resolver, err := secret.NewScopedCredentialResolver(underlying, scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ref, _ := secretstore.ParseOpaqueRef(strings.Repeat("A", 43))
	material, err := resolver.Resolve(context.Background(), ref)
	if err != nil || string(material) != "credential-canary" {
		t.Fatalf("material=%q err=%v", material, err)
	}
	clear(material)
	if underlying.scope != scope || underlying.ref != ref {
		t.Fatalf("scope=%+v ref=%v", underlying.scope, underlying.ref)
	}
}

func TestEnvProviderOnlyResolvesExplicitAllowlistAndRedactsErrors(t *testing.T) {
	const envName = "GATEWAY_SECRET_CANARY"
	canary := "json:{\"secret\":\"雪\"}\nurl:a%2Fb\nbase64:c2VjcmV0"
	t.Setenv(envName, canary)
	t.Setenv("UNLISTED_SECRET", "must-not-resolve")

	provider := secret.NewEnvProvider([]string{"env://" + envName})
	ref, err := secret.ParseRef("env://" + envName)
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	value, err := provider.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatalf("Resolve allowlisted: %v", err)
	}
	if value != canary {
		t.Fatalf("resolved value = %q, want canary", value)
	}

	unlisted, _ := secret.ParseRef("env://UNLISTED_SECRET")
	if _, err := provider.Resolve(context.Background(), unlisted); err == nil {
		t.Fatal("Resolve accepted unlisted env ref")
	} else if strings.Contains(err.Error(), "UNLISTED_SECRET") || strings.Contains(err.Error(), "must-not-resolve") {
		t.Fatalf("unlisted error leaked ref or value: %v", err)
	}

	missing, _ := secret.ParseRef("env://MISSING_SECRET")
	provider = secret.NewEnvProvider([]string{"env://MISSING_SECRET"})
	if _, err := provider.Resolve(context.Background(), missing); err == nil {
		t.Fatal("Resolve accepted missing env var")
	} else if strings.Contains(err.Error(), "MISSING_SECRET") {
		t.Fatalf("missing error leaked env name: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Resolve(canceled, missing); err == nil {
		t.Fatal("Resolve ignored canceled context")
	}
}

func TestOpaqueRefIsProviderResolvableAndAlwaysRedacted(t *testing.T) {
	ref, err := secret.ParseOpaqueRef("opaque_ref_1")
	if err != nil {
		t.Fatal(err)
	}
	provider := secret.StaticProvider{"opaque_ref_1": "credential-canary"}
	value, err := provider.Resolve(context.Background(), ref.ProviderRef())
	if err != nil || value != "credential-canary" {
		t.Fatalf("Resolve() value=%q err=%v", value, err)
	}
	formatted := fmt.Sprintf("%s %#v", ref, ref)
	if strings.Contains(formatted, "opaque_ref_1") || !strings.Contains(formatted, "redacted") {
		t.Fatalf("opaque reference formatting was not redacted: %s", formatted)
	}
	payload, err := json.Marshal(ref)
	if err != nil || strings.Contains(string(payload), "opaque_ref_1") {
		t.Fatalf("opaque reference JSON leaked: %s err=%v", payload, err)
	}
}
