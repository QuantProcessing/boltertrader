package perp

import "testing"

func TestNewClientDefaultsPublicAPIVersionV2(t *testing.T) {
	client := NewClient()
	if client.PublicAPIVersion != "v2" {
		t.Fatalf("expected EdgeX public API v2 by default, got %q", client.PublicAPIVersion)
	}
}
