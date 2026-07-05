package cache

import (
	"testing"
	"time"

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

func TestOrderUpsertRejectsStaleTerminalRegression(t *testing.T) {
	c := New()
	newer := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	older := newer.Add(-time.Minute)
	c.UpsertOrder(model.Order{
		Request:   model.OrderRequest{ClientID: "terminal"},
		Status:    enums.StatusCanceled,
		UpdatedAt: newer,
	})
	c.UpsertOrder(model.Order{
		Request:   model.OrderRequest{ClientID: "terminal"},
		Status:    enums.StatusRejected,
		UpdatedAt: older,
	})
	got, _ := c.Order("terminal")
	if got.Status != enums.StatusCanceled {
		t.Fatalf("status=%s, want CANCELED", got.Status)
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

func TestApplyAccountStateCreatesAccountAndCompatibilityBalance(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	state := model.AccountState{
		AccountID: model.AccountIDBinanceSpot,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("90"),
			Locked:   decimal.RequireFromString("10"),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:        "BINANCE",
			AccountID:    model.AccountIDBinanceSpot,
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   ts,
			Source:       "test",
		},
		Reported: true,
		TsEvent:  ts,
	}
	if err := c.ApplyAccountStateAt(state, ts); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	acct, ok := c.Account(model.AccountIDBinanceSpot)
	if !ok || acct.ID() != model.AccountIDBinanceSpot {
		t.Fatalf("account lookup failed: ok=%v acct=%v", ok, acct)
	}
	if acct, ok := c.AccountForVenue("BINANCE"); !ok || acct.ID() != model.AccountIDBinanceSpot {
		t.Fatalf("account for venue failed: ok=%v acct=%v", ok, acct)
	}
	if b, ok := c.Balance("USDT"); !ok || !b.Free.Equal(decimal.RequireFromString("90")) {
		t.Fatalf("compat balance=%+v ok=%v, want free 90", b, ok)
	}
}

func TestApplyAccountStateRejectsNonTradingReadyMode(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	err := c.ApplyAccountStateAt(model.AccountState{
		AccountID: model.AccountIDBinanceSpot,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("90"),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:      "BINANCE",
			AccountID:  model.AccountIDBinanceSpot,
			Verified:   true,
			VerifiedAt: ts,
			Source:     "test",
		},
		Reported: true,
		TsEvent:  ts,
	}, ts)
	if err == nil {
		t.Fatal("account state without product scope should not enter runtime")
	}
}

func TestApplyAccountStateRejectsMultipleAccountsForSameVenue(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	state := model.AccountState{
		AccountID: model.AccountIDBinanceSpot,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("1"),
			Free:     decimal.RequireFromString("1"),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:        "BINANCE",
			AccountID:    model.AccountIDBinanceSpot,
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   ts,
			Source:       "test",
		},
		Reported: true,
		TsEvent:  ts,
	}
	if err := c.ApplyAccountStateAt(state, ts); err != nil {
		t.Fatalf("apply spot: %v", err)
	}
	state.AccountID = model.AccountIDBinanceUSDM
	state.Type = model.AccountMargin
	state.ModeInfo.AccountID = model.AccountIDBinanceUSDM
	state.ModeInfo.AccountMode = "USD-M"
	state.ModeInfo.ProductScope = []enums.InstrumentKind{enums.KindPerp}
	if err := c.ApplyAccountStateAt(state, ts); err == nil {
		t.Fatal("second account for same venue should be rejected")
	}
}
