package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestTradingStreamPushesPersistedOwnedOrdersAndCanonicalFills(t *testing.T) {
	service := newCommandService(orders.NewMemoryStore(), time.Now)
	create := func(principal, key string) orders.Command {
		c, _, e := service.Create(t.Context(), orders.CreateRequest{PrincipalID: principal, AccountID: "a", ProductID: "manual", LineageID: key, IdempotencyKey: key, Method: "POST", Path: "/commands", CommandKind: "manual.place", Payload: []byte(`{}`)})
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	own := create("owner", "own")
	create("other", "private")
	mux := http.NewServeMux()
	e := RegisterTradingRoutes(mux, TradingOptions{Commands: service, Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now), Principal: func(*http.Request) (string, error) { return "owner", nil }, AuthorizeMutation: func(*http.Request) error { return nil }})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, _, e := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/api/v1/trading/realtime/ws", nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.CloseNow()
	if e := wsjson.Write(ctx, c, wire.Control{Operation: wire.OperationSubscribe, RequestID: "orders", Topics: []wire.TopicRequest{{Topic: wire.TopicTradingOrders}}}); e != nil {
		t.Fatal(e)
	}
	var ack, initial wire.Message
	if e := wsjson.Read(ctx, c, &ack); e != nil {
		t.Fatal(e)
	}
	if e := wsjson.Read(ctx, c, &initial); e != nil {
		t.Fatal(e)
	}
	var data struct {
		Orders []orders.Command `json:"orders"`
		Fills  []FillProjection `json:"fills"`
	}
	if json.Unmarshal(initial.Data, &data) != nil || len(data.Orders) != 1 || data.Orders[0].CommandID != own.CommandID {
		t.Fatalf("unscoped snapshot: %s", initial.Data)
	}
	_, e = service.Transition(ctx, orders.TransitionRequest{CommandID: own.CommandID, State: orders.StateFilled, Evidence: orders.ExecutionEvidence{CommandID: "gateway", GatewayOrderID: "venue-order", ClientOrderID: "client", State: "filled", ResourceVersion: 2, Fills: []orders.FillEvidence{{FillID: "f", AccountID: "a", InstrumentID: "btc", Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT"}}}})
	if e != nil {
		t.Fatal(e)
	}
	var updated wire.Message
	if e := wsjson.Read(ctx, c, &updated); e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(updated.Data, &data) != nil || data.Orders[0].State != orders.StateFilled || len(data.Fills) != 1 || data.Fills[0].CommandID != own.CommandID || data.Fills[0].LineageID != "own" || updated.Sequence <= initial.Sequence {
		t.Fatalf("missing canonical fill push: %s", updated.Data)
	}
}
