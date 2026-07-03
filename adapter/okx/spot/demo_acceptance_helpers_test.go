package spot

import "testing"

func TestOKXSpotDemoClientOrderIDIsVenueSafe(t *testing.T) {
	id := demoClientOrderID("runtime-close")
	if len(id) > 32 {
		t.Fatalf("client order id length=%d, want <=32: %q", len(id), id)
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("client order id contains non-alphanumeric rune %q in %q", r, id)
		}
	}
}

func TestOKXSpotDemoClientOrderIDKindSanitizesAndTruncates(t *testing.T) {
	if got := demoClientOrderIDKind("runtime-close-!@#-ABCDEFGHIJKLMNOPQRSTUVWXYZ", 12); got != "runtimeclose" {
		t.Fatalf("kind=%q, want runtimeclose", got)
	}
	if got := demoClientOrderIDKind("!!!", 12); got != "x" {
		t.Fatalf("empty sanitized kind=%q, want x", got)
	}
}
