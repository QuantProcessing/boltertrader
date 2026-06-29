package sdk

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/exchanges/internal/testenv"
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
