package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientKeepAliveDoesNotWriteReplacementConnection(t *testing.T) {
	t.Setenv("PROXY", "")

	var connectionID atomic.Int64
	accepted := make(chan *websocket.Conn, 2)
	pongs := make(chan int64, 16)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		id := connectionID.Add(1)
		conn.SetPongHandler(func(string) error {
			pongs <- id
			return nil
		})
		accepted <- conn
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	parentCtx, cancel := context.WithCancel(context.Background())
	client := NewWSClient(parentCtx, wsURL)
	client.pongInterval = 5 * time.Millisecond
	if err := client.Connect(); err != nil {
		cancel()
		t.Fatalf("Connect: %v", err)
	}
	originalConn := client.Conn
	var replacementConn *websocket.Conn
	var firstServerConn, secondServerConn *websocket.Conn
	defer func() {
		client.Mu.Lock()
		client.isClosed = true
		client.Conn = nil
		client.Mu.Unlock()
		cancel()
		if originalConn != nil {
			_ = originalConn.Close()
		}
		if replacementConn != nil {
			_ = replacementConn.Close()
		}
		if firstServerConn != nil {
			_ = firstServerConn.Close()
		}
		if secondServerConn != nil {
			_ = secondServerConn.Close()
		}
		client.wg.Wait()
	}()
	firstServerConn = <-accepted
	select {
	case id := <-pongs:
		if id != 1 {
			t.Fatalf("initial pong reached connection %d, want 1", id)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial keepalive pong")
	}

	replacementConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		cancel()
		t.Fatalf("dial replacement: %v", err)
	}
	secondServerConn = <-accepted

	client.Mu.Lock()
	client.Conn = replacementConn
	client.Mu.Unlock()

	firstConnectionPongs := 0
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for firstConnectionPongs < 3 {
		select {
		case id := <-pongs:
			if id == 2 {
				t.Fatal("keepalive goroutine from the original connection wrote the replacement connection")
			}
			if id == 1 {
				firstConnectionPongs++
			}
		case <-deadline.C:
			t.Fatal("original keepalive stopped instead of remaining bound to its exact connection")
		}
	}

}

func TestWSClientKeepAliveStopsAfterExactConnectionWriteFails(t *testing.T) {
	t.Setenv("PROXY", "")

	accepted := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		accepted <- conn
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	client := NewWSClient(context.Background(), wsURL)
	defer client.cancel()
	client.pongInterval = 5 * time.Millisecond
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	serverConn := <-accepted
	defer serverConn.Close()

	keepAliveDone := make(chan struct{})
	client.wg.Add(1)
	go func() {
		client.keepAlive(conn)
		close(keepAliveDone)
	}()
	_ = conn.Close()

	select {
	case <-keepAliveDone:
	case <-time.After(250 * time.Millisecond):
		client.cancel()
		<-keepAliveDone
		t.Fatal("keepalive continued retrying a permanently closed exact connection")
	}
}
