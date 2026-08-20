package httpapi

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestOrderBookSequenceFenceJoinsSnapshotToBufferedDeltas(t *testing.T) {
	fence := newOrderBookSequenceFence("100")

	if emit, gap := fence.Accept(exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "99", Previous: "98"}); emit || gap {
		t.Fatalf("older buffered update = (%t, %t), want discarded", emit, gap)
	}
	if emit, gap := fence.Accept(exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "102", Previous: "99"}); !emit || gap {
		t.Fatalf("snapshot-bridging update = (%t, %t), want emitted", emit, gap)
	}
	if emit, gap := fence.Accept(exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "103", Previous: "102"}); !emit || gap {
		t.Fatalf("contiguous update = (%t, %t), want emitted", emit, gap)
	}
	if emit, gap := fence.Accept(exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "105", Previous: "104"}); emit || !gap {
		t.Fatalf("gapped update = (%t, %t), want resync", emit, gap)
	}
}

func TestOrderBookSequenceFenceAllowsOpaqueVenueSequences(t *testing.T) {
	fence := newOrderBookSequenceFence("opaque-snapshot")
	if emit, gap := fence.Accept(exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "next", Previous: "opaque-snapshot"}); !emit || gap {
		t.Fatalf("opaque update = (%t, %t), want adapter-owned continuity", emit, gap)
	}
}
