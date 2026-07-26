package httpapi

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestPrivateWebSocketAuthenticatesFiltersAndPublishesNormalizedEvents(t *testing.T) {
	server, hub := newWebSocketServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	unauthorized, response, err := websocket.Dial(ctx, websocketURL(server.URL)+"/v1/ws?account_id=acct-a", nil)
	if unauthorized != nil {
		_ = unauthorized.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized dial response=%v err=%v", response, err)
	}

	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	connection, response, err := websocket.Dial(ctx,
		websocketURL(server.URL)+"/v1/ws?account_id=acct-a&kind=order",
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("websocket dial response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })

	publishHubEvent(t, hub, "acct-b", gatewayevent.KindOrder, `{"state":"open"}`)
	publishHubEvent(t, hub, "acct-a", gatewayevent.KindBalance, `{"asset":"USDT"}`)
	want := publishHubEvent(t, hub, "acct-a", gatewayevent.KindOrder, `{"state":"open"}`)

	var got gatewayevent.Event
	if err := wsjson.Read(ctx, connection, &got); err != nil {
		t.Fatal(err)
	}
	if got.EventID != want.EventID || got.AccountID != "acct-a" || got.Kind != gatewayevent.KindOrder {
		t.Fatalf("websocket event=%+v want=%+v", got, want)
	}
}

func TestPrivateWebSocketEmitsResyncRequiredForStaleBootAndThenCloses(t *testing.T) {
	server, _ := newWebSocketServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	connection, response, err := websocket.Dial(ctx,
		websocketURL(server.URL)+"/v1/ws?account_id=acct-a&cursor=018f0000-0000-7000-8000-000000000099:1",
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("websocket dial response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })

	var event gatewayevent.Event
	if err := wsjson.Read(ctx, connection, &event); err != nil {
		t.Fatal(err)
	}
	if event.Kind != gatewayevent.KindResyncRequired {
		t.Fatalf("event=%+v", event)
	}
	var payload gatewayevent.ResyncPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reason != "process_restarted" || len(payload.Resources) != 5 {
		t.Fatalf("resync payload=%+v", payload)
	}
}

func TestPrivateWebSocketRejectsUnknownAccountKindAndCursorBeforeUpgrade(t *testing.T) {
	server, _ := newWebSocketServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	tests := []string{
		"/v1/ws",
		"/v1/ws?account_id=missing",
		"/v1/ws?account_id=acct-a&kind=public_trade",
		"/v1/ws?account_id=acct-a&cursor=bad",
	}
	for _, path := range tests {
		connection, response, err := websocket.Dial(ctx, websocketURL(server.URL)+path,
			&websocket.DialOptions{HTTPHeader: header})
		if connection != nil {
			_ = connection.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode == http.StatusSwitchingProtocols {
			t.Errorf("%s response=%v err=%v", path, response, err)
		}
	}
}

func TestPrivateWebSocketHandlesPingAndClientGracefulClose(t *testing.T) {
	server, _ := newWebSocketServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	connection, response, err := websocket.Dial(ctx,
		websocketURL(server.URL)+"/v1/ws?account_id=acct-a",
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("websocket dial response=%v err=%v", response, err)
	}
	readContext := connection.CloseRead(ctx)
	if err := connection.Ping(readContext); err != nil {
		_ = connection.CloseNow()
		t.Fatalf("websocket ping: %v", err)
	}
	if err := connection.Close(websocket.StatusNormalClosure, "client_done"); err != nil {
		t.Fatalf("graceful client close: %v", err)
	}
}

func TestPrivateWebSocketClosesGracefullyWhenEventHubStops(t *testing.T) {
	server, hub := newWebSocketServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer test-token"}}
	connection, response, err := websocket.Dial(ctx,
		websocketURL(server.URL)+"/v1/ws?account_id=acct-a",
		&websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatalf("websocket dial response=%v err=%v", response, err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })

	hub.Close()
	var event gatewayevent.Event
	err = wsjson.Read(ctx, connection, &event)
	if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("hub shutdown read error=%v status=%d", err, websocket.CloseStatus(err))
	}
}

func TestPrivateWebSocketSlowConsumerUsesStablePolicyClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer connection.CloseNow()
		closePrivateEventStream(connection, gatewayevent.ErrSlowConsumer)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, websocketURL(server.URL), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	var event gatewayevent.Event
	err = wsjson.Read(ctx, connection, &event)
	if websocket.CloseStatus(err) != websocket.StatusPolicyViolation ||
		!strings.Contains(err.Error(), "slow_consumer_resync_required") {
		t.Fatalf("slow consumer close error=%v status=%d", err, websocket.CloseStatus(err))
	}
}

func newWebSocketServer(t *testing.T) (*httptest.Server, *gatewayevent.Hub) {
	t.Helper()
	base := newHTTPFixture(t)
	identity := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
		Now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
		Random: strings.NewReader(strings.Repeat("w", 4096)),
	})
	hub, err := gatewayevent.NewHub(gatewayevent.Options{
		Capacity: 8, SubscriberBuffer: 4, Identity: identity,
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: base.accounts, References: &fakeHTTPReferences{accounts: base.accounts},
		Orders: base.orders, Auditor: &fakeHTTPAuditor{}, Identity: identity, Events: hub,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, err := auth.NewBearerMiddleware(context.Background(),
		secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(middleware.Wrap(handler))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server, hub
}

func publishHubEvent(t *testing.T, hub *gatewayevent.Hub, account domain.AccountID, kind gatewayevent.Kind, payload string) gatewayevent.Event {
	t.Helper()
	eventID, err := domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}).EventID()
	if err != nil {
		t.Fatal(err)
	}
	event, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "token-4c5dc9b7708905f7", AccountID: account, EventID: eventID,
		ResourceVersion: 1, Kind: kind, Payload: json.RawMessage(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func websocketURL(serverURL string) string {
	return "ws" + strings.TrimPrefix(serverURL, "http")
}
