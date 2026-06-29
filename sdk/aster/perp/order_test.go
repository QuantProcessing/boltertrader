package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClient_PlaceOrderBuildsClosePositionStopRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/order", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		q := r.URL.Query()
		require.Equal(t, "BTCUSDT", q.Get("symbol"))
		require.Equal(t, "SELL", q.Get("side"))
		require.Equal(t, "STOP_MARKET", q.Get("type"))
		require.Equal(t, "GTC", q.Get("timeInForce"))
		require.Equal(t, "close-pos", q.Get("newClientOrderId"))
		require.Equal(t, "190", q.Get("stopPrice"))
		require.Equal(t, "true", q.Get("closePosition"))
		require.Equal(t, "CONTRACT_PRICE", q.Get("workingType"))
		require.Empty(t, q.Get("quantity"))
		require.Empty(t, q.Get("reduceOnly"))
		require.NotEmpty(t, q.Get("timestamp"))
		require.NotEmpty(t, q.Get("signature"))
		_, _ = w.Write([]byte(`{"symbol":"BTCUSDT","orderId":200,"clientOrderId":"close-pos","status":"NEW","side":"SELL","type":"STOP_MARKET","stopPrice":"190","closePosition":true}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().WithBaseURL(server.URL).WithCredentials("key", "secret")
	resp, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol:           "BTCUSDT",
		Side:             "SELL",
		Type:             OrderType_STOP_MARKET,
		TimeInForce:      TimeInForce_GTC,
		NewClientOrderID: "close-pos",
		StopPrice:        "190",
		ClosePosition:    true,
		WorkingType:      "CONTRACT_PRICE",
	})
	require.NoError(t, err)
	require.Equal(t, int64(200), resp.OrderID)
	require.True(t, resp.ClosePosition)
	require.Equal(t, "STOP_MARKET", resp.Type)
}
