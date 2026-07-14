package perp

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestWSClientCloseInterruptsBlockedWrite(t *testing.T) {
	conn, writeObserved := newBlockedPerpWebSocket(t)
	client := newWSClient(context.Background(), "ws://aster.test/ws")
	client.Mu.Lock()
	client.Conn = conn
	client.Mu.Unlock()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- client.WriteJSON(map[string]string{"payload": strings.Repeat("x", 64<<10)})
	}()
	waitForPerpBlockedWrite(t, writeObserved)

	closeDone := make(chan struct{})
	go func() {
		client.Close()
		close(closeDone)
	}()
	select {
	case <-closeDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close blocked behind an in-flight websocket write")
	}
	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked websocket write unexpectedly succeeded after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close returned without interrupting the precise blocked connection")
	}
}

func TestWSClientBlockedWriteHasDeadline(t *testing.T) {
	conn, writeObserved := newBlockedPerpWebSocket(t)
	client := newWSClient(context.Background(), "ws://aster.test/ws")
	client.Mu.Lock()
	client.Conn = conn
	client.Mu.Unlock()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- client.WriteJSON(map[string]string{"payload": strings.Repeat("x", 64<<10)})
	}()
	waitForPerpBlockedWrite(t, writeObserved)

	select {
	case err := <-writeDone:
		if err == nil {
			t.Fatal("blocked websocket write unexpectedly succeeded")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("blocked websocket write did not honor a bounded write deadline")
	}
	client.Close()
}

func TestWSClientBlockedStaleWriteDoesNotCloseReplacement(t *testing.T) {
	stale, writeObserved := newBlockedPerpWebSocket(t)
	replacementReceived := make(chan struct{}, 1)
	server := newPerpWSServer(t, func(_ int, conn *websocket.Conn) {
		defer conn.Close()
		if _, _, err := conn.ReadMessage(); err == nil {
			replacementReceived <- struct{}{}
		}
	})
	defer server.Close()
	replacement, _, err := websocket.DefaultDialer.Dial(websocketURL(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()

	client := newWSClient(context.Background(), "ws://aster.test/ws")
	client.Mu.Lock()
	client.Conn = stale
	client.Mu.Unlock()
	firstWriteDone := make(chan error, 1)
	go func() {
		firstWriteDone <- client.WriteJSON(map[string]string{"payload": strings.Repeat("x", 64<<10)})
	}()
	waitForPerpBlockedWrite(t, writeObserved)

	queuedWriteDone := make(chan error, 1)
	go func() {
		queuedWriteDone <- client.writeJSONOnConn(stale, map[string]string{"queued": "stale"})
	}()
	swapDone := make(chan struct{})
	go func() {
		client.Mu.Lock()
		client.Conn = replacement
		client.Mu.Unlock()
		close(swapDone)
	}()
	select {
	case <-swapDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("connection replacement blocked behind stale websocket I/O")
	}
	_ = stale.Close()
	for name, done := range map[string]<-chan error{
		"blocked": firstWriteDone,
		"queued":  queuedWriteDone,
	} {
		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("%s stale write unexpectedly succeeded", name)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s stale write did not return", name)
		}
	}
	if err := client.WriteJSON(map[string]string{"replacement": "current"}); err != nil {
		t.Fatalf("replacement connection was closed by stale write cleanup: %v", err)
	}
	select {
	case <-replacementReceived:
	case <-time.After(time.Second):
		t.Fatal("replacement connection did not receive a write")
	}
	client.Close()
}

func waitForPerpBlockedWrite(t *testing.T, observed <-chan struct{}) {
	t.Helper()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("websocket peer did not observe the blocked write")
	}
}

func newBlockedPerpWebSocket(t *testing.T) (*websocket.Conn, <-chan struct{}) {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	writeObserved := make(chan struct{})
	releaseServer := make(chan struct{})
	serverReady := make(chan error, 1)
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		reader := bufio.NewReader(serverSide)
		request, err := http.ReadRequest(reader)
		if err != nil {
			serverReady <- err
			return
		}
		accept := perpWebSocketAccept(request.Header.Get("Sec-WebSocket-Key"))
		_, err = io.WriteString(serverSide, "HTTP/1.1 101 Switching Protocols\r\n"+
			"Upgrade: websocket\r\n"+
			"Connection: Upgrade\r\n"+
			"Sec-WebSocket-Accept: "+accept+"\r\n\r\n")
		serverReady <- err
		if err != nil {
			return
		}
		firstFrameByte := make([]byte, 1)
		if _, err := serverSide.Read(firstFrameByte); err == nil {
			close(writeObserved)
		}
		<-releaseServer
	}()

	endpoint, err := url.Parse("ws://aster.test/ws")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := websocket.NewClient(clientSide, endpoint, nil, 1024, 1024)
	if err != nil {
		_ = clientSide.Close()
		_ = serverSide.Close()
		close(releaseServer)
		<-serverDone
		t.Fatalf("create local websocket: %v", err)
	}
	if err := <-serverReady; err != nil {
		_ = conn.Close()
		_ = serverSide.Close()
		close(releaseServer)
		<-serverDone
		t.Fatalf("complete local websocket handshake: %v", err)
	}

	var cleanupOnce sync.Once
	t.Cleanup(func() {
		cleanupOnce.Do(func() {
			close(releaseServer)
			_ = conn.Close()
			_ = serverSide.Close()
			<-serverDone
		})
	})
	return conn, writeObserved
}

func perpWebSocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(digest[:])
}
