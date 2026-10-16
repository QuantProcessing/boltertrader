package market

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

type warmSink struct {
	once sync.Once
	done chan struct{}
}

func (sink *warmSink) Send([]byte) bool {
	sink.once.Do(func() { close(sink.done) })
	return true
}

// Warm opens each market's public sockets once by briefly subscribing to its
// most liquid instrument. Venue WebSocket connections stay open after their
// last topic closes, so the first real subscriber skips the TLS handshake,
// which can cost several seconds on some networks.
func Warm(ctx context.Context, catalog *Catalog, public *hub.Hub) {
	var wg sync.WaitGroup
	started := time.Now()
	for _, market := range venue.Markets {
		instrument, ok := warmInstrument(catalog, market)
		if !ok {
			continue
		}
		// One book subscription opens the market's shared public socket.
		// Candle streams are not warmed: some venues back them with a REST
		// snapshot, which would spend request budget for nothing.
		channels := []hub.Key{{Channel: ChannelBook, Instrument: string(instrument)}}
		for _, key := range channels {
			wg.Add(1)
			go func(key hub.Key) {
				defer wg.Done()
				sink := &warmSink{done: make(chan struct{})}
				public.Subscribe(key, sink)
				defer public.Unsubscribe(key, sink)
				select {
				case <-sink.done:
				case <-time.After(20 * time.Second):
				case <-ctx.Done():
				}
			}(key)
		}
	}
	wg.Wait()
	slog.Info("public sockets warmed", "elapsed", time.Since(started).Round(time.Millisecond))
}

func warmInstrument(catalog *Catalog, market venue.Market) (venue.InstrumentID, bool) {
	instruments := catalog.MarketInstruments(market)
	for _, base := range []string{"BTC", "UBTC", "ETH"} {
		for _, instrument := range instruments {
			if strings.EqualFold(instrument.Base, base) && !strings.Contains(instrument.Symbol, ":") {
				return instrument.ID, true
			}
		}
	}
	if len(instruments) > 0 {
		return instruments[0].ID, true
	}
	return "", false
}
