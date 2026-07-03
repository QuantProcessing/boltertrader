package latency

import (
	"testing"
	"time"
)

func BenchmarkRecorderRecordEvent(b *testing.B) {
	rec := NewRecorder(b.N + 1)
	lat := EventLatency{Chain: ChainMarket, TsBusRecv: time.Now(), TsApplied: time.Now(), TsCallbackDone: time.Now()}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rec.RecordEventLatency(lat)
	}
}
