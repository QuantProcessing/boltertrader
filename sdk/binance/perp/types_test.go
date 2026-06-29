package perp

import "testing"

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Code: -1121, Message: "Invalid symbol."}

	if got := err.Error(); got != "Invalid symbol." {
		t.Fatalf("unexpected error string: %s", got)
	}
}
