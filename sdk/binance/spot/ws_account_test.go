package spot

import (
	"context"
	"testing"
)

func TestWSAccountCompanion_NewWsAccountClient(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	if client.wsAPI == nil || client.apiKey != "api-key" || client.secretKey != "secret" {
		t.Fatalf("unexpected account client: %+v", client)
	}
}
