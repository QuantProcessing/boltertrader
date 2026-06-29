package sdk

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestClient_GetClassicSpotAssets(t *testing.T) {
	got, err := newLivePrivateClient(t).GetClassicSpotAssets(context.Background(), bitgetEnvOrDefault("BITGET_TEST_ASSET_COIN", "USDT"))
	if err != nil {
		t.Fatalf("GetClassicSpotAssets: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot assets slice")
	}
}

func TestClient_PlaceClassicSpotOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_TEST_ORDER_QTY", "BITGET_TEST_ORDER_PRICE")
	got, err := client.PlaceClassicSpotOrder(context.Background(), &PlaceOrderRequest{
		Symbol:      bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol),
		Qty:         os.Getenv("BITGET_TEST_ORDER_QTY"),
		Side:        bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:   "limit",
		Price:       os.Getenv("BITGET_TEST_ORDER_PRICE"),
		TimeInForce: "gtc",
		ClientOID:   bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceClassicSpotOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot order response")
	}
}

func TestClient_CancelClassicSpotOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_TEST_ORDER_ID")
	got, err := client.CancelClassicSpotOrder(context.Background(), bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol), os.Getenv("BITGET_TEST_ORDER_ID"), "")
	if err != nil {
		t.Fatalf("CancelClassicSpotOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot cancel response")
	}
}

func TestClient_CancelAllClassicSpotOrders(t *testing.T) {
	client := requireBitgetLiveWrite(t)
	if err := client.CancelAllClassicSpotOrders(context.Background(), bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)); err != nil {
		t.Fatalf("CancelAllClassicSpotOrders: %v", err)
	}
}

func TestClient_GetClassicSpotOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	orderID := os.Getenv("BITGET_TEST_ORDER_ID")
	if orderID == "" {
		t.Skip("BITGET_TEST_ORDER_ID is required for GetClassicSpotOrder live test")
	}
	got, err := client.GetClassicSpotOrder(context.Background(), orderID, "")
	if err != nil {
		t.Fatalf("GetClassicSpotOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot order")
	}
}

func TestClient_GetClassicSpotOpenOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetClassicSpotOpenOrders(context.Background(), bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol))
	if err != nil {
		t.Fatalf("GetClassicSpotOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot open orders slice")
	}
}

func TestClient_GetClassicSpotOrderHistory(t *testing.T) {
	got, err := newLivePrivateClient(t).GetClassicSpotOrderHistory(context.Background(), bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol))
	if err != nil {
		t.Fatalf("GetClassicSpotOrderHistory: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic spot history slice")
	}
}

func TestClient_PlaceClassicMixOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL", "BITGET_TEST_ORDER_QTY")
	got, err := client.PlaceClassicMixOrder(context.Background(), &PlaceOrderRequest{
		Symbol:     os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		Qty:        os.Getenv("BITGET_TEST_ORDER_QTY"),
		Side:       bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:  bitgetEnvOrDefault("BITGET_TEST_ORDER_TYPE", "market"),
		Price:      os.Getenv("BITGET_TEST_ORDER_PRICE"),
		MarginMode: bitgetEnvOrDefault("BITGET_TEST_MARGIN_MODE", "crossed"),
		TradeSide:  bitgetEnvOrDefault("BITGET_TEST_TRADE_SIDE", "open"),
		ClientOID:  bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	}, bitgetPerpCategory, bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"))
	if err != nil {
		t.Fatalf("PlaceClassicMixOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix order response")
	}
}

func TestClient_CancelClassicMixOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL", "BITGET_TEST_ORDER_ID")
	got, err := client.CancelClassicMixOrder(
		context.Background(),
		os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		bitgetPerpCategory,
		bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"),
		os.Getenv("BITGET_TEST_ORDER_ID"),
		"",
	)
	if err != nil {
		t.Fatalf("CancelClassicMixOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix cancel response")
	}
}

func TestClient_CancelAllClassicMixOrders(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL")
	if err := client.CancelAllClassicMixOrders(
		context.Background(),
		bitgetPerpCategory,
		os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"),
	); err != nil {
		t.Fatalf("CancelAllClassicMixOrders: %v", err)
	}
}

func TestClient_ModifyClassicMixOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL", "BITGET_TEST_ORDER_ID")
	got, err := client.ModifyClassicMixOrder(context.Background(), &ModifyOrderRequest{
		Symbol:   os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		OrderID:  os.Getenv("BITGET_TEST_ORDER_ID"),
		NewQty:   os.Getenv("BITGET_TEST_ORDER_QTY"),
		NewPrice: os.Getenv("BITGET_TEST_ORDER_PRICE"),
	}, bitgetPerpCategory, bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"))
	if err != nil {
		t.Fatalf("ModifyClassicMixOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix modify response")
	}
}

func TestClient_GetClassicMixOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	symbol := os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL")
	orderID := os.Getenv("BITGET_TEST_ORDER_ID")
	if symbol == "" || orderID == "" {
		t.Skip("BITGET_CLASSIC_TEST_PERP_SYMBOL and BITGET_TEST_ORDER_ID are required for GetClassicMixOrder live test")
	}
	got, err := client.GetClassicMixOrder(context.Background(), symbol, bitgetPerpCategory, orderID, "")
	if err != nil {
		t.Fatalf("GetClassicMixOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix order")
	}
}

func TestClient_GetClassicMixOpenOrders(t *testing.T) {
	symbol := os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL")
	if symbol == "" {
		t.Skip("BITGET_CLASSIC_TEST_PERP_SYMBOL is required for GetClassicMixOpenOrders live test")
	}
	got, err := newLivePrivateClient(t).GetClassicMixOpenOrders(context.Background(), bitgetPerpCategory, symbol)
	if err != nil {
		t.Fatalf("GetClassicMixOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix open orders slice")
	}
}

func TestClient_GetClassicMixOrderHistory(t *testing.T) {
	symbol := os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL")
	if symbol == "" {
		t.Skip("BITGET_CLASSIC_TEST_PERP_SYMBOL is required for GetClassicMixOrderHistory live test")
	}
	got, err := newLivePrivateClient(t).GetClassicMixOrderHistory(context.Background(), bitgetPerpCategory, symbol)
	if err != nil {
		t.Fatalf("GetClassicMixOrderHistory: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix order history slice")
	}
}

func TestClient_GetClassicMixAccount(t *testing.T) {
	symbol := os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL")
	if symbol == "" {
		t.Skip("BITGET_CLASSIC_TEST_PERP_SYMBOL is required for GetClassicMixAccount live test")
	}
	got, err := newLivePrivateClient(t).GetClassicMixAccount(context.Background(), symbol, bitgetPerpCategory, bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"))
	if err != nil {
		t.Fatalf("GetClassicMixAccount: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix account")
	}
}

func TestClient_GetClassicMixPositions(t *testing.T) {
	got, err := newLivePrivateClient(t).GetClassicMixPositions(context.Background(), bitgetPerpCategory, bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"))
	if err != nil {
		t.Fatalf("GetClassicMixPositions: %v", err)
	}
	if got == nil {
		t.Fatal("expected classic mix positions slice")
	}
}

func TestClient_SetClassicMixLeverage(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_CLASSIC_TEST_PERP_SYMBOL")
	if err := client.SetClassicMixLeverage(
		context.Background(),
		os.Getenv("BITGET_CLASSIC_TEST_PERP_SYMBOL"),
		bitgetPerpCategory,
		bitgetEnvOrDefault("BITGET_TEST_MARGIN_COIN", "USDT"),
		bitgetEnvOrDefault("BITGET_TEST_LEVERAGE", "2"),
	); err != nil {
		t.Fatalf("SetClassicMixLeverage: %v", err)
	}
}

func TestClient_SetClassicMixLeverageBuildsClassicRequest(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	if err := client.SetClassicMixLeverage(context.Background(), "BTCUSDT", bitgetPerpCategory, "USDT", "3"); err != nil {
		t.Fatalf("SetClassicMixLeverage: %v", err)
	}
	if seenPath != "/api/v2/mix/account/set-leverage" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"symbol":"BTCUSDT"`, `"productType":"USDT-FUTURES"`, `"marginCoin":"USDT"`, `"leverage":"3"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected classic mix leverage body to contain %s, got %s", want, seenBody)
		}
	}
}
