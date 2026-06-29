package lighter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
	"github.com/vmihailenco/msgpack/v5"
)

func TestWSClientHandleIncomingFrameDispatchesJSONTextOrderBook(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got WsOrderBookEvent
	require.NoError(t, client.registerTypedSubscription("order_book/7", nil, func(env *Envelope) error {
		return env.Unmarshal(&got)
	}))

	msg := []byte(`{"channel":"order_book:7","type":"update/order_book","timestamp":1700000000000,"order_book":{"nonce":9,"begin_nonce":8,"asks":[{"price":"1.2","size":"3"}],"bids":[]}}`)
	require.NoError(t, client.handleIncomingFrame(websocket.TextMessage, msg))

	require.Equal(t, "order_book:7", got.Channel)
	require.Equal(t, int64(9), got.OrderBook.Nonce)
	require.Equal(t, int64(8), got.OrderBook.BeginNonce)
	require.Len(t, got.OrderBook.Asks, 1)
}

func TestWSClientHandleIncomingFrameDispatchesMsgpackBinaryTrade(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got WsTradeEvent
	require.NoError(t, client.registerTypedSubscription("trade/4", nil, func(env *Envelope) error {
		return env.Unmarshal(&got)
	}))

	raw, err := msgpack.Marshal(map[string]any{
		"channel": "trade:4",
		"type":    "update/trade",
		"nonce":   int64(5),
		"trades": []map[string]any{{
			"trade_id": int64(12),
			"price":    "2000.5",
			"size":     "0.3",
		}},
	})
	require.NoError(t, err)

	require.NoError(t, client.handleIncomingFrame(websocket.BinaryMessage, raw))
	require.Len(t, got.Trades, 1)
	require.Equal(t, int64(12), got.Trades[0].TradeId)
	require.Equal(t, "2000.5", got.Trades[0].Price)
}

func TestWSClientRawCallbackReceivesNormalizedJSONAfterMsgpackDecode(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got map[string]any
	require.NoError(t, client.registerRawSubscription("height", nil, func(data []byte) {
		require.NoError(t, json.Unmarshal(data, &got))
	}))

	raw, err := msgpack.Marshal(map[string]any{
		"channel": "height",
		"type":    "update/height",
		"height":  int64(123),
	})
	require.NoError(t, err)

	require.NoError(t, client.handleIncomingFrame(websocket.BinaryMessage, raw))
	require.Equal(t, "height", got["channel"])
	require.Equal(t, "update/height", got["type"])
	require.Equal(t, float64(123), got["height"])
}

func TestWSClientRawCallbackNormalizesNestedIntegerMapKeysFromMsgpack(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got map[string]any
	require.NoError(t, client.registerRawSubscription("account_all_orders/42", nil, func(data []byte) {
		require.NoError(t, json.Unmarshal(data, &got))
	}))

	raw, err := msgpack.Marshal(map[string]any{
		"channel": "account_all_orders:42",
		"type":    "subscribed/account_all_orders",
		"orders": map[any]any{
			int64(0): []any{
				map[string]any{
					"order_id":            "123",
					"client_order_id":     "456",
					"market_index":        int64(0),
					"initial_base_amount": "0.01",
				},
			},
		},
	})
	require.NoError(t, err)

	require.NoError(t, client.handleIncomingFrame(websocket.BinaryMessage, raw))
	require.Equal(t, "account_all_orders:42", got["channel"])
	require.Equal(t, "subscribed/account_all_orders", got["type"])
	orders, ok := got["orders"].(map[string]any)
	require.True(t, ok)
	bucket, ok := orders["0"].([]any)
	require.True(t, ok)
	require.Len(t, bucket, 1)
	entry, ok := bucket[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "123", entry["order_id"])
	require.Equal(t, "456", entry["client_order_id"])
}

func TestWSClientTypedDispatcherHandlesNestedIntegerMapKeysFromMsgpack(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got WsAccountAllOrdersEvent
	require.NoError(t, client.registerTypedSubscription("account_all_orders/42", nil, func(env *Envelope) error {
		return env.Unmarshal(&got)
	}))

	raw, err := msgpack.Marshal(map[string]any{
		"channel": "account_all_orders:42",
		"type":    "update/account_all_orders",
		"orders": map[any]any{
			int64(2048): []any{
				map[string]any{
					"order_id":            "789",
					"client_order_id":     "999",
					"market_index":        int64(2048),
					"initial_base_amount": "0.02",
					"filled_base_amount":  "0.01",
					"status":              "open",
				},
			},
		},
	})
	require.NoError(t, err)

	require.NoError(t, client.handleIncomingFrame(websocket.BinaryMessage, raw))
	require.Contains(t, got.Orders, "2048")
	require.Len(t, got.Orders["2048"], 1)
	require.Equal(t, "789", got.Orders["2048"][0].OrderId)
	require.Equal(t, "999", got.Orders["2048"][0].ClientOrderId)
	require.Equal(t, OrderStatusOpen, got.Orders["2048"][0].Status)
}

func TestWSClientBuildURLIncludesReadonlyAndEncoding(t *testing.T) {
	client := NewWebsocketClientWithConfig(context.Background(), WSConfig{
		URL:      MainnetWSURL,
		ReadOnly: true,
		Encoding: WSEncodingMsgpack,
	})

	got, err := client.buildURL()
	require.NoError(t, err)
	require.Equal(t, "wss://mainnet.zklighter.elliot.ai/stream?encoding=msgpack&readonly=true", got)
}

func TestWSClientDefaultsToMsgpackEncoding(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	got, err := client.buildURL()
	require.NoError(t, err)
	require.Equal(t, "wss://mainnet.zklighter.elliot.ai/stream?encoding=msgpack", got)
}

func TestWSClientPingLoopSendsControlPing(t *testing.T) {
	client := NewWebsocketClientWithConfig(context.Background(), WSConfig{
		KeepaliveInterval: 5 * time.Millisecond,
	})
	rec := &recordingConn{}
	client.conn = rec

	go client.pingLoop()
	time.Sleep(20 * time.Millisecond)
	client.Close()

	require.GreaterOrEqual(t, rec.pingCount, 1)
	require.Zero(t, rec.jsonCount)
}

type recordingConn struct {
	pingCount int
	jsonCount int
}

func (c *recordingConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("not implemented")
}

func (c *recordingConn) WriteJSON(v interface{}) error {
	c.jsonCount++
	return nil
}

func (c *recordingConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	if messageType == websocket.PingMessage {
		c.pingCount++
	}
	return nil
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *recordingConn) Close() error {
	return nil
}
