package perp

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestWsAccountClientCallbacksRunWithoutClientLock(t *testing.T) {
	tests := []struct {
		name      string
		subscribe func(*WsAccountClient, func())
		handle    func(*WsAccountClient) error
	}{
		{
			name: "account update",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeAccountUpdate(func(*AccountUpdateEvent) { callback() })
			},
			handle: func(client *WsAccountClient) error { return client.handleAccountUpdate([]byte(`{}`)) },
		},
		{
			name: "order update",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeOrderUpdate(func(*OrderUpdateEvent) { callback() })
			},
			handle: func(client *WsAccountClient) error { return client.handleOrderUpdate([]byte(`{}`)) },
		},
		{
			name: "algo update",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeAlgoUpdate(func(*AlgoUpdateEvent) { callback() })
			},
			handle: func(client *WsAccountClient) error { return client.handleAlgoUpdate([]byte(`{}`)) },
		},
		{
			name: "account config update",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeAccountConfigUpdate(func(*AccountConfigUpdateEvent) { callback() })
			},
			handle: func(client *WsAccountClient) error {
				return client.handleAccountConfigUpdate([]byte(`{}`))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewWsAccountClient(context.Background(), "api-key", "secret")
			var called atomic.Bool
			var lockHeld atomic.Bool
			test.subscribe(client, func() {
				called.Store(true)
				if !client.mu.TryLock() {
					lockHeld.Store(true)
					return
				}
				client.mu.Unlock()
				client.SetReconnectHooks(nil, nil)
			})

			if err := test.handle(client); err != nil {
				t.Fatalf("handle event: %v", err)
			}
			if !called.Load() {
				t.Fatal("callback was not invoked")
			}
			if lockHeld.Load() {
				t.Fatal("callback ran while the client mutex was held")
			}
		})
	}
}
