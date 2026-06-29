package sdk

import "testing"

func TestAuth_Sign(t *testing.T) {
	got := sign("secret", "payload")
	if got != "b82fcb791acec57859b989b430a826488ce2e479fdf92326bd0a2e8375a42ba4" {
		t.Fatalf("unexpected signature: %s", got)
	}
}
