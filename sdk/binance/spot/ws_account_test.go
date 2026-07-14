package spot

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSAccountCompanion_NewWsAccountClient(t *testing.T) {
	client := NewWsAccountClient(NewWsAPIClient(context.Background()), "api-key", "secret")
	if client.wsAPI == nil || client.apiKey != "api-key" || client.secretKey != "secret" {
		t.Fatalf("unexpected account client: %+v", client)
	}
}

func TestWsAccountClientReconnectReplaysUserDataSubscriptionBeforeRecovery(t *testing.T) {
	type subscription struct {
		connection int64
		method     string
	}

	var connections atomic.Int64
	subscriptions := make(chan subscription, 2)
	releaseFirst := make(chan struct{})
	releaseSecondResponse := make(chan struct{})
	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		connection := connections.Add(1)

		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read subscription on connection %d: %v", connection, err)
			return
		}
		var request struct {
			ID     string `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Errorf("decode subscription on connection %d: %v", connection, err)
			return
		}
		subscriptions <- subscription{connection: connection, method: request.Method}

		if connection == 2 {
			<-releaseSecondResponse
		}
		if err := conn.WriteJSON(map[string]interface{}{"id": request.ID, "result": map[string]interface{}{}}); err != nil {
			t.Errorf("write subscription response on connection %d: %v", connection, err)
			return
		}
		if connection == 1 {
			<-releaseFirst
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
	wsAPI := NewWsAPIClient(ctx).WithURL(wsURL + "/ws")
	wsAPI.ReconnectWait = 10 * time.Millisecond
	defer wsAPI.Close()
	client := NewWsAccountClient(wsAPI, "api-key", "secret")
	reconnectPhases := make(chan string, 2)
	client.SetReconnectHooks(func(error) {
		reconnectPhases <- "started"
	}, func() {
		reconnectPhases <- "recovered"
	})

	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	select {
	case first := <-subscriptions:
		if first.connection != 1 || first.method != "userDataStream.subscribe.signature" {
			t.Fatalf("initial subscription = %+v", first)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial user-data subscription")
	}
	select {
	case phase := <-reconnectPhases:
		t.Fatalf("initial connect emitted reconnect phase %q", phase)
	default:
	}

	close(releaseFirst)
	select {
	case phase := <-reconnectPhases:
		if phase != "started" {
			t.Fatalf("first reconnect phase = %q, want started", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect start")
	}
	select {
	case replay := <-subscriptions:
		if replay.connection != 2 || replay.method != "userDataStream.subscribe.signature" {
			t.Fatalf("replayed subscription = %+v", replay)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for replayed user-data subscription")
	}
	select {
	case phase := <-reconnectPhases:
		t.Fatalf("recovery emitted before subscription response: %q", phase)
	default:
	}
	close(releaseSecondResponse)
	select {
	case phase := <-reconnectPhases:
		if phase != "recovered" {
			t.Fatalf("second reconnect phase = %q, want recovered", phase)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect recovery")
	}
}
