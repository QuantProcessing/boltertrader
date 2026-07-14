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

func TestWSReplacementDataWaitsForRecoveredCallback(t *testing.T) {
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
		var subscribe wsRequest
		if err := conn.ReadJSON(&subscribe); err != nil {
			return
		}
		if attempt == 1 {
			if err := writeGateSubscribeACK(conn, subscribe, true, ""); err != nil {
				return
			}
			if err := conn.WriteJSON(map[string]any{"channel": ChannelSpotOrder, "event": "update", "seq": "old", "result": []any{}}); err != nil {
				return
			}
			<-allowFirstClose
			return
		}
		<-allowReplacement
		if err := conn.WriteJSON(map[string]any{"channel": ChannelSpotOrder, "event": "update", "seq": "replacement", "result": []any{}}); err != nil {
			return
		}
		replacementOnce.Do(func() { close(replacementSent) })
		if err := writeGateSubscribeACK(conn, subscribe, true, ""); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	events := make(chan string, 8)
	client.SetReconnectHooks(func(error) { events <- "started" }, func() { events <- "recovered" })
	if err := client.Subscribe(context.Background(), ChannelSpotOrder, []string{"BTC_USDT"}, func(payload json.RawMessage) {
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

	wantGateCallbackEvent(t, events, "old")
	releaseFirst.Do(func() { close(allowFirstClose) })
	wantGateCallbackEvent(t, events, "started")

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

func wantGateCallbackEvent(t *testing.T, events <-chan string, want string) {
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

func TestWSCallbackDispatcherIsBoundedAndStopDoesNotWaitForCallback(t *testing.T) {
	dispatcher := newGateWSCallbackDispatcher()
	conn := &websocket.Conn{}
	dispatcher.activateConnection(0, conn, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	if !dispatcher.enqueueData(conn, gateWSCallback{run: func() {
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
	for i := 0; i < gateWSCallbackQueueLimit-gateWSCallbackControlSlots; i++ {
		if !dispatcher.enqueueData(conn, gateWSCallback{run: func() {}}) {
			t.Fatalf("callback %d was rejected below the queue bound", i)
		}
	}
	if dispatcher.enqueueData(conn, gateWSCallback{run: func() {}}) {
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

func TestWSCallbackDispatcherDropsFailedReplacementAndAllowsReentrantClose(t *testing.T) {
	dispatcher := newGateWSCallbackDispatcher()
	oldConn := &websocket.Conn{}
	failedConn := &websocket.Conn{}
	currentConn := &websocket.Conn{}
	dispatcher.activateConnection(0, oldConn, false)
	dispatcher.beginGap(1, nil)
	dispatcher.activateConnection(1, failedConn, true)
	failedRan := make(chan struct{}, 1)
	if !dispatcher.enqueueData(failedConn, gateWSCallback{run: func() { failedRan <- struct{}{} }}) {
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

	client := MustNewWSClient(ProductSpot)
	callbackConn := &websocket.Conn{}
	client.callbackDispatcher.activateConnection(0, callbackConn, false)
	closed := make(chan struct{})
	if !client.callbackDispatcher.enqueueData(callbackConn, gateWSCallback{run: func() {
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

func TestWSCallbackOverflowClosesExactSocket(t *testing.T) {
	clientConn, peer := gateWSPair(t)
	client := MustNewWSClient(ProductSpot)
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
	client.handlers[wsKey(ChannelSpotOrder, nil)] = func(json.RawMessage) {
		enteredOnce.Do(func() { close(firstEntered) })
		<-release
	}
	client.mu.Unlock()
	client.callbackDispatcher.activateConnection(0, clientConn, false)
	go client.readLoop(clientConn)

	peerClosed := make(chan struct{})
	go func() {
		_, _, _ = peer.ReadMessage()
		close(peerClosed)
	}()
	if err := peer.WriteJSON(map[string]any{"channel": ChannelSpotOrder, "event": "update", "seq": "one", "result": []any{}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not enter")
	}
	for _, sequence := range []string{"two", "overflow"} {
		if err := peer.WriteJSON(map[string]any{"channel": ChannelSpotOrder, "event": "update", "seq": sequence, "result": []any{}}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("read-loop callback overflow did not close the exact websocket")
	}
}
