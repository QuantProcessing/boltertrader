package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type derivativeMarketFeed struct {
	native        *nativeDerivativeFeed
	routeProfiles []string
	nativeFamily  string
	resolver      exchange.DerivativeInstrumentResolver
	hotMu         sync.Mutex
	nativeByID    map[string]string
	hot           map[string]bool
}

func NewDerivativeFeed(scope exchange.CatalogScope, routeProfiles []string, nativeFamily string, loader DerivativeSnapshotLoader, resolver exchange.DerivativeInstrumentResolver, settings Settings) exchange.DerivativeFeed {
	native := newNativeDerivativeFeed(scope, loader, settings)
	return &derivativeMarketFeed{
		native: native, routeProfiles: append([]string(nil), routeProfiles...), nativeFamily: nativeFamily, resolver: resolver,
		nativeByID: make(map[string]string), hot: make(map[string]bool),
	}
}

func (feed *derivativeMarketFeed) SetHotInstrument(instrumentID string, active bool) {
	if feed == nil || feed.native == nil || instrumentID == "" {
		return
	}
	feed.hotMu.Lock()
	if active {
		feed.hot[instrumentID] = true
	} else {
		delete(feed.hot, instrumentID)
	}
	native := feed.nativeByID[instrumentID]
	feed.hotMu.Unlock()
	if native != "" {
		feed.native.SetHotInstrument(native, active)
	}
}

func (feed *derivativeMarketFeed) Bootstrap(ctx context.Context) (exchange.DerivativePatch, error) {
	if feed == nil || feed.native == nil {
		return exchange.DerivativePatch{}, errors.New("derivative feed is unavailable")
	}
	patch, err := feed.native.Bootstrap(ctx)
	if err != nil {
		return exchange.DerivativePatch{}, err
	}
	return feed.project(ctx, patch)
}

func (feed *derivativeMarketFeed) Stream(ctx context.Context, emit func(exchange.DerivativePatch)) error {
	if feed == nil || feed.native == nil || emit == nil {
		return errors.New("derivative feed is unavailable")
	}
	return feed.native.Stream(ctx, func(patch nativeDerivativePatch) {
		projected, err := feed.project(ctx, patch)
		if err == nil {
			emit(projected)
		}
	})
}

func (feed *derivativeMarketFeed) Close() error {
	if feed == nil || feed.native == nil {
		return nil
	}
	return feed.native.Close()
}

func (feed *derivativeMarketFeed) project(ctx context.Context, patch nativeDerivativePatch) (exchange.DerivativePatch, error) {
	if feed == nil || feed.resolver == nil || len(feed.routeProfiles) == 0 || feed.nativeFamily == "" {
		return exchange.DerivativePatch{}, errors.New("derivative projection is unavailable")
	}
	if err := patch.validate(); err != nil {
		return exchange.DerivativePatch{}, err
	}
	facts := make([]exchange.DerivativeFact, 0, len(patch.Facts))
	for _, native := range patch.Facts {
		for _, routeProfile := range feed.routeProfiles {
			fact, ok := feed.resolver.ResolveDerivativeInstrument(ctx, routeProfile, native.Instrument.NativeSymbol, native.Instrument.NativeID)
			if !ok {
				continue
			}
			fact.FundingRate, fact.FundingIntervalHours, fact.NextFundingTime = native.FundingRate, native.FundingIntervalHours, native.NextFundingTime
			fact.MarkPrice, fact.IndexPrice = native.MarkPrice, native.IndexPrice
			fact.OpenInterest, fact.OpenInterestNotional = native.OpenInterest, native.OpenInterestNotional
			fact.OpenInterestNotionalAsset = native.OpenInterestNotionalAsset
			fact.FundingObservedAt = native.FundingObservedAt
			fact.MarkObservedAt = firstObservedAt(native.MarkObservedAt, native.PriceObservedAt)
			fact.IndexObservedAt = firstObservedAt(native.IndexObservedAt, native.PriceObservedAt)
			fact.OpenInterestObservedAt = native.OpenInterestObservedAt
			facts = append(facts, fact)
			feed.hotMu.Lock()
			feed.nativeByID[fact.InstrumentID] = native.Instrument.NativeSymbol
			active := feed.hot[fact.InstrumentID]
			feed.hotMu.Unlock()
			if active {
				feed.native.SetHotInstrument(native.Instrument.NativeSymbol, true)
			}
		}
	}
	if len(patch.Facts) > 0 && len(facts) == 0 {
		return exchange.DerivativePatch{}, fmt.Errorf("derivative catalog projection unavailable")
	}
	kind := exchange.EventDelta
	if patch.Kind == nativeDerivativePatchSnapshot {
		kind = exchange.EventSnapshot
	}
	return exchange.DerivativePatch{
		Kind: kind, Scope: patch.Scope, NativeFamily: feed.nativeFamily, Generation: 1,
		ObservedAt: patch.ObservedAt, SourceSequence: strconv.FormatUint(patch.Sequence, 10),
		Fields: derivativeFields(patch.Fields), Facts: facts,
	}, nil
}

func derivativeFields(fields nativeDerivativeField) exchange.DerivativeField {
	var result exchange.DerivativeField
	for _, item := range []struct {
		old  nativeDerivativeField
		next exchange.DerivativeField
	}{
		{nativeDerivativeFieldIdentity, exchange.DerivativeFieldIdentity},
		{nativeDerivativeFieldFunding, exchange.DerivativeFieldFunding},
		{nativeDerivativeFieldFundingInterval, exchange.DerivativeFieldFundingInterval},
		{nativeDerivativeFieldNextFundingTime, exchange.DerivativeFieldNextFundingTime},
		{nativeDerivativeFieldMarkPrice, exchange.DerivativeFieldMark},
		{nativeDerivativeFieldIndexPrice, exchange.DerivativeFieldIndex},
		{nativeDerivativeFieldOpenInterest, exchange.DerivativeFieldOpenInterest},
		{nativeDerivativeFieldOpenInterestNotional, exchange.DerivativeFieldOpenInterestNotional},
	} {
		if fields.Has(item.old) {
			result |= item.next
		}
	}
	return result
}

type derivativeWatcher interface {
	stream(context.Context, nativeDerivativePatch, func(nativeDerivativePatch)) error
	close() error
}

type hotDerivativeWatcher interface {
	setHotInstrument(string, bool)
}

type nativeDerivativeFeed struct {
	scope    exchange.CatalogScope
	loader   DerivativeSnapshotLoader
	watcher  derivativeWatcher
	settings Settings
	now      func() time.Time

	sequence     atomic.Uint64
	mu           sync.RWMutex
	bootstrap    nativeDerivativePatch
	hasBootstrap bool
}

func newNativeDerivativeFeed(scope exchange.CatalogScope, loader DerivativeSnapshotLoader, settings Settings) *nativeDerivativeFeed {
	feed := &nativeDerivativeFeed{
		scope: scope, loader: loader, settings: settings, now: time.Now,
	}
	feed.watcher = derivativeWatcherFor(scope, loader, settings)
	return feed
}

// restDerivativeWatcher is the safety-net transport for a venue whose
// realtime connection cannot be constructed. Venue-specific WebSocket
// watchers replace it below; keeping the fallback here lets a route remain
// degraded and observable rather than disappear from the board altogether.
type restDerivativeWatcher struct {
	scope    exchange.CatalogScope
	loader   DerivativeSnapshotLoader
	interval time.Duration
	settings Settings
}

func derivativeWatcherFor(scope exchange.CatalogScope, loader DerivativeSnapshotLoader, settings Settings) derivativeWatcher {
	streaming, interval := derivativeRuntimeStrategy(settings.MarketCapabilities)
	if !streaming {
		return &restDerivativeWatcher{scope: scope, loader: loader, interval: interval, settings: settings}
	}
	switch scope.Venue {
	case exchange.VenueBinance:
		return &binanceDerivativeWatcher{settings: settings}
	case exchange.VenueAster:
		return &binanceDerivativeWatcher{settings: settings, aster: true}
	case exchange.VenueHyperliquid:
		return &hyperliquidDerivativeWatcher{settings: settings}
	case exchange.VenueLighter:
		return &lighterDerivativeWatcher{settings: settings}
	case exchange.VenueGate:
		return &gateDerivativeWatcher{scope: scope, loader: loader, settings: settings}
	case exchange.VenueNado:
		return &nadoDerivativeWatcher{settings: settings}
	default:
		return &restDerivativeWatcher{scope: scope, loader: loader, interval: interval, settings: settings}
	}
}

// derivativeRuntimeStrategy turns the validated capability rows selected by
// Gateway into the adapter's broad-feed transport and cadence. Venue switches
// below choose only the concrete protocol implementation; they no longer
// decide whether the runtime uses WebSocket or REST.
func derivativeRuntimeStrategy(capabilities []exchange.MarketCapability) (bool, time.Duration) {
	streaming := false
	interval := time.Duration(0)
	for _, capability := range capabilities {
		if capability.Transport == exchange.TransportWebSocket {
			streaming = true
		}
		if capability.Transport == exchange.TransportREST && capability.NominalCadence > 0 && (interval == 0 || capability.NominalCadence < interval) {
			interval = capability.NominalCadence
		}
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}
	return streaming, interval
}

func (watcher *restDerivativeWatcher) stream(ctx context.Context, _ nativeDerivativePatch, emit func(nativeDerivativePatch)) error {
	interval := watcher.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			snapshot, err := loadDerivativeSnapshot(ctx, watcher.scope, watcher.loader, watcher.settings)
			if err != nil {
				return err
			}
			emit(nativeDerivativePatchFromSnapshot(snapshot))
		}
	}
}

func (*restDerivativeWatcher) close() error { return nil }

func (feed *nativeDerivativeFeed) Bootstrap(ctx context.Context) (nativeDerivativePatch, error) {
	if feed == nil || feed.loader == nil || ctx == nil {
		return nativeDerivativePatch{}, errors.New("native derivative bootstrap is unavailable")
	}
	snapshot, err := loadDerivativeSnapshot(ctx, feed.scope, feed.loader, feed.settings)
	if err != nil {
		return nativeDerivativePatch{}, err
	}
	if err := snapshot.Validate(feed.scope); err != nil {
		return nativeDerivativePatch{}, err
	}
	patch := nativeDerivativePatchFromSnapshot(snapshot)
	patch.Sequence = feed.sequence.Add(1)
	if err := patch.validate(); err != nil {
		return nativeDerivativePatch{}, err
	}
	feed.mu.Lock()
	feed.bootstrap = patch
	feed.hasBootstrap = true
	feed.mu.Unlock()
	return patch, nil
}

func (feed *nativeDerivativeFeed) Stream(ctx context.Context, emit func(nativeDerivativePatch)) error {
	if feed == nil || feed.watcher == nil || ctx == nil || emit == nil {
		return errors.New("native derivative stream is unavailable")
	}
	feed.mu.RLock()
	bootstrap, hasBootstrap := feed.bootstrap, feed.hasBootstrap
	feed.mu.RUnlock()
	if !hasBootstrap {
		var err error
		bootstrap, err = feed.Bootstrap(ctx)
		if err != nil {
			return err
		}
	}
	return feed.watcher.stream(ctx, bootstrap, func(patch nativeDerivativePatch) {
		if patch.Scope == (exchange.CatalogScope{}) {
			patch.Scope = feed.scope
		}
		if patch.ObservedAt.IsZero() {
			patch.ObservedAt = feed.now().UTC()
		}
		patch.Sequence = feed.sequence.Add(1)
		if err := patch.validate(); err != nil {
			return
		}
		emit(patch)
	})
}

func (feed *nativeDerivativeFeed) Close() error {
	if feed == nil || feed.watcher == nil {
		return nil
	}
	return feed.watcher.close()
}

func (feed *nativeDerivativeFeed) SetHotInstrument(nativeSymbol string, active bool) {
	if feed == nil || nativeSymbol == "" {
		return
	}
	if watcher, ok := feed.watcher.(hotDerivativeWatcher); ok {
		watcher.setHotInstrument(nativeSymbol, active)
	}
}

func nativeDerivativePatchFromSnapshot(snapshot DerivativeSnapshot) nativeDerivativePatch {
	facts := make([]nativeDerivativeFact, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		settlement := snapshot.Scope.Settlement
		if settlement == "" {
			settlement = entry.QuoteAsset
		}
		fact := nativeDerivativeFact{
			Instrument: exchange.InstrumentIdentity{
				Venue: snapshot.Scope.Venue, Product: exchange.ProductPerp,
				Symbol: entry.Symbol, NativeSymbol: entry.NativeSymbol, NativeID: entry.NativeID,
				BaseAsset: entry.BaseAsset, QuoteAsset: entry.QuoteAsset, SettleAsset: settlement,
			},
			FundingRate:               exchange.OptionalDecimal{Value: entry.Rate, Valid: entry.RateValid},
			FundingIntervalHours:      entry.IntervalHours,
			NextFundingTime:           entry.NextFundingTime,
			MarkPrice:                 entry.MarkPrice,
			IndexPrice:                entry.IndexPrice,
			OpenInterest:              entry.OpenInterest,
			OpenInterestNotional:      entry.OpenInterestNotional,
			OpenInterestNotionalAsset: entry.OpenInterestNotionalAsset,
			FundingObservedAt:         entry.FundingObservedAt,
			MarkObservedAt:            entry.MarkObservedAt,
			IndexObservedAt:           entry.IndexObservedAt,
			PriceObservedAt:           entry.PriceObservedAt,
			OpenInterestObservedAt:    entry.OpenInterestObservedAt,
		}
		if fact.FundingRate.Valid && fact.FundingObservedAt.IsZero() {
			fact.FundingObservedAt = snapshot.ObservedAt
		}
		if fact.OpenInterestNotional.Valid && fact.OpenInterestNotionalAsset == "" {
			fact.OpenInterestNotionalAsset = settlement
		}
		if fact.MarkPrice.Valid && fact.MarkObservedAt.IsZero() {
			fact.MarkObservedAt = firstObservedAt(fact.PriceObservedAt, snapshot.ObservedAt)
		}
		if fact.IndexPrice.Valid && fact.IndexObservedAt.IsZero() {
			fact.IndexObservedAt = firstObservedAt(fact.PriceObservedAt, snapshot.ObservedAt)
		}
		fact.PriceObservedAt = latestFundingObservation(fact.MarkObservedAt, fact.IndexObservedAt)
		if (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) && fact.OpenInterestObservedAt.IsZero() {
			fact.OpenInterestObservedAt = snapshot.ObservedAt
		}
		facts = append(facts, fact)
	}
	return nativeDerivativePatch{
		Kind: nativeDerivativePatchSnapshot, Scope: snapshot.Scope,
		ObservedAt: snapshot.ObservedAt, Fields: nativeDerivativeFieldAll, Facts: facts,
	}
}

func firstObservedAt(candidate, fallback time.Time) time.Time {
	if !candidate.IsZero() {
		return candidate.UTC()
	}
	return fallback.UTC()
}

func latestFundingObservation(left, right time.Time) time.Time {
	if right.After(left) {
		return right.UTC()
	}
	if left.IsZero() {
		return time.Time{}
	}
	return left.UTC()
}
