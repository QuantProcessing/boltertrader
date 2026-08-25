package factoryclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type fundingMarketWatcher interface {
	stream(context.Context, exchange.FundingMarketPatch, func(exchange.FundingMarketPatch)) error
	close() error
}

type fundingMarketFeed struct {
	scope   exchange.CatalogScope
	loader  exchange.FundingBoardLoader
	watcher fundingMarketWatcher
	now     func() time.Time

	sequence     atomic.Uint64
	mu           sync.RWMutex
	status       exchange.FundingMarketFeedStatus
	bootstrap    exchange.FundingMarketPatch
	hasBootstrap bool
}

func NewFundingMarketFeed(scope exchange.CatalogScope, loader exchange.FundingBoardLoader, settings Settings) exchange.FundingMarketFeed {
	feed := &fundingMarketFeed{
		scope: scope, loader: loader, now: time.Now,
		status: exchange.FundingMarketFeedStatus{State: exchange.FundingMarketFeedUnavailable},
	}
	feed.watcher = fundingMarketWatcherFor(scope, loader, settings)
	return feed
}

// restFundingMarketWatcher is the safety-net transport for a venue whose
// realtime connection cannot be constructed. Venue-specific WebSocket
// watchers replace it below; keeping the fallback here lets a route remain
// degraded and observable rather than disappear from the board altogether.
type restFundingMarketWatcher struct {
	scope    exchange.CatalogScope
	loader   exchange.FundingBoardLoader
	interval time.Duration
}

func fundingMarketWatcherFor(scope exchange.CatalogScope, loader exchange.FundingBoardLoader, settings Settings) fundingMarketWatcher {
	switch scope.Venue {
	case exchange.VenueBinance:
		return &binanceFundingMarketWatcher{settings: settings}
	case exchange.VenueAster:
		return &binanceFundingMarketWatcher{settings: settings, aster: true}
	case exchange.VenueHyperliquid:
		return &hyperliquidFundingMarketWatcher{settings: settings}
	case exchange.VenueLighter:
		return &lighterFundingMarketWatcher{settings: settings}
	case exchange.VenueBybit:
		return &restFundingMarketWatcher{scope: scope, loader: loader, interval: 15 * time.Second}
	case exchange.VenueBitget:
		return &restFundingMarketWatcher{scope: scope, loader: loader, interval: 15 * time.Second}
	case exchange.VenueGate:
		return &gateFundingMarketWatcher{settings: settings}
	case exchange.VenueOKX:
		return &restFundingMarketWatcher{scope: scope, loader: loader, interval: 15 * time.Second}
	case exchange.VenueNado:
		return &nadoFundingMarketWatcher{settings: settings}
	default:
		return &restFundingMarketWatcher{scope: scope, loader: loader}
	}
}

func (watcher *restFundingMarketWatcher) stream(ctx context.Context, _ exchange.FundingMarketPatch, emit func(exchange.FundingMarketPatch)) error {
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
			snapshot, err := watcher.loader.LoadFundingBoard(ctx, watcher.scope)
			if err != nil {
				return err
			}
			emit(fundingMarketPatchFromBoard(snapshot))
		}
	}
}

func (*restFundingMarketWatcher) close() error { return nil }

func (feed *fundingMarketFeed) Bootstrap(ctx context.Context) (exchange.FundingMarketPatch, error) {
	if feed == nil || feed.loader == nil || ctx == nil {
		return exchange.FundingMarketPatch{}, errors.New("funding market bootstrap is unavailable")
	}
	snapshot, err := feed.loader.LoadFundingBoard(ctx, feed.scope)
	if err != nil {
		feed.fail(err)
		return exchange.FundingMarketPatch{}, err
	}
	if err := snapshot.Validate(feed.scope); err != nil {
		feed.fail(err)
		return exchange.FundingMarketPatch{}, err
	}
	patch := fundingMarketPatchFromBoard(snapshot)
	patch.Sequence = feed.sequence.Add(1)
	if err := patch.Validate(); err != nil {
		feed.fail(err)
		return exchange.FundingMarketPatch{}, err
	}
	feed.succeed(patch.ObservedAt)
	feed.mu.Lock()
	feed.bootstrap = patch
	feed.hasBootstrap = true
	feed.mu.Unlock()
	return patch, nil
}

func (feed *fundingMarketFeed) Stream(ctx context.Context, emit func(exchange.FundingMarketPatch)) error {
	if feed == nil || feed.watcher == nil || ctx == nil || emit == nil {
		return errors.New("funding market stream is unavailable")
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
	return feed.watcher.stream(ctx, bootstrap, func(patch exchange.FundingMarketPatch) {
		if patch.Scope == (exchange.CatalogScope{}) {
			patch.Scope = feed.scope
		}
		if patch.ObservedAt.IsZero() {
			patch.ObservedAt = feed.now().UTC()
		}
		patch.Sequence = feed.sequence.Add(1)
		if err := patch.Validate(); err != nil {
			feed.fail(err)
			return
		}
		feed.succeed(patch.ObservedAt)
		emit(patch)
	})
}

func (feed *fundingMarketFeed) Status() exchange.FundingMarketFeedStatus {
	if feed == nil {
		return exchange.FundingMarketFeedStatus{State: exchange.FundingMarketFeedUnavailable}
	}
	feed.mu.RLock()
	defer feed.mu.RUnlock()
	return feed.status
}

func (feed *fundingMarketFeed) Close() error {
	if feed == nil || feed.watcher == nil {
		return nil
	}
	return feed.watcher.close()
}

func (feed *fundingMarketFeed) succeed(observedAt time.Time) {
	feed.mu.Lock()
	feed.status.State = exchange.FundingMarketFeedLive
	feed.status.LastSuccessAt = observedAt.UTC()
	feed.status.LastError = ""
	feed.mu.Unlock()
}

func (feed *fundingMarketFeed) fail(err error) {
	feed.mu.Lock()
	if feed.status.LastSuccessAt.IsZero() {
		feed.status.State = exchange.FundingMarketFeedUnavailable
	} else {
		feed.status.State = exchange.FundingMarketFeedDegraded
	}
	if err != nil {
		feed.status.LastError = err.Error()
	}
	feed.mu.Unlock()
}

func fundingMarketPatchFromBoard(snapshot exchange.FundingBoardSnapshot) exchange.FundingMarketPatch {
	facts := make([]exchange.FundingMarketFact, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		settlement := snapshot.Scope.Settlement
		if settlement == "" {
			settlement = entry.QuoteAsset
		}
		fact := exchange.FundingMarketFact{
			Instrument: exchange.InstrumentIdentity{
				Venue: snapshot.Scope.Venue, Product: exchange.ProductPerp,
				Symbol: entry.Symbol, NativeSymbol: entry.NativeSymbol, NativeID: entry.NativeID,
				BaseAsset: entry.BaseAsset, QuoteAsset: entry.QuoteAsset, SettleAsset: settlement,
			},
			FundingRate:               exchange.OptionalDecimal{Value: entry.Rate, Valid: true},
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
		if fact.FundingObservedAt.IsZero() {
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
	return exchange.FundingMarketPatch{
		Kind: exchange.FundingMarketPatchSnapshot, Scope: snapshot.Scope,
		ObservedAt: snapshot.ObservedAt, Fields: exchange.FundingMarketFieldAll, Facts: facts,
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
