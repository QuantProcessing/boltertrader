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
	mu       sync.RWMutex
	facts    map[string]exchange.DerivativeFact
	owners   map[string]OwnerKey
	sequence uint64
	nextSub  uint64
	subs     map[uint64]chan FactChange
	observer Observer
	now      func() time.Time
}

type FactChange struct {
	Sequence uint64                   `json:"sequence"`
	Owner    OwnerKey                 `json:"owner"`
	Fields   exchange.DerivativeField `json:"fields"`
}

func NewFactStore(observer Observer, now func() time.Time) *FactStore {
	if now == nil {
		now = time.Now
	}
	return &FactStore{
		facts: make(map[string]exchange.DerivativeFact), owners: make(map[string]OwnerKey),
		subs: make(map[uint64]chan FactChange), observer: observer, now: now,
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
	store.mu.Lock()
	if patch.Kind == exchange.EventSnapshot && patch.Fields.Has(exchange.DerivativeFieldIdentity) {
		seen := make(map[string]struct{}, len(patch.Facts))
		for _, fact := range patch.Facts {
			seen[fact.InstrumentID] = struct{}{}
		}
		for instrumentID, currentOwner := range store.owners {
			if factOwnerID(currentOwner) != ownerID {
				continue
			}
			if patch.Scope.Settlement != "" && store.facts[instrumentID].Instrument.SettleAsset != patch.Scope.Settlement {
				continue
			}
			if _, present := seen[instrumentID]; !present {
				delete(store.facts, instrumentID)
				delete(store.owners, instrumentID)
			}
		}
	}
	for _, incoming := range patch.Facts {
		normalizeDerivativeClocks(&incoming, patch.Fields, patch.ObservedAt)
		current := store.facts[incoming.InstrumentID]
		current = exchange.ApplyDerivativePatch(current, incoming, patch.Fields)
		store.facts[incoming.InstrumentID] = current
		store.owners[incoming.InstrumentID] = owner
	}
	store.sequence++
	change := FactChange{Sequence: store.sequence, Owner: owner, Fields: patch.Fields}
	for id, updates := range store.subs {
		select {
		case updates <- change:
		default:
			close(updates)
			delete(store.subs, id)
		}
	}
	store.observeLocked(owner, patch.Fields)
	store.mu.Unlock()
	return nil
}

func normalizeDerivativeClocks(fact *exchange.DerivativeFact, fields exchange.DerivativeField, observedAt time.Time) {
	observedAt = observedAt.UTC()
	if fields.Has(exchange.DerivativeFieldFunding) || fields.Has(exchange.DerivativeFieldFundingInterval) || fields.Has(exchange.DerivativeFieldNextFundingTime) {
		if fact.FundingObservedAt.IsZero() {
			fact.FundingObservedAt = observedAt
		} else {
			fact.FundingObservedAt = fact.FundingObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldMark) {
		if fact.MarkObservedAt.IsZero() {
			fact.MarkObservedAt = observedAt
		} else {
			fact.MarkObservedAt = fact.MarkObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldIndex) {
		if fact.IndexObservedAt.IsZero() {
			fact.IndexObservedAt = observedAt
		} else {
			fact.IndexObservedAt = fact.IndexObservedAt.UTC()
		}
	}
	if fields.Has(exchange.DerivativeFieldOpenInterest) || fields.Has(exchange.DerivativeFieldOpenInterestNotional) {
		if fact.OpenInterestObservedAt.IsZero() {
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

func (store *FactStore) Subscribe(ctx context.Context, buffer int) (<-chan FactChange, func(), error) {
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
	store.subs[id] = updates
	store.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			store.mu.Lock()
			if current, ok := store.subs[id]; ok {
				delete(store.subs, id)
				close(current)
			}
			store.mu.Unlock()
		})
	}
	go func() { <-ctx.Done(); cancel() }()
	return updates, cancel, nil
}

func (store *FactStore) observeLocked(owner OwnerKey, fields exchange.DerivativeField) {
	if store.observer == nil {
		return
	}
	now := store.now().UTC()
	for _, item := range []struct {
		field   exchange.DerivativeField
		dataset exchange.MarketDataset
		at      func(exchange.DerivativeFact) time.Time
		valid   func(exchange.DerivativeFact) bool
	}{
		{exchange.DerivativeFieldFunding, exchange.DatasetFunding, func(f exchange.DerivativeFact) time.Time { return f.FundingObservedAt }, func(f exchange.DerivativeFact) bool { return f.FundingRate.Valid }},
		{exchange.DerivativeFieldMark, exchange.DatasetMark, func(f exchange.DerivativeFact) time.Time { return f.MarkObservedAt }, func(f exchange.DerivativeFact) bool { return f.MarkPrice.Valid }},
		{exchange.DerivativeFieldIndex, exchange.DatasetIndex, func(f exchange.DerivativeFact) time.Time { return f.IndexObservedAt }, func(f exchange.DerivativeFact) bool { return f.IndexPrice.Valid }},
		{exchange.DerivativeFieldOpenInterest | exchange.DerivativeFieldOpenInterestNotional, exchange.DatasetOpenInterest, func(f exchange.DerivativeFact) time.Time { return f.OpenInterestObservedAt }, func(f exchange.DerivativeFact) bool { return f.OpenInterest.Valid || f.OpenInterestNotional.Valid }},
	} {
		if fields&item.field == 0 {
			continue
		}
		var total, available int
		var newest time.Time
		for instrumentID, factOwner := range store.owners {
			if factOwner != owner {
				continue
			}
			total++
			fact := store.facts[instrumentID]
			if item.valid(fact) {
				available++
			}
			if at := item.at(fact); at.After(newest) {
				newest = at
			}
		}
		coverage := 0.0
		if total > 0 {
			coverage = float64(available) / float64(total)
		}
		age := time.Duration(0)
		if !newest.IsZero() {
			age = now.Sub(newest)
			if age < 0 {
				age = 0
			}
		}
		store.observer.SetMarketFact(string(owner.Venue), owner.NativeFamily, string(item.dataset), age, coverage, 0)
	}
}

func factOwnerID(owner OwnerKey) string {
	return string(owner.Venue) + "|" + string(owner.Environment) + "|" + owner.NativeFamily
}
