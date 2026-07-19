package sdk

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPrivateWSTradeUTACompanion_FirstUTAAck(t *testing.T) {
	resp := &utaTradeResponse{Args: []utaTradeAck{{OrderID: "1", ClientOID: "c1"}}}
	ack := firstUTAAck(resp)
	if ack.OrderID != "1" || ack.ClientOID != "c1" {
		t.Fatalf("unexpected first ack: %+v", ack)
	}
}

func TestPrivateWSTradeUTACompanion_PruneTradeArgs(t *testing.T) {
	args := pruneUTATradeArgs(map[string]any{
		"symbol":    "BTCUSDT",
		"price":     "",
		"clientOid": "client-1",
		"nil":       nil,
	})
	if args["symbol"] != "BTCUSDT" || args["clientOid"] != "client-1" {
		t.Fatalf("expected non-empty values to remain: %#v", args)
	}
	if _, ok := args["price"]; ok {
		t.Fatalf("expected empty string value to be pruned: %#v", args)
	}
	if _, ok := args["nil"]; ok {
		t.Fatalf("expected nil value to be pruned: %#v", args)
	}
}

func TestPrivateWSTradeUTACommandHonorsContextAfterConnect(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.requestTimeout = 10 * time.Second
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.mu.Unlock()
	defer client.Close()
	defer peer.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.PlaceUTAOrderWSContext(ctx, &PlaceOrderRequest{
			Category:    "spot",
			Symbol:      "BTCUSDT",
			Qty:         "1",
			Side:        "buy",
			OrderType:   "limit",
			Price:       "100",
			TimeInForce: "gtc",
			PosSide:     "short",
		})
		result <- err
	}()

	var request utaTradeRequest
	if err := peer.ReadJSON(&request); err != nil {
		t.Fatalf("read UTA trade request: %v", err)
	}
	if request.Op != "trade" || request.Topic != "place-order" {
		t.Fatalf("trade request = %+v", request)
	}
	if len(request.Args) != 1 || request.Args[0]["posSide"] != "short" {
		t.Fatalf("trade args = %#v, want hedge position side", request.Args)
	}
	if _, exists := request.Args[0]["reduceOnly"]; exists {
		t.Fatalf("trade args assign reduceOnly with hedge position side: %#v", request.Args)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("PlaceUTAOrderWS error = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PlaceUTAOrderWS did not return promptly after context cancellation")
	}
}

func TestPrivateWSTradeUTAReturnsTypedRedactedVenueError(t *testing.T) {
	clientConn, peer := bitgetPrivateWSPair(t)
	client := NewPrivateWSClient().WithCredentials("key", "secret", "passphrase")
	client.mu.Lock()
	client.conn = clientConn
	client.authenticated = true
	client.mu.Unlock()
	defer client.Close()
	defer peer.Close()
	go client.readLoop(clientConn, make(chan error, 1))

	result := make(chan error, 1)
	go func() {
		_, err := client.PlaceUTAOrderWSContext(context.Background(), &PlaceOrderRequest{
			Category:  "usdt-futures",
			Symbol:    "BTCUSDT",
			Qty:       "1",
			Side:      "buy",
			OrderType: "market",
			PosSide:   "long",
		})
		result <- err
	}()

	var request utaTradeRequest
	if err := peer.ReadJSON(&request); err != nil {
		t.Fatalf("read UTA trade request: %v", err)
	}
	const venueMessage = "SENTINEL_PRIVATE_WS_MESSAGE"
	if err := peer.WriteJSON(map[string]any{
		"event": "trade",
		"id":    request.ID,
		"topic": request.Topic,
		"code":  "25236",
		"msg":   venueMessage,
	}); err != nil {
		t.Fatalf("write UTA trade response: %v", err)
	}
	err := <-result
	var responseErr *ResponseError
	if !errors.As(err, &responseErr) || responseErr.Code != "25236" {
		t.Fatalf("error=%v (%T), want typed response code 25236", err, err)
	}
	if strings.Contains(err.Error(), venueMessage) {
		t.Fatalf("private WS response leaked venue message: %v", err)
	}
}

func TestPrivateWSClient_PlaceUTAOrderWS(t *testing.T) {
	client := newLiveUTATradeWSClient(t, "BITGET_TEST_SYMBOL", "BITGET_TEST_ORDER_QTY", "BITGET_TEST_ORDER_PRICE")

	resp, err := client.PlaceUTAOrderWS(&PlaceOrderRequest{
		Category:    bitgetEnvOrDefault("BITGET_TEST_CATEGORY", "spot"),
		Symbol:      os.Getenv("BITGET_TEST_SYMBOL"),
		Qty:         os.Getenv("BITGET_TEST_ORDER_QTY"),
		Side:        bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:   "limit",
		Price:       os.Getenv("BITGET_TEST_ORDER_PRICE"),
		TimeInForce: "gtc",
		ClientOID:   bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceUTAOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected UTA order response")
	}
}

func TestPrivateWSClient_CancelUTAOrderWS(t *testing.T) {
	client := newLiveUTATradeWSClient(t, "BITGET_TEST_ORDER_ID")

	resp, err := client.CancelUTAOrderWS(&CancelOrderRequest{
		Category:  bitgetEnvOrDefault("BITGET_TEST_CATEGORY", "spot"),
		OrderID:   os.Getenv("BITGET_TEST_ORDER_ID"),
		ClientOID: bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	})
	if err != nil {
		t.Fatalf("CancelUTAOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected UTA cancel response")
	}
}

func newLiveUTATradeWSClient(t *testing.T, vars ...string) *PrivateWSClient {
	t.Helper()
	required := append([]string{"BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE"}, vars...)
	testenv.RequireLiveWrite(t, bitgetLiveWriteFlag, required...)
	client := NewPrivateWSClient().
		WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect UTA private WS: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
