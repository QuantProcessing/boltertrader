package perp

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
