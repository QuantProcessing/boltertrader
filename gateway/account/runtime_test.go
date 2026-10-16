package account

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

// fakePerp implements the parts of exchange.PerpClient the runtime touches.
type fakePerp struct {
	exchange.PerpClient
	mu        sync.Mutex
	positions []exchange.Position
	reads     int
	socket    *fakeSocket
}

func (client *fakePerp) Positions(context.Context, exchange.PositionsRequest) ([]exchange.Position, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.reads++
	return append([]exchange.Position(nil), client.positions...), nil
}

func (client *fakePerp) Balances(context.Context) ([]exchange.Balance, error) {
	return []exchange.Balance{{Asset: "USDT", Total: decimal.NewFromInt(100), Available: decimal.NewFromInt(90), Locked: decimal.NewFromInt(10)}}, nil
}

func (client *fakePerp) WebSocket() exchange.PerpWebSocket { return client.socket }
func (client *fakePerp) Close() error                      { return nil }

func (client *fakePerp) setPositions(positions ...exchange.Position) {
	client.mu.Lock()
	client.positions = positions
	client.mu.Unlock()
}

func (client *fakePerp) readCount() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.reads
}

type fakeSocket struct {
	exchange.PerpWebSocket
	subscription *fakeSubscription
}

func (socket *fakeSocket) WatchAccountEvents(context.Context, exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	return socket.subscription, nil
}

type fakeSubscription struct {
	events chan exchange.AccountEvent
	status chan exchange.StreamStatusEvent
	errors chan error
}

func newFakeSubscription() *fakeSubscription {
	return &fakeSubscription{events: make(chan exchange.AccountEvent, 8), status: make(chan exchange.StreamStatusEvent, 8), errors: make(chan error, 1)}
}

func (s *fakeSubscription) ID() string                                { return "fake" }
func (s *fakeSubscription) Events() <-chan exchange.AccountEvent      { return s.events }
func (s *fakeSubscription) Status() <-chan exchange.StreamStatusEvent { return s.status }
func (s *fakeSubscription) Errors() <-chan error                      { return s.errors }
func (s *fakeSubscription) Close() error                              { return nil }

type collector struct {
	mu       sync.Mutex
	messages []hub.Message
}

func (c *collector) emit(message hub.Message, _ bool) {
	c.mu.Lock()
	c.messages = append(c.messages, message)
	c.mu.Unlock()
}

func (c *collector) lastPositions() ([]Position, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].Channel == ChannelPositions {
			return c.messages[i].Data.([]Position), true
		}
	}
	return nil, false
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestRuntimePushesFullPositionListFromSnapshotStreamAndFillRefresh(t *testing.T) {
	native := testInstrument().Native()
	catalog := market.NewStaticCatalog([]exchange.Instrument{native})
	marketKey, _ := venue.MarketOf(native)
	subscription := newFakeSubscription()
	perp := &fakePerp{socket: &fakeSocket{subscription: subscription}}
	perp.setPositions(exchange.Position{NativeSymbol: "BTCUSDT", PositionSlot: exchange.PositionSlotNet, Quantity: decimal.RequireFromString("0.01"), EntryPrice: decimal.NewFromInt(60000)})

	rt := newRuntime("acc_test", exchange.VenueBinance, []routeClient{{market: marketKey, perp: perp}}, catalog)
	sink := &collector{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- rt.stream(ctx, sink.emit) }()

	// 1. Initial REST snapshot is published.
	waitFor(t, func() bool {
		positions, ok := sink.lastPositions()
		return ok && len(positions) == 1 && positions[0].Size == "0.01" && positions[0].Side == "long"
	})

	// 2. A venue position event replaces the instrument's rows.
	subscription.events <- exchange.AccountEvent{Kind: exchange.AccountEventPosition, InstrumentID: "binance:perp:BTC-USDT", ObservedAt: time.Now(),
		Position: &exchange.PositionEvent{Kind: exchange.EventDelta, Positions: []exchange.Position{{NativeSymbol: "BTCUSDT", PositionSlot: exchange.PositionSlotNet, Quantity: decimal.RequireFromString("-0.03"), EntryPrice: decimal.NewFromInt(61000)}}}}
	waitFor(t, func() bool {
		positions, _ := sink.lastPositions()
		return len(positions) == 1 && positions[0].Size == "0.03" && positions[0].Side == "short"
	})

	// 3. A fill on a venue that does not push positions triggers a REST refresh.
	perp.setPositions()
	reads := perp.readCount()
	subscription.events <- exchange.AccountEvent{Kind: exchange.AccountEventFill, InstrumentID: "binance:perp:BTC-USDT", ObservedAt: time.Now(),
		Fill: &exchange.FillEvent{Fill: exchange.Fill{FillID: "1", OrderID: "2", Side: exchange.SideBuy, Price: decimal.NewFromInt(1), Quantity: decimal.NewFromInt(1)}}}
	waitFor(t, func() bool {
		positions, _ := sink.lastPositions()
		return perp.readCount() > reads && len(positions) == 0
	})

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
