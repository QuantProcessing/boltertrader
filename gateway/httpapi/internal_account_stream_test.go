package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	gatewayevent "github.com/QuantProcessing/boltertrader/gateway/event"
	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
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
	var ack, checkpoint realtimewire.Message
	if err := wsjson.Read(ctx, connection, &ack); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, connection, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if ack.Type != realtimewire.MessageAck || checkpoint.Type != realtimewire.MessageCheckpoint {
		t.Fatalf("initial messages=%+v %+v", ack, checkpoint)
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
	if _, err := hub.PublishResync("principal-a", "acct-b", "private-stream-gap"); err != nil {
		t.Fatal(err)
	}
	var degraded realtimewire.Message
	if err := wsjson.Read(ctx, connection, &degraded); err != nil {
		t.Fatal(err)
	}
	if degraded.Type != realtimewire.MessageResyncRequired || degraded.Code != "UPSTREAM_GAP" {
		t.Fatalf("account degraded message=%+v", degraded)
	}
	if _, err := hub.Publish(gatewayevent.PublishRequest{
		PrincipalID: "principal-a", AccountID: "acct-b", EventID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c45",
		ResourceVersion: 3, Kind: gatewayevent.KindAccountHealth, OccurredAt: time.Now().UTC(), Payload: []byte(`{"realtime_ready":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	var recovered realtimewire.Message
	if err := wsjson.Read(ctx, connection, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.Type != realtimewire.MessageDelta || !strings.Contains(string(recovered.Data), `"kind":"account.account_health"`) {
		t.Fatalf("account recovery message=%+v data=%s", recovered, recovered.Data)
	}
}
