package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func TestBinancePrivateWSSpotStreamsNormalizeAndFilter(t *testing.T) {
	wsapi := &fakeBinanceSpotPrivateAPI{}
	account := &fakeBinanceSpotAccountWS{}
	backend := newBinanceSpotPrivateWSBackendForTest(wsapi, account, "key", "secret")

	var gotOrder exchange.OrderEvent
	var gotFill exchange.FillEvent
	var gotBalance exchange.BalanceEvent
	var gotErr error

	stopOrders, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{
		Event: func(event exchange.OrderEvent) { gotOrder = event },
		Error: func(err error) { gotErr = err },
	})
	if err != nil {
		t.Fatalf("StartOrders: %v", err)
	}
	stopFills, err := backend.StartFills(context.Background(), "ETH-USDT", streamCallbacks[exchange.FillEvent]{
		Event: func(event exchange.FillEvent) { gotFill = event },
		Error: func(err error) { gotErr = err },
	})
	if err != nil {
		t.Fatalf("StartFills: %v", err)
	}
	stopBalances, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{
		Event: func(event exchange.BalanceEvent) { gotBalance = event },
		Error: func(err error) { gotErr = err },
	})
	if err != nil {
		t.Fatalf("StartBalances: %v", err)
	}
	if account.connectCalls != 1 {
		t.Fatalf("private account Connect calls = %d, want 1 shared connection", account.connectCalls)
	}

	account.emitExecution(&binancespot.ExecutionReportEvent{
		Symbol:                   "BTCUSDT",
		OrderID:                  99,
		ClientOrderID:            "1001",
		Side:                     "BUY",
		OrderType:                "LIMIT",
		TimeInForce:              "GTC",
		Quantity:                 "1",
		Price:                    "10",
		CumulativeFilledQuantity: "0",
		OrderStatus:              "NEW",
		CreationTime:             1700000000000,
		TransactionTime:          1700000000001,
	})
	if gotOrder.Order.OrderID != "" {
		t.Fatalf("foreign instrument order leaked: %+v", gotOrder)
	}

	account.emitExecution(&binancespot.ExecutionReportEvent{
		Symbol:                                 "ETHUSDT",
		OrderID:                                77,
		ClientOrderID:                          "1002",
		Side:                                   "SELL",
		OrderType:                              "LIMIT_MAKER",
		Quantity:                               "2.5",
		Price:                                  "101.25",
		LastExecutedQuantity:                   "0.5",
		LastExecutedPrice:                      "101.50",
		CumulativeFilledQuantity:               "0.5",
		CumulativeQuoteAssetTransactedQuantity: "50.75",
		LastQuoteAssetTransactedQuantity:       "50.75",
		CommissionAmount:                       "0.01",
		CommissionAsset:                        "USDT",
		TradeID:                                555,
		IsMaker:                                true,
		OrderStatus:                            "PARTIALLY_FILLED",
		CreationTime:                           1700000000000,
		TransactionTime:                        1700000000002,
	})
	if gotOrder.Kind != exchange.EventDelta || gotOrder.Order.Instrument != "ETH-USDT" ||
		gotOrder.Order.OrderID != "77" || gotOrder.Order.Side != exchange.SideSell ||
		gotOrder.Order.Type != exchange.OrderTypeLimit || gotOrder.Order.LimitPolicy != exchange.LimitPolicyPostOnly ||
		!gotOrder.Order.Filled.Equal(decimal.RequireFromString("0.5")) ||
		!gotOrder.Order.AverageFillPrice.Valid {
		t.Fatalf("order event = %+v", gotOrder)
	}
	if gotFill.Kind != exchange.EventDelta || gotFill.Fill.Instrument != "ETH-USDT" ||
		gotFill.Fill.OrderID != "77" || gotFill.Fill.FillID != "555" ||
		gotFill.Fill.Liquidity != exchange.LiquidityMaker ||
		!gotFill.Fill.Quantity.Equal(decimal.RequireFromString("0.5")) ||
		!gotFill.Fill.Fee.Equal(decimal.RequireFromString("0.01")) {
		t.Fatalf("fill event = %+v", gotFill)
	}

	account.emitBalance(&binancespot.AccountPositionEvent{
		EventTime: 1700000000003,
		Balances: []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		}{
			{Asset: "USDT", Free: "12.5", Locked: "1.5"},
		},
	})
	if gotBalance.Kind != exchange.EventDelta || gotBalance.Time.UnixMilli() != 1700000000003 ||
		len(gotBalance.Balances) != 1 ||
		!gotBalance.Balances[0].Available.Equal(decimal.RequireFromString("12.5")) ||
		!gotBalance.Balances[0].Total.Equal(decimal.RequireFromString("14")) {
		t.Fatalf("balance event = %+v", gotBalance)
	}

	account.emitExecution(&binancespot.ExecutionReportEvent{Symbol: "ETHUSDT", OrderID: 1, ClientOrderID: "1003", Side: "BAD"})
	if gotErr == nil || !errors.Is(gotErr, exchange.ErrMalformedResponse) {
		t.Fatalf("malformed execution error = %v, want ErrMalformedResponse", gotErr)
	}

	if err := stopOrders(); err != nil {
		t.Fatalf("stop orders: %v", err)
	}
	if err := stopFills(); err != nil {
		t.Fatalf("stop fills: %v", err)
	}
	if err := stopBalances(); err != nil {
		t.Fatalf("stop balances: %v", err)
	}
	if account.closeCalls != 0 {
		t.Fatalf("topic stop closed shared account client")
	}
	if err := backend.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if wsapi.closeCalls != 1 || account.closeCalls != 1 {
		t.Fatalf("close calls wsapi=%d account=%d, want 1/1", wsapi.closeCalls, account.closeCalls)
	}
}

func TestBinanceSpotWSFillEventSkipsNonTradeExecutionReports(t *testing.T) {
	fill, ok, err := binanceSpotWSFillEvent("SOL-USDT", "SOLUSDT", &binancespot.ExecutionReportEvent{
		Symbol:               "SOLUSDT",
		ExecutionType:        "NEW",
		LastExecutedQuantity: "0.00000000",
	})
	if err != nil {
		t.Fatalf("non-trade execution report returned error: %v", err)
	}
	if ok {
		t.Fatalf("non-trade execution report produced fill: %+v", fill)
	}
}

func TestBinancePrivateWSSpotCommandsUseWSAPIAndMapBranches(t *testing.T) {
	wsapi := &fakeBinanceSpotPrivateAPI{}
	backend := newBinanceSpotPrivateWSBackendForTest(wsapi, &fakeBinanceSpotAccountWS{}, "key", "secret")

	cases := []struct {
		name string
		req  exchange.PlaceOrderRequest
		want binancespot.PlaceOrderParams
		resp binancespot.OrderResponse
	}{
		{
			name: "market",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "1001", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("0.5")},
			want: binancespot.PlaceOrderParams{Symbol: "ETHUSDT", Side: "BUY", Type: "MARKET", Quantity: "0.5", NewClientOrderID: "1001", NewOrderRespType: "RESULT"},
			resp: binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 800, ClientOrderID: "1001", Status: "FILLED", ExecutedQty: "0.5", CummulativeQuoteQty: "50"},
		},
		{
			name: "limit-resting",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "1002", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("0.6"), LimitPrice: decimal.RequireFromString("101.5"), LimitPolicy: exchange.LimitPolicyResting},
			want: binancespot.PlaceOrderParams{Symbol: "ETHUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "GTC", Quantity: "0.6", Price: "101.5", NewClientOrderID: "1002", NewOrderRespType: "RESULT"},
			resp: binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 800, ClientOrderID: "1002", Status: "NEW", ExecutedQty: "0", CummulativeQuoteQty: "0"},
		},
		{
			name: "limit-ioc",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "1003", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("0.6"), LimitPrice: decimal.RequireFromString("101.5"), LimitPolicy: exchange.LimitPolicyIOC},
			want: binancespot.PlaceOrderParams{Symbol: "ETHUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "IOC", Quantity: "0.6", Price: "101.5", NewClientOrderID: "1003", NewOrderRespType: "RESULT"},
			resp: binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 800, ClientOrderID: "1003", Status: "NEW", ExecutedQty: "0", CummulativeQuoteQty: "0"},
		},
		{
			name: "post-only",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "1004", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("0.7"), LimitPrice: decimal.RequireFromString("99.5"), LimitPolicy: exchange.LimitPolicyPostOnly},
			want: binancespot.PlaceOrderParams{Symbol: "ETHUSDT", Side: "BUY", Type: "LIMIT_MAKER", Quantity: "0.7", Price: "99.5", NewClientOrderID: "1004", NewOrderRespType: "RESULT"},
			resp: binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 800, ClientOrderID: "1004", Status: "NEW", ExecutedQty: "0", CummulativeQuoteQty: "0"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wsapi.placeCalls = 0
			wsapi.placeResponse = &tc.resp
			ack, err := backend.PlaceOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("PlaceOrder: %v", err)
			}
			if wsapi.placeCalls != 1 || wsapi.lastPlaceID == "" || wsapi.lastPlaceKey != "key" || wsapi.lastPlaceSecret != "secret" {
				t.Fatalf("PlaceOrderWS call metadata = calls:%d id:%q key:%q secret:%q", wsapi.placeCalls, wsapi.lastPlaceID, wsapi.lastPlaceKey, wsapi.lastPlaceSecret)
			}
			if wsapi.lastSpotPlace != tc.want {
				t.Fatalf("place params = %+v, want %+v", wsapi.lastSpotPlace, tc.want)
			}
			if ack.Venue != exchange.VenueBinance || ack.Product != exchange.ProductSpot || ack.Operation != exchange.OrderOperationPlace ||
				ack.Instrument != "ETH-USDT" || ack.OrderID != "800" || ack.ClientOrderID != tc.req.ClientOrderID {
				t.Fatalf("ack = %+v", ack)
			}
			wsapi.placeCalls = 0
		})
	}

	wsapi.cancelResponse = &binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 800, ClientOrderID: "1003", Status: "CANCELED"}
	ack, err := backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "800"})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if wsapi.cancelCalls != 1 || wsapi.lastCancelSymbol != "ETHUSDT" || wsapi.lastCancelOrigClientID != "" || wsapi.lastCancelOrderID != 800 {
		t.Fatalf("cancel args = calls:%d symbol:%q orderID:%d clientID:%q", wsapi.cancelCalls, wsapi.lastCancelSymbol, wsapi.lastCancelOrderID, wsapi.lastCancelOrigClientID)
	}
	if ack.State != exchange.AckCanceled || ack.OrderID != "800" || ack.ClientOrderID != "1003" {
		t.Fatalf("cancel ack = %+v", ack)
	}

	wsapi.cancelCalls = 0
	wsapi.cancelResponse = &binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 801, ClientOrderID: "1004", Status: "CANCELED"}
	ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "801"})
	if err != nil {
		t.Fatalf("CancelOrder by order id: %v", err)
	}
	if wsapi.cancelCalls != 1 || wsapi.lastCancelSymbol != "ETHUSDT" || wsapi.lastCancelOrderID != 801 || wsapi.lastCancelOrigClientID != "" {
		t.Fatalf("cancel by order id args = calls:%d symbol:%q orderID:%d clientID:%q", wsapi.cancelCalls, wsapi.lastCancelSymbol, wsapi.lastCancelOrderID, wsapi.lastCancelOrigClientID)
	}
	if ack.State != exchange.AckCanceled || ack.OrderID != "801" || ack.ClientOrderID != "1004" {
		t.Fatalf("cancel by order id ack = %+v", ack)
	}
}

func TestBinancePrivateWSPerpStreamsCommandsAndPositions(t *testing.T) {
	wsapi := &fakeBinancePerpPrivateAPI{}
	account := &fakeBinancePerpAccountWS{}
	backend := newBinancePerpPrivateWSBackendForTest(wsapi, account, "key", "secret")

	var gotOrder exchange.OrderEvent
	var gotFill exchange.FillEvent
	var gotBalance exchange.BalanceEvent
	var gotPosition exchange.PositionEvent
	var gotErr error
	if _, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{Event: func(event exchange.OrderEvent) { gotOrder = event }, Error: func(err error) { gotErr = err }}); err != nil {
		t.Fatalf("StartOrders: %v", err)
	}
	if _, err := backend.StartFills(context.Background(), "ETH-USDT", streamCallbacks[exchange.FillEvent]{Event: func(event exchange.FillEvent) { gotFill = event }, Error: func(err error) { gotErr = err }}); err != nil {
		t.Fatalf("StartFills: %v", err)
	}
	if _, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{Event: func(event exchange.BalanceEvent) { gotBalance = event }, Error: func(err error) { gotErr = err }}); err != nil {
		t.Fatalf("StartBalances: %v", err)
	}
	if _, err := backend.StartPositions(context.Background(), "ETH-USDT", streamCallbacks[exchange.PositionEvent]{Event: func(event exchange.PositionEvent) { gotPosition = event }, Error: func(err error) { gotErr = err }}); err != nil {
		t.Fatalf("StartPositions: %v", err)
	}

	order := &binanceperp.OrderUpdateEvent{EventTime: 1700000000001, TransactionTime: 1700000000002}
	order.Order.Symbol = "ETHUSDT"
	order.Order.OrderID = 88
	order.Order.ClientOrderID = "2001"
	order.Order.Side = "BUY"
	order.Order.OrderType = "LIMIT"
	order.Order.TimeInForce = "GTX"
	order.Order.OriginalQty = "3"
	order.Order.OriginalPrice = "100"
	order.Order.AveragePrice = "101"
	order.Order.AccumulatedFilledQty = "1"
	order.Order.LastFilledQty = "1"
	order.Order.LastFilledPrice = "101"
	order.Order.Commission = "0.02"
	order.Order.CommissionAsset = "USDT"
	order.Order.TradeID = 901
	order.Order.TradeTime = 1700000000003
	order.Order.OrderStatus = "PARTIALLY_FILLED"
	order.Order.PositionSide = "BOTH"
	order.Order.IsReduceOnly = true
	account.emitOrder(order)
	if gotOrder.Order.Instrument != "ETH-USDT" || gotOrder.Order.OrderID != "88" ||
		gotOrder.Order.LimitPolicy != exchange.LimitPolicyPostOnly || !gotOrder.Order.ReduceOnly {
		t.Fatalf("perp order = %+v", gotOrder)
	}
	if gotFill.Fill.Instrument != "ETH-USDT" || gotFill.Fill.FillID != "901" ||
		!gotFill.Fill.Price.Equal(decimal.RequireFromString("101")) || !gotFill.Fill.Fee.Equal(decimal.RequireFromString("0.02")) {
		t.Fatalf("perp fill = %+v", gotFill)
	}

	accountUpdate := &binanceperp.AccountUpdateEvent{EventTime: 1700000000004}
	accountUpdate.UpdateData.Balances = append(accountUpdate.UpdateData.Balances, struct {
		Asset              string `json:"a"`
		WalletBalance      string `json:"wb"`
		CrossWalletBalance string `json:"cw"`
		BalanceChange      string `json:"bc"`
	}{Asset: "USDT", WalletBalance: "15", CrossWalletBalance: "14", BalanceChange: "0"})
	accountUpdate.UpdateData.Positions = append(accountUpdate.UpdateData.Positions, struct {
		Symbol              string `json:"s"`
		PositionAmount      string `json:"pa"`
		EntryPrice          string `json:"ep"`
		AccumulatedRealized string `json:"cr"`
		UnrealizedPnL       string `json:"up"`
		MarginType          string `json:"mt"`
		IsolatedWallet      string `json:"iw"`
		PositionSide        string `json:"ps"`
	}{Symbol: "ETHUSDT", PositionAmount: "-2", EntryPrice: "100", UnrealizedPnL: "1.5", PositionSide: "BOTH"})
	account.emitAccount(accountUpdate)
	if len(gotBalance.Balances) != 1 || !gotBalance.Balances[0].Total.Equal(decimal.RequireFromString("15")) {
		t.Fatalf("perp balances = %+v", gotBalance)
	}
	if len(gotPosition.Positions) != 1 || gotPosition.Positions[0].Instrument != "ETH-USDT" ||
		gotPosition.Positions[0].Side != exchange.SideSell ||
		!gotPosition.Positions[0].Quantity.Equal(decimal.RequireFromString("2")) ||
		!gotPosition.Positions[0].UnrealizedPnL.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("perp positions = %+v", gotPosition)
	}

	account.emitOrder(&binanceperp.OrderUpdateEvent{})
	if gotErr == nil || !errors.Is(gotErr, exchange.ErrMalformedResponse) {
		t.Fatalf("perp malformed error = %v, want ErrMalformedResponse", gotErr)
	}

	wsapi.placeResponse = &binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 801, ClientOrderID: "2002", Status: "NEW", Type: "LIMIT", Side: "SELL", OrigQty: "4", Price: "99", ExecutedQty: "0", AvgPrice: "0", PositionSide: "BOTH", UpdateTime: 1700000000005, ReduceOnly: true}
	ack, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2002", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("4"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyIOC, ReduceOnly: true})
	if err != nil {
		t.Fatalf("perp PlaceOrder: %v", err)
	}
	if wsapi.lastPerpPlace.Symbol != "ETHUSDT" || wsapi.lastPerpPlace.Type != "LIMIT" || wsapi.lastPerpPlace.TimeInForce != "IOC" || !wsapi.lastPerpPlace.ReduceOnly {
		t.Fatalf("perp place params = %+v", wsapi.lastPerpPlace)
	}
	if validateErr := ack.Validate(); validateErr != nil {
		t.Fatalf("perp place ack invalid = %+v err=%v", ack, validateErr)
	}
	if ack.Product != exchange.ProductPerp || ack.OrderID != "801" {
		t.Fatalf("perp place ack = %+v", ack)
	}

	wsapi.cancelResponse = &binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 801, ClientOrderID: "2002", Status: "CANCELED", Type: "LIMIT", Side: "SELL", OrigQty: "4", Price: "99", ExecutedQty: "0", AvgPrice: "0", PositionSide: "BOTH", UpdateTime: 1700000000006}
	ack, err = backend.CancelOrder(context.Background(), exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "801"})
	if err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	if wsapi.lastPerpCancel.Symbol != "ETHUSDT" || wsapi.lastPerpCancel.OrderID != "801" || ack.State != exchange.AckAcceptedPending {
		t.Fatalf("perp cancel args=%+v ack=%+v", wsapi.lastPerpCancel, ack)
	}
}

func TestBinancePrivateWSPerpCommandsNormalizeEveryPortableOrderBranch(t *testing.T) {
	wsapi := &fakeBinancePerpPrivateAPI{}
	backend := newBinancePerpPrivateWSBackendForTest(wsapi, &fakeBinancePerpAccountWS{}, "key", "secret")

	cases := []struct {
		name string
		req  exchange.PlaceOrderRequest
		want binanceperp.PlaceOrderParams
	}{
		{
			name: "market",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2001", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.RequireFromString("1.5")},
			want: binanceperp.PlaceOrderParams{Symbol: "ETHUSDT", Side: "BUY", Type: "MARKET", Quantity: "1.5", NewClientOrderID: "2001"},
		},
		{
			name: "limit-resting",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2002", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("2"), LimitPrice: decimal.RequireFromString("100.5"), LimitPolicy: exchange.LimitPolicyResting},
			want: binanceperp.PlaceOrderParams{Symbol: "ETHUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "GTC", Quantity: "2", Price: "100.5", NewClientOrderID: "2002"},
		},
		{
			name: "limit-ioc",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2003", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("2.5"), LimitPrice: decimal.RequireFromString("100.75"), LimitPolicy: exchange.LimitPolicyIOC},
			want: binanceperp.PlaceOrderParams{Symbol: "ETHUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "IOC", Quantity: "2.5", Price: "100.75", NewClientOrderID: "2003"},
		},
		{
			name: "post-only",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2004", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("3"), LimitPrice: decimal.RequireFromString("99.5"), LimitPolicy: exchange.LimitPolicyPostOnly},
			want: binanceperp.PlaceOrderParams{Symbol: "ETHUSDT", Side: "BUY", Type: "LIMIT", TimeInForce: "GTX", Quantity: "3", Price: "99.5", NewClientOrderID: "2004"},
		},
		{
			name: "reduce-only",
			req:  exchange.PlaceOrderRequest{Instrument: "ETH-USDT", ClientOrderID: "2005", Side: exchange.SideSell, Type: exchange.OrderTypeLimit, Quantity: decimal.RequireFromString("4"), LimitPrice: decimal.RequireFromString("99"), LimitPolicy: exchange.LimitPolicyResting, ReduceOnly: true},
			want: binanceperp.PlaceOrderParams{Symbol: "ETHUSDT", Side: "SELL", Type: "LIMIT", TimeInForce: "GTC", Quantity: "4", Price: "99", NewClientOrderID: "2005", ReduceOnly: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := "NEW"
			executedQty := "0"
			if tc.req.Type == exchange.OrderTypeMarket {
				status = "FILLED"
				executedQty = tc.want.Quantity
			}
			wsapi.placeResponse = &binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 900, ClientOrderID: tc.req.ClientOrderID, Status: status, Type: tc.want.Type, Side: tc.want.Side, OrigQty: tc.want.Quantity, Price: tc.want.Price, ExecutedQty: executedQty, AvgPrice: "100", PositionSide: "BOTH", UpdateTime: 1700000000005, ReduceOnly: tc.req.ReduceOnly}
			ack, err := backend.PlaceOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("PlaceOrder: %v", err)
			}
			if wsapi.lastPerpPlace != tc.want {
				t.Fatalf("perp place params = %+v, want %+v", wsapi.lastPerpPlace, tc.want)
			}
			if ack.Product != exchange.ProductPerp || ack.Operation != exchange.OrderOperationPlace || ack.OrderID != "900" || ack.ClientOrderID != tc.req.ClientOrderID {
				t.Fatalf("perp place ack = %+v", ack)
			}
		})
	}
}

func TestBinancePrivateWSPerpCancelUsesPortableOrderID(t *testing.T) {
	wsapi := &fakeBinancePerpPrivateAPI{}
	backend := newBinancePerpPrivateWSBackendForTest(wsapi, &fakeBinancePerpAccountWS{}, "key", "secret")

	cases := []struct {
		name string
		req  exchange.CancelOrderRequest
		want binanceperp.CancelOrderParams
		resp binanceperp.OrderResponse
	}{
		{
			name: "order-id",
			req:  exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "801"},
			want: binanceperp.CancelOrderParams{Symbol: "ETHUSDT", OrderID: "801"},
			resp: binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 801, ClientOrderID: "2001", Status: "CANCELED", Type: "LIMIT", Side: "SELL", OrigQty: "4", Price: "99", ExecutedQty: "0", AvgPrice: "0", PositionSide: "BOTH", UpdateTime: 1700000000006},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wsapi.cancelResponse = &tc.resp
			ack, err := backend.CancelOrder(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("CancelOrder: %v", err)
			}
			if wsapi.lastPerpCancel != tc.want {
				t.Fatalf("perp cancel params = %+v, want %+v", wsapi.lastPerpCancel, tc.want)
			}
			if ack.Product != exchange.ProductPerp || ack.Operation != exchange.OrderOperationCancel || ack.State != exchange.AckAcceptedPending {
				t.Fatalf("perp cancel ack = %+v", ack)
			}
		})
	}
}

func TestBinancePrivateWSCommandErrorsPreserveOutcomeBoundaries(t *testing.T) {
	place := exchange.PlaceOrderRequest{
		Instrument: "ETH-USDT", ClientOrderID: "1001", Side: exchange.SideBuy,
		Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1),
	}
	cancel := exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "801"}

	t.Run("spot rejection and ambiguity", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			command    string
			commandErr error
			wantState  exchange.OrderAckState
			wantErr    error
		}{
			{
				name:       "place rejected",
				command:    "place",
				commandErr: &binancespot.APIError{Code: -1013, Message: "secret venue rejection", HTTPStatus: http.StatusBadRequest},
				wantState:  exchange.AckRejected,
				wantErr:    exchange.ErrVenueRejected,
			},
			{
				name:       "cancel rejected",
				command:    "cancel",
				commandErr: &binancespot.APIError{Code: -2011, Message: "secret venue rejection", HTTPStatus: http.StatusBadRequest},
				wantState:  exchange.AckRejected,
				wantErr:    exchange.ErrVenueRejected,
			},
			{
				name:       "place ambiguous",
				command:    "place",
				commandErr: errors.New("transport failed: secret wire detail"),
				wantState:  exchange.AckAmbiguous,
				wantErr:    exchange.ErrAmbiguousOutcome,
			},
			{
				name:       "cancel ambiguous",
				command:    "cancel",
				commandErr: errors.New("transport failed: secret wire detail"),
				wantState:  exchange.AckAmbiguous,
				wantErr:    exchange.ErrAmbiguousOutcome,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api := &fakeBinanceSpotPrivateAPI{}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				var ack exchange.OrderAcknowledgement
				var err error
				if tc.command == "place" {
					api.placeErr = tc.commandErr
					ack, err = backend.PlaceOrder(context.Background(), place)
				} else {
					api.cancelErr = tc.commandErr
					ack, err = backend.CancelOrder(context.Background(), cancel)
				}
				if !errors.Is(err, tc.wantErr) || ack.State != tc.wantState {
					t.Fatalf("ack=%+v err=%v, want state=%s error=%v", ack, err, tc.wantState, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(ack.VenueMessage, "secret") {
					t.Fatalf("unsafe venue detail leaked: ack=%+v err=%v", ack, err)
				}
			})
		}
	})

	t.Run("perp rejection and ambiguity", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			command    string
			commandErr error
			wantState  exchange.OrderAckState
			wantErr    error
		}{
			{
				name:       "place rejected",
				command:    "place",
				commandErr: &binanceperp.APIError{Code: -1013, Message: "secret venue rejection", HTTPStatus: http.StatusBadRequest},
				wantState:  exchange.AckRejected,
				wantErr:    exchange.ErrVenueRejected,
			},
			{
				name:       "cancel rejected",
				command:    "cancel",
				commandErr: &binanceperp.APIError{Code: -2011, Message: "secret venue rejection", HTTPStatus: http.StatusBadRequest},
				wantState:  exchange.AckRejected,
				wantErr:    exchange.ErrVenueRejected,
			},
			{
				name:       "place ambiguous",
				command:    "place",
				commandErr: errors.New("secret outcome unknown"),
				wantState:  exchange.AckAmbiguous,
				wantErr:    exchange.ErrAmbiguousOutcome,
			},
			{
				name:       "cancel ambiguous",
				command:    "cancel",
				commandErr: errors.New("secret outcome unknown"),
				wantState:  exchange.AckAmbiguous,
				wantErr:    exchange.ErrAmbiguousOutcome,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				api := &fakeBinancePerpPrivateAPI{}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				var ack exchange.OrderAcknowledgement
				var err error
				if tc.command == "place" {
					api.placeErr = tc.commandErr
					ack, err = backend.PlaceOrder(context.Background(), place)
				} else {
					api.cancelErr = tc.commandErr
					ack, err = backend.CancelOrder(context.Background(), cancel)
				}
				if !errors.Is(err, tc.wantErr) || ack.State != tc.wantState {
					t.Fatalf("ack=%+v err=%v, want state=%s error=%v", ack, err, tc.wantState, tc.wantErr)
				}
				if strings.Contains(err.Error(), "secret") || strings.Contains(ack.VenueMessage, "secret") {
					t.Fatalf("unsafe venue detail leaked: ack=%+v err=%v", ack, err)
				}
			})
		}
	})

	t.Run("connect failure remains pre-send", func(t *testing.T) {
		for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
			t.Run(string(product), func(t *testing.T) {
				var ack exchange.OrderAcknowledgement
				var err error
				if product == exchange.ProductSpot {
					api := &fakeBinanceSpotPrivateAPI{connectErr: errors.New("secret dial detail")}
					backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
					ack, err = backend.PlaceOrder(context.Background(), place)
					if api.placeCalls != 0 {
						t.Fatalf("place command sent after connect failure")
					}
				} else {
					api := &fakeBinancePerpPrivateAPI{connectErr: errors.New("secret dial detail")}
					backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
					ack, err = backend.PlaceOrder(context.Background(), place)
					if api.placeCalls != 0 {
						t.Fatalf("place command sent after connect failure")
					}
				}
				if !errors.Is(err, exchange.ErrTransport) || ack.State != "" {
					t.Fatalf("ack=%+v err=%v, want pre-send transport failure", ack, err)
				}
				if strings.Contains(err.Error(), "secret") {
					t.Fatalf("unsafe connection detail leaked: %v", err)
				}
			})
		}
	})
}

func TestBinancePrivateWSCommandMalformedNativeResponsesReturnNoAck(t *testing.T) {
	place := exchange.PlaceOrderRequest{
		Instrument: "ETH-USDT", ClientOrderID: "1001", Side: exchange.SideBuy,
		Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1),
	}
	cancel := exchange.CancelOrderRequest{Instrument: "ETH-USDT", OrderID: "801"}

	spotGoodPlace := func() *binancespot.OrderResponse {
		return &binancespot.OrderResponse{
			Symbol:        "ETHUSDT",
			OrderID:       800,
			ClientOrderID: "1001",
			Status:        "NEW",
			ExecutedQty:   "0",
		}
	}
	spotGoodCancel := func() *binancespot.OrderResponse {
		return &binancespot.OrderResponse{
			Symbol:        "ETHUSDT",
			OrderID:       801,
			ClientOrderID: "",
		}
	}
	perpGoodPlace := func() *binanceperp.OrderResponse {
		return &binanceperp.OrderResponse{
			Symbol:        "ETHUSDT",
			OrderID:       800,
			ClientOrderID: "1001",
			Status:        "NEW",
			ExecutedQty:   "0",
			PositionSide:  "BOTH",
		}
	}
	perpGoodCancel := func() *binanceperp.OrderResponse {
		return &binanceperp.OrderResponse{
			Symbol:       "ETHUSDT",
			OrderID:      801,
			PositionSide: "BOTH",
		}
	}

	for _, tc := range []struct {
		name string
		run  func() (exchange.OrderAcknowledgement, error)
	}{
		{
			name: "spot place nil response",
			run: func() (exchange.OrderAcknowledgement, error) {
				api := &fakeBinanceSpotPrivateAPI{}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "spot place mismatched symbol",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := spotGoodPlace()
				resp.Symbol = "BTCUSDT"
				api := &fakeBinanceSpotPrivateAPI{placeResponse: resp}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "spot place invalid order id",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := spotGoodPlace()
				resp.OrderID = 0
				api := &fakeBinanceSpotPrivateAPI{placeResponse: resp}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "spot cancel nil response",
			run: func() (exchange.OrderAcknowledgement, error) {
				api := &fakeBinanceSpotPrivateAPI{}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
		{
			name: "spot cancel mismatched order id",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := spotGoodCancel()
				resp.OrderID = 802
				api := &fakeBinanceSpotPrivateAPI{cancelResponse: resp}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
		{
			name: "spot cancel invalid order id",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := spotGoodCancel()
				resp.OrderID = 0
				api := &fakeBinanceSpotPrivateAPI{cancelResponse: resp}
				backend := newBinanceSpotPrivateWSBackendForTest(api, &fakeBinanceSpotAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
		{
			name: "perp place nil response",
			run: func() (exchange.OrderAcknowledgement, error) {
				api := &fakeBinancePerpPrivateAPI{}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "perp place mismatched symbol",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := perpGoodPlace()
				resp.Symbol = "BTCUSDT"
				api := &fakeBinancePerpPrivateAPI{placeResponse: resp}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "perp place invalid status",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := perpGoodPlace()
				resp.Status = "UNKNOWN"
				api := &fakeBinancePerpPrivateAPI{placeResponse: resp}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.PlaceOrder(context.Background(), place)
			},
		},
		{
			name: "perp cancel nil response",
			run: func() (exchange.OrderAcknowledgement, error) {
				api := &fakeBinancePerpPrivateAPI{}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
		{
			name: "perp cancel mismatched order id",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := perpGoodCancel()
				resp.OrderID = 802
				api := &fakeBinancePerpPrivateAPI{cancelResponse: resp}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
		{
			name: "perp cancel invalid native shape",
			run: func() (exchange.OrderAcknowledgement, error) {
				resp := perpGoodCancel()
				resp.PositionSide = "LONG"
				api := &fakeBinancePerpPrivateAPI{cancelResponse: resp}
				backend := newBinancePerpPrivateWSBackendForTest(api, &fakeBinancePerpAccountWS{}, "key", "secret")
				return backend.CancelOrder(context.Background(), cancel)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ack, err := tc.run()
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("ack=%+v err=%v, want ErrMalformedResponse", ack, err)
			}
			if ack.State != "" {
				t.Fatalf("ack=%+v, want no acknowledgement", ack)
			}
		})
	}
}

func TestBinancePrivateWSConstructorsWirePrivateBackends(t *testing.T) {
	spotClient, ok := NewBinanceSpot("key", "secret", Settings{}).(*binanceSpotClient)
	if !ok {
		t.Fatalf("spot client type = %T", NewBinanceSpot("key", "secret", Settings{}))
	}
	spotWS, ok := spotClient.ws.(*spotWebSocket)
	if !ok || spotWS.private == nil || spotWS.private.backend == nil {
		t.Fatalf("spot websocket private backend not wired: %T %#v", spotClient.ws, spotWS)
	}
	perpClient, ok := NewBinanceUSDPerp("key", "secret", Settings{}).(*binancePerpClient)
	if !ok {
		t.Fatalf("perp client type = %T", NewBinanceUSDPerp("key", "secret", Settings{}))
	}
	perpWS, ok := perpClient.ws.(*perpWebSocket)
	if !ok || perpWS.spotWebSocket == nil || perpWS.spotWebSocket.private == nil || perpWS.spotWebSocket.private.backend == nil {
		t.Fatalf("perp websocket private backend not wired: %T %#v", perpClient.ws, perpWS)
	}
}

func TestBinanceSpotConstructorSeparatesCommandAndAccountWSConnections(t *testing.T) {
	client, ok := NewBinanceSpot("key", "secret", Settings{Environment: "demo"}).(*binanceSpotClient)
	if !ok {
		t.Fatalf("spot client type = %T", NewBinanceSpot("key", "secret", Settings{Environment: "demo"}))
	}
	if client.commandWSAPI == nil || client.accountWSAPI == nil {
		t.Fatalf("spot websocket API clients are not wired: command=%p account=%p", client.commandWSAPI, client.accountWSAPI)
	}
	if client.commandWSAPI == client.accountWSAPI {
		t.Fatal("spot order commands and account events share one websocket API connection")
	}
	if client.commandWSAPI.URL != binancespot.DemoWSAPIBaseURL || client.accountWSAPI.URL != binancespot.DemoWSAPIBaseURL {
		t.Fatalf("spot demo websocket API endpoints = command:%q account:%q", client.commandWSAPI.URL, client.accountWSAPI.URL)
	}
}

type fakeBinanceSpotPrivateAPI struct {
	placeCalls             int
	cancelCalls            int
	closeCalls             int
	lastPlaceKey           string
	lastPlaceSecret        string
	lastPlaceID            string
	lastSpotPlace          binancespot.PlaceOrderParams
	placeResponse          *binancespot.OrderResponse
	placeErr               error
	lastCancelKey          string
	lastCancelSecret       string
	lastCancelID           string
	lastCancelSymbol       string
	lastCancelOrderID      int64
	lastCancelOrigClientID string
	cancelResponse         *binancespot.OrderResponse
	cancelErr              error
	connectErr             error
}

func (fake *fakeBinanceSpotPrivateAPI) Connect() error { return fake.connectErr }
func (fake *fakeBinanceSpotPrivateAPI) Close()         { fake.closeCalls++ }
func (fake *fakeBinanceSpotPrivateAPI) PlaceOrderWS(apiKey, secretKey string, params binancespot.PlaceOrderParams, id string) (*binancespot.OrderResponse, error) {
	fake.placeCalls++
	fake.lastPlaceKey = apiKey
	fake.lastPlaceSecret = secretKey
	fake.lastPlaceID = id
	fake.lastSpotPlace = params
	return fake.placeResponse, fake.placeErr
}
func (fake *fakeBinanceSpotPrivateAPI) CancelOrderWS(apiKey, secretKey string, symbol string, orderID int64, origClientOrderID string, id string) (*binancespot.OrderResponse, error) {
	fake.cancelCalls++
	fake.lastCancelKey = apiKey
	fake.lastCancelSecret = secretKey
	fake.lastCancelID = id
	fake.lastCancelSymbol = symbol
	fake.lastCancelOrderID = orderID
	fake.lastCancelOrigClientID = origClientOrderID
	return fake.cancelResponse, fake.cancelErr
}

type fakeBinanceSpotAccountWS struct {
	connectCalls int
	closeCalls   int
	execCBs      []func(*binancespot.ExecutionReportEvent)
	balanceCBs   []func(*binancespot.AccountPositionEvent)
	started      func(error)
	recovered    func()
}

func (fake *fakeBinanceSpotAccountWS) Connect() error { fake.connectCalls++; return nil }
func (fake *fakeBinanceSpotAccountWS) Close()         { fake.closeCalls++ }
func (fake *fakeBinanceSpotAccountWS) SubscribeExecutionReport(cb func(*binancespot.ExecutionReportEvent)) {
	fake.execCBs = append(fake.execCBs, cb)
}
func (fake *fakeBinanceSpotAccountWS) SubscribeAccountPosition(cb func(*binancespot.AccountPositionEvent)) {
	fake.balanceCBs = append(fake.balanceCBs, cb)
}
func (fake *fakeBinanceSpotAccountWS) SetReconnectHooks(started func(error), recovered func()) {
	fake.started = started
	fake.recovered = recovered
}
func (fake *fakeBinanceSpotAccountWS) emitExecution(event *binancespot.ExecutionReportEvent) {
	for _, cb := range fake.execCBs {
		cb(event)
	}
}
func (fake *fakeBinanceSpotAccountWS) emitBalance(event *binancespot.AccountPositionEvent) {
	for _, cb := range fake.balanceCBs {
		cb(event)
	}
}

type fakeBinancePerpPrivateAPI struct {
	placeCalls     int
	cancelCalls    int
	closeCalls     int
	lastPerpPlace  binanceperp.PlaceOrderParams
	lastPerpCancel binanceperp.CancelOrderParams
	placeResponse  *binanceperp.OrderResponse
	cancelResponse *binanceperp.OrderResponse
	placeErr       error
	cancelErr      error
	connectErr     error
}

func (fake *fakeBinancePerpPrivateAPI) Connect() error { return fake.connectErr }
func (fake *fakeBinancePerpPrivateAPI) Close()         { fake.closeCalls++ }
func (fake *fakeBinancePerpPrivateAPI) PlaceOrderWS(apiKey, secretKey string, params binanceperp.PlaceOrderParams, id string) (*binanceperp.OrderResponse, error) {
	fake.placeCalls++
	fake.lastPerpPlace = params
	return fake.placeResponse, fake.placeErr
}
func (fake *fakeBinancePerpPrivateAPI) CancelOrderWS(apiKey, secretKey string, params binanceperp.CancelOrderParams, id string) (*binanceperp.OrderResponse, error) {
	fake.cancelCalls++
	fake.lastPerpCancel = params
	return fake.cancelResponse, fake.cancelErr
}

type fakeBinancePerpAccountWS struct {
	connectCalls int
	closeCalls   int
	orderCBs     []func(*binanceperp.OrderUpdateEvent)
	accountCBs   []func(*binanceperp.AccountUpdateEvent)
}

func (fake *fakeBinancePerpAccountWS) Connect() error { fake.connectCalls++; return nil }
func (fake *fakeBinancePerpAccountWS) Close()         { fake.closeCalls++ }
func (fake *fakeBinancePerpAccountWS) SubscribeOrderUpdate(cb func(*binanceperp.OrderUpdateEvent)) {
	fake.orderCBs = append(fake.orderCBs, cb)
}
func (fake *fakeBinancePerpAccountWS) SubscribeAccountUpdate(cb func(*binanceperp.AccountUpdateEvent)) {
	fake.accountCBs = append(fake.accountCBs, cb)
}
func (fake *fakeBinancePerpAccountWS) SetReconnectHooks(func(error), func()) {}
func (fake *fakeBinancePerpAccountWS) emitOrder(event *binanceperp.OrderUpdateEvent) {
	for _, cb := range fake.orderCBs {
		cb(event)
	}
}
func (fake *fakeBinancePerpAccountWS) emitAccount(event *binanceperp.AccountUpdateEvent) {
	for _, cb := range fake.accountCBs {
		cb(event)
	}
}
