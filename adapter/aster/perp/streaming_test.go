package perp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
)

func TestPerpMarketSubscriptionsUseInjectedSDKWebsocket(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	market := newMarketDataClient(nil, ws, testProvider(inst), nil)

	if err := market.SubscribeBook(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeBook returned error: %v", err)
	}
	if err := market.SubscribeQuotes(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeQuotes returned error: %v", err)
	}
	if err := market.SubscribeTrades(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeTrades returned error: %v", err)
	}

	if !ws.connected {
		t.Fatalf("market websocket was not connected")
	}
	if ws.bookSymbol != inst.VenueSymbol || ws.quoteSymbol != inst.VenueSymbol || ws.tradeSymbol != inst.VenueSymbol {
		t.Fatalf("subscriptions used symbols book=%q quote=%q trade=%q want %q", ws.bookSymbol, ws.quoteSymbol, ws.tradeSymbol, inst.VenueSymbol)
	}
}

func TestPerpMarketReconnectCallsSDKConnectAgainAndKeepsChannel(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	market := newMarketDataClient(nil, ws, testProvider(inst), nil)
	ch := market.Events()
	if err := market.SubscribeTrades(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if !market.Connected() || ws.connects != 1 {
		t.Fatalf("initial connect state connected=%v connects=%d", market.Connected(), ws.connects)
	}
	ws.connected = false
	if err := market.Reconnect(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !market.Connected() || ws.connects != 2 {
		t.Fatalf("reconnect state connected=%v connects=%d", market.Connected(), ws.connects)
	}
	if ch != market.Events() {
		t.Fatalf("Reconnect changed market event channel")
	}
	if ws.tradeSubs != 1 {
		t.Fatalf("Reconnect duplicated adapter trade subscription registrations: %d", ws.tradeSubs)
	}
}

func TestPerpStreamEventsTranslateAndFailClosed(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	market := newMarketDataClient(nil, ws, testProvider(inst), nil)
	if err := market.SubscribeTrades(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := ws.tradeHandler(&sdkperp.WsAggTradeEvent{Symbol: inst.VenueSymbol, AggTradeID: 99, Price: "10.5", Quantity: "2", TradeTime: 1700000000000, IsBuyerMaker: true}); err != nil {
		t.Fatalf("trade handler returned error: %v", err)
	}
	env := <-market.Events()
	trade := env.Payload.(contract.TradeEvent).Trade
	if trade.InstrumentID != inst.ID || trade.TradeID != "99" || trade.AggressorSide != enums.SideSell || !trade.Timestamp.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("bad trade event: %#v", trade)
	}
	if err := ws.tradeHandler(&sdkperp.WsAggTradeEvent{Symbol: inst.VenueSymbol, AggTradeID: 100, Price: "bad", Quantity: "2"}); err == nil {
		t.Fatalf("malformed trade event accepted")
	}
	if err := ws.tradeHandler(&sdkperp.WsAggTradeEvent{Symbol: "OTHERUSDT", AggTradeID: 101, Price: "10", Quantity: "2", TradeTime: 1700000000000}); err == nil {
		t.Fatalf("cross-symbol trade event accepted")
	}
	if err := ws.tradeHandler(&sdkperp.WsAggTradeEvent{Symbol: inst.VenueSymbol, AggTradeID: 102, Price: "10", Quantity: "2"}); err == nil {
		t.Fatalf("missing timestamp trade event accepted")
	}
	select {
	case env := <-market.Events():
		t.Fatalf("malformed trade emitted event: %#v", env)
	default:
	}
}

func TestPerpPrivateOrderUpdateRejectsMalformedIdentitiesEnumsAndHedgeMode(t *testing.T) {
	valid := &sdkperp.OrderUpdateEvent{EventType: "ORDER_TRADE_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000}
	valid.Order.Symbol = "ASTERUSDT"
	valid.Order.ClientOrderID = "c1"
	valid.Order.Side = "SELL"
	valid.Order.OrderType = "LIMIT"
	valid.Order.TimeInForce = "GTC"
	valid.Order.OriginalQty = "1"
	valid.Order.OriginalPrice = "10"
	valid.Order.ExecutionType = "TRADE"
	valid.Order.OrderStatus = "PARTIALLY_FILLED"
	valid.Order.OrderID = 42
	valid.Order.LastFilledQty = "0.5"
	valid.Order.AccumulatedFilledQty = "0.5"
	valid.Order.LastFilledPrice = "10"
	valid.Order.Commission = "0.01"
	valid.Order.CommissionAsset = "USDT"
	valid.Order.TradeTime = 1700000000000
	valid.Order.TradeID = 99
	valid.Order.PositionSide = "BOTH"
	resolved := func(string) (model.InstrumentID, bool) { return testPerpID(), true }
	if events, err := execEventsFromOrderUpdate(valid, resolved, AccountIDDefault); err != nil || len(events) != 2 {
		t.Fatalf("valid private event rejected events=%d err=%v", len(events), err)
	}
	if _, err := execEventsFromOrderUpdate(valid, func(string) (model.InstrumentID, bool) { return model.InstrumentID{}, false }, AccountIDDefault); err == nil {
		t.Fatalf("unknown private event symbol accepted")
	}
	cases := map[string]func(*sdkperp.OrderUpdateEvent){
		"missing client": func(e *sdkperp.OrderUpdateEvent) { e.Order.ClientOrderID = "" },
		"missing order":  func(e *sdkperp.OrderUpdateEvent) { e.Order.OrderID = 0 },
		"missing trade":  func(e *sdkperp.OrderUpdateEvent) { e.Order.TradeID = 0 },
		"missing time":   func(e *sdkperp.OrderUpdateEvent) { e.EventTime, e.TransactionTime, e.Order.TradeTime = 0, 0, 0 },
		"bad status":     func(e *sdkperp.OrderUpdateEvent) { e.Order.OrderStatus = "BOGUS" },
		"bad type":       func(e *sdkperp.OrderUpdateEvent) { e.Order.OrderType = "BOGUS" },
		"bad tif":        func(e *sdkperp.OrderUpdateEvent) { e.Order.TimeInForce = "BOGUS" },
		"hedge side":     func(e *sdkperp.OrderUpdateEvent) { e.Order.PositionSide = "LONG" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ev := *valid
			mutate(&ev)
			if _, err := execEventsFromOrderUpdate(&ev, resolved, AccountIDDefault); err == nil {
				t.Fatalf("malformed order update accepted")
			}
		})
	}
}

func TestPerpAccountUpdateEmitsOnlyBalanceAndPositionDeltas(t *testing.T) {
	events, err := accountEventsFromUpdate(&sdkperp.AccountUpdateEvent{
		EventType: "ACCOUNT_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000,
		UpdateData: struct {
			EventReasonType string `json:"m"`
			Balances        []struct {
				Asset              string `json:"a"`
				WalletBalance      string `json:"wb"`
				CrossWalletBalance string `json:"cw"`
				BalanceChange      string `json:"bc"`
			} `json:"B"`
			Positions []struct {
				Symbol              string `json:"s"`
				PositionAmount      string `json:"pa"`
				EntryPrice          string `json:"ep"`
				AccumulatedRealized string `json:"cr"`
				UnrealizedPnL       string `json:"up"`
				MarginType          string `json:"mt"`
				IsolatedWallet      string `json:"iw"`
				PositionSide        string `json:"ps"`
			} `json:"P"`
		}{
			Balances: []struct {
				Asset              string `json:"a"`
				WalletBalance      string `json:"wb"`
				CrossWalletBalance string `json:"cw"`
				BalanceChange      string `json:"bc"`
			}{{Asset: "USDT", WalletBalance: "10", CrossWalletBalance: "8"}},
			Positions: []struct {
				Symbol              string `json:"s"`
				PositionAmount      string `json:"pa"`
				EntryPrice          string `json:"ep"`
				AccumulatedRealized string `json:"cr"`
				UnrealizedPnL       string `json:"up"`
				MarginType          string `json:"mt"`
				IsolatedWallet      string `json:"iw"`
				PositionSide        string `json:"ps"`
			}{{Symbol: "ASTERUSDT", PositionAmount: "1", EntryPrice: "2", UnrealizedPnL: "0", PositionSide: "BOTH"}},
		},
	}, func(string) (model.InstrumentID, bool) { return testPerpID(), true }, AccountIDDefault)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events len=%d, want balance and position deltas only", len(events))
	}
	if _, ok := events[0].(contract.BalanceEvent); !ok {
		t.Fatalf("event[0]=%T, want BalanceEvent", events[0])
	}
	if _, ok := events[1].(contract.PositionEvent); !ok {
		t.Fatalf("event[1]=%T, want PositionEvent", events[1])
	}
	bal := events[0].(contract.BalanceEvent).Balance
	assertDec(t, bal.Total, "10")
	if !bal.Free.IsZero() || !bal.Available.IsZero() {
		t.Fatalf("ACCOUNT_UPDATE manufactured free/available from cross wallet: %#v", bal)
	}
}

func TestPerpAccountUpdateRejectsHedgePositionSide(t *testing.T) {
	_, err := accountEventsFromUpdate(&sdkperp.AccountUpdateEvent{
		EventType: "ACCOUNT_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000,
		UpdateData: struct {
			EventReasonType string `json:"m"`
			Balances        []struct {
				Asset              string `json:"a"`
				WalletBalance      string `json:"wb"`
				CrossWalletBalance string `json:"cw"`
				BalanceChange      string `json:"bc"`
			} `json:"B"`
			Positions []struct {
				Symbol              string `json:"s"`
				PositionAmount      string `json:"pa"`
				EntryPrice          string `json:"ep"`
				AccumulatedRealized string `json:"cr"`
				UnrealizedPnL       string `json:"up"`
				MarginType          string `json:"mt"`
				IsolatedWallet      string `json:"iw"`
				PositionSide        string `json:"ps"`
			} `json:"P"`
		}{Positions: []struct {
			Symbol              string `json:"s"`
			PositionAmount      string `json:"pa"`
			EntryPrice          string `json:"ep"`
			AccumulatedRealized string `json:"cr"`
			UnrealizedPnL       string `json:"up"`
			MarginType          string `json:"mt"`
			IsolatedWallet      string `json:"iw"`
			PositionSide        string `json:"ps"`
		}{{Symbol: "ASTERUSDT", PositionAmount: "1", EntryPrice: "2", UnrealizedPnL: "0", PositionSide: "LONG"}}},
	}, func(string) (model.InstrumentID, bool) { return testPerpID(), true }, AccountIDDefault)
	if err == nil {
		t.Fatalf("hedge position side accepted")
	}
}

func TestPerpAccountUpdateRejectsUnknownNonzeroPositionSymbol(t *testing.T) {
	_, err := accountEventsFromUpdate(&sdkperp.AccountUpdateEvent{
		EventType: "ACCOUNT_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000,
		UpdateData: struct {
			EventReasonType string `json:"m"`
			Balances        []struct {
				Asset              string `json:"a"`
				WalletBalance      string `json:"wb"`
				CrossWalletBalance string `json:"cw"`
				BalanceChange      string `json:"bc"`
			} `json:"B"`
			Positions []struct {
				Symbol              string `json:"s"`
				PositionAmount      string `json:"pa"`
				EntryPrice          string `json:"ep"`
				AccumulatedRealized string `json:"cr"`
				UnrealizedPnL       string `json:"up"`
				MarginType          string `json:"mt"`
				IsolatedWallet      string `json:"iw"`
				PositionSide        string `json:"ps"`
			} `json:"P"`
		}{Positions: []struct {
			Symbol              string `json:"s"`
			PositionAmount      string `json:"pa"`
			EntryPrice          string `json:"ep"`
			AccumulatedRealized string `json:"cr"`
			UnrealizedPnL       string `json:"up"`
			MarginType          string `json:"mt"`
			IsolatedWallet      string `json:"iw"`
			PositionSide        string `json:"ps"`
		}{{Symbol: "OTHERUSDT", PositionAmount: "1", EntryPrice: "2", UnrealizedPnL: "0", PositionSide: "BOTH"}}},
	}, func(string) (model.InstrumentID, bool) { return model.InstrumentID{}, false }, AccountIDDefault)
	if err == nil {
		t.Fatalf("unknown nonzero position symbol accepted")
	}
}

func TestPerpCapabilitiesDoNotAdvertiseAccountStateStreaming(t *testing.T) {
	acct := newAccountClient(nil, newInstrumentProvider(), nil, AccountIDDefault)
	acct.streaming = true
	if acct.Capabilities().Streaming.AccountState {
		t.Fatalf("perp account state streaming advertised for delta stream")
	}
}

func TestPerpStartIsIdempotent(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpAccountWS{}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, provider, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.connects != 2 || ws.orderSubs != 1 || ws.accountSubs != 1 {
		t.Fatalf("Start not idempotent connects=%d orderSubs=%d accountSubs=%d", ws.connects, ws.orderSubs, ws.accountSubs)
	}
}

func TestPerpStartRetriesAfterFirstConnectFailureWithoutDuplicateCallbacks(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpAccountWS{failConnects: 1}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, provider, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err == nil {
		t.Fatalf("first Start unexpectedly succeeded")
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("retry Start failed: %v", err)
	}
	if ws.connects != 2 || ws.orderSubs != 1 || ws.accountSubs != 1 {
		t.Fatalf("retry leaked callbacks or skipped connect connects=%d orderSubs=%d accountSubs=%d", ws.connects, ws.orderSubs, ws.accountSubs)
	}
}

func TestPerpPrivateConversionErrorsEmitNothing(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpAccountWS{}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, provider, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.orderHandler == nil || ws.accountHandler == nil {
		t.Fatalf("Start did not register private handlers")
	}
	ev := &sdkperp.OrderUpdateEvent{EventType: "ORDER_TRADE_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000}
	ev.Order.Symbol = "UNKNOWNUSDT"
	ev.Order.ClientOrderID = "c1"
	ev.Order.Side = "SELL"
	ev.Order.OrderType = "LIMIT"
	ev.Order.TimeInForce = "GTC"
	ev.Order.OriginalQty = "1"
	ev.Order.OriginalPrice = "10"
	ev.Order.ExecutionType = "TRADE"
	ev.Order.OrderStatus = "PARTIALLY_FILLED"
	ev.Order.OrderID = 42
	ev.Order.LastFilledQty = "0.5"
	ev.Order.AccumulatedFilledQty = "0.5"
	ev.Order.LastFilledPrice = "10"
	ev.Order.Commission = "0.01"
	ev.Order.CommissionAsset = "USDT"
	ev.Order.TradeTime = 1700000000000
	ev.Order.TradeID = 99
	ev.Order.PositionSide = "BOTH"
	ws.orderHandler(ev)
	ws.accountHandler(&sdkperp.AccountUpdateEvent{EventType: "ACCOUNT_UPDATE", EventTime: 1700000000000, TransactionTime: 1700000000000, UpdateData: struct {
		EventReasonType string `json:"m"`
		Balances        []struct {
			Asset              string `json:"a"`
			WalletBalance      string `json:"wb"`
			CrossWalletBalance string `json:"cw"`
			BalanceChange      string `json:"bc"`
		} `json:"B"`
		Positions []struct {
			Symbol              string `json:"s"`
			PositionAmount      string `json:"pa"`
			EntryPrice          string `json:"ep"`
			AccumulatedRealized string `json:"cr"`
			UnrealizedPnL       string `json:"up"`
			MarginType          string `json:"mt"`
			IsolatedWallet      string `json:"iw"`
			PositionSide        string `json:"ps"`
		} `json:"P"`
	}{Positions: []struct {
		Symbol              string `json:"s"`
		PositionAmount      string `json:"pa"`
		EntryPrice          string `json:"ep"`
		AccumulatedRealized string `json:"cr"`
		UnrealizedPnL       string `json:"up"`
		MarginType          string `json:"mt"`
		IsolatedWallet      string `json:"iw"`
		PositionSide        string `json:"ps"`
	}{{Symbol: "UNKNOWNUSDT", PositionAmount: "1", EntryPrice: "2", UnrealizedPnL: "0", PositionSide: "BOTH"}}}})
	select {
	case env := <-exec.Events():
		t.Fatalf("malformed private execution emitted event: %#v", env)
	default:
	}
	select {
	case env := <-acct.Events():
		t.Fatalf("malformed private account emitted event: %#v", env)
	default:
	}
}

func TestPerpNewRejectsInvalidConfiguredWebsockets(t *testing.T) {
	body := readAsterFixture(t, "perp", "exchange_info.json")
	client := perpClientSequence(t, map[string]string{"/fapi/v3/exchangeInfo": body})
	_, err := New(context.Background(), Config{Profile: mustProfile(t), Client: client, MarketWS: struct{}{}})
	if err == nil {
		t.Fatalf("New accepted invalid configured market websocket")
	}
	client = perpClientSequence(t, map[string]string{"/fapi/v3/exchangeInfo": body})
	_, err = New(context.Background(), Config{Profile: mustProfile(t), Client: client, AccountWS: struct{}{}})
	if err == nil {
		t.Fatalf("New accepted invalid configured account websocket")
	}
}

func readAsterFixture(t *testing.T, product, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk", "aster", product, "testdata", "v3", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type fakePerpAccountWS struct {
	connects       int
	failConnects   int
	orderSubs      int
	accountSubs    int
	orderHandler   func(*sdkperp.OrderUpdateEvent)
	accountHandler func(*sdkperp.AccountUpdateEvent)
}

func (f *fakePerpAccountWS) SubscribeAccountUpdate(handler func(*sdkperp.AccountUpdateEvent)) {
	f.accountSubs++
	f.accountHandler = handler
}
func (f *fakePerpAccountWS) SubscribeOrderUpdate(handler func(*sdkperp.OrderUpdateEvent)) {
	f.orderSubs++
	f.orderHandler = handler
}
func (f *fakePerpAccountWS) Connect() error {
	f.connects++
	if f.failConnects > 0 {
		f.failConnects--
		return context.Canceled
	}
	return nil
}
func (f *fakePerpAccountWS) Close() {}

type fakePerpMarketWS struct {
	connected    bool
	connects     int
	bookSymbol   string
	quoteSymbol  string
	tradeSymbol  string
	markSymbol   string
	markInterval string
	tradeSubs    int
	markSubs     int
	tradeHandler func(*sdkperp.WsAggTradeEvent) error
	markHandler  func(*sdkperp.WsMarkPriceEvent) error
}

func (f *fakePerpMarketWS) Connect() error {
	f.connected = true
	f.connects++
	return nil
}

func (f *fakePerpMarketWS) Close() {}

func (f *fakePerpMarketWS) IsConnected() bool { return f.connected }

func (f *fakePerpMarketWS) SubscribeLimitOrderBook(symbol string, levels int, interval string, handler func(*sdkperp.WsDepthEvent) error) error {
	f.bookSymbol = symbol
	return nil
}

func (f *fakePerpMarketWS) SubscribeBookTicker(symbol string, handler func(*sdkperp.WsBookTickerEvent) error) error {
	f.quoteSymbol = symbol
	return nil
}

func (f *fakePerpMarketWS) SubscribeAggTrade(symbol string, handler func(*sdkperp.WsAggTradeEvent) error) error {
	f.tradeSubs++
	f.tradeSymbol = symbol
	f.tradeHandler = handler
	return nil
}

func (f *fakePerpMarketWS) SubscribeMarkPrice(symbol string, interval string, handler func(*sdkperp.WsMarkPriceEvent) error) error {
	f.markSubs++
	f.markSymbol = symbol
	f.markInterval = interval
	f.markHandler = handler
	return nil
}
