package factoryclient

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestGateOrderBookSubscriptionsAndPayloadsMatchOfficialFullSnapshotChannels(t *testing.T) {
	if got, want := gateSpotOrderBookSubscription("BTC_USDT"), []string{"BTC_USDT", "5", "100ms"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spot subscription = %v, want %v", got, want)
	}
	if got, want := gatePerpOrderBookSubscription("BTC_USDT"), []string{"BTC_USDT", "20", "0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("perp subscription = %v, want %v", got, want)
	}

	spotRaw := json.RawMessage(`{
		"time": 1606295412,
		"channel": "spot.order_book",
		"event": "update",
		"result": {
			"t": 1606295412123,
			"lastUpdateId": 48791820,
			"s": "BTC_USDT",
			"l": "5",
			"bids": [["19079.55", "0.0195"]],
			"asks": [["19080.24", "0.1638"]]
		}
	}`)
	spotEvent, err := gateSpotOrderBookEvent(spotRaw, "BTC_USDT")
	if err != nil {
		t.Fatalf("spot order book event: %v", err)
	}
	if spotEvent.Kind != exchange.EventSnapshot || spotEvent.Sequence != "48791820" || len(spotEvent.Bids) != 1 || len(spotEvent.Asks) != 1 {
		t.Fatalf("spot order book event = %+v", spotEvent)
	}
	if !spotEvent.Bids[0].Price.Equal(decimal.RequireFromString("19079.55")) || !spotEvent.Asks[0].Quantity.Equal(decimal.RequireFromString("0.1638")) {
		t.Fatalf("spot levels = bids=%+v asks=%+v", spotEvent.Bids, spotEvent.Asks)
	}

	perpRaw := json.RawMessage(`{
		"channel": "futures.order_book",
		"event": "all",
		"time": 1541500161,
		"time_ms": 1541500161123,
		"result": {
			"t": 1541500161123,
			"contract": "BTC_USDT",
			"id": 93973511,
			"asks": [{"p": "97.1", "s": 2245}],
			"bids": [{"p": "96.9", "s": 3100}],
			"l": "20"
		}
	}`)
	perpEvent, err := gatePerpOrderBookEvent(perpRaw, "BTC_USDT", decimal.RequireFromString("0.001"))
	if err != nil {
		t.Fatalf("perp order book event: %v", err)
	}
	if perpEvent.Kind != exchange.EventSnapshot || perpEvent.Instrument != "BTC_USDT" || perpEvent.Sequence != "93973511" || len(perpEvent.Bids) != 1 || len(perpEvent.Asks) != 1 {
		t.Fatalf("perp order book event = %+v", perpEvent)
	}
	if !perpEvent.Bids[0].Quantity.Equal(decimal.RequireFromString("3.1")) || !perpEvent.Asks[0].Price.Equal(decimal.RequireFromString("97.1")) {
		t.Fatalf("perp levels = bids=%+v asks=%+v", perpEvent.Bids, perpEvent.Asks)
	}
}

func TestGateCandlestickPayloadsMatchOfficialSpotObjectAndFuturesArrayShapes(t *testing.T) {
	spotRaw := json.RawMessage(`{
		"channel": "spot.candlesticks",
		"event": "update",
		"result": {
			"t": "1606292580",
			"v": "2362.32035",
			"c": "19128.1",
			"h": "19129.2",
			"l": "19120.3",
			"o": "19121.4",
			"n": "1m_BTC_USDT",
			"a": "3.8283",
			"w": true
		}
	}`)
	spotEvent, err := gateSpotCandleEvent(spotRaw, "BTC_USDT", "1m")
	if err != nil {
		t.Fatalf("spot candle event: %v", err)
	}
	if spotEvent.Instrument != "BTC_USDT" || spotEvent.Interval != "1m" || !spotEvent.Candle.Complete {
		t.Fatalf("spot candle event = %+v", spotEvent)
	}
	if !spotEvent.Candle.Open.Equal(decimal.RequireFromString("19121.4")) || !spotEvent.Candle.Volume.Equal(decimal.RequireFromString("2362.32035")) {
		t.Fatalf("spot candle = %+v", spotEvent.Candle)
	}

	perpRaw := json.RawMessage(`{
		"channel": "futures.candlesticks",
		"event": "update",
		"result": [{
			"t": 1545129300,
			"v": 27525555,
			"c": "95.4",
			"h": "96.9",
			"l": "89.5",
			"o": "94.3",
			"n": "1m_BTC_USDT",
			"a": "314732.87412",
			"w": false
		}]
	}`)
	perpEvents, err := gatePerpCandleEvents(perpRaw, "BTC_USDT", "1m")
	if err != nil {
		t.Fatalf("perp candle events: %v", err)
	}
	if len(perpEvents) != 1 || perpEvents[0].Candle.Complete || !perpEvents[0].Candle.High.Equal(decimal.RequireFromString("96.9")) {
		t.Fatalf("perp candle events = %+v", perpEvents)
	}
}

func TestGatePerpReferencePayloadMatchesOfficialTickerArrayShape(t *testing.T) {
	raw := json.RawMessage(`{
		"channel": "futures.tickers",
		"event": "update",
		"result": [{
			"contract": "BTC_USDT",
			"funding_rate": "-0.000114",
			"mark_price": "118.35",
			"t": 1541659086123
		}]
	}`)
	events, err := gatePerpReferenceEvents(raw, "BTC_USDT")
	if err != nil {
		t.Fatalf("perp reference events: %v", err)
	}
	if len(events) != 1 || !events[0].MarkValid || !events[0].FundingValid {
		t.Fatalf("perp reference events = %+v", events)
	}
	if !events[0].MarkPrice.Price.Equal(decimal.RequireFromString("118.35")) ||
		!events[0].FundingRate.Rate.Equal(decimal.RequireFromString("-0.000114")) {
		t.Fatalf("perp reference event = %+v", events[0])
	}
}

func TestGatePerpPublicTradePayloadMatchesTestnetArrayShape(t *testing.T) {
	raw := json.RawMessage(`{
		"channel": "futures.trades",
		"event": "update",
		"result": [{
			"id": 2,
			"create_time": 1720000000.125,
			"contract": "BTC_USDT",
			"size": "-1",
			"price": "100"
		}]
	}`)
	events, err := gatePerpPublicTradeEvents(raw, "BTC_USDT", decimal.RequireFromString("0.001"))
	if err != nil {
		t.Fatalf("perp public trade events: %v", err)
	}
	if len(events) != 1 || events[0].Side != exchange.SideSell {
		t.Fatalf("perp public trade events = %+v", events)
	}
	if !events[0].Quantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("perp public trade quantity = %s, want neutral base quantity 0.001", events[0].Quantity)
	}
}

func TestGatePerpOrderPayloadAcceptsDocumentedLifecycleMarkers(t *testing.T) {
	raw := []byte(`{
		"channel": "futures.orders",
		"event": "update",
		"result": [{
			"id": 21,
			"contract": "BTC_USDT",
			"size": 1,
			"left": 1,
			"price": "100",
			"fill_price": "0",
			"status": "open",
			"finish_as": "_new"
		}]
	}`)
	events, err := gateFuturesOrderEvents(raw, decimal.RequireFromString("0.001"))
	if err != nil {
		t.Fatalf("perp order events: %v", err)
	}
	if len(events) != 1 || events[0].Order.Status != "_new" {
		t.Fatalf("perp order events = %+v", events)
	}
}

func TestGatePerpPrivateSubscriptionsIncludeVenueUserIDPlaceholder(t *testing.T) {
	if got, want := gatePerpPrivateInstrumentPayload("20011", "BTC_USDT"), []string{"20011", "BTC_USDT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("instrument payload = %v, want %v", got, want)
	}
	if got, want := gatePerpPrivateAccountPayload("20011"), []string{"20011"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("account payload = %v, want %v", got, want)
	}
}

func TestGateWebSocketParsesPrivateEnvelopeResultArrays(t *testing.T) {
	spotOrderRaw := []byte(`{"time":1720000000,"channel":"spot.orders","event":"update","result":[{"id":"11","text":"101","currency_pair":"BTC_USDT","type":"limit","side":"buy","amount":"1","price":"99","left":"0","filled_amount":"1","status":"closed","finish_as":"filled"}]}`)
	spotOrderEvents, err := gateSpotOrderEvents(spotOrderRaw)
	if err != nil {
		t.Fatalf("spot order events: %v", err)
	}
	if len(spotOrderEvents) != 1 || spotOrderEvents[0].Order.OrderID != "11" {
		t.Fatalf("spot order events = %+v", spotOrderEvents)
	}

	spotFillRaw := []byte(`{"time":1720000000,"channel":"spot.usertrades","event":"update","result":[{"id":"1","currency_pair":"BTC_USDT","order_id":"11","side":"buy","role":"maker","amount":"1","price":"99","fee":"0.01","fee_currency":"USDT","create_time_ms":"1720000000000","text":"101"}]}`)
	spotFillEvents, err := gateSpotFillEvents(spotFillRaw)
	if err != nil {
		t.Fatalf("spot fill events: %v", err)
	}
	if len(spotFillEvents) != 1 || spotFillEvents[0].Fill.FillID != "1" {
		t.Fatalf("spot fill events = %+v", spotFillEvents)
	}

	perpOrderRaw := []byte(`{"time":1720000000,"channel":"futures.orders","event":"update","result":[{"id":21,"contract":"BTC_USDT","size":1,"price":"99","left":0,"status":"finished","finish_as":"filled"}]}`)
	perpOrderEvents, err := gateFuturesOrderEvents(perpOrderRaw, decimal.RequireFromString("0.001"))
	if err != nil {
		t.Fatalf("perp order events: %v", err)
	}
	if len(perpOrderEvents) != 1 || perpOrderEvents[0].Order.OrderID != "21" {
		t.Fatalf("perp order events = %+v", perpOrderEvents)
	}

	perpPositionRaw := []byte(`{"time":1720000000,"channel":"futures.positions","event":"update","result":[{"contract":"BTC_USDT","size":-2,"leverage":"5","margin":"10","entry_price":"99","mark_price":"100","unrealised_pnl":"1"}]}`)
	positionEvents, err := gateFuturesPositionEvents(perpPositionRaw, "BTC_USDT", decimal.RequireFromString("0.001"))
	if err != nil {
		t.Fatalf("position events: %v", err)
	}
	if len(positionEvents) != 1 || len(positionEvents[0].Positions) != 1 || positionEvents[0].Positions[0].Side != exchange.SideSell {
		t.Fatalf("position events = %+v", positionEvents)
	}
	if !positionEvents[0].Positions[0].Quantity.Equal(decimal.RequireFromString("0.002")) {
		t.Fatalf("position quantity = %s, want neutral base quantity 0.002", positionEvents[0].Positions[0].Quantity)
	}
}

func TestGateWebSocketCommandsUseRESTBridge(t *testing.T) {
	router := &gateOpenAPIRouter{}
	client := NewGateSpot("key", "secret", Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot",
		Environment:       "testnet",
		HTTPClient:        &http.Client{Transport: router},
	})
	ack, err := client.WebSocket().PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC_USDT",
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(99),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatalf("WS PlaceOrder bridge: %v", err)
	}
	if ack.Venue != exchange.VenueGate || ack.OrderID != "11" {
		t.Fatalf("WS PlaceOrder ack = %+v", ack)
	}
}

func TestGateWebSocketExercisesEveryExposedMethod(t *testing.T) {
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot",
		Environment:       "testnet",
		HTTPClient:        &http.Client{Transport: &gateOpenAPIRouter{}},
	}
	spot := NewGateSpot("key", "secret", settings).WebSocket()
	perp := NewGateUSDTPerp("key", "secret", settings).WebSocket()
	for _, method := range []string{
		"WatchOrderBook", "WatchBBO", "WatchPublicTrades", "WatchCandles",
		"WatchOrders", "WatchFills", "WatchBalances", "PlaceOrder", "CancelOrder", "Close",
	} {
		if _, ok := reflect.TypeOf(spot).MethodByName(method); !ok {
			t.Fatalf("Gate spot websocket missing %s", method)
		}
		if _, ok := reflect.TypeOf(perp).MethodByName(method); !ok {
			t.Fatalf("Gate perp websocket missing inherited %s", method)
		}
	}
	for _, method := range []string{"WatchPositions", "WatchMarkPrice", "WatchFundingRate"} {
		if _, ok := reflect.TypeOf(perp).MethodByName(method); !ok {
			t.Fatalf("Gate perp websocket missing %s", method)
		}
	}
}
