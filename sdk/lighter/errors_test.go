package lighter

import "testing"

func TestErrorsCompanion_SentinelErrors(t *testing.T) {
	if ErrInvalidSignature == nil || ErrOrderNotFound == nil {
		t.Fatal("expected sentinel errors")
	}
}
