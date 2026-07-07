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
