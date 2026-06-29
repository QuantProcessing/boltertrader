package okx

import (
	"context"
	"testing"
)

func TestWSClientCompanion_NewWSClient(t *testing.T) {
	client := NewWSClient(context.Background())
	if client.URL != WSBaseURL || client.Subs == nil || client.PendingReqs == nil {
		t.Fatalf("unexpected ws client: %+v", client)
	}
}

func TestWSClientCompanion_BusinessPrivateURL(t *testing.T) {
	client := NewWSClient(context.Background()).WithCredentials("key", "secret", "pass").WithBusinessURL()
	if client.URL != WSBusinessBaseURL || !client.IsPrivate {
		t.Fatalf("unexpected business ws client: url=%s private=%v", client.URL, client.IsPrivate)
	}
}
