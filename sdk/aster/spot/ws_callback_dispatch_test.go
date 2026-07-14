package spot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsCallbackOverflowClosesExactSocket(t *testing.T) {
	firstEntered := make(chan struct{})
	peerClosed := make(chan struct{}, 1)
	server := newSpotWSServer(t, func(_ int, conn *websocket.Conn) {
		defer conn.Close()
		if err := conn.WriteMessage(websocket.TextMessage, []byte("one")); err != nil {
			return
		}
		<-firstEntered
		for _, message := range []string{"two", "overflow"} {
			if err := conn.WriteMessage(websocket.TextMessage, []byte(message)); err != nil {
				return
			}
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			peerClosed <- struct{}{}
		}
	})
	defer server.Close()

	client := newWSClient(context.Background(), websocketURL(server.URL))
	client.ReconnectWait = time.Hour
	client.callbackDispatcher.limit = 3
	release := make(chan struct{})
	var enteredOnce sync.Once
	client.Handler = func([]byte) {
		enteredOnce.Do(func() { close(firstEntered) })
		<-release
	}
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer close(release)

	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first callback did not enter")
	}
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
		t.Fatal("overflow did not close the exact websocket")
	}
}

func TestWsCallbackDispatcherIsBoundedAndStopDoesNotWaitForCallback(t *testing.T) {
	dispatcher := newWSCallbackDispatcher()
	conn := &websocket.Conn{}
	dispatcher.activateConnection(0, conn, false)
	entered := make(chan struct{})
	release := make(chan struct{})
	if !dispatcher.enqueueData(conn, wsCallback{run: func() {
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
	for i := 0; i < asterWSCallbackQueueLimit-asterWSCallbackControlSlots; i++ {
		if !dispatcher.enqueueData(conn, wsCallback{run: func() {}}) {
			t.Fatalf("callback %d was rejected below the queue bound", i)
		}
	}
	if dispatcher.enqueueData(conn, wsCallback{run: func() {}}) {
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

func TestWsCallbackDispatcherDropsFailedReplacementAndAllowsReentrantClose(t *testing.T) {
	dispatcher := newWSCallbackDispatcher()
	oldConn := &websocket.Conn{}
	failedConn := &websocket.Conn{}
	currentConn := &websocket.Conn{}
	dispatcher.activateConnection(0, oldConn, false)
	dispatcher.beginGap(1, nil)
	dispatcher.activateConnection(1, failedConn, true)
	failedRan := make(chan struct{}, 1)
	if !dispatcher.enqueueData(failedConn, wsCallback{run: func() { failedRan <- struct{}{} }}) {
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

	client := newWSClient(context.Background(), "ws://example.invalid")
	callbackConn := &websocket.Conn{}
	client.callbackDispatcher.activateConnection(0, callbackConn, false)
	closed := make(chan struct{})
	if !client.callbackDispatcher.enqueueData(callbackConn, wsCallback{run: func() {
		client.Close()
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
