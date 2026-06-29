package sdk

import "testing"

func TestAuth_BuildPayload(t *testing.T) {
	got := buildPayload("1", "get", "/api", "a=b", "{}")
	if got != "1GET/api?a=b{}" {
		t.Fatalf("unexpected payload: %s", got)
	}
}
