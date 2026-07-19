package lighter

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
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

func TestWSClientHandleIncomingFrameAcceptsBinaryJSONUnsubscribeAck(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	require.NoError(t, client.handleIncomingFrame(
		websocket.BinaryMessage,
		[]byte(`{"type":"unsubscribed","channel":"order_book:2048"}`),
	))
}

func TestWSClientWaitsForOutstandingUnsubscribeAckBeforeNextSubscription(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	conn := &recordingConn{}
	client.Mu.Lock()
	client.conn = conn
	client.Mu.Unlock()
	require.NoError(t, client.registerRawSubscription("market_stats/0", nil, func([]byte) {}))
	require.NoError(t, client.Unsubscribe("market_stats/0"))

	resubscribed := make(chan error, 1)
	var called atomic.Bool
	go func() {
		resubscribed <- client.Subscribe("trade/0", nil, func([]byte) {
			called.Store(true)
		})
	}()
	select {
	case err := <-resubscribed:
		t.Fatalf("resubscribe returned before unsubscribe acknowledgement: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	require.NoError(t, client.handleIncomingFrame(
		websocket.TextMessage,
		[]byte(`{"type":"unsubscribed","channel":"market_stats:0"}`),
	))
	select {
	case err := <-resubscribed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("resubscribe did not continue after unsubscribe acknowledgement")
	}

	require.NoError(t, client.handleIncomingFrame(
		websocket.TextMessage,
		[]byte(`{"type":"subscribed/trade","channel":"trade:0","timestamp":1700000000000}`),
	))
	require.True(t, called.Load())
}

func TestWSClientHandleIncomingFrameDispatchesCandle(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got WsCandleEvent
	require.NoError(t, client.registerTypedSubscription("candle/0/1m", nil, func(env *Envelope) error {
		return env.Unmarshal(&got)
	}))

	msg := []byte(`{"channel":"candle:0:1m","type":"subscribed/candle","timestamp":1778500801812,"candles":[{"t":1778500800000,"o":2334.89,"h":2335.14,"l":2334.73,"c":2334.73,"v":19.8259,"V":46293.345028,"i":19694821240}]}`)
	require.NoError(t, client.handleIncomingFrame(websocket.TextMessage, msg))

	require.Equal(t, "candle:0:1m", got.Channel)
	require.Len(t, got.Candles, 1)
	require.Equal(t, int64(1778500800000), got.Candles[0].OpenTime)
	require.Equal(t, "2334.89", got.Candles[0].Open.String())
	require.Equal(t, "46293.345028", got.Candles[0].QuoteVolume.String())
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

func TestWSClientDispatchesAccountOrdersResponseWithoutAccountChannelSegment(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	var got map[string]any
	require.NoError(t, client.registerRawSubscription("account_orders/2048/42", nil, func(data []byte) {
		require.NoError(t, json.Unmarshal(data, &got))
	}))
	require.NoError(t, client.handleIncomingFrame(
		websocket.TextMessage,
		[]byte(`{"channel":"account_orders:2048","type":"subscribed/account_orders","account":42,"nonce":1,"orders":{}}`),
	))
	require.Equal(t, "subscribed/account_orders", got["type"])
	require.Equal(t, float64(42), got["account"])
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

func TestWSClientResubscribeRefreshesAuthenticatedChannel(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	conn := &authCaptureConn{}
	client.Mu.Lock()
	client.conn = conn
	client.Mu.Unlock()
	oldToken := "old-token"
	require.NoError(t, client.registerRawSubscription("account_all_trades/42", &oldToken, func([]byte) {}))
	client.SetSubscriptionAuthProvider(func(channel string) (*string, error) {
		require.Equal(t, "account_all_trades/42", channel)
		token := "fresh-token"
		return &token, nil
	})

	require.NoError(t, client.resubscribeAll())
	require.Equal(t, map[string]string{
		"type":    "subscribe",
		"channel": "account_all_trades/42",
		"auth":    "fresh-token",
	}, conn.last())
	client.Mu.RLock()
	stored := client.Subscriptions["account_all_trades/42"].authToken
	client.Mu.RUnlock()
	require.NotNil(t, stored)
	require.Equal(t, "fresh-token", *stored)
}

func TestWSClientResubscribeFailsClosedWhenAuthRefreshFails(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	conn := &authCaptureConn{}
	client.Mu.Lock()
	client.conn = conn
	client.Mu.Unlock()
	oldToken := "old-token"
	require.NoError(t, client.registerRawSubscription("account_all_positions/42", &oldToken, func([]byte) {}))
	client.SetSubscriptionAuthProvider(func(string) (*string, error) {
		return nil, errors.New("credential-secret-canary")
	})

	err := client.resubscribeAll()
	require.Error(t, err)
	require.NotContains(t, err.Error(), "credential-secret-canary")
	require.Nil(t, conn.last())
}

type authCaptureConn struct {
	recordingConn
	mu      sync.Mutex
	command map[string]string
}

func (conn *authCaptureConn) WriteJSON(value interface{}) error {
	command, _ := value.(map[string]string)
	conn.mu.Lock()
	conn.command = command
	conn.mu.Unlock()
	return nil
}

func (conn *authCaptureConn) last() map[string]string {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.command
}

func TestWSClientDefaultsToMsgpackEncoding(t *testing.T) {
	client := NewWebsocketClient(context.Background())

	got, err := client.buildURL()
	require.NoError(t, err)
	require.Equal(t, "wss://mainnet.zklighter.elliot.ai/stream?encoding=msgpack", got)
}

func TestWSClientReportsMalformedIncomingFrames(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	reported := make(chan error, 1)
	client.SetErrorHandler(func(err error) {
		reported <- err
	})

	client.HandleMessage([]byte(`{"channel":`))

	select {
	case err := <-reported:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("malformed websocket frame was silently dropped")
	}
}

func TestWSClientReconnectHooksPreserveLifecycleError(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	started := make(chan error, 1)
	recovered := make(chan struct{}, 1)
	client.SetReconnectHooks(
		func(err error) { started <- err },
		func() { recovered <- struct{}{} },
	)
	cause := errors.New("socket lost")

	client.notifyReconnectStarted(cause)
	client.notifyReconnectRecovered()

	require.ErrorIs(t, <-started, cause)
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("recovered hook was not called")
	}
}

func TestWSClientPingLoopSendsControlPing(t *testing.T) {
	client := NewWebsocketClientWithConfig(context.Background(), WSConfig{
		KeepaliveInterval: 5 * time.Millisecond,
	})
	rec := &recordingConn{}
	client.Mu.Lock()
	client.conn = rec
	client.Mu.Unlock()

	go client.pingLoop()
	time.Sleep(20 * time.Millisecond)
	client.Close()

	require.GreaterOrEqual(t, rec.pingCount.Load(), int64(1))
	require.Zero(t, rec.jsonCount.Load())
}

type recordingConn struct {
	pingCount atomic.Int64
	jsonCount atomic.Int64
}

func (c *recordingConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("not implemented")
}

func (c *recordingConn) WriteJSON(v interface{}) error {
	c.jsonCount.Add(1)
	return nil
}

func (c *recordingConn) WriteControl(messageType int, data []byte, deadline time.Time) error {
	if messageType == websocket.PingMessage {
		c.pingCount.Add(1)
	}
	return nil
}

func (c *recordingConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *recordingConn) Close() error {
	return nil
}
