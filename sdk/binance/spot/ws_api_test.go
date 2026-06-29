package spot

import (
	"context"
	"testing"
)

func TestWSAPICompanion_NewWsAPIClient(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	if client.URL != WSAPIBaseURL || client.PendingRequests == nil || client.Done == nil {
		t.Fatalf("unexpected ws-api client: %+v", client)
	}
}

func TestWSAPICompanion_SetPostReconnect(t *testing.T) {
	client := NewWsAPIClient(context.Background())
	called := false
	client.SetPostReconnect(func() {
		called = true
	})
	client.postReconnect()
	if !called {
		t.Fatal("expected post reconnect hook to be stored")
	}
}
