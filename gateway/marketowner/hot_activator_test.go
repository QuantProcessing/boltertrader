package marketowner

import (
	"slices"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type hotMetricRecorder struct {
	mu     sync.Mutex
	counts []int
}

func (recorder *hotMetricRecorder) SetMarketHot(_, _ string, count int) {
	recorder.mu.Lock()
	recorder.counts = append(recorder.counts, count)
	recorder.mu.Unlock()
}

func TestHotActivatorCountsDistinctInstrumentsAndReleasesDemand(t *testing.T) {
	metrics := &hotMetricRecorder{}
	activator := NewHotActivator(BoardMarks{}, nil, metrics)
	defer activator.Close()
	key := hotset.Key{
		Owner: markethub.OwnerKey{
			Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, NativeFamily: "perpetual",
		},
		InstrumentID: "binance:live:perp:USDT:BTC-USDT",
	}
	activator.SetActive(key, true)
	activator.SetActive(key, true) // duplicate demand must not double-count
	second := key
	second.InstrumentID = "binance:live:perp:USDT:ETH-USDT"
	activator.SetActive(second, true)
	activator.SetActive(key, false)
	activator.SetActive(key, false)
	activator.Close()

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if !slices.Equal(metrics.counts, []int{1, 2, 1, 0}) {
		t.Fatalf("hot instrument counts = %v", metrics.counts)
	}
}
