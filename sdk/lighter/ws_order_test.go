package lighter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestWebsocketClientPlaceOrderOutcomeDistinguishesAcceptedRejectedAndUnknown(t *testing.T) {
	writeErr := errors.New("write failed")
	tests := []struct {
		name       string
		response   *TxResponse
		writeErr   error
		wantCode   int
		wantSent   bool
		wantErr    error
		wantAccept bool
	}{
		{
			name:       "accepted",
			response:   &TxResponse{Code: 200},
			wantCode:   200,
			wantSent:   true,
			wantAccept: true,
		},
		{
			name:     "rejected",
			response: &TxResponse{Code: 400, Message: "secret venue rejection"},
			wantCode: 400,
			wantSent: true,
			wantErr:  ErrOrderRejected,
		},
		{
			name:     "unknown_after_send",
			wantSent: true,
			wantErr:  ErrWSOutcomeUnknown,
		},
		{
			name:     "write_failed_before_send_confirmation",
			writeErr: writeErr,
			wantErr:  writeErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient().WithCredentials(strings.Repeat("01", 40), 42, 7)
			client.nonce = 1
			client.nonceInit = true
			ws := NewWebsocketClient(context.Background())
			ws.TxResponseTimeout = 10 * time.Millisecond
			conn := &txOutcomeConn{writeErr: tt.writeErr}
			if tt.response != nil {
				conn.onWrite = func(value any) {
					message := value.(txMsg)
					ws.pendingMu.RLock()
					response := ws.PendingRequests[message.Data.ID]
					ws.pendingMu.RUnlock()
					response <- tt.response
				}
			}
			ws.Mu.Lock()
			ws.conn = conn
			ws.Mu.Unlock()

			outcome, err := ws.PlaceOrderOutcome(context.Background(), client, CreateOrderRequest{
				MarketId:      7,
				Price:         100,
				BaseAmount:    10,
				OrderType:     OrderTypeLimit,
				TimeInForce:   OrderTimeInForcePostOnly,
				ClientOrderId: 17,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v, want %v", err, tt.wantErr)
			}
			if outcome.Sent != tt.wantSent ||
				outcome.Code != tt.wantCode ||
				(outcome.TransactionHash == "") ||
				outcome.Accepted() != tt.wantAccept {
				t.Fatalf("outcome=%+v", outcome)
			}
			if err != nil && strings.Contains(err.Error(), "secret venue rejection") {
				t.Fatal("SDK error leaked venue rejection message")
			}
		})
	}
}

type txOutcomeConn struct {
	onWrite  func(any)
	writeErr error
}

func (conn *txOutcomeConn) ReadMessage() (int, []byte, error) {
	return 0, nil, errors.New("not implemented")
}

func (conn *txOutcomeConn) WriteJSON(value any) error {
	if conn.writeErr != nil {
		return conn.writeErr
	}
	if conn.onWrite != nil {
		conn.onWrite(value)
	}
	return nil
}

func (conn *txOutcomeConn) WriteControl(int, []byte, time.Time) error { return nil }
func (conn *txOutcomeConn) SetReadDeadline(time.Time) error           { return nil }
func (conn *txOutcomeConn) Close() error                              { return nil }

var _ websocketConn = (*txOutcomeConn)(nil)

func TestWebsocketClientCancelOrderOutcomePreservesGatewayRejection(t *testing.T) {
	client := NewClient().WithCredentials(strings.Repeat("01", 40), 42, 7)
	client.nonce = 1
	client.nonceInit = true
	ws := NewWebsocketClient(context.Background())
	conn := &txOutcomeConn{}
	conn.onWrite = func(value any) {
		message := value.(txMsg)
		ws.pendingMu.RLock()
		response := ws.PendingRequests[message.Data.ID]
		ws.pendingMu.RUnlock()
		response <- &TxResponse{Code: 409, Message: "secret venue rejection"}
	}
	ws.Mu.Lock()
	ws.conn = conn
	ws.Mu.Unlock()

	outcome, err := ws.CancelOrderOutcome(context.Background(), client, CancelOrderRequest{
		MarketId: 7,
		OrderId:  41,
	})
	if !errors.Is(err, ErrOrderRejected) {
		t.Fatalf("err=%v", err)
	}
	if !outcome.Sent || outcome.Code != 409 || outcome.TransactionHash == "" {
		t.Fatalf("outcome=%+v", outcome)
	}
	if strings.Contains(err.Error(), "secret venue rejection") {
		t.Fatal("SDK error leaked venue rejection message")
	}
}

func TestWebsocketClient_PlaceOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.PlaceOrder(context.Background(), client, CreateOrderRequest{
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
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_CancelOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.CancelOrder(context.Background(), client, CancelOrderRequest{
		MarketId: lighterMarketID(t),
		OrderId:  lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_ModifyOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID", "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.ModifyOrder(context.Background(), client, ModifyOrderRequest{
		MarketId:   lighterMarketID(t),
		OrderIndex: lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
		BaseAmount: lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		Price:      uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_CancelAllOrders(t *testing.T) {
	client := requireLighterLiveWrite(t)
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.CancelAllOrders(context.Background(), client, CancelAllOrdersRequest{MarketId: lighterMarketID(t)})
	if err != nil {
		t.Fatalf("CancelAllOrders: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func newLiveWSClient(t *testing.T) *WebsocketClient {
	t.Helper()
	testenv.RequireLiveRead(t)

	wsClient := NewWebsocketClient(context.Background())
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect websocket: %v", err)
	}
	t.Cleanup(wsClient.Close)
	return wsClient
}
