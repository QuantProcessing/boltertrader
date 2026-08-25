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
	hub    *markethub.Hub
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	active map[hotset.Key]context.CancelFunc
}

func NewHotActivator(hub *markethub.Hub) *HotActivator {
	ctx, cancel := context.WithCancel(context.Background())
	return &HotActivator{hub: hub, ctx: ctx, cancel: cancel, active: make(map[hotset.Key]context.CancelFunc)}
}

func (activator *HotActivator) SetActive(key hotset.Key, active bool) {
	if activator == nil || activator.hub == nil {
		return
	}
	activator.mu.Lock()
	if !active {
		if cancel := activator.active[key]; cancel != nil {
			cancel()
			delete(activator.active, key)
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
	activator.mu.Unlock()
	go activator.run(ctx, key)
}

func (activator *HotActivator) run(ctx context.Context, key hotset.Key) {
	var subscriptions []*markethub.Subscription
	defer func() {
		for _, subscription := range subscriptions {
			subscription.Close()
		}
	}()
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
		delete(activator.active, key)
	}
	activator.mu.Unlock()
}
