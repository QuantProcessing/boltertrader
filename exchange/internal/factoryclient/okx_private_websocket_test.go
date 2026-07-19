package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

type fakeOKXPrivateWSClient struct {
	connects   int
	closed     bool
	connectErr error

	reconnectStarted   func(error)
	reconnectRecovered func()

	orderSubs    []okx.WsSubscribeArgs
	accountSubs  int
	positionSubs []okx.WsSubscribeArgs
	unsubs       []okx.WsSubscribeArgs

	orderHandler    func(*okx.Order)
	orderError      func(error)
	accountHandler  func(*okx.Balance)
	accountError    func(error)
	positionHandler func(*okx.Position)
	positionError   func(error)

	placed   []*okx.OrderRequest
	canceled []fakeOKXCancelRequest

	placeResult  *okx.OrderId
	placeErr     error
	cancelResult *okx.OrderId
	cancelErr    error
}

type fakeOKXCancelRequest struct {
	instIDCode int64
	orderID    *string
	clientID   *string
}

func (client *fakeOKXPrivateWSClient) Connect() error {
	client.connects++
	return client.connectErr
}

func (client *fakeOKXPrivateWSClient) Close() {
	client.closed = true
}

func (client *fakeOKXPrivateWSClient) SetReconnectHooks(started func(error), recovered func()) {
	client.reconnectStarted = started
	client.reconnectRecovered = recovered
}

func (client *fakeOKXPrivateWSClient) SubscribeOrdersWithError(
	instType string,
	instID *string,
	handler func(*okx.Order),
	errorHandler func(error),
) error {
	args := okx.WsSubscribeArgs{Channel: "orders", InstType: instType}
	if instID != nil {
		args.InstId = *instID
	}
	client.orderSubs = append(client.orderSubs, args)
	client.orderHandler = handler
	client.orderError = errorHandler
	return nil
}

func (client *fakeOKXPrivateWSClient) SubscribeAccountWithError(handler func(*okx.Balance), errorHandler func(error)) error {
	client.accountSubs++
	client.accountHandler = handler
	client.accountError = errorHandler
	return nil
}

func (client *fakeOKXPrivateWSClient) SubscribePositionsWithError(
	instType string,
	handler func(*okx.Position),
	errorHandler func(error),
) error {
	args := okx.WsSubscribeArgs{Channel: "positions", InstType: instType}
	client.positionSubs = append(client.positionSubs, args)
	client.positionHandler = handler
	client.positionError = errorHandler
	return nil
}

func (client *fakeOKXPrivateWSClient) PlaceOrderWS(req *okx.OrderRequest) (*okx.OrderId, error) {
	client.placed = append(client.placed, req)
	return client.placeResult, client.placeErr
}

func (client *fakeOKXPrivateWSClient) CancelOrderWS(instIDCode int64, orderID, clientOrderID *string) (*okx.OrderId, error) {
	client.canceled = append(client.canceled, fakeOKXCancelRequest{
		instIDCode: instIDCode,
		orderID:    orderID,
		clientID:   clientOrderID,
	})
	return client.cancelResult, client.cancelErr
}

func (client *fakeOKXPrivateWSClient) Unsubscribe(args okx.WsSubscribeArgs) error {
	client.unsubs = append(client.unsubs, args)
	return nil
}

func TestOKXPrivateWSBackend_OrdersAndFillsShareOrdersChannel(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{}
	backend := newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)

	var orders []exchange.OrderEvent
	stopOrders, err := backend.StartOrders(context.Background(), "BTC-USDT", streamCallbacks[exchange.OrderEvent]{
		Event: func(event exchange.OrderEvent) { orders = append(orders, event) },
	})
	if err != nil {
		t.Fatalf("StartOrders: %v", err)
	}
	var fills []exchange.FillEvent
	stopFills, err := backend.StartFills(context.Background(), "BTC-USDT", streamCallbacks[exchange.FillEvent]{
		Event: func(event exchange.FillEvent) { fills = append(fills, event) },
	})
	if err != nil {
		t.Fatalf("StartFills: %v", err)
	}
	if ws.connects != 1 {
		t.Fatalf("expected one shared connect, got %d", ws.connects)
	}
	if len(ws.orderSubs) != 1 || ws.orderSubs[0] != (okx.WsSubscribeArgs{Channel: "orders", InstType: okxSpotType, InstId: "BTC-USDT"}) {
		t.Fatalf("unexpected order subscriptions: %#v", ws.orderSubs)
	}

	ws.orderHandler(&okx.Order{
		InstType:  okxSpotType,
		InstId:    "ETH-USDT",
		OrdId:     "ignored",
		ClOrdId:   "12",
		Side:      okx.SideBuy,
		OrdType:   okx.OrderTypeLimit,
		Sz:        "1",
		Px:        "100",
		AccFillSz: "0",
		State:     okx.OrderStatusLive,
		CTime:     "1700000000000",
		UTime:     "1700000000000",
	})
	ws.orderHandler(&okx.Order{
		InstType:   okxSpotType,
		InstId:     "BTC-USDT",
		OrdId:      "1",
		ClOrdId:    "12",
		Side:       okx.SideBuy,
		OrdType:    okx.OrderTypeLimit,
		Sz:         "1",
		Px:         "100",
		AccFillSz:  "0.2",
		AvgPx:      "101",
		State:      okx.OrderStatusPartiallyFilled,
		FillPx:     "101",
		FillSz:     "0.2",
		FillTime:   "1700000001000",
		TradeId:    "t1",
		Fee:        "-0.01",
		FeeCcy:     "USDT",
		ExecType:   "T",
		CTime:      "1700000000000",
		UTime:      "1700000001000",
		ReduceOnly: "false",
	})
	if len(orders) != 1 || orders[0].Order.OrderID != "1" || !orders[0].Order.Filled.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("unexpected orders: %#v", orders)
	}
	if len(fills) != 1 || fills[0].Fill.FillID != "t1" || !fills[0].Fill.Quantity.Equal(decimal.RequireFromString("0.2")) {
		t.Fatalf("unexpected fills: %#v", fills)
	}

	if err := stopFills(); err != nil {
		t.Fatalf("stop fills: %v", err)
	}
	if len(ws.unsubs) != 0 {
		t.Fatalf("fills must not unsubscribe shared orders channel while orders remain: %#v", ws.unsubs)
	}
	if err := stopOrders(); err != nil {
		t.Fatalf("stop orders: %v", err)
	}
	if len(ws.unsubs) != 1 || ws.unsubs[0] != (okx.WsSubscribeArgs{Channel: "orders", InstType: okxSpotType, InstId: "BTC-USDT"}) {
		t.Fatalf("unexpected unsubs: %#v", ws.unsubs)
	}
}

func TestOKXPrivateWSBackend_BalancesPositionsErrorsAndClose(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{}
	backend := newOKXPerpPrivateWSBackendWithClient(
		ws,
		fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT-SWAP": 2001}),
		fakeOKXPerpMetaLoader(map[string]okxContractMeta{
			"BTC-USDT-SWAP": {contractValue: decimal.RequireFromString("0.01"), contractIncrement: decimal.NewFromInt(1)},
		}),
	)

	var balances []exchange.BalanceEvent
	var balanceErrors []error
	stopBalances, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{
		Event: func(event exchange.BalanceEvent) { balances = append(balances, event) },
		Error: func(err error) { balanceErrors = append(balanceErrors, err) },
	})
	if err != nil {
		t.Fatalf("StartBalances: %v", err)
	}
	var positions []exchange.PositionEvent
	var positionErrors []error
	stopPositions, err := backend.StartPositions(context.Background(), "BTC-USDT-SWAP", streamCallbacks[exchange.PositionEvent]{
		Event: func(event exchange.PositionEvent) { positions = append(positions, event) },
		Error: func(err error) { positionErrors = append(positionErrors, err) },
	})
	if err != nil {
		t.Fatalf("StartPositions: %v", err)
	}
	ws.accountHandler(&okx.Balance{Details: []okx.BalanceDetail{{Ccy: "USDT", Eq: "11", AvailBal: "10", FrozenBal: "1"}}})
	ws.positionHandler(&okx.Position{
		InstType: okxSwapType,
		InstId:   "BTC-USDT-SWAP",
		PosSide:  okx.PosSideNet,
		Pos:      "2",
		AvgPx:    "100",
		MarkPx:   "101",
		Upl:      "2",
		LiqPx:    "50",
		Lever:    "5",
		Margin:   "20",
	})
	ws.accountError(errors.New("bad account payload"))
	ws.positionError(errors.New("bad position payload"))

	if len(balances) != 1 || len(balances[0].Balances) != 1 || balances[0].Balances[0].Asset != "USDT" {
		t.Fatalf("unexpected balances: %#v", balances)
	}
	if len(positions) != 1 || len(positions[0].Positions) != 1 || !positions[0].Positions[0].Quantity.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("unexpected positions: %#v", positions)
	}
	if len(balanceErrors) != 1 || !errors.Is(balanceErrors[0], exchange.ErrMalformedResponse) {
		t.Fatalf("expected normalized balance error, got %#v", balanceErrors)
	}
	if len(positionErrors) != 1 || !errors.Is(positionErrors[0], exchange.ErrMalformedResponse) {
		t.Fatalf("expected normalized position error, got %#v", positionErrors)
	}

	ws.reconnectStarted(errors.New("gap"))
	ws.reconnectRecovered()
	if ws.reconnectStarted == nil || ws.reconnectRecovered == nil {
		t.Fatalf("reconnect hooks were not installed")
	}

	if err := stopPositions(); err != nil {
		t.Fatalf("stop positions: %v", err)
	}
	if err := stopBalances(); err != nil {
		t.Fatalf("stop balances: %v", err)
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !ws.closed {
		t.Fatalf("expected ws closed")
	}
	if len(ws.unsubs) != 2 {
		t.Fatalf("expected account and positions unsubs, got %#v", ws.unsubs)
	}
}

func TestOKXPrivateWSBackend_SpotBalancesNormalizeAccountChannel(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{}
	backend := newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)

	var balances []exchange.BalanceEvent
	stopBalances, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{
		Event: func(event exchange.BalanceEvent) { balances = append(balances, event) },
	})
	if err != nil {
		t.Fatalf("StartBalances: %v", err)
	}

	ws.accountHandler(&okx.Balance{
		UTime:   "1700000000005",
		Details: []okx.BalanceDetail{{Ccy: "USDT", Eq: "11.5", AvailBal: "10.25", FrozenBal: "1.25"}},
	})
	if len(balances) != 1 || balances[0].Kind != exchange.EventSnapshot || balances[0].Time.UnixMilli() != 1700000000005 {
		t.Fatalf("unexpected spot balance event: %#v", balances)
	}
	if len(balances[0].Balances) != 1 || balances[0].Balances[0].Asset != "USDT" ||
		!balances[0].Balances[0].Available.Equal(decimal.RequireFromString("10.25")) ||
		!balances[0].Balances[0].Total.Equal(decimal.RequireFromString("11.5")) {
		t.Fatalf("unexpected spot balances: %#v", balances[0].Balances)
	}

	if err := stopBalances(); err != nil {
		t.Fatalf("stop balances: %v", err)
	}
	if len(ws.unsubs) != 1 || ws.unsubs[0] != (okx.WsSubscribeArgs{Channel: "account"}) {
		t.Fatalf("unexpected balance unsubs: %#v", ws.unsubs)
	}
}

func TestOKXPrivateWSBackend_WSCommandsNormalizeEveryPortableOrderBranch(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{
		placeResult:  &okx.OrderId{OrdId: "900", ClOrdId: "77", SCode: "0"},
		cancelResult: &okx.OrderId{OrdId: "900", ClOrdId: "77", SCode: "0"},
	}
	backend := newOKXPerpPrivateWSBackendWithClient(
		ws,
		fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT-SWAP": 2001}),
		fakeOKXPerpMetaLoader(map[string]okxContractMeta{
			"BTC-USDT-SWAP": {contractValue: decimal.RequireFromString("0.01"), contractIncrement: decimal.NewFromInt(1)},
		}),
	)

	cases := []struct {
		name       string
		req        exchange.PlaceOrderRequest
		wantType   string
		wantPx     *string
		reduceOnly bool
	}{
		{
			name:     "market",
			req:      okxPrivateTestPlace(exchange.OrderTypeMarket, "", decimal.Zero, false),
			wantType: "market",
		},
		{
			name:     "limit_resting",
			req:      okxPrivateTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyResting, decimal.RequireFromString("100"), false),
			wantType: "limit",
			wantPx:   okxStringPtr("100"),
		},
		{
			name:     "limit_ioc",
			req:      okxPrivateTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyIOC, decimal.RequireFromString("100"), false),
			wantType: "ioc",
			wantPx:   okxStringPtr("100"),
		},
		{
			name:       "limit_post_only_reduce_only",
			req:        okxPrivateTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyPostOnly, decimal.RequireFromString("100"), true),
			wantType:   "post_only",
			wantPx:     okxStringPtr("100"),
			reduceOnly: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack, err := backend.PlaceOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("PlaceOrder: %v", err)
			}
			if ack.State != exchange.AckAcceptedPending || ack.OrderType != tc.req.Type {
				t.Fatalf("unexpected ack: %#v", ack)
			}
			native := ws.placed[len(ws.placed)-1]
			if native.InstIdCode == nil || *native.InstIdCode != 2001 {
				t.Fatalf("unexpected instIdCode: %#v", native.InstIdCode)
			}
			if native.TdMode != okxCrossMode || native.OrdType != tc.wantType || native.Sz != "2" {
				t.Fatalf("unexpected native request: %#v", native)
			}
			if stringPtrValue(native.Px) != stringPtrValue(tc.wantPx) {
				t.Fatalf("unexpected price: got %v want %v", stringPtrValue(native.Px), stringPtrValue(tc.wantPx))
			}
			if native.ReduceOnly == nil || *native.ReduceOnly != tc.reduceOnly {
				t.Fatalf("unexpected reduceOnly: %#v", native.ReduceOnly)
			}
		})
	}

	ack, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "BTC-USDT-SWAP",
		OrderID:    "900",
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if ack.State != exchange.AckAcceptedPending || ack.Operation != exchange.OrderOperationCancel {
		t.Fatalf("unexpected cancel ack: %#v", ack)
	}
	if len(ws.canceled) != 1 || ws.canceled[0].instIDCode != 2001 || stringPtrValue(ws.canceled[0].orderID) != "900" {
		t.Fatalf("unexpected cancel call: %#v", ws.canceled)
	}

}

func TestOKXPrivateWSBackend_SpotWSCommandsNormalizeEveryPortableOrderBranch(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{
		placeResult:  &okx.OrderId{OrdId: "901", ClOrdId: "88", SCode: "0"},
		cancelResult: &okx.OrderId{OrdId: "901", ClOrdId: "88", SCode: "0"},
	}
	backend := newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)

	cases := []struct {
		name       string
		req        exchange.PlaceOrderRequest
		wantType   string
		wantPx     *string
		wantTgtCcy *string
	}{
		{
			name:       "market",
			req:        okxPrivateSpotTestPlace(exchange.OrderTypeMarket, "", decimal.Zero),
			wantType:   "market",
			wantTgtCcy: okxStringPtr("base_ccy"),
		},
		{
			name:     "limit_resting",
			req:      okxPrivateSpotTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyResting, decimal.RequireFromString("100")),
			wantType: "limit",
			wantPx:   okxStringPtr("100"),
		},
		{
			name:     "limit_ioc",
			req:      okxPrivateSpotTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyIOC, decimal.RequireFromString("100")),
			wantType: "ioc",
			wantPx:   okxStringPtr("100"),
		},
		{
			name:     "post_only",
			req:      okxPrivateSpotTestPlace(exchange.OrderTypeLimit, exchange.LimitPolicyPostOnly, decimal.RequireFromString("100")),
			wantType: "post_only",
			wantPx:   okxStringPtr("100"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ack, err := backend.PlaceOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("PlaceOrder: %v", err)
			}
			if ack.Product != exchange.ProductSpot || ack.State != exchange.AckAcceptedPending || ack.OrderType != tc.req.Type {
				t.Fatalf("unexpected ack: %#v", ack)
			}
			native := ws.placed[len(ws.placed)-1]
			if native.InstId != "BTC-USDT" || native.InstIdCode == nil || *native.InstIdCode != 1001 {
				t.Fatalf("unexpected instrument fields: %#v", native)
			}
			if native.TdMode != okxCashMode || native.OrdType != tc.wantType || native.Sz != "0.25" {
				t.Fatalf("unexpected native request: %#v", native)
			}
			if stringPtrValue(native.Px) != stringPtrValue(tc.wantPx) {
				t.Fatalf("unexpected price: got %v want %v", stringPtrValue(native.Px), stringPtrValue(tc.wantPx))
			}
			if stringPtrValue(native.TgtCcy) != stringPtrValue(tc.wantTgtCcy) {
				t.Fatalf("unexpected tgtCcy: got %v want %v", stringPtrValue(native.TgtCcy), stringPtrValue(tc.wantTgtCcy))
			}
			if native.ReduceOnly != nil {
				t.Fatalf("spot request must not send reduceOnly: %#v", native.ReduceOnly)
			}
		})
	}

	ws.cancelResult = &okx.OrderId{OrdId: "901", ClOrdId: "88", SCode: "0"}
	ack, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
		Instrument: "BTC-USDT",
		OrderID:    "901",
	})
	if err != nil {
		t.Fatalf("CancelOrder by order id: %v", err)
	}
	if ack.Product != exchange.ProductSpot || ack.State != exchange.AckAcceptedPending || ack.OrderID != "901" {
		t.Fatalf("unexpected order-id cancel ack: %#v", ack)
	}
	if len(ws.canceled) != 1 || ws.canceled[0].instIDCode != 1001 ||
		stringPtrValue(ws.canceled[0].orderID) != "901" || ws.canceled[0].clientID != nil {
		t.Fatalf("unexpected order-id cancel call: %#v", ws.canceled)
	}
}

func okxPrivateTestPlace(orderType exchange.OrderType, policy exchange.LimitPolicy, price decimal.Decimal, reduceOnly bool) exchange.PlaceOrderRequest {
	return exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT-SWAP",
		ClientOrderID: "77",
		Side:          exchange.SideBuy,
		Type:          orderType,
		Quantity:      decimal.RequireFromString("0.02"),
		LimitPrice:    price,
		LimitPolicy:   policy,
		ReduceOnly:    reduceOnly,
	}
}

func okxPrivateSpotTestPlace(orderType exchange.OrderType, policy exchange.LimitPolicy, price decimal.Decimal) exchange.PlaceOrderRequest {
	return exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "88",
		Side:          exchange.SideBuy,
		Type:          orderType,
		Quantity:      decimal.RequireFromString("0.25"),
		LimitPrice:    price,
		LimitPolicy:   policy,
	}
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func fakeOKXInstrumentCodeLoader(values map[string]int64) okxInstrumentCodeLoader {
	return func(ctx context.Context, operation, instrument string) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		code, ok := values[instrument]
		if !ok {
			return 0, okxInvalid(exchange.ProductPerp, operation, "missing instrument code")
		}
		return code, nil
	}
}

func fakeOKXPerpMetaLoader(values map[string]okxContractMeta) okxPerpMetaLoader {
	return func(ctx context.Context, instrument string) (okxContractMeta, error) {
		if err := ctx.Err(); err != nil {
			return okxContractMeta{}, err
		}
		meta, ok := values[instrument]
		if !ok {
			return okxContractMeta{}, okxInvalid(exchange.ProductPerp, "test", "missing perp meta")
		}
		return meta, nil
	}
}

func TestOKXPrivateWSBackend_CommandErrorsMapToAmbiguousAndRejected(t *testing.T) {
	ws := &fakeOKXPrivateWSClient{
		placeErr: errors.New("timeout waiting for order response"),
	}
	backend := newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)
	_, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "77",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	})
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("expected ambiguous place error, got %v", err)
	}

	ws.placeErr = nil
	ws.placeResult = &okx.OrderId{OrdId: "900", ClOrdId: "77", SCode: "51000"}
	ack, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "77",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeMarket,
		Quantity:      decimal.NewFromInt(1),
	})
	if !errors.Is(err, exchange.ErrVenueRejected) || ack.State != exchange.AckRejected {
		t.Fatalf("expected rejected ack+error, got %#v %v", ack, err)
	}

	ws.cancelErr = errors.New("timeout waiting for cancel response")
	_, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "900"})
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("expected ambiguous cancel error, got %v", err)
	}
}

func TestOKXPrivateWSBackend_PostSendAmbiguousForEveryProductAndCommand(t *testing.T) {
	for _, tc := range []struct {
		name    string
		product exchange.Product
		command exchange.OrderOperation
	}{
		{name: "spot place", product: exchange.ProductSpot, command: exchange.OrderOperationPlace},
		{name: "spot cancel", product: exchange.ProductSpot, command: exchange.OrderOperationCancel},
		{name: "perp place", product: exchange.ProductPerp, command: exchange.OrderOperationPlace},
		{name: "perp cancel", product: exchange.ProductPerp, command: exchange.OrderOperationCancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcomeErr := fmt.Errorf("%w: secret socket detail", okx.ErrWSOutcomeUnknown)
			ws := &fakeOKXPrivateWSClient{}
			var backend privateWSBackend
			instrument := "BTC-USDT"
			if tc.product == exchange.ProductSpot {
				backend = newOKXSpotPrivateWSBackendWithClient(
					ws,
					fakeOKXInstrumentCodeLoader(map[string]int64{instrument: 1001}),
					nil,
				)
			} else {
				instrument = "BTC-USDT-SWAP"
				backend = newOKXPerpPrivateWSBackendWithClient(
					ws,
					fakeOKXInstrumentCodeLoader(map[string]int64{instrument: 2001}),
					fakeOKXPerpMetaLoader(map[string]okxContractMeta{
						instrument: {contractValue: decimal.RequireFromString("0.01"), contractIncrement: decimal.NewFromInt(1)},
					}),
				)
			}

			var ack exchange.OrderAcknowledgement
			var err error
			if tc.command == exchange.OrderOperationPlace {
				ws.placeErr = outcomeErr
				ack, err = backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
					Instrument:    instrument,
					ClientOrderID: "77",
					Side:          exchange.SideBuy,
					Type:          exchange.OrderTypeMarket,
					Quantity:      decimal.NewFromInt(1),
				})
			} else {
				ws.cancelErr = outcomeErr
				ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{
					Instrument: instrument,
					OrderID:    "900",
				})
			}
			if !errors.Is(err, exchange.ErrAmbiguousOutcome) ||
				ack.State != exchange.AckAmbiguous ||
				ack.Product != tc.product ||
				ack.Operation != tc.command {
				t.Fatalf("ack=%+v err=%v, want product-scoped ambiguous outcome", ack, err)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(ack.VenueMessage, "secret") {
				t.Fatalf("unsafe outcome detail leaked: ack=%+v err=%v", ack, err)
			}
			if tc.command == exchange.OrderOperationPlace && len(ws.placed) != 1 {
				t.Fatalf("place calls=%d, want 1", len(ws.placed))
			}
			if tc.command == exchange.OrderOperationCancel && len(ws.canceled) != 1 {
				t.Fatalf("cancel calls=%d, want 1", len(ws.canceled))
			}
		})
	}
}

func TestOKXPrivateWSBackend_CommandPreSendFailuresDoNotSend(t *testing.T) {
	for _, tc := range []struct {
		name    string
		product exchange.Product
		command exchange.OrderOperation
	}{
		{name: "spot place", product: exchange.ProductSpot, command: exchange.OrderOperationPlace},
		{name: "spot cancel", product: exchange.ProductSpot, command: exchange.OrderOperationCancel},
		{name: "perp place", product: exchange.ProductPerp, command: exchange.OrderOperationPlace},
		{name: "perp cancel", product: exchange.ProductPerp, command: exchange.OrderOperationCancel},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := &fakeOKXPrivateWSClient{connectErr: errors.New("secret dial detail")}
			var backend privateWSBackend
			if tc.product == exchange.ProductSpot {
				backend = newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)
			} else {
				backend = newOKXPerpPrivateWSBackendWithClient(
					ws,
					fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT-SWAP": 2001}),
					fakeOKXPerpMetaLoader(map[string]okxContractMeta{
						"BTC-USDT-SWAP": {contractValue: decimal.RequireFromString("0.01"), contractIncrement: decimal.NewFromInt(1)},
					}),
				)
			}
			instrument := "BTC-USDT"
			if tc.product == exchange.ProductPerp {
				instrument = "BTC-USDT-SWAP"
			}
			var ack exchange.OrderAcknowledgement
			var err error
			if tc.command == exchange.OrderOperationPlace {
				ack, err = backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
					Instrument:    instrument,
					ClientOrderID: "77",
					Side:          exchange.SideBuy,
					Type:          exchange.OrderTypeMarket,
					Quantity:      decimal.RequireFromString("1"),
				})
			} else {
				ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "900"})
			}
			if !errors.Is(err, exchange.ErrTransport) || ack.State != "" {
				t.Fatalf("ack=%+v err=%v, want pre-send transport failure without acknowledgement", ack, err)
			}
			if len(ws.placed) != 0 || len(ws.canceled) != 0 {
				t.Fatalf("command was sent after pre-send failure: placed=%d canceled=%d", len(ws.placed), len(ws.canceled))
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe transport detail leaked: %v", err)
			}
		})
	}
}

func TestOKXPrivateWSBackend_CommandRejectedAndMalformedRowsPreserveBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name      string
		product   exchange.Product
		command   exchange.OrderOperation
		row       *okx.OrderId
		wantErr   error
		wantState exchange.OrderAckState
		wantAck   bool
		wantCode  string
	}{
		{
			name:      "spot place rejected row",
			product:   exchange.ProductSpot,
			command:   exchange.OrderOperationPlace,
			row:       &okx.OrderId{OrdId: "900", ClOrdId: "77", SCode: "51000"},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
			wantAck:   true,
			wantCode:  "51000",
		},
		{
			name:      "spot cancel rejected row",
			product:   exchange.ProductSpot,
			command:   exchange.OrderOperationCancel,
			row:       &okx.OrderId{OrdId: "900", ClOrdId: "77", SCode: "51000"},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
			wantAck:   true,
			wantCode:  "51000",
		},
		{
			name:      "perp place rejected row",
			product:   exchange.ProductPerp,
			command:   exchange.OrderOperationPlace,
			row:       &okx.OrderId{OrdId: "901", ClOrdId: "77", SCode: "51000"},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
			wantAck:   true,
			wantCode:  "51000",
		},
		{
			name:      "perp cancel rejected row",
			product:   exchange.ProductPerp,
			command:   exchange.OrderOperationCancel,
			row:       &okx.OrderId{OrdId: "901", ClOrdId: "77", SCode: "51000"},
			wantErr:   exchange.ErrVenueRejected,
			wantState: exchange.AckRejected,
			wantAck:   true,
			wantCode:  "51000",
		},
		{
			name:    "spot place nil row",
			product: exchange.ProductSpot,
			command: exchange.OrderOperationPlace,
			row:     nil,
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:    "spot cancel missing sCode",
			product: exchange.ProductSpot,
			command: exchange.OrderOperationCancel,
			row:     &okx.OrderId{OrdId: "900", ClOrdId: "77"},
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:    "perp place missing sCode",
			product: exchange.ProductPerp,
			command: exchange.OrderOperationPlace,
			row:     &okx.OrderId{OrdId: "901", ClOrdId: "77"},
			wantErr: exchange.ErrMalformedResponse,
		},
		{
			name:    "perp cancel nil row",
			product: exchange.ProductPerp,
			command: exchange.OrderOperationCancel,
			row:     nil,
			wantErr: exchange.ErrMalformedResponse,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := &fakeOKXPrivateWSClient{}
			var backend privateWSBackend
			instrument := "BTC-USDT"
			if tc.product == exchange.ProductSpot {
				backend = newOKXSpotPrivateWSBackendWithClient(ws, fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT": 1001}), nil)
			} else {
				instrument = "BTC-USDT-SWAP"
				backend = newOKXPerpPrivateWSBackendWithClient(
					ws,
					fakeOKXInstrumentCodeLoader(map[string]int64{"BTC-USDT-SWAP": 2001}),
					fakeOKXPerpMetaLoader(map[string]okxContractMeta{
						"BTC-USDT-SWAP": {contractValue: decimal.RequireFromString("0.01"), contractIncrement: decimal.NewFromInt(1)},
					}),
				)
			}
			if tc.command == exchange.OrderOperationPlace {
				ws.placeResult = tc.row
			} else {
				ws.cancelResult = tc.row
			}

			var ack exchange.OrderAcknowledgement
			var err error
			if tc.command == exchange.OrderOperationPlace {
				ack, err = backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
					Instrument:    instrument,
					ClientOrderID: "77",
					Side:          exchange.SideBuy,
					Type:          exchange.OrderTypeMarket,
					Quantity:      decimal.RequireFromString("1"),
				})
			} else {
				ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: instrument, OrderID: "900"})
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ack=%+v err=%v, want %v", ack, err, tc.wantErr)
			}
			if tc.wantAck {
				if ack.State != tc.wantState || ack.VenueCode != tc.wantCode {
					t.Fatalf("ack=%+v, want state=%s code=%s", ack, tc.wantState, tc.wantCode)
				}
			} else if ack.State != "" {
				t.Fatalf("ack=%+v, want no acknowledgement", ack)
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(ack.VenueMessage, "secret") {
				t.Fatalf("unsafe venue detail leaked: ack=%+v err=%v", ack, err)
			}
		})
	}
}

var _ = time.Second
