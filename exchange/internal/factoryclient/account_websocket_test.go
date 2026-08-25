package factoryclient

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type fakeRouteAccountBackend struct {
	*fakePrivateWSBackend
	mu         sync.Mutex
	starts     int
	stops      int
	indexCount int
	callbacks  accountEventCallbacks
}

func newFakeRouteAccountBackend() *fakeRouteAccountBackend {
	return &fakeRouteAccountBackend{fakePrivateWSBackend: newFakePrivateWSBackend()}
}

func (backend *fakeRouteAccountBackend) StartAccountEvents(_ context.Context, index accountInstrumentIndex, callbacks accountEventCallbacks) (func() error, error) {
	backend.mu.Lock()
	backend.starts++
	backend.indexCount = len(index.instruments())
	backend.callbacks = callbacks
	backend.mu.Unlock()
	var once sync.Once
	return func() error {
		once.Do(func() {
			backend.mu.Lock()
			backend.stops++
			backend.mu.Unlock()
		})
		return nil
	}, nil
}

func TestAccountEventRouteSubscriptionCountDoesNotGrowWithCatalog(t *testing.T) {
	backend := newFakeRouteAccountBackend()
	meta := clientMeta{venue: exchange.VenueLighter, product: exchange.ProductSpot}
	socket := newSpotWebSocket(newPublicWebSocket(meta, &fakePublicWSBackend{}), backend)
	catalog := make([]exchange.AccountInstrument, 0, 1000)
	for index := 0; index < 1000; index++ {
		native := fmt.Sprintf("ASSET%04d-USDC", index)
		catalog = append(catalog, exchange.AccountInstrument{
			InstrumentID: "lighter:live:spot:" + native,
			Instrument:   websocketTestInstrument(exchange.VenueLighter, exchange.ProductSpot, native),
		})
	}
	if err := socket.SetAccountInstruments(catalog); err != nil {
		t.Fatal(err)
	}
	route := exchange.AccountRoute{AccountID: "account-1", RouteID: "route-1", Venue: exchange.VenueLighter, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, Settlement: "USDC", NativeFamily: "spot"}
	subscription, err := socket.WatchAccountEvents(context.Background(), exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	starts, indexed, callbacks := backend.starts, backend.indexCount, backend.callbacks
	backend.mu.Unlock()
	if starts != 1 || indexed != 1000 {
		t.Fatalf("route starts=%d indexed=%d, want 1 and 1000", starts, indexed)
	}
	first := receiveAccountEvent(t, subscription.Events())
	if first.Kind != exchange.AccountEventStreamStatus || first.Generation != 1 || first.Sequence != 1 {
		t.Fatalf("initial account barrier = %+v", first)
	}
	order := exchange.OrderEvent{Kind: exchange.EventDelta, Order: exchange.Order{NativeSymbol: "ASSET0999-USDC", OrderID: "order-1", UpdatedAt: time.Now().UTC()}}
	callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &order})
	got := receiveAccountEvent(t, subscription.Events())
	if got.Kind != exchange.AccountEventOrder || got.InstrumentID != "lighter:live:spot:ASSET0999-USDC" || got.Generation != 1 || got.Sequence != 2 {
		t.Fatalf("projected account order = %+v", got)
	}
	callbacks.Status(exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted, Generation: 1, Reason: "test-gap", Time: time.Now().UTC()})
	gap := receiveAccountEvent(t, subscription.Events())
	resync := receiveAccountEvent(t, subscription.Events())
	if gap.Kind != exchange.AccountEventStreamStatus || gap.Generation != 2 || gap.Sequence != 1 || resync.Kind != exchange.AccountEventResyncRequired || resync.Generation != 2 || resync.Sequence != 2 {
		t.Fatalf("gap=%+v resync=%+v", gap, resync)
	}
	unknown := exchange.OrderEvent{Kind: exchange.EventDelta, Order: exchange.Order{NativeSymbol: "UNKNOWN-USDC", OrderID: "order-2", UpdatedAt: time.Now().UTC()}}
	callbacks.Event(accountBackendEvent{Kind: exchange.AccountEventOrder, Order: &unknown})
	select {
	case projectErr := <-subscription.Errors():
		if projectErr == nil {
			t.Fatal("unknown native identity emitted nil error")
		}
	case <-time.After(time.Second):
		t.Fatal("unknown native identity was silently dropped")
	}
	if event := receiveAccountEvent(t, subscription.Events()); event.Kind != exchange.AccountEventResyncRequired {
		t.Fatalf("unknown identity event = %+v", event)
	}
	if err := subscription.Close(); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	stops := backend.stops
	backend.mu.Unlock()
	if stops != 1 {
		t.Fatalf("route stops=%d, want 1", stops)
	}

	second, err := socket.WatchAccountEvents(context.Background(), exchange.WatchAccountEventsRequest{Route: route})
	if err != nil {
		t.Fatal(err)
	}
	if barrier := receiveAccountEvent(t, second.Events()); barrier.Generation != 3 || barrier.Sequence != 1 {
		t.Fatalf("reconnected account barrier = %+v, want generation 3 sequence 1", barrier)
	}
	_ = second.Close()
}

func TestProjectEmptyPositionUsesBackendNativeIdentity(t *testing.T) {
	route := exchange.AccountRoute{AccountID: "account-1", RouteID: "route-1", Venue: exchange.VenueLighter, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, NativeFamily: "perp"}
	instrument := websocketTestInstrument(exchange.VenueLighter, exchange.ProductPerp, "ETH-USDC")
	index, err := newAccountInstrumentIndex(route, []exchange.AccountInstrument{{InstrumentID: "lighter:live:perp:ETH-USDC", Instrument: instrument}})
	if err != nil {
		t.Fatal(err)
	}
	position := exchange.PositionEvent{Kind: exchange.EventSnapshot, Time: time.Now().UTC()}
	events, err := projectAccountBackendEvent(route, index, accountBackendEvent{Kind: exchange.AccountEventPosition, NativeSymbol: "ETH-USDC", Position: &position})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].InstrumentID != "lighter:live:perp:ETH-USDC" || len(events[0].Position.Positions) != 0 {
		t.Fatalf("empty position projection = %+v", events)
	}
}

func receiveAccountEvent(t *testing.T, events <-chan exchange.AccountEvent) exchange.AccountEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for account event")
		return exchange.AccountEvent{}
	}
}
