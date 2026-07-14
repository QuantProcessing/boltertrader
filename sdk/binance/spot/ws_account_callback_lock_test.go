package spot

import (
	"sync/atomic"
	"testing"
)

func TestWsAccountClientCallbacksRunWithoutClientLock(t *testing.T) {
	tests := []struct {
		name      string
		subscribe func(*WsAccountClient, func())
		handle    func(*WsAccountClient)
	}{
		{
			name: "execution report",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeExecutionReport(func(*ExecutionReportEvent) { callback() })
			},
			handle: func(client *WsAccountClient) {
				client.handleExecutionReport([]byte(`{}`))
			},
		},
		{
			name: "account position",
			subscribe: func(client *WsAccountClient, callback func()) {
				client.SubscribeAccountPosition(func(*AccountPositionEvent) { callback() })
			},
			handle: func(client *WsAccountClient) {
				client.handleAccountPosition([]byte(`{}`))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewWsAccountClient(&WsAPIClient{}, "api-key", "secret")
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

			test.handle(client)
			if !called.Load() {
				t.Fatal("callback was not invoked")
			}
			if lockHeld.Load() {
				t.Fatal("callback ran while the client mutex was held")
			}
		})
	}
}
