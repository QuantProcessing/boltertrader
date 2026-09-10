package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/services/authedge/session"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestRealtimeMultiplexesOwnedTopicsAndProtectsPrivateSubscriptions(t *testing.T) {
	for _, signedIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "signed-in"}[signedIn], func(t *testing.T) {
			requests := make(chan *http.Request, 8)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.Clone(context.Background())
				c, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer c.CloseNow()
				for {
					var control wire.Control
					if wsjson.Read(r.Context(), c, &control) != nil {
						return
					}
					for _, topic := range control.Topics {
						id := "same-id"
						if topic.SubscriptionID != "" {
							id = topic.SubscriptionID
						}
						if wsjson.Write(r.Context(), c, wire.Message{Type: wire.MessageAck, RequestID: control.RequestID, SubscriptionID: id, Topic: topic.Topic}) != nil {
							return
						}
						if control.Operation == wire.OperationSubscribe {
							if wsjson.Write(r.Context(), c, wire.Message{Type: wire.MessageSnapshot, SubscriptionID: id, Topic: topic.Topic, Data: wire.EncodeData(map[string]string{"path": r.URL.Path})}) != nil {
								return
							}
						}
					}
				}
			}))
			defer upstream.Close()
			sessions := &fakeSessions{principal: session.Principal{ID: "owner"}}
			p, err := New(Options{Gateway: Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken}, Trading: Upstream{BaseURL: mustURL(t, upstream.URL), Token: testToken}, Sessions: sessions})
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			if _, err := p.Register(mux); err != nil {
				t.Fatal(err)
			}
			edge := httptest.NewServer(mux)
			defer edge.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			headers := make(http.Header)
			headers.Set(principalHeader, "forged")
			if signedIn {
				headers.Set("Cookie", session.CookieName+"=session-token")
			}
			c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(edge.URL, "http")+"/api/v1/realtime/ws", &websocket.DialOptions{HTTPHeader: headers})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			topics := []wire.Topic{wire.TopicVenueConnectivity, wire.TopicAccountEvents, wire.TopicTradingOrders}
			active := map[string]bool{}
			for _, topic := range topics {
				if err := wsjson.Write(ctx, c, wire.Control{Operation: wire.OperationSubscribe, RequestID: string(topic), Topics: []wire.TopicRequest{{Topic: topic}}}); err != nil {
					t.Fatal(err)
				}
				var ack wire.Message
				if err := wsjson.Read(ctx, c, &ack); err != nil {
					t.Fatal(err)
				}
				if !signedIn && topic != wire.TopicVenueConnectivity {
					if ack.Code != "AUTH_REQUIRED" {
						t.Fatalf("private subscription: %+v", ack)
					}
					continue
				}
				if ack.Type != wire.MessageAck || active[ack.SubscriptionID] {
					t.Fatalf("subscription identities collide: %+v", ack)
				}
				active[ack.SubscriptionID] = true
				var snapshot wire.Message
				if err := wsjson.Read(ctx, c, &snapshot); err != nil {
					t.Fatal(err)
				}
				if snapshot.SubscriptionID != ack.SubscriptionID || snapshot.Type != wire.MessageSnapshot {
					t.Fatalf("snapshot: %+v", snapshot)
				}
				request := <-requests
				wantPrincipal := ""
				if signedIn {
					wantPrincipal = "owner"
				}
				if request.Header.Get(principalHeader) != wantPrincipal || request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "Bearer "+testToken {
					t.Fatal("upstream identity was not sanitized")
				}
			}
			for id := range active {
				if err := wsjson.Write(ctx, c, wire.Control{Operation: wire.OperationUnsubscribe, RequestID: "cancel", Topics: []wire.TopicRequest{{SubscriptionID: id}}}); err != nil {
					t.Fatal(err)
				}
				var ack wire.Message
				if err := wsjson.Read(ctx, c, &ack); err != nil || ack.SubscriptionID != id {
					t.Fatalf("cancel: %+v %v", ack, err)
				}
			}
			if err := wsjson.Write(ctx, c, wire.Control{Operation: wire.OperationPing, RequestID: "alive"}); err != nil {
				t.Fatal(err)
			}
			var pong wire.Message
			if err := wsjson.Read(ctx, c, &pong); err != nil || pong.Type != wire.MessagePong {
				t.Fatalf("heartbeat: %+v %v", pong, err)
			}
		})
	}
}

func TestRealtimeExpiredCookieCannotBecomeAnonymous(t *testing.T) {
	p, _ := New(Options{Gateway: Upstream{BaseURL: mustURL(t, "http://gateway.invalid"), Token: testToken}, Trading: Upstream{BaseURL: mustURL(t, "http://trading.invalid"), Token: testToken}, Sessions: &fakeSessions{err: session.ErrInvalidSession}})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/ws", nil)
	r.AddCookie(&http.Cookie{Name: session.CookieName, Value: "expired"})
	w := httptest.NewRecorder()
	p.realtime(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", w.Code)
	}
}
