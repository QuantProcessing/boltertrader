package portfolio

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
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

func instrumentFill(id model.InstrumentID, side enums.OrderSide, price, qty, fee, feeCurrency string) model.Fill {
	return model.Fill{
		InstrumentID: id,
		Side:         side,
		Price:        d(price),
		Quantity:     d(qty),
		Fee:          d(fee),
		FeeCurrency:  feeCurrency,
	}
}

type testInstrumentProvider struct {
	instruments map[string]*model.Instrument
}

func (p *testInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	instrument, ok := p.instruments[id.String()]
	return instrument, ok
}

func (p *testInstrumentProvider) All() []*model.Instrument {
	out := make([]*model.Instrument, 0, len(p.instruments))
	for _, instrument := range p.instruments {
		out = append(out, instrument)
	}
	return out
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

func TestSpotBaseFeeReducesNetQty(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}

	pf.OnFill(instrumentFill(id, enums.SideBuy, "100", "1", "0.01", "ETH"), enums.PosNet)

	if got := pf.NetQty(id, enums.PosNet); !got.Equal(d("0.99")) {
		t.Fatalf("netQty=%s, want 0.99 after base-asset fee", got)
	}
	if got := pf.AvgPrice(id, enums.PosNet); !got.GreaterThan(d("100")) {
		t.Fatalf("avg=%s, want above fill price after base-asset fee", got)
	}
	if got := pf.Fees(); !got.IsZero() {
		t.Fatalf("quote fees=%s, want 0 for base-asset fee", got)
	}
	if got := pf.FeesByCurrency()["ETH"]; !got.Equal(d("0.01")) {
		t.Fatalf("ETH fees=%s, want 0.01", got)
	}
}

func TestSpotBaseFeeRoundTripCanReturnFlat(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}

	pf.OnFill(instrumentFill(id, enums.SideBuy, "100", "1", "0.01", "ETH"), enums.PosNet)
	pf.OnFill(instrumentFill(id, enums.SideSell, "110", "0.99", "0.2", "USDT"), enums.PosNet)

	if got := pf.NetQty(id, enums.PosNet); !got.IsZero() {
		t.Fatalf("netQty=%s, want flat after selling net base quantity", got)
	}
}

func TestSpotBaseFeesAreTrackedByCurrencyAndNotSubtractedFromQuotePnL(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}

	pf.OnFill(instrumentFill(id, enums.SideBuy, "100", "1", "0.01", "ETH"), enums.PosNet)
	pf.OnFill(instrumentFill(id, enums.SideSell, "110", "0.99", "0.2", "USDT"), enums.PosNet)

	if got := pf.RealizedPnL(); !got.Round(8).Equal(d("8.9")) {
		t.Fatalf("realized=%s, want 8.9", got)
	}
	if got := pf.Fees(); !got.Equal(d("0.2")) {
		t.Fatalf("quote fees=%s, want 0.2", got)
	}
	if got := pf.RealizedPnLNetFees(); !got.Round(8).Equal(d("8.7")) {
		t.Fatalf("net=%s, want 8.7", got)
	}
	feesByCurrency := pf.FeesByCurrency()
	if got := feesByCurrency["ETH"]; !got.Equal(d("0.01")) {
		t.Fatalf("ETH fees=%s, want 0.01", got)
	}
	if got := feesByCurrency["USDT"]; !got.Equal(d("0.2")) {
		t.Fatalf("USDT fees=%s, want 0.2", got)
	}
}

func TestSpotSellBaseFeeDoesNotDoubleCountQuantity(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}

	pf.OnFill(instrumentFill(id, enums.SideBuy, "100", "1", "0", "USDT"), enums.PosNet)
	pf.OnFill(instrumentFill(id, enums.SideSell, "110", "1", "0.01", "ETH"), enums.PosNet)

	if got := pf.NetQty(id, enums.PosNet); !got.IsZero() {
		t.Fatalf("netQty=%s, want flat; sell fill quantity already represents removed base", got)
	}
}

func TestSpotQuoteFeeDoesNotChangeNetQty(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "ETH-USDT", Kind: enums.KindSpot}

	pf.OnFill(instrumentFill(id, enums.SideBuy, "100", "1", "0.5", "USDT"), enums.PosNet)

	if got := pf.NetQty(id, enums.PosNet); !got.Equal(d("1")) {
		t.Fatalf("netQty=%s, want 1 when fee is not in base asset", got)
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

func TestContractMultiplierScalesRealizedAndUnrealizedPnL(t *testing.T) {
	id := model.InstrumentID{Venue: "GATE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	provider := &testInstrumentProvider{instruments: map[string]*model.Instrument{
		id.String(): {ID: id, ContractMultiplier: d("0.0001")},
	}}
	pf := New().WithInstrumentProvider(provider)
	pf.OnFill(instrumentFill(id, enums.SideBuy, "100000", "1", "0", "USDT"), enums.PosNet)

	// A lot retains the multiplier that applied when it opened; later registry
	// mutation must not change historical PnL arithmetic.
	provider.instruments[id.String()] = &model.Instrument{ID: id, ContractMultiplier: d("1")}
	if got := pf.UnrealizedPnL(id, enums.PosNet, d("101000")); !got.Equal(d("0.1")) {
		t.Fatalf("unrealized=%s, want 0.1", got)
	}

	pf.OnFill(instrumentFill(id, enums.SideSell, "101000", "1", "0", "USDT"), enums.PosNet)
	if got := pf.RealizedPnL(); !got.Equal(d("0.1")) {
		t.Fatalf("realized=%s, want 0.1", got)
	}

	// Once the old lot is flat, a newly opened lot uses current reference data.
	pf.OnFill(instrumentFill(id, enums.SideBuy, "100000", "1", "0", "USDT"), enums.PosNet)
	if got := pf.UnrealizedPnL(id, enums.PosNet, d("101000")); !got.Equal(d("1000")) {
		t.Fatalf("reopened lot unrealized=%s, want current multiplier result 1000", got)
	}
}

func TestNetExposureUsesContractMultiplier(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	id := model.InstrumentID{Venue: "GATE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	state := model.AccountState{
		AccountID:    "GATE:perp",
		Venue:        "GATE",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Reported:     true,
		EventID:      model.AccountStateEventID("GATE", "GATE:perp", now),
		TsEvent:      now,
		TsInit:       now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	c.UpsertPosition(model.Position{
		AccountID:    "GATE:perp",
		InstrumentID: id,
		Side:         enums.PosNet,
		Quantity:     d("1"),
		MarkPrice:    d("100000"),
	})
	provider := &testInstrumentProvider{instruments: map[string]*model.Instrument{
		id.String(): {ID: id, ContractMultiplier: d("0.0001")},
	}}
	pf := New().WithAccountSource(c).WithInstrumentProvider(provider)

	exposure, ok := pf.NetExposure("GATE:perp")
	if !ok {
		t.Fatal("net exposure account lookup failed")
	}
	if got := exposure[id]; !got.Equal(d("10")) {
		t.Fatalf("net exposure=%s, want 10", got)
	}
}

func TestLotsAreScopedByAccountID(t *testing.T) {
	pf := New()
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	acctABuy := fill(enums.SideBuy, "100", "1", "0")
	acctABuy.AccountID = "acct-a"
	acctBBuy := fill(enums.SideBuy, "200", "2", "0")
	acctBBuy.AccountID = "acct-b"

	pf.OnFill(acctABuy, enums.PosNet)
	pf.OnFill(acctBBuy, enums.PosNet)

	if got := pf.NetQtyForAccount("acct-a", id, enums.PosNet); !got.Equal(d("1")) {
		t.Fatalf("acct-a net qty=%s, want 1", got)
	}
	if got := pf.NetQtyForAccount("acct-b", id, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("acct-b net qty=%s, want 2", got)
	}
	if got := pf.AvgPriceForAccount("acct-b", id, enums.PosNet); !got.Equal(d("200")) {
		t.Fatalf("acct-b avg=%s, want 200", got)
	}
	if got := pf.NetQty(id, enums.PosNet); !got.IsZero() {
		t.Fatalf("legacy net qty should not aggregate ambiguous accounts, got %s", got)
	}
	if got := pf.UnrealizedPnL(id, enums.PosNet, d("210")); !got.IsZero() {
		t.Fatalf("legacy unrealized should not aggregate ambiguous accounts, got %s", got)
	}

	acctASell := fill(enums.SideSell, "110", "1", "0")
	acctASell.AccountID = "acct-a"
	pf.OnFill(acctASell, enums.PosNet)
	if got := pf.RealizedPnLForAccount("acct-a"); !got.Equal(d("10")) {
		t.Fatalf("acct-a realized=%s, want 10", got)
	}
	if got := pf.RealizedPnLForAccount("acct-b"); !got.IsZero() {
		t.Fatalf("acct-b realized=%s, want 0", got)
	}
	if got := pf.UnrealizedPnLForAccount("acct-b", id, enums.PosNet, d("210")); !got.Equal(d("20")) {
		t.Fatalf("acct-b unrealized=%s, want 20", got)
	}
}

func TestAccountViewsAggregateEquityMarginAndExposure(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	state := model.AccountState{
		AccountID:    "T:perp",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1000"), Free: d("900")},
		},
		Margins: []model.MarginBalance{
			{Currency: "USDT", Initial: d("100"), Maintenance: d("50")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:perp", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	c.UpsertPosition(model.Position{
		AccountID:     "T:perp",
		InstrumentID:  id,
		Side:          enums.PosNet,
		Quantity:      d("2"),
		EntryPrice:    d("100"),
		MarkPrice:     d("110"),
		UnrealizedPnL: d("20"),
	})
	pf := New().WithAccountSource(c)

	equity, ok := pf.Equity("T:perp")
	if !ok {
		t.Fatal("equity account lookup failed")
	}
	if got := equity["USDT"]; !got.Equal(d("1020")) {
		t.Fatalf("equity USDT=%s, want 1020", got)
	}
	initial, ok := pf.MarginInitial("T:perp")
	if !ok {
		t.Fatal("initial margin account lookup failed")
	}
	if got := initial["USDT"]; !got.Equal(d("100")) {
		t.Fatalf("initial margin=%s, want 100", got)
	}
	maintenance, ok := pf.MarginMaintenance("T:perp")
	if !ok {
		t.Fatal("maintenance margin account lookup failed")
	}
	if got := maintenance["USDT"]; !got.Equal(d("50")) {
		t.Fatalf("maintenance margin=%s, want 50", got)
	}
	exposure, ok := pf.NetExposure("T:perp")
	if !ok {
		t.Fatal("net exposure account lookup failed")
	}
	if got := exposure[id]; !got.Equal(d("220")) {
		t.Fatalf("net exposure=%s, want 220", got)
	}
}

func TestAccountSummaryViewsUseDirectAccountLookup(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	state := model.AccountState{
		AccountID:    "T:perp",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1000"), Free: d("900")},
		},
		Summary: &model.AccountSummary{
			SettlementCurrency:  "USDT",
			Equity:              d("-10"),
			AvailableCollateral: d("250"),
			UpdatedAt:           now,
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:perp", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(state, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	pf := New().WithAccountSource(c)

	summary, ok := pf.AccountSummary("T:perp")
	if !ok {
		t.Fatal("account summary lookup failed")
	}
	if summary == nil || summary.SettlementCurrency != "USDT" || !summary.Equity.Equal(d("-10")) || !summary.AvailableCollateral.Equal(d("250")) {
		t.Fatalf("summary=%+v, want USDT -10 250", summary)
	}
	summary.AvailableCollateral = d("0")
	again, ok := pf.AccountSummaryForVenue("T")
	if !ok {
		t.Fatal("venue account summary lookup failed")
	}
	if again == nil || !again.AvailableCollateral.Equal(d("250")) {
		t.Fatalf("mutating account summary result aliased source: %+v", again)
	}

	equity, ok := pf.Equity("T:perp")
	if !ok {
		t.Fatal("equity account lookup failed")
	}
	if got := equity["USDT"]; !got.Equal(d("1000")) {
		t.Fatalf("portfolio equity should not fold in account summary equity, got %s", got)
	}
}

func TestAccountSummaryForVenueRequiresUnambiguousAccount(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	for _, accountID := range []string{"T:one", "T:two"} {
		if err := c.ApplyAccountStateAt(model.AccountState{
			AccountID:    accountID,
			Venue:        "T",
			Type:         model.AccountMargin,
			BaseCurrency: "USDT",
			Balances: []model.AccountBalance{
				{Currency: "USDT", Total: d("1000"), Free: d("900")},
			},
			Summary: &model.AccountSummary{
				SettlementCurrency:  "USDT",
				Equity:              d("1000"),
				AvailableCollateral: d("900"),
				UpdatedAt:           now,
			},
			Reported: true,
			EventID:  model.AccountStateEventID("T", accountID, now),
			TsEvent:  now,
			TsInit:   now,
		}, now); err != nil {
			t.Fatalf("apply account state %s: %v", accountID, err)
		}
	}
	pf := New().WithAccountSource(c)
	if summary, ok := pf.AccountSummaryForVenue("T"); ok || summary != nil {
		t.Fatalf("ambiguous venue summary=%+v ok=%v, want nil false", summary, ok)
	}
}

func TestAccountSummaryNilCompatibility(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    "T:nil-summary",
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1"), Free: d("1")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:nil-summary", now),
		TsEvent:  now,
		TsInit:   now,
	}, now); err != nil {
		t.Fatalf("apply account state: %v", err)
	}
	pf := New().WithAccountSource(c)
	if summary, ok := pf.AccountSummary("T:nil-summary"); !ok || summary != nil {
		t.Fatalf("summary=%+v ok=%v, want nil true for an account without optional summary", summary, ok)
	}
}

func TestAccountViewsRequireAccountIDOwnership(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	if err := c.ApplyAccountStateAt(model.AccountState{
		AccountID:    "T:perp",
		Venue:        "T",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("1000"), Free: d("900")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:perp", now),
		TsEvent:  now,
		TsInit:   now,
	}, now); err != nil {
		t.Fatalf("mode-free account state should enter portfolio account source: %v", err)
	}
	pf := New().WithAccountSource(c)
	c.UpsertPosition(model.Position{
		AccountID:    "T:other",
		InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.PosNet,
		Quantity:     d("1"),
		MarkPrice:    d("100"),
	})
	exposure, ok := pf.NetExposure("T:perp")
	if !ok {
		t.Fatal("net exposure account lookup failed")
	}
	if len(exposure) != 0 {
		t.Fatalf("account view should exclude positions owned by another account, got %#v", exposure)
	}
}

func TestAccountViewsUseAccountIDInsteadOfProductMetadata(t *testing.T) {
	c := cache.New()
	now := time.Unix(100, 0)
	perpID := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	spotState := model.AccountState{
		AccountID:    "T:spot",
		Venue:        "T",
		Type:         model.AccountCash,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{
			{Currency: "USDT", Total: d("100"), Free: d("80"), Locked: d("20")},
		},
		Reported: true,
		EventID:  model.AccountStateEventID("T", "T:spot", now),
		TsEvent:  now,
		TsInit:   now,
	}
	if err := c.ApplyAccountStateAt(spotState, now); err != nil {
		t.Fatalf("apply spot account state: %v", err)
	}
	c.UpsertPosition(model.Position{
		AccountID:     "T:other",
		InstrumentID:  perpID,
		Side:          enums.PosNet,
		Quantity:      d("1"),
		MarkPrice:     d("110"),
		UnrealizedPnL: d("10"),
	})
	pf := New().WithAccountSource(c)

	equity, ok := pf.Equity("T:spot")
	if !ok {
		t.Fatal("equity account lookup failed")
	}
	if got := equity["USDT"]; !got.Equal(d("100")) {
		t.Fatalf("spot equity should exclude perp PnL, got %s", got)
	}
	exposure, ok := pf.NetExposure("T:spot")
	if !ok {
		t.Fatal("net exposure account lookup failed")
	}
	if len(exposure) != 0 {
		t.Fatalf("spot exposure should exclude perp positions, got %#v", exposure)
	}
}
