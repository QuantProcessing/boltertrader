package risk

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var inst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
var spotInst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindSpot}

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

func TestMaxOrderNotionalUsesContractMultiplier(t *testing.T) {
	instrument := &model.Instrument{
		ID:                 inst,
		ContractMultiplier: d("0.0001"),
	}
	req := buy("1", "100000")
	if got := orderNotional(req, instrument); !got.Equal(d("10")) {
		t.Fatalf("notional=%s, want 10", got)
	}

	e := New(Limits{MaxOrderNotional: d("10")}, cache.New())
	if err := e.Check(req, instrument); err != nil {
		t.Fatalf("true notional 10 should pass max-order-notional 10: %v", err)
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

func TestInstrumentMinNotionalUsesContractMultiplier(t *testing.T) {
	instrument := &model.Instrument{
		ID:                 inst,
		MinNotional:        d("11"),
		ContractMultiplier: d("0.0001"),
	}
	err := New(Limits{}, cache.New()).Check(buy("1", "100000"), instrument)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "notional 10") {
		t.Fatalf("true notional 10 should be below instrument minimum 11, got %v", err)
	}
}

func TestConfiguredNotionalChecksRejectWithoutReferencePrice(t *testing.T) {
	req := model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     d("1"),
	}
	tests := []struct {
		name       string
		limits     Limits
		instrument *model.Instrument
	}{
		{
			name:       "max order notional",
			limits:     Limits{MaxOrderNotional: d("100")},
			instrument: &model.Instrument{ID: inst},
		},
		{
			name:       "instrument min notional",
			instrument: &model.Instrument{ID: inst, MinNotional: d("10")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.limits, cache.New()).Check(req, tt.instrument)
			if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "reference price") {
				t.Fatalf("zero-reference market order should fail closed, got %v", err)
			}
		})
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

func TestMaxPositionQtyCountsRiskBearingOrders(t *testing.T) {
	tests := []struct {
		name      string
		status    enums.OrderStatus
		quantity  string
		filledQty string
	}{
		{name: "unknown", status: enums.StatusUnknown, quantity: "4", filledQty: "0"},
		{name: "pending new", status: enums.StatusPendingNew, quantity: "4", filledQty: "0"},
		{name: "new", status: enums.StatusNew, quantity: "4", filledQty: "0"},
		{name: "partially filled", status: enums.StatusPartiallyFilled, quantity: "6", filledQty: "2"},
		{name: "triggered", status: enums.StatusTriggered, quantity: "4", filledQty: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := cache.New()
			c.UpsertOrder(model.Order{
				Request: model.OrderRequest{
					InstrumentID: inst,
					ClientID:     "working-order",
					Side:         enums.SideBuy,
					Quantity:     d(tt.quantity),
					Price:        d("100"),
					PositionSide: enums.PosNet,
				},
				Status:    tt.status,
				FilledQty: d(tt.filledQty),
			})
			err := New(Limits{MaxPositionQty: d("5")}, c).Check(buy("2", "100"), nil)
			if !errors.Is(err, ErrRiskRejected) {
				t.Fatalf("working %s order plus request should exceed max position, got %v", tt.status, err)
			}
		})
	}
}

func TestMaxPositionQtyDoesNotNetOpposingWorkingOrders(t *testing.T) {
	c := cache.New()
	for _, order := range []model.Order{
		{
			Request: model.OrderRequest{InstrumentID: inst, ClientID: "working-buy", Side: enums.SideBuy, Quantity: d("4"), Price: d("100")},
			Status:  enums.StatusPendingNew,
		},
		{
			Request: model.OrderRequest{InstrumentID: inst, ClientID: "working-sell", Side: enums.SideSell, Quantity: d("4"), Price: d("100")},
			Status:  enums.StatusPendingNew,
		},
	} {
		c.UpsertOrder(order)
	}

	err := New(Limits{MaxPositionQty: d("5")}, c).Check(buy("2", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("opposing working sell must not offset worst-case buy exposure, got %v", err)
	}
}

func TestNonPositiveQty(t *testing.T) {
	e := New(Limits{}, cache.New())
	if err := e.Check(buy("0", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("zero qty should be rejected")
	}
}

func TestSpotBuyRejectsInsufficientQuoteBalance(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("100"), Available: d("100")})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("2"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := e.Check(req, inst); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot buy should reject when quote balance is insufficient, got %v", err)
	}
}

func TestSpotBuyReservesQuoteForWorkingOrders(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "100", "1")
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			AccountID:    "T:spot",
			InstrumentID: spotInst,
			ClientID:     "working-buy",
			Side:         enums.SideBuy,
			Quantity:     d("0.6"),
			Price:        d("100"),
		},
		Status: enums.StatusPendingNew,
	})
	req := model.OrderRequest{
		AccountID:    "T:spot",
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Quantity:     d("0.5"),
		Price:        d("100"),
	}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := New(Limits{}, c).WithClock(func() time.Time { return now }).Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("working buy reserves 60, so another 50 must exceed quote free 100, got %v", err)
	}
}

func TestSpotSellReservesBaseForWorkingOrders(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "100", "1")
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			AccountID:    "T:spot",
			InstrumentID: spotInst,
			ClientID:     "working-sell",
			Side:         enums.SideSell,
			Quantity:     d("0.6"),
			Price:        d("100"),
		},
		Status: enums.StatusPendingNew,
	})
	req := model.OrderRequest{
		AccountID:    "T:spot",
		InstrumentID: spotInst,
		Side:         enums.SideSell,
		Quantity:     d("0.5"),
		Price:        d("100"),
	}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := New(Limits{}, c).WithClock(func() time.Time { return now }).Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("working sell reserves 0.6, so another 0.5 must exceed base free 1, got %v", err)
	}
}

func TestSpotRiskRejectsNoAccountStateUnlessLegacyFallback(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Available: d("1000000")})
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := New(Limits{}, c).Check(req, inst)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "no account state") {
		t.Fatalf("spot risk without account state should fail closed, got %v", err)
	}
	if err := New(Limits{}, c).AllowLegacyBalanceFallback().Check(req, inst); err != nil {
		t.Fatalf("explicit legacy fallback should allow cached balance path: %v", err)
	}
}

func TestSpotMarketBuyRejectsWithoutReferencePrice(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Available: d("1000000")})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("1")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := e.Check(req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot market buy without reference price should fail closed, got %v", err)
	}
}

func TestSpotBuyRejectsWithoutInstrumentMetadata(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Available: d("1000000")})
	e := New(Limits{}, c)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}

	err := e.Check(req, nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot buy without instrument metadata should fail closed, got %v", err)
	}
}

func TestSpotBuyRejectsMissingQuoteMetadata(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Available: d("1000000")})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC"}

	err := e.Check(req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot buy without quote metadata should fail closed, got %v", err)
	}
}

func TestSpotSellRejectsMissingBaseMetadata(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "BTC", Total: d("1000000"), Available: d("1000000")})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Quote: "USDT"}

	err := e.Check(req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot sell without base metadata should fail closed, got %v", err)
	}
}

func TestSpotSellRejectsInsufficientBaseBalanceEvenWithSyntheticPosition(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "BTC", Total: d("0"), Available: d("0")})
	c.UpsertPosition(model.Position{InstrumentID: spotInst, Side: enums.PosNet, Quantity: d("10")})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := e.Check(req, inst); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot sell should reject from base balance, not synthetic position, got %v", err)
	}
}

func TestSpotBuyReservationAggregatesQuoteAssetAcrossInstruments(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000"), Free: d("1000")})
	ethUSDT := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: ethUSDT,
			ClientID:     "working-eth-buy",
			Side:         enums.SideBuy,
			Quantity:     d("6"),
			Price:        d("100"),
		},
		Status: enums.StatusNew,
	})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("5"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := e.Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("ETH-USDT and BTC-USDT buys must share the USDT reservation, got %v", err)
	}
}

func TestSpotSellReservationAggregatesBaseAssetAcrossInstruments(t *testing.T) {
	c := cache.New()
	c.UpsertBalance(model.AccountBalance{Currency: "BTC", Total: d("1"), Free: d("1")})
	btcUSDC := model.InstrumentID{Venue: "T", Symbol: "BTC-USDC", Kind: enums.KindSpot}
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			InstrumentID: btcUSDC,
			ClientID:     "working-btc-sell",
			Side:         enums.SideSell,
			Quantity:     d("0.6"),
			Price:        d("100"),
		},
		Status: enums.StatusNew,
	})
	e := New(Limits{}, c).AllowLegacyBalanceFallback()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("0.5"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := e.Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("BTC-USDC and BTC-USDT sells must share the BTC reservation, got %v", err)
	}
}

func TestSpotAccountFreeDoesNotDoubleCountReflectedWorkingOrder(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	accountID := "T:spot"
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    accountID,
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{AccountID: accountID, Currency: "USDT", Total: d("100"), Free: d("60"), Locked: d("40"), UpdatedAt: now},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", accountID, now),
		TsEvent:  now,
		TsInit:   now,
	}, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	c.UpsertOrder(model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: spotInst,
			ClientID:     "reflected-working-buy",
			Side:         enums.SideBuy,
			Quantity:     d("0.4"),
			Price:        d("100"),
		},
		Status:    enums.StatusNew,
		CreatedAt: now.Add(-time.Second),
		UpdatedAt: now.Add(-time.Second),
	})
	e := New(Limits{}, c).WithClock(func() time.Time { return now })
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: spotInst,
		ClientID:     "new-buy",
		Side:         enums.SideBuy,
		Quantity:     d("0.5"),
		Price:        d("100"),
	}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := e.Check(req, spot); err != nil {
		t.Fatalf("free 60 already excludes locked 40, so a new 50 buy should pass: %v", err)
	}
}

func TestSpotRiskUsesAccountFreeBalanceWhenPresent(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000"), Free: d("1000")})
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    "T:spot",
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("50"), Free: d("50")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:spot", now),
		TsEvent:  now,
		TsInit:   now,
	}, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	e := New(Limits{}, c).WithClock(func() time.Time { return now })
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := e.Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "free 50") {
		t.Fatalf("spot buy should use account free balance, got %v", err)
	}
}

func TestRiskUsesExplicitOrderAccountID(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyCashAccount(t, c, "T:poor", now, "10")
	applyCashAccount(t, c, "T:rich", now, "1000")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	req := model.OrderRequest{AccountID: "T:rich", InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := e.Check(req, spot); err != nil {
		t.Fatalf("explicit rich account should pass, got %v", err)
	}
	req.AccountID = "T:poor"
	err := e.Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "account T:poor") {
		t.Fatalf("explicit poor account should reject with account id, got %v", err)
	}
}

func TestRiskAllowsMarginAccountForSpotCashPath(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyUnifiedMarginAccount(t, c, "T:unified", now)
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	req := model.OrderRequest{AccountID: "T:unified", InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := e.Check(req, spot); err != nil {
		t.Fatalf("margin account with reported spot balances should pass spot risk, got %v", err)
	}
}

func TestRiskRejectsAmbiguousVenueAccountFallback(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyCashAccount(t, c, "T:acct-a", now, "1000")
	applyCashAccount(t, c, "T:acct-b", now, "1000")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := e.Check(req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "ambiguous account state") {
		t.Fatalf("missing account id should reject ambiguous venue fallback, got %v", err)
	}
}

func TestAccountRequiredRejectsNoAccount(t *testing.T) {
	e := New(Limits{}, cache.New()).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "no account state") {
		t.Fatalf("want no-account rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsStaleAccount(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now.Add(-time.Minute), "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "stale account state") {
		t.Fatalf("want stale-account rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsMissingPrice(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	req := model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Quantity: d("1")}

	err := e.Check(req, nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "reference price") {
		t.Fatalf("want missing-price rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsMissingFreeBalance(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "BTC")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "missing free balance for USDT") {
		t.Fatalf("want missing-free rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsUnsupportedAccountType(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    "T:spot",
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1000"), Free: d("1000")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:spot", now),
		TsEvent:  now,
		TsInit:   now,
	}, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "unsupported account type") {
		t.Fatalf("want unsupported-account-type rejection, got %v", err)
	}
}

func TestProductCapabilityGateRejectsUnsupportedTradingProduct(t *testing.T) {
	e := New(Limits{}, cache.New())
	e.SetRuntimeCapabilities()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "unsupported product") {
		t.Fatalf("want unsupported product rejection, got %v", err)
	}
}

func TestProductCapabilityGateRequiresAccountStateSupport(t *testing.T) {
	e := New(Limits{}, cache.New()).RequireAccountState()
	e.SetRuntimeCapabilities(contract.Capabilities{
		Venue:    "T",
		Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Trading:  contract.TradingCapabilities{Submit: true},
	})

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "account-state-backed risk") {
		t.Fatalf("want missing account-state product support rejection, got %v", err)
	}
}

func TestAccountRequiredAllowsFreshMarginAccountWithFreeBalance(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceDefault, now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	if err := e.Check(buy("1", "100"), nil); err != nil {
		t.Fatalf("fresh account should pass risk check: %v", err)
	}
}

func TestUnifiedCollateralMarginCanUseBaseCurrencyFreeBalance(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	state := model.AccountState{
		AccountID:    "T:unified",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USD",
		Balances: []model.AccountBalance{
			{Currency: "USD", Total: d("1000"), Free: d("1000")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:unified", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply unified margin account: %v", err)
	}
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	req := model.OrderRequest{
		AccountID:    "T:unified",
		InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDC", Kind: enums.KindPerp},
		Side:         enums.SideBuy,
		Quantity:     d("1"),
		Price:        d("100"),
	}
	inst := &model.Instrument{ID: req.InstrumentID, Settle: "USDC"}

	err := e.Check(req, inst)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "missing free balance for USDC") {
		t.Fatalf("base-currency free balance should not be used as generic unified collateral, got %v", err)
	}
}

func applyMarginAccount(t *testing.T, c *cache.Cache, accountID string, eventTime time.Time, balanceCurrency string) {
	t.Helper()
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: balanceCurrency, Total: d("1000"), Free: d("1000")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", accountID, eventTime),
		TsEvent:  eventTime,
		TsInit:   eventTime,
	}
	if err := c.ApplyAccountStateAt(state, eventTime); err != nil {
		t.Fatalf("apply margin account: %v", err)
	}
}

func applyCashAccount(t *testing.T, c *cache.Cache, accountID string, eventTime time.Time, free string) {
	t.Helper()
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{AccountID: accountID, Currency: "USDT", Total: d(free), Free: d(free)},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", accountID, eventTime),
		TsEvent:  eventTime,
		TsInit:   eventTime,
	}
	if err := c.ApplyAccountStateAt(state, eventTime); err != nil {
		t.Fatalf("apply cash account: %v", err)
	}
}

func applyUnifiedMarginAccount(t *testing.T, c *cache.Cache, accountID string, eventTime time.Time) {
	t.Helper()
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{AccountID: accountID, Currency: "USDT", Total: d("1000"), Free: d("1000")},
			{AccountID: accountID, Currency: "BTC", Total: d("1"), Free: d("1")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", accountID, eventTime),
		TsEvent:  eventTime,
		TsInit:   eventTime,
	}
	if err := c.ApplyAccountStateAt(state, eventTime); err != nil {
		t.Fatalf("apply unified margin account: %v", err)
	}
}

func applySpotCashAccount(t *testing.T, c *cache.Cache, eventTime time.Time, quoteFree, baseFree string) {
	t.Helper()
	accountID := "T:spot"
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{AccountID: accountID, Currency: "USDT", Total: d(quoteFree), Free: d(quoteFree)},
			{AccountID: accountID, Currency: "BTC", Total: d(baseFree), Free: d(baseFree)},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", accountID, eventTime),
		TsEvent:  eventTime,
		TsInit:   eventTime,
	}
	if err := c.ApplyAccountStateAt(state, eventTime); err != nil {
		t.Fatalf("apply spot account: %v", err)
	}
}
