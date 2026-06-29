package spot

import "testing"

func TestWSOrderCompanion_BuildCancelOrderAction(t *testing.T) {
	action, err := buildCancelOrderAction(CancelOrderRequest{AssetID: 1, OrderID: 2})
	if err != nil {
		t.Fatalf("buildCancelOrderAction: %v", err)
	}
	if action.Type != "cancel" || len(action.Cancels) != 1 || action.Cancels[0].OrderId != 2 {
		t.Fatalf("unexpected cancel action: %+v", action)
	}
}
