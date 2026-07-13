package accounting

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func cashState(ts time.Time) model.AccountState {
	return model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    dec("100"),
			Free:     dec("80"),
			Locked:   dec("20"),
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
		TsEvent:  ts,
		TsInit:   ts,
	}
}

func TestCashAccountApplyAndLookup(t *testing.T) {
	ts := time.Unix(1, 0)
	acct, err := New(cashState(ts), time.Minute, ts)
	if err != nil {
		t.Fatalf("new account: %v", err)
	}
	if acct.ID() != model.AccountIDBinanceDefault || acct.Type() != model.AccountCash {
		t.Fatalf("account identity/type mismatch: %s %s", acct.ID(), acct.Type())
	}
	free, ok := acct.BalanceFree("USDT")
	if !ok || !free.Equal(dec("80")) {
		t.Fatalf("free=%s ok=%v, want 80 true", free, ok)
	}
	if !acct.IsFresh(ts.Add(time.Second)) {
		t.Fatal("account should be fresh within stale-after")
	}

	next := cashState(ts.Add(time.Second))
	next.Balances[0].Free = dec("90")
	next.Balances[0].Locked = dec("10")
	if err := acct.Apply(next, ts.Add(time.Second)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	free, _ = acct.BalanceFree("USDT")
	if !free.Equal(dec("90")) {
		t.Fatalf("updated free=%s, want 90", free)
	}
}

func TestAccountSummaryReturnsCopyAndUpdatesOnApply(t *testing.T) {
	ts := time.Unix(1, 0)
	state := cashState(ts)
	state.Summary = &model.AccountSummary{
		SettlementCurrency:  "USDT",
		Equity:              dec("-5"),
		AvailableCollateral: dec("80"),
		UpdatedAt:           ts,
	}
	acct, err := New(state, time.Minute, ts)
	if err != nil {
		t.Fatalf("new account with summary: %v", err)
	}
	summary := acct.Summary()
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.SettlementCurrency != "USDT" || !summary.Equity.Equal(dec("-5")) || !summary.AvailableCollateral.Equal(dec("80")) {
		t.Fatalf("summary=%+v, want USDT -5 80", summary)
	}
	summary.AvailableCollateral = dec("0")
	again := acct.Summary()
	if !again.AvailableCollateral.Equal(dec("80")) {
		t.Fatalf("mutating returned summary changed account state: %s", again.AvailableCollateral)
	}

	next := cashState(ts.Add(time.Second))
	next.Summary = &model.AccountSummary{
		SettlementCurrency:  "USDT",
		Equity:              dec("25"),
		AvailableCollateral: dec("20"),
		UpdatedAt:           ts.Add(time.Second),
	}
	if err := acct.Apply(next, ts.Add(time.Second)); err != nil {
		t.Fatalf("apply summary update: %v", err)
	}
	updated := acct.Summary()
	if updated == nil || !updated.Equity.Equal(dec("25")) || !updated.AvailableCollateral.Equal(dec("20")) {
		t.Fatalf("updated summary=%+v, want equity 25 collateral 20", updated)
	}
}

func TestAccountSummaryNilCompatibilityAndValidation(t *testing.T) {
	ts := time.Unix(1, 0)
	acct, err := New(cashState(ts), time.Minute, ts)
	if err != nil {
		t.Fatalf("new account without summary: %v", err)
	}
	if got := acct.Summary(); got != nil {
		t.Fatalf("summary=%+v, want nil", got)
	}

	state := cashState(ts)
	state.Summary = &model.AccountSummary{
		SettlementCurrency:  "USDT",
		Equity:              dec("1"),
		AvailableCollateral: dec("-0.01"),
		UpdatedAt:           ts,
	}
	if _, err := New(state, time.Minute, ts); err == nil {
		t.Fatal("negative available collateral should reject account creation")
	}
}

func TestAccountApplyRejectsIdentityChange(t *testing.T) {
	ts := time.Unix(1, 0)
	acct, err := New(cashState(ts), time.Minute, ts)
	if err != nil {
		t.Fatalf("new account: %v", err)
	}
	next := cashState(ts.Add(time.Second))
	next.AccountID = "other"
	if err := acct.Apply(next, ts.Add(time.Second)); err == nil {
		t.Fatal("account id change should fail")
	}
}

func TestAccountApplyIgnoresOlderVenueState(t *testing.T) {
	base := time.Unix(10, 0)
	newer := cashState(base.Add(2 * time.Second))
	newer.Balances[0].Free = dec("95")
	newer.Balances[0].Locked = dec("5")
	appliedAt := base.Add(10 * time.Second)
	acct, err := New(newer, time.Minute, appliedAt)
	if err != nil {
		t.Fatalf("new account: %v", err)
	}

	older := cashState(base.Add(time.Second))
	older.Balances[0].Free = dec("1")
	older.Balances[0].Locked = dec("99")
	if err := acct.Apply(older, appliedAt.Add(time.Second)); err != nil {
		t.Fatalf("older account state should be ignored without failing recovery: %v", err)
	}
	free, _ := acct.BalanceFree("USDT")
	if !free.Equal(dec("95")) || acct.LastEvent().EventID != newer.EventID {
		t.Fatalf("older state replaced newer state: free=%s event=%s", free, acct.LastEvent().EventID)
	}
	if got := acct.Freshness().LastAccountStateAt; !got.Equal(appliedAt) {
		t.Fatalf("older state refreshed account timestamp to %s, want %s", got, appliedAt)
	}
}

func TestMarginAccountStoresMargins(t *testing.T) {
	ts := time.Unix(2, 0)
	inst := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	state := model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountMargin,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    dec("1000"),
			Free:     dec("900"),
		}},
		Margins: []model.MarginBalance{{
			Currency:     "USDT",
			InstrumentID: &inst,
			Initial:      dec("50"),
			Maintenance:  dec("20"),
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", model.AccountIDBinanceDefault, ts),
		TsEvent:  ts,
		TsInit:   ts,
	}
	acct, err := New(state, time.Minute, ts)
	if err != nil {
		t.Fatalf("new margin account: %v", err)
	}
	inst.Symbol = "MUTATED-USDT"
	if _, ok := acct.MarginInitial("USDT", &inst); ok {
		t.Fatal("mutating original margin instrument pointer should not change account margin key")
	}
	lookup := model.InstrumentID{Venue: "BINANCE", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	initial, ok := acct.MarginInitial("USDT", &lookup)
	if !ok || !initial.Equal(dec("50")) {
		t.Fatalf("initial=%s ok=%v, want 50 true", initial, ok)
	}
	margins := acct.Margins()
	if len(margins) != 1 || margins[0].InstrumentID == nil {
		t.Fatalf("margins=%+v, want one instrument margin", margins)
	}
	margins[0].InstrumentID.Symbol = "ALSO-MUTATED"
	initial, ok = acct.MarginInitial("USDT", &lookup)
	if !ok || !initial.Equal(dec("50")) {
		t.Fatalf("returned margin pointer mutated account state: initial=%s ok=%v", initial, ok)
	}
}
