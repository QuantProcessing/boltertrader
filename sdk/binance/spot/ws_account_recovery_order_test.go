package spot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsAccountClientRecoveryCallbacksRemainGenerationOrdered(t *testing.T) {
	wsAPI := &WsAPIClient{
		Conn:      &websocket.Conn{},
		connEpoch: 1,
	}
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	client.mu.Lock()
	client.recovering = true
	client.recoveryEpoch = 1
	client.subscribed = true
	client.subscribedConnEpoch = 1
	client.mu.Unlock()

	events := make(chan string, 2)
	recoveredEntered := make(chan struct{})
	releaseRecovered := make(chan struct{})
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(releaseRecovered) }) })
	client.SetReconnectHooks(func(error) {
		events <- "started"
	}, func() {
		close(recoveredEntered)
		<-releaseRecovered
		events <- "recovered"
	})
	completeDone := make(chan struct{})
	go func() {
		client.completeReconnect(1)
		close(completeDone)
	}()
	select {
	case <-recoveredEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for recovered callback entry")
	}
	startedDone := make(chan struct{})
	go func() {
		client.handleDisconnect(errors.New("new disconnect during recovered callback"))
		close(startedDone)
	}()
	select {
	case event := <-events:
		t.Fatalf("new generation callback overtook blocked recovered callback: %q", event)
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(releaseRecovered) })
	for _, want := range []string{"recovered", "started"} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("callback order = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %q callback", want)
		}
	}
	select {
	case <-completeDone:
	case <-time.After(time.Second):
		t.Fatal("old recovery completion did not return")
	}
	select {
	case <-startedDone:
	case <-time.After(time.Second):
		t.Fatal("new recovery start did not return")
	}
	client.mu.Lock()
	recovering := client.recovering
	client.mu.Unlock()
	if !recovering {
		t.Fatal("new recovery generation was cleared by the old recovered callback")
	}
}

func TestWsAccountClientDisconnectBeforeCompletionCannotPublishRecovered(t *testing.T) {
	serverURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read subscription: %v", err)
			return
		}
		var request struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Errorf("decode subscription: %v", err)
			return
		}
		if err := conn.WriteJSON(map[string]any{"id": request.ID, "result": map[string]any{}}); err != nil {
			t.Errorf("write subscription response: %v", err)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	wsAPI := NewWsAPIClient(ctx).WithURL(serverURL + "/ws")
	defer wsAPI.Close()
	if err := wsAPI.Connect(); err != nil {
		t.Fatalf("connect ws-api: %v", err)
	}
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	client.mu.Lock()
	client.recovering = true
	client.mu.Unlock()
	var recovered atomic.Int64
	client.SetReconnectHooks(nil, func() { recovered.Add(1) })
	client.beforeRecoveryComplete = func() {
		client.handleDisconnect(errors.New("replacement disconnected before completion"))
	}

	client.restoreSubscription()
	client.mu.Lock()
	recovering := client.recovering
	subscribed := client.subscribed
	client.mu.Unlock()
	if recovered.Load() != 0 || !recovering || subscribed {
		t.Fatalf("stale completion published recovery: recovered=%d recovering=%t subscribed=%t", recovered.Load(), recovering, subscribed)
	}
}
