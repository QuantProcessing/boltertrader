package boltertrader

import (
	"reflect"
	"testing"
)

func TestBolterPublicSourcesRejectPrivateSurfaceAtCompileContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		surface   any
		forbidden []string
	}{
		{
			name:    "catalog",
			surface: (*PublicCatalogSource)(nil),
			forbidden: []string{
				"PlaceOrder", "CancelOrder", "OpenOrders", "OrderHistory", "Fills",
				"Balances", "Positions", "SetLeverage", "SubscribePrivateEvents",
			},
		},
		{
			name:    "market",
			surface: (*PublicMarketSource)(nil),
			forbidden: []string{
				"PlaceOrder", "CancelOrder", "OpenOrders", "OrderHistory", "Fills",
				"Balances", "Positions", "SetLeverage", "SubscribePrivateEvents",
				"WatchOrders", "WatchFills", "WatchBalances", "WatchPositions",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			typ := reflect.TypeOf(test.surface).Elem()
			for _, method := range test.forbidden {
				if _, ok := typ.MethodByName(method); ok {
					t.Fatalf("%s exposes forbidden method %s", typ, method)
				}
			}
		})
	}
}

func TestConcretePublicMarketTypesHaveNoPrivateGatewayMethods(t *testing.T) {
	for _, surface := range []any{(*PublicMarketClient)(nil), (*PublicMarketProvider)(nil)} {
		typ := reflect.TypeOf(surface)
		for _, forbidden := range []string{
			"PlaceOrder", "CancelOrder", "OpenOrders", "ListOrders", "Fills",
			"Balances", "Positions", "RiskLimits", "SubscribePrivateEvents",
		} {
			if _, ok := typ.MethodByName(forbidden); ok {
				t.Fatalf("%s exposes forbidden private method %s", typ, forbidden)
			}
		}
	}
}
