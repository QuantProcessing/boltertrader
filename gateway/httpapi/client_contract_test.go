package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	client "github.com/QuantProcessing/boltertrader/clients/gateway"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"gopkg.in/yaml.v3"
)

func TestIndependentClientConsumesRealOrderHandler(t *testing.T) {
	fixture := newHTTPFixture(t)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	consumer, err := client.New(client.Config{URL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	request := client.PlaceOrderRequest{AccountID: "acct-a", RouteID: "spot", InstrumentID: "binance:live:spot:BTC-USDT", IdempotencyKey: "idem-1", Side: "buy", Type: "limit", Quantity: "0.001", LimitPrice: "100.000000000000001", LimitPolicy: "resting", RuntimeID: "018f47a8-2578-7d3c-9d7d-5e4f4a445e11"}
	result, err := consumer.PlaceOrder(t.Context(), request)
	if err != nil || result.AccountID != request.AccountID || result.InstrumentID != request.InstrumentID || fixture.orders.place.LimitPrice.String() != request.LimitPrice {
		t.Fatalf("place=%+v %v", result, err)
	}
	fixture.orders.getResult = order.Result{CommandID: domain.CommandID(result.CommandID), GatewayOrderID: domain.GatewayOrderID(result.GatewayOrderID), ClientOrderID: result.ClientOrderID, State: domain.OrderState(result.State), ResourceVersion: domain.ResourceVersion(result.ResourceVersion), AccountID: domain.AccountID(request.AccountID), InstrumentID: domain.InstrumentID(request.InstrumentID)}
	scope := client.OrderScope{AccountID: request.AccountID, InstrumentID: request.InstrumentID, GatewayOrderID: result.GatewayOrderID}
	if _, err := consumer.Order(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.OrderByKey(t.Context(), request.IdempotencyKey, request.AccountID, request.InstrumentID); err != nil {
		t.Fatal(err)
	}
	wrong := scope
	wrong.AccountID = "other"
	if _, err := consumer.CancelOrder(t.Context(), wrong); err == nil || fixture.orders.cancelCalls != 0 {
		t.Fatal("wrong account reached cancellation")
	}
	canceled, err := consumer.CancelOrder(t.Context(), scope)
	if err != nil || canceled.State != "canceled" || fixture.orders.cancelCalls != 1 {
		t.Fatalf("cancel=%+v %v", canceled, err)
	}
	balances, err := consumer.Balances(t.Context(), request.AccountID)
	if err != nil || balances == nil {
		t.Fatalf("balances=%+v %v", balances, err)
	}
}
func TestIndependentClientConsumesRealMarketHandlerAndHub(t *testing.T) {
	source := newAPIMarketSource()
	owner := &apiMarketOwner{opened: make(chan *apiNativeStream, 4), data: json.RawMessage(`{"bid":{"price":"99","quantity":"1"},"ask":{"price":"101","quantity":"1"}}`)}
	hub, err := markethub.New(markethub.Options{Owner: owner})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	server := httptest.NewServer(newMarketTestHandler(t, source, hub))
	defer server.Close()
	consumer, err := client.New(client.Config{URL: server.URL, Token: "test-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := consumer.MarketCatalog(t.Context())
	if err != nil || len(catalog.Items) != 1 || !catalog.Complete {
		t.Fatalf("catalog=%+v %v", catalog, err)
	}
	topic := client.MarketTopic{Topic: client.BestBidOffer, InstrumentID: marketTestID}
	snapshot, err := consumer.MarketSnapshot(t.Context(), topic)
	if err != nil || snapshot.Snapshot.Type != wire.MessageSnapshot {
		t.Fatalf("snapshot=%+v %v", snapshot, err)
	}
	candles, err := consumer.MarketCandles(t.Context(), marketTestID, "1m", 5)
	if err != nil || candles.Candles == nil {
		t.Fatalf("candles=%+v %v", candles, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := consumer.SubscribeMarkets(ctx, []client.MarketTopic{topic}, client.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case event, ok := <-stream.Events():
		if !ok || event.Topic.InstrumentID != marketTestID || event.Message.Type != wire.MessageSnapshot {
			t.Fatalf("event=%+v err=%v", event, stream.Err())
		}
	case <-ctx.Done():
		t.Fatal("real market stream timed out")
	}
	stream.Close()
}

func TestIndependentClientAcceptsRealHubCoalescedBBO(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	now := func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	source := newAPIMarketSource()
	owner := &apiMarketOwner{opened: make(chan *apiNativeStream, 1), data: json.RawMessage(`{"bid":{"price":"99","quantity":"1"},"ask":{"price":"101","quantity":"1"}}`)}
	hub, err := markethub.New(markethub.Options{Owner: owner, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	server := httptest.NewServer(newMarketTestHandler(t, source, hub))
	defer server.Close()
	consumer, err := client.New(client.Config{URL: server.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	stream, err := consumer.SubscribeMarkets(ctx, []client.MarketTopic{{Topic: client.BestBidOffer, InstrumentID: marketTestID}}, client.StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case first := <-stream.Events():
		if first.Message.Sequence != 1 || first.Message.Type != wire.MessageSnapshot {
			t.Fatal(first)
		}
	case <-ctx.Done():
		t.Fatal("snapshot timed out")
	}
	var upstream *apiNativeStream
	select {
	case upstream = <-owner.opened:
	case <-ctx.Done():
		t.Fatal("upstream not opened")
	}
	for range 500 {
		upstream.events <- markethub.Message{Kind: markethub.MessageDelta, ObservedAt: now(), Data: owner.data}
	}
	key, _ := source.SubscriptionKey(ctx, marketTestID, exchange.DatasetBBO, "", 0)
	for {
		if latest, ok := hub.Latest(key); ok && latest.Sequence == 501 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("Hub did not consume burst")
		case <-time.After(time.Millisecond):
		}
	}
	clock.Add(int64(50 * time.Millisecond))
	upstream.events <- markethub.Message{Kind: markethub.MessageDelta, ObservedAt: now(), Data: owner.data}
	select {
	case event := <-stream.Events():
		if event.Message.Type != wire.MessageDelta || event.Message.Sequence != 502 || owner.opens.Load() != 1 {
			t.Fatalf("coalescing caused false disconnect: %+v opens=%d", event, owner.opens.Load())
		}
	case <-ctx.Done():
		t.Fatal("coalesced quote timed out")
	}
}
func TestOrderResponseSchemaMatchesNeutralDTOAndServer(t *testing.T) {
	raw, err := os.ReadFile("../../api/exchange-gateway/v1/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]any `yaml:"properties"`
				Required   []string       `yaml:"required"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	schema := doc.Components.Schemas["OrderResponse"]
	for _, typ := range []reflect.Type{reflect.TypeFor[wire.Order](), reflect.TypeFor[order.Result]()} {
		fields := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			key := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
			if key == "-" {
				continue
			}
			fields[key] = true
			if _, ok := schema.Properties[key]; !ok {
				t.Fatalf("%s.%s missing from schema", typ.Name(), key)
			}
		}
		for _, key := range schema.Required {
			if !fields[key] {
				t.Fatalf("%s missing required %s", typ.Name(), key)
			}
		}
	}
}

var _ http.Handler = (*Handler)(nil)
