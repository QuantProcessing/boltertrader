package hyperliquid

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestPostActionDebugLogRedactsSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient()
	client.BaseURL = server.URL
	client.Http = server.Client()
	client.Logger = zap.New(core).Sugar()
	const signatureR = "hyperliquid-signature-r-secret"
	const signatureS = "hyperliquid-signature-s-secret"

	if _, err := client.PostAction(context.Background(), map[string]any{"type": "cancel"}, SignatureResult{
		R: signatureR,
		S: signatureS,
		V: 27,
	}, 123); err != nil {
		t.Fatalf("PostAction: %v", err)
	}

	logged := hyperliquidObservedText(observed.All())
	if strings.Contains(logged, signatureR) || strings.Contains(logged, signatureS) || strings.Contains(logged, `"signature"`) {
		t.Fatalf("debug log leaked signed request material: %s", logged)
	}
}

func TestWSCommandDebugSummaryRedactsSignature(t *testing.T) {
	const secret = "hyperliquid-ws-signature-secret"
	summary := wsCommandDebugSummary(map[string]any{
		"signature": SignatureResult{R: secret, S: secret, V: 27},
	})
	if strings.Contains(summary, secret) || strings.Contains(summary, "signature") {
		t.Fatalf("WS command debug summary leaked signed material: %q", summary)
	}
}

func hyperliquidObservedText(entries []observer.LoggedEntry) string {
	var logged strings.Builder
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	return logged.String()
}
