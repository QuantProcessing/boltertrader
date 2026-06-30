package sdk

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestPrivateWSTradeClassicCompanion_FirstClassicAck(t *testing.T) {
	resp := &classicTradeResponse{Arg: []classicTradeAck{{ID: "1"}}}
	if firstClassicAck(resp).ID != "1" {
		t.Fatalf("unexpected first ack: %+v", firstClassicAck(resp))
	}
}

func TestPrivateWSClient_PlaceClassicSpotOrderWS(t *testing.T) {
	client := newLiveClassicTradeWSClient(t, "BITGET_TEST_ORDER_QTY", "BITGET_TEST_ORDER_PRICE")
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	resp, err := client.PlaceClassicSpotOrderWS(&PlaceOrderRequest{
		Symbol:      symbol,
		Qty:         os.Getenv("BITGET_TEST_ORDER_QTY"),
		Side:        bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:   "limit",
		Price:       os.Getenv("BITGET_TEST_ORDER_PRICE"),
		TimeInForce: "gtc",
		ClientOID:   bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceClassicSpotOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected classic spot order response")
	}
}

func TestPrivateWSClient_CancelClassicSpotOrderWS(t *testing.T) {
	client := newLiveClassicTradeWSClient(t, "BITGET_TEST_ORDER_ID")
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	resp, err := client.CancelClassicSpotOrderWS(symbol, os.Getenv("BITGET_TEST_ORDER_ID"), "")
	if err != nil {
		t.Fatalf("CancelClassicSpotOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected classic spot cancel response")
	}
}

func TestPrivateWSClient_PlaceClassicPerpOrderWS(t *testing.T) {
	client := newLiveClassicTradeWSClient(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL", "BITGET_TEST_ORDER_QTY")
	resp, err := client.PlaceClassicPerpOrderWS(&PlaceOrderRequest{
		Symbol:     os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		Qty:        os.Getenv("BITGET_TEST_ORDER_QTY"),
		Side:       bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:  bitgetEnvOrDefault("BITGET_TEST_ORDER_TYPE", "market"),
		Price:      os.Getenv("BITGET_TEST_ORDER_PRICE"),
		ClientOID:  bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
		MarginMode: bitgetEnvOrDefault("BITGET_TEST_MARGIN_MODE", "crossed"),
		TradeSide:  bitgetEnvOrDefault("BITGET_TEST_TRADE_SIDE", "open"),
		ReduceOnly: "no",
	}, bitgetPerpCategory, bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"))
	if err != nil {
		t.Fatalf("PlaceClassicPerpOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected classic perp order response")
	}
}

func TestPrivateWSClient_CancelClassicPerpOrderWS(t *testing.T) {
	client := newLiveClassicTradeWSClient(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL", "BITGET_TEST_ORDER_ID")
	resp, err := client.CancelClassicPerpOrderWS(
		os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		bitgetPerpCategory,
		bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"),
		os.Getenv("BITGET_TEST_ORDER_ID"),
		"",
	)
	if err != nil {
		t.Fatalf("CancelClassicPerpOrderWS: %v", err)
	}
	if resp == nil {
		t.Fatal("expected classic perp cancel response")
	}
}

func newLiveClassicTradeWSClient(t *testing.T, vars ...string) *PrivateWSClient {
	t.Helper()
	required := append([]string{"BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE"}, vars...)
	testenv.RequireLiveWrite(t, bitgetLiveWriteFlag, required...)
	client := NewPrivateWSClient().
		WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE")).
		WithClassicMode()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect classic private WS: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}
