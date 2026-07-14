package exec_test

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
)

func TestFillBufferBoundsAppliedFillIdempotencyWindow(t *testing.T) {
	buf := exec.NewFillBufferWithAppliedLimit(2)
	fill := func(tradeID string) model.Fill {
		return model.Fill{
			AccountID:    "acct",
			InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			TradeID:      tradeID,
		}
	}

	if !buf.MarkApplied(fill("oldest")) || !buf.MarkApplied(fill("recent-1")) || !buf.MarkApplied(fill("recent-2")) {
		t.Fatal("first observation of each fill should apply")
	}
	if buf.MarkApplied(fill("recent-2")) {
		t.Fatal("most recent fill should remain inside the idempotency window")
	}
	if !buf.MarkApplied(fill("oldest")) {
		t.Fatal("oldest fill should be reusable after it leaves the bounded window")
	}
}

func TestFillBufferNeverEvictsPendingUnmatchedFills(t *testing.T) {
	buf := exec.NewFillBufferWithAppliedLimit(1)
	inst := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	for _, id := range []string{"pending-1", "pending-2", "pending-3"} {
		buf.Buffer(model.Fill{InstrumentID: inst, ClientID: id, TradeID: id})
		buf.MarkApplied(model.Fill{AccountID: "acct", InstrumentID: inst, TradeID: "applied-" + id})
	}
	if got := buf.Count(); got != 3 {
		t.Fatalf("pending fills=%d, want all 3 retained", got)
	}
}
