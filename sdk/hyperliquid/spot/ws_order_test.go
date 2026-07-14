package spot

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestWSOrderCompanion_BuildCancelOrderAction(t *testing.T) {
	action, err := buildCancelOrderAction(CancelOrderRequest{AssetID: 1, OrderID: 2})
	if err != nil {
		t.Fatalf("buildCancelOrderAction: %v", err)
	}
	if action.Type != "cancel" || len(action.Cancels) != 1 || action.Cancels[0].OrderId != 2 {
		t.Fatalf("unexpected cancel action: %+v", action)
	}
}

func TestBuildModifyOrderActionSupportsExactIdentityAndPreservesCloid(t *testing.T) {
	oid := int64(42)
	cloid := "0x1234567890abcdef1234567890abcdef"
	orderCloid := "0xabcdef1234567890abcdef1234567890"
	base := ModifyOrderRequest{Order: PlaceOrderRequest{
		AssetID: 1, IsBuy: true, Price: 10, Size: 2, ClientOrderID: &orderCloid,
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
			if err == nil && (action.Order.Cloid == nil || *action.Order.Cloid != orderCloid) {
				t.Fatalf("action=%+v, modify must preserve order cloid", action)
			}
		})
	}
}
