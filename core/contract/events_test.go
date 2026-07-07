package contract

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestEventEnvelopeRequiresEventID(t *testing.T) {
	env := EventEnvelope[ExecEvent]{Payload: RejectEvent{ClientID: "c1"}}
	if err := env.Validate(); err == nil {
		t.Fatal("missing event id should fail validation")
	}
}

func TestEventEnvelopeLatencyTimestampsAreMonotonicWhenPresent(t *testing.T) {
	t0 := time.Unix(1, 0)
	env := EventEnvelope[ExecEvent]{
		EventMeta: EventMeta{
			EventID:       model.EventID("e1"),
			TsVenue:       t0.Add(2 * time.Second),
			TsAdapterRecv: t0.Add(time.Second),
		},
		Payload: RejectEvent{ClientID: "c1"},
	}
	if err := env.Validate(); err == nil {
		t.Fatal("non-monotonic timestamps should fail validation")
	}
	env.TsAdapterRecv = t0.Add(3 * time.Second)
	if err := env.Validate(); err != nil {
		t.Fatalf("monotonic timestamps should validate: %v", err)
	}
}

func TestEnvelopeFlagsRoundTrip(t *testing.T) {
	env := NewExecEnvelope(RejectEvent{ClientID: "c1", Reason: "rejected"})
	env.Flags |= EventFlagSynthetic | EventFlagAmbiguous
	if !env.Flags.Has(EventFlagFromStream) || !env.Flags.Has(EventFlagSynthetic) || !env.Flags.Has(EventFlagAmbiguous) {
		t.Fatalf("flags did not round-trip: %b", env.Flags)
	}
	if env.EventID == "" || env.ClientID != "c1" {
		t.Fatalf("inferred meta not populated: %+v", env.EventMeta)
	}
}

func TestExecEnvelopeWithMetaOverridesSourceAndFlags(t *testing.T) {
	env := NewExecEnvelopeWithMeta(RejectEvent{ClientID: "c1", Reason: "rejected"}, EventMeta{
		Source: SourceTest,
		Flags:  EventFlagSynthetic,
	})
	if env.Source != SourceTest {
		t.Fatalf("source=%s, want test", env.Source)
	}
	if env.Flags.Has(EventFlagFromStream) || !env.Flags.Has(EventFlagSynthetic) {
		t.Fatalf("flags=%b, want synthetic without stream", env.Flags)
	}
	if env.EventID == "" || env.ClientID != "c1" {
		t.Fatalf("inferred meta not retained: %+v", env.EventMeta)
	}
}

func TestAccountStateEnvelopeInfersMeta(t *testing.T) {
	ts := time.Unix(10, 0)
	env := NewAccountEnvelope(AccountStateEvent{State: model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("100"),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:        "BINANCE",
			AccountID:    model.AccountIDBinanceDefault,
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   ts,
			Source:       "test",
		},
		Reported: true,
		TsEvent:  ts,
	}})
	if env.Venue != "BINANCE" || env.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("account state meta not inferred: %+v", env.EventMeta)
	}
	if !env.TsVenue.Equal(ts) {
		t.Fatalf("TsVenue=%s, want %s", env.TsVenue, ts)
	}
	if env.EventID == "" {
		t.Fatal("account state event id should be inferred")
	}
	if !env.Flags.Has(EventFlagFromStream) {
		t.Fatalf("account state envelope should retain stream flag: %b", env.Flags)
	}
}

func TestExecAndAccountEnvelopesInferAccountID(t *testing.T) {
	fill := model.Fill{
		AccountID:    "T:acct",
		InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		ClientID:     "c1",
		VenueOrderID: "v1",
		TradeID:      "t1",
		Timestamp:    time.Unix(11, 0),
	}
	fillEnv := NewExecEnvelope(FillEvent{Fill: fill})
	if fillEnv.AccountID != "T:acct" {
		t.Fatalf("fill envelope account id=%q, want T:acct", fillEnv.AccountID)
	}

	order := model.Order{Request: model.OrderRequest{AccountID: "T:acct", InstrumentID: fill.InstrumentID, ClientID: "c1"}}
	orderEnv := NewExecEnvelope(OrderEvent{Order: order})
	if orderEnv.AccountID != "T:acct" {
		t.Fatalf("order envelope account id=%q, want T:acct", orderEnv.AccountID)
	}

	pos := model.Position{AccountID: "T:acct", InstrumentID: fill.InstrumentID, Side: enums.PosNet, UpdatedAt: time.Unix(12, 0)}
	posEnv := NewAccountEnvelope(PositionEvent{Position: pos})
	if posEnv.AccountID != "T:acct" {
		t.Fatalf("position envelope account id=%q, want T:acct", posEnv.AccountID)
	}

	bal := model.AccountBalance{AccountID: "T:acct", Currency: "USDT", UpdatedAt: time.Unix(13, 0)}
	balEnv := NewAccountEnvelope(BalanceEvent{Balance: bal})
	if balEnv.AccountID != "T:acct" {
		t.Fatalf("balance envelope account id=%q, want T:acct", balEnv.AccountID)
	}
}
