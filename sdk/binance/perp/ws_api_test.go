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

func TestWSAPICompanion_NewDemoWsAPIClient(t *testing.T) {
	client := NewDemoWsAPIClient(context.Background())
	if client.URL != DemoWSAPIBaseURL || client.URL == WSAPIBaseURL || client.PendingRequests == nil || client.Done == nil {
		t.Fatalf("unexpected Demo ws-api client: %+v", client)
	}
}

func TestWSAPICompanion_WithEndpointProfile(t *testing.T) {
	profile := EndpointProfile{WSAPIBaseURL: "wss://profile.test/ws-api"}
	client := NewWsAPIClientWithEndpointProfile(context.Background(), profile)
	if client.URL != profile.WSAPIBaseURL || client.PendingRequests == nil || client.Done == nil {
		t.Fatalf("unexpected profile ws-api client: %+v", client)
	}
}
