package manual

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
)

func TestManualOrderValidationRejectsCatalogRuleViolations(t *testing.T) {
	instrument := catalog.Instrument{Rules: catalog.TradeRules{
		PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5",
	}}
	request := OrderRequest{
		OrderType: "limit", Quantity: "0.0005", LimitPrice: "100.03",
	}
	if validateManualOrderRules(instrument, request) == nil {
		t.Fatal("catalog rule violations were accepted")
	}
}

func TestManualMarketOrderLeavesPriceDependentNotionalToVenue(t *testing.T) {
	instrument := catalog.Instrument{Rules: catalog.TradeRules{
		PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5",
	}}
	if err := validateManualOrderRules(instrument, OrderRequest{
		OrderType: "market", Quantity: "0.01",
	}); err != nil {
		t.Fatalf("market order rejected without a fabricated price: %v", err)
	}
}

func TestAccountRoutePrefersSettlementSpecificRouteOverProductWideOne(t *testing.T) {
	account := Account{Routes: []AccountRoute{
		{RouteID: "perp-any", Product: "perp"},
		{RouteID: "perp-usdc", Product: "perp", SettlementAsset: "USDC"},
		{RouteID: "spot", Product: "spot"},
	}}
	tests := []struct {
		name       string
		product    string
		settlement string
		wantRoute  string
		wantRouted bool
	}{
		{"settlement specific wins", "perp", "USDC", "perp-usdc", true},
		{"unnamed settlement falls back to product wide", "perp", "USDT", "perp-any", true},
		{"registered route serves its product", "spot", "USDT", "spot", true},
		{"unrouted product is refused", "margin", "USDT", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route, routed := manualAccountRoute(account, test.product, test.settlement)
			if route != test.wantRoute || routed != test.wantRouted {
				t.Fatalf("route=%q routed=%v, want %q/%v", route, routed, test.wantRoute, test.wantRouted)
			}
		})
	}
}

func TestAccountReadyRequiresActiveState(t *testing.T) {
	for _, state := range []string{"creating", "rotating"} {
		account := readyAccount()
		account.State = state
		if manualAccountReady(account) {
			t.Fatalf("state %s reported ready", state)
		}
	}
	missingRuntime := readyAccount()
	missingRuntime.RuntimeID = ""
	if manualAccountReady(missingRuntime) {
		t.Fatal("account without runtime identity reported ready")
	}

	if !manualAccountReady(readyAccount()) {
		t.Fatal("active account reported unready")
	}
}

func readyAccount() Account {
	return Account{
		ID: "account-a", Venue: "binance", VenueEnvironment: "live",
		ProductIDs: []string{"manual"}, RuntimeID: "runtime-1", State: manualAccountActive,
		Routes: []AccountRoute{{RouteID: "perp", Product: "perp"}},
	}
}
