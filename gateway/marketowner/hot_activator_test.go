package marketowner

import (
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type hotMetricRecorder struct {
	mu       sync.Mutex
	datasets []string
}

func (recorder *hotMetricRecorder) SetMarketHot(_, _, dataset string, _ int) {
	recorder.mu.Lock()
	recorder.datasets = append(recorder.datasets, dataset)
	recorder.mu.Unlock()
}

func TestHotActivatorDoesNotTurnGenericHotDemandIntoBBO(t *testing.T) {
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

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	for _, dataset := range metrics.datasets {
		if dataset == string(exchange.DatasetBBO) || dataset == string(exchange.DatasetDepth) {
			t.Fatalf("generic hot demand activated order-book dataset %q", dataset)
		}
	}
}
