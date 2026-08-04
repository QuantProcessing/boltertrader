package secret_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/secret"
)

func TestRefRejectsLiteralValuesAndRedactsEveryFormattingPath(t *testing.T) {
	canary := "sk_live_{\"unicode\":\"雪\",\"url\":\"a%2Fb\",\"b64\":\"c2VjcmV0\",\"line\":\"first\\nsecond\"}"
	for _, value := range []string{"literal", "BINANCE_KEY", canary, "env://"} {
		if _, err := secret.ParseRef(value); err == nil {
			t.Fatalf("ParseRef(%q) accepted literal or empty reference", value)
		}
	}

	ref, err := secret.ParseRef("env://BINANCE_DEMO_API_SECRET")
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
		if strings.Contains(formatted, "BINANCE_DEMO_API_SECRET") || strings.Contains(formatted, canary) {
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
	if strings.Contains(string(payload), "BINANCE_DEMO_API_SECRET") || strings.Contains(string(payload), "env://") {
		t.Fatalf("json ref leaked ref material: %s", payload)
	}
	if !strings.Contains(string(payload), "redacted") {
		t.Fatalf("json ref did not render as redacted: %s", payload)
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
