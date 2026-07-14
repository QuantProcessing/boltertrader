package okx

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWSLoginDebugLogRedactsCredentialsAndSignature(t *testing.T) {
	t.Setenv("PROXY", "")
	core, observed := observer.New(zap.DebugLevel)
	client := NewWSClient(context.Background())
	client.ApiKey = "okx-api-key-secret"
	client.SecretKey = "okx-signing-secret"
	client.Passphrase = "okx-passphrase-secret"
	client.Logger = zap.New(core).Sugar()

	_ = client.loginOn(nil)

	var logged strings.Builder
	for _, entry := range observed.All() {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	text := logged.String()
	for _, secret := range []string{client.ApiKey, client.SecretKey, client.Passphrase, `"sign"`} {
		if strings.Contains(text, secret) {
			t.Fatalf("WS login debug log leaked authentication material %q: %s", secret, text)
		}
	}
}
