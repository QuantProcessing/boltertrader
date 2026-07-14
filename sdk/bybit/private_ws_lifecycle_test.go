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

func TestPrivateWSReconnectHooksBracketAuthenticationAndSubscriptionRestore(t *testing.T) {
	var dials atomic.Int32
	var sequenceMu sync.Mutex
	var sequence []string
	record := func(step string) {
		sequenceMu.Lock()
		sequence = append(sequence, step)
		sequenceMu.Unlock()
	}

	closeInitial := make(chan struct{})
	subscriptionsRead := make(chan struct{})
	recovered := make(chan struct{})
	var recoveredOnce sync.Once
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
		if attempt > 1 {
			record("auth")
		}
		if err := conn.WriteJSON(map[string]any{"op": "auth", "success": true, "ret_msg": "OK"}); err != nil {
			return
		}

		if attempt == 1 {
			for range 2 {
				var req wsCommandRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if err := writeBybitSubscribeACK(conn, req, true, "OK"); err != nil {
					return
				}
			}
			<-closeInitial
			return
		}

		for range 2 {
			var req wsCommandRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Op == "subscribe" && len(req.Args) == 1 {
				record("subscribe:" + req.Args[0])
			}
			if err := writeBybitSubscribeACK(conn, req, true, "OK"); err != nil {
				return
			}
		}
		close(subscriptionsRead)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	defer client.Close()
	client.SetReconnectHooks(func(error) {
		record("started")
	}, func() {
		record("recovered")
		recoveredOnce.Do(func() { close(recovered) })
	})

	for _, topic := range []string{"order", "execution"} {
		if err := client.Subscribe(context.Background(), topic, func(json.RawMessage) {}); err != nil {
			t.Fatalf("Subscribe %s: %v", topic, err)
		}
	}
	close(closeInitial)
	select {
	case <-recovered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect recovery")
	}
	select {
	case <-subscriptionsRead:
	case <-time.After(time.Second):
		t.Fatal("reconnected server did not receive every subscription")
	}

	sequenceMu.Lock()
	got := append([]string(nil), sequence...)
	sequenceMu.Unlock()
	assertPrivateReconnectSequence(t, got, []string{"subscribe:order", "subscribe:execution"})
}

func TestPrivateWSInitialSubscribeRequiresACKAndClosesFailedConnection(t *testing.T) {
	tests := []struct {
		name      string
		firstACK  *bool
		wantError string
	}{
		{name: "reject", firstACK: boolPointer(false), wantError: "subscribe failed"},
		{name: "timeout", wantError: "subscription timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			firstClosed := make(chan struct{})
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
				var req wsCommandRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if attempt == 1 {
					if tc.firstACK != nil {
						if err := writeBybitSubscribeACK(conn, req, *tc.firstACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(firstClosed)
					return
				}
				if err := writeBybitSubscribeACK(conn, req, true, "OK"); err != nil {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()

			err := client.Subscribe(context.Background(), "order", func(json.RawMessage) {})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("first Subscribe error=%v, want %q", err, tc.wantError)
			}
			client.mu.RLock()
			conn := client.conn
			_, retained := client.handlers["order"]
			client.mu.RUnlock()
			if conn != nil || retained {
				t.Fatalf("failed Subscribe retained conn=%v handler=%v", conn != nil, retained)
			}
			select {
			case <-firstClosed:
			case <-time.After(time.Second):
				t.Fatal("failed Subscribe did not close its exact websocket")
			}

			if err := client.Subscribe(context.Background(), "order", func(json.RawMessage) {}); err != nil {
				t.Fatalf("retry Subscribe: %v", err)
			}
			if got := dials.Load(); got != 2 {
				t.Fatalf("websocket dials=%d, want fresh second connection", got)
			}
		})
	}
}

func TestPrivateWSReconnectRequiresEveryACKBeforeRecovered(t *testing.T) {
	tests := []struct {
		name      string
		secondACK *bool
	}{
		{name: "reject", secondACK: boolPointer(false)},
		{name: "timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			closeInitial := make(chan struct{})
			secondClosed := make(chan struct{})
			thirdSubscribeSeen := make(chan struct{})
			allowThirdACK := make(chan struct{})
			recovered := make(chan struct{})
			started := make(chan struct{})
			var startedOnce sync.Once
			var recoveredOnce sync.Once
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
				var req wsCommandRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				switch attempt {
				case 1:
					if err := writeBybitSubscribeACK(conn, req, true, "OK"); err != nil {
						return
					}
					<-closeInitial
				case 2:
					if tc.secondACK != nil {
						if err := writeBybitSubscribeACK(conn, req, *tc.secondACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(secondClosed)
				case 3:
					close(thirdSubscribeSeen)
					<-allowThirdACK
					if err := writeBybitSubscribeACK(conn, req, true, "OK"); err != nil {
						return
					}
					_, _, _ = conn.ReadMessage()
				}
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()
			client.SetReconnectHooks(func(error) {
				startedOnce.Do(func() { close(started) })
			}, func() {
				recoveredOnce.Do(func() { close(recovered) })
			})

			if err := client.Subscribe(context.Background(), "order", func(json.RawMessage) {}); err != nil {
				t.Fatalf("initial Subscribe: %v", err)
			}
			close(closeInitial)
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for reconnect start")
			}
			select {
			case <-secondClosed:
			case <-time.After(3 * time.Second):
				t.Fatal("failed reconnect subscription did not close its exact websocket")
			}
			select {
			case <-recovered:
				t.Fatal("recovered emitted after rejected or missing subscription ACK")
			default:
			}
			select {
			case <-thirdSubscribeSeen:
			case <-time.After(3 * time.Second):
				t.Fatal("reconnect did not retry with a fresh websocket")
			}
			select {
			case <-recovered:
				t.Fatal("recovered emitted before replacement subscription ACK")
			default:
			}
			close(allowThirdACK)
			select {
			case <-recovered:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for recovery after subscription ACK")
			}
			if got := dials.Load(); got != 3 {
				t.Fatalf("websocket dials=%d, want initial + failed reconnect + successful retry", got)
			}
		})
	}
}

func assertPrivateReconnectSequence(t *testing.T, got []string, subscriptions []string) {
	t.Helper()
	index := make(map[string]int, len(got))
	for i, step := range got {
		index[step] = i
	}
	started, startedOK := index["started"]
	auth, authOK := index["auth"]
	recovered, recoveredOK := index["recovered"]
	if !startedOK || !authOK || !recoveredOK || !(started < auth && auth < recovered) {
		t.Fatalf("reconnect sequence=%v, want started -> auth -> subscriptions -> recovered", got)
	}
	for _, subscription := range subscriptions {
		at, ok := index[subscription]
		if !ok || at <= auth {
			t.Fatalf("reconnect sequence=%v, want %s written after authentication", got, subscription)
		}
	}
}

func TestPrivateWSFailedAuthenticationClosesConnectionBeforeFreshRetry(t *testing.T) {
	tests := []struct {
		name         string
		firstReply   any
		connectFirst func(*PrivateWSClient, <-chan struct{}) error
		wantError    string
	}{
		{
			name:       "reject",
			firstReply: map[string]any{"op": "auth", "success": false, "ret_msg": "invalid credentials"},
			connectFirst: func(client *PrivateWSClient, _ <-chan struct{}) error {
				return client.Connect(context.Background())
			},
			wantError: "auth failed",
		},
		{
			name: "timeout",
			connectFirst: func(client *PrivateWSClient, _ <-chan struct{}) error {
				return client.Connect(context.Background())
			},
			wantError: "auth timeout",
		},
		{
			name: "context cancellation",
			connectFirst: func(client *PrivateWSClient, authSeen <-chan struct{}) error {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					<-authSeen
					cancel()
				}()
				return client.Connect(ctx)
			},
			wantError: context.Canceled.Error(),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var dials atomic.Int32
			firstAuthSeen := make(chan struct{})
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer conn.Close()
				attempt := dials.Add(1)
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
				if attempt == 1 {
					close(firstAuthSeen)
					if tc.firstReply != nil {
						if err := conn.WriteJSON(tc.firstReply); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					return
				}
				if err := conn.WriteJSON(map[string]any{"op": "auth", "success": true, "ret_msg": "OK"}); err != nil {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			defer client.Close()

			err := tc.connectFirst(client, firstAuthSeen)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("first Connect error=%v, want %q", err, tc.wantError)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("retry Connect: %v", err)
			}
			if got := dials.Load(); got != 2 {
				t.Fatalf("websocket dials=%d, want fresh second connection after failed authentication", got)
			}
		})
	}
}

func TestPrivateWSResubscribeAllUsesCapturedConnection(t *testing.T) {
	captured, peer := bybitPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret")
	other := &websocket.Conn{}
	client.mu.Lock()
	client.conn = other
	client.handlers["order"] = func(json.RawMessage) {}
	client.mu.Unlock()
	t.Cleanup(func() {
		client.mu.Lock()
		client.conn = nil
		client.mu.Unlock()
	})

	authCh := make(chan error, 1)
	go client.readLoop(captured, authCh)
	errCh := make(chan error, 1)
	go func() { errCh <- client.resubscribeAll(captured) }()

	var req wsCommandRequest
	if err := peer.ReadJSON(&req); err != nil {
		t.Fatalf("read restored subscription: %v", err)
	}
	if req.Op != "subscribe" || len(req.Args) != 1 || req.Args[0] != "order" {
		t.Fatalf("restored request=%+v, want captured connection topic order", req)
	}
	if err := writeBybitSubscribeACK(peer, req, true, "OK"); err != nil {
		t.Fatalf("write captured connection ACK: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("resubscribeAll: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("resubscribeAll did not accept ACK from captured connection")
	}
}

func TestPrivateWSSubscriptionACKRequiresExactConnectionAndRequestID(t *testing.T) {
	captured, capturedPeer := bybitPrivateWSPair(t)
	stale, stalePeer := bybitPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret")
	client.subscriptionAckTimeout = time.Second
	go client.readLoop(captured, make(chan error, 1))
	go client.readLoop(stale, make(chan error, 1))

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.subscribeOnConn(context.Background(), captured, "order")
	}()

	var req wsCommandRequest
	if err := capturedPeer.ReadJSON(&req); err != nil {
		t.Fatalf("read subscription request: %v", err)
	}
	if req.ReqID == "" {
		t.Fatal("subscription request did not include req_id")
	}
	if err := writeBybitSubscribeACK(stalePeer, req, true, "OK"); err != nil {
		t.Fatalf("write stale-connection ACK: %v", err)
	}
	assertBybitSubscriptionStillWaiting(t, errCh, "stale connection")

	wrongID := req
	wrongID.ReqID += "-stale"
	if err := writeBybitSubscribeACK(capturedPeer, wrongID, true, "OK"); err != nil {
		t.Fatalf("write wrong-request ACK: %v", err)
	}
	assertBybitSubscriptionStillWaiting(t, errCh, "wrong request id")

	if err := writeBybitSubscribeACK(capturedPeer, req, true, "OK"); err != nil {
		t.Fatalf("write exact ACK: %v", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("subscribeOnConn exact ACK: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exact ACK did not complete subscription")
	}
}

func assertBybitSubscriptionStillWaiting(t *testing.T, errCh <-chan error, mismatch string) {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("subscription completed from %s ACK: %v", mismatch, err)
	case <-time.After(25 * time.Millisecond):
	}
}

func writeBybitSubscribeACK(conn *websocket.Conn, req wsCommandRequest, success bool, message string) error {
	return conn.WriteJSON(map[string]any{
		"op":      req.Op,
		"req_id":  req.ReqID,
		"success": success,
		"ret_msg": message,
	})
}

func boolPointer(value bool) *bool {
	return &value
}

func bybitPrivateWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	serverConn := make(chan *websocket.Conn, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err == nil {
			serverConn <- conn
		}
	}))
	t.Cleanup(server.Close)

	clientConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial websocket pair: %v", err)
	}
	peer := <-serverConn
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = peer.Close()
	})
	return clientConn, peer
}
