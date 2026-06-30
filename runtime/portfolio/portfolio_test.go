package portfolio

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func fill(side enums.OrderSide, price, qty, fee string) model.Fill {
	return model.Fill{
		InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         side,
		Price:        d(price),
		Quantity:     d(qty),
		Fee:          d(fee),
	}
}

// TestRealizedPnL_LongRoundTrip: buy 1 @100, sell 1 @110 => +10 realized.
func TestRealizedPnL_LongRoundTrip(t *testing.T) {
	pf := New()
	pf.OnFill(fill(enums.SideBuy, "100", "1", "0"), enums.PosNet)
	pf.OnFill(fill(enums.SideSell, "110", "1", "0"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("10")) {
		t.Fatalf("realized=%s, want 10", got)
	}
	if !pf.NetQty(model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}, enums.PosNet).IsZero() {
		t.Error("net qty should be flat after round trip")
	}
}

// TestRealizedPnL_ShortRoundTrip: sell 1 @100, buy 1 @90 => +10 realized.
func TestRealizedPnL_ShortRoundTrip(t *testing.T) {
	pf := New()
	pf.OnFill(fill(enums.SideSell, "100", "1", "0"), enums.PosNet)
	pf.OnFill(fill(enums.SideBuy, "90", "1", "0"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("10")) {
		t.Fatalf("realized=%s, want 10", got)
	}
}

// TestAvgCost_ScaleIn: buy 1@100, buy 1@200 => avg 150; sell 2@160 => +20.
func TestAvgCost_ScaleIn(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	pf.OnFill(fill(enums.SideBuy, "100", "1", "0"), enums.PosNet)
	pf.OnFill(fill(enums.SideBuy, "200", "1", "0"), enums.PosNet)
	if got := pf.AvgPrice(id, enums.PosNet); !got.Equal(d("150")) {
		t.Fatalf("avg=%s, want 150", got)
	}
	pf.OnFill(fill(enums.SideSell, "160", "2", "0"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("20")) { // (160-150)*2
		t.Fatalf("realized=%s, want 20", got)
	}
}

// TestPartialReduce: buy 3@100, sell 1@120 => +20 realized, 2 remain @100.
func TestPartialReduce(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	pf.OnFill(fill(enums.SideBuy, "100", "3", "0"), enums.PosNet)
	pf.OnFill(fill(enums.SideSell, "120", "1", "0"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("20")) {
		t.Fatalf("realized=%s, want 20", got)
	}
	if got := pf.NetQty(id, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("netQty=%s, want 2", got)
	}
	if got := pf.AvgPrice(id, enums.PosNet); !got.Equal(d("100")) {
		t.Fatalf("avg=%s, want 100 (unchanged on reduce)", got)
	}
}

// TestFlip: long 1@100, sell 2@120 => close 1 (+20), then short 1 opened @120.
func TestFlip(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	pf.OnFill(fill(enums.SideBuy, "100", "1", "0"), enums.PosNet)
	pf.OnFill(fill(enums.SideSell, "120", "2", "0"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("20")) {
		t.Fatalf("realized=%s, want 20", got)
	}
	if got := pf.NetQty(id, enums.PosNet); !got.Equal(d("-1")) {
		t.Fatalf("netQty=%s, want -1 (flipped short)", got)
	}
	if got := pf.AvgPrice(id, enums.PosNet); !got.Equal(d("120")) {
		t.Fatalf("avg=%s, want 120 (new short basis)", got)
	}
}

// TestFees: fees accrue and net them out of PnL.
func TestFees(t *testing.T) {
	pf := New()
	pf.OnFill(fill(enums.SideBuy, "100", "1", "0.5"), enums.PosNet)
	pf.OnFill(fill(enums.SideSell, "110", "1", "0.5"), enums.PosNet)
	if got := pf.Fees(); !got.Equal(d("1")) {
		t.Fatalf("fees=%s, want 1", got)
	}
	if got := pf.RealizedPnLNetFees(); !got.Equal(d("9")) { // 10 - 1
		t.Fatalf("net=%s, want 9", got)
	}
}

// TestUnrealized: long 2@100, mark 105 => +10 unrealized.
func TestUnrealized(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	pf.OnFill(fill(enums.SideBuy, "100", "2", "0"), enums.PosNet)
	if got := pf.UnrealizedPnL(id, enums.PosNet, d("105")); !got.Equal(d("10")) {
		t.Fatalf("unrealized=%s, want 10", got)
	}
}
