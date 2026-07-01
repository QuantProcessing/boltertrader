package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
)

var spotInst = model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindSpot}

func TestSpotMarketBuyUpdatesCashBalancesWithoutPosition(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		TakerFeeRate: d("0.001"),
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		Instruments: []*model.Instrument{{
			ID:    spotInst,
			Base:  "BTC",
			Quote: "USDT",
		}},
	})
	venue.Feed(model.TradeTick{InstrumentID: spotInst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	order, err := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     d("2"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusFilled {
		t.Fatalf("status=%s, want FILLED", order.Status)
	}

	usdt := balanceOf(t, mustBalances(t, venue), "USDT")
	if !usdt.Total.Equal(d("799.8")) || !usdt.Available.Equal(d("799.8")) || !usdt.Locked.IsZero() {
		t.Fatalf("USDT balance=%+v, want total=available=799.8 locked=0", usdt)
	}
	btc := balanceOf(t, mustBalances(t, venue), "BTC")
	if !btc.Total.Equal(d("2")) || !btc.Available.Equal(d("2")) || !btc.Locked.IsZero() {
		t.Fatalf("BTC balance=%+v, want total=available=2 locked=0", btc)
	}
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%+v, want no derivative position for spot inventory", poss)
	}
}

func TestSpotLimitBuyLocksQuoteAndCancelUnlocks(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		Instruments: []*model.Instrument{{
			ID:    spotInst,
			Base:  "BTC",
			Quote: "USDT",
		}},
	})
	venue.Feed(model.TradeTick{InstrumentID: spotInst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	order, err := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		Quantity:     d("2"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusNew {
		t.Fatalf("status=%s, want NEW", order.Status)
	}

	locked := balanceOf(t, mustBalances(t, venue), "USDT")
	if !locked.Total.Equal(d("1000")) || !locked.Available.Equal(d("800")) || !locked.Locked.Equal(d("200")) {
		t.Fatalf("locked USDT balance=%+v, want total=1000 available=800 locked=200", locked)
	}

	if err := venue.Execution().Cancel(context.Background(), spotInst, order.VenueOrderID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	unlocked := balanceOf(t, mustBalances(t, venue), "USDT")
	if !unlocked.Total.Equal(d("1000")) || !unlocked.Available.Equal(d("1000")) || !unlocked.Locked.IsZero() {
		t.Fatalf("unlocked USDT balance=%+v, want total=available=1000 locked=0", unlocked)
	}
}

func TestSpotLimitBuyModifyReplacesQuoteLock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		Instruments: []*model.Instrument{{
			ID:    spotInst,
			Base:  "BTC",
			Quote: "USDT",
		}},
	})
	venue.Feed(model.TradeTick{InstrumentID: spotInst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	order, err := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		Quantity:     d("2"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := venue.Execution().Modify(context.Background(), spotInst, order.VenueOrderID, d("110"), d("3")); err != nil {
		t.Fatalf("modify: %v", err)
	}

	modified := balanceOf(t, mustBalances(t, venue), "USDT")
	if !modified.Total.Equal(d("1000")) || !modified.Available.Equal(d("670")) || !modified.Locked.Equal(d("330")) {
		t.Fatalf("modified USDT balance=%+v, want total=1000 available=670 locked=330", modified)
	}
	if err := venue.Execution().Cancel(context.Background(), spotInst, order.VenueOrderID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	unlocked := balanceOf(t, mustBalances(t, venue), "USDT")
	if !unlocked.Total.Equal(d("1000")) || !unlocked.Available.Equal(d("1000")) || !unlocked.Locked.IsZero() {
		t.Fatalf("unlocked USDT balance=%+v, want total=available=1000 locked=0", unlocked)
	}
}

func TestSpotLimitSellModifyReplacesBaseLock(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		StartBalance: model.AccountBalance{Currency: "BTC", Total: d("5"), Available: d("5")},
		Instruments: []*model.Instrument{{
			ID:    spotInst,
			Base:  "BTC",
			Quote: "USDT",
		}},
	})
	venue.Feed(model.TradeTick{InstrumentID: spotInst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	order, err := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		Quantity:     d("2"),
		Price:        d("110"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := venue.Execution().Modify(context.Background(), spotInst, order.VenueOrderID, d("120"), d("4")); err != nil {
		t.Fatalf("modify: %v", err)
	}

	modified := balanceOf(t, mustBalances(t, venue), "BTC")
	if !modified.Total.Equal(d("5")) || !modified.Available.Equal(d("1")) || !modified.Locked.Equal(d("4")) {
		t.Fatalf("modified BTC balance=%+v, want total=5 available=1 locked=4", modified)
	}
	if err := venue.Execution().Cancel(context.Background(), spotInst, order.VenueOrderID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	unlocked := balanceOf(t, mustBalances(t, venue), "BTC")
	if !unlocked.Total.Equal(d("5")) || !unlocked.Available.Equal(d("5")) || !unlocked.Locked.IsZero() {
		t.Fatalf("unlocked BTC balance=%+v, want total=available=5 locked=0", unlocked)
	}
}
