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

func TestWSReconnectHooksBracketSubscriptionRestore(t *testing.T) {
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
		if attempt == 1 {
			for range 2 {
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if err := writeGateSubscribeACK(conn, req, true, ""); err != nil {
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
			if req.Event == "subscribe" {
				record("subscribe:" + req.Channel)
			}
			if err := writeGateSubscribeACK(conn, req, true, ""); err != nil {
				return
			}
		}
		close(subscriptionsRead)
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	client := MustNewWSClient(ProductSpot).
		WithCredentials("key", "secret").
		WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	defer client.Close()
	client.SetReconnectHooks(func(error) {
		record("started")
	}, func() {
		record("recovered")
		recoveredOnce.Do(func() { close(recovered) })
	})

	for _, channel := range []string{ChannelSpotOrder, ChannelSpotUserTrade} {
		if err := client.Subscribe(context.Background(), channel, []string{"BTC_USDT"}, func(json.RawMessage) {}); err != nil {
			t.Fatalf("Subscribe %s: %v", channel, err)
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
	started, recoveredAt := -1, -1
	seen := map[string]bool{}
	for i, step := range got {
		switch step {
		case "started":
			started = i
		case "recovered":
			recoveredAt = i
		default:
			seen[step] = true
		}
	}
	if started < 0 || recoveredAt <= started || !seen["subscribe:"+ChannelSpotOrder] || !seen["subscribe:"+ChannelSpotUserTrade] {
		t.Fatalf("reconnect sequence=%v, want started, both restored subscriptions, recovered", got)
	}
}

func TestWSInitialSubscribeRequiresACKAndClosesFailedConnection(t *testing.T) {
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
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				if attempt == 1 {
					if tc.firstACK != nil {
						if err := writeGateSubscribeACK(conn, req, *tc.firstACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(firstClosed)
					return
				}
				if err := writeGateSubscribeACK(conn, req, true, ""); err != nil {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()

			client := MustNewWSClient(ProductSpot).
				WithCredentials("key", "secret").
				WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()

			err := client.Subscribe(context.Background(), ChannelSpotOrder, []string{"BTC_USDT"}, func(json.RawMessage) {})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("first Subscribe error=%v, want %q", err, tc.wantError)
			}
			key := wsKey(ChannelSpotOrder, []string{"BTC_USDT"})
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

			if err := client.Subscribe(context.Background(), ChannelSpotOrder, []string{"BTC_USDT"}, func(json.RawMessage) {}); err != nil {
				t.Fatalf("retry Subscribe: %v", err)
			}
			if got := dials.Load(); got != 2 {
				t.Fatalf("websocket dials=%d, want fresh second connection", got)
			}
		})
	}
}

func TestWSReconnectRequiresEveryACKBeforeRecovered(t *testing.T) {
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
				var req wsRequest
				if err := conn.ReadJSON(&req); err != nil {
					return
				}
				switch attempt {
				case 1:
					if err := writeGateSubscribeACK(conn, req, true, ""); err != nil {
						return
					}
					<-closeInitial
				case 2:
					if tc.secondACK != nil {
						if err := writeGateSubscribeACK(conn, req, *tc.secondACK, "rejected"); err != nil {
							return
						}
					}
					_, _, _ = conn.ReadMessage()
					close(secondClosed)
				case 3:
					close(thirdSubscribeSeen)
					<-allowThirdACK
					if err := writeGateSubscribeACK(conn, req, true, ""); err != nil {
						return
					}
					_, _, _ = conn.ReadMessage()
				}
			}))
			defer server.Close()

			client := MustNewWSClient(ProductSpot).
				WithCredentials("key", "secret").
				WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
			client.subscriptionAckTimeout = 75 * time.Millisecond
			defer client.Close()
			client.SetReconnectHooks(func(error) {
				startedOnce.Do(func() { close(started) })
			}, func() {
				recoveredOnce.Do(func() { close(recovered) })
			})

			if err := client.Subscribe(context.Background(), ChannelSpotOrder, []string{"BTC_USDT"}, func(json.RawMessage) {}); err != nil {
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

func TestWSResubscribeAllPropagatesWriteFailure(t *testing.T) {
	client := MustNewWSClient(ProductSpot).WithCredentials("key", "secret")
	client.subs[wsKey(ChannelSpotOrder, []string{"BTC_USDT"})] = wsSubscription{
		channel: ChannelSpotOrder,
		payload: []string{"BTC_USDT"},
	}
	if err := client.resubscribeAll(nil); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("resubscribeAll error=%v, want write failure", err)
	}
}

func TestWSResubscribeAllUsesCapturedConnection(t *testing.T) {
	captured, peer := gateWSPair(t)
	client := MustNewWSClient(ProductSpot).WithCredentials("key", "secret")
	other := &websocket.Conn{}
	client.mu.Lock()
	client.conn = other
	client.subs[wsKey(ChannelSpotOrder, []string{"BTC_USDT"})] = wsSubscription{
		channel: ChannelSpotOrder,
		payload: []string{"BTC_USDT"},
	}
	client.mu.Unlock()
	t.Cleanup(func() {
		client.mu.Lock()
		client.conn = nil
		client.mu.Unlock()
	})

	go client.readLoop(captured)
	errCh := make(chan error, 1)
	go func() { errCh <- client.resubscribeAll(captured) }()

	var req wsRequest
	if err := peer.ReadJSON(&req); err != nil {
		t.Fatalf("read restored subscription: %v", err)
	}
	if req.Event != "subscribe" || req.Channel != ChannelSpotOrder {
		t.Fatalf("restored request=%+v, want captured private subscription", req)
	}
	if err := writeGateSubscribeACK(peer, req, true, ""); err != nil {
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

func TestWSSubscriptionACKRequiresExactConnectionIDChannelAndEvent(t *testing.T) {
	captured, capturedPeer := gateWSPair(t)
	stale, stalePeer := gateWSPair(t)
	client := MustNewWSClient(ProductSpot).WithCredentials("key", "secret")
	client.subscriptionAckTimeout = time.Second
	go client.readLoop(captured)
	go client.readLoop(stale)

	errCh := make(chan error, 1)
	go func() {
		errCh <- client.subscribeOnConn(context.Background(), captured, wsRequest{
			Time:    time.Now().Unix(),
			Channel: ChannelSpotOrder,
			Event:   "subscribe",
			Payload: []string{"BTC_USDT"},
		})
	}()

	var req wsRequest
	if err := capturedPeer.ReadJSON(&req); err != nil {
		t.Fatalf("read subscription request: %v", err)
	}
	if req.ID == 0 {
		t.Fatal("subscription request did not include a unique id")
	}
	if err := writeGateSubscribeACK(stalePeer, req, true, ""); err != nil {
		t.Fatalf("write stale-connection ACK: %v", err)
	}
	assertGateSubscriptionStillWaiting(t, errCh, "stale connection")

	wrongID := req
	wrongID.ID++
	if err := writeGateSubscribeACK(capturedPeer, wrongID, true, ""); err != nil {
		t.Fatalf("write wrong-id ACK: %v", err)
	}
	assertGateSubscriptionStillWaiting(t, errCh, "wrong id")

	wrongChannel := req
	wrongChannel.Channel = ChannelSpotUserTrade
	if err := writeGateSubscribeACK(capturedPeer, wrongChannel, true, ""); err != nil {
		t.Fatalf("write wrong-channel ACK: %v", err)
	}
	assertGateSubscriptionStillWaiting(t, errCh, "wrong channel")

	wrongEvent := req
	wrongEvent.Event = "unsubscribe"
	if err := writeGateSubscribeACK(capturedPeer, wrongEvent, true, ""); err != nil {
		t.Fatalf("write wrong-event ACK: %v", err)
	}
	assertGateSubscriptionStillWaiting(t, errCh, "wrong event")

	if err := writeGateSubscribeACK(capturedPeer, req, true, ""); err != nil {
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

func assertGateSubscriptionStillWaiting(t *testing.T, errCh <-chan error, mismatch string) {
	t.Helper()
	select {
	case err := <-errCh:
		t.Fatalf("subscription completed from %s ACK: %v", mismatch, err)
	case <-time.After(25 * time.Millisecond):
	}
}

func writeGateSubscribeACK(conn *websocket.Conn, req wsRequest, success bool, message string) error {
	response := map[string]any{
		"id":      req.ID,
		"time":    req.Time,
		"channel": req.Channel,
		"event":   req.Event,
		"result":  map[string]string{"status": "success"},
	}
	if !success {
		response["result"] = nil
		response["error"] = map[string]any{"code": 1, "message": message}
	}
	return conn.WriteJSON(response)
}

func boolPointer(value bool) *bool {
	return &value
}

func gateWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
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
