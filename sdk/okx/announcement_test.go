package okx

import (
	"context"
	"testing"
)

func TestClient_GetAnnouncements(t *testing.T) {
	announcements, err := newLiveClient().GetAnnouncements(context.Background(), "new_crypto")
	if err != nil {
		t.Fatalf("GetAnnouncements: %v", err)
	}

	if announcements == nil {
		t.Fatalf("unexpected announcements: %+v", announcements)
	}
}
