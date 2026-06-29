package spot

import "testing"

func TestTypesCompanion_OrderRequestShape(t *testing.T) {
	req := PlaceOrderRequest{AssetID: 1, IsBuy: true, Price: 100, Size: 1}
	if req.AssetID != 1 || !req.IsBuy || req.Price != 100 || req.Size != 1 {
		t.Fatalf("unexpected request: %+v", req)
	}
}
