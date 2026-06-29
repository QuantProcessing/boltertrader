package perp

import (
	"testing"

	hyperliquid "github.com/QuantProcessing/exchanges/sdk/hyperliquid"
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
