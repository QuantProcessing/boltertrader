package enums

import "testing"

func TestStringValues(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{SideBuy.String(), "BUY"},
		{SideSell.String(), "SELL"},
		{SideUnknown.String(), "UNKNOWN"},
		{TypeMarket.String(), "MARKET"},
		{TypeLimit.String(), "LIMIT"},
		{TypeStopMarket.String(), "STOP_MARKET"},
		{TypeStopLimit.String(), "STOP_LIMIT"},
		{TypeMarketIfTouched.String(), "MARKET_IF_TOUCHED"},
		{TypeLimitIfTouched.String(), "LIMIT_IF_TOUCHED"},
		{TypeTrailingStopMarket.String(), "TRAILING_STOP_MARKET"},
		{TifGTC.String(), "GTC"},
		{TifIOC.String(), "IOC"},
		{TifFOK.String(), "FOK"},
		{TifGTX.String(), "GTX"},
		{StatusPendingNew.String(), "PENDING_NEW"},
		{StatusNew.String(), "NEW"},
		{StatusPartiallyFilled.String(), "PARTIALLY_FILLED"},
		{StatusFilled.String(), "FILLED"},
		{StatusCanceled.String(), "CANCELED"},
		{StatusRejected.String(), "REJECTED"},
		{StatusExpired.String(), "EXPIRED"},
		{StatusTriggered.String(), "TRIGGERED"},
		{PosNet.String(), "NET"},
		{PosLong.String(), "LONG"},
		{PosShort.String(), "SHORT"},
		{LiqMaker.String(), "MAKER"},
		{LiqTaker.String(), "TAKER"},
		{KindSpot.String(), "SPOT"},
		{KindPerp.String(), "PERP"},
		{KindFuture.String(), "FUTURE"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("String() = %q, want %q", c.got, c.want)
		}
	}
}

// TestNoStringCollision guards against two distinct enum values rendering to the
// same non-UNKNOWN string within a type, which would make logs ambiguous.
func TestNoStringCollision(t *testing.T) {
	check := func(name string, strs []string) {
		seen := map[string]bool{}
		for _, s := range strs {
			if s == "UNKNOWN" {
				continue
			}
			if seen[s] {
				t.Errorf("%s: duplicate String() %q", name, s)
			}
			seen[s] = true
		}
	}
	check("OrderType", []string{
		TypeMarket.String(), TypeLimit.String(), TypeStopMarket.String(),
		TypeStopLimit.String(), TypeMarketIfTouched.String(), TypeLimitIfTouched.String(),
		TypeTrailingStopMarket.String(),
	})
	check("OrderStatus", []string{
		StatusPendingNew.String(), StatusNew.String(), StatusPartiallyFilled.String(),
		StatusFilled.String(), StatusCanceled.String(), StatusRejected.String(),
		StatusExpired.String(), StatusTriggered.String(),
	})
}

func TestDeprecatedTakeProfitAliasesUseTouchedSemantics(t *testing.T) {
	if TypeTakeProfitMarket != TypeMarketIfTouched {
		t.Fatalf("TypeTakeProfitMarket=%v, want alias of TypeMarketIfTouched", TypeTakeProfitMarket)
	}
	if TypeTakeProfitLimit != TypeLimitIfTouched {
		t.Fatalf("TypeTakeProfitLimit=%v, want alias of TypeLimitIfTouched", TypeTakeProfitLimit)
	}
}
