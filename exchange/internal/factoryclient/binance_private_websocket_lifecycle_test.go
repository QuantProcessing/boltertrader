package factoryclient

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	"github.com/shopspring/decimal"
)

func TestBinancePrivateWSSpotConnectSingleflightAndCloseRace(t *testing.T) {
	api := &binanceSpotLifecycleAPI{
		placeResponse: &binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 1, ClientOrderID: "1001", Status: "FILLED", ExecutedQty: "1", CummulativeQuoteQty: "100"},
	}
	account := &binanceSpotLifecycleAccount{}
	backend := newBinanceSpotPrivateWSBackendForTest(api, account, "key", "secret")

	runConcurrent(t, 12, func() {
		if _, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
			Instrument: "ETH-USDT", ClientOrderID: "1001", Side: exchange.SideBuy,
			Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1),
		}); err != nil {
			t.Errorf("PlaceOrder: %v", err)
		}
	})
	if got := api.connectCount(); got != 1 {
		t.Fatalf("spot api Connect calls = %d, want 1", got)
	}

	runConcurrent(t, 12, func() {
		stop, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{Event: func(exchange.OrderEvent) {}})
		if err != nil {
			t.Errorf("StartOrders: %v", err)
			return
		}
		if err := stop(); err != nil {
			t.Errorf("stop: %v", err)
		}
	})
	if got := account.connectCount(); got != 1 {
		t.Fatalf("spot account Connect calls = %d, want 1", got)
	}

	raceAPI := &binanceSpotLifecycleAPI{
		connectStarted: make(chan struct{}),
		connectRelease: make(chan struct{}),
		placeResponse:  &binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 2, ClientOrderID: "1002", Status: "FILLED", ExecutedQty: "1", CummulativeQuoteQty: "100"},
	}
	connectStarted := raceAPI.connectStarted
	connectRelease := raceAPI.connectRelease
	raceBackend := newBinanceSpotPrivateWSBackendForTest(raceAPI, &binanceSpotLifecycleAccount{}, "key", "secret")
	done := make(chan struct{})
	go func() {
		_, _ = raceBackend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
			Instrument: "ETH-USDT", ClientOrderID: "1002", Side: exchange.SideBuy,
			Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1),
		})
		close(done)
	}()
	<-connectStarted
	closeDone := make(chan struct{})
	go func() {
		_ = raceBackend.Close()
		_ = raceBackend.Close()
		close(closeDone)
	}()
	close(connectRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("PlaceOrder did not return after releasing in-flight Connect")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not return after in-flight Connect")
	}
	if got := raceAPI.closeCount(); got != 1 {
		t.Fatalf("spot api Close calls after connect/close race = %d, want 1", got)
	}
}

func TestBinancePrivateWSSpotRegistersOneNativeCallbackAndRemovesRoutes(t *testing.T) {
	account := &fakeBinanceSpotAccountWS{}
	backend := newBinanceSpotPrivateWSBackendForTest(&fakeBinanceSpotPrivateAPI{}, account, "key", "secret")

	var ordersA, ordersB, fills, balances int
	stopA, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{Event: func(exchange.OrderEvent) { ordersA++ }})
	if err != nil {
		t.Fatalf("StartOrders A: %v", err)
	}
	stopB, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{Event: func(exchange.OrderEvent) { ordersB++ }})
	if err != nil {
		t.Fatalf("StartOrders B: %v", err)
	}
	stopFill, err := backend.StartFills(context.Background(), "ETH-USDT", streamCallbacks[exchange.FillEvent]{Event: func(exchange.FillEvent) { fills++ }})
	if err != nil {
		t.Fatalf("StartFills: %v", err)
	}
	stopBalanceA, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{Event: func(exchange.BalanceEvent) { balances++ }})
	if err != nil {
		t.Fatalf("StartBalances A: %v", err)
	}
	stopBalanceB, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{Event: func(exchange.BalanceEvent) { balances++ }})
	if err != nil {
		t.Fatalf("StartBalances B: %v", err)
	}
	if len(account.execCBs) != 1 || len(account.balanceCBs) != 1 {
		t.Fatalf("native callbacks exec=%d balance=%d, want 1/1", len(account.execCBs), len(account.balanceCBs))
	}

	account.emitExecution(spotLifecycleExecution("ETHUSDT", 10, "1003"))
	if ordersA != 1 || ordersB != 1 || fills != 1 {
		t.Fatalf("before stop ordersA=%d ordersB=%d fills=%d, want 1/1/1", ordersA, ordersB, fills)
	}
	if err := stopA(); err != nil {
		t.Fatalf("stop A: %v", err)
	}
	if err := stopA(); err != nil {
		t.Fatalf("stop A idempotent: %v", err)
	}
	account.emitExecution(spotLifecycleExecution("ETHUSDT", 11, "1004"))
	if ordersA != 1 || ordersB != 2 || fills != 2 {
		t.Fatalf("after stop ordersA=%d ordersB=%d fills=%d, want 1/2/2", ordersA, ordersB, fills)
	}

	account.emitBalance(&binancespot.AccountPositionEvent{
		EventTime: 1700000000001,
		Balances: []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		}{{Asset: "USDT", Free: "1", Locked: "0"}},
	})
	if balances != 2 {
		t.Fatalf("balances = %d, want 2", balances)
	}
	if err := stopBalanceA(); err != nil {
		t.Fatalf("stop balance A: %v", err)
	}
	account.emitBalance(&binancespot.AccountPositionEvent{
		EventTime: 1700000000002,
		Balances: []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		}{{Asset: "USDT", Free: "2", Locked: "0"}},
	})
	if balances != 3 {
		t.Fatalf("balances after stop = %d, want 3", balances)
	}
	if len(account.execCBs) != 1 || len(account.balanceCBs) != 1 {
		t.Fatalf("native callbacks accumulated after stops exec=%d balance=%d", len(account.execCBs), len(account.balanceCBs))
	}
	_ = stopB()
	_ = stopFill()
	_ = stopBalanceB()
}

func TestBinancePrivateWSPerpConnectSingleflightAndCallbacks(t *testing.T) {
	api := &binancePerpLifecycleAPI{
		placeResponse: &binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 1, ClientOrderID: "1001", Status: "FILLED", Type: "MARKET", Side: "BUY", OrigQty: "1", ExecutedQty: "1", AvgPrice: "100", PositionSide: "BOTH"},
	}
	account := &binancePerpLifecycleAccount{}
	backend := newBinancePerpPrivateWSBackendForTest(api, account, "key", "secret")

	runConcurrent(t, 12, func() {
		if _, err := backend.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
			Instrument: "ETH-USDT", ClientOrderID: "1001", Side: exchange.SideBuy,
			Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1),
		}); err != nil {
			t.Errorf("PlaceOrder: %v", err)
		}
	})
	if got := api.connectCount(); got != 1 {
		t.Fatalf("perp api Connect calls = %d, want 1", got)
	}

	var orders, fills, balances, positions int
	stopOrder, err := backend.StartOrders(context.Background(), "ETH-USDT", streamCallbacks[exchange.OrderEvent]{Event: func(exchange.OrderEvent) { orders++ }})
	if err != nil {
		t.Fatalf("StartOrders: %v", err)
	}
	stopFill, err := backend.StartFills(context.Background(), "ETH-USDT", streamCallbacks[exchange.FillEvent]{Event: func(exchange.FillEvent) { fills++ }})
	if err != nil {
		t.Fatalf("StartFills: %v", err)
	}
	stopBalance, err := backend.StartBalances(context.Background(), streamCallbacks[exchange.BalanceEvent]{Event: func(exchange.BalanceEvent) { balances++ }})
	if err != nil {
		t.Fatalf("StartBalances: %v", err)
	}
	stopPosition, err := backend.StartPositions(context.Background(), "ETH-USDT", streamCallbacks[exchange.PositionEvent]{Event: func(exchange.PositionEvent) { positions++ }})
	if err != nil {
		t.Fatalf("StartPositions: %v", err)
	}
	if got := account.connectCount(); got != 1 {
		t.Fatalf("perp account Connect calls = %d, want 1", got)
	}
	if account.orderCallbackCount() != 1 || account.accountCallbackCount() != 1 {
		t.Fatalf("perp native callbacks order=%d account=%d, want 1/1", account.orderCallbackCount(), account.accountCallbackCount())
	}

	account.emitOrder(perpLifecycleOrder("ETHUSDT", 20, "1003"))
	account.emitAccount(perpLifecycleAccountUpdate("ETHUSDT"))
	if orders != 1 || fills != 1 || balances != 1 || positions != 1 {
		t.Fatalf("before stop orders=%d fills=%d balances=%d positions=%d, want 1 each", orders, fills, balances, positions)
	}
	_ = stopOrder()
	_ = stopBalance()
	account.emitOrder(perpLifecycleOrder("ETHUSDT", 21, "1004"))
	account.emitAccount(perpLifecycleAccountUpdate("ETHUSDT"))
	if orders != 1 || fills != 2 || balances != 1 || positions != 2 {
		t.Fatalf("after stop orders=%d fills=%d balances=%d positions=%d, want 1/2/1/2", orders, fills, balances, positions)
	}
	if account.orderCallbackCount() != 1 || account.accountCallbackCount() != 1 {
		t.Fatalf("perp native callbacks accumulated order=%d account=%d", account.orderCallbackCount(), account.accountCallbackCount())
	}
	_ = stopFill()
	_ = stopPosition()
	_ = backend.Close()
	_ = backend.Close()
	if api.closeCount() != 1 || account.closeCount() != 1 {
		t.Fatalf("perp close counts api=%d account=%d, want 1/1", api.closeCount(), account.closeCount())
	}
}

func runConcurrent(t *testing.T, n int, fn func()) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			fn()
		}()
	}
	wg.Wait()
}

type binanceSpotLifecycleAPI struct {
	mu             sync.Mutex
	connectCalls   int
	closeCalls     int
	placeCalls     int
	connectStarted chan struct{}
	connectRelease chan struct{}
	placeResponse  *binancespot.OrderResponse
}

func (api *binanceSpotLifecycleAPI) Connect() error {
	api.mu.Lock()
	api.connectCalls++
	started := api.connectStarted
	release := api.connectRelease
	if started != nil {
		close(started)
		api.connectStarted = nil
	}
	api.mu.Unlock()
	if release != nil {
		<-release
	}
	return nil
}

func (api *binanceSpotLifecycleAPI) Close() {
	api.mu.Lock()
	api.closeCalls++
	api.mu.Unlock()
}

func (api *binanceSpotLifecycleAPI) PlaceOrderWS(string, string, binancespot.PlaceOrderParams, string) (*binancespot.OrderResponse, error) {
	api.mu.Lock()
	api.placeCalls++
	resp := api.placeResponse
	api.mu.Unlock()
	return resp, nil
}

func (api *binanceSpotLifecycleAPI) CancelOrderWS(string, string, string, int64, string, string) (*binancespot.OrderResponse, error) {
	return &binancespot.OrderResponse{Symbol: "ETHUSDT", OrderID: 1, Status: "CANCELED"}, nil
}

func (api *binanceSpotLifecycleAPI) connectCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.connectCalls
}

func (api *binanceSpotLifecycleAPI) closeCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.closeCalls
}

type binanceSpotLifecycleAccount struct {
	mu         sync.Mutex
	connects   int
	closes     int
	execCBs    []func(*binancespot.ExecutionReportEvent)
	balanceCBs []func(*binancespot.AccountPositionEvent)
	started    func(error)
	recovered  func()
}

func (account *binanceSpotLifecycleAccount) Connect() error {
	account.mu.Lock()
	account.connects++
	account.mu.Unlock()
	return nil
}
func (account *binanceSpotLifecycleAccount) Close() {
	account.mu.Lock()
	account.closes++
	account.mu.Unlock()
}
func (account *binanceSpotLifecycleAccount) SetReconnectHooks(started func(error), recovered func()) {
	account.mu.Lock()
	account.started = started
	account.recovered = recovered
	account.mu.Unlock()
}
func (account *binanceSpotLifecycleAccount) SubscribeExecutionReport(cb func(*binancespot.ExecutionReportEvent)) {
	account.mu.Lock()
	account.execCBs = append(account.execCBs, cb)
	account.mu.Unlock()
}
func (account *binanceSpotLifecycleAccount) SubscribeAccountPosition(cb func(*binancespot.AccountPositionEvent)) {
	account.mu.Lock()
	account.balanceCBs = append(account.balanceCBs, cb)
	account.mu.Unlock()
}
func (account *binanceSpotLifecycleAccount) connectCount() int {
	account.mu.Lock()
	defer account.mu.Unlock()
	return account.connects
}

type binancePerpLifecycleAPI struct {
	mu            sync.Mutex
	connectCalls  int
	closeCalls    int
	placeResponse *binanceperp.OrderResponse
}

func (api *binancePerpLifecycleAPI) Connect() error {
	api.mu.Lock()
	api.connectCalls++
	api.mu.Unlock()
	return nil
}
func (api *binancePerpLifecycleAPI) Close() {
	api.mu.Lock()
	api.closeCalls++
	api.mu.Unlock()
}
func (api *binancePerpLifecycleAPI) PlaceOrderWS(string, string, binanceperp.PlaceOrderParams, string) (*binanceperp.OrderResponse, error) {
	api.mu.Lock()
	resp := api.placeResponse
	api.mu.Unlock()
	return resp, nil
}
func (api *binancePerpLifecycleAPI) CancelOrderWS(string, string, binanceperp.CancelOrderParams, string) (*binanceperp.OrderResponse, error) {
	return &binanceperp.OrderResponse{Symbol: "ETHUSDT", OrderID: 1, Status: "CANCELED", Type: "LIMIT", Side: "BUY", OrigQty: "1", ExecutedQty: "0", AvgPrice: "0", PositionSide: "BOTH"}, nil
}
func (api *binancePerpLifecycleAPI) connectCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.connectCalls
}
func (api *binancePerpLifecycleAPI) closeCount() int {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.closeCalls
}

type binancePerpLifecycleAccount struct {
	mu         sync.Mutex
	connects   int
	closes     int
	orderCBs   []func(*binanceperp.OrderUpdateEvent)
	accountCBs []func(*binanceperp.AccountUpdateEvent)
}

func (account *binancePerpLifecycleAccount) Connect() error {
	account.mu.Lock()
	account.connects++
	account.mu.Unlock()
	return nil
}
func (account *binancePerpLifecycleAccount) Close() {
	account.mu.Lock()
	account.closes++
	account.mu.Unlock()
}
func (account *binancePerpLifecycleAccount) SetReconnectHooks(func(error), func()) {}
func (account *binancePerpLifecycleAccount) SubscribeOrderUpdate(cb func(*binanceperp.OrderUpdateEvent)) {
	account.mu.Lock()
	account.orderCBs = append(account.orderCBs, cb)
	account.mu.Unlock()
}
func (account *binancePerpLifecycleAccount) SubscribeAccountUpdate(cb func(*binanceperp.AccountUpdateEvent)) {
	account.mu.Lock()
	account.accountCBs = append(account.accountCBs, cb)
	account.mu.Unlock()
}
func (account *binancePerpLifecycleAccount) emitOrder(event *binanceperp.OrderUpdateEvent) {
	account.mu.Lock()
	cbs := append([]func(*binanceperp.OrderUpdateEvent){}, account.orderCBs...)
	account.mu.Unlock()
	for _, cb := range cbs {
		cb(event)
	}
}
func (account *binancePerpLifecycleAccount) emitAccount(event *binanceperp.AccountUpdateEvent) {
	account.mu.Lock()
	cbs := append([]func(*binanceperp.AccountUpdateEvent){}, account.accountCBs...)
	account.mu.Unlock()
	for _, cb := range cbs {
		cb(event)
	}
}
func (account *binancePerpLifecycleAccount) connectCount() int {
	account.mu.Lock()
	defer account.mu.Unlock()
	return account.connects
}
func (account *binancePerpLifecycleAccount) closeCount() int {
	account.mu.Lock()
	defer account.mu.Unlock()
	return account.closes
}
func (account *binancePerpLifecycleAccount) orderCallbackCount() int {
	account.mu.Lock()
	defer account.mu.Unlock()
	return len(account.orderCBs)
}
func (account *binancePerpLifecycleAccount) accountCallbackCount() int {
	account.mu.Lock()
	defer account.mu.Unlock()
	return len(account.accountCBs)
}

func spotLifecycleExecution(symbol string, orderID int64, clientID string) *binancespot.ExecutionReportEvent {
	return &binancespot.ExecutionReportEvent{
		Symbol:                                 symbol,
		OrderID:                                orderID,
		ClientOrderID:                          clientID,
		Side:                                   "BUY",
		OrderType:                              "LIMIT",
		TimeInForce:                            "GTC",
		Quantity:                               "1",
		Price:                                  "100",
		LastExecutedQuantity:                   "1",
		LastExecutedPrice:                      "100",
		CumulativeFilledQuantity:               "1",
		CumulativeQuoteAssetTransactedQuantity: "100",
		LastQuoteAssetTransactedQuantity:       "100",
		CommissionAmount:                       "0",
		CommissionAsset:                        "USDT",
		TradeID:                                orderID,
		OrderStatus:                            "FILLED",
		CreationTime:                           1700000000000,
		TransactionTime:                        1700000000001,
	}
}

func perpLifecycleOrder(symbol string, orderID int64, clientID string) *binanceperp.OrderUpdateEvent {
	event := &binanceperp.OrderUpdateEvent{EventTime: 1700000000001, TransactionTime: 1700000000002}
	event.Order.Symbol = symbol
	event.Order.OrderID = orderID
	event.Order.ClientOrderID = clientID
	event.Order.Side = "BUY"
	event.Order.OrderType = "LIMIT"
	event.Order.TimeInForce = "GTC"
	event.Order.OriginalQty = "1"
	event.Order.OriginalPrice = "100"
	event.Order.AveragePrice = "100"
	event.Order.AccumulatedFilledQty = "1"
	event.Order.LastFilledQty = "1"
	event.Order.LastFilledPrice = "100"
	event.Order.Commission = "0"
	event.Order.CommissionAsset = "USDT"
	event.Order.TradeID = orderID
	event.Order.TradeTime = 1700000000003
	event.Order.OrderStatus = "FILLED"
	event.Order.PositionSide = "BOTH"
	return event
}

func perpLifecycleAccountUpdate(symbol string) *binanceperp.AccountUpdateEvent {
	event := &binanceperp.AccountUpdateEvent{EventTime: 1700000000004}
	event.UpdateData.Balances = append(event.UpdateData.Balances, struct {
		Asset              string `json:"a"`
		WalletBalance      string `json:"wb"`
		CrossWalletBalance string `json:"cw"`
		BalanceChange      string `json:"bc"`
	}{Asset: "USDT", WalletBalance: "15", CrossWalletBalance: "14", BalanceChange: "0"})
	event.UpdateData.Positions = append(event.UpdateData.Positions, struct {
		Symbol              string `json:"s"`
		PositionAmount      string `json:"pa"`
		EntryPrice          string `json:"ep"`
		AccumulatedRealized string `json:"cr"`
		UnrealizedPnL       string `json:"up"`
		MarginType          string `json:"mt"`
		IsolatedWallet      string `json:"iw"`
		PositionSide        string `json:"ps"`
	}{Symbol: symbol, PositionAmount: "1", EntryPrice: "100", UnrealizedPnL: "1", PositionSide: "BOTH"})
	return event
}
