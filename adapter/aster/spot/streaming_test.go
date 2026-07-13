package spot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
)

func TestSpotMarketSubscriptionsUseInjectedSDKWebsocket(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotMarketWS{}
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

func TestSpotMarketReconnectCallsSDKConnectAgainAndKeepsChannel(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotMarketWS{}
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

func TestSpotStreamEventsTranslateAndFailClosed(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotMarketWS{}
	market := newMarketDataClient(nil, ws, testProvider(inst), nil)
	if err := market.SubscribeTrades(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	if err := ws.tradeHandler(&sdkspot.AggTradeEvent{WsEventHeader: sdkspot.WsEventHeader{Symbol: inst.VenueSymbol}, AggTradeID: 99, Price: "10.5", Quantity: "2", TradeTime: 1700000000000, IsBuyerMaker: true}); err != nil {
		t.Fatalf("trade handler returned error: %v", err)
	}
	env := <-market.Events()
	trade := env.Payload.(contract.TradeEvent).Trade
	if trade.InstrumentID != inst.ID || trade.TradeID != "99" || trade.AggressorSide != enums.SideSell || !trade.Timestamp.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("bad trade event: %#v", trade)
	}
	if err := ws.tradeHandler(&sdkspot.AggTradeEvent{WsEventHeader: sdkspot.WsEventHeader{Symbol: inst.VenueSymbol}, AggTradeID: 100, Price: "bad", Quantity: "2"}); err == nil {
		t.Fatalf("malformed trade event accepted")
	}
	if err := ws.tradeHandler(&sdkspot.AggTradeEvent{WsEventHeader: sdkspot.WsEventHeader{Symbol: "OTHERUSDT"}, AggTradeID: 101, Price: "10", Quantity: "2", TradeTime: 1700000000000}); err == nil {
		t.Fatalf("cross-symbol trade event accepted")
	}
	if err := ws.tradeHandler(&sdkspot.AggTradeEvent{WsEventHeader: sdkspot.WsEventHeader{Symbol: inst.VenueSymbol}, AggTradeID: 102, Price: "10", Quantity: "2"}); err == nil {
		t.Fatalf("missing timestamp trade event accepted")
	}
	select {
	case env := <-market.Events():
		t.Fatalf("malformed trade emitted event: %#v", env)
	default:
	}
}

func TestSpotQuoteReplayUsesStableSourceEventID(t *testing.T) {
	inst := mustSpotInstrument(t)
	clk := clock.NewSimulatedClock(time.Unix(100, 0))
	ws := &fakeSpotMarketWS{}
	market := newMarketDataClient(nil, ws, testProvider(inst), clk)
	if err := market.SubscribeQuotes(context.Background(), inst.ID); err != nil {
		t.Fatal(err)
	}
	ev := &sdkspot.BookTickerEvent{UpdateID: 7, Symbol: inst.VenueSymbol, BestBidPrice: "10", BestBidQty: "1", BestAskPrice: "11", BestAskQty: "2"}
	if err := ws.quoteHandler(ev); err != nil {
		t.Fatal(err)
	}
	first := <-market.Events()
	clk.Advance(time.Hour)
	if err := ws.quoteHandler(ev); err != nil {
		t.Fatal(err)
	}
	replay := <-market.Events()
	if first.EventID == "" || first.EventID != replay.EventID {
		t.Fatalf("replay event ids not stable first=%q replay=%q", first.EventID, replay.EventID)
	}
	ev.UpdateID = 8
	if err := ws.quoteHandler(ev); err != nil {
		t.Fatal(err)
	}
	next := <-market.Events()
	if next.EventID == first.EventID {
		t.Fatalf("different update id reused event id %q", next.EventID)
	}
}

func TestSpotPrivateExecutionReportRejectsMalformedIdentitiesAndEnums(t *testing.T) {
	asset := "USDT"
	valid := &sdkspot.ExecutionReportEvent{
		EventType: "executionReport", EventTime: 1700000000000, Symbol: "ASTERUSDT", ClientOrderID: "c1", Side: "BUY", OrderType: "LIMIT", TimeInForce: "GTC",
		Quantity: "1", Price: "10", ExecutionType: "TRADE", OrderStatus: "PARTIALLY_FILLED", OrderID: 42, LastExecutedQuantity: "0.5",
		CumulativeFilledQuantity: "0.5", LastExecutedPrice: "10", CommissionAmount: "0.01", CommissionAsset: &asset, TransactionTime: 1700000000000,
		TradeID: 99, CumulativeQuoteAssetTransactedQuantity: "5",
	}
	resolved := func(string) (model.InstrumentID, bool) { return testSpotID(), true }
	if events, err := execEventsFromExecutionReport(valid, resolved, AccountIDDefault); err != nil || len(events) != 2 {
		t.Fatalf("valid private event rejected events=%d err=%v", len(events), err)
	}
	if _, err := execEventsFromExecutionReport(valid, func(string) (model.InstrumentID, bool) { return model.InstrumentID{}, false }, AccountIDDefault); err == nil {
		t.Fatalf("unknown private event symbol accepted")
	}
	cases := map[string]func(*sdkspot.ExecutionReportEvent){
		"missing client": func(e *sdkspot.ExecutionReportEvent) { e.ClientOrderID = "" },
		"missing order":  func(e *sdkspot.ExecutionReportEvent) { e.OrderID = 0 },
		"missing trade":  func(e *sdkspot.ExecutionReportEvent) { e.TradeID = 0 },
		"missing time":   func(e *sdkspot.ExecutionReportEvent) { e.EventTime, e.TransactionTime = 0, 0 },
		"bad status":     func(e *sdkspot.ExecutionReportEvent) { e.OrderStatus = "BOGUS" },
		"bad type":       func(e *sdkspot.ExecutionReportEvent) { e.OrderType = "BOGUS" },
		"bad tif":        func(e *sdkspot.ExecutionReportEvent) { e.TimeInForce = "BOGUS" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			ev := *valid
			mutate(&ev)
			if _, err := execEventsFromExecutionReport(&ev, resolved, AccountIDDefault); err == nil {
				t.Fatalf("malformed execution report accepted")
			}
		})
	}
}

func TestSpotPartialOrderReplayIDsIncludeSourceProgress(t *testing.T) {
	asset := "USDT"
	base := &sdkspot.ExecutionReportEvent{
		EventType: "executionReport", EventTime: 1700000000000, Symbol: "ASTERUSDT", ClientOrderID: "c1", Side: "BUY", OrderType: "LIMIT", TimeInForce: "GTC",
		Quantity: "1", Price: "10", ExecutionType: "TRADE", OrderStatus: "PARTIALLY_FILLED", OrderID: 42, LastExecutedQuantity: "0.1",
		CumulativeFilledQuantity: "0.1", LastExecutedPrice: "10", CommissionAmount: "0.01", CommissionAsset: &asset, TransactionTime: 1700000000000,
		TradeID: 99, CumulativeQuoteAssetTransactedQuantity: "1",
	}
	resolved := func(string) (model.InstrumentID, bool) { return testSpotID(), true }
	first, err := execEnvelopesFromExecutionReport(base, resolved, AccountIDDefault)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := execEnvelopesFromExecutionReport(base, resolved, AccountIDDefault)
	if err != nil {
		t.Fatal(err)
	}
	nextEv := *base
	nextEv.CumulativeFilledQuantity = "0.2"
	nextEv.LastExecutedQuantity = "0.1"
	nextEv.TradeID = 100
	next, err := execEnvelopesFromExecutionReport(&nextEv, resolved, AccountIDDefault)
	if err != nil {
		t.Fatal(err)
	}
	if first[0].EventID == "" || first[0].EventID != replay[0].EventID {
		t.Fatalf("order replay IDs unstable first=%q replay=%q", first[0].EventID, replay[0].EventID)
	}
	if first[0].EventID == next[0].EventID {
		t.Fatalf("distinct partial progress reused order event id %q", first[0].EventID)
	}
}

func TestSpotAccountPositionEmitsOnlyBalanceDeltas(t *testing.T) {
	events, err := accountEventsFromAccountPosition(&sdkspot.AccountPositionEvent{
		EventType: "outboundAccountPosition", EventTime: 1700000000000, LastAccountUpdate: 1700000000000,
		Balances: []struct {
			Asset  string `json:"a"`
			Free   string `json:"f"`
			Locked string `json:"l"`
		}{{Asset: "USDT", Free: "1", Locked: "2"}},
	}, AccountIDDefault)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events len=%d, want one balance delta", len(events))
	}
	if _, ok := events[0].(contract.BalanceEvent); !ok {
		t.Fatalf("event=%T, want BalanceEvent", events[0])
	}
}

func TestSpotCapabilitiesDoNotAdvertiseAccountStateStreaming(t *testing.T) {
	acct := newAccountClient(nil, nil, AccountIDDefault)
	acct.streaming = true
	if acct.Capabilities().Streaming.AccountState {
		t.Fatalf("spot account state streaming advertised for delta stream")
	}
}

func TestSpotStartIsIdempotent(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotAccountWS{}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.connects != 2 || ws.execSubs != 1 || ws.accountSubs != 1 {
		t.Fatalf("Start not idempotent connects=%d execSubs=%d accountSubs=%d", ws.connects, ws.execSubs, ws.accountSubs)
	}
}

func TestSpotStartRetriesAfterFirstConnectFailureWithoutDuplicateCallbacks(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotAccountWS{failConnects: 1}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err == nil {
		t.Fatalf("first Start unexpectedly succeeded")
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("retry Start failed: %v", err)
	}
	if ws.connects != 2 || ws.execSubs != 1 || ws.accountSubs != 1 {
		t.Fatalf("retry leaked callbacks or skipped connect connects=%d execSubs=%d accountSubs=%d", ws.connects, ws.execSubs, ws.accountSubs)
	}
}

func TestSpotPrivateConversionErrorsEmitNothing(t *testing.T) {
	inst := mustSpotInstrument(t)
	ws := &fakeSpotAccountWS{}
	provider := testProvider(inst)
	exec := newExecutionClient(nil, provider, nil, AccountIDDefault)
	acct := newAccountClient(nil, nil, AccountIDDefault)
	adapter := &Adapter{Execution: exec, Account: acct, provider: provider, exec: exec, acct: acct, wsAcct: ws}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.execHandler == nil || ws.accountHandler == nil {
		t.Fatalf("Start did not register private handlers")
	}
	asset := "USDT"
	ws.execHandler(&sdkspot.ExecutionReportEvent{
		EventType: "executionReport", EventTime: 1700000000000, Symbol: "UNKNOWNUSDT", ClientOrderID: "c1", Side: "BUY", OrderType: "LIMIT", TimeInForce: "GTC",
		Quantity: "1", Price: "10", ExecutionType: "TRADE", OrderStatus: "PARTIALLY_FILLED", OrderID: 42, LastExecutedQuantity: "0.5",
		CumulativeFilledQuantity: "0.5", LastExecutedPrice: "10", CommissionAmount: "0.01", CommissionAsset: &asset, TransactionTime: 1700000000000,
		TradeID: 99, CumulativeQuoteAssetTransactedQuantity: "5",
	})
	ws.accountHandler(&sdkspot.AccountPositionEvent{EventType: "outboundAccountPosition", EventTime: 1700000000000, LastAccountUpdate: 1700000000000, Balances: []struct {
		Asset  string `json:"a"`
		Free   string `json:"f"`
		Locked string `json:"l"`
	}{{Asset: "", Free: "1", Locked: "0"}}})
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

func TestSpotNewRejectsInvalidConfiguredWebsockets(t *testing.T) {
	body := readAsterFixture(t, "spot", "exchange_info.json")
	client := spotClientSequence(t, map[string]string{"/api/v3/exchangeInfo": body})
	_, err := New(context.Background(), Config{Profile: mustProfile(t), Client: client, MarketWS: struct{}{}})
	if err == nil {
		t.Fatalf("New accepted invalid configured market websocket")
	}
	client = spotClientSequence(t, map[string]string{"/api/v3/exchangeInfo": body})
	_, err = New(context.Background(), Config{Profile: mustProfile(t), Client: client, AccountWS: struct{}{}})
	if err == nil {
		t.Fatalf("New accepted invalid configured account websocket")
	}
}

type fakeSpotAccountWS struct {
	connects       int
	failConnects   int
	execSubs       int
	accountSubs    int
	execHandler    func(*sdkspot.ExecutionReportEvent)
	accountHandler func(*sdkspot.AccountPositionEvent)
}

func (f *fakeSpotAccountWS) SubscribeExecutionReport(handler func(*sdkspot.ExecutionReportEvent)) {
	f.execSubs++
	f.execHandler = handler
}
func (f *fakeSpotAccountWS) SubscribeAccountPosition(handler func(*sdkspot.AccountPositionEvent)) {
	f.accountSubs++
	f.accountHandler = handler
}
func (f *fakeSpotAccountWS) Connect() error {
	f.connects++
	if f.failConnects > 0 {
		f.failConnects--
		return context.Canceled
	}
	return nil
}
func (f *fakeSpotAccountWS) Close() {}

func readAsterFixture(t *testing.T, product, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk", "aster", product, "testdata", "v3", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type fakeSpotMarketWS struct {
	connected    bool
	connects     int
	bookSymbol   string
	quoteSymbol  string
	tradeSymbol  string
	tradeSubs    int
	tradeHandler func(*sdkspot.AggTradeEvent) error
	quoteHandler func(*sdkspot.BookTickerEvent) error
}

func (f *fakeSpotMarketWS) Connect() error {
	f.connected = true
	f.connects++
	return nil
}

func (f *fakeSpotMarketWS) Close() {}

func (f *fakeSpotMarketWS) IsConnected() bool { return f.connected }

func (f *fakeSpotMarketWS) SubscribeLimitOrderBook(symbol string, depth int, speed string, handler func(*sdkspot.DepthEvent) error) error {
	f.bookSymbol = symbol
	return nil
}

func (f *fakeSpotMarketWS) SubscribeBookTicker(symbol string, handler func(*sdkspot.BookTickerEvent) error) error {
	f.quoteSymbol = symbol
	f.quoteHandler = handler
	return nil
}

func (f *fakeSpotMarketWS) SubscribeAggTrade(symbol string, handler func(*sdkspot.AggTradeEvent) error) error {
	f.tradeSubs++
	f.tradeSymbol = symbol
	f.tradeHandler = handler
	return nil
}
