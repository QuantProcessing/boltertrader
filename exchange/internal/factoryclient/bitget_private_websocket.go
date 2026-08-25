package factoryclient

import (
	"context"
	"encoding/json"
	"sync"

	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

const accountWideNative = "*"

// bitgetPrivateTopicHub adapts Bitget's account-wide UTA private topics to the
// instrument-scoped exchange subscriptions exposed by this package. Bitget
// permits one subscription to each topic and identifies the instrument in each
// pushed row; subscribing once per catalog instrument either replaces the SDK
// handler or is rejected as a duplicate by the venue.
type bitgetPrivateTopicHub[T any] struct {
	mu         sync.Mutex
	ws         *bitget.PrivateWSClient
	arg        bitget.WSArg
	subscribed bool
	nextID     uint64
	callbacks  map[string]map[uint64]streamCallbacks[T]
}

func newBitgetPrivateTopicHub[T any](ws *bitget.PrivateWSClient, arg bitget.WSArg) *bitgetPrivateTopicHub[T] {
	return &bitgetPrivateTopicHub[T]{
		ws:        ws,
		arg:       arg,
		callbacks: make(map[string]map[uint64]streamCallbacks[T]),
	}
}

func (hub *bitgetPrivateTopicHub[T]) start(
	ctx context.Context,
	native string,
	callbacks streamCallbacks[T],
	handler func(json.RawMessage),
) (func() error, error) {
	hub.mu.Lock()
	if hub.callbacks == nil {
		hub.callbacks = make(map[string]map[uint64]streamCallbacks[T])
	}
	hub.nextID++
	id := hub.nextID
	if hub.callbacks[native] == nil {
		hub.callbacks[native] = make(map[uint64]streamCallbacks[T])
	}
	hub.callbacks[native][id] = callbacks
	if hub.subscribed {
		hub.mu.Unlock()
		return func() error { return hub.remove(native, id) }, nil
	}

	// Keep the hub locked through the acknowledgement. That makes the first
	// subscription transactional: a concurrent watcher cannot observe the
	// topic as live before Bitget has accepted it.
	err := hub.ws.Subscribe(ctx, hub.arg, handler)
	if err != nil {
		delete(hub.callbacks[native], id)
		if len(hub.callbacks[native]) == 0 {
			delete(hub.callbacks, native)
		}
		hub.mu.Unlock()
		return nil, err
	}
	hub.subscribed = true
	hub.mu.Unlock()
	return func() error { return hub.remove(native, id) }, nil
}

func (hub *bitgetPrivateTopicHub[T]) remove(native string, id uint64) error {
	hub.mu.Lock()
	if routes := hub.callbacks[native]; routes != nil {
		delete(routes, id)
		if len(routes) == 0 {
			delete(hub.callbacks, native)
		}
	}
	if len(hub.callbacks) != 0 || !hub.subscribed {
		hub.mu.Unlock()
		return nil
	}
	hub.subscribed = false
	err := hub.ws.Unsubscribe(context.Background(), hub.arg)
	hub.mu.Unlock()
	return err
}

func (hub *bitgetPrivateTopicHub[T]) reset() {
	hub.mu.Lock()
	hub.subscribed = false
	hub.callbacks = make(map[string]map[uint64]streamCallbacks[T])
	hub.mu.Unlock()
}

func (hub *bitgetPrivateTopicHub[T]) emit(native string, event T) {
	for _, callbacks := range hub.snapshot(native) {
		callbacks.Event(event)
	}
}

func (hub *bitgetPrivateTopicHub[T]) emitError(native string, err error) {
	for _, callbacks := range hub.snapshot(native) {
		callbacks.Error(err)
	}
}

func (hub *bitgetPrivateTopicHub[T]) emitAllError(err error) {
	for _, callbacks := range hub.snapshotAll() {
		callbacks.Error(err)
	}
}

func (hub *bitgetPrivateTopicHub[T]) snapshot(native string) []streamCallbacks[T] {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	routes := hub.callbacks[native]
	accountRoutes := hub.callbacks[accountWideNative]
	result := make([]streamCallbacks[T], 0, len(routes)+len(accountRoutes))
	for _, callbacks := range routes {
		result = append(result, callbacks)
	}
	if native != accountWideNative {
		for _, callbacks := range accountRoutes {
			result = append(result, callbacks)
		}
	}
	return result
}

func (hub *bitgetPrivateTopicHub[T]) snapshotAll() []streamCallbacks[T] {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var result []streamCallbacks[T]
	for _, routes := range hub.callbacks {
		for _, callbacks := range routes {
			result = append(result, callbacks)
		}
	}
	return result
}
