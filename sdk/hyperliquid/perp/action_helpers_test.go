package perp

import (
	"testing"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestActionHelpers_BuildPlaceOrderAction(t *testing.T) {
	action, err := buildPlaceOrderAction(PlaceOrderRequest{
		AssetID: 1,
		IsBuy:   true,
		Price:   100,
		Size:    1,
		OrderType: OrderType{Limit: &OrderTypeLimit{
			Tif: hyperliquid.TifGtc,
		}},
	})
	if err != nil {
		t.Fatalf("buildPlaceOrderAction: %v", err)
	}
	if action.Type != "order" || len(action.Orders) != 1 || action.Orders[0].Asset != 1 {
		t.Fatalf("unexpected action: %+v", action)
	}
}

func TestBuildModifyOrderActionSupportsExactIdentityAndPreservesCloid(t *testing.T) {
	oid := int64(42)
	cloid := "0x1234567890abcdef1234567890abcdef"
	orderCloid := "0xabcdef1234567890abcdef1234567890"
	base := ModifyOrderRequest{Order: PlaceOrderRequest{
		AssetID: 1, IsBuy: false, Price: 10, Size: 2, ReduceOnly: true, ClientOrderID: &orderCloid,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifAlo}},
	}}

	for _, tt := range []struct {
		name    string
		oid     *int64
		cloid   *string
		wantErr bool
	}{
		{name: "oid", oid: &oid},
		{name: "cloid", cloid: &cloid},
		{name: "both", oid: &oid, cloid: &cloid, wantErr: true},
		{name: "none", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			req.Oid, req.Cloid = tt.oid, tt.cloid
			action, err := buildModifyOrderAction(req)
			if (err != nil) != tt.wantErr {
				t.Fatalf("buildModifyOrderAction err=%v wantErr=%v", err, tt.wantErr)
			}
			if err == nil && (action.Order.Cloid == nil || *action.Order.Cloid != orderCloid || !action.Order.ReduceOnly) {
				t.Fatalf("action=%+v, modify must preserve cloid and reduce-only", action)
			}
		})
	}
}
