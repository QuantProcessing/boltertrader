package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
)

func fundedVenue(t *testing.T, leverage string) (*backtest.Venue, *clock.SimulatedClock) {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d(leverage),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	return venue, clk
}

// TestMarginRejectsOversizedOrder: at 1x leverage a 2000-notional order needs
// 2000 margin but only 1000 is available, so the venue rejects it.
func TestMarginRejectsOversizedOrder(t *testing.T) {
	venue, _ := fundedVenue(t, "1")
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("20"), ClientID: "c1",
	})
	if order.Status != enums.StatusRejected {
		t.Fatalf("status=%v, want Rejected (IM 2000 > avail 1000)", order.Status)
	}
	// A RejectEvent must accompany the rejected order event.
	var sawReject bool
	for _, ev := range drainExec(venue.Execution().Events()) {
		if re, ok := ev.(contract.RejectEvent); ok && re.ClientID == "c1" {
			sawReject = true
		}
	}
	if !sawReject {
		t.Fatal("no RejectEvent emitted for c1")
	}
}

// TestLeveragePermitsLargerPosition: the same 2000-notional order clears at 10x,
// and free margin drops by the locked initial margin.
func TestLeveragePermitsLargerPosition(t *testing.T) {
	venue, _ := fundedVenue(t, "10")
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("20"),
	})
	if order.Status != enums.StatusFilled {
		t.Fatalf("status=%v, want Filled at 10x", order.Status)
	}
	// equity 1000, used IM = 2000/10 = 200 -> available 800.
	bal := balanceOf(t, mustBalances(t, venue), "USDT")
	if !bal.Total.Equal(d("1000")) {
		t.Errorf("total=%s, want 1000", bal.Total)
	}
	if !bal.Available.Equal(d("800")) {
		t.Errorf("available=%s, want 800", bal.Available)
	}
}

// TestAvailableMarginTracksPrice: unrealized profit raises equity and thus free
// margin even though realized wallet balance is unchanged.
func TestAvailableMarginTracksPrice(t *testing.T) {
	venue, clk := fundedVenue(t, "10")
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("20"),
	})
	// Price 100 -> 110: unrealized = (110-100)*20 = 200; used IM at mark = 2200/10 = 220.
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("110"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})
	bal := balanceOf(t, mustBalances(t, venue), "USDT")
	if !bal.Total.Equal(d("1000")) {
		t.Errorf("total=%s, want 1000 (realized unchanged)", bal.Total)
	}
	// equity 1200 - usedIM 220 = 980.
	if !bal.Available.Equal(d("980")) {
		t.Errorf("available=%s, want 980", bal.Available)
	}
}

// TestReduceAllowedWithoutFreeMargin: closing a position never needs margin, so a
// reducing order clears even when free margin is exhausted.
func TestReduceAllowedWithoutFreeMargin(t *testing.T) {
	venue, _ := fundedVenue(t, "10")
	// Open 90 @ 100: notional 9000, IM 900 -> available 100.
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("90"),
	})
	// Opening more than 100/IM headroom is rejected: buy 20 -> IM 200 > 100.
	more, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("20"),
	})
	if more.Status != enums.StatusRejected {
		t.Fatalf("scale-in status=%v, want Rejected", more.Status)
	}
	// But fully closing the 90 (a reduce) is always allowed.
	closeOrd, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideSell, Type: enums.TypeMarket, Quantity: d("90"),
	})
	if closeOrd.Status != enums.StatusFilled {
		t.Fatalf("close status=%v, want Filled (reduce needs no margin)", closeOrd.Status)
	}
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%d, want flat after close", len(poss))
	}
}

func TestReduceOnlyRejectsOpeningOrder(t *testing.T) {
	venue, _ := fundedVenue(t, "10")

	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideSell,
		Type:         enums.TypeMarket,
		Quantity:     d("1"),
		ReduceOnly:   true,
		ClientID:     "ro-open",
	})

	if order.Status != enums.StatusRejected {
		t.Fatalf("status=%v, want Rejected for reduce-only order on flat account", order.Status)
	}
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%d, want no position opened by reduce-only order", len(poss))
	}
}

func TestReduceOnlyRejectsFlipPastFlat(t *testing.T) {
	venue, _ := fundedVenue(t, "10")
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("5"),
	})

	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideSell,
		Type:         enums.TypeMarket,
		Quantity:     d("10"),
		ReduceOnly:   true,
		ClientID:     "ro-flip",
	})

	if order.Status != enums.StatusRejected {
		t.Fatalf("status=%v, want Rejected for reduce-only order that flips past flat", order.Status)
	}
	poss := mustPositions(t, venue)
	if len(poss) != 1 || !poss[0].Quantity.Equal(d("5")) {
		t.Fatalf("positions=%+v, want original long qty 5 preserved", poss)
	}
}

func TestModifyRejectsOversizedRestingOrder(t *testing.T) {
	venue, _ := fundedVenue(t, "1")
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100"),
	})
	if order.Status != enums.StatusNew {
		t.Fatalf("initial status=%v, want New", order.Status)
	}

	modified, _ := venue.Execution().Modify(context.Background(), inst, order.VenueOrderID, d("100"), d("20"))

	if modified == nil || modified.Status != enums.StatusRejected {
		t.Fatalf("modify status=%v, want Rejected for oversized resting order", statusOf(modified))
	}
	open, _ := venue.Execution().OpenOrders(context.Background(), inst)
	if len(open) != 1 || !open[0].Request.Quantity.Equal(d("1")) {
		t.Fatalf("open orders=%+v, want original qty 1 unchanged after rejected modify", open)
	}
}

func TestModifyQuantityOnlyUsesExistingPriceForMargin(t *testing.T) {
	venue, _ := fundedVenue(t, "1")
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100"),
	})
	if order.Status != enums.StatusNew {
		t.Fatalf("initial status=%v, want New", order.Status)
	}

	modified, _ := venue.Execution().Modify(context.Background(), inst, order.VenueOrderID, d("0"), d("20"))

	if modified == nil || modified.Status != enums.StatusRejected {
		t.Fatalf("quantity-only modify status=%v, want Rejected from existing price margin check", statusOf(modified))
	}
	open, _ := venue.Execution().OpenOrders(context.Background(), inst)
	if len(open) != 1 || !open[0].Request.Quantity.Equal(d("1")) {
		t.Fatalf("open orders=%+v, want original qty 1 unchanged after rejected quantity-only modify", open)
	}
}

func TestModifyPriceOnlyKeepsExistingQuantityForFill(t *testing.T) {
	venue, clk := fundedVenue(t, "1")
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("2"), Price: d("90"),
	})
	if order.Status != enums.StatusNew {
		t.Fatalf("initial status=%v, want New", order.Status)
	}

	modified, _ := venue.Execution().Modify(context.Background(), inst, order.VenueOrderID, d("105"), d("0"))
	if modified == nil || modified.Status != enums.StatusNew {
		t.Fatalf("price-only modify status=%v, want New", statusOf(modified))
	}
	drainExec(venue.Execution().Events())

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})

	fillQty := d("0")
	for _, ev := range drainExec(venue.Execution().Events()) {
		if fe, ok := ev.(contract.FillEvent); ok && fe.Fill.VenueOrderID == order.VenueOrderID {
			fillQty = fe.Fill.Quantity
		}
	}
	if !fillQty.Equal(d("2")) {
		t.Fatalf("fill qty after price-only modify=%s, want existing qty 2", fillQty)
	}
}

func TestPassiveLimitFillBalanceEventReleasesRestingMargin(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
	})
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt",
	)

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	node.ProcessAvailable()
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("20"), Price: d("100"),
	})
	node.ProcessAvailable()

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("99"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)})
	node.ProcessAvailable()

	bal, ok := node.Cache.Balance("USDT")
	if !ok {
		t.Fatal("node cache has no USDT balance event")
	}
	// At mark 99: equity = 1000 - 20 unrealized = 980; position IM = 99*20/10 = 198.
	if !bal.Available.Equal(d("782")) {
		t.Fatalf("cached available=%s, want 782 after filled resting order released its reservation", bal.Available)
	}
}

func statusOf(o *model.Order) enums.OrderStatus {
	if o == nil {
		return enums.StatusUnknown
	}
	return o.Status
}
