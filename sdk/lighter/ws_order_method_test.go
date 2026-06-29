package lighter

import (
	"strings"
	"testing"
)

func TestTxResponse_IsSuccess(t *testing.T) {
	if !(&TxResponse{Code: 200}).IsSuccess() {
		t.Fatal("expected success response")
	}
	if (&TxResponse{Code: 400}).IsSuccess() {
		t.Fatal("expected non-200 response to fail")
	}
}

func TestTxResponse_Error(t *testing.T) {
	if got := (&TxResponse{Code: 200}).Error(); got != "" {
		t.Fatalf("expected empty error for success, got %q", got)
	}
	if got := (&TxResponse{Code: 400, Message: "bad request"}).Error(); got != "bad request" {
		t.Fatalf("unexpected message error: %q", got)
	}
	if got := (&TxResponse{Code: 400, TxError: &TxError{Code: 12, Message: "denied"}}).Error(); !strings.Contains(got, "denied") {
		t.Fatalf("unexpected tx error: %q", got)
	}
}
