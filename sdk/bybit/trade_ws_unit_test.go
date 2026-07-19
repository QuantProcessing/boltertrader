package sdk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestTradeWSClientOrderCommandsUseTradeFramesAndReturnACK(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()

		var auth map[string]any
		if err := conn.ReadJSON(&auth); err != nil {
			t.Errorf("read auth: %v", err)
			return
		}
		if auth["op"] != "auth" {
			t.Errorf("auth op = %v", auth["op"])
			return
		}
		if err := conn.WriteJSON(map[string]any{"op": "auth", "retCode": 0, "retMsg": "OK"}); err != nil {
			return
		}

		for index := 0; index < 2; index++ {
			var request struct {
				ReqID string            `json:"reqId"`
				Op    string            `json:"op"`
				Args  []json.RawMessage `json:"args"`
			}
			if err := conn.ReadJSON(&request); err != nil {
				t.Errorf("read trade request: %v", err)
				return
			}
			wantOp := "order.create"
			orderID := "51"
			orderLinkID := "client-51"
			if index == 1 {
				wantOp = "order.cancel"
				orderID = "51"
				orderLinkID = ""
			}
			if request.Op != wantOp || request.ReqID == "" || len(request.Args) != 1 {
				t.Errorf("trade request = %+v, want op %s with one arg", request, wantOp)
				return
			}
			if err := conn.WriteJSON(map[string]any{
				"reqId":   request.ReqID,
				"op":      request.Op,
				"retCode": 0,
				"retMsg":  "OK",
				"data": map[string]string{
					"orderId":     orderID,
					"orderLinkId": orderLinkID,
				},
			}); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	profile := TestnetEnvironmentProfile()
	profile.TradeWSURL = "ws" + strings.TrimPrefix(server.URL, "http")
	client, err := NewTradeWSClientWithProfile(profile)
	if err != nil {
		t.Fatalf("NewTradeWSClientWithProfile: %v", err)
	}
	client.WithCredentials("key", "secret")
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	placed, err := client.PlaceOrderWithResponse(ctx, PlaceOrderRequest{
		Category:    "linear",
		Symbol:      "BTCUSDT",
		Side:        "Buy",
		OrderType:   "Limit",
		Qty:         "1",
		Price:       "100",
		TimeInForce: "GTC",
		OrderLinkID: "client-51",
	})
	if err != nil {
		t.Fatalf("PlaceOrderWithResponse: %v", err)
	}
	if placed.OrderID != "51" || placed.OrderLinkID != "client-51" {
		t.Fatalf("place ACK = %+v", placed)
	}

	canceled, err := client.CancelOrderWithResponse(ctx, CancelOrderRequest{Category: "linear", Symbol: "BTCUSDT", OrderID: "51"})
	if err != nil {
		t.Fatalf("CancelOrderWithResponse: %v", err)
	}
	if canceled.OrderID != "51" {
		t.Fatalf("cancel ACK = %+v", canceled)
	}
}
