package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestOrderBookContractEnforcesUnifiedSnapshotDeltaSemantics(t *testing.T) {
	instrument := orderBookContractInstrument(exchange.VenueBybit)
	contract := newOrderBookContract(clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}, instrument)

	snapshot := exchange.BookEvent{
		Kind: exchange.EventSnapshot, Instrument: instrument, Sequence: "100",
		Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(99), Quantity: decimal.NewFromInt(2)}},
		Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
	}
	if emit, err := contract.accept(snapshot); err != nil || !emit {
		t.Fatalf("snapshot emit=%t err=%v", emit, err)
	}

	delta := exchange.BookEvent{
		Kind: exchange.EventDelta, Instrument: instrument, Sequence: "101", Previous: "100",
		Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(99), Quantity: decimal.Zero}},
	}
	if emit, err := contract.accept(delta); err != nil || !emit {
		t.Fatalf("delta emit=%t err=%v", emit, err)
	}
	if emit, err := contract.accept(delta); err != nil || emit {
		t.Fatalf("duplicate delta emit=%t err=%v", emit, err)
	}

	gapped := delta
	gapped.Sequence = "103"
	gapped.Previous = "102"
	if emit, err := contract.accept(gapped); err == nil || emit {
		t.Fatalf("gapped delta emit=%t err=%v", emit, err)
	}
}

func TestOrderBookContractRejectsMalformedBackendEvents(t *testing.T) {
	instrument := orderBookContractInstrument(exchange.VenueBitget)
	meta := clientMeta{venue: exchange.VenueBitget, product: exchange.ProductPerp}
	tests := []struct {
		name  string
		event exchange.BookEvent
	}{
		{name: "missing kind", event: exchange.BookEvent{Instrument: instrument}},
		{name: "wrong instrument", event: exchange.BookEvent{Kind: exchange.EventSnapshot, Instrument: orderBookContractInstrument(exchange.VenueBybit)}},
		{name: "non-positive price", event: exchange.BookEvent{Kind: exchange.EventDelta, Instrument: instrument, Bids: []exchange.BookLevel{{Price: decimal.Zero, Quantity: decimal.NewFromInt(1)}}}},
		{name: "negative quantity", event: exchange.BookEvent{Kind: exchange.EventDelta, Instrument: instrument, Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(-1)}}}},
		{name: "zero snapshot quantity", event: exchange.BookEvent{Kind: exchange.EventSnapshot, Instrument: instrument, Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(1), Quantity: decimal.Zero}}}},
		{name: "predecessor without sequence", event: exchange.BookEvent{Kind: exchange.EventDelta, Instrument: instrument, Previous: "1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if emit, err := newOrderBookContract(meta, instrument).accept(test.event); err == nil || emit {
				t.Fatalf("accept = (%t, %v), want contract violation", emit, err)
			}
		})
	}
}

func TestVenueOrderBookClassifiersPreserveWireSemantics(t *testing.T) {
	if kind, err := bybitOrderBookEventKind("snapshot"); err != nil || kind != exchange.EventSnapshot {
		t.Fatalf("Bybit snapshot kind=%q err=%v", kind, err)
	}
	if kind, err := bybitOrderBookEventKind("delta"); err != nil || kind != exchange.EventDelta {
		t.Fatalf("Bybit delta kind=%q err=%v", kind, err)
	}
	if _, err := bybitOrderBookEventKind("unknown"); err == nil {
		t.Fatal("Bybit accepted an unknown event type")
	}

	if kind, err := bitgetOrderBookEventKind("snapshot"); err != nil || kind != exchange.EventSnapshot {
		t.Fatalf("Bitget books5 kind=%q err=%v", kind, err)
	}
	if _, err := bitgetOrderBookEventKind("update"); err == nil {
		t.Fatal("Bitget books5 accepted an incremental event")
	}

	if got := asterDepthPrevious(101, 0); got != "100" {
		t.Fatalf("Aster first-update predecessor=%q, want 100", got)
	}
	if got := asterDepthPrevious(101, 99); got != "99" {
		t.Fatalf("Aster explicit predecessor=%q, want 99", got)
	}

	nadoInstrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueNado, Product: exchange.ProductSpot, Symbol: "BTC-USDT",
		NativeSymbol: "BTC-USDT", NativeID: "7", BaseAsset: "BTC", QuoteAsset: "USDT",
	}}
	nadoEvent, err := nadoBookEvent(&nadosdk.OrderBook{
		ProductId: 7, MaxTimestamp: "1720000000", LastMaxTimestamp: "1719999999",
		Bids: [][2]string{{"100000000000000000000", "1000000000000000000"}},
	}, nadoProduct{productID: 7, instrument: nadoInstrument}, exchange.ProductSpot, "WatchOrderBook")
	if err != nil || nadoEvent.Kind != exchange.EventDelta || nadoEvent.Previous != "1719999999" {
		t.Fatalf("Nado book event=%+v err=%v", nadoEvent, err)
	}
}

func orderBookContractInstrument(venue exchange.Venue) exchange.Instrument {
	return exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: venue, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}}
}
