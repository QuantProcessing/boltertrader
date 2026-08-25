package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
)

type arbitrageProjectionSnapshot struct {
	Items      []arbitrageOpportunity
	ObservedAt time.Time
	Generation uint64
	Sequence   uint64
}

type arbitrageHotSubscription struct {
	subscription realtimewire.Subscription
	done         chan struct{}
}

// ArbitrageProjection is resident for the Trading process lifetime. Browser
// subscribers consume its memory projection and therefore cannot control
// scanning cadence or the Top-100 hotset.
type ArbitrageProjection struct {
	catalog CatalogProjection
	board   FundingBoardProjection
	market  MarketTopicProjection
	now     func() time.Time

	mu         sync.Mutex
	current    arbitrageProjectionSnapshot
	encoded    string
	nextSub    uint64
	subs       map[uint64]chan arbitrageProjectionSnapshot
	run        bool
	generation uint64
}

func NewArbitrageProjection(catalog CatalogProjection, board FundingBoardProjection, market MarketTopicProjection, now func() time.Time) (*ArbitrageProjection, error) {
	if catalog == nil || board == nil || now == nil {
		return nil, fmt.Errorf("arbitrage projection dependencies are incomplete")
	}
	return &ArbitrageProjection{catalog: catalog, board: board, market: market, now: now, subs: make(map[uint64]chan arbitrageProjectionSnapshot), generation: 1}, nil
}

func (projection *ArbitrageProjection) Run(ctx context.Context) {
	if projection == nil || ctx == nil {
		return
	}
	projection.mu.Lock()
	if projection.run {
		projection.mu.Unlock()
		<-ctx.Done()
		return
	}
	projection.run = true
	projection.mu.Unlock()

	hot := make(map[string]arbitrageHotSubscription)
	defer func() {
		for _, current := range hot {
			current.subscription.Close()
		}
		projection.mu.Lock()
		for id, updates := range projection.subs {
			close(updates)
			delete(projection.subs, id)
		}
		projection.run = false
		projection.mu.Unlock()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		projection.refresh(ctx, hot)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (projection *ArbitrageProjection) refresh(ctx context.Context, hot map[string]arbitrageHotSubscription) {
	board, err := projection.board.Get(ctx)
	if err != nil {
		return
	}
	catalogSnapshot, err := projection.catalog.Snapshot(ctx)
	if err != nil {
		return
	}
	items := scanArbitrageOpportunities(catalogSnapshot, board, map[string]string{})
	projection.syncHot(ctx, hot, items)
	encoded, err := json.Marshal(struct {
		Items      []arbitrageOpportunity `json:"items"`
		ObservedAt time.Time              `json:"observedAt"`
	}{Items: items, ObservedAt: board.ObservedAt})
	if err != nil {
		return
	}
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if string(encoded) == projection.encoded {
		return
	}
	projection.current = arbitrageProjectionSnapshot{Items: append([]arbitrageOpportunity(nil), items...), ObservedAt: board.ObservedAt, Generation: projection.generation, Sequence: projection.current.Sequence + 1}
	projection.encoded = string(encoded)
	for id, updates := range projection.subs {
		select {
		case updates <- projection.current:
		default:
			close(updates)
			delete(projection.subs, id)
		}
	}
}

func (projection *ArbitrageProjection) syncHot(ctx context.Context, hot map[string]arbitrageHotSubscription, items []arbitrageOpportunity) {
	desired := make(map[string]struct{})
	limit := min(100, len(items))
	for _, item := range items[:limit] {
		for _, instrumentID := range []string{item.Long.InstrumentID, item.Short.InstrumentID} {
			if strings.TrimSpace(instrumentID) != "" {
				desired[instrumentID] = struct{}{}
			}
		}
	}
	for instrumentID, current := range hot {
		_, wanted := desired[instrumentID]
		select {
		case <-current.done:
			wanted = false
		default:
		}
		if !wanted {
			current.subscription.Close()
			delete(hot, instrumentID)
		}
	}
	if projection.market == nil {
		return
	}
	ordered := make([]string, 0, len(desired))
	for instrumentID := range desired {
		ordered = append(ordered, instrumentID)
	}
	sort.Strings(ordered)
	for _, instrumentID := range ordered {
		if _, exists := hot[instrumentID]; exists {
			continue
		}
		subscription, err := projection.market.Subscribe(ctx, realtimewire.TopicRequest{Topic: realtimewire.TopicMarkPrice, InstrumentID: instrumentID, InternalPurpose: "arbitrage_top"}, 4)
		if err != nil {
			continue
		}
		done := make(chan struct{})
		hot[instrumentID] = arbitrageHotSubscription{subscription: subscription, done: done}
		go drainArbitrageHot(ctx, subscription, done)
	}
}

func drainArbitrageHot(ctx context.Context, subscription realtimewire.Subscription, done chan<- struct{}) {
	defer close(done)
	for {
		select {
		case <-ctx.Done():
			return
		case _, open := <-subscription.Events():
			if !open {
				return
			}
		}
	}
}

func (projection *ArbitrageProjection) Subscribe(ctx context.Context, buffer int) (arbitrageProjectionSnapshot, bool, <-chan arbitrageProjectionSnapshot, func(), error) {
	if projection == nil || ctx == nil || ctx.Err() != nil {
		return arbitrageProjectionSnapshot{}, false, nil, nil, fmt.Errorf("arbitrage projection unavailable")
	}
	if buffer <= 0 {
		buffer = 1
	}
	projection.mu.Lock()
	projection.nextSub++
	id := projection.nextSub
	updates := make(chan arbitrageProjectionSnapshot, buffer)
	current := projection.current
	ready := current.Sequence > 0
	projection.subs[id] = updates
	projection.mu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			projection.mu.Lock()
			if active, exists := projection.subs[id]; exists {
				delete(projection.subs, id)
				close(active)
			}
			projection.mu.Unlock()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		}
	}()
	return current, ready, updates, cancel, nil
}
