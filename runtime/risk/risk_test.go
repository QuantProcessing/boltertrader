package risk

import (
	"context"
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

func checkRisk(e *Engine, req model.OrderRequest, inst *model.Instrument) error {
	release, err := e.CheckSubmission(context.Background(), req, inst)
	if release != nil {
		release()
	}
	return err
}

var inst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
var spotInst = model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindSpot}

func configureRuntimeCapabilities(e *Engine, venue string, kinds ...enums.InstrumentKind) {
	execution := contract.Capabilities{Venue: venue, Trading: contract.TradingCapabilities{Submit: true}}
	account := contract.Capabilities{Venue: venue}
	for _, kind := range kinds {
		execution.Products = append(execution.Products, contract.ProductCapability{Kind: kind, Trading: true})
		account.Products = append(account.Products, contract.ProductCapability{Kind: kind, Account: true})
	}
	e.SetRuntimeCapabilities(&execution, &account)
}

func configuredRisk(limits Limits, c *cache.Cache, now time.Time, kinds ...enums.InstrumentKind) *Engine {
	e := New(limits, c).WithClock(func() time.Time { return now })
	configureRuntimeCapabilities(e, "T", kinds...)
	return e
}

func buy(qty, price string) model.OrderRequest {
	return model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Quantity: d(qty), Price: d(price)}
}

func TestMaxOrderQty(t *testing.T) {
	e := New(Limits{MaxOrderQty: d("5")}, cache.New())
	if err := checkRisk(e, buy("3", "100"), nil); err != nil {
		t.Fatalf("3 should pass: %v", err)
	}
	err := checkRisk(e, buy("6", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("6 should be rejected, got %v", err)
	}
}

func TestMaxOrderNotional(t *testing.T) {
	e := New(Limits{MaxOrderNotional: d("1000")}, cache.New())
	if err := checkRisk(e, buy("5", "100"), nil); err != nil { // 500
		t.Fatalf("notional 500 should pass: %v", err)
	}
	if err := checkRisk(e, buy("20", "100"), nil); !errors.Is(err, ErrRiskRejected) { // 2000
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
	if err := checkRisk(e, req, instrument); err != nil {
		t.Fatalf("true notional 10 should pass max-order-notional 10: %v", err)
	}
}

func TestKillSwitch(t *testing.T) {
	e := New(Limits{}, cache.New())
	e.Trip()
	if err := checkRisk(e, buy("1", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("kill switch should reject all orders")
	}
	e.Reset()
	if err := checkRisk(e, buy("1", "100"), nil); err != nil {
		t.Fatalf("after reset should pass: %v", err)
	}
}

func TestDuplicateClientID(t *testing.T) {
	e := New(Limits{}, cache.New())
	req := buy("1", "100")
	req.ClientID = "dup-1"
	if err := checkRisk(e, req, nil); err != nil {
		t.Fatalf("first should pass: %v", err)
	}
	if err := checkRisk(e, req, nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("duplicate client id should be rejected")
	}
}

func TestInstrumentMinimums(t *testing.T) {
	e := New(Limits{}, cache.New())
	in := &model.Instrument{MinQty: d("0.01"), MinNotional: d("10")}
	if err := checkRisk(e, buy("0.005", "100"), in); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("below min qty should be rejected")
	}
	if err := checkRisk(e, buy("0.02", "100"), in); err != nil { // qty ok, notional 2 < 10
		// notional 0.02*100 = 2 < 10 => rejected
		if !errors.Is(err, ErrRiskRejected) {
			t.Fatalf("unexpected: %v", err)
		}
	}
	if err := checkRisk(e, buy("0.2", "100"), in); err != nil { // notional 20 ok
		t.Fatalf("valid order should pass: %v", err)
	}
}

func TestInstrumentMinNotionalUsesContractMultiplier(t *testing.T) {
	instrument := &model.Instrument{
		ID:                 inst,
		MinNotional:        d("11"),
		ContractMultiplier: d("0.0001"),
	}
	err := checkRisk(New(Limits{}, cache.New()), buy("1", "100000"), instrument)
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
			err := checkRisk(New(tt.limits, cache.New()), req, tt.instrument)
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
	if err := checkRisk(e, buy("2", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("resulting position 6 should be rejected")
	}
	// Buying 1 => resulting 5 == limit => ok.
	if err := checkRisk(e, buy("1", "100"), nil); err != nil {
		t.Fatalf("resulting position 5 should pass: %v", err)
	}
	// Selling 2 => resulting 2 => ok.
	sell := model.OrderRequest{InstrumentID: inst, Side: enums.SideSell, Quantity: d("2"), Price: d("100")}
	if err := checkRisk(e, sell, nil); err != nil {
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
			err := checkRisk(New(Limits{MaxPositionQty: d("5")}, c), buy("2", "100"), nil)
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

	err := checkRisk(New(Limits{MaxPositionQty: d("5")}, c), buy("2", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("opposing working sell must not offset worst-case buy exposure, got %v", err)
	}
}

func TestNonPositiveQty(t *testing.T) {
	e := New(Limits{}, cache.New())
	if err := checkRisk(e, buy("0", "100"), nil); !errors.Is(err, ErrRiskRejected) {
		t.Fatal("zero qty should be rejected")
	}
}

func TestSpotBuyRejectsInsufficientQuoteBalance(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "100", "1")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("2"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, inst); !errors.Is(err, ErrRiskRejected) {
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

	err := checkRisk(configuredRisk(Limits{}, c, now, enums.KindSpot), req, spot)
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

	err := checkRisk(configuredRisk(Limits{}, c, now, enums.KindSpot), req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("working sell reserves 0.6, so another 0.5 must exceed base free 1, got %v", err)
	}
}

func TestSpotRiskRejectsWithoutAuthoritativeAccountState(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Free: d("1000000")})
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(configuredRisk(Limits{}, c, now, enums.KindSpot), req, inst)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "no account state") {
		t.Fatalf("spot risk without account state should fail closed, got %v", err)
	}
}

func TestSpotMarketBuyRejectsWithoutReferencePrice(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "1000000", "1")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("1")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(e, req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot market buy without reference price should fail closed, got %v", err)
	}
}

func TestSpotBuyRejectsWithoutInstrumentMetadata(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	c.UpsertBalance(model.AccountBalance{Currency: "USDT", Total: d("1000000"), Free: d("1000000")})
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}

	err := checkRisk(e, req, nil)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot buy without instrument metadata should fail closed, got %v", err)
	}
}

func TestSpotBuyRejectsMissingQuoteMetadata(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "1000000", "1")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC"}

	err := checkRisk(e, req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot buy without quote metadata should fail closed, got %v", err)
	}
}

func TestSpotSellRejectsMissingBaseMetadata(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "1000000", "1000000")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Quote: "USDT"}

	err := checkRisk(e, req, inst)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot sell without base metadata should fail closed, got %v", err)
	}
}

func TestSpotSellRejectsInsufficientBaseBalanceEvenWithSyntheticPosition(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "100", "0")
	c.UpsertPosition(model.Position{InstrumentID: spotInst, Side: enums.PosNet, Quantity: d("10")})
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("1"), Price: d("100")}
	inst := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, inst); !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("spot sell should reject from base balance, not synthetic position, got %v", err)
	}
}

func TestSpotBuyReservationAggregatesQuoteAssetAcrossInstruments(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "1000", "1")
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
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("5"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(e, req, spot)
	if !errors.Is(err, ErrRiskRejected) {
		t.Fatalf("ETH-USDT and BTC-USDT buys must share the USDT reservation, got %v", err)
	}
}

func TestSpotSellReservationAggregatesBaseAssetAcrossInstruments(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	applySpotCashAccount(t, c, now, "1000", "1")
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
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideSell, Quantity: d("0.5"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(e, req, spot)
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
	c.MarkAccountReconciled(accountID, now)
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
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: spotInst,
		ClientID:     "new-buy",
		Side:         enums.SideBuy,
		Quantity:     d("0.5"),
		Price:        d("100"),
	}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, spot); err != nil {
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
	c.MarkAccountReconciled("T:spot", now)
	e := configuredRisk(Limits{}, c, now, enums.KindSpot)
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(e, req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "free 50") {
		t.Fatalf("spot buy should use account free balance, got %v", err)
	}
}

func TestRiskUsesExplicitOrderAccountID(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyCashAccount(t, c, "T:poor", now, "10")
	applyCashAccount(t, c, "T:rich", now, "1000")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot).RequireAccountState()
	req := model.OrderRequest{AccountID: "T:rich", InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, spot); err != nil {
		t.Fatalf("explicit rich account should pass, got %v", err)
	}
	req.AccountID = "T:poor"
	err := checkRisk(e, req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "account T:poor") {
		t.Fatalf("explicit poor account should reject with account id, got %v", err)
	}
}

func TestRiskAllowsMarginAccountForSpotCashPath(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyUnifiedMarginAccount(t, c, "T:unified", now)
	e := configuredRisk(Limits{}, c, now, enums.KindSpot).RequireAccountState()
	req := model.OrderRequest{AccountID: "T:unified", InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, spot); err != nil {
		t.Fatalf("margin account with reported spot balances should pass spot risk, got %v", err)
	}
}

func TestRiskRejectsAmbiguousVenueAccountFallback(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyCashAccount(t, c, "T:acct-a", now, "1000")
	applyCashAccount(t, c, "T:acct-b", now, "1000")
	e := configuredRisk(Limits{}, c, now, enums.KindSpot).RequireAccountState()
	req := model.OrderRequest{InstrumentID: spotInst, Side: enums.SideBuy, Quantity: d("1"), Price: d("100")}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	err := checkRisk(e, req, spot)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "ambiguous account state") {
		t.Fatalf("missing account id should reject ambiguous venue fallback, got %v", err)
	}
}

func TestAccountRequiredRejectsNoAccount(t *testing.T) {
	now := time.Unix(100, 0)
	e := configuredRisk(Limits{}, cache.New(), now, enums.KindPerp).RequireAccountState()

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "no account state") {
		t.Fatalf("want no-account rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsStaleAccount(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", now.Add(-time.Minute), "USDT")
	e := configuredRisk(Limits{}, c, now, enums.KindPerp).RequireAccountState()

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "stale account state") {
		t.Fatalf("want stale-account rejection, got %v", err)
	}
}

func TestAccountRequiredMarginReadinessDoesNotRequireLocalCapacityInputs(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", now, "BTC")
	e := configuredRisk(Limits{}, c, now, enums.KindPerp).RequireAccountState()
	req := model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Quantity: d("1")}

	if err := checkRisk(e, req, nil); err != nil {
		t.Fatalf("authoritative margin readiness must not infer price or settlement capacity: %v", err)
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
	c.MarkAccountReconciled("T:spot", now)
	e := configuredRisk(Limits{}, c, now, enums.KindPerp).RequireAccountState()

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "unsupported account type") {
		t.Fatalf("want unsupported-account-type rejection, got %v", err)
	}
}

func TestProductCapabilityGateRejectsUnsupportedTradingProduct(t *testing.T) {
	e := New(Limits{}, cache.New())
	e.SetRuntimeCapabilities(nil, nil)

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "unsupported product") {
		t.Fatalf("want unsupported product rejection, got %v", err)
	}
}

func TestProductCapabilityGateRequiresAccountStateSupport(t *testing.T) {
	e := New(Limits{}, cache.New()).RequireAccountState()
	execution := contract.Capabilities{
		Venue:    "T",
		Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Trading:  contract.TradingCapabilities{Submit: true},
	}
	e.SetRuntimeCapabilities(&execution, nil)

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "account-state-backed risk") {
		t.Fatalf("want missing account-state product support rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsWhenRuntimeCapabilitiesWereNotConfigured(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("want missing configured authoritative flow rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsStreamOnlyStateBeforeAuthoritativeReconcile(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyUnreconciledMarginAccount(t, c, "T:acct", now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	configureRuntimeCapabilities(e, "T", enums.KindPerp)

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "authoritative reconciliation") {
		t.Fatalf("want stream-only account-state rejection, got %v", err)
	}
}

func TestAccountRequiredAllowsStreamRefreshAfterAuthoritativeReconcile(t *testing.T) {
	initial := time.Unix(100, 0)
	streamAt := initial.Add(time.Minute)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", initial, "USDT")
	applyUnreconciledMarginAccount(t, c, "T:acct", streamAt, "USDT")
	e := configuredRisk(Limits{}, c, streamAt, enums.KindPerp).RequireAccountState()

	if err := checkRisk(e, buy("1", "100"), nil); err != nil {
		t.Fatalf("stream refresh after initial authoritative reconciliation should remain ready: %v", err)
	}
}

func TestAccountRequiredDoesNotGateReduceOnlyOrderOnSnapshotFreshness(t *testing.T) {
	now := time.Unix(100, 0)
	e := New(Limits{}, cache.New()).WithClock(func() time.Time { return now }).RequireAccountState()
	execution := contract.Capabilities{
		Venue: "T", Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Trading: contract.TradingCapabilities{Submit: true},
	}
	e.SetRuntimeCapabilities(&execution, nil)
	req := buy("1", "100")
	req.ReduceOnly = true

	if err := checkRisk(e, req, nil); err != nil {
		t.Fatalf("reduce-only order should not require risk-increasing account readiness: %v", err)
	}
}

func TestAccountRequiredReduceOnlyStillRequiresRuntimeCapabilities(t *testing.T) {
	now := time.Unix(100, 0)
	e := New(Limits{}, cache.New()).WithClock(func() time.Time { return now }).RequireAccountState()
	req := buy("1", "100")
	req.ReduceOnly = true

	err := checkRisk(e, req, nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "runtime capabilities") {
		t.Fatalf("reduce-only must still prove execution support, got %v", err)
	}
}

func TestAccountRequiredRejectsExplicitAccountFromWrongVenue(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	state := model.AccountState{
		AccountID:    "shared-account-id",
		Venue:        "OTHER",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances:     []model.AccountBalance{{AccountID: "shared-account-id", Currency: "USDT"}},
		Reported:     true,
		EventID:      model.AccountStateEventID("OTHER", "shared-account-id", now),
		TsEvent:      now,
		TsInit:       now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply wrong-venue account state: %v", err)
	}
	c.MarkAccountReconciled(state.AccountID, now)
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	configureRuntimeCapabilities(e, "T", enums.KindPerp)
	req := buy("1", "100")
	req.AccountID = state.AccountID

	err := checkRisk(e, req, nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(strings.ToLower(err.Error()), "venue") {
		t.Fatalf("want wrong-venue account rejection, got %v", err)
	}
}

func TestAccountRequiredRejectsAccountCapabilityFromWrongVenue(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", now, "USDT")
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	execution := contract.Capabilities{
		Venue: "T", Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
		Trading: contract.TradingCapabilities{Submit: true},
	}
	account := contract.Capabilities{
		Venue: "OTHER", Products: []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
	}
	e.SetRuntimeCapabilities(&execution, &account)

	err := checkRisk(e, buy("1", "100"), nil)
	if !errors.Is(err, ErrRiskRejected) || !strings.Contains(err.Error(), "account-state-backed risk") {
		t.Fatalf("want wrong-venue account capability rejection, got %v", err)
	}
}

func TestProductCapabilityGateUsesConfiguredAccountClientIndependentOfStreamBit(t *testing.T) {
	now := time.Unix(100, 0)
	for _, streaming := range []bool{false, true} {
		t.Run(map[bool]string{false: "snapshot-only", true: "snapshot-and-stream"}[streaming], func(t *testing.T) {
			c := cache.New()
			applyMarginAccount(t, c, "T:margin", now, "USDT")
			e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
			execution := contract.Capabilities{
				Venue:    "T",
				Products: []contract.ProductCapability{{Kind: enums.KindPerp, Trading: true}},
				Trading:  contract.TradingCapabilities{Submit: true},
			}
			account := contract.Capabilities{
				Venue:     "T",
				Products:  []contract.ProductCapability{{Kind: enums.KindPerp, Account: true}},
				Streaming: contract.StreamCapabilities{AccountState: streaming},
			}
			e.SetRuntimeCapabilities(&execution, &account)

			if err := checkRisk(e, buy("1", "100"), nil); err != nil {
				t.Fatalf("fresh configured account client should pass independently of stream bit: %v", err)
			}
		})
	}
}

func TestAccountRequiredAllowsFreshMarginAccountWithFreeBalance(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	applyMarginAccount(t, c, "T:acct", now, "USDT")
	e := configuredRisk(Limits{}, c, now, enums.KindPerp).RequireAccountState()

	if err := checkRisk(e, buy("1", "100"), nil); err != nil {
		t.Fatalf("fresh account should pass risk check: %v", err)
	}
}

func TestAccountRequiredDoesNotInferMarginCapacityFromBalanceFree(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	state := model.AccountState{
		AccountID:    "T:margin",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{{
			AccountID: "T:margin",
			Currency:  "USDT",
			Total:     d("1000"),
			Free:      decimal.Zero,
		}},
		Summary: &model.AccountSummary{
			SettlementCurrency:  "USDT",
			Equity:              d("1000"),
			AvailableCollateral: d("900"),
			UpdatedAt:           now,
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:margin", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply margin account: %v", err)
	}
	c.MarkAccountReconciled(state.AccountID, now)
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	configureRuntimeCapabilities(e, "T", enums.KindPerp)
	req := buy("1", "100")
	req.AccountID = state.AccountID

	if err := checkRisk(e, req, nil); err != nil {
		t.Fatalf("fresh margin account must leave capacity to the venue: %v", err)
	}
}

func TestSpotRiskDoesNotInferCashCapacityFromUnifiedMarginFree(t *testing.T) {
	now := time.Unix(100, 0)
	c := cache.New()
	state := model.AccountState{
		AccountID:    "T:unified",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{AccountID: "T:unified", Currency: "USDT", Total: d("1000"), Free: decimal.Zero},
			{AccountID: "T:unified", Currency: "BTC", Total: d("1"), Free: decimal.Zero},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:unified", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply unified margin account: %v", err)
	}
	c.MarkAccountReconciled(state.AccountID, now)
	e := New(Limits{}, c).WithClock(func() time.Time { return now }).RequireAccountState()
	configureRuntimeCapabilities(e, "T", enums.KindSpot)
	req := model.OrderRequest{
		AccountID: state.AccountID, InstrumentID: spotInst, Side: enums.SideBuy,
		Quantity: d("1"), Price: d("100"),
	}
	spot := &model.Instrument{ID: spotInst, Base: "BTC", Quote: "USDT"}

	if err := checkRisk(e, req, spot); err != nil {
		t.Fatalf("unified margin spot capacity must remain server-authoritative: %v", err)
	}
}

func applyMarginAccount(t *testing.T, c *cache.Cache, accountID string, eventTime time.Time, balanceCurrency string) {
	t.Helper()
	applyUnreconciledMarginAccount(t, c, accountID, eventTime, balanceCurrency)
	c.MarkAccountReconciled(accountID, eventTime)
}

func applyUnreconciledMarginAccount(t *testing.T, c *cache.Cache, accountID string, eventTime time.Time, balanceCurrency string) {
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
	c.MarkAccountReconciled(accountID, eventTime)
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
	c.MarkAccountReconciled(accountID, eventTime)
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
	c.MarkAccountReconciled(accountID, eventTime)
}
