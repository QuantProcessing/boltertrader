package okx

import (
	"strings"
	"testing"
)

func TestWSClient_PlaceOrderWS_RequiresRequest(t *testing.T) {
	_, err := NewWSClient(nil).PlaceOrderWS(nil)
	if err == nil || !strings.Contains(err.Error(), "order request is required") {
		t.Fatalf("expected missing request error, got %v", err)
	}
}

func TestWSClient_PlaceOrderWS_RequiresInstIdCode(t *testing.T) {
	_, err := NewWSClient(nil).PlaceOrderWS(&OrderRequest{})
	if err == nil || !strings.Contains(err.Error(), "instIdCode is required") {
		t.Fatalf("expected missing instIdCode error, got %v", err)
	}
}

func TestWSClient_ModifyOrderWS_RequiresRequest(t *testing.T) {
	_, err := NewWSClient(nil).ModifyOrderWS(nil)
	if err == nil || !strings.Contains(err.Error(), "modify order request is required") {
		t.Fatalf("expected missing request error, got %v", err)
	}
}

func TestWSClient_ModifyOrderWS_RequiresInstIdCode(t *testing.T) {
	_, err := NewWSClient(nil).ModifyOrderWS(&ModifyOrderRequest{})
	if err == nil || !strings.Contains(err.Error(), "instIdCode is required") {
		t.Fatalf("expected missing instIdCode error, got %v", err)
	}
}

func TestWSClient_CancelOrdersWS_RequiresInstIdCode(t *testing.T) {
	_, err := NewWSClient(nil).CancelOrdersWS([]CancelOrderRequest{{}})
	if err == nil || !strings.Contains(err.Error(), "instIdCode is required") {
		t.Fatalf("expected missing instIdCode error, got %v", err)
	}
}

func TestValidateWSActionResult(t *testing.T) {
	if err := validateWSActionResult("order", OrderId{SCode: "0"}); err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	err := validateWSActionResult("order", OrderId{SCode: "51000", SubCode: "51149", SMsg: "order rejected"})
	if err == nil || !strings.Contains(err.Error(), "subCode=51149") {
		t.Fatalf("expected rejected order error, got %v", err)
	}
}
