package hyperliquid

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnectReplaysRetainedSubscriptionsAfterDisconnect(t *testing.T) {
	var connections atomic.Int32
	received := make(chan int32, 2)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		connection := connections.Add(1)
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req WsSubscribeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		received <- connection
		_ = conn.WriteJSON(map[string]any{"channel": "subscriptionResponse", "data": req.Subscription})
		_ = conn.SetReadDeadline(time.Time{})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	client := NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client.SubscriptionAckTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		cancel()
		client.Close()
	})
	if err := client.Connect(); err != nil {
		t.Fatalf("first Connect: %v", err)
	}
	publicSubscription := map[string]string{"type": "l2Book", "coin": "BTC"}
	if err := client.Subscribe("l2Book", publicSubscription, func(WsMessage) {}); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	select {
	case got := <-received:
		if got != 1 {
			t.Fatalf("first subscription connection=%d, want 1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first connection did not receive subscription")
	}

	client.Disconnect()
	if err := client.Connect(); err != nil {
		t.Fatalf("second Connect: %v", err)
	}
	select {
	case got := <-received:
		if got != 2 {
			t.Fatalf("replayed subscription connection=%d, want 2", got)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("fresh Connect did not replay retained public subscription")
	}
}

func TestSubscriptionKeyTreatsUserAddressCaseInsensitively(t *testing.T) {
	request := map[string]any{
		"type": "orderUpdates",
		"user": "0x000000000000000000000000000000000000dEaD",
	}
	acknowledged := map[string]any{
		"type": "orderUpdates",
		"user": "0x000000000000000000000000000000000000dead",
	}

	if got, want := subscriptionKey(acknowledged), subscriptionKey(request); got != want {
		t.Fatalf("subscription keys differ when only user address casing changed")
	}
}

func TestSubscriptionKeyKeepsNonIdentityFieldsCaseSensitive(t *testing.T) {
	lowerCoin := map[string]any{
		"type": "l2Book",
		"coin": "btc",
	}
	upperCoin := map[string]any{
		"type": "l2Book",
		"coin": "BTC",
	}

	if subscriptionKey(lowerCoin) == subscriptionKey(upperCoin) {
		t.Fatal("subscription key normalized a case-sensitive non-identity field")
	}
}

func TestSubscriptionKeyNormalizesL2BookServerDefaultFields(t *testing.T) {
	request := map[string]any{
		"type": "l2Book",
		"coin": "@1",
	}
	acknowledged := map[string]any{
		"type":     "l2Book",
		"coin":     "@1",
		"nSigFigs": nil,
		"mantissa": nil,
		"fast":     false,
	}

	if got, want := subscriptionKey(acknowledged), subscriptionKey(request); got != want {
		t.Fatalf("l2Book server defaults changed acknowledgement key\ngot:  %s\nwant: %s", got, want)
	}

	for name, configured := range map[string]map[string]any{
		"significant figures": {"type": "l2Book", "coin": "@1", "nSigFigs": float64(5)},
		"mantissa":            {"type": "l2Book", "coin": "@1", "mantissa": float64(2)},
		"fast":                {"type": "l2Book", "coin": "@1", "fast": true},
	} {
		t.Run(name, func(t *testing.T) {
			if subscriptionKey(configured) == subscriptionKey(request) {
				t.Fatalf("l2Book key collapsed meaningful %s configuration", name)
			}
		})
	}
}

func TestWrappedL2BookSubscriptionResponseMatchesServerDefaultFields(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWebsocketClient(ctx)
	conn := &websocket.Conn{}
	client.Mu.Lock()
	client.Conn = conn
	client.Mu.Unlock()

	request := map[string]any{"type": "l2Book", "coin": "@1"}
	waiter, err := client.registerSubscriptionAck(conn, subscriptionKey(request))
	if err != nil {
		t.Fatalf("register subscription acknowledgement: %v", err)
	}
	data, err := json.Marshal(map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type":     "l2Book",
			"coin":     "@1",
			"nSigFigs": nil,
			"mantissa": nil,
			"fast":     false,
		},
	})
	if err != nil {
		t.Fatalf("marshal subscription acknowledgement: %v", err)
	}

	client.handleSubscriptionResponse(conn, data)
	select {
	case result := <-waiter:
		if result != nil {
			t.Fatalf("subscription acknowledgement result: %v", result)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("l2Book server-default acknowledgement did not satisfy waiter")
	}
	client.Mu.Lock()
	client.Conn = nil
	client.Mu.Unlock()
}

func TestDelayedUnsubscribeResponseCannotConsumeNewSubscribeWaiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWebsocketClient(ctx)
	conn := &websocket.Conn{}
	client.Mu.Lock()
	client.Conn = conn
	client.Mu.Unlock()

	subscription := map[string]any{"type": "l2Book", "coin": "@1"}
	waiter, err := client.registerSubscriptionAck(conn, subscriptionKey(subscription))
	if err != nil {
		t.Fatalf("register new subscribe acknowledgement: %v", err)
	}
	unsubscribeData, err := json.Marshal(map[string]any{
		"method":       "unsubscribe",
		"subscription": subscription,
	})
	if err != nil {
		t.Fatalf("marshal delayed unsubscribe response: %v", err)
	}
	client.handleSubscriptionResponse(conn, unsubscribeData)
	select {
	case result := <-waiter:
		t.Fatalf("delayed unsubscribe response consumed new subscribe waiter: %v", result)
	default:
	}

	subscribeData, err := json.Marshal(map[string]any{
		"method":       "subscribe",
		"subscription": subscription,
	})
	if err != nil {
		t.Fatalf("marshal subscribe response: %v", err)
	}
	client.handleSubscriptionResponse(conn, subscribeData)
	select {
	case result := <-waiter:
		if result != nil {
			t.Fatalf("new subscribe acknowledgement result: %v", result)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("new subscribe acknowledgement did not satisfy waiter")
	}
	client.Mu.Lock()
	client.Conn = nil
	client.Mu.Unlock()
}

func TestSubscriptionKeyNormalizesSpotStatePortfolioMarginACKAlias(t *testing.T) {
	request := map[string]any{
		"type":              "spotState",
		"user":              "0x000000000000000000000000000000000000dEaD",
		"isPortfolioMargin": false,
	}
	acknowledged := map[string]any{
		"type":                  "spotState",
		"user":                  "0x000000000000000000000000000000000000dead",
		"ignorePortfolioMargin": false,
	}

	if got, want := subscriptionKey(acknowledged), subscriptionKey(request); got != want {
		t.Fatal("spotState ACK alias did not match the official request field")
	}
}

func TestSubscriptionKeyDoesNotCollapseDifferentSpotStatePortfolioModes(t *testing.T) {
	request := map[string]any{
		"type":              "spotState",
		"user":              "0x000000000000000000000000000000000000dEaD",
		"isPortfolioMargin": false,
	}
	acknowledged := map[string]any{
		"type":                  "spotState",
		"user":                  "0x000000000000000000000000000000000000dead",
		"ignorePortfolioMargin": true,
	}

	if subscriptionKey(acknowledged) == subscriptionKey(request) {
		t.Fatal("spotState ACK with a different portfolio mode matched")
	}
}

func TestSubscriptionKeyDoesNotCollapseDifferentUsersOrTypes(t *testing.T) {
	base := map[string]any{
		"type": "orderUpdates",
		"user": "0x000000000000000000000000000000000000dEaD",
	}
	for name, other := range map[string]map[string]any{
		"user": {
			"type": "orderUpdates",
			"user": "0x000000000000000000000000000000000000bEEF",
		},
		"type": {
			"type": "userFills",
			"user": "0x000000000000000000000000000000000000dead",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if subscriptionKey(base) == subscriptionKey(other) {
				t.Fatalf("subscription key collapsed different %s", name)
			}
		})
	}
}

func TestWrappedSubscriptionResponseMatchesUserAddressCaseInsensitively(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWebsocketClient(ctx)
	conn := &websocket.Conn{}
	client.Mu.Lock()
	client.Conn = conn
	client.Mu.Unlock()

	request := map[string]any{
		"type": "userFills",
		"user": "0x000000000000000000000000000000000000dEaD",
	}
	waiter, err := client.registerSubscriptionAck(conn, subscriptionKey(request))
	if err != nil {
		t.Fatalf("register subscription acknowledgement: %v", err)
	}
	data, err := json.Marshal(map[string]any{
		"method": "subscribe",
		"subscription": map[string]any{
			"type": "userFills",
			"user": "0x000000000000000000000000000000000000dead",
		},
	})
	if err != nil {
		t.Fatalf("marshal subscription acknowledgement: %v", err)
	}

	client.handleSubscriptionResponse(conn, data)
	select {
	case result := <-waiter:
		if result != nil {
			t.Fatalf("subscription acknowledgement result: %v", result)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("case-normalized subscription acknowledgement did not satisfy waiter")
	}
	client.Mu.Lock()
	client.Conn = nil
	client.Mu.Unlock()
}
