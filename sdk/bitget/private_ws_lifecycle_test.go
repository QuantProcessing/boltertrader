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
		var login wsLoginRequest
		if err := conn.ReadJSON(&login); err != nil {
			return
		}
		if attempt > 1 {
			record("auth")
		}
		if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": "success"}); err != nil {
			return
		}

		if attempt == 1 {
			for range 2 {
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
					return
				}
			}
			<-closeInitial
			return
		}

		for range 2 {
			var req wsRequest
			if err := conn.ReadJSON(&req); err != nil {
				return
			}
			if req.Op == "subscribe" && len(req.Args) == 1 {
				record("subscribe:" + req.Args[0].Topic)
			}
			if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
				return
			}
		}
		close(subscriptionsRead)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.url = "ws" + strings.TrimPrefix(server.URL, "http")
	defer client.Close()
	client.SetReconnectHooks(func(error) {
		record("started")
	}, func() {
		record("recovered")
		recoveredOnce.Do(func() { close(recovered) })
	})

	for _, topic := range []string{"order", "fill"} {
		if err := client.Subscribe(context.Background(), WSArg{InstType: "UTA", Topic: topic}, func(json.RawMessage) {}); err != nil {
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
	assertReconnectSequence(t, got, []string{"subscribe:order", "subscribe:fill"})
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
				var login wsLoginRequest
				if err := conn.ReadJSON(&login); err != nil {
					return
				}
				if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": "success"}); err != nil {
					return
				}
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if attempt == 1 {
					if tc.firstACK != nil {
						if err := writeBitgetSubscribeACK(conn, req, *tc.firstACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(firstClosed)
					return
				}
				if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()
			arg := WSArg{InstType: "UTA", Topic: "order"}

			err := client.Subscribe(context.Background(), arg, func(json.RawMessage) {})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("first Subscribe error=%v, want %q", err, tc.wantError)
			}
			key := wsKey(arg)
			client.mu.RLock()
			conn := client.conn
			_, retainedSub := client.subs[key]
			_, retainedHandler := client.handlers[key]
			client.mu.RUnlock()
			if conn != nil || retainedSub || retainedHandler {
				t.Fatalf("failed Subscribe retained conn=%v sub=%v handler=%v", conn != nil, retainedSub, retainedHandler)
			}
			select {
			case <-firstClosed:
			case <-time.After(time.Second):
				t.Fatal("failed Subscribe did not close its exact websocket")
			}

			if err := client.Subscribe(context.Background(), arg, func(json.RawMessage) {}); err != nil {
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
				var login wsLoginRequest
				if err := conn.ReadJSON(&login); err != nil {
					return
				}
				if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": "success"}); err != nil {
					return
				}
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				switch attempt {
				case 1:
					if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
						return
					}
					<-closeInitial
				case 2:
					if tc.secondACK != nil {
						if err := writeBitgetSubscribeACK(conn, req, *tc.secondACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(secondClosed)
				case 3:
					close(thirdSubscribeSeen)
					<-allowThirdACK
					if err := writeBitgetSubscribeACK(conn, req, true, "success"); err != nil {
						return
					}
					_, _, _ = conn.ReadMessage()
				}
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()
			client.SetReconnectHooks(func(error) {
				startedOnce.Do(func() { close(started) })
			}, func() {
				recoveredOnce.Do(func() { close(recovered) })
			})
			arg := WSArg{InstType: "UTA", Topic: "order"}

			if err := client.Subscribe(context.Background(), arg, func(json.RawMessage) {}); err != nil {
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

func assertReconnectSequence(t *testing.T, got []string, subscriptions []string) {
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
		connectFirst func(*testing.T, *PrivateWSClient, <-chan struct{}) error
		wantError    string
	}{
		{
			name:       "reject",
			firstReply: map[string]any{"event": "login", "code": "30005", "msg": "invalid credentials"},
			connectFirst: func(t *testing.T, client *PrivateWSClient, _ <-chan struct{}) error {
				return client.Connect(context.Background())
			},
			wantError: "login failed",
		},
		{
			name: "timeout",
			connectFirst: func(t *testing.T, client *PrivateWSClient, _ <-chan struct{}) error {
				return client.Connect(context.Background())
			},
			wantError: "login timeout",
		},
		{
			name: "context cancellation",
			connectFirst: func(t *testing.T, client *PrivateWSClient, loginSeen <-chan struct{}) error {
				ctx, cancel := context.WithCancel(context.Background())
				go func() {
					<-loginSeen
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
			firstLoginSeen := make(chan struct{})
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
					close(firstLoginSeen)
					if tc.firstReply != nil {
						if err := conn.WriteJSON(tc.firstReply); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					return
				}
				if err := conn.WriteJSON(map[string]any{"event": "login", "code": "0", "msg": "success"}); err != nil {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
			client.url = "ws" + strings.TrimPrefix(server.URL, "http")
			defer client.Close()

			err := tc.connectFirst(t, client, firstLoginSeen)
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
	captured, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	other := &websocket.Conn{}
	client.mu.Lock()
	client.conn = other
	client.subs["order"] = WSArg{InstType: "UTA", Topic: "order"}
	client.mu.Unlock()
	t.Cleanup(func() {
		client.mu.Lock()
		client.conn = nil
		client.mu.Unlock()
	})

	loginCh := make(chan error, 1)
	go client.readLoop(captured, loginCh)
	errCh := make(chan error, 1)
	go func() { errCh <- client.resubscribeAll(captured) }()

	var req wsRequest
	if err := peer.ReadJSON(&req); err != nil {
		t.Fatalf("read restored subscription: %v", err)
	}
	if req.Op != "subscribe" || len(req.Args) != 1 || req.Args[0].Topic != "order" {
		t.Fatalf("restored request=%+v, want captured connection topic order", req)
	}
	if err := writeBitgetSubscribeACK(peer, req, true, "success"); err != nil {
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

func TestPrivateWSSubscriptionACKRequiresExactConnectionAndArgument(t *testing.T) {
	captured, capturedPeer := bitgetPrivateWSPair(t)
	stale, stalePeer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.subscriptionAckTimeout = time.Second
	go client.readLoop(captured, make(chan error, 1))
	go client.readLoop(stale, make(chan error, 1))

	arg := WSArg{InstType: "UTA", Topic: "order"}
	errCh := make(chan error, 1)
	go func() {
		errCh <- client.subscribeOnConn(context.Background(), captured, arg)
	}()

	var req wsRequest
	if err := capturedPeer.ReadJSON(&req); err != nil {
		t.Fatalf("read subscription request: %v", err)
	}
	if err := writeBitgetSubscribeACK(stalePeer, req, true, "success"); err != nil {
		t.Fatalf("write stale-connection ACK: %v", err)
	}
	assertBitgetSubscriptionStillWaiting(t, errCh, "stale connection")

	wrongArg := req
	wrongArg.Args = []WSArg{{InstType: "UTA", Topic: "fill"}}
	if err := writeBitgetSubscribeACK(capturedPeer, wrongArg, true, "success"); err != nil {
		t.Fatalf("write wrong-argument ACK: %v", err)
	}
	assertBitgetSubscriptionStillWaiting(t, errCh, "wrong subscription argument")

	if err := writeBitgetSubscribeACK(capturedPeer, req, true, "success"); err != nil {
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

func assertBitgetSubscriptionStillWaiting(t *testing.T, errCh <-chan error, mismatch string) {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("subscription completed from %s ACK: %v", mismatch, err)
	case <-time.After(25 * time.Millisecond):
	}
}

func writeBitgetSubscribeACK(conn *websocket.Conn, req wsRequest, success bool, message string) error {
	code := "0"
	if !success {
		code = "30001"
	}
	var arg WSArg
	if len(req.Args) > 0 {
		arg = req.Args[0]
	}
	return conn.WriteJSON(map[string]any{
		"event": "subscribe",
		"code":  code,
		"msg":   message,
		"arg":   arg,
	})
}

func boolPointer(value bool) *bool {
	return &value
}

func bitgetPrivateWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
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
