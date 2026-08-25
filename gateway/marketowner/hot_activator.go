package marketowner

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

// HotActivator materializes selected Hotset instruments as background Hub
// consumers. Hub reference counting guarantees these leases share one native
// topic with browser and execution consumers.
type HotActivator struct {
	hub         *markethub.Hub
	marks       BoardMarks
	derivatives interface {
		SetHot(hotset.Key, bool)
	}
	metrics interface {
		SetMarketHot(venue, family, dataset string, hot int)
	}
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active map[hotset.Key]context.CancelFunc
	counts map[markethub.OwnerKey]int
}

func NewHotActivator(hub *markethub.Hub, marks BoardMarks, derivatives interface {
	SetHot(hotset.Key, bool)
}, metrics interface {
	SetMarketHot(venue, family, dataset string, hot int)
}) *HotActivator {
	ctx, cancel := context.WithCancel(context.Background())
	return &HotActivator{hub: hub, marks: marks, derivatives: derivatives, metrics: metrics, ctx: ctx, cancel: cancel, active: make(map[hotset.Key]context.CancelFunc), counts: make(map[markethub.OwnerKey]int)}
}

func (activator *HotActivator) SetActive(key hotset.Key, active bool) {
	if activator == nil || activator.hub == nil {
		return
	}
	activator.mu.Lock()
	if !active {
		if cancel := activator.active[key]; cancel != nil {
			cancel()
			if activator.derivatives != nil {
				activator.derivatives.SetHot(key, false)
			}
			delete(activator.active, key)
			activator.counts[key.Owner]--
			activator.observeLocked(key.Owner)
		}
		activator.mu.Unlock()
		return
	}
	if activator.active[key] != nil {
		activator.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(activator.ctx)
	activator.active[key] = cancel
	if activator.derivatives != nil {
		activator.derivatives.SetHot(key, true)
	}
	activator.counts[key.Owner]++
	activator.observeLocked(key.Owner)
	activator.mu.Unlock()
	go activator.run(ctx, key)
}

func (activator *HotActivator) observeLocked(owner markethub.OwnerKey) {
	if activator.metrics == nil {
		return
	}
	count := activator.counts[owner]
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetBBO} {
		activator.metrics.SetMarketHot(string(owner.Venue), owner.NativeFamily, string(dataset), count)
	}
}

func (activator *HotActivator) run(ctx context.Context, key hotset.Key) {
	var subscriptions []*markethub.Subscription
	var reference exchange.Subscription[exchange.DerivativeReferenceEvent]
	defer func() {
		for _, subscription := range subscriptions {
			subscription.Close()
		}
		if reference != nil {
			_ = reference.Close()
		}
	}()
	if activator.hotReferenceSupported(key) {
		if stream, err := activator.marks.Markets.WatchDerivativeReference(ctx, key.InstrumentID); err == nil {
			reference = stream.Subscription
			activator.marks.PublishReference(ctx, key.InstrumentID, stream.Snapshot)
			go activator.drainReference(ctx, key.InstrumentID, reference)
		}
	}
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetBBO} {
		subscription, err := activator.hub.Subscribe(ctx, markethub.SubscriptionKey{Owner: key.Owner, Dataset: dataset, InstrumentID: key.InstrumentID}, 4)
		if err != nil {
			continue
		}
		subscriptions = append(subscriptions, subscription)
		go drainHotSubscription(ctx, subscription)
	}
	<-ctx.Done()
}

func (activator *HotActivator) hotReferenceSupported(key hotset.Key) bool {
	if activator == nil || activator.marks.Markets == nil || activator.marks.Capabilities == nil {
		return false
	}
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetFunding, exchange.DatasetOpenInterest} {
		capability, err := activator.marks.Capabilities.Resolve(key.Owner.Venue, key.Owner.NativeFamily, dataset, true)
		if err != nil || capability.Granularity != exchange.GranularitySingleInstrument || capability.Transport != exchange.TransportWebSocket {
			return false
		}
	}
	return true
}

func (activator *HotActivator) drainReference(ctx context.Context, instrumentID string, subscription exchange.Subscription[exchange.DerivativeReferenceEvent]) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-subscription.Events():
			if !open {
				return
			}
			activator.marks.PublishReference(ctx, instrumentID, event)
		case _, open := <-subscription.Errors():
			if !open {
				return
			}
			return
		}
	}
}

func drainHotSubscription(ctx context.Context, subscription *markethub.Subscription) {
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

func (activator *HotActivator) Close() {
	if activator == nil {
		return
	}
	activator.cancel()
	activator.mu.Lock()
	for key, cancel := range activator.active {
		cancel()
		if activator.derivatives != nil {
			activator.derivatives.SetHot(key, false)
		}
		delete(activator.active, key)
		activator.counts[key.Owner]--
		activator.observeLocked(key.Owner)
	}
	activator.mu.Unlock()
}
