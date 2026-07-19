package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type fakeLighterPrivateWS struct {
	place  lighter.CreateOrderRequest
	cancel lighter.CancelOrderRequest

	placeCalls  int
	cancelCalls int

	placeOutcome  lighter.WSCommandOutcome
	placeErr      error
	cancelOutcome lighter.WSCommandOutcome
	cancelErr     error

	mu                     sync.Mutex
	rejectDuplicateAccount bool
	tradesSubscribes       int
	tradesUnsubscribes     int
	tradesHandler          func([]byte)
	tradesPayload          []byte
	tradesSubscribeErr     error
	tradesSubscribeStarted chan struct{}
	tradesSubscribeRelease <-chan struct{}
	tradesSubscribeOnce    sync.Once
	positionsSubscribes    int
	positionsUnsubscribes  int
	positionsHandler       func([]byte)
	positionsPayload       []byte
	authProvider           func(string) (*string, error)
	reconnectStarted       func(error)
	reconnectRecovered     func()
}

func (ws *fakeLighterPrivateWS) Connect() error              { return nil }
func (ws *fakeLighterPrivateWS) Close()                      {}
func (ws *fakeLighterPrivateWS) SetErrorHandler(func(error)) {}
func (ws *fakeLighterPrivateWS) SetReconnectHooks(started func(error), recovered func()) {
	ws.reconnectStarted = started
	ws.reconnectRecovered = recovered
}
func (ws *fakeLighterPrivateWS) SetSubscriptionAuthProvider(provider func(string) (*string, error)) {
	ws.authProvider = provider
}
func (ws *fakeLighterPrivateWS) SubscribeOrderBook(int, func([]byte)) error { return nil }
func (ws *fakeLighterPrivateWS) UnsubscribeOrderBook(int) error             { return nil }
func (ws *fakeLighterPrivateWS) SubscribeTicker(int, func([]byte)) error    { return nil }
func (ws *fakeLighterPrivateWS) UnsubscribeTicker(int) error                { return nil }
func (ws *fakeLighterPrivateWS) SubscribeTrades(int, func([]byte)) error    { return nil }
func (ws *fakeLighterPrivateWS) UnsubscribeTrades(int) error                { return nil }
func (ws *fakeLighterPrivateWS) SubscribeMarketStats(int, func([]byte)) error {
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeMarketStats(int) error { return nil }
func (ws *fakeLighterPrivateWS) SubscribeCandle(int, string, func([]byte)) error {
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeCandle(int, string) error { return nil }
func (ws *fakeLighterPrivateWS) SubscribeAccountOrders(_ int, _ int64, _ string, cb func([]byte)) error {
	cb([]byte(`{"orders":{"7":[{"order_index":11,"client_order_index":123,"market_index":7,"initial_base_amount":"1.2","remaining_base_amount":"0.7","filled_base_amount":"0.5","price":"100","is_ask":false,"type":"limit","time_in_force":"post-only","status":"open","created_at":1700000000000,"updated_at":1700000000100}]}}`))
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeAccountOrders(int, int64) error { return nil }
func (ws *fakeLighterPrivateWS) SubscribeAccountAllTrades(_ int64, _ string, cb func([]byte)) error {
	ws.mu.Lock()
	ws.tradesSubscribes++
	if ws.rejectDuplicateAccount && ws.tradesSubscribes > 1 {
		ws.mu.Unlock()
		return errors.New("duplicate trades subscription")
	}
	subscribeErr := ws.tradesSubscribeErr
	started := ws.tradesSubscribeStarted
	release := ws.tradesSubscribeRelease
	ws.tradesHandler = cb
	payload := ws.tradesPayload
	if len(payload) == 0 {
		payload = []byte(`{"trades":{"7":[{"trade_id":21,"market_id":7,"size":"0.3","price":"101","ask_id":11,"bid_id":12,"ask_client_id":123,"bid_client_id":124,"ask_account_id":42,"bid_account_id":99,"is_maker_ask":true,"timestamp":1700000000200,"maker_fee":2,"taker_fee":3}]}}`)
	}
	ws.mu.Unlock()
	if started != nil {
		ws.tradesSubscribeOnce.Do(func() { close(started) })
	}
	if release != nil {
		<-release
	}
	if subscribeErr != nil {
		return subscribeErr
	}
	cb(payload)
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeAccountAllTrades(int64) error {
	ws.mu.Lock()
	ws.tradesUnsubscribes++
	ws.mu.Unlock()
	return nil
}
func (ws *fakeLighterPrivateWS) SubscribeAccountAllAssets(_ int64, _ string, cb func([]byte)) error {
	cb([]byte(`{"timestamp":1700000000300,"assets":{"USDC":{"symbol":"USDC","balance":"10","locked_balance":"2"}}}`))
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeAccountAllAssets(int64) error { return nil }
func (ws *fakeLighterPrivateWS) SubscribeAccountAllPositions(_ int64, _ string, cb func([]byte)) error {
	ws.mu.Lock()
	ws.positionsSubscribes++
	if ws.rejectDuplicateAccount && ws.positionsSubscribes > 1 {
		ws.mu.Unlock()
		return errors.New("duplicate positions subscription")
	}
	ws.positionsHandler = cb
	payload := ws.positionsPayload
	if len(payload) == 0 {
		payload = []byte(`{"positions":{"7":{"market_id":7,"symbol":"ETH","sign":1,"position":"1","avg_entry_price":"100","position_value":"105","unrealized_pnl":"5","liquidation_price":"50","allocated_margin":"20"}}}`)
	}
	ws.mu.Unlock()
	cb(payload)
	return nil
}
func (ws *fakeLighterPrivateWS) UnsubscribeAccountAllPositions(int64) error {
	ws.mu.Lock()
	ws.positionsUnsubscribes++
	ws.mu.Unlock()
	return nil
}
func (ws *fakeLighterPrivateWS) PlaceOrderOutcome(_ context.Context, _ *lighter.Client, req lighter.CreateOrderRequest) (lighter.WSCommandOutcome, error) {
	ws.placeCalls++
	ws.place = req
	if ws.placeOutcome == (lighter.WSCommandOutcome{}) && ws.placeErr == nil {
		return lighter.WSCommandOutcome{TransactionHash: "0xplace", Sent: true, Code: 200}, nil
	}
	return ws.placeOutcome, ws.placeErr
}
func (ws *fakeLighterPrivateWS) CancelOrderOutcome(_ context.Context, _ *lighter.Client, req lighter.CancelOrderRequest) (lighter.WSCommandOutcome, error) {
	ws.cancelCalls++
	ws.cancel = req
	if ws.cancelOutcome == (lighter.WSCommandOutcome{}) && ws.cancelErr == nil {
		return lighter.WSCommandOutcome{TransactionHash: "0xcancel", Sent: true, Code: 200}, nil
	}
	return ws.cancelOutcome, ws.cancelErr
}

func TestLighterPrivateWSBackendStreamsAndCommands(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	ctx := context.Background()
	orders, err := backend.StartOrders(ctx, "ETH-USDC", streamCallbacks[exchange.OrderEvent]{Event: func(event exchange.OrderEvent) {
		if event.Order.OrderID != "11" || event.Order.LimitPolicy != exchange.LimitPolicyPostOnly {
			t.Fatalf("order event=%+v", event)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer orders()
	fills, err := backend.StartFills(ctx, "ETH-USDC", streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) {
		if event.Fill.FillID != "21" || event.Fill.Side != exchange.SideSell {
			t.Fatalf("fill event=%+v", event)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer fills()
	balances, err := backend.StartBalances(ctx, streamCallbacks[exchange.BalanceEvent]{Event: func(event exchange.BalanceEvent) {
		if len(event.Balances) != 1 ||
			!event.Balances[0].Available.Equal(decimal.NewFromInt(10)) ||
			!event.Balances[0].Locked.Equal(decimal.NewFromInt(2)) ||
			!event.Balances[0].Total.Equal(decimal.NewFromInt(12)) {
			t.Fatalf("balance event=%+v", event)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer balances()
	positions, err := backend.StartPositions(ctx, "ETH-USDC", streamCallbacks[exchange.PositionEvent]{Event: func(event exchange.PositionEvent) {
		if len(event.Positions) != 1 || event.Positions[0].Instrument != "ETH-USDC" {
			t.Fatalf("position event=%+v", event)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer positions()
	ack, err := backend.PlaceOrder(ctx, exchange.PlaceOrderRequest{
		Instrument:    "ETH-USDC",
		ClientOrderID: "123",
		Side:          exchange.SideSell,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("1.2"),
		LimitPrice:    decimal.RequireFromString("100"),
		LimitPolicy:   exchange.LimitPolicyPostOnly,
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.TransactionHash != "0xplace" || fake.place.TimeInForce != lighter.OrderTimeInForcePostOnly || fake.place.ReduceOnly != 1 {
		t.Fatalf("ack=%+v native=%+v", ack, fake.place)
	}
	cancel, err := backend.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "ETH-USDC", OrderID: "11"})
	if err != nil {
		t.Fatal(err)
	}
	if cancel.TransactionHash != "0xcancel" || fake.cancel.OrderId != 11 {
		t.Fatalf("cancel=%+v native=%+v", cancel, fake.cancel)
	}
}

func TestLighterPrivateWSBackendSpotStreamsAndCancel(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductSpot)
	ctx := context.Background()
	orderStop, err := backend.StartOrders(ctx, "ETH-USDC", streamCallbacks[exchange.OrderEvent]{
		Event: func(event exchange.OrderEvent) {
			if event.Order.Instrument != "ETH-USDC" || event.Order.ReduceOnly {
				t.Fatalf("spot order event=%+v", event)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fillStop, err := backend.StartFills(ctx, "ETH-USDC", streamCallbacks[exchange.FillEvent]{
		Event: func(event exchange.FillEvent) {
			if event.Fill.Instrument != "ETH-USDC" {
				t.Fatalf("spot fill event=%+v", event)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	balanceStop, err := backend.StartBalances(ctx, streamCallbacks[exchange.BalanceEvent]{
		Event: func(event exchange.BalanceEvent) {
			if len(event.Balances) != 1 || event.Balances[0].Asset != "USDC" {
				t.Fatalf("spot balance event=%+v", event)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel, err := backend.CancelOrder(ctx, exchange.CancelOrderRequest{
		Instrument: "ETH-USDC",
		OrderID:    "11",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cancel.Product != exchange.ProductSpot ||
		cancel.TransactionHash != "0xcancel" ||
		fake.cancel.MarketId != 7 {
		t.Fatalf("cancel=%+v native=%+v", cancel, fake.cancel)
	}
	for _, stop := range []func() error{orderStop, fillStop, balanceStop} {
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLighterPrivateWSBackendPlaceOrderRequestMapping(t *testing.T) {
	cases := []struct {
		name       string
		product    exchange.Product
		req        exchange.PlaceOrderRequest
		wantType   uint32
		wantTIF    uint32
		wantPrice  uint32
		wantReduce uint32
	}{
		{
			name:    "spot market",
			product: exchange.ProductSpot,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "101", Side: exchange.SideBuy,
				Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("1.2"),
			},
			wantType:  lighter.OrderTypeMarket,
			wantTIF:   lighter.OrderTimeInForceImmediateOrCancel,
			wantPrice: 10151,
		},
		{
			name:    "spot limit resting",
			product: exchange.ProductSpot,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "102", Side: exchange.SideSell,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("100"), LimitPolicy: exchange.LimitPolicyResting,
			},
			wantType:  lighter.OrderTypeLimit,
			wantTIF:   lighter.OrderTimeInForceGoodTillTime,
			wantPrice: 10000,
		},
		{
			name:    "spot limit IOC",
			product: exchange.ProductSpot,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "106", Side: exchange.SideBuy,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("100"), LimitPolicy: exchange.LimitPolicyIOC,
			},
			wantType:  lighter.OrderTypeLimit,
			wantTIF:   lighter.OrderTimeInForceImmediateOrCancel,
			wantPrice: 10000,
		},
		{
			name:    "spot limit post-only",
			product: exchange.ProductSpot,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "107", Side: exchange.SideSell,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("100"), LimitPolicy: exchange.LimitPolicyPostOnly,
			},
			wantType:  lighter.OrderTypeLimit,
			wantTIF:   lighter.OrderTimeInForcePostOnly,
			wantPrice: 10000,
		},
		{
			name:    "perp limit resting",
			product: exchange.ProductPerp,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "108", Side: exchange.SideSell,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("100"), LimitPolicy: exchange.LimitPolicyResting,
			},
			wantType:  lighter.OrderTypeLimit,
			wantTIF:   lighter.OrderTimeInForceGoodTillTime,
			wantPrice: 10000,
		},
		{
			name:    "perp limit IOC",
			product: exchange.ProductPerp,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "103", Side: exchange.SideBuy,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("101"), LimitPolicy: exchange.LimitPolicyIOC,
			},
			wantType:  lighter.OrderTypeLimit,
			wantTIF:   lighter.OrderTimeInForceImmediateOrCancel,
			wantPrice: 10100,
		},
		{
			name:    "perp limit post-only reduce-only",
			product: exchange.ProductPerp,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "104", Side: exchange.SideSell,
				Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("1.2"),
				LimitPrice: decimal.RequireFromString("102"), LimitPolicy: exchange.LimitPolicyPostOnly,
				ReduceOnly: true,
			},
			wantType:   lighter.OrderTypeLimit,
			wantTIF:    lighter.OrderTimeInForcePostOnly,
			wantPrice:  10200,
			wantReduce: 1,
		},
		{
			name:    "perp market",
			product: exchange.ProductPerp,
			req: exchange.PlaceOrderRequest{
				Instrument: "ETH-USDC", ClientOrderID: "105", Side: exchange.SideBuy,
				Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("1.2"),
			},
			wantType:  lighter.OrderTypeMarket,
			wantTIF:   lighter.OrderTimeInForceImmediateOrCancel,
			wantPrice: 10151,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend, fake := newTestLighterPrivateBackend(tc.product)
			_, err := backend.PlaceOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatal(err)
			}
			if fake.place.OrderType != tc.wantType || fake.place.TimeInForce != tc.wantTIF || fake.place.Price != tc.wantPrice || fake.place.ReduceOnly != tc.wantReduce {
				t.Fatalf("native place=%+v", fake.place)
			}
		})
	}
}

func TestLighterPrivateWSBackendCommandOutcomeSemantics(t *testing.T) {
	tests := []struct {
		name       string
		product    exchange.Product
		operation  exchange.OrderOperation
		outcome    lighter.WSCommandOutcome
		commandErr error
		wantState  exchange.OrderAckState
		wantErr    error
	}{
		{
			name:       "spot place rejected",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xrejected", Sent: true, Code: 400},
			commandErr: lighter.ErrOrderRejected,
			wantState:  exchange.AckRejected,
			wantErr:    exchange.ErrVenueRejected,
		},
		{
			name:       "spot place unknown after send",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunknown", Sent: true},
			commandErr: lighter.ErrWSOutcomeUnknown,
			wantState:  exchange.AckAmbiguous,
			wantErr:    exchange.ErrAmbiguousOutcome,
		},
		{
			name:       "spot place write failed before confirmed send",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunsent"},
			commandErr: errors.New("SECRET write failed"),
			wantErr:    exchange.ErrTransport,
		},
		{
			name:      "spot place malformed response",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationPlace,
			outcome:   lighter.WSCommandOutcome{TransactionHash: "0xmalformed"},
			wantErr:   exchange.ErrMalformedResponse,
		},
		{
			name:       "spot cancel rejected",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xrejected", Sent: true, Code: 409},
			commandErr: lighter.ErrOrderRejected,
			wantState:  exchange.AckRejected,
			wantErr:    exchange.ErrVenueRejected,
		},
		{
			name:       "spot cancel unknown after send",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunknown", Sent: true},
			commandErr: context.DeadlineExceeded,
			wantState:  exchange.AckAmbiguous,
			wantErr:    exchange.ErrAmbiguousOutcome,
		},
		{
			name:       "spot cancel write failed before confirmed send",
			product:    exchange.ProductSpot,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunsent"},
			commandErr: errors.New("SECRET write failed"),
			wantErr:    exchange.ErrTransport,
		},
		{
			name:      "spot cancel malformed response",
			product:   exchange.ProductSpot,
			operation: exchange.OrderOperationCancel,
			outcome:   lighter.WSCommandOutcome{TransactionHash: "0xmalformed"},
			wantErr:   exchange.ErrMalformedResponse,
		},
		{
			name:       "perp place rejected",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xrejected", Sent: true, Code: 400},
			commandErr: lighter.ErrOrderRejected,
			wantState:  exchange.AckRejected,
			wantErr:    exchange.ErrVenueRejected,
		},
		{
			name:       "perp place unknown after send",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunknown", Sent: true},
			commandErr: lighter.ErrWSOutcomeUnknown,
			wantState:  exchange.AckAmbiguous,
			wantErr:    exchange.ErrAmbiguousOutcome,
		},
		{
			name:       "perp place write failed before confirmed send",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationPlace,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunsent"},
			commandErr: errors.New("SECRET write failed"),
			wantErr:    exchange.ErrTransport,
		},
		{
			name:      "perp place malformed response",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationPlace,
			outcome:   lighter.WSCommandOutcome{TransactionHash: "0xmalformed"},
			wantErr:   exchange.ErrMalformedResponse,
		},
		{
			name:       "perp cancel rejected",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xrejected", Sent: true, Code: 409},
			commandErr: lighter.ErrOrderRejected,
			wantState:  exchange.AckRejected,
			wantErr:    exchange.ErrVenueRejected,
		},
		{
			name:       "perp cancel unknown after send",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunknown", Sent: true},
			commandErr: context.DeadlineExceeded,
			wantState:  exchange.AckAmbiguous,
			wantErr:    exchange.ErrAmbiguousOutcome,
		},
		{
			name:       "perp cancel write failed before confirmed send",
			product:    exchange.ProductPerp,
			operation:  exchange.OrderOperationCancel,
			outcome:    lighter.WSCommandOutcome{TransactionHash: "0xunsent"},
			commandErr: errors.New("SECRET write failed"),
			wantErr:    exchange.ErrTransport,
		},
		{
			name:      "perp cancel malformed response",
			product:   exchange.ProductPerp,
			operation: exchange.OrderOperationCancel,
			outcome:   lighter.WSCommandOutcome{TransactionHash: "0xmalformed"},
			wantErr:   exchange.ErrMalformedResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend, fake := newTestLighterPrivateBackend(tt.product)
			var ack exchange.OrderAcknowledgement
			var err error
			if tt.operation == exchange.OrderOperationPlace {
				fake.placeOutcome = tt.outcome
				fake.placeErr = tt.commandErr
				ack, err = backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
					Instrument:    "ETH-USDC",
					ClientOrderID: "101",
					Side:          exchange.SideBuy,
					Type:          exchange.OrderTypeMarket,
					Quantity:      decimal.RequireFromString("1.2"),
				})
			} else {
				fake.cancelOutcome = tt.outcome
				fake.cancelErr = tt.commandErr
				ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
					Instrument: "ETH-USDC",
					OrderID:    "11",
				})
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err=%v, want %v", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("error leaked unsafe command detail: %v", err)
			}
			if ack.State != tt.wantState {
				t.Fatalf("ack=%+v", ack)
			}
			if ack.Product != "" && ack.Product != tt.product {
				t.Fatalf("ack product=%s, want %s", ack.Product, tt.product)
			}
			if tt.wantState != "" && ack.TransactionHash != tt.outcome.TransactionHash {
				t.Fatalf("ack=%+v", ack)
			}
			if tt.wantState == "" && (ack != exchange.OrderAcknowledgement{}) {
				t.Fatalf("failure before a proven venue outcome returned ack=%+v", ack)
			}
		})
	}
}

func TestLighterPrivateWSBackendCancelRejectsMissingOrderIDWithoutNativeCall(t *testing.T) {
	for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		t.Run(string(product), func(t *testing.T) {
			backend, fake := newTestLighterPrivateBackend(product)
			ack, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
				Instrument: "ETH-USDC",
			})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("err=%v, want %v", err, exchange.ErrInvalidRequest)
			}
			if ack != (exchange.OrderAcknowledgement{}) {
				t.Fatalf("invalid request returned ack=%+v", ack)
			}
			if fake.cancelCalls != 0 {
				t.Fatalf("native cancel was called %d times", fake.cancelCalls)
			}
		})
	}
}

func TestLighterPrivateWSBackendRefreshesAuthForSubscriptionReplay(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	backend.authToken = "stale-token"
	if fake.authProvider == nil {
		t.Fatal("subscription auth provider is not installed")
	}
	token, err := fake.authProvider("account_all_trades/42")
	if err != nil {
		t.Fatal(err)
	}
	if token == nil || *token == "" || *token == "stale-token" || backend.authToken != *token {
		t.Fatalf("refreshed token=%v cached=%q", token, backend.authToken)
	}
}

func TestLighterPrivateWSBackendRecoversOnlyAfterFirstValidPrivateEvent(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	var statuses []backendStatus
	stop, err := backend.StartFills(context.Background(), "ETH-USDC", streamCallbacks[exchange.FillEvent]{
		Status: func(status backendStatus) {
			statuses = append(statuses, status)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	fake.reconnectStarted(errors.New("connection lost"))
	fake.reconnectRecovered()
	if len(statuses) != 2 ||
		statuses[0].State != exchange.SubscriptionGap ||
		statuses[1].State != exchange.SubscriptionResyncing {
		t.Fatalf("statuses before valid replay=%+v", statuses)
	}
	fake.emitTrades([]byte(`{"trades":{"7":[]}}`))
	if len(statuses) != 3 ||
		statuses[2].State != exchange.SubscriptionActive ||
		statuses[2].Phase != exchange.GapRecovered {
		t.Fatalf("statuses after valid replay=%+v", statuses)
	}
}

func TestLighterPrivateWSBackendSharesAccountWideFills(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	addTestLighterMarket(backend, "BTC-USDC", 8)
	fake.rejectDuplicateAccount = true
	fake.tradesPayload = []byte(`{"trades":{
		"7":[{"trade_id":71,"market_id":7,"size":"0.3","price":"101","ask_id":11,"bid_id":12,"ask_client_id":123,"bid_client_id":124,"ask_account_id":42,"bid_account_id":99,"is_maker_ask":true,"timestamp":1700000000200,"maker_fee":2,"taker_fee":3}],
		"8":[{"trade_id":81,"market_id":8,"size":"0.4","price":"201","ask_id":21,"bid_id":22,"ask_client_id":223,"bid_client_id":224,"ask_account_id":99,"bid_account_id":42,"is_maker_ask":false,"timestamp":1700000000300,"maker_fee":2,"taker_fee":3}]
	}}`)
	var ethFills, btcFills []string
	ethStop, err := backend.StartFills(context.Background(), "ETH-USDC", streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) {
		ethFills = append(ethFills, event.Fill.FillID)
		if event.Fill.Instrument != "ETH-USDC" {
			t.Fatalf("ETH watcher received fill for %s", event.Fill.Instrument)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	btcStop, err := backend.StartFills(context.Background(), "BTC-USDC", streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) {
		btcFills = append(btcFills, event.Fill.FillID)
		if event.Fill.Instrument != "BTC-USDC" {
			t.Fatalf("BTC watcher received fill for %s", event.Fill.Instrument)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	fake.emitTrades([]byte(`{"trades":{
		"7":[{"trade_id":72,"market_id":7,"size":"0.5","price":"102","ask_id":13,"bid_id":14,"ask_client_id":125,"bid_client_id":126,"ask_account_id":42,"bid_account_id":99,"is_maker_ask":true,"timestamp":1700000000400,"maker_fee":2,"taker_fee":3}],
		"8":[{"trade_id":82,"market_id":8,"size":"0.6","price":"202","ask_id":23,"bid_id":24,"ask_client_id":225,"bid_client_id":226,"ask_account_id":99,"bid_account_id":42,"is_maker_ask":false,"timestamp":1700000000500,"maker_fee":2,"taker_fee":3}]
	}}`))
	if fake.tradesSubscribes != 1 {
		t.Fatalf("native trades subscribes=%d", fake.tradesSubscribes)
	}
	if err := ethStop(); err != nil {
		t.Fatal(err)
	}
	if fake.tradesUnsubscribes != 0 {
		t.Fatalf("native trades unsubscribed before last owner: %d", fake.tradesUnsubscribes)
	}
	if err := btcStop(); err != nil {
		t.Fatal(err)
	}
	if fake.tradesUnsubscribes != 1 {
		t.Fatalf("native trades unsubscribes=%d", fake.tradesUnsubscribes)
	}
	if strings.Join(ethFills, ",") != "71,72" || strings.Join(btcFills, ",") != "82" {
		t.Fatalf("ethFills=%v btcFills=%v", ethFills, btcFills)
	}
}

func TestLighterPrivateWSBackendConcurrentStartSharesAuthAndHubs(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	addTestLighterMarket(backend, "BTC-USDC", 8)
	fake.rejectDuplicateAccount = true
	fake.tradesPayload = []byte(`{"trades":{
		"7":[{"trade_id":91,"market_id":7,"size":"0.3","price":"101","ask_id":11,"bid_id":12,"ask_client_id":123,"bid_client_id":124,"ask_account_id":42,"bid_account_id":99,"is_maker_ask":true,"timestamp":1700000000200,"maker_fee":2,"taker_fee":3}],
		"8":[{"trade_id":92,"market_id":8,"size":"0.4","price":"201","ask_id":21,"bid_id":22,"ask_client_id":223,"bid_client_id":224,"ask_account_id":99,"bid_account_id":42,"is_maker_ask":false,"timestamp":1700000000300,"maker_fee":2,"taker_fee":3}]
	}}`)
	start := make(chan struct{})
	errs := make(chan error, 2)
	stops := make(chan func() error, 2)
	var wg sync.WaitGroup
	for _, instrument := range []string{"ETH-USDC", "BTC-USDC"} {
		instrument := instrument
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stop, err := backend.StartFills(context.Background(), instrument, streamCallbacks[exchange.FillEvent]{})
			errs <- err
			if err == nil {
				stops <- stop
			}
		}()
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	close(stops)
	for stop := range stops {
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
	if fake.tradesSubscribes != 1 || fake.tradesUnsubscribes != 1 {
		t.Fatalf("trades subscribes=%d unsubscribes=%d", fake.tradesSubscribes, fake.tradesUnsubscribes)
	}
}

func TestLighterPrivateWSBackendConcurrentStartPropagatesNativeSubscribeFailure(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	addTestLighterMarket(backend, "BTC-USDC", 8)

	started := make(chan struct{})
	release := make(chan struct{})
	fake.tradesSubscribeErr = errors.New("native subscribe failed")
	fake.tradesSubscribeStarted = started
	fake.tradesSubscribeRelease = release

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 2)
	go func() {
		_, err := backend.StartFills(ctx, "ETH-USDC", streamCallbacks[exchange.FillEvent]{})
		results <- err
	}()
	<-started
	go func() {
		_, err := backend.StartFills(ctx, "BTC-USDC", streamCallbacks[exchange.FillEvent]{})
		results <- err
	}()

	deadline := time.Now().Add(time.Second)
	for {
		hub := backend.accountFillsHub()
		hub.mu.Lock()
		watchers := len(hub.watchers)
		hub.mu.Unlock()
		if watchers == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second watcher did not join the shared startup")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)

	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				t.Fatal("StartFills unexpectedly succeeded")
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("shared startup failure was not propagated to every waiter")
		}
	}
	fake.mu.Lock()
	subscribes := fake.tradesSubscribes
	unsubscribes := fake.tradesUnsubscribes
	fake.mu.Unlock()
	if subscribes != 1 || unsubscribes != 0 {
		t.Fatalf("native trades subscribes=%d unsubscribes=%d", subscribes, unsubscribes)
	}
}

func TestLighterPrivateWSBackendSharesAccountWidePositions(t *testing.T) {
	backend, fake := newTestLighterPrivateBackend(exchange.ProductPerp)
	addTestLighterMarket(backend, "BTC-USDC", 8)
	fake.rejectDuplicateAccount = true
	fake.positionsPayload = []byte(`{"positions":{
		"7":{"market_id":7,"symbol":"ETH","sign":1,"position":"1","avg_entry_price":"100","position_value":"105","unrealized_pnl":"5","liquidation_price":"50","allocated_margin":"20"},
		"8":{"market_id":8,"symbol":"BTC","sign":1,"position":"2","avg_entry_price":"200","position_value":"210","unrealized_pnl":"10","liquidation_price":"100","allocated_margin":"40"}
	}}`)
	var ethSnapshots, btcSnapshots [][]exchange.Position
	ethStop, err := backend.StartPositions(context.Background(), "ETH-USDC", streamCallbacks[exchange.PositionEvent]{Event: func(event exchange.PositionEvent) {
		ethSnapshots = append(ethSnapshots, event.Positions)
		if len(event.Positions) != 1 || event.Positions[0].Instrument != "ETH-USDC" {
			t.Fatalf("ETH watcher received positions=%+v", event.Positions)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	btcStop, err := backend.StartPositions(context.Background(), "BTC-USDC", streamCallbacks[exchange.PositionEvent]{Event: func(event exchange.PositionEvent) {
		btcSnapshots = append(btcSnapshots, event.Positions)
		if len(event.Positions) != 1 || event.Positions[0].Instrument != "BTC-USDC" {
			t.Fatalf("BTC watcher received positions=%+v", event.Positions)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	fake.emitPositions([]byte(`{"positions":{
		"7":{"market_id":7,"symbol":"ETH","sign":1,"position":"3","avg_entry_price":"101","position_value":"303","unrealized_pnl":"6","liquidation_price":"51","allocated_margin":"21"},
		"8":{"market_id":8,"symbol":"BTC","sign":1,"position":"4","avg_entry_price":"201","position_value":"804","unrealized_pnl":"11","liquidation_price":"101","allocated_margin":"41"}
	}}`))
	if fake.positionsSubscribes != 1 {
		t.Fatalf("native positions subscribes=%d", fake.positionsSubscribes)
	}
	if err := ethStop(); err != nil {
		t.Fatal(err)
	}
	if fake.positionsUnsubscribes != 0 {
		t.Fatalf("native positions unsubscribed before last owner: %d", fake.positionsUnsubscribes)
	}
	if err := btcStop(); err != nil {
		t.Fatal(err)
	}
	if fake.positionsUnsubscribes != 1 {
		t.Fatalf("native positions unsubscribes=%d", fake.positionsUnsubscribes)
	}
	if len(ethSnapshots) != 2 || len(btcSnapshots) != 1 {
		t.Fatalf("ethSnapshots=%v btcSnapshots=%v", ethSnapshots, btcSnapshots)
	}
}

func newTestLighterPrivateBackend(product exchange.Product) (*lighterPrivateWSBackend, *fakeLighterPrivateWS) {
	state := newLighterRESTState()
	marketType := lighterPerp
	if product == exchange.ProductSpot {
		marketType = lighterSpot
	}
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{
			Symbol:            "ETH-USDC",
			Product:           product,
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.1"),
			MinQuantity:       decimal.RequireFromString("0.1"),
		},
		marketID:   7,
		marketType: marketType,
		priceScale: decimal.NewFromInt(100),
		sizeScale:  decimal.NewFromInt(10),
		quoteScale: decimal.NewFromInt(100),
	}
	state.metas = map[string]lighterMarketMeta{"ETH-USDC": meta}
	state.byID = map[int]lighterMarketMeta{7: meta}
	rest := lighter.NewClient().WithCredentials(strings.Repeat("01", 40), 42, 7)
	rest.WithBaseURL("https://openapi.invalid")
	rest.HTTPClient = &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/api/v1/orderBookOrders" {
			return openAPIJSONResponse(`{"code":200,"message":"","bids":[{"remaining_base_amount":"1","price":"99"}],"asks":[{"remaining_base_amount":"1","price":"101"}]}`), nil
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       http.NoBody,
		}, nil
	})}
	fake := &fakeLighterPrivateWS{}
	base := newLighterWSBackend(product, marketType, rest, state, fake)
	backend := &lighterPrivateWSBackend{lighterWSBackend: base, ws: fake}
	fake.SetSubscriptionAuthProvider(backend.refreshSubscriptionAuth)
	return backend, fake
}

func addTestLighterMarket(backend *lighterPrivateWSBackend, instrument string, marketID int) {
	meta := lighterMarketMeta{
		instrument: exchange.Instrument{
			Symbol:            instrument,
			Product:           backend.product,
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.1"),
			MinQuantity:       decimal.RequireFromString("0.1"),
		},
		marketID:   marketID,
		marketType: backend.marketType,
		priceScale: decimal.NewFromInt(100),
		sizeScale:  decimal.NewFromInt(10),
		quoteScale: decimal.NewFromInt(100),
	}
	backend.state.metas[instrument] = meta
	backend.state.byID[marketID] = meta
}

func (ws *fakeLighterPrivateWS) emitTrades(payload []byte) {
	ws.mu.Lock()
	handler := ws.tradesHandler
	ws.mu.Unlock()
	if handler != nil {
		handler(payload)
	}
}

func (ws *fakeLighterPrivateWS) emitPositions(payload []byte) {
	ws.mu.Lock()
	handler := ws.positionsHandler
	ws.mu.Unlock()
	if handler != nil {
		handler(payload)
	}
}
