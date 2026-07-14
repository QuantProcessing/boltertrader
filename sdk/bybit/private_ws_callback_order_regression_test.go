package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPrivateWSReplacementDataWaitsForRecoveredCallback(t *testing.T) {
	var dials atomic.Int32
	allowFirstClose := make(chan struct{})
	allowReplacement := make(chan struct{})
	replacementSent := make(chan struct{})
	var releaseFirst sync.Once
	var releaseReplacement sync.Once
	var replacementOnce sync.Once
	t.Cleanup(func() {
		releaseFirst.Do(func() { close(allowFirstClose) })
		releaseReplacement.Do(func() { close(allowReplacement) })
	})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		attempt := dials.Add(1)
		var auth wsAuthRequest
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(map[string]any{"op": "auth", "success": true, "ret_msg": "OK"}); err != nil {
			return
		}
		var subscribe wsCommandRequest
		if err := conn.ReadJSON(&subscribe); err != nil {
			return
		}
		if attempt == 1 {
			if err := writeBybitSubscribeACK(conn, subscribe, true, "OK"); err != nil {
				return
			}
			if err := conn.WriteJSON(map[string]any{"topic": "order", "seq": "old", "data": []any{}}); err != nil {
				return
			}
			<-allowFirstClose
			return
		}
		<-allowReplacement
		if err := conn.WriteJSON(map[string]any{"topic": "order", "seq": "replacement", "data": []any{}}); err != nil {
			return
		}
		replacementOnce.Do(func() { close(replacementSent) })
		if err := writeBybitSubscribeACK(conn, subscribe, true, "OK"); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	events := make(chan string, 8)
	client.SetReconnectHooks(func(error) { events <- "started" }, func() { events <- "recovered" })
	if err := client.Subscribe(context.Background(), "order", func(payload json.RawMessage) {
		var message struct {
			Sequence string `json:"seq"`
		}
		if json.Unmarshal(payload, &message) == nil {
			events <- message.Sequence
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	wantBybitCallbackEvent(t, events, "old")
	releaseFirst.Do(func() { close(allowFirstClose) })
	wantBybitCallbackEvent(t, events, "started")

	client.hookMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			client.hookMu.Unlock()
		}
	})
	releaseReplacement.Do(func() { close(allowReplacement) })
	select {
	case <-replacementSent:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement connection did not send data")
	}
	var early string
	select {
	case early = <-events:
	case <-time.After(50 * time.Millisecond):
	}
	client.hookMu.Unlock()
	locked = false

	got := []string{"old", "started"}
	if early != "" {
		got = append(got, early)
	}
	for len(got) < 4 {
		select {
		case event := <-events:
			got = append(got, event)
		case <-time.After(2 * time.Second):
			t.Fatalf("callback order = %v, want [old started recovered replacement]", got)
		}
	}
	want := []string{"old", "started", "recovered", "replacement"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("callback order = %v, want %v", got, want)
		}
	}
}

func wantBybitCallbackEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("callback = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for callback %q", want)
	}
}

func TestPrivateWSCallbackDispatcherIsBoundedAndStopDoesNotWaitForCallback(t *testing.T) {
	dispatcher := newPrivateWSCallbackDispatcher()
	conn := &websocket.Conn{}
	dispatcher.activateConnection(0, conn, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	if !dispatcher.enqueueData(conn, privateWSCallback{run: func() {
		close(entered)
		<-release
	}}) {
		t.Fatal("first callback was rejected")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not start")
	}
	for i := 0; i < privateWSCallbackQueueLimit-privateWSCallbackControlSlots; i++ {
		if !dispatcher.enqueueData(conn, privateWSCallback{run: func() {}}) {
			t.Fatalf("callback %d was rejected below the queue bound", i)
		}
	}
	if dispatcher.enqueueData(conn, privateWSCallback{run: func() {}}) {
		t.Fatal("callback queue accepted data beyond its bound")
	}
	stopped := make(chan struct{})
	go func() {
		dispatcher.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("dispatcher stop waited for the in-flight callback")
	}
	close(release)
}

func TestPrivateWSCallbackDispatcherDropsFailedReplacementAndAllowsReentrantClose(t *testing.T) {
	dispatcher := newPrivateWSCallbackDispatcher()
	oldConn := &websocket.Conn{}
	failedConn := &websocket.Conn{}
	currentConn := &websocket.Conn{}
	dispatcher.activateConnection(0, oldConn, false)
	dispatcher.beginGap(1, nil)
	dispatcher.activateConnection(1, failedConn, true)
	failedRan := make(chan struct{}, 1)
	if !dispatcher.enqueueData(failedConn, privateWSCallback{run: func() { failedRan <- struct{}{} }}) {
		t.Fatal("failed replacement data was rejected before discard")
	}
	dispatcher.discardReplacement(1, failedConn)
	dispatcher.activateConnection(1, currentConn, true)
	recovered := make(chan struct{})
	if !dispatcher.enqueueRecovered(1, currentConn, func() { close(recovered) }) {
		t.Fatal("current replacement was not accepted")
	}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("current replacement did not recover")
	}
	select {
	case <-failedRan:
		t.Fatal("failed replacement data crossed into the recovered generation")
	case <-time.After(25 * time.Millisecond):
	}
	dispatcher.stop()

	client := NewPrivateWSClient()
	callbackConn := &websocket.Conn{}
	client.callbackDispatcher.activateConnection(0, callbackConn, false)
	closed := make(chan struct{})
	if !client.callbackDispatcher.enqueueData(callbackConn, privateWSCallback{run: func() {
		_ = client.Close()
		close(closed)
	}}) {
		t.Fatal("reentrant close callback was rejected")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked in a callback")
	}
}

func TestPrivateWSPingLoopStopsImmediatelyOnClose(t *testing.T) {
	client := NewPrivateWSClient()
	done := make(chan struct{})
	go func() {
		client.pingLoop(&websocket.Conn{})
		close(done)
	}()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("ping loop remained alive after Close")
	}
}

func TestPrivateWSCallbackOverflowClosesExactSocket(t *testing.T) {
	clientConn, peer := bybitPrivateWSPair(t)
	client := NewPrivateWSClient()
	client.callbackDispatcher.limit = 3
	firstEntered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		_ = client.Close()
	})
	client.mu.Lock()
	client.conn = clientConn
	client.closed = true
	client.authenticated = false
	client.handlers["order"] = func(json.RawMessage) {
		enteredOnce.Do(func() { close(firstEntered) })
		<-release
	}
	client.mu.Unlock()
	client.callbackDispatcher.activateConnection(0, clientConn, false)
	go client.readLoop(clientConn, make(chan error, 1))

	peerClosed := make(chan struct{})
	go func() {
		_, _, _ = peer.ReadMessage()
		close(peerClosed)
	}()
	if err := peer.WriteJSON(map[string]any{"topic": "order", "seq": "one", "data": []any{}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not enter")
	}
	for _, sequence := range []string{"two", "overflow"} {
		if err := peer.WriteJSON(map[string]any{"topic": "order", "seq": sequence, "data": []any{}}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("read-loop callback overflow did not close the exact websocket")
	}
}
