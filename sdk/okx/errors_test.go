package okx

import "testing"

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Code: "51000", Message: "bad request"}

	if got := err.Error(); got != "okx api error: code=51000, msg=bad request" {
		t.Fatalf("unexpected error string: %s", got)
	}
}
