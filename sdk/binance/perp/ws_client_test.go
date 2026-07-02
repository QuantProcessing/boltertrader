package perp

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestPerpNewWSClientAndNewWsClientReturnCompatibleTypes(t *testing.T) {
	newClient := NewWSClient(context.Background(), "wss://example.com/ws")
	legacyClient := NewWsClient(context.Background(), "wss://example.com/ws")

	if newClient == nil {
		t.Fatal("NewWSClient returned nil")
	}
	if legacyClient == nil {
		t.Fatal("NewWsClient returned nil")
	}

	var canonical *WSClient = legacyClient
	var legacy *WsClient = newClient

	if reflect.TypeOf(canonical) != reflect.TypeOf(legacy) {
		t.Fatalf("expected compatible client types, got %T and %T", canonical, legacy)
	}
}

func TestPerpWsMarketClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := NewWsMarketClient(context.Background())

	if client.WsClient == nil {
		t.Fatal("expected legacy WsClient embedded field to be populated")
	}

	field, ok := reflect.TypeOf(client).Elem().FieldByName("WsClient")
	if !ok {
		t.Fatal("expected WsMarketClient to keep embedded field named WsClient")
	}
	if !field.Anonymous {
		t.Fatal("expected WsClient field to remain embedded")
	}
	if field.Type != reflect.TypeOf((*WSClient)(nil)) {
		t.Fatalf("expected embedded field type %v, got %v", reflect.TypeOf((*WSClient)(nil)), field.Type)
	}
}

func TestPerpWsAccountClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "api-secret")

	if client.WsClient == nil {
		t.Fatal("expected legacy WsClient embedded field to be populated")
	}

	field, ok := reflect.TypeOf(client).Elem().FieldByName("WsClient")
	if !ok {
		t.Fatal("expected WsAccountClient to keep embedded field named WsClient")
	}
	if !field.Anonymous {
		t.Fatal("expected WsClient field to remain embedded")
	}
	if field.Type != reflect.TypeOf((*WSClient)(nil)) {
		t.Fatalf("expected embedded field type %v, got %v", reflect.TypeOf((*WSClient)(nil)), field.Type)
	}
}

func TestWSClientDispatchBuffersWhilePausedAndDrainsInOrder(t *testing.T) {
	client := NewWSClient(context.Background(), "wss://example.com/ws")

	var got []string
	client.Handler = func(msg []byte) {
		got = append(got, string(msg))
	}

	client.PauseDispatch()
	client.dispatchMessage([]byte("one"))
	client.dispatchMessage([]byte("two"))
	if len(got) != 0 {
		t.Fatalf("paused dispatch delivered messages: %v", got)
	}

	client.ResumeDispatch(func() { got = append(got, "hook") })
	want := []string{"hook", "one", "two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestWSClientReconnectResubscribesOffline(t *testing.T) {
	var mu sync.Mutex
	connectionCount := 0
	firstSubscribe := make(chan struct{}, 1)
	replayedSubscribe := make(chan string, 1)

	wsURL := newPerpWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()

		mu.Lock()
		connectionCount++
		n := connectionCount
		mu.Unlock()

		_, msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if n == 1 {
			firstSubscribe <- struct{}{}
			return
		}
		replayedSubscribe <- string(msg)
		time.Sleep(25 * time.Millisecond)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWSClient(ctx, wsURL+"/ws")
	client.ReconnectWait = time.Millisecond
	client.pongInterval = time.Hour
	defer client.Close()

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := client.Subscribe("btcusdt@trade", func([]byte) error { return nil }); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case <-firstSubscribe:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for initial subscribe")
	}
	select {
	case msg := <-replayedSubscribe:
		if !strings.Contains(msg, "SUBSCRIBE") || !strings.Contains(msg, "btcusdt@trade") {
			t.Fatalf("replayed subscribe=%s", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for replayed subscribe after reconnect")
	}
}
