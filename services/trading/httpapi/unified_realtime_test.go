package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type realtimeArbitrageStub struct{}

func (realtimeArbitrageStub) Subscribe(context.Context, int) (arbitrageProjectionSnapshot, bool, <-chan arbitrageProjectionSnapshot, func(), error) {
	updates := make(chan arbitrageProjectionSnapshot)
	return arbitrageProjectionSnapshot{}, false, updates, func() { close(updates) }, nil
}

func TestUnifiedRealtimeRejectsTopicsOwnedByGateway(t *testing.T) {
	api := &PublicDataAPI{dependencies: PublicDataDependencies{
		Arbitrage: realtimeArbitrageStub{},
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
	moved := make(map[realtimewire.Topic]bool)
	for len(moved) != 2 {
		var message realtimewire.Message
		if err := wsjson.Read(ctx, connection, &message); err != nil {
			t.Fatal(err)
		}
		if message.Type == realtimewire.MessageError && message.Code == "TOPIC_MOVED_TO_GATEWAY" {
			moved[message.Topic] = true
		}
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
