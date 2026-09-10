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
// per-symbol mark subscription when Trader, PnL and funding ask for
// the same value.
type BoardMarks struct {
	Facts        *markethub.FactStore
	Markets      *publicmarket.Source
	Capabilities *marketcapability.Registry
}

func (source BoardMarks) PublishMark(ctx context.Context, instrumentID string, price exchange.OptionalDecimal, observedAt time.Time) {
	source.PublishReference(ctx, instrumentID, exchange.DerivativeReferenceEvent{
		MarkPrice: price, MarkObservedAt: observedAt,
	})
}

// PublishReference merges every field carried by one hot native ticker into
// the same FactStore used by the broad sweep. ApplyDerivativePatch owns the
// per-field clocks, so a late hot message cannot replace newer broad data (or
// vice versa).
func (source BoardMarks) PublishReference(ctx context.Context, instrumentID string, event exchange.DerivativeReferenceEvent) {
	if source.Facts == nil || source.Markets == nil {
		return
	}
	fact, ok := source.Facts.Get(instrumentID)
	if !ok {
		return
	}
	fields := exchange.DerivativeField(0)
	if event.MarkPrice.Valid && !event.MarkObservedAt.IsZero() {
		fact.MarkPrice, fact.MarkObservedAt = event.MarkPrice, event.MarkObservedAt.UTC()
		fields |= exchange.DerivativeFieldMark
	}
	if event.IndexPrice.Valid && !event.IndexObservedAt.IsZero() {
		fact.IndexPrice, fact.IndexObservedAt = event.IndexPrice, event.IndexObservedAt.UTC()
		fields |= exchange.DerivativeFieldIndex
	}
	if (event.FundingRate.Valid || event.FundingIntervalHours.Valid || !event.NextFundingTime.IsZero()) && !event.FundingObservedAt.IsZero() {
		fact.FundingObservedAt = event.FundingObservedAt.UTC()
		if event.FundingRate.Valid {
			fact.FundingRate = event.FundingRate
			fields |= exchange.DerivativeFieldFunding
		}
		if event.FundingIntervalHours.Valid {
			fact.FundingIntervalHours = event.FundingIntervalHours
			fields |= exchange.DerivativeFieldFundingInterval
		}
		if !event.NextFundingTime.IsZero() {
			fact.NextFundingTime = event.NextFundingTime.UTC()
			fields |= exchange.DerivativeFieldNextFundingTime
		}
	}
	if (event.OpenInterest.Valid || event.OpenInterestNotional.Valid) && !event.OpenInterestObservedAt.IsZero() {
		fact.OpenInterestObservedAt = event.OpenInterestObservedAt.UTC()
		if event.OpenInterest.Valid {
			fact.OpenInterest = event.OpenInterest
			fields |= exchange.DerivativeFieldOpenInterest
		}
		if event.OpenInterestNotional.Valid && event.OpenInterestAsset != "" {
			fact.OpenInterestNotional = event.OpenInterestNotional
			fact.OpenInterestNotionalAsset = event.OpenInterestAsset
			fields |= exchange.DerivativeFieldOpenInterestNotional
		}
	}
	if fields == 0 {
		return
	}
	owner := markethub.OwnerKey{Venue: fact.Instrument.Venue, Environment: fact.Environment, NativeFamily: exchange.NativeMarketFamily(fact.Instrument.Venue, exchange.ProductPerp, fact.Instrument.SettleAsset)}
	generation := source.Facts.Generation(owner)
	if generation == 0 {
		return
	}
	_ = source.Facts.Apply(exchange.DerivativePatch{
		Kind:         exchange.EventDelta,
		Scope:        exchange.CatalogScope{Venue: fact.Instrument.Venue, Environment: fact.Environment, Product: exchange.ProductPerp, Settlement: fact.Instrument.SettleAsset},
		NativeFamily: owner.NativeFamily,
		Generation:   generation,
		ObservedAt:   latestReferenceObservation(event), Fields: fields, Facts: []exchange.DerivativeFact{fact},
	})
}

func latestReferenceObservation(event exchange.DerivativeReferenceEvent) time.Time {
	latest := event.MarkObservedAt
	for _, candidate := range []time.Time{event.IndexObservedAt, event.FundingObservedAt, event.OpenInterestObservedAt} {
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest.UTC()
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
	staleAfter := marketcapability.FreshnessWindow(capability)
	changes, cancel, err := source.Facts.SubscribeInstrument(ctx, key.Owner, key.InstrumentID, exchange.DerivativeFieldMark, 8)
	if err != nil {
		return markethub.NativeStream{}, true, err
	}
	price, at, receivedAt, err := source.snapshot(instrument)
	if err != nil {
		cancel()
		return markethub.NativeStream{}, true, err
	}
	events := make(chan markethub.Message, 64)
	failures := make(chan error, 1)
	streamCtx, stop := context.WithCancel(ctx)
	go source.forward(streamCtx, instrument, price, at, receivedAt, staleAfter, changes, events, failures)
	return markethub.NativeStream{
		Snapshot: message(key, markethub.MessageSnapshot, at, "", markData(price, at, receivedAt, staleAfter, time.Now().UTC())),
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

func (source BoardMarks) snapshot(instrument publicmarket.Instrument) (string, time.Time, time.Time, error) {
	fact, ok := source.Facts.Get(string(instrument.Catalog.ID))
	if ok && fact.MarkPrice.Valid && !fact.MarkObservedAt.IsZero() {
		receivedAt, _ := source.Facts.FieldReceivedAt(string(instrument.Catalog.ID), exchange.DatasetMark)
		if receivedAt.IsZero() {
			receivedAt = fact.MarkObservedAt.UTC()
		}
		return fact.MarkPrice.Value.String(), fact.MarkObservedAt.UTC(), receivedAt, nil
	}
	return "", time.Time{}, time.Time{}, fmt.Errorf("%w: broad mark is absent", markethub.ErrUnavailable)
}

func (source BoardMarks) forward(ctx context.Context, instrument publicmarket.Instrument, lastPrice string, lastAt, lastReceivedAt time.Time, staleAfter time.Duration, changes <-chan markethub.FactChange, events chan<- markethub.Message, failures chan<- error) {
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
			price, at, receivedAt, err := source.snapshot(instrument)
			if err != nil || (price == lastPrice && at.Equal(lastAt) && receivedAt.Equal(lastReceivedAt)) {
				continue
			}
			lastPrice, lastAt, lastReceivedAt = price, at, receivedAt
			if !emit(ctx, events, markethub.Message{Kind: markethub.MessageDelta, ObservedAt: at, Data: markethub.Encode(markData(price, at, receivedAt, staleAfter, time.Now().UTC()))}) {
				return
			}
		}
	}
}

func markData(price string, observedAt, receivedAt time.Time, staleAfter time.Duration, now time.Time) map[string]any {
	if receivedAt.IsZero() {
		receivedAt = observedAt
	}
	age := now.Sub(receivedAt)
	if age < 0 {
		age = 0
	}
	return map[string]any{
		"price": price, "staleAfterMs": staleAfter.Milliseconds(), "ageMs": age.Milliseconds(),
		"receivedAt": receivedAt.UTC(),
	}
}
