package risk

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var inst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}

func buy(qty, price string) model.OrderRequest {
	return model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Quantity: d(qty), Price: d(price)}
}

func TestMaxOrderQty(t *testing.T) {
	e := New(Limits{MaxOrderQty: d("5")}, cache.New())
	if err := e.Check(buy("3", "100"), nil); err != nil {
		t.Fatalf("3 should pass: %v", err)
	}
	err := e.Check(buy("6", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("6 should be rejected, got %v", err)
	}
}

func TestMaxOrderNotional(t *testing.T) {
	e := New(Limits{MaxOrderNotional: d("1000")}, cache.New())
	if err := e.Check(buy("5", "100"), nil); err != nil { // 500
		t.Fatalf("notional 500 should pass: %v", err)
	}
	if err := e.Check(buy("20", "100"), nil); !errors.Is(err, ErrRiskRejected) { // 2000
		t.Fatalf("notional 2000 should be rejected, got %v", err)
	}
}

func TestKillSwitch(t *testing.T) {
	e := New(Limits{}, cache.New())
	e.Trip()
	if err := e.Check(buy("1", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("kill switch should reject all orders")
	}
	e.Reset()
	if err := e.Check(buy("1", "100"), nil); err != nil {
		t.Fatalf("after reset should pass: %v", err)
	}
}

func TestDuplicateClientID(t *testing.T) {
	e := New(Limits{}, cache.New())
	req := buy("1", "100")
	req.ClientID = "dup-1"
	if err := e.Check(req, nil); err != nil {
		t.Fatalf("first should pass: %v", err)
	}
	if err := e.Check(req, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("duplicate client id should be rejected")
	}
}

func TestInstrumentMinimums(t *testing.T) {
	e := New(Limits{}, cache.New())
	in := &model.Instrument{MinQty: d("0.01"), MinNotional: d("10")}
	if err := e.Check(buy("0.005", "100"), in); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("below min qty should be rejected")
	}
	if err := e.Check(buy("0.02", "100"), in); err != nil { // qty ok, notional 2 < 10
		// notional 0.02*100 = 2 < 10 => rejected
		if !errors.Is(err, ErrRiskRejected) {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if err := e.Check(buy("0.2", "100"), in); err != nil { // notional 20 ok
		t.Fatalf("valid order should pass: %v", err)
	}
}

func TestMaxPositionQty_UsesCache(t *testing.T) {
	c := cache.New()
	// Existing long of 4.
	c.UpsertPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: d("4")})
	e := New(Limits{MaxPositionQty: d("5")}, c)

	// Buying 2 more => resulting 6 > 5 => rejected.
	if err := e.Check(buy("2", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("resulting position 6 should be rejected")
	}
	// Buying 1 => resulting 5 == limit => ok.
	if err := e.Check(buy("1", "100"), nil); err != nil {
		t.Fatalf("resulting position 5 should pass: %v", err)
	}
	// Selling 2 => resulting 2 => ok.
	sell := model.OrderRequest{InstrumentID: inst, Side: enums.SideSell, Quantity: d("2"), Price: d("100")}
	if err := e.Check(sell, nil); err != nil {
		t.Fatalf("reducing sell should pass: %v", err)
	}
}

func TestNonPositiveQty(t *testing.T) {
	e := New(Limits{}, cache.New())
	if err := e.Check(buy("0", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("zero qty should be rejected")
	}
}
