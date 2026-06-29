package perp

import "testing"

func TestTypesCompanion_OrderRequestShape(t *testing.T) {
	req := PlaceOrderRequest{AssetID: 1, IsBuy: true, Price: 100, Size: 1, ReduceOnly: true}
	if req.AssetID != 1 || !req.IsBuy || !req.ReduceOnly {
		t.Fatalf("unexpected request: %+v", req)
	}
}
