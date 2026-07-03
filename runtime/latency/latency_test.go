package latency

import (
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func TestRecorderRecordsEventChain(t *testing.T) {
	rec := NewRecorder(4)
	t0 := time.Unix(1, 0)
	env := contract.NewMarketEnvelope(contract.TradeEvent{Trade: model.TradeTick{
		InstrumentID: model.InstrumentID{Venue: "T", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Timestamp:    t0,
	}})
	env.TsAdapterRecv = t0.Add(time.Microsecond)
	env.TsAdapterEmit = t0.Add(2 * time.Microsecond)
	env.TsBusRecv = t0.Add(3 * time.Microsecond)
	lat := EventFromMeta(ChainMarket, env.Meta(), t0.Add(4*time.Microsecond), t0.Add(5*time.Microsecond))
	rec.RecordEventLatency(lat)

	snap := rec.Snapshot()
	if snap.EventsTotal != 1 || len(snap.RecentEvents) != 1 {
		t.Fatalf("snapshot=%+v, want one event", snap)
	}
	if !snap.RecentEvents[0].NonNegative() {
		t.Fatalf("event latency should be non-negative: %+v", snap.RecentEvents[0])
	}
}

func TestRecorderRecordsCommandChain(t *testing.T) {
	rec := NewRecorder(4)
	t0 := time.Unix(1, 0)
	cmd := CommandLatency{Command: "submit", ClientID: "c1", StartedAt: t0, AdapterStart: t0.Add(time.Microsecond), AdapterEnd: t0.Add(2 * time.Microsecond), CacheApplied: t0.Add(3 * time.Microsecond)}
	cmd.Finish(t0.Add(4 * time.Microsecond))
	rec.RecordCommandLatency(cmd)
	snap := rec.Snapshot()
	if snap.CommandsTotal != 1 || len(snap.RecentCommands) != 1 {
		t.Fatalf("snapshot=%+v, want one command", snap)
	}
	if !snap.RecentCommands[0].NonNegative() {
		t.Fatalf("command latency should be non-negative: %+v", snap.RecentCommands[0])
	}
}

func TestRecorderSnapshotIsRaceFree(t *testing.T) {
	rec := NewRecorder(1024)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				rec.RecordCommandLatency(CommandLatency{Command: "submit", StartedAt: time.Now(), CompletedAt: time.Now()})
				_ = rec.Snapshot()
			}
		}()
	}
	wg.Wait()
}

func TestRecorderDropsWhenBoundedQueueFull(t *testing.T) {
	rec := NewRecorder(1)
	rec.RecordEventLatency(EventLatency{Chain: ChainMarket})
	rec.RecordEventLatency(EventLatency{Chain: ChainExecution})
	if rec.Drops() == 0 {
		t.Fatal("expected drop after bounded recorder filled")
	}
}

func TestHistogramBucketsNanoseconds(t *testing.T) {
	rec := NewRecorder(4)
	rec.RecordCommandLatency(CommandLatency{Command: "submit", StartedAt: time.Unix(1, 0), CompletedAt: time.Unix(1, int64(time.Millisecond))})
	snap := rec.Snapshot()
	var total uint64
	for _, bucket := range snap.CommandBuckets {
		total += bucket.Count
	}
	if total != 1 {
		t.Fatalf("bucket total=%d, want 1", total)
	}
}

func TestLatencySnapshotDoesNotExposeMutableState(t *testing.T) {
	rec := NewRecorder(4)
	rec.RecordEventLatency(EventLatency{Chain: ChainMarket, EventID: "e1"})
	snap := rec.Snapshot()
	snap.RecentEvents[0].EventID = "mutated"
	again := rec.Snapshot()
	if again.RecentEvents[0].EventID != "e1" {
		t.Fatal("snapshot mutation affected recorder state")
	}
}
