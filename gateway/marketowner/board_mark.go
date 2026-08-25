package marketowner

import (
	"context"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

// BoardMarks projects marks from the resident broad derivative feed. Venues
// whose native feed already carries every market must not open a second
// per-symbol mark subscription when Trader, PnL, funding and arbitrage ask for
// the same value.
type BoardMarks struct {
	Facts        *markethub.FactStore
	Markets      *publicmarket.Source
	Capabilities *marketcapability.Registry
}

func (source BoardMarks) PublishMark(ctx context.Context, instrumentID string, price exchange.OptionalDecimal, observedAt time.Time) {
	if source.Facts == nil || source.Markets == nil || !price.Valid {
		return
	}
	fact, ok := source.Facts.Get(instrumentID)
	if !ok {
		return
	}
	fact.MarkPrice, fact.MarkObservedAt = price, observedAt.UTC()
	owner := markethub.OwnerKey{Venue: fact.Instrument.Venue, Environment: fact.Environment, NativeFamily: marketcapability.NativeFamily(fact.Instrument.Venue, exchange.ProductPerp, fact.Instrument.SettleAsset)}
	generation := source.Facts.Generation(owner)
	if generation == 0 {
		return
	}
	_ = source.Facts.Apply(exchange.DerivativePatch{
		Kind:         exchange.EventDelta,
		Scope:        exchange.CatalogScope{Venue: fact.Instrument.Venue, Environment: fact.Environment, Product: exchange.ProductPerp, Settlement: fact.Instrument.SettleAsset},
		NativeFamily: owner.NativeFamily,
		Generation:   generation,
		ObservedAt:   observedAt.UTC(), Fields: exchange.DerivativeFieldMark, Facts: []exchange.DerivativeFact{fact},
	})
}

func (source BoardMarks) Open(ctx context.Context, key markethub.SubscriptionKey) (markethub.NativeStream, bool, error) {
	if source.Facts == nil || source.Markets == nil {
		return markethub.NativeStream{}, false, nil
	}
	instrument, ok, err := source.instrument(ctx, key.InstrumentID)
	if err != nil || !ok || source.Capabilities == nil {
		return markethub.NativeStream{}, false, err
	}
	capability, capabilityErr := source.Capabilities.Resolve(key.Owner.Venue, key.Owner.NativeFamily, exchange.DatasetMark, false)
	if capabilityErr != nil || capability.Granularity == exchange.GranularitySingleInstrument {
		return markethub.NativeStream{}, false, capabilityErr
	}
	changes, cancel, err := source.Facts.Subscribe(ctx, 64)
	if err != nil {
		return markethub.NativeStream{}, true, err
	}
	price, at, err := source.snapshot(instrument)
	if err != nil {
		cancel()
		return markethub.NativeStream{}, true, err
	}
	events := make(chan markethub.Message, 64)
	failures := make(chan error, 1)
	streamCtx, stop := context.WithCancel(ctx)
	go source.forward(streamCtx, instrument, price, at, changes, events, failures)
	return markethub.NativeStream{
		Snapshot: message(key, markethub.MessageSnapshot, at, "", map[string]any{"price": price}),
		Events:   events, Errors: failures,
		Close: func() error {
			stop()
			cancel()
			return nil
		},
	}, true, nil
}

func (source BoardMarks) instrument(ctx context.Context, instrumentID string) (publicmarket.Instrument, bool, error) {
	snapshot, err := source.Markets.Snapshot(ctx)
	if err != nil {
		return publicmarket.Instrument{}, false, err
	}
	instrument, ok := snapshot.Find(instrumentID)
	return instrument, ok, nil
}

func (source BoardMarks) snapshot(instrument publicmarket.Instrument) (string, time.Time, error) {
	fact, ok := source.Facts.Get(string(instrument.Catalog.ID))
	if ok && fact.MarkPrice.Valid && !fact.MarkObservedAt.IsZero() {
		return fact.MarkPrice.Value.String(), fact.MarkObservedAt.UTC(), nil
	}
	return "", time.Time{}, fmt.Errorf("%w: broad mark is absent", markethub.ErrUnavailable)
}

func (source BoardMarks) forward(ctx context.Context, instrument publicmarket.Instrument, lastPrice string, lastAt time.Time, changes <-chan markethub.FactChange, events chan<- markethub.Message, failures chan<- error) {
	defer close(events)
	defer close(failures)
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-changes:
			if !open {
				select {
				case failures <- markethub.ErrResyncRequired:
				case <-ctx.Done():
				}
				return
			}
			price, at, err := source.snapshot(instrument)
			if err != nil || (price == lastPrice && at.Equal(lastAt)) {
				continue
			}
			lastPrice, lastAt = price, at
			if !emit(ctx, events, markethub.Message{Kind: markethub.MessageDelta, ObservedAt: at, Data: markethub.Encode(map[string]any{"price": price})}) {
				return
			}
		}
	}
}
