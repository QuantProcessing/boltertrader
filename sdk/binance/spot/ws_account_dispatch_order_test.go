package spot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsAccountClientReplacementPrivateDataWaitsForRecovered(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	var got []string
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) {
		got = append(got, fmt.Sprintf("data-%d", event.OrderID))
	})
	client.SetReconnectHooks(func(error) {
		got = append(got, "started")
	}, func() {
		got = append(got, "recovered")
	})

	wsAPI.handleMessage(spotExecutionReportForDispatchTest(1))
	client.handleDisconnect(errors.New("connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))

	client.mu.Lock()
	epoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()
	if !client.completeReconnect(epoch) {
		t.Fatal("recovery completion was rejected")
	}

	want := []string{"data-1", "started", "recovered", "data-2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback order = %v, want %v", got, want)
	}
}

func TestWsAccountClientFailedReplacementDropsBufferedPrivateData(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	var got []string
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) {
		got = append(got, fmt.Sprintf("data-%d", event.OrderID))
	})
	client.SetReconnectHooks(func(error) {
		got = append(got, "started")
	}, func() {
		got = append(got, "recovered")
	})

	client.handleDisconnect(errors.New("first connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))
	client.handleDisconnect(errors.New("replacement connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(3))

	client.mu.Lock()
	epoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()
	if !client.completeReconnect(epoch) {
		t.Fatal("latest recovery completion was rejected")
	}

	want := []string{"started", "recovered", "data-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback order = %v, want %v", got, want)
	}
}

func TestWsAccountClientCloseDropsBufferedPrivateDataAndUnblocksDispatch(t *testing.T) {
	wsAPI := NewWsAPIClient(context.Background())
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	var got []int64
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) {
		got = append(got, event.OrderID)
	})

	client.handleDisconnect(errors.New("connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))
	client.Close()
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(3))

	want := []int64{3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivered order ids = %v, want %v", got, want)
	}
}

func TestWsAccountClientReconnectStartedHookCanClose(t *testing.T) {
	wsAPI := NewWsAPIClient(context.Background())
	client := NewWsAccountClient(wsAPI, "api-key", "secret")

	hookReturned := make(chan struct{})
	client.SetReconnectHooks(func(error) {
		client.Close()
		close(hookReturned)
	}, nil)
	disconnectReturned := make(chan struct{})
	go func() {
		client.handleDisconnect(errors.New("connection lost"))
		close(disconnectReturned)
	}()

	waitSpotDispatchSignal(t, hookReturned, "Started hook to return from Close")
	waitSpotDispatchSignal(t, disconnectReturned, "disconnect handler to return")

	delivered := make(chan struct{}, 1)
	wsAPI.SetEventHandler(func([]byte) { delivered <- struct{}{} })
	wsAPI.handleMessage([]byte(`{"e":"executionReport"}`))
	waitSpotDispatchSignal(t, delivered, "dispatch to remain unpaused after Close")
}

func TestWsAccountClientReconnectRecoveredHookCanClose(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	delivered := make(chan int64, 2)
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) {
		delivered <- event.OrderID
	})
	client.handleDisconnect(errors.New("connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))

	hookReturned := make(chan struct{})
	client.SetReconnectHooks(nil, func() {
		client.Close()
		close(hookReturned)
	})
	client.mu.Lock()
	epoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()
	completionReturned := make(chan struct{})
	go func() {
		client.completeReconnect(epoch)
		close(completionReturned)
	}()

	waitSpotDispatchSignal(t, hookReturned, "Recovered hook to return from Close")
	waitSpotDispatchSignal(t, completionReturned, "recovery completion to return")

	wsAPI.handleMessage(spotExecutionReportForDispatchTest(3))
	select {
	case got := <-delivered:
		if got != 3 {
			t.Fatalf("delivered order id = %d, want 3; buffered replacement was not reset", got)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatch remained paused after Recovered hook called Close")
	}
}

func TestWsAccountClientBufferedPrivateCallbackCanClose(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	callbackReturned := make(chan struct{})
	client.SubscribeExecutionReport(func(*ExecutionReportEvent) {
		client.Close()
		close(callbackReturned)
	})
	client.handleDisconnect(errors.New("connection lost"))
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))
	client.mu.Lock()
	epoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()
	completionReturned := make(chan struct{})
	go func() {
		client.completeReconnect(epoch)
		close(completionReturned)
	}()

	waitSpotDispatchSignal(t, callbackReturned, "buffered private callback to return from Close")
	waitSpotDispatchSignal(t, completionReturned, "recovery drain to return")
}

func TestWsAccountClientCloseInvalidatesPendingRecoveredAndUnpausesDispatch(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	client.handleDisconnect(errors.New("connection lost"))
	client.mu.Lock()
	epoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()

	recovered := false
	client.SetReconnectHooks(nil, func() { recovered = true })
	client.Close()
	if client.completeReconnect(epoch) {
		t.Fatal("recovery completed after Close invalidated its generation")
	}
	if recovered {
		t.Fatal("Close allowed stale Recovered callback")
	}

	delivered := make(chan struct{}, 1)
	wsAPI.SetEventHandler(func([]byte) { delivered <- struct{}{} })
	wsAPI.handleMessage([]byte(`{"e":"executionReport"}`))
	waitSpotDispatchSignal(t, delivered, "dispatch to remain unpaused after stale completion")
}

func TestWsAccountClientStaleCompletionKeepsNewerRecoveryPaused(t *testing.T) {
	wsAPI := connectedSpotWSAPIForDispatchTest(t)
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	wsAPI.SetEventHandler(client.handlePushedEvent)

	var got []string
	client.SubscribeExecutionReport(func(event *ExecutionReportEvent) {
		got = append(got, fmt.Sprintf("data-%d", event.OrderID))
	})
	client.SetReconnectHooks(func(error) {
		got = append(got, "started")
	}, func() {
		got = append(got, "recovered")
	})

	client.handleDisconnect(errors.New("first connection lost"))
	client.mu.Lock()
	staleEpoch := client.recoveryEpoch
	client.mu.Unlock()
	client.handleDisconnect(errors.New("replacement connection lost"))
	client.mu.Lock()
	currentEpoch := client.recoveryEpoch
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()

	wsAPI.handleMessage(spotExecutionReportForDispatchTest(2))
	if client.completeReconnect(staleEpoch) {
		t.Fatal("stale recovery generation unexpectedly completed")
	}
	wsAPI.handleMessage(spotExecutionReportForDispatchTest(3))
	if len(got) != 1 || got[0] != "started" {
		t.Fatalf("newer recovery barrier was released early: %v", got)
	}
	if !client.completeReconnect(currentEpoch) {
		t.Fatal("current recovery generation was rejected")
	}

	want := []string{"started", "recovered", "data-2", "data-3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("callback order = %v, want %v", got, want)
	}
}

func connectedSpotWSAPIForDispatchTest(t *testing.T) *WsAPIClient {
	t.Helper()
	wsAPI := NewWsAPIClient(context.Background())
	wsAPI.Conn = &websocket.Conn{}
	wsAPI.connEpoch = 1
	return wsAPI
}

func spotExecutionReportForDispatchTest(orderID int64) []byte {
	return []byte(fmt.Sprintf(`{"subscriptionId":0,"event":{"e":"executionReport","i":%d}}`, orderID))
}

func waitSpotDispatchSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
