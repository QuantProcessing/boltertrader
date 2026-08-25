package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
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
	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Start(ctx, []domain.AccountID{"acct-a"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool {
		return reconciler.Recoveries() == 1 && accounts.LatestReady()
	})
	if event := receiveEvent(t, subscription); event.Kind != KindAccountHealth {
		t.Fatalf("initial private stream event kind=%s, want %s", event.Kind, KindAccountHealth)
	}
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
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
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

func TestStreamManagerOpensOneAccountRouteStreamForLargeCatalog(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	socket := &fakeAccountRouteSocket{subscription: newFakeSubscription[exchange.AccountEvent]()}
	client := &fakeAccountRoutePerpClient{socket: socket}
	instrument := streamTestInstrument(exchange.ProductPerp)
	instruments := make([]catalog.Instrument, 1000)
	for index := range instruments {
		instruments[index] = catalog.Instrument{ID: domain.InstrumentID(fmt.Sprintf("binance:live:perp:USDT:TEST-%04d-USDT", index))}
	}
	accounts := &fakeStreamAccounts{
		status:   account.Status{Resource: account.Resource{AccountID: "acct-large", Product: exchange.ProductPerp, ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: instruments}, instrument: instrument, perp: client,
	}
	manager, err := NewStreamManager(StreamOptions{Accounts: accounts, Reconciler: &fakeStreamReconciler{}, Hub: hub, PrincipalID: "principal-a"})
	if err != nil {
		t.Fatal(err)
	}
	streams, err := manager.openAccountStreams(t.Context(), "acct-large")
	if err != nil {
		t.Fatal(err)
	}
	defer streams.close()
	if socket.calls != 1 || socket.catalogSize != len(instruments) {
		t.Fatalf("account route calls=%d catalog=%d", socket.calls, socket.catalogSize)
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
			instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
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

func TestStreamManagerOpensDynamicRoutesFromAtomicSelectionsAndDeduplicatesBalances(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	spotGeneration := newFakeStreamGeneration()
	perpGeneration := newFakeStreamGeneration()
	spotSocket := &fakeSpotSocket{generations: []*fakeStreamGeneration{spotGeneration}}
	perpSocket := &fakePerpSocket{spot: fakeSpotSocket{generations: []*fakeStreamGeneration{perpGeneration}}}
	spotClient := &fakeStreamSpotClient{socket: spotSocket}
	perpClient := &fakeStreamPerpClient{socket: perpSocket}
	spotBTC := domain.InstrumentID("binance:live:spot:BTC-USDT")
	spotETH := domain.InstrumentID("binance:live:spot:ETH-USDT")
	perpBTC := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	fence := domain.AccountFence{
		AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3,
	}
	base := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{AccountID: "acct-dynamic", ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{
			{ID: spotBTC}, {ID: spotETH}, {ID: perpBTC},
		}},
	}
	accounts := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		spotBTC: dynamicStreamSelection(fence, "spot", spotBTC, streamTestInstrument(exchange.ProductSpot), spotClient),
		spotETH: dynamicStreamSelection(fence, "spot", spotETH, streamTestInstrument(exchange.ProductSpot), spotClient),
		perpBTC: dynamicStreamSelection(fence, "perp-usdt", perpBTC, streamTestInstrument(exchange.ProductPerp), perpClient),
	}}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: &fakeStreamReconciler{}, Hub: hub, PrincipalID: "principal-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	streams, err := manager.openAccountStreams(t.Context(), "acct-dynamic")
	if err != nil {
		t.Fatal(err)
	}
	defer streams.close()
	subscription := subscribeTest(t, hub, "principal-a", "acct-dynamic", "")

	spotOrders, spotFills, spotBalances := spotSocket.Calls()
	perpOrders, perpFills, perpBalances := perpSocket.spot.Calls()
	if spotOrders != 2 || spotFills != 2 || spotBalances != 1 {
		t.Fatalf("spot calls=(orders=%d fills=%d balances=%d)", spotOrders, spotFills, spotBalances)
	}
	if perpOrders != 1 || perpFills != 1 || perpBalances != 1 || perpSocket.PositionCalls() != 1 {
		t.Fatalf("perp calls=(orders=%d fills=%d balances=%d positions=%d)",
			perpOrders, perpFills, perpBalances, perpSocket.PositionCalls())
	}

	spotGeneration.balances.events <- exchange.BalanceEvent{
		Kind: exchange.EventSnapshot,
		Balances: []exchange.Balance{{
			Asset: "SPOT-SNAPSHOT", Available: decimal.NewFromInt(1), Total: decimal.NewFromInt(1),
		}},
	}
	spotGeneration.balances.events <- exchange.BalanceEvent{
		Kind: exchange.EventDelta,
		Balances: []exchange.Balance{{
			Asset: "SPOT-DELTA", Available: decimal.NewFromInt(1), Total: decimal.NewFromInt(1),
		}},
	}
	receiveEvent(t, subscription)
	receiveEvent(t, subscription)

	// Both balance events are now published, and the barrier is still shut. It
	// asks whether the private streams are live, and a payload's baseline does
	// not answer that: five of the nine adapters only ever emit EventDelta, and
	// Binance emits nothing at all while an account's balances do not move.
	select {
	case <-streams.activated:
		t.Fatal("private activation barrier completed on balance payloads alone")
	default:
	}

	// The exchange layer reports liveness the same way for every venue, once the
	// backend's subscribe has succeeded. That is what opens the barrier.
	spotGeneration.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	perpGeneration.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	select {
	case <-streams.activated:
	case <-time.After(time.Second):
		t.Fatal("private activation barrier did not complete after every route reported active")
	}
}

func TestAccountStreamsFaultBeforeEveryRouteActivatesDoesNotReleaseBarrier(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	streams := &accountStreams{
		cancel: cancel, faults: make(chan struct{}, 1), activated: make(chan struct{}),
	}
	defer streams.close()
	streams.expectActivations(map[domain.RouteID]struct{}{"spot": {}, "perp-usdt": {}})
	streams.observeActivation("spot")
	streams.fault()

	select {
	case <-streams.faults:
	default:
		t.Fatal("private stream fault was not reported")
	}
	select {
	case <-streams.activated:
		t.Fatal("fault released the private activation barrier before every route activated")
	default:
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("fault canceled stream context: %v", err)
	}
}

func TestStreamManagerFailsClosedWhenDynamicRouteSelectionsCrossGenerations(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	spotSocket := &fakeSpotSocket{generations: []*fakeStreamGeneration{newFakeStreamGeneration()}}
	spotClient := &fakeStreamSpotClient{socket: spotSocket}
	firstID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	secondID := domain.InstrumentID("binance:live:spot:ETH-USDT")
	firstFence := domain.AccountFence{AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}
	secondFence := firstFence
	secondFence.RuntimeGeneration = 2
	base := &fakeStreamAccounts{
		status:   account.Status{Resource: account.Resource{AccountID: "acct-dynamic", ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{{ID: firstID}, {ID: secondID}}},
	}
	accounts := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		firstID:  dynamicStreamSelection(firstFence, "spot", firstID, streamTestInstrument(exchange.ProductSpot), spotClient),
		secondID: dynamicStreamSelection(secondFence, "spot", secondID, streamTestInstrument(exchange.ProductSpot), spotClient),
	}}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: &fakeStreamReconciler{}, Hub: hub, PrincipalID: "principal-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if streams, err := manager.openAccountStreams(t.Context(), "acct-dynamic"); err == nil {
		streams.close()
		t.Fatal("cross-generation stream selection succeeded")
	}
	if accounts.SelectionCalls() != 2 {
		t.Fatalf("selection calls=%d want=2", accounts.SelectionCalls())
	}
	orders, fills, balances := spotSocket.Calls()
	if orders != 0 || fills != 0 || balances != 0 {
		t.Fatalf("cross-generation selection opened streams: %d %d %d", orders, fills, balances)
	}
}

func TestStreamManagerReconnectRequiresFreshPrivateActivationBarrier(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	first, second := newFakeStreamGeneration(), newFakeStreamGeneration()
	socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second}}
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	base := &fakeStreamAccounts{
		status:     account.Status{Resource: account.Resource{AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: streamTestInstrument(exchange.ProductSpot),
		spot:       &fakeStreamSpotClient{socket: socket},
	}
	accounts := &barrierStreamAccounts{fakeStreamAccounts: base}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-a"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 1 })
	if accounts.LatestReady() || accounts.BarrierCount() != 0 {
		t.Fatalf("ready=%t barriers=%d before initial activation", accounts.LatestReady(), accounts.BarrierCount())
	}
	first.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.LatestReady() && accounts.BarrierCount() == 1 })

	first.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	awaitCondition(t, func() bool {
		return reconciler.Recoveries() == 2 && socket.Generation() == 2 && !accounts.LatestReady()
	})
	if accounts.BarrierCount() != 1 {
		t.Fatalf("barriers=%d before reconnect activation", accounts.BarrierCount())
	}
	second.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.LatestReady() && accounts.BarrierCount() == 2 })
	barriers := accounts.Barriers()
	if barriers[0].snapshotID == barriers[1].snapshotID || barriers[0].sequence >= barriers[1].sequence {
		t.Fatalf("barriers=%+v", barriers)
	}
}

func TestStreamManagerDynamicRoutesRequireIndependentActivationBarriers(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	spotFirst, spotSecond := newFakeStreamGeneration(), newFakeStreamGeneration()
	perpFirst := newFakeStreamGeneration()
	spotSocket := &fakeSpotSocket{generations: []*fakeStreamGeneration{spotFirst, spotSecond}}
	perpSocket := &fakePerpSocket{spot: fakeSpotSocket{generations: []*fakeStreamGeneration{perpFirst}}}
	spotID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	perpID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	fence := domain.AccountFence{
		AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3,
	}
	base := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{AccountID: "acct-dynamic", ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{
			{ID: spotID}, {ID: perpID},
		}},
	}
	routes := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		spotID: dynamicStreamSelection(fence, "spot", spotID, streamTestInstrument(exchange.ProductSpot), &fakeStreamSpotClient{socket: spotSocket}),
		perpID: dynamicStreamSelection(fence, "perp-usdt", perpID, streamTestInstrument(exchange.ProductPerp), &fakeStreamPerpClient{socket: perpSocket}),
	}}
	accounts := &dynamicBarrierStreamAccounts{atomicStreamAccounts: routes}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-dynamic"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 1 })

	spotFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	perpFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool {
		return accounts.LatestReady() && accounts.BarrierCount() == 2 &&
			accounts.RouteReady("spot") && accounts.RouteReady("perp-usdt")
	})

	spotFirst.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	awaitCondition(t, func() bool {
		return reconciler.Recoveries() == 2 && spotSocket.Generation() == 2 && !accounts.LatestReady()
	})
	if !accounts.RouteReady("perp-usdt") || perpSocket.spot.Generation() != 1 {
		t.Fatalf("spot reconnect changed perp route: ready=%t generation=%d",
			accounts.RouteReady("perp-usdt"), perpSocket.spot.Generation())
	}
	spotSecond.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.LatestReady() && accounts.BarrierCount() == 3 })
	if !accounts.RouteReady("spot") || !accounts.RouteReady("perp-usdt") {
		t.Fatalf("route readiness after spot recovery: spot=%t perp=%t",
			accounts.RouteReady("spot"), accounts.RouteReady("perp-usdt"))
	}
}

func TestStreamManagerDynamicRouteGapDoesNotRestartOrDegradeUnrelatedRoute(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	spotFirst, spotSecond := newFakeStreamGeneration(), newFakeStreamGeneration()
	perpFirst := newFakeStreamGeneration()
	spotSocket := &fakeSpotSocket{generations: []*fakeStreamGeneration{spotFirst, spotSecond}}
	perpSocket := &fakePerpSocket{spot: fakeSpotSocket{generations: []*fakeStreamGeneration{perpFirst}}}
	spotID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	perpID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	fence := domain.AccountFence{
		AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3,
	}
	base := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{AccountID: "acct-dynamic", ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{
			{ID: spotID}, {ID: perpID},
		}},
	}
	routes := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		spotID: dynamicStreamSelection(fence, "spot", spotID, streamTestInstrument(exchange.ProductSpot), &fakeStreamSpotClient{socket: spotSocket}),
		perpID: dynamicStreamSelection(fence, "perp-usdt", perpID, streamTestInstrument(exchange.ProductPerp), &fakeStreamPerpClient{socket: perpSocket}),
	}}
	accounts := &dynamicBarrierStreamAccounts{atomicStreamAccounts: routes, routeReady: make(map[domain.RouteID]bool)}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: &fakeStreamReconciler{}, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-dynamic"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	spotFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	perpFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.RouteReady("spot") && accounts.RouteReady("perp-usdt") })

	spotFirst.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	awaitCondition(t, func() bool { return spotSocket.Generation() == 2 && !accounts.RouteReady("spot") })
	if !accounts.RouteReady("perp-usdt") {
		t.Fatal("spot gap degraded unrelated perp readiness")
	}
	if generation := perpSocket.spot.Generation(); generation != 1 {
		t.Fatalf("spot gap restarted unrelated perp subscriptions: generation=%d", generation)
	}

	spotSecond.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.RouteReady("spot") })
	if !accounts.RouteReady("perp-usdt") {
		t.Fatal("spot recovery changed unrelated perp readiness")
	}
}

func TestStreamManagerProcessesSiblingFaultWhileAnotherRouteReconnectIsBlocked(t *testing.T) {
	hub := newTestHub(t, 32, 16)
	spotFirst, spotSecond := newFakeStreamGeneration(), newFakeStreamGeneration()
	perpFirst := newFakeStreamGeneration()
	spotSocket := &fakeSpotSocket{generations: []*fakeStreamGeneration{spotFirst, spotSecond}}
	perpSocket := &fakePerpSocket{spot: fakeSpotSocket{generations: []*fakeStreamGeneration{perpFirst}}}
	spotID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	perpID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	fence := domain.AccountFence{AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3}
	base := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{AccountID: fence.AccountID, ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{
			{ID: spotID}, {ID: perpID},
		}},
	}
	routes := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		spotID: dynamicStreamSelection(fence, "spot", spotID, streamTestInstrument(exchange.ProductSpot), &fakeStreamSpotClient{socket: spotSocket}),
		perpID: dynamicStreamSelection(fence, "perp-usdt", perpID, streamTestInstrument(exchange.ProductPerp), &fakeStreamPerpClient{socket: perpSocket}),
	}}
	accounts := &dynamicBarrierStreamAccounts{atomicStreamAccounts: routes, routeReady: make(map[domain.RouteID]bool)}
	reconciler := &nthBlockingRecoveryReconciler{
		fakeStreamReconciler: &fakeStreamReconciler{}, blockAt: 2,
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{fence.AccountID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	defer close(reconciler.release)
	spotFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	perpFirst.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.RouteReady("spot") && accounts.RouteReady("perp-usdt") })

	spotFirst.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	select {
	case <-reconciler.entered:
	case <-time.After(time.Second):
		t.Fatal("spot reconnect did not enter blocked recovery")
	}
	perpFirst.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	deadline := time.Now().Add(250 * time.Millisecond)
	for accounts.RouteReady("perp-usdt") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if accounts.RouteReady("perp-usdt") {
		t.Fatal("blocked spot reconnect prevented sibling perp fault from becoming unavailable")
	}
}

func TestStreamManagerReconnectsRouteAfterBarrierCallbackFailure(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	first, second := newFakeStreamGeneration(), newFakeStreamGeneration()
	socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second}}
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	fence := domain.AccountFence{AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3}
	base := &fakeStreamAccounts{
		status:   account.Status{Resource: account.Resource{AccountID: fence.AccountID, ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
	}
	routes := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		instrumentID: dynamicStreamSelection(fence, "spot", instrumentID, streamTestInstrument(exchange.ProductSpot), &fakeStreamSpotClient{socket: socket}),
	}}
	accounts := &dynamicBarrierStreamAccounts{
		atomicStreamAccounts: routes, routeReady: make(map[domain.RouteID]bool),
		barrierFailures: map[domain.RouteID]int{"spot": 1},
	}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{fence.AccountID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	first.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return socket.Generation() == 2 && reconciler.Recoveries() == 2 })
	if accounts.RouteReady("spot") {
		t.Fatal("route became ready after rejected private-stream barrier")
	}
	second.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.RouteReady("spot") })
}

func TestStreamManagerReconnectsDynamicRouteAfterFaultBeforeInitialActivation(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	first, second := newFakeStreamGeneration(), newFakeStreamGeneration()
	socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second}}
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	fence := domain.AccountFence{AccountID: "acct-dynamic", DesiredStateRevision: 1, RuntimeGeneration: 7, CredentialVersion: 3}
	base := &fakeStreamAccounts{
		status:   account.Status{Resource: account.Resource{AccountID: fence.AccountID, ReadReady: true}},
		snapshot: catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
	}
	routes := &atomicStreamAccounts{fakeStreamAccounts: base, selections: map[domain.InstrumentID]account.SelectedOrderRoute{
		instrumentID: dynamicStreamSelection(fence, "spot", instrumentID, streamTestInstrument(exchange.ProductSpot), &fakeStreamSpotClient{socket: socket}),
	}}
	accounts := &dynamicBarrierStreamAccounts{atomicStreamAccounts: routes, routeReady: make(map[domain.RouteID]bool)}
	reconciler := &fakeStreamReconciler{}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), []domain.AccountID{fence.AccountID}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(manager.Close)
	awaitCondition(t, func() bool { return socket.Generation() == 1 && reconciler.Recoveries() == 1 })
	if accounts.RouteReady("spot") {
		t.Fatal("route became ready before its private stream activated")
	}

	first.fills.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionGap, Phase: exchange.GapStarted}
	awaitCondition(t, func() bool { return socket.Generation() == 2 && reconciler.Recoveries() == 2 })
	if accounts.RouteReady("spot") {
		t.Fatal("route became ready after reconnect but before the replacement stream activated")
	}
	second.balances.status <- exchange.StreamStatusEvent{State: exchange.SubscriptionActive}
	awaitCondition(t, func() bool { return accounts.RouteReady("spot") })
}

func TestStreamManagerRegistersAccountsBeforeAndDuringStartIdempotently(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	first, second, third := newFakeStreamGeneration(), newFakeStreamGeneration(), newFakeStreamGeneration()
	socket := &fakeSpotSocket{generations: []*fakeStreamGeneration{first, second, third}}
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	accounts := &fakeStreamAccounts{
		status:     account.Status{Resource: account.Resource{AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: streamTestInstrument(exchange.ProductSpot),
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
	if err := manager.RegisterAccount("acct-a"); err != nil {
		t.Fatal(err)
	}
	if reconciler.Recoveries() != 0 {
		t.Fatal("pre-start registration launched supervisor")
	}
	if err := manager.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 1 })
	if err := manager.RegisterAccount("acct-a"); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 2 && socket.Generation() == 2 })
	if !first.Closed() {
		t.Fatal("runtime registration did not replace old-generation subscriptions")
	}
	manager.UnregisterAccount("acct-a")
	if !second.Closed() {
		t.Fatal("unregister returned before subscriptions closed")
	}
	manager.UnregisterAccount("acct-a")
	if err := manager.RegisterAccount("acct-a"); err != nil {
		t.Fatal(err)
	}
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 3 && socket.Generation() == 3 })

	closed := make(chan struct{}, 2)
	for range 2 {
		go func() {
			manager.Close()
			closed <- struct{}{}
		}()
	}
	for range 2 {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Close did not return")
		}
	}
	if err := manager.RegisterAccount("acct-a"); !errors.Is(err, ErrClosed) {
		t.Fatalf("register after close err=%v", err)
	}
}

func TestStreamManagerUnregisterDoesNotBlockIndependentRegistrationWhileSupervisorStops(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	generation := newFakeStreamGeneration()
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	accounts := &fakeStreamAccounts{
		status:     account.Status{Resource: account.Resource{AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: streamTestInstrument(exchange.ProductSpot),
		spot:       &fakeStreamSpotClient{socket: &fakeSpotSocket{generations: []*fakeStreamGeneration{generation}}},
	}
	reconciler := &blockingRecoveryReconciler{
		fakeStreamReconciler: &fakeStreamReconciler{},
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	defer close(reconciler.release)
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-a"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reconciler.entered:
	case <-time.After(time.Second):
		t.Fatal("account supervisor did not enter recovery")
	}

	unregistered := make(chan struct{})
	go func() {
		manager.UnregisterAccount("acct-a")
		close(unregistered)
	}()
	select {
	case <-unregistered:
		t.Fatal("unregister returned before blocked supervisor stopped")
	case <-time.After(20 * time.Millisecond):
	}

	registered := make(chan error, 1)
	go func() { registered <- manager.RegisterAccount("acct-b") }()
	select {
	case err := <-registered:
		if err != nil {
			t.Fatalf("register independent account: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("independent registration blocked behind another account shutdown")
	}
}

func TestStreamManagerStartFailureRollsBackForRealRetry(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	generation := newFakeStreamGeneration()
	instrumentID := domain.InstrumentID("binance:live:spot:BTC-USDT")
	accounts := &fakeStreamAccounts{
		status:     account.Status{Resource: account.Resource{AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true}},
		snapshot:   catalog.Snapshot{Instruments: []catalog.Instrument{{ID: instrumentID}}},
		instrument: streamTestInstrument(exchange.ProductSpot),
		spot:       &fakeStreamSpotClient{socket: &fakeSpotSocket{generations: []*fakeStreamGeneration{generation}}},
	}
	reconciler := &fakeStreamReconciler{suspendFailures: 1}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
		ReconnectBackoff: time.Millisecond, RecoveryTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-a"}); err == nil {
		t.Fatal("Start succeeded despite injected suspend failure")
	}
	if err := manager.Start(t.Context(), []domain.AccountID{"acct-a"}); err != nil {
		t.Fatalf("retry Start: %v", err)
	}
	awaitCondition(t, func() bool { return reconciler.Recoveries() == 1 })
}

func TestStreamManagerClosePublishesTerminalStateWhileRegistrationIsBlocked(t *testing.T) {
	hub := newTestHub(t, 16, 8)
	accounts := &fakeStreamAccounts{
		status: account.Status{Resource: account.Resource{AccountID: "acct-a", Product: exchange.ProductSpot, ReadReady: true}},
	}
	reconciler := &blockingSuspendReconciler{
		fakeStreamReconciler: &fakeStreamReconciler{},
		entered:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	manager, err := NewStreamManager(StreamOptions{
		Accounts: accounts, Reconciler: reconciler, Hub: hub, PrincipalID: "principal-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reconciler.release) }) }
	defer release()

	registered := make(chan error, 1)
	go func() { registered <- manager.RegisterAccount("acct-a") }()
	select {
	case <-reconciler.entered:
	case <-time.After(time.Second):
		t.Fatal("registration did not enter suspend")
	}
	closed := make(chan struct{})
	go func() {
		manager.Close()
		close(closed)
	}()
	awaitCondition(t, func() bool {
		manager.lifecycleMu.Lock()
		defer manager.lifecycleMu.Unlock()
		return manager.closed
	})
	if err := manager.RegisterAccount("acct-b"); !errors.Is(err, ErrClosed) {
		t.Fatalf("register after terminal state err=%v, want ErrClosed", err)
	}
	select {
	case <-closed:
		t.Fatal("Close returned while lifecycle operation was blocked")
	default:
	}

	release()
	if err := <-registered; !errors.Is(err, ErrClosed) {
		t.Fatalf("blocked registration error=%v, want ErrClosed", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after registration was released")
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

type atomicStreamAccounts struct {
	*fakeStreamAccounts
	selectionMu    sync.Mutex
	selections     map[domain.InstrumentID]account.SelectedOrderRoute
	selectionCalls int
}

func (accounts *atomicStreamAccounts) SelectOrderRoute(
	_ context.Context,
	_ domain.AccountID,
	instrumentID domain.InstrumentID,
	purpose account.OrderRoutePurpose,
	_ account.OrderRouteSelector,
) (account.SelectedOrderRoute, error) {
	accounts.selectionMu.Lock()
	defer accounts.selectionMu.Unlock()
	accounts.selectionCalls++
	if purpose != account.OrderRouteRecovery {
		return account.SelectedOrderRoute{}, errors.New("stream selection did not use recovery route")
	}
	selected, ok := accounts.selections[instrumentID]
	if !ok {
		return account.SelectedOrderRoute{}, account.ErrRuntimeBlocked
	}
	return selected, nil
}

func (accounts *atomicStreamAccounts) SelectionCalls() int {
	accounts.selectionMu.Lock()
	defer accounts.selectionMu.Unlock()
	return accounts.selectionCalls
}

func dynamicStreamSelection(
	fence domain.AccountFence,
	routeID domain.RouteID,
	_ domain.InstrumentID,
	instrument exchange.Instrument,
	orders exchange.OrderREST,
) account.SelectedOrderRoute {
	return account.SelectedOrderRoute{
		Dynamic: true, Fence: fence, RouteID: routeID, Instrument: instrument,
		Status: account.Status{Resource: account.Resource{AccountID: fence.AccountID, Product: instrument.Identity.Product}},
		Orders: orders,
	}
}

type privateBarrier struct {
	snapshotID string
	sequence   uint64
}

type barrierStreamAccounts struct {
	*fakeStreamAccounts
	barrierMu sync.Mutex
	barriers  []privateBarrier
}

type dynamicBarrierStreamAccounts struct {
	*atomicStreamAccounts
	barrierMu       sync.Mutex
	barriers        []privateBarrier
	routeReady      map[domain.RouteID]bool
	barrierFailures map[domain.RouteID]int
}

func (accounts *barrierStreamAccounts) SetPrivateStreamState(accountID domain.AccountID, ready bool, reason string) error {
	if ready {
		return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, false, account.ReasonPrivateStreamStarting)
	}
	return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, false, reason)
}

func (accounts *barrierStreamAccounts) ObservePrivateStreamBarrier(accountID domain.AccountID, snapshotID string, sequence uint64) error {
	accounts.barrierMu.Lock()
	accounts.barriers = append(accounts.barriers, privateBarrier{snapshotID: snapshotID, sequence: sequence})
	accounts.barrierMu.Unlock()
	return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, true, "")
}

func (accounts *barrierStreamAccounts) BarrierCount() int {
	accounts.barrierMu.Lock()
	defer accounts.barrierMu.Unlock()
	return len(accounts.barriers)
}

func (accounts *barrierStreamAccounts) Barriers() []privateBarrier {
	accounts.barrierMu.Lock()
	defer accounts.barrierMu.Unlock()
	return append([]privateBarrier(nil), accounts.barriers...)
}

func (accounts *dynamicBarrierStreamAccounts) SetPrivateStreamState(accountID domain.AccountID, ready bool, reason string) error {
	accounts.barrierMu.Lock()
	if accounts.routeReady == nil {
		accounts.routeReady = make(map[domain.RouteID]bool)
	}
	for _, selection := range accounts.selections {
		accounts.routeReady[selection.RouteID] = false
	}
	accounts.barrierMu.Unlock()
	if ready {
		return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, false, account.ReasonPrivateStreamStarting)
	}
	return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, false, reason)
}

func (accounts *dynamicBarrierStreamAccounts) ObservePrivateStreamBarrier(accountID domain.AccountID, snapshotID string, sequence uint64) error {
	accounts.barrierMu.Lock()
	if accounts.routeReady == nil {
		accounts.routeReady = make(map[domain.RouteID]bool)
	}
	accounts.barriers = append(accounts.barriers, privateBarrier{snapshotID: snapshotID, sequence: sequence})
	for _, selection := range accounts.selections {
		accounts.routeReady[selection.RouteID] = true
	}
	accounts.barrierMu.Unlock()
	return accounts.fakeStreamAccounts.SetPrivateStreamState(accountID, true, "")
}

func (accounts *dynamicBarrierStreamAccounts) SetPrivateStreamRouteState(fence domain.WriteFence, ready bool, reason string) error {
	accounts.barrierMu.Lock()
	if accounts.routeReady == nil {
		accounts.routeReady = make(map[domain.RouteID]bool)
	}
	accounts.routeReady[fence.RouteID] = false
	accounts.barrierMu.Unlock()
	return accounts.fakeStreamAccounts.SetPrivateStreamState(fence.AccountID, false, reason)
}

func (accounts *dynamicBarrierStreamAccounts) ObservePrivateStreamRouteBarrier(fence domain.WriteFence, snapshotID string, sequence uint64) error {
	accounts.barrierMu.Lock()
	if accounts.routeReady == nil {
		accounts.routeReady = make(map[domain.RouteID]bool)
	}
	if accounts.barrierFailures[fence.RouteID] > 0 {
		accounts.barrierFailures[fence.RouteID]--
		accounts.barrierMu.Unlock()
		return errors.New("injected route barrier failure")
	}
	accounts.barriers = append(accounts.barriers, privateBarrier{snapshotID: snapshotID, sequence: sequence})
	accounts.routeReady[fence.RouteID] = true
	allReady := accounts.allRoutesReadyLocked()
	accounts.barrierMu.Unlock()
	return accounts.fakeStreamAccounts.SetPrivateStreamState(fence.AccountID, allReady, "")
}

func (accounts *dynamicBarrierStreamAccounts) RouteReady(routeID domain.RouteID) bool {
	accounts.barrierMu.Lock()
	defer accounts.barrierMu.Unlock()
	return accounts.routeReady[routeID]
}

func (accounts *dynamicBarrierStreamAccounts) allRoutesReadyLocked() bool {
	seen := make(map[domain.RouteID]struct{})
	for _, selection := range accounts.selections {
		seen[selection.RouteID] = struct{}{}
	}
	if len(seen) == 0 {
		return false
	}
	for routeID := range seen {
		if !accounts.routeReady[routeID] {
			return false
		}
	}
	return true
}

func (accounts *dynamicBarrierStreamAccounts) BarrierCount() int {
	accounts.barrierMu.Lock()
	defer accounts.barrierMu.Unlock()
	return len(accounts.barriers)
}

type fakeStreamReconciler struct {
	mu              sync.Mutex
	suspensions     int
	suspendFailures int
	recoveries      int
	observed        []order.VenueOrderObservation
	observeErr      error
}

type blockingRecoveryReconciler struct {
	*fakeStreamReconciler
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type nthBlockingRecoveryReconciler struct {
	*fakeStreamReconciler
	mu      sync.Mutex
	blockAt int
	calls   int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type blockingSuspendReconciler struct {
	*fakeStreamReconciler
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (reconciler *blockingSuspendReconciler) SuspendAccount(accountID domain.AccountID) error {
	reconciler.once.Do(func() { close(reconciler.entered) })
	<-reconciler.release
	return reconciler.fakeStreamReconciler.SuspendAccount(accountID)
}

func (reconciler *blockingRecoveryReconciler) RecoverAccount(_ context.Context, accountID domain.AccountID) (order.RecoveryReport, error) {
	reconciler.once.Do(func() { close(reconciler.entered) })
	<-reconciler.release
	return order.RecoveryReport{AccountID: accountID, WriteReady: true}, nil
}

func (reconciler *nthBlockingRecoveryReconciler) RecoverAccount(ctx context.Context, accountID domain.AccountID) (order.RecoveryReport, error) {
	reconciler.mu.Lock()
	reconciler.calls++
	call := reconciler.calls
	reconciler.mu.Unlock()
	if call == reconciler.blockAt {
		reconciler.once.Do(func() { close(reconciler.entered) })
		select {
		case <-ctx.Done():
			return order.RecoveryReport{}, ctx.Err()
		case <-reconciler.release:
		}
	}
	return reconciler.fakeStreamReconciler.RecoverAccount(ctx, accountID)
}

func (reconciler *fakeStreamReconciler) SuspendAccount(domain.AccountID) error {
	reconciler.mu.Lock()
	defer reconciler.mu.Unlock()
	reconciler.suspensions++
	if reconciler.suspendFailures > 0 {
		reconciler.suspendFailures--
		return errors.New("injected suspend failure")
	}
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

func (socket *fakeSpotSocket) Calls() (orders, fills, balances int) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return socket.orderCalls, socket.fillCalls, socket.balanceCalls
}

type fakePerpSocket struct {
	exchange.PerpWebSocket
	spot          fakeSpotSocket
	positionCalls int
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
	socket.positionCalls++
	return socket.spot.current().positions, nil
}

func (socket *fakePerpSocket) PositionCalls() int {
	socket.spot.mu.Lock()
	defer socket.spot.mu.Unlock()
	return socket.positionCalls
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

type fakeAccountRouteSocket struct {
	exchange.PerpWebSocket
	calls        int
	catalogSize  int
	subscription exchange.Subscription[exchange.AccountEvent]
}

func (socket *fakeAccountRouteSocket) WatchAccountEvents(_ context.Context, request exchange.WatchAccountEventsRequest) (exchange.Subscription[exchange.AccountEvent], error) {
	socket.calls++
	socket.catalogSize = len(request.Catalog)
	return socket.subscription, nil
}

type fakeAccountRoutePerpClient struct {
	exchange.PerpClient
	socket *fakeAccountRouteSocket
}

func (client *fakeAccountRoutePerpClient) WebSocket() exchange.PerpWebSocket { return client.socket }

type fakeSubscription[T any] struct {
	events chan T
	status chan exchange.StreamStatusEvent
	errors chan error
	closed chan struct{}
	once   sync.Once
}

func newFakeSubscription[T any]() *fakeSubscription[T] {
	return &fakeSubscription[T]{
		events: make(chan T, 8), status: make(chan exchange.StreamStatusEvent, 8), errors: make(chan error, 8),
		closed: make(chan struct{}),
	}
}

func (*fakeSubscription[T]) ID() string                    { return "test-subscription" }
func (subscription *fakeSubscription[T]) Events() <-chan T { return subscription.events }
func (subscription *fakeSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	return subscription.status
}
func (subscription *fakeSubscription[T]) Errors() <-chan error { return subscription.errors }
func (subscription *fakeSubscription[T]) Close() error {
	subscription.once.Do(func() { close(subscription.closed) })
	return nil
}

func (subscription *fakeSubscription[T]) Closed() bool {
	select {
	case <-subscription.closed:
		return true
	default:
		return false
	}
}

func (generation *fakeStreamGeneration) Closed() bool {
	return generation.orders.Closed() && generation.fills.Closed() && generation.balances.Closed()
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
