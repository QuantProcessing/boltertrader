package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	realtimewire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestInternalAccountStreamPublishesAllOwnedAccountsWithoutCursorReplay(t *testing.T) {
	hub, err := gatewayevent.NewHub(gatewayevent.Options{
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	if _, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "principal-a", AccountID: "acct-b", EventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c40",
		ResourceVersion: 1, Kind: gatewayevent.KindAccountConnection, OccurredAt: time.Now().UTC(), Payload: []byte(`{"account_id":"acct-b","route_id":"perp-usdt","state":"connected"}`),
	}); err != nil {
		t.Fatal(err)
	}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: &fakeInternalAuthority{}, Authenticate: func(*http.Request) error { return nil },
		AccountEvents: hub, AccountEventOwner: "principal-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/internal/v1/account/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if err := wsjson.Write(ctx, connection, realtimewire.Control{
		Operation: realtimewire.OperationSubscribe, RequestID: "account-subscribe",
		Topics: []realtimewire.TopicRequest{{Topic: realtimewire.TopicAccountEvents}},
	}); err != nil {
		t.Fatal(err)
	}
	var ack, connectionSnapshot realtimewire.Message
	if err := wsjson.Read(ctx, connection, &ack); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, connection, &connectionSnapshot); err != nil {
		t.Fatal(err)
	}
	if ack.Type != realtimewire.MessageAck || connectionSnapshot.Type != realtimewire.MessageDelta ||
		!strings.Contains(string(connectionSnapshot.Data), `"kind":"account.account_connection"`) {
		t.Fatalf("initial messages=%+v %+v", ack, connectionSnapshot)
	}
	if _, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "principal-a", AccountID: "acct-b", EventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c44",
		ResourceVersion: 2, Kind: gatewayevent.KindPosition, OccurredAt: time.Now().UTC(), Payload: []byte(`{"quantity":"0.1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var update realtimewire.Message
	if err := wsjson.Read(ctx, connection, &update); err != nil {
		t.Fatal(err)
	}
	if update.Type != realtimewire.MessageDelta || !strings.Contains(string(update.Data), `"kind":"account.position"`) || !strings.Contains(string(update.Data), `"accountId":"acct-b"`) {
		t.Fatalf("account update=%+v data=%s", update, update.Data)
	}
	if _, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "principal-a", AccountID: "acct-b", EventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c45",
		ResourceVersion: 2, Kind: gatewayevent.KindAccountConnection, OccurredAt: time.Now().UTC(), Payload: []byte(`{"account_id":"acct-b","route_id":"perp-usdt","state":"disconnected","reason":"transport_closed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var degraded realtimewire.Message
	if err := wsjson.Read(ctx, connection, &degraded); err != nil {
		t.Fatal(err)
	}
	if degraded.Type != realtimewire.MessageDelta || !strings.Contains(string(degraded.Data), `"state":"disconnected"`) {
		t.Fatalf("account degraded message=%+v", degraded)
	}
	if _, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "principal-a", AccountID: "acct-b", EventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c46",
		ResourceVersion: 3, Kind: gatewayevent.KindAccountConnection, OccurredAt: time.Now().UTC(), Payload: []byte(`{"account_id":"acct-b","route_id":"perp-usdt","state":"connected"}`),
	}); err != nil {
		t.Fatal(err)
	}
	var recovered realtimewire.Message
	if err := wsjson.Read(ctx, connection, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Type != realtimewire.MessageDelta || !strings.Contains(string(recovered.Data), `"state":"connected"`) {
		t.Fatalf("account recovery message=%+v data=%s", recovered, recovered.Data)
	}
}
