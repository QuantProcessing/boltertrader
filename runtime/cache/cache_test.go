package cache

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

var inst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}

func TestOrderUpsertAndOpenFilter(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{Request: model.OrderRequest{ClientID: "a"}, Status: enums.StatusNew})
	c.UpsertOrder(model.Order{Request: model.OrderRequest{ClientID: "b"}, Status: enums.StatusFilled})

	if got, ok := c.Order("a"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("order a: ok=%v status=%v", ok, got.Status)
	}
	if open := c.OpenOrders(); len(open) != 1 || open[0].Request.ClientID != "a" {
		t.Fatalf("open orders=%+v, want only a", open)
	}
	// Re-upsert a as canceled => no longer open.
	c.UpsertOrder(model.Order{Request: model.OrderRequest{ClientID: "a"}, Status: enums.StatusCanceled})
	if open := c.OpenOrders(); len(open) != 0 {
		t.Fatalf("open orders=%+v, want none", open)
	}
}

func TestPositionUpsertRemovesFlat(t *testing.T) {
	c := New()
	c.UpsertPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.RequireFromString("1.5")})
	if _, ok := c.Position(inst, enums.PosNet); !ok {
		t.Fatal("position should exist")
	}
	// Flat removes it.
	c.UpsertPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.Zero})
	if _, ok := c.Position(inst, enums.PosNet); ok {
		t.Fatal("flat position should be removed")
	}
}

func TestHedgeLegsCoexist(t *testing.T) {
	c := New()
	c.UpsertPosition(model.Position{InstrumentID: inst, Side: enums.PosLong, Quantity: decimal.RequireFromString("1")})
	c.UpsertPosition(model.Position{InstrumentID: inst, Side: enums.PosShort, Quantity: decimal.RequireFromString("-2")})
	if len(c.Positions()) != 2 {
		t.Fatalf("want 2 hedge legs, got %d", len(c.Positions()))
	}
}

func TestBalanceUpsert(t *testing.T) {
	c := New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: decimal.RequireFromString("100")})
	if b, ok := c.Balance("USDT"); !ok || !b.Total.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("balance: ok=%v total=%s", ok, b.Total)
	}
}
