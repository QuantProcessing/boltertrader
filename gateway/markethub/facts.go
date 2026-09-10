package markethub

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// FactStore is the single in-memory authority for derivative facts. Broad
// venue feeds and hot per-instrument feeds both write patches here; field
// clocks decide which observation wins.
type FactStore struct {
	// dispatchMu preserves change ordering while notifications are delivered
	// outside mu. A slow observer never holds either lock.
	dispatchMu sync.Mutex
	mu         sync.RWMutex
	facts      map[string]exchange.DerivativeFact
	receipts   map[string]factReceipts
	owners     map[string]OwnerKey
	ownerFacts map[string]map[string]struct{}
	ownerKeys  map[string]OwnerKey
	generation map[string]uint64
	fields     map[string]exchange.DerivativeField
	metrics    map[string]map[exchange.MarketDataset]factMetricState
	sequence   uint64
	nextSub    uint64
	subs       map[uint64]*factSubscriber
	globalSubs map[uint64]*factSubscriber
	ownerSubs  map[string]map[uint64]*factSubscriber
	factSubs   map[string]map[string]map[uint64]*factSubscriber
	observer   Observer
	now        func() time.Time
}

type factSubscriber struct {
	id           uint64
	updates      chan FactChange
	ownerID      string
	instrumentID string
	fields       exchange.DerivativeField
	mu           sync.Mutex
	closed       bool
}

type factMetricState struct {
	available int
	newest    time.Time
}

// factReceipts records when Gateway accepted each field group. Source
// observation clocks remain on DerivativeFact for ordering and provenance;
// freshness uses this local clock so clock skew cannot make a live feed stale.
type factReceipts struct {
	funding      time.Time
	mark         time.Time
	index        time.Time
	openInterest time.Time
}

type factMetricSample struct {
	owner    OwnerKey
	dataset  exchange.MarketDataset
	age      time.Duration
	coverage float64
}

type factMetricSpec struct {
	field   exchange.DerivativeField
	dataset exchange.MarketDataset
	at      func(exchange.DerivativeFact) time.Time
	valid   func(exchange.DerivativeFact) bool
}

var factMetricSpecs = [...]factMetricSpec{
	{exchange.DerivativeFieldFunding, exchange.DatasetFunding, func(f exchange.DerivativeFact) time.Time { return f.FundingObservedAt }, func(f exchange.DerivativeFact) bool { return f.FundingRate.Valid }},
	{exchange.DerivativeFieldMark, exchange.DatasetMark, func(f exchange.DerivativeFact) time.Time { return f.MarkObservedAt }, func(f exchange.DerivativeFact) bool { return f.MarkPrice.Valid }},
	{exchange.DerivativeFieldIndex, exchange.DatasetIndex, func(f exchange.DerivativeFact) time.Time { return f.IndexObservedAt }, func(f exchange.DerivativeFact) bool { return f.IndexPrice.Valid }},
	{exchange.DerivativeFieldOpenInterest | exchange.DerivativeFieldOpenInterestNotional, exchange.DatasetOpenInterest, func(f exchange.DerivativeFact) time.Time { return f.OpenInterestObservedAt }, func(f exchange.DerivativeFact) bool { return f.OpenInterest.Valid || f.OpenInterestNotional.Valid }},
}

type FactChange struct {
	Sequence   uint64                   `json:"sequence"`
	Owner      OwnerKey                 `json:"owner"`
	Generation uint64                   `json:"generation"`
	Fields     exchange.DerivativeField `json:"fields"`
}

func NewFactStore(observer Observer, now func() time.Time) *FactStore {
	if now == nil {
		now = time.Now
	}
	return &FactStore{
		facts: make(map[string]exchange.DerivativeFact), receipts: make(map[string]factReceipts), owners: make(map[string]OwnerKey),
		ownerFacts: make(map[string]map[string]struct{}), ownerKeys: make(map[string]OwnerKey),
		generation: make(map[string]uint64), fields: make(map[string]exchange.DerivativeField),
		metrics: make(map[string]map[exchange.MarketDataset]factMetricState),
		subs:    make(map[uint64]*factSubscriber), globalSubs: make(map[uint64]*factSubscriber),
		ownerSubs: make(map[string]map[uint64]*factSubscriber), factSubs: make(map[string]map[string]map[uint64]*factSubscriber),
		observer: observer, now: now,
	}
}

func (store *FactStore) Apply(patch exchange.DerivativePatch) error {
	if store == nil {
		return ErrUnavailable
	}
	if err := patch.Validate(); err != nil {
		return fmt.Errorf("apply derivative patch: %w", err)
	}
	owner := OwnerKey{Venue: patch.Scope.Venue, Environment: patch.Scope.Environment, NativeFamily: patch.NativeFamily}
	ownerID := factOwnerID(owner)
	receivedAt := store.now().UTC()
	store.dispatchMu.Lock()
	store.mu.Lock()
	currentGeneration := store.generation[ownerID]
	if patch.Generation < currentGeneration {
		store.mu.Unlock()
		store.dispatchMu.Unlock()
		return nil
	}
	if patch.Generation > currentGeneration {
		if patch.Kind != exchange.EventSnapshot || !patch.Fields.Has(exchange.DerivativeFieldIdentity) {
			store.mu.Unlock()
			store.dispatchMu.Unlock()
			return fmt.Errorf("%w: derivative generation requires snapshot", ErrResyncRequired)
		}
		store.generation[ownerID] = patch.Generation
		store.fields[ownerID] = 0
	}
	store.ownerKeys[ownerID] = owner
	store.fields[ownerID] |= patch.Fields
	changed := make(map[string]struct{}, len(patch.Facts))
	rebuild := make(map[string]struct{})
	if patch.Kind == exchange.EventSnapshot && patch.Fields.Has(exchange.DerivativeFieldIdentity) {
		seen := make(map[string]struct{}, len(patch.Facts))
		for _, fact := range patch.Facts {
			seen[fact.InstrumentID] = struct{}{}
		}
		for instrumentID := range store.ownerFacts[ownerID] {
			if patch.Scope.Settlement != "" && store.facts[instrumentID].Instrument.SettleAsset != patch.Scope.Settlement {
				continue
			}
			if _, present := seen[instrumentID]; !present {
				delete(store.facts, instrumentID)
				delete(store.receipts, instrumentID)
				delete(store.owners, instrumentID)
				delete(store.ownerFacts[ownerID], instrumentID)
				changed[instrumentID] = struct{}{}
			}
		}
		rebuild[ownerID] = struct{}{}
	}
	for _, incoming := range patch.Facts {
		normalizeDerivativeClocks(&incoming, patch.Fields, patch.ObservedAt)
		current, existed := store.facts[incoming.InstrumentID]
		currentOwner, hadOwner := store.owners[incoming.InstrumentID]
		currentOwnerID := factOwnerID(currentOwner)
		if hadOwner && currentOwnerID != ownerID {
			delete(store.ownerFacts[currentOwnerID], incoming.InstrumentID)
			delete(store.receipts, incoming.InstrumentID)
			rebuild[currentOwnerID] = struct{}{}
			existed = false
		}
		store.updateReceiptsLocked(incoming.InstrumentID, current, incoming, patch.Fields, receivedAt)
		next := exchange.ApplyDerivativePatch(current, incoming, patch.Fields)
		store.facts[incoming.InstrumentID] = next
		store.owners[incoming.InstrumentID] = owner
		if store.ownerFacts[ownerID] == nil {
			store.ownerFacts[ownerID] = make(map[string]struct{})
		}
		store.ownerFacts[ownerID][incoming.InstrumentID] = struct{}{}
		if _, fullRebuild := rebuild[ownerID]; !fullRebuild {
			store.updateMetricsLocked(ownerID, current, existed, next)
		}
		changed[incoming.InstrumentID] = struct{}{}
	}
	for dirtyOwnerID := range rebuild {
		store.rebuildMetricsLocked(dirtyOwnerID)
	}
	store.sequence++
	change := FactChange{Sequence: store.sequence, Owner: owner, Generation: patch.Generation, Fields: patch.Fields}
	subscribers := store.subscribersLocked(ownerID, changed, patch.Fields)
	store.mu.Unlock()
	failed := make([]*factSubscriber, 0)
	for _, subscriber := range subscribers {
		if !subscriber.send(change) {
			failed = append(failed, subscriber)
		}
	}
	if len(failed) > 0 {
		store.mu.Lock()
		for _, subscriber := range failed {
			store.removeSubscriberLocked(subscriber.id, subscriber)
		}
		store.mu.Unlock()
	}
	store.dispatchMu.Unlock()
	return nil
}

func (store *FactStore) Generation(owner OwnerKey) uint64 {
	if store == nil {
		return 0
	}
	store.mu.RLock()
	generation := store.generation[factOwnerID(owner)]
	store.mu.RUnlock()
	return generation
}

func normalizeDerivativeClocks(fact *exchange.DerivativeFact, fields exchange.DerivativeField, observedAt time.Time) {
	observedAt = observedAt.UTC()
	if fields.Has(exchange.DerivativeFieldFunding) || fields.Has(exchange.DerivativeFieldFundingInterval) || fields.Has(exchange.DerivativeFieldNextFundingTime) {
		if (fact.FundingRate.Valid || fact.FundingIntervalHours.Valid || !fact.NextFundingTime.IsZero()) && fact.FundingObservedAt.IsZero() {
			fact.FundingObservedAt = observedAt
		} else {
			fact.FundingObservedAt = fact.FundingObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldMark) {
		if fact.MarkPrice.Valid && fact.MarkObservedAt.IsZero() {
			fact.MarkObservedAt = observedAt
		} else {
			fact.MarkObservedAt = fact.MarkObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldIndex) {
		if fact.IndexPrice.Valid && fact.IndexObservedAt.IsZero() {
			fact.IndexObservedAt = observedAt
		} else {
			fact.IndexObservedAt = fact.IndexObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldOpenInterest) || fields.Has(exchange.DerivativeFieldOpenInterestNotional) {
		if (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) && fact.OpenInterestObservedAt.IsZero() {
			fact.OpenInterestObservedAt = observedAt
		} else {
			fact.OpenInterestObservedAt = fact.OpenInterestObservedAt.UTC()
		}
	}
}

func (store *FactStore) Snapshot(context.Context) ([]exchange.DerivativeFact, error) {
	if store == nil {
		return nil, ErrUnavailable
	}
	store.mu.RLock()
	result := make([]exchange.DerivativeFact, 0, len(store.facts))
	for _, fact := range store.facts {
		result = append(result, fact)
	}
	store.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].RouteProfileID != result[j].RouteProfileID {
			return result[i].RouteProfileID < result[j].RouteProfileID
		}
		return result[i].InstrumentID < result[j].InstrumentID
	})
	return result, nil
}

func (store *FactStore) Get(instrumentID string) (exchange.DerivativeFact, bool) {
	if store == nil {
		return exchange.DerivativeFact{}, false
	}
	store.mu.RLock()
	fact, ok := store.facts[instrumentID]
	store.mu.RUnlock()
	return fact, ok
}

// FieldReceivedAt returns Gateway's local acceptance time for one dataset.
// The venue-provided observation time remains the source-ordering clock.
func (store *FactStore) FieldReceivedAt(instrumentID string, dataset exchange.MarketDataset) (time.Time, bool) {
	if store == nil {
		return time.Time{}, false
	}
	store.mu.RLock()
	receipt, ok := store.receipts[instrumentID]
	store.mu.RUnlock()
	if !ok {
		return time.Time{}, false
	}
	var at time.Time
	switch dataset {
	case exchange.DatasetFunding:
		at = receipt.funding
	case exchange.DatasetMark:
		at = receipt.mark
	case exchange.DatasetIndex:
		at = receipt.index
	case exchange.DatasetOpenInterest:
		at = receipt.openInterest
	default:
		return time.Time{}, false
	}
	return at, !at.IsZero()
}

func (store *FactStore) updateReceiptsLocked(instrumentID string, current, incoming exchange.DerivativeFact, fields exchange.DerivativeField, receivedAt time.Time) {
	receipt := store.receipts[instrumentID]
	fundingFields := fields & (exchange.DerivativeFieldFunding | exchange.DerivativeFieldFundingInterval | exchange.DerivativeFieldNextFundingTime)
	if fundingFields != 0 && observationAccepted(current.FundingObservedAt, incoming.FundingObservedAt) {
		receipt.funding = receivedAt
	}
	if fields.Has(exchange.DerivativeFieldMark) && observationAccepted(current.MarkObservedAt, incoming.MarkObservedAt) {
		receipt.mark = receivedAt
	}
	if fields.Has(exchange.DerivativeFieldIndex) && observationAccepted(current.IndexObservedAt, incoming.IndexObservedAt) {
		receipt.index = receivedAt
	}
	openInterestFields := fields & (exchange.DerivativeFieldOpenInterest | exchange.DerivativeFieldOpenInterestNotional)
	if openInterestFields != 0 && observationAccepted(current.OpenInterestObservedAt, incoming.OpenInterestObservedAt) {
		receipt.openInterest = receivedAt
	}
	store.receipts[instrumentID] = receipt
}

func observationAccepted(current, incoming time.Time) bool {
	return !incoming.IsZero() && (current.IsZero() || !incoming.Before(current))
}

func (store *FactStore) Subscribe(ctx context.Context, buffer int) (<-chan FactChange, func(), error) {
	return store.subscribe(ctx, nil, "", 0, buffer)
}

// SubscribeInstrument delivers only patches that can change one instrument's
// requested fields. It prevents every per-instrument mark projection from
// waking on every venue and every other instrument in the shared FactStore.
func (store *FactStore) SubscribeInstrument(ctx context.Context, owner OwnerKey, instrumentID string, fields exchange.DerivativeField, buffer int) (<-chan FactChange, func(), error) {
	if owner.Venue == "" || owner.Environment == "" || owner.NativeFamily == "" || instrumentID == "" || fields == 0 {
		return nil, nil, ErrUnavailable
	}
	return store.subscribe(ctx, &owner, instrumentID, fields, buffer)
}

func (store *FactStore) subscribe(ctx context.Context, owner *OwnerKey, instrumentID string, fields exchange.DerivativeField, buffer int) (<-chan FactChange, func(), error) {
	if store == nil || ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrUnavailable
	}
	if buffer <= 0 {
		buffer = 1
	}
	store.mu.Lock()
	store.nextSub++
	id := store.nextSub
	updates := make(chan FactChange, buffer)
	ownerID := ""
	if owner != nil {
		ownerID = factOwnerID(*owner)
	}
	subscriber := &factSubscriber{id: id, updates: updates, ownerID: ownerID, instrumentID: instrumentID, fields: fields}
	store.addSubscriberLocked(subscriber)
	store.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			store.mu.Lock()
			current := store.removeSubscriberLocked(id, subscriber)
			store.mu.Unlock()
			if current != nil {
				current.close()
			}
		})
	}
	go func() { <-ctx.Done(); cancel() }()
	return updates, cancel, nil
}

func (store *FactStore) addSubscriberLocked(subscriber *factSubscriber) {
	store.subs[subscriber.id] = subscriber
	if subscriber.ownerID == "" {
		store.globalSubs[subscriber.id] = subscriber
		return
	}
	if subscriber.instrumentID == "" {
		if store.ownerSubs[subscriber.ownerID] == nil {
			store.ownerSubs[subscriber.ownerID] = make(map[uint64]*factSubscriber)
		}
		store.ownerSubs[subscriber.ownerID][subscriber.id] = subscriber
		return
	}
	if store.factSubs[subscriber.ownerID] == nil {
		store.factSubs[subscriber.ownerID] = make(map[string]map[uint64]*factSubscriber)
	}
	if store.factSubs[subscriber.ownerID][subscriber.instrumentID] == nil {
		store.factSubs[subscriber.ownerID][subscriber.instrumentID] = make(map[uint64]*factSubscriber)
	}
	store.factSubs[subscriber.ownerID][subscriber.instrumentID][subscriber.id] = subscriber
}

func (store *FactStore) removeSubscriberLocked(id uint64, expected *factSubscriber) *factSubscriber {
	current := store.subs[id]
	if current == nil || (expected != nil && current != expected) {
		return nil
	}
	delete(store.subs, id)
	if current.ownerID == "" {
		delete(store.globalSubs, id)
		return current
	}
	if current.instrumentID == "" {
		delete(store.ownerSubs[current.ownerID], id)
		if len(store.ownerSubs[current.ownerID]) == 0 {
			delete(store.ownerSubs, current.ownerID)
		}
		return current
	}
	byInstrument := store.factSubs[current.ownerID]
	delete(byInstrument[current.instrumentID], id)
	if len(byInstrument[current.instrumentID]) == 0 {
		delete(byInstrument, current.instrumentID)
	}
	if len(byInstrument) == 0 {
		delete(store.factSubs, current.ownerID)
	}
	return current
}

func (store *FactStore) subscribersLocked(ownerID string, changed map[string]struct{}, fields exchange.DerivativeField) []*factSubscriber {
	selected := make(map[uint64]*factSubscriber, len(store.globalSubs)+len(store.ownerSubs[ownerID]))
	appendMatching := func(candidates map[uint64]*factSubscriber) {
		for id, subscriber := range candidates {
			if subscriber.fields == 0 || fields&subscriber.fields != 0 {
				selected[id] = subscriber
			}
		}
	}
	appendMatching(store.globalSubs)
	appendMatching(store.ownerSubs[ownerID])
	for instrumentID := range changed {
		appendMatching(store.factSubs[ownerID][instrumentID])
	}
	result := make([]*factSubscriber, 0, len(selected))
	for _, subscriber := range selected {
		result = append(result, subscriber)
	}
	return result
}

func (subscriber *factSubscriber) send(change FactChange) bool {
	subscriber.mu.Lock()
	defer subscriber.mu.Unlock()
	if subscriber.closed {
		return false
	}
	select {
	case subscriber.updates <- change:
		return true
	default:
		subscriber.closed = true
		close(subscriber.updates)
		return false
	}
}

func (subscriber *factSubscriber) close() {
	subscriber.mu.Lock()
	defer subscriber.mu.Unlock()
	if subscriber.closed {
		return
	}
	subscriber.closed = true
	close(subscriber.updates)
}

// RunMetrics keeps age gauges moving even while a feed is silent. Updating an
// age only when a patch arrives freezes a healthy-looking value precisely when
// the upstream has stopped, which defeats the metric's purpose.
func (store *FactStore) RunMetrics(ctx context.Context, interval time.Duration) {
	if store == nil || store.observer == nil || ctx == nil {
		return
	}
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := store.now().UTC()
			store.mu.RLock()
			samples := make([]factMetricSample, 0, len(store.ownerFacts)*len(factMetricSpecs))
			for ownerID, members := range store.ownerFacts {
				if len(members) == 0 {
					continue
				}
				samples = append(samples, store.metricSamplesLocked(ownerID, store.fields[ownerID], now)...)
			}
			store.mu.RUnlock()
			store.observe(samples)
		}
	}
}

func (store *FactStore) updateMetricsLocked(ownerID string, current exchange.DerivativeFact, existed bool, next exchange.DerivativeFact) {
	if store.metrics[ownerID] == nil {
		store.metrics[ownerID] = make(map[exchange.MarketDataset]factMetricState, len(factMetricSpecs))
	}
	for _, spec := range factMetricSpecs {
		state := store.metrics[ownerID][spec.dataset]
		oldAvailable := existed && spec.valid(current)
		newAvailable := spec.valid(next)
		if oldAvailable != newAvailable {
			if newAvailable {
				state.available++
			} else {
				state.available--
			}
		}
		if observedAt := spec.at(next); observedAt.After(state.newest) {
			state.newest = observedAt
		}
		store.metrics[ownerID][spec.dataset] = state
	}
}

func (store *FactStore) rebuildMetricsLocked(ownerID string) {
	states := make(map[exchange.MarketDataset]factMetricState, len(factMetricSpecs))
	for instrumentID := range store.ownerFacts[ownerID] {
		fact, ok := store.facts[instrumentID]
		if !ok {
			continue
		}
		for _, spec := range factMetricSpecs {
			state := states[spec.dataset]
			if spec.valid(fact) {
				state.available++
			}
			if observedAt := spec.at(fact); observedAt.After(state.newest) {
				state.newest = observedAt
			}
			states[spec.dataset] = state
		}
	}
	store.metrics[ownerID] = states
}

func (store *FactStore) metricSamplesLocked(ownerID string, fields exchange.DerivativeField, now time.Time) []factMetricSample {
	owner, ok := store.ownerKeys[ownerID]
	if !ok {
		return nil
	}
	total := len(store.ownerFacts[ownerID])
	result := make([]factMetricSample, 0, len(factMetricSpecs))
	for _, spec := range factMetricSpecs {
		if fields&spec.field == 0 {
			continue
		}
		state := store.metrics[ownerID][spec.dataset]
		coverage := 0.0
		if total > 0 {
			coverage = float64(state.available) / float64(total)
		}
		age := time.Duration(0)
		if !state.newest.IsZero() {
			age = now.Sub(state.newest)
			if age < 0 {
				age = 0
			}
		}
		result = append(result, factMetricSample{owner: owner, dataset: spec.dataset, age: age, coverage: coverage})
	}
	return result
}

func (store *FactStore) observe(samples []factMetricSample) {
	if store.observer == nil {
		return
	}
	for _, sample := range samples {
		store.observer.SetMarketFact(string(sample.owner.Venue), sample.owner.NativeFamily, string(sample.dataset), sample.age, sample.coverage)
	}
}

func factOwnerID(owner OwnerKey) string {
	return string(owner.Venue) + "|" + string(owner.Environment) + "|" + owner.NativeFamily
}
