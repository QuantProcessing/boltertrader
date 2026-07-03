package okx

import "testing"

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Code: "51000", Message: "bad request"}

	if got := err.Error(); got != "okx api error: code=51000, msg=bad request" {
		t.Fatalf("unexpected error string: %s", got)
	}
}

func TestAPIError_ErrorIncludesDetails(t *testing.T) {
	err := &APIError{
		Code:    "1",
		Message: "All operations failed",
		Details: `[{"sCode":"51008","sMsg":"insufficient balance"}]`,
	}

	if got := err.Error(); got != `okx api error: code=1, msg=All operations failed, details=[{"sCode":"51008","sMsg":"insufficient balance"}]` {
		t.Fatalf("unexpected error string: %s", got)
	}
}
