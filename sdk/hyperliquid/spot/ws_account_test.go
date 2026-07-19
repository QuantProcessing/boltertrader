package spot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/gorilla/websocket"
)

func TestWSAccountCompanion_SubscriptionTypes(t *testing.T) {
	if "orderUpdates" == "" || "userFills" == "" || "user" == "" {
		t.Fatal("expected spot account websocket subscription names")
	}
}

func TestDecodeSpotStateMessageReportsOnlyMatchingUserPayloadErrors(t *testing.T) {
	const user = "0x000000000000000000000000000000000000dEaD"

	if _, matched, err := decodeSpotStateMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000beef","spotState":{"balances":"bad"}}`), user); matched || err != nil {
		t.Fatalf("other user matched=%v err=%v, want ignored", matched, err)
	}
	if _, matched, err := decodeSpotStateMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000dead","spotState":{"balances":"bad"}}`), user); !matched || err == nil {
		t.Fatalf("matching malformed payload matched=%v err=%v, want observable decode error", matched, err)
	}
	if _, matched, err := decodeSpotStateMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000dead","spotState":{}}`), user); !matched || err == nil {
		t.Fatalf("matching missing balances matched=%v err=%v, want observable schema error", matched, err)
	}
	state, matched, err := decodeSpotStateMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000dead","spotState":{"balances":[]}}`), user)
	if err != nil || !matched || state.Balances == nil {
		t.Fatalf("valid empty balance snapshot state=%+v matched=%v err=%v", state, matched, err)
	}
}

func TestDecodePrivateAccountMessagesSurfaceMatchingPayloadErrors(t *testing.T) {
	const user = "0x000000000000000000000000000000000000dEaD"

	if _, err := decodeOrderUpdatesMessage(json.RawMessage(`{"bad":"shape"}`)); err == nil {
		t.Fatal("malformed orderUpdates payload was silently dropped")
	}
	updates, err := decodeOrderUpdatesMessage(json.RawMessage(`[]`))
	if err != nil || updates == nil {
		t.Fatalf("valid empty orderUpdates snapshot updates=%v err=%v", updates, err)
	}

	if _, matched, err := decodeUserFillsMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000beef","fills":"bad"}`), user); matched || err != nil {
		t.Fatalf("other user matched=%v err=%v, want ignored", matched, err)
	}
	if _, matched, err := decodeUserFillsMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000dead","fills":"bad"}`), user); !matched || err == nil {
		t.Fatalf("matching malformed fills matched=%v err=%v, want observable decode error", matched, err)
	}
	fills, matched, err := decodeUserFillsMessage(json.RawMessage(`{"user":"0x000000000000000000000000000000000000dead","fills":[]}`), user)
	if err != nil || !matched || fills.Fills == nil {
		t.Fatalf("valid empty fills snapshot fills=%+v matched=%v err=%v", fills, matched, err)
	}
}

func TestSubscribeUserEventsUsesOfficialRequestAndUserResponseChannel(t *testing.T) {
	requests := make(chan hyperliquid.WsSubscribeRequest, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req hyperliquid.WsSubscribeRequest
		if json.Unmarshal(raw, &req) != nil {
			return
		}
		requests <- req
		_ = conn.WriteJSON(map[string]any{"channel": "user", "data": map[string]any{"nonUserCancel": []any{map[string]any{"coin": "PURR/USDC", "oid": 7}}}})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := NewWebsocketClient(hyperliquid.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http")))
	defer client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	events := make(chan hyperliquid.WsUserEvent, 1)
	if err := client.SubscribeUserEvents("0xabc", func(event hyperliquid.WsUserEvent) { events <- event }); err != nil {
		t.Fatalf("SubscribeUserEvents: %v", err)
	}
	select {
	case req := <-requests:
		data, _ := json.Marshal(req.Subscription)
		if !strings.Contains(string(data), `"type":"userEvents"`) {
			t.Fatalf("subscription=%s, want official userEvents request", data)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription")
	}
	select {
	case event := <-events:
		if len(event.NonUserCancel) != 1 || event.NonUserCancel[0].Oid != 7 {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("user response channel did not dispatch")
	}
}

func TestUserFillsSubscriptionUsesExplicitAggregationDefault(t *testing.T) {
	requests := make(chan hyperliquid.WsSubscribeRequest, 2)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for i := 0; i < 2; i++ {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req hyperliquid.WsSubscribeRequest
			if json.Unmarshal(raw, &req) != nil {
				return
			}
			requests <- req
			if req.Method != "subscribe" {
				continue
			}
			encoded, _ := json.Marshal(req.Subscription)
			var acknowledged map[string]any
			_ = json.Unmarshal(encoded, &acknowledged)
			acknowledged["aggregateByTime"] = false
			if user, ok := acknowledged["user"].(string); ok {
				acknowledged["user"] = strings.ToLower(user)
			}
			_ = conn.WriteJSON(map[string]any{
				"channel": "subscriptionResponse",
				"data": map[string]any{
					"method":       "subscribe",
					"subscription": acknowledged,
				},
			})
		}
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := hyperliquid.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	base.SubscriptionAckTimeout = 100 * time.Millisecond
	client := NewWebsocketClient(base)
	defer client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	user := "0x000000000000000000000000000000000000dEaD"
	if err := client.SubscribeUserFillsConfirmed(user, func(hyperliquid.WsUserFills) {}); err != nil {
		t.Fatalf("SubscribeUserFillsConfirmed: %v", err)
	}
	if err := client.UnsubscribeUserFills(user); err != nil {
		t.Fatalf("UnsubscribeUserFills: %v", err)
	}

	for _, method := range []string{"subscribe", "unsubscribe"} {
		select {
		case req := <-requests:
			if req.Method != method {
				t.Fatalf("method=%q, want %q", req.Method, method)
			}
			encoded, _ := json.Marshal(req.Subscription)
			var subscription map[string]any
			_ = json.Unmarshal(encoded, &subscription)
			if aggregate, ok := subscription["aggregateByTime"].(bool); !ok || aggregate {
				t.Fatalf("%s userFills aggregateByTime=%v, want explicit false", method, subscription["aggregateByTime"])
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s request", method)
		}
	}
}

func TestSpotStateSubscriptionUsesOfficialRequestAndPayload(t *testing.T) {
	requests := make(chan hyperliquid.WsSubscribeRequest, 2)
	serverErrors := make(chan error, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrors <- err
			return
		}
		defer conn.Close()
		_, raw, err := conn.ReadMessage()
		if err != nil {
			serverErrors <- err
			return
		}
		var req hyperliquid.WsSubscribeRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			serverErrors <- err
			return
		}
		requests <- req
		encoded, _ := json.Marshal(req.Subscription)
		var acknowledged map[string]any
		_ = json.Unmarshal(encoded, &acknowledged)
		if user, ok := acknowledged["user"].(string); ok {
			acknowledged["user"] = strings.ToLower(user)
		}
		if portfolio, ok := acknowledged["isPortfolioMargin"]; ok {
			delete(acknowledged, "isPortfolioMargin")
			acknowledged["ignorePortfolioMargin"] = portfolio
		}
		if err := conn.WriteJSON(map[string]any{
			"channel": "subscriptionResponse",
			"data": map[string]any{
				"method":       "subscribe",
				"subscription": acknowledged,
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		if err := conn.WriteJSON(map[string]any{
			"channel": "spotState",
			"data": map[string]any{
				"user": acknowledged["user"],
				"spotState": map[string]any{
					"balances": []any{map[string]any{
						"coin": "PURR", "token": 1, "hold": "0.25", "total": "2", "entryNtl": "1.5",
					}},
				},
			},
		}); err != nil {
			serverErrors <- err
			return
		}
		_, raw, err = conn.ReadMessage()
		if err != nil {
			serverErrors <- err
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			serverErrors <- err
			return
		}
		requests <- req
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := hyperliquid.NewWebsocketClient(ctx).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	client := NewWebsocketClient(base)
	defer client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	user := "0x000000000000000000000000000000000000dEaD"
	states := make(chan hyperliquid.SpotClearinghouseState, 1)
	if err := client.SubscribeSpotStateConfirmed(user, false, func(state hyperliquid.SpotClearinghouseState) { states <- state }); err != nil {
		t.Fatalf("SubscribeSpotStateConfirmed: %v", err)
	}
	select {
	case state := <-states:
		if len(state.Balances) != 1 || state.Balances[0].Coin != "PURR" || state.Balances[0].Hold != "0.25" || state.Balances[0].Total != "2" {
			t.Fatalf("state=%+v, want decoded Spot state", state)
		}
	case err := <-serverErrors:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Spot state")
	}
	if err := client.UnsubscribeSpotState(user, false); err != nil {
		t.Fatalf("UnsubscribeSpotState: %v", err)
	}

	for _, method := range []string{"subscribe", "unsubscribe"} {
		select {
		case req := <-requests:
			if req.Method != method {
				t.Fatalf("method=%q, want %q", req.Method, method)
			}
			encoded, _ := json.Marshal(req.Subscription)
			var subscription map[string]any
			_ = json.Unmarshal(encoded, &subscription)
			portfolio, ok := subscription["isPortfolioMargin"].(bool)
			if subscription["type"] != "spotState" || subscription["user"] != user || !ok || portfolio {
				t.Fatalf("%s subscription has wrong official fields", method)
			}
		case err := <-serverErrors:
			t.Fatal(err)
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s request", method)
		}
	}
}
