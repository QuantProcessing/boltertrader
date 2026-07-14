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

func TestOpenOrdersIncludesUnknownSafetyState(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{AccountID: "acct", ClientID: "unknown"},
		Status:  enums.StatusUnknown,
	})
	open := c.OpenOrders()
	if len(open) != 1 || open[0].Request.ClientID != "unknown" {
		t.Fatalf("open orders=%+v, want UNKNOWN order retained as unresolved open state", open)
	}
}

func TestApplyConfirmedCancelPreservesConcurrentFill(t *testing.T) {
	c := New()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.UpsertOrder(model.Order{
		Request:   model.OrderRequest{AccountID: "acct", ClientID: "cancel-race"},
		Status:    enums.StatusFilled,
		UpdatedAt: now.Add(time.Minute),
	})
	if !c.ApplyConfirmedCancel("acct", "cancel-race", now) {
		t.Fatal("confirmed cancel did not find the order")
	}
	got, ok := c.OrderForAccount("acct", "cancel-race")
	if !ok || got.Status != enums.StatusFilled {
		t.Fatalf("order=(%+v,%v), want concurrent FILLED preserved", got, ok)
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

func TestOrdersAreScopedByAccountID(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a", ClientID: "same-client"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusNew,
	})
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-b", ClientID: "same-client"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusFilled,
		FilledQty:    decimal.RequireFromString("1"),
	})

	if _, ok := c.Order("same-client"); ok {
		t.Fatal("legacy client-id lookup should be ambiguous across accounts")
	}
	if _, ok := c.Order("same-venue"); ok {
		t.Fatal("legacy venue-order-id lookup should be ambiguous across accounts")
	}
	if got, ok := c.OrderForAccount("acct-a", "same-client"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-a order=%+v ok=%v, want NEW", got, ok)
	}
	if got, ok := c.OrderForAccount("acct-b", "same-client"); !ok || got.Status != enums.StatusFilled {
		t.Fatalf("acct-b order=%+v ok=%v, want FILLED", got, ok)
	}
	if got, ok := c.OrderForAccount("acct-a", "same-venue"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-a venue lookup=%+v ok=%v, want NEW", got, ok)
	}
	if got, ok := c.OrderForAccount("acct-b", "same-venue"); !ok || got.Status != enums.StatusFilled {
		t.Fatalf("acct-b venue lookup=%+v ok=%v, want FILLED", got, ok)
	}
}

func TestVenueOrderMergeIsScopedByAccountID(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a", ClientID: "client-a"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusNew,
	})
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-b", ClientID: "client-b"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusNew,
	})

	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusCanceled,
	})

	if got, ok := c.OrderForAccount("acct-a", "client-a"); !ok || got.Status != enums.StatusCanceled {
		t.Fatalf("acct-a merged order=%+v ok=%v, want CANCELED", got, ok)
	}
	if got, ok := c.OrderForAccount("acct-b", "client-b"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-b order=%+v ok=%v, want untouched NEW", got, ok)
	}
}

func TestAmbiguousVenueOrderUpdateWithoutAccountIDDoesNotMergeAcrossAccounts(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a", ClientID: "client-a"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusNew,
	})
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-b", ClientID: "client-b"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusNew,
	})

	c.UpsertOrder(model.Order{VenueOrderID: "same-venue", Status: enums.StatusCanceled})

	if got, ok := c.OrderForAccount("acct-a", "client-a"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-a order=%+v ok=%v, want untouched NEW", got, ok)
	}
	if got, ok := c.OrderForAccount("acct-b", "client-b"); !ok || got.Status != enums.StatusNew {
		t.Fatalf("acct-b order=%+v ok=%v, want untouched NEW", got, ok)
	}
	if _, ok := c.Order("same-venue"); ok {
		t.Fatal("legacy venue lookup should stay ambiguous after unscoped update")
	}

	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a"},
		VenueOrderID: "same-venue",
		Status:       enums.StatusPartiallyFilled,
		FilledQty:    decimal.RequireFromString("0.5"),
	})
	if got, ok := c.OrderForAccount("acct-a", "client-a"); !ok || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("later acct-a update=%+v ok=%v, want PARTIALLY_FILLED without inherited cancel", got, ok)
	}
}

func TestOrderUpsertMigratesLegacyEmptyAccountOrderIntoScopedKey(t *testing.T) {
	c := New()
	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{ClientID: "client-a"},
		VenueOrderID: "venue-a",
		Status:       enums.StatusNew,
	})

	c.UpsertOrder(model.Order{
		Request:      model.OrderRequest{AccountID: "acct-a"},
		VenueOrderID: "venue-a",
		Status:       enums.StatusCanceled,
	})

	if got := c.Orders(); len(got) != 1 {
		t.Fatalf("orders=%+v, want single migrated order", got)
	}
	if got, ok := c.OrderForAccount("acct-a", "client-a"); !ok || got.Status != enums.StatusCanceled {
		t.Fatalf("migrated order=%+v ok=%v, want CANCELED", got, ok)
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

func TestPositionsAreScopedByAccountID(t *testing.T) {
	c := New()
	c.UpsertPosition(model.Position{AccountID: "T:acct-a", InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.RequireFromString("1")})
	c.UpsertPosition(model.Position{AccountID: "T:acct-b", InstrumentID: inst, Side: enums.PosNet, Quantity: decimal.RequireFromString("2")})

	gotA, ok := c.PositionForAccount("T:acct-a", inst, enums.PosNet)
	if !ok || !gotA.Quantity.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("acct-a position=%+v ok=%v, want qty 1", gotA, ok)
	}
	gotB, ok := c.PositionForAccount("T:acct-b", inst, enums.PosNet)
	if !ok || !gotB.Quantity.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("acct-b position=%+v ok=%v, want qty 2", gotB, ok)
	}
	if got := c.Positions(); len(got) != 2 {
		t.Fatalf("positions len=%d, want 2: %+v", len(got), got)
	}
	if _, ok := c.Position(inst, enums.PosNet); ok {
		t.Fatal("legacy position lookup should be ambiguous across accounts")
	}
}

func TestBalanceUpsert(t *testing.T) {
	c := New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: decimal.RequireFromString("100")})
	if b, ok := c.Balance("USDT"); !ok || !b.Total.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("balance: ok=%v total=%s", ok, b.Total)
	}
}

func TestBalancesAreScopedByAccountID(t *testing.T) {
	c := New()
	c.UpsertBalance(model.AccountBalance{AccountID: "T:acct-a", Currency: "USDT", Total: decimal.RequireFromString("100"), Free: decimal.RequireFromString("100")})
	c.UpsertBalance(model.AccountBalance{AccountID: "T:acct-b", Currency: "USDT", Total: decimal.RequireFromString("200"), Free: decimal.RequireFromString("200")})

	gotA, ok := c.BalanceForAccount("T:acct-a", "USDT")
	if !ok || !gotA.Total.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("acct-a balance=%+v ok=%v, want total 100", gotA, ok)
	}
	gotB, ok := c.BalanceForAccount("T:acct-b", "USDT")
	if !ok || !gotB.Total.Equal(decimal.RequireFromString("200")) {
		t.Fatalf("acct-b balance=%+v ok=%v, want total 200", gotB, ok)
	}
	if _, ok := c.Balance("USDT"); ok {
		t.Fatal("legacy balance lookup should be ambiguous across accounts")
	}
}

func TestApplyAccountStateCreatesAccountAndCompatibilityBalance(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	state := model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("90"),
			Locked:   decimal.RequireFromString("10"),
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
		TsEvent:  ts,
		TsInit:   ts,
	}
	if err := c.ApplyAccountStateAt(state, ts); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	acct, ok := c.Account(model.AccountIDBinanceDefault)
	if !ok || acct.ID() != model.AccountIDBinanceDefault {
		t.Fatalf("account lookup failed: ok=%v acct=%v", ok, acct)
	}
	if acct, ok := c.AccountForVenue("BINANCE"); !ok || acct.ID() != model.AccountIDBinanceDefault {
		t.Fatalf("account for venue failed: ok=%v acct=%v", ok, acct)
	}
	if b, ok := c.Balance("USDT"); !ok || !b.Free.Equal(decimal.RequireFromString("90")) {
		t.Fatalf("compat balance=%+v ok=%v, want free 90", b, ok)
	}
}

func TestApplyAccountStateRemovesBalancesOmittedByNewSnapshot(t *testing.T) {
	c := New()
	firstAt := time.Unix(10, 0)
	state := model.AccountState{
		AccountID: "acct",
		Venue:     "VENUE",
		Type:      model.AccountMargin,
		Balances: []model.AccountBalance{
			{AccountID: "acct", Currency: "USDT", Total: decimal.NewFromInt(100), Free: decimal.NewFromInt(100)},
			{AccountID: "acct", Currency: "BTC", Total: decimal.NewFromInt(1), Free: decimal.NewFromInt(1)},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("VENUE", "acct", firstAt),
		TsEvent:  firstAt,
		TsInit:   firstAt,
	}
	if err := c.ApplyAccountStateAt(state, firstAt); err != nil {
		t.Fatalf("apply first account state: %v", err)
	}
	secondAt := firstAt.Add(time.Second)
	state.Balances = state.Balances[:1]
	state.EventID = model.AccountStateEventID("VENUE", "acct", secondAt)
	state.TsEvent = secondAt
	state.TsInit = secondAt
	if err := c.ApplyAccountStateAt(state, secondAt); err != nil {
		t.Fatalf("apply second account state: %v", err)
	}
	if _, ok := c.BalanceForAccount("acct", "BTC"); ok {
		t.Fatal("balance omitted by authoritative snapshot remained in compatibility cache")
	}
	if got, ok := c.BalanceForAccount("acct", "USDT"); !ok || !got.Total.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("retained USDT balance=%+v ok=%v", got, ok)
	}
}

func TestApplyAccountStateRejectsNonTradingReadyState(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	err := c.ApplyAccountStateAt(model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("90"),
		}},
		Reported: true,
		TsEvent:  ts,
		TsInit:   ts,
	}, ts)
	if err == nil {
		t.Fatal("account state without event id should not enter runtime")
	}
}

func TestApplyAccountStateAllowsMultipleAccountsForSameVenueAndAmbiguousFallback(t *testing.T) {
	c := New()
	ts := time.Unix(10, 0)
	state := model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("1"),
			Free:     decimal.RequireFromString("1"),
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
		TsEvent:  ts,
		TsInit:   ts,
	}
	if err := c.ApplyAccountStateAt(state, ts); err != nil {
		t.Fatalf("apply spot: %v", err)
	}
	state.AccountID = "BINANCE-002"
	state.Type = model.AccountMargin
	state.EventID = model.AccountStateEventID("BINANCE", "BINANCE-002", ts)
	if err := c.ApplyAccountStateAt(state, ts); err != nil {
		t.Fatalf("second account for same venue should be accepted under account-id ownership: %v", err)
	}
	if _, ok := c.AccountForVenue("BINANCE"); ok {
		t.Fatal("venue fallback should be ambiguous when two accounts exist for one venue")
	}
}

func TestBalanceAndPositionUpsertsIgnoreOlderVenueUpdates(t *testing.T) {
	c := New()
	newer := time.Unix(20, 0)
	older := newer.Add(-time.Second)
	c.UpsertBalance(model.AccountBalance{
		AccountID: "acct",
		Currency:  "USDT",
		Total:     decimal.RequireFromString("100"),
		Free:      decimal.RequireFromString("90"),
		Locked:    decimal.RequireFromString("10"),
		UpdatedAt: newer,
	})
	c.UpsertBalance(model.AccountBalance{
		AccountID: "acct",
		Currency:  "USDT",
		Total:     decimal.RequireFromString("1"),
		Free:      decimal.RequireFromString("1"),
		UpdatedAt: older,
	})
	if got, ok := c.BalanceForAccount("acct", "USDT"); !ok || !got.Total.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("older balance replaced newer update: %+v ok=%v", got, ok)
	}

	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: id, Side: enums.PosNet, Quantity: decimal.RequireFromString("2"), UpdatedAt: newer})
	c.UpsertPosition(model.Position{AccountID: "acct", InstrumentID: id, Side: enums.PosNet, Quantity: decimal.Zero, UpdatedAt: older})
	if got, ok := c.PositionForAccount("acct", id, enums.PosNet); !ok || !got.Quantity.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("older flat position deleted newer position: %+v ok=%v", got, ok)
	}
}

func TestBalanceUpsertSynchronizesAccountWithoutRefreshingSnapshotFreshness(t *testing.T) {
	c := New()
	initialAt := time.Unix(20, 0)
	state := model.AccountState{
		AccountID:    "ASTER-001",
		Venue:        "ASTER",
		BaseCurrency: "USDT",
		Type:         model.AccountCash,
		Balances: []model.AccountBalance{
			{AccountID: "ASTER-001", Currency: "USDT", Total: decimal.NewFromInt(100), Free: decimal.NewFromInt(100)},
			{AccountID: "ASTER-001", Currency: "ASTER", Total: decimal.Zero, Free: decimal.Zero},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("ASTER", "ASTER-001", initialAt),
		TsEvent:  initialAt,
		TsInit:   initialAt,
	}
	if err := c.ApplyAccountStateAt(state, initialAt); err != nil {
		t.Fatal(err)
	}
	acct, ok := c.Account("ASTER-001")
	if !ok {
		t.Fatal("account missing after initial state")
	}
	freshness := acct.Freshness()
	c.UpsertBalance(model.AccountBalance{
		AccountID: "ASTER-001",
		Currency:  "ASTER",
		Total:     decimal.RequireFromString("8.18"),
		Free:      decimal.RequireFromString("8.18"),
		UpdatedAt: initialAt.Add(time.Second),
	})
	free, ok := acct.BalanceFree("ASTER")
	if !ok || !free.Equal(decimal.RequireFromString("8.18")) {
		t.Fatalf("account ASTER free=%s ok=%v, want 8.18 true", free, ok)
	}
	if got := acct.Freshness().LastAccountStateAt; !got.Equal(freshness.LastAccountStateAt) {
		t.Fatalf("balance delta refreshed full account snapshot from %s to %s", freshness.LastAccountStateAt, got)
	}
}
