package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type realtimeTestSubscription struct {
	events chan realtimewire.Message
	once   sync.Once
}

func (subscription *realtimeTestSubscription) Events() <-chan realtimewire.Message {
	return subscription.events
}
func (subscription *realtimeTestSubscription) Close() {
	subscription.once.Do(func() { close(subscription.events) })
}

type realtimeMarketTopicsStub struct{}

func (realtimeMarketTopicsStub) Subscribe(ctx context.Context, request realtimewire.TopicRequest, _ int) (realtimewire.Subscription, error) {
	subscription := &realtimeTestSubscription{events: make(chan realtimewire.Message, 1)}
	subscription.events <- realtimewire.Message{
		Type: realtimewire.MessageSnapshot, Topic: request.Topic, Generation: 1, Sequence: 1,
		ObservedAt: time.Now().UTC(), Data: realtimewire.EncodeData(map[string]string{"price": "100"}),
	}
	go func() {
		<-ctx.Done()
		subscription.Close()
	}()
	return subscription, nil
}

func TestUnifiedRealtimeRejectsOnlyAnonymousPrivateTopic(t *testing.T) {
	hub, err := realtime.NewHub(8, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	api := &PublicDataAPI{dependencies: PublicDataDependencies{
		MarketTopics: realtimeMarketTopicsStub{}, Realtime: hub,
		Principal: func(*http.Request) (string, error) { return "", errors.New("anonymous") },
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/realtime/ws", api.unifiedRealtime)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/realtime/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	control := realtimewire.Control{Operation: realtimewire.OperationSubscribe, RequestID: "mixed", Topics: []realtimewire.TopicRequest{
		{Topic: realtimewire.TopicMarkPrice, InstrumentID: "instrument"},
		{Topic: realtimewire.TopicAccountEvents},
	}}
	if err := wsjson.Write(ctx, connection, control); err != nil {
		t.Fatal(err)
	}
	var publicSnapshot, authError bool
	for !publicSnapshot || !authError {
		var message realtimewire.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			t.Fatal(err)
		}
		publicSnapshot = publicSnapshot || message.Type == realtimewire.MessageSnapshot && message.Topic == realtimewire.TopicMarkPrice
		authError = authError || message.Type == realtimewire.MessageError && message.Topic == realtimewire.TopicAccountEvents && message.Code == "AUTH_REQUIRED"
	}
	if err := wsjson.Write(ctx, connection, realtimewire.Control{Operation: realtimewire.OperationPing, RequestID: "still-open"}); err != nil {
		t.Fatal(err)
	}
	for {
		var message realtimewire.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type == realtimewire.MessagePong && message.RequestID == "still-open" {
			break
		}
	}
}

func TestAccountTopicExpiredCursorRequiresFullResnapshot(t *testing.T) {
	hub, err := realtime.NewHub(1, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := hub.PublishScoped("principal", "account.order", map[string]string{"id": "first"})
	_, _ = hub.PublishScoped("principal", "account.fill", map[string]string{"id": "second"})
	api := &PublicDataAPI{dependencies: PublicDataDependencies{Realtime: hub}}
	var messages []realtimewire.Message
	err = api.runBrowserAccountTopic(t.Context(), "principal", "account-1", realtimewire.TopicRequest{Topic: realtimewire.TopicAccountEvents, Cursor: first.Cursor}, func(message realtimewire.Message) bool {
		messages = append(messages, message)
		return true
	})
	if err != nil {
		t.Fatalf("expired cursor returned transport error: %v", err)
	}
	if len(messages) != 1 || messages[0].Type != realtimewire.MessageResyncRequired || messages[0].Code != "CURSOR_EXPIRED" {
		t.Fatalf("expired cursor messages = %#v", messages)
	}
}
