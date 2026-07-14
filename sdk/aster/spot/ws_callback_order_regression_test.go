package spot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWsReplacementDataWaitsForRecoveredCallback(t *testing.T) {
	t.Setenv("PROXY", "")
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
	server := newSpotWSServer(t, func(connection int, conn *websocket.Conn) {
		defer conn.Close()
		if connection == 1 {
			if err := conn.WriteMessage(websocket.TextMessage, []byte("old")); err != nil {
				t.Errorf("write old data: %v", err)
				return
			}
			<-allowFirstClose
			return
		}
		<-allowReplacement
		if err := conn.WriteMessage(websocket.TextMessage, []byte("replacement")); err != nil {
			t.Errorf("write replacement data: %v", err)
			return
		}
		replacementOnce.Do(func() { close(replacementSent) })
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer server.Close()

	client := newWSClient(context.Background(), websocketURL(server.URL))
	client.ReconnectWait = 200 * time.Millisecond
	events := make(chan string, 8)
	client.Handler = func(message []byte) { events <- string(message) }
	client.SetReconnectHooks(func(error) { events <- "started" }, func() { events <- "recovered" })
	if err := client.Connect(); err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	wantSpotCallbackEvent(t, events, "old")
	releaseFirst.Do(func() { close(allowFirstClose) })
	wantSpotCallbackEvent(t, events, "started")

	client.reconnectHookMu.Lock()
	locked := true
	t.Cleanup(func() {
		if locked {
			client.reconnectHookMu.Unlock()
		}
	})
	releaseReplacement.Do(func() { close(allowReplacement) })
	select {
	case <-replacementSent:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement connection did not send data")
	}

	var early string
	select {
	case early = <-events:
	case <-time.After(50 * time.Millisecond):
	}
	client.reconnectHookMu.Unlock()
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

func wantSpotCallbackEvent(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("callback = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for callback %q", want)
	}
}
