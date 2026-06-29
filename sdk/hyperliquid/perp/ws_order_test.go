package perp

import "testing"

func TestWSOrderCompanion_ActionTypes(t *testing.T) {
	cancel, err := buildCancelOrderAction(CancelOrderRequest{AssetID: 1, OrderID: 2})
	if err != nil {
		t.Fatalf("buildCancelOrderAction: %v", err)
	}
	if cancel.Type != "cancel" || len(cancel.Cancels) != 1 || cancel.Cancels[0].OrderId != 2 {
		t.Fatalf("unexpected cancel action: %+v", cancel)
	}
}
