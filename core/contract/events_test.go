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
		AccountID: "T:acct",
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("100"),
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("BINANCE", "T:acct", ts),
		TsEvent:  ts,
		TsInit:   ts,
	}})
	if env.Venue != "BINANCE" || env.AccountID != "T:acct" {
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

func TestReferenceDataEnvelopeInfersMarketMeta(t *testing.T) {
	ts := time.Unix(20, 0)
	id := model.InstrumentID{Venue: "BINANCE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	env := NewMarketEnvelopeWithMeta(ReferenceDataEvent{Snapshot: model.DerivativeReferenceSnapshot{
		InstrumentID: id,
		MarkPrice:    decimal.RequireFromString("64100.5"),
		Timestamp:    ts,
		Fields:       model.ReferenceHasMarkPrice,
	}}, EventMeta{
		Source: SourceAdapterREST,
		Flags:  EventFlagFromSnapshot,
	})
	if env.InstrumentID != id || env.Venue != "BINANCE" {
		t.Fatalf("reference meta not inferred: %+v", env.EventMeta)
	}
	if !env.TsVenue.Equal(ts) {
		t.Fatalf("TsVenue=%s, want %s", env.TsVenue, ts)
	}
	if env.EventID == "" {
		t.Fatal("reference event id should be inferred")
	}
	if env.Source != SourceAdapterREST || !env.Flags.Has(EventFlagFromSnapshot) {
		t.Fatalf("source/flags not retained: %+v", env.EventMeta)
	}
}

func TestInferredEventIDsDistinguishLifecycleProgressAndPreserveReplay(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	ts := time.Unix(30, 0)
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    "T:acct",
			InstrumentID: id,
			ClientID:     "client",
			Quantity:     decimal.NewFromInt(2),
		},
		VenueOrderID: "venue",
		Status:       enums.StatusPartiallyFilled,
		FilledQty:    decimal.NewFromInt(1),
		UpdatedAt:    ts,
	}
	first := NewExecEnvelope(OrderEvent{Order: order})
	replay := NewExecEnvelope(OrderEvent{Order: order})
	if first.EventID == "" || replay.EventID != first.EventID {
		t.Fatalf("exact order replay ids first=%q replay=%q", first.EventID, replay.EventID)
	}
	order.FilledQty = decimal.RequireFromString("1.5")
	order.UpdatedAt = ts.Add(time.Nanosecond)
	progress := NewExecEnvelope(OrderEvent{Order: order})
	if progress.EventID == first.EventID {
		t.Fatalf("order lifecycle progress reused event id %q", first.EventID)
	}

	trade := model.TradeTick{InstrumentID: id, TradeID: "trade-1", Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: ts}
	tradeOne := NewMarketEnvelope(TradeEvent{Trade: trade})
	trade.TradeID = "trade-2"
	tradeTwo := NewMarketEnvelope(TradeEvent{Trade: trade})
	if tradeOne.EventID == tradeTwo.EventID {
		t.Fatalf("distinct trades reused event id %q", tradeOne.EventID)
	}
}

func TestInferredAccountEventIDsIncludeAccountScope(t *testing.T) {
	id := model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	ts := time.Unix(40, 0)
	leftBalance := NewAccountEnvelope(BalanceEvent{Balance: model.AccountBalance{AccountID: "T:left", Currency: "USDT", Total: decimal.NewFromInt(1), UpdatedAt: ts}})
	rightBalance := NewAccountEnvelope(BalanceEvent{Balance: model.AccountBalance{AccountID: "T:right", Currency: "USDT", Total: decimal.NewFromInt(1), UpdatedAt: ts}})
	if leftBalance.EventID == rightBalance.EventID {
		t.Fatalf("balances from different accounts reused event id %q", leftBalance.EventID)
	}

	leftPosition := NewAccountEnvelope(PositionEvent{Position: model.Position{AccountID: "T:left", InstrumentID: id, Side: enums.PosNet, Quantity: decimal.NewFromInt(1), UpdatedAt: ts}})
	rightPosition := NewAccountEnvelope(PositionEvent{Position: model.Position{AccountID: "T:right", InstrumentID: id, Side: enums.PosNet, Quantity: decimal.NewFromInt(1), UpdatedAt: ts}})
	if leftPosition.EventID == rightPosition.EventID {
		t.Fatalf("positions from different accounts reused event id %q", leftPosition.EventID)
	}
}

func TestStreamGapEnvelopeInfersStableMetadata(t *testing.T) {
	event := StreamGapEvent{
		Venue:      "GATE",
		AccountID:  "GATE:unified",
		StreamID:   "gate:private:spot",
		Generation: 7,
		Phase:      StreamGapStarted,
		Reason:     "unexpected websocket close",
	}
	first := NewExecEnvelope(event)
	second := NewExecEnvelope(event)

	if first.EventID == "" || first.EventID != second.EventID {
		t.Fatalf("gap event ids first=%q second=%q, want stable non-empty id", first.EventID, second.EventID)
	}
	if first.Venue != event.Venue || first.AccountID != event.AccountID {
		t.Fatalf("gap metadata=%+v, want venue/account from event", first.Meta())
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("valid gap: %v", err)
	}
}

func TestStreamGapEventValidation(t *testing.T) {
	tests := []StreamGapEvent{
		{Generation: 1, Phase: StreamGapStarted},
		{StreamID: "private", Phase: StreamGapStarted},
		{StreamID: "private", Generation: 1, Phase: "unknown"},
	}
	for _, event := range tests {
		if err := event.Validate(); err == nil {
			t.Fatalf("invalid gap event accepted: %+v", event)
		}
	}
}
