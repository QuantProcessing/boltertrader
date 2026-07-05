package risk

import (
	"errors"
	"strings"
	"testing"
	"time"

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
		ModeInfo: model.AccountModeInfo{
			Venue:        "T",
			AccountID:    "T:spot",
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   now,
			Source:       "test",
		},
		TsEvent: now,
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
	applyMarginAccount(t, c, model.AccountIDBinanceUSDM, now.Add(-time.Minute), "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "stale account state") {
		t.Fatalf("want stale-account rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsMissingPrice(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceUSDM, now, "USDT")
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
	applyMarginAccount(t, c, model.AccountIDBinanceUSDM, now, "BTC")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "missing free balance for USDT") {
		t.Fatalf("want missing-free rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsUnsupportedAccountMode(t *testing.T) {
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
		ModeInfo: model.AccountModeInfo{
			Venue:        "T",
			AccountID:    "T:spot",
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   now,
			Source:       "test",
		},
		TsEvent: now,
	}, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := e.Check(buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "unsupported account mode") {
		t.Fatalf("want unsupported-mode rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsEmptyProductScope(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    "T:perp",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1000"), Free: d("1000")},
		},
		TsEvent: now,
	}, now); err == nil {
		t.Fatal("account admission should reject empty product scope before risk sees it")
	}
}

func TestAccountRequiredAllowsFreshMarginAccountWithFreeBalance(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, model.AccountIDBinanceUSDM, now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	if err := e.Check(buy("1", "100"), nil); err != nil {
		t.Fatalf("fresh account should pass risk check: %v", err)
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
		ModeInfo: model.AccountModeInfo{
			Venue:        "T",
			AccountID:    accountID,
			AccountMode:  "perp",
			ProductScope: []enums.InstrumentKind{enums.KindPerp},
			Verified:     true,
			VerifiedAt:   eventTime,
			Source:       "test",
		},
		TsEvent: eventTime,
	}
	if err := c.ApplyAccountStateAt(state, eventTime); err != nil {
		t.Fatalf("apply margin account: %v", err)
	}
}
