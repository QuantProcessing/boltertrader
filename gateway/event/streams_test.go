package event

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/order"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/shopspring/decimal"
)

func TestStreamManagerPublishesPrivateResourcesAndRecoversAfterGap(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	first := newFakeStreamGeneration()
	second := newFakeStreamGeneration()
	socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second}}
	instrumentID := domain.InstrumentID("binance:demo:spot:BTC-USDT")
	instrument := streamTestInstrument(exchange.ProductSpot)
	accounts := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{
			AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true, WriteReady: true,
		}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: instrument,
		spot:       &fakeStreamSpotClient{socket: socket},
	}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx, []domain.AccountID{"acct-a"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool {
		return reconciler.Recoveries() == 1 && accounts.LatestReady()
	})

	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	fill := exchange.Fill{
		NativeSymbol: "BTCUSDT", FillID: "fill-1", OrderID: "venue-1",
		Price: decimal.NewFromInt(100), Quantity: decimal.RequireFromString("0.01"),
	}
	first.fills.events <- exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}
	first.balances.events <- exchange.BalanceEvent{
		Kind: exchange.EventSnapshot,
		Balances: []exchange.Balance{{
			Asset: "USDT", Available: decimal.NewFromInt(100), Total: decimal.NewFromInt(100),
		}},
	}
	first.fills.events <- exchange.FillEvent{Kind: exchange.EventDelta, Fill: fill}

	events := map[Kind][]Event{}
	for range 3 {
		event := receiveEvent(t, subscription)
		events[event.Kind] = append(events[event.Kind], event)
	}
	if len(events[KindFill]) != 2 || events[KindFill][0].ResourceVersion != 1 ||
		events[KindFill][1].ResourceVersion != 2 {
		t.Fatalf("fill events=%+v", events[KindFill])
	}
	var fillPayload FillPayload
	if err := json.Unmarshal(events[KindFill][0].Payload, &fillPayload); err != nil {
		t.Fatal(err)
	}
	if fillPayload.AccountID != "acct-a" || fillPayload.InstrumentID != instrumentID ||
		fillPayload.Fill.FillID != fill.FillID {
		t.Fatalf("fill payload=%+v", fillPayload)
	}
	var balancePayload BalancePayload
	if err := json.Unmarshal(events[KindBalance][0].Payload, &balancePayload); err != nil {
		t.Fatal(err)
	}
	if balancePayload.AccountID != "acct-a" || balancePayload.Balance.Asset != "USDT" {
		t.Fatalf("balance payload=%+v", balancePayload)
	}
	first.orders.events <- exchange.OrderEvent{
		Kind: exchange.EventDelta,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1",
			ClientOrderID: "101", Status: "filled",
		},
	}
	awaitCondition(t, func() bool { return len(reconciler.Observations()) == 1 })
	observation := reconciler.Observations()[0]
	if observation.AccountID != "acct-a" || observation.InstrumentID != instrumentID ||
		observation.Product != exchange.ProductSpot || observation.Order.ClientOrderID != "101" {
		t.Fatalf("order observation=%+v", observation)
	}

	first.fills.status <- exchange.StreamStatusEvent{
		State: exchange.SubscriptionGap, Phase: exchange.GapStarted, Reason: "sequence_gap",
	}
	resync := receiveEvent(t, subscription)
	if resync.Kind == KindAccountHealth {
		var health AccountHealthPayload
		if err := json.Unmarshal(resync.Payload, &health); err != nil {
			t.Fatal(err)
		}
		status, err := accounts.Status("acct-a")
		if err != nil {
			t.Fatal(err)
		}
		if health.WriteReady != status.WriteReady || health.WriteReady ||
			health.ReadReady != status.ReadReady || health.Reason != status.Reason {
			t.Fatalf("account health=%+v REST status=%+v", health, status)
		}
		resync = receiveEvent(t, subscription)
	}
	if resync.Kind != KindResyncRequired {
		t.Fatalf("gap event=%+v", resync)
	}
	awaitCondition(t, func() bool {
		return reconciler.Suspensions() >= 2 && reconciler.Recoveries() >= 2 &&
			socket.Generation() >= 2 && accounts.LatestReady()
	})
}

func TestStreamManagerPublishesPerpPositionsWithMonotonicVersions(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	generation := newFakeStreamGeneration()
	socket := &fakePerpSocket{spot: fakeSpotSocket{generations: []*fakeStreamGeneration{generation}}}
	instrumentID := domain.InstrumentID("binance:demo:perp:USDT:BTC-USDT")
	instrument := streamTestInstrument(exchange.ProductPerp)
	accounts := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{
			AccountID: "acct-p", Product: exchange.ProductPerp, ReadReady: true, WriteReady: true,
		}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: instrument,
		perp:       &fakeStreamPerpClient{socket: socket},
	}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx, []domain.AccountID{"acct-p"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool { return accounts.LatestReady() })

	subscription := subscribeTest(t, hub, "principal-a", "acct-p", "")
	position := exchange.Position{
		NativeSymbol: "BTCUSDT", Side: exchange.SideBuy, Quantity: decimal.NewFromInt(1),
	}
	generation.positions.events <- exchange.PositionEvent{
		Kind: exchange.EventSnapshot, Positions: []exchange.Position{position},
	}
	generation.positions.events <- exchange.PositionEvent{
		Kind: exchange.EventDelta, Positions: []exchange.Position{position},
	}
	first, second := receiveEvent(t, subscription), receiveEvent(t, subscription)
	if first.Kind != KindPosition || second.Kind != KindPosition ||
		first.ResourceVersion != 1 || second.ResourceVersion != 2 {
		t.Fatalf("position events=%+v %+v", first, second)
	}
	var payload PositionPayload
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != "acct-p" || payload.InstrumentID != instrumentID ||
		payload.Position.NativeSymbol != "BTCUSDT" {
		t.Fatalf("position payload=%+v", payload)
	}
}

func TestStreamManagerIgnoresExternalOrdersButResyncsOnOwnedOrderPersistenceFailure(t *testing.T) {
	tests := []struct {
		name        string
		observeErr  error
		wantResync  bool
		wantRecover int
	}{
		{name: "external order", observeErr: store.ErrNotFound, wantRecover: 1},
		{name: "durable observation failure", observeErr: errors.New("store unavailable"), wantResync: true, wantRecover: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := newTestHub(t, 16, 8)
			first, second := newFakeStreamGeneration(), newFakeStreamGeneration()
			socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second}}
			instrumentID := domain.InstrumentID("binance:demo:spot:BTC-USDT")
			accounts := &fakeStreamAccounts{
				status: account.Status{Resource: account.Resource{
					AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true, WriteReady: true,
				}},
				snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
				instrument: streamTestInstrument(exchange.ProductSpot),
				spot:       &fakeStreamSpotClient{socket: socket},
			}
			reconciler := &fakeStreamReconciler{observeErr: test.observeErr}
			manager, err := NewStreamManager(StreamOptions{
				Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
				ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := manager.Start(ctx, []domain.AccountID{"acct-a"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			awaitCondition(t, func() bool { return accounts.LatestReady() })
			subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
			first.orders.events <- exchange.OrderEvent{
				Kind: exchange.EventDelta,
				Order: exchange.Order{
					NativeSymbol: "BTCUSDT", OrderID: "venue-external",
					ClientOrderID: "external-order", Status: "open",
				},
			}
			awaitCondition(t, func() bool { return len(reconciler.Observations()) == 1 })
			if test.wantResync {
				event := receiveEvent(t, subscription)
				if event.Kind == KindAccountHealth {
					event = receiveEvent(t, subscription)
				}
				if event.Kind != KindResyncRequired {
					t.Fatalf("event=%+v, want resync_required", event)
				}
				awaitCondition(t, func() bool { return reconciler.Recoveries() >= test.wantRecover })
			} else {
				time.Sleep(20 * time.Millisecond)
				if got := reconciler.Recoveries(); got != test.wantRecover {
					t.Fatalf("recoveries=%d, want %d", got, test.wantRecover)
				}
				assertNoEvent(t, subscription)
			}
		})
	}
}

type fakeStreamAccounts struct {
	mu         sync.Mutex
	status     account.Status
	snapshot   catalog.Snapshot
	instrument exchange.Instrument
	spot       exchange.SpotClient
	perp       exchange.PerpClient
	states     []bool
}

func (accounts *fakeStreamAccounts) Status(accountID domain.AccountID) (account.Status, error) {
	accounts.mu.Lock()
	defer accounts.mu.Unlock()
	if accountID != accounts.status.AccountID {
		return account.Status{}, account.ErrUnknownAccount
	}
	return accounts.status, nil
}

func (accounts *fakeStreamAccounts) Instruments(accountID domain.AccountID) (catalog.Snapshot, error) {
	if accountID != accounts.status.AccountID {
		return catalog.Snapshot{}, account.ErrUnknownAccount
	}
	return accounts.snapshot, nil
}

func (accounts *fakeStreamAccounts) Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error) {
	return accounts.instrument, nil
}

func (accounts *fakeStreamAccounts) Spot(domain.AccountID) (exchange.SpotClient, error) {
	if accounts.spot == nil {
		return nil, account.ErrProductMismatch
	}
	return accounts.spot, nil
}

func (accounts *fakeStreamAccounts) Perp(domain.AccountID) (exchange.PerpClient, error) {
	if accounts.perp == nil {
		return nil, account.ErrProductMismatch
	}
	return accounts.perp, nil
}

func (accounts *fakeStreamAccounts) SetPrivateStreamState(_ domain.AccountID, ready bool, reason string) error {
	if ready && reason != "" {
		return errors.New("ready state has reason")
	}
	accounts.mu.Lock()
	defer accounts.mu.Unlock()
	accounts.states = append(accounts.states, ready)
	accounts.status.WriteReady = ready
	accounts.status.Reason = reason
	return nil
}

func (accounts *fakeStreamAccounts) LatestReady() bool {
	accounts.mu.Lock()
	defer accounts.mu.Unlock()
	return len(accounts.states) > 0 && accounts.states[len(accounts.states)-1]
}

type fakeStreamReconciler struct {
	mu          sync.Mutex
	suspensions int
	recoveries  int
	observed    []order.VenueOrderObservation
	observeErr  error
}

func (reconciler *fakeStreamReconciler) SuspendAccount(domain.AccountID) error {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.suspensions++
	return nil
}

func (reconciler *fakeStreamReconciler) RecoverAccount(_ context.Context, accountID domain.AccountID) (order.RecoveryReport, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.recoveries++
	return order.RecoveryReport{AccountID: accountID, WriteReady: true}, nil
}

func (reconciler *fakeStreamReconciler) Suspensions() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.suspensions
}

func (reconciler *fakeStreamReconciler) Recoveries() int {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return reconciler.recoveries
}

func (reconciler *fakeStreamReconciler) ObserveVenueOrder(_ context.Context, observation order.VenueOrderObservation) (order.Result, error) {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.observed = append(reconciler.observed, observation)
	return order.Result{}, reconciler.observeErr
}

func (reconciler *fakeStreamReconciler) Observations() []order.VenueOrderObservation {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	return append([]order.VenueOrderObservation(nil), reconciler.observed...)
}

type fakeStreamGeneration struct {
	orders    *fakeSubscription[exchange.OrderEvent]
	fills     *fakeSubscription[exchange.FillEvent]
	balances  *fakeSubscription[exchange.BalanceEvent]
	positions *fakeSubscription[exchange.PositionEvent]
}

func newFakeStreamGeneration() *fakeStreamGeneration {
	return &fakeStreamGeneration{
		orders:    newFakeSubscription[exchange.OrderEvent](),
		fills:     newFakeSubscription[exchange.FillEvent](),
		balances:  newFakeSubscription[exchange.BalanceEvent](),
		positions: newFakeSubscription[exchange.PositionEvent](),
	}
}

type fakeSpotSocket struct {
	exchange.SpotWebSocket
	mu           sync.Mutex
	generations  []*fakeStreamGeneration
	generation   int
	orderCalls   int
	fillCalls    int
	balanceCalls int
}

func (socket *fakeSpotSocket) current() *fakeStreamGeneration {
	if socket.generation >= len(socket.generations) {
		return socket.generations[len(socket.generations)-1]
	}
	return socket.generations[socket.generation]
}

func (socket *fakeSpotSocket) WatchOrders(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.OrderEvent], error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.orderCalls++
	return socket.current().orders, nil
}

func (socket *fakeSpotSocket) WatchFills(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.FillEvent], error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.fillCalls++
	return socket.current().fills, nil
}

func (socket *fakeSpotSocket) WatchBalances(context.Context, exchange.WatchAccountRequest) (exchange.Subscription[exchange.BalanceEvent], error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	socket.balanceCalls++
	result := socket.current().balances
	socket.generation++
	return result, nil
}

func (socket *fakeSpotSocket) Generation() int {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return socket.generation
}

type fakePerpSocket struct {
	exchange.PerpWebSocket
	spot fakeSpotSocket
}

func (socket *fakePerpSocket) WatchOrders(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.OrderEvent], error) {
	return socket.spot.WatchOrders(ctx, request)
}

func (socket *fakePerpSocket) WatchFills(ctx context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.FillEvent], error) {
	return socket.spot.WatchFills(ctx, request)
}

func (socket *fakePerpSocket) WatchBalances(ctx context.Context, request exchange.WatchAccountRequest) (exchange.Subscription[exchange.BalanceEvent], error) {
	return socket.spot.WatchBalances(ctx, request)
}

func (socket *fakePerpSocket) WatchPositions(context.Context, exchange.WatchRequest) (exchange.Subscription[exchange.PositionEvent], error) {
	socket.spot.mu.Lock()
	defer socket.spot.mu.Unlock()
	return socket.spot.current().positions, nil
}

type fakeStreamSpotClient struct {
	exchange.SpotClient
	socket *fakeSpotSocket
}

func (client *fakeStreamSpotClient) WebSocket() exchange.SpotWebSocket { return client.socket }

type fakeStreamPerpClient struct {
	exchange.PerpClient
	socket *fakePerpSocket
}

func (client *fakeStreamPerpClient) WebSocket() exchange.PerpWebSocket { return client.socket }

type fakeSubscription[T any] struct {
	events chan T
	status chan exchange.StreamStatusEvent
	errors chan error
	once   sync.Once
}

func newFakeSubscription[T any]() *fakeSubscription[T] {
	return &fakeSubscription[T]{
		events: make(chan T, 8), status: make(chan exchange.StreamStatusEvent, 8), errors: make(chan error, 8),
	}
}

func (*fakeSubscription[T]) ID() string                    { return "test-subscription" }
func (subscription *fakeSubscription[T]) Events() <-chan T { return subscription.events }
func (subscription *fakeSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	return subscription.status
}
func (subscription *fakeSubscription[T]) Errors() <-chan error { return subscription.errors }
func (subscription *fakeSubscription[T]) Close() error {
	subscription.once.Do(func() {})
	return nil
}

func streamTestInstrument(product exchange.Product) exchange.Instrument {
	settlement := ""
	if product == exchange.ProductPerp {
		settlement = "USDT"
	}
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: product, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: settlement,
			NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
	}
}

func awaitCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}
