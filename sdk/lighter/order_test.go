package lighter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestClientBuildCreateOrderTxSignsCreateOrderPayload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nextNonce" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":200,"nonce":123}`))
	}))
	defer server.Close()

	client := NewClient().WithCredentials(strings.Repeat("01", 40), 42, 7)
	client.BaseURL = server.URL
	tx, err := client.BuildCreateOrderTx(context.Background(), CreateOrderRequest{
		MarketId:      9,
		Price:         12345,
		BaseAmount:    678,
		IsAsk:         1,
		OrderType:     OrderTypeLimit,
		TimeInForce:   OrderTimeInForceGoodTillTime,
		ClientOrderId: 55,
		ReduceOnly:    1,
		TriggerPrice:  12000,
		OrderExpiry:   Default28DayOrderExpiry,
	})
	if err != nil {
		t.Fatalf("BuildCreateOrderTx: %v", err)
	}
	if tx["tx_type"] != "14" {
		t.Fatalf("unexpected tx_type: %s", tx["tx_type"])
	}
	var info struct {
		Nonce            int64
		AccountIndex     int64
		ApiKeyIndex      uint32
		MarketIndex      uint32
		ClientOrderIndex int64
		BaseAmount       int64
		Price            uint32
		IsAsk            uint32
		Type             uint32
		TimeInForce      uint32
		ReduceOnly       uint32
		TriggerPrice     uint32
		OrderExpiry      int64
		Sig              []byte
	}
	if err := json.Unmarshal([]byte(tx["tx_info"]), &info); err != nil {
		t.Fatalf("unmarshal tx_info: %v", err)
	}
	if info.Nonce != 123 || info.AccountIndex != 42 || info.ApiKeyIndex != 7 ||
		info.MarketIndex != 9 || info.ClientOrderIndex != 55 || info.BaseAmount != 678 ||
		info.Price != 12345 || info.IsAsk != 1 || info.Type != OrderTypeLimit ||
		info.TimeInForce != OrderTimeInForceGoodTillTime || info.ReduceOnly != 1 ||
		info.TriggerPrice != 12000 || info.OrderExpiry != Default28DayOrderExpiry {
		t.Fatalf("unexpected tx_info: %+v", info)
	}
	if len(info.Sig) == 0 {
		t.Fatal("expected signature")
	}
}

func TestClient_PlaceOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	got, err := client.PlaceOrder(context.Background(), CreateOrderRequest{
		MarketId:      lighterMarketID(t),
		Price:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
		BaseAmount:    lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		IsAsk:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_IS_ASK", 0)),
		OrderType:     OrderTypeLimit,
		TimeInForce:   OrderTimeInForcePostOnly,
		ClientOrderId: lighterInt64Env(t, "LIGHTER_TEST_CLIENT_ORDER_ID", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected create order response")
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID")
	got, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		MarketId: lighterMarketID(t),
		OrderId:  lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected cancel order response")
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID", "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	got, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
		MarketId:   lighterMarketID(t),
		OrderIndex: lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
		BaseAmount: lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		Price:      uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected modify order response")
	}
}

func TestClient_SendTxBatch(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_BATCH_TX_TYPE", "LIGHTER_TEST_BATCH_TX_INFO")
	got, err := client.SendTxBatch(context.Background(), []map[string]string{{
		"tx_type": os.Getenv("LIGHTER_TEST_BATCH_TX_TYPE"),
		"tx_info": os.Getenv("LIGHTER_TEST_BATCH_TX_INFO"),
	}})
	if err != nil {
		t.Fatalf("SendTxBatch: %v", err)
	}
	if got == nil {
		t.Fatal("expected batch response")
	}
}
