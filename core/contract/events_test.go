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
		AccountID: model.AccountIDBinanceSpot,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("100"),
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
	}})
	if env.Venue != "BINANCE" || env.AccountID != model.AccountIDBinanceSpot {
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
