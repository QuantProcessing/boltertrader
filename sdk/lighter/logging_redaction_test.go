package lighter

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

func TestPostDebugLogRedactsSignedBodyAndTokenResponse(t *testing.T) {
	const requestSignature = "lighter-request-signature-secret"
	const responseToken = "lighter-response-api-token-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"api_token":%q}`, responseToken)
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient()
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()
	client.Logger = zap.New(core).Sugar()

	if _, err := client.Post(context.Background(), "/api/v1/sendTxBatch", map[string]any{
		"signature": requestSignature,
	}, false); err != nil {
		t.Fatalf("Post: %v", err)
	}

	logged := lighterObservedText(observed.All())
	if strings.Contains(logged, requestSignature) || strings.Contains(logged, responseToken) || strings.Contains(logged, `"api_token"`) {
		t.Fatalf("debug log leaked signed request or token response: %s", logged)
	}
}

func lighterObservedText(entries []observer.LoggedEntry) string {
	var logged strings.Builder
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	return logged.String()
}
