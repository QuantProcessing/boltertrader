package account

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/market"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// Account channel names pushed on the "account" topic.
const (
	ChannelOrders    = "account.orders"
	ChannelFills     = "account.fills"
	ChannelPositions = "account.positions"
	ChannelBalances  = "account.balances"
	ChannelStatus    = "account.status"
)

const (
	// refreshDelay coalesces bursts of fills into one REST position read.
	refreshDelay = 300 * time.Millisecond
	// resyncInterval repairs anything a private stream missed.
	resyncInterval = 60 * time.Second
)

// runtime is the live state of one account: its clients and an in-memory
// position/balance snapshot fed by REST and the private stream.
type runtime struct {
	id      string
	venue   exchange.Venue
	clients []routeClient
	catalog *market.Catalog

	mu        sync.Mutex
	positions map[venue.Market][]Position
	balances  map[venue.Market][]Balance
	loaded    map[venue.Market]bool
	connected map[venue.Market]bool
	emit      hub.Emit
	refresh   chan venue.Market
}

func newRuntime(id string, venueName exchange.Venue, clients []routeClient, catalog *market.Catalog) *runtime {
	return &runtime{
		id: id, venue: venueName, clients: clients, catalog: catalog,
		positions: map[venue.Market][]Position{}, balances: map[venue.Market][]Balance{},
		loaded: map[venue.Market]bool{}, connected: map[venue.Market]bool{},
		refresh: make(chan venue.Market, 16),
	}
}

func (rt *runtime) close() {
	for _, client := range rt.clients {
		client.close()
	}
}

func (rt *runtime) client(market venue.Market) (routeClient, bool) {
	for _, client := range rt.clients {
		if client.market == market {
			return client, true
		}
	}
	return routeClient{}, false
}

func (rt *runtime) clientFor(instrument market.Instrument) (routeClient, error) {
	owner, ok := venue.MarketOf(instrument.Native())
	if !ok || owner.Venue != rt.venue {
		return routeClient{}, fmt.Errorf("%w: instrument %s does not belong to this account", ErrInvalidRequest, instrument.ID)
	}
	client, ok := rt.client(owner)
	if !ok {
		return routeClient{}, fmt.Errorf("%w: market %s is not available for this account", ErrInvalidRequest, owner)
	}
	return client, nil
}

// ---------------------------------------------------------------------------
// Snapshot reads
// ---------------------------------------------------------------------------

// Positions returns the current snapshot. While a private stream keeps the
// snapshot live it is served from memory; otherwise each call reads the venue.
func (rt *runtime) Positions(ctx context.Context) ([]Position, error) {
	var errs []error
	for _, client := range rt.clients {
		if client.perp == nil {
			continue
		}
		rt.mu.Lock()
		fresh := rt.loaded[client.market] && rt.emit != nil && rt.connected[client.market]
		rt.mu.Unlock()
		if !fresh {
			if err := rt.loadMarket(ctx, client); err != nil {
				errs = append(errs, err)
			}
		}
	}
	positions := rt.positionList()
	if len(positions) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return positions, nil
}

func (rt *runtime) positionList() []Position {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	var result []Position
	for _, client := range rt.clients {
		result = append(result, rt.positions[client.market]...)
	}
	return result
}

// Balances reads balances fresh from every market.
func (rt *runtime) Balances(ctx context.Context) ([]Balance, error) {
	var result []Balance
	var errs []error
	for _, client := range rt.clients {
		balances, err := rt.fetchBalances(ctx, client)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", client.market, err))
			continue
		}
		result = append(result, balances...)
	}
	if len(result) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return result, nil
}

func (rt *runtime) fetchBalances(ctx context.Context, client routeClient) ([]Balance, error) {
	var natives []exchange.Balance
	var err error
	if client.perp != nil {
		natives, err = client.perp.Balances(ctx)
	} else {
		natives, err = client.spot.Balances(ctx)
	}
	if err != nil {
		return nil, err
	}
	balances := make([]Balance, 0, len(natives))
	for _, item := range natives {
		if item.Total.IsZero() && item.Available.IsZero() && item.Locked.IsZero() {
			continue
		}
		balances = append(balances, Balance{Market: client.market.String(), Asset: item.Asset,
			Total: item.Total.String(), Available: item.Available.String(), Locked: item.Locked.String()})
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
	rt.mu.Lock()
	rt.balances[client.market] = balances
	rt.mu.Unlock()
	return balances, nil
}

func (rt *runtime) fetchPositions(ctx context.Context, client routeClient) ([]Position, error) {
	natives, err := client.perp.Positions(ctx, exchange.PositionsRequest{})
	if err != nil {
		return nil, err
	}
	positions := make([]Position, 0, len(natives))
	for _, native := range natives {
		instrument, ok := rt.catalog.ResolveNative(client.market, native.NativeSymbol, native.NativeID)
		if !ok {
			slog.Warn("position on unknown instrument skipped", "account", rt.id, "native", native.NativeSymbol)
			continue
		}
		if position, ok := projectPosition(instrument.ID, native); ok {
			positions = append(positions, position)
		}
	}
	sortPositions(positions)
	return positions, nil
}

func sortPositions(positions []Position) {
	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Instrument != positions[j].Instrument {
			return positions[i].Instrument < positions[j].Instrument
		}
		return positions[i].Side < positions[j].Side
	})
}

// loadMarket replaces the market's positions with a REST read and publishes.
func (rt *runtime) loadMarket(ctx context.Context, client routeClient) error {
	if client.perp == nil {
		return nil
	}
	positions, err := rt.fetchPositions(ctx, client)
	if err != nil {
		return err
	}
	rt.mu.Lock()
	rt.positions[client.market] = positions
	rt.loaded[client.market] = true
	rt.mu.Unlock()
	rt.publishPositions()
	return nil
}

func (rt *runtime) publishPositions() {
	rt.mu.Lock()
	emit := rt.emit
	rt.mu.Unlock()
	if emit == nil {
		return
	}
	positions := rt.positionList()
	if positions == nil {
		positions = []Position{}
	}
	emit(hub.Message{Channel: ChannelPositions, Account: rt.id, Data: positions}, true)
}

func (rt *runtime) publishBalances() {
	rt.mu.Lock()
	emit := rt.emit
	var balances []Balance
	for _, client := range rt.clients {
		balances = append(balances, rt.balances[client.market]...)
	}
	rt.mu.Unlock()
	if emit == nil {
		return
	}
	if balances == nil {
		balances = []Balance{}
	}
	emit(hub.Message{Channel: ChannelBalances, Account: rt.id, Data: balances}, true)
}

func (rt *runtime) publishStatus() {
	rt.mu.Lock()
	emit := rt.emit
	markets := map[string]bool{}
	connected := len(rt.clients) > 0
	for _, client := range rt.clients {
		markets[client.market.String()] = rt.connected[client.market]
		connected = connected && rt.connected[client.market]
	}
	rt.mu.Unlock()
	if emit != nil {
		emit(hub.Message{Channel: ChannelStatus, Account: rt.id, Data: map[string]any{"connected": connected, "markets": markets}}, true)
	}
}

// scheduleRefresh asks the stream loop to re-read positions for a market.
func (rt *runtime) scheduleRefresh(market venue.Market) {
	select {
	case rt.refresh <- market:
	default:
	}
}

// ---------------------------------------------------------------------------
// Private stream (hub source for the "account" topic)
// ---------------------------------------------------------------------------

// stream runs every market's private stream and keeps the snapshot fresh.
// It returns when ctx ends or any market's stream fails (the hub retries).
func (rt *runtime) stream(ctx context.Context, emit hub.Emit) error {
	rt.mu.Lock()
	rt.emit = emit
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		rt.emit = nil
		for market := range rt.connected {
			rt.connected[market] = false
		}
		rt.mu.Unlock()
	}()

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	failures := make(chan error, len(rt.clients))
	for _, client := range rt.clients {
		go func(client routeClient) {
			if err := rt.streamMarket(streamCtx, client); err != nil && streamCtx.Err() == nil {
				failures <- fmt.Errorf("%s: %w", client.market, err)
			}
		}(client)
	}

	// Initial REST snapshot so subscribers see state immediately.
	rt.resync(ctx)

	pending := map[venue.Market]bool{}
	var timer <-chan time.Time
	resync := time.NewTicker(resyncInterval)
	defer resync.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-failures:
			return err
		case market := <-rt.refresh:
			pending[market] = true
			if timer == nil {
				timer = time.After(refreshDelay)
			}
		case <-timer:
			timer = nil
			for market := range pending {
				if client, ok := rt.client(market); ok {
					if err := rt.loadMarket(ctx, client); err != nil {
						slog.Warn("position refresh failed", "account", rt.id, "market", market.String(), "error", err)
					}
					if _, err := rt.fetchBalances(ctx, client); err == nil {
						rt.publishBalances()
					}
				}
			}
			pending = map[venue.Market]bool{}
		case <-resync.C:
			rt.resync(ctx)
		}
	}
}

func (rt *runtime) resync(ctx context.Context) {
	for _, client := range rt.clients {
		if err := rt.loadMarket(ctx, client); err != nil {
			slog.Warn("position snapshot failed", "account", rt.id, "market", client.market.String(), "error", err)
		}
		if _, err := rt.fetchBalances(ctx, client); err != nil {
			slog.Warn("balance snapshot failed", "account", rt.id, "market", client.market.String(), "error", err)
		}
	}
	rt.publishPositions()
	rt.publishBalances()
	rt.publishStatus()
}

func (rt *runtime) streamMarket(ctx context.Context, client routeClient) error {
	socket, ok := client.socket().(exchange.AccountWebSocket)
	if !ok {
		return fmt.Errorf("private account stream unsupported")
	}
	instruments := rt.catalog.MarketInstruments(client.market)
	if len(instruments) == 0 {
		return fmt.Errorf("instrument catalog for %s is empty", client.market)
	}
	if indexer, ok := client.socket().(exchange.AccountInstrumentIndexer); ok {
		items := make([]exchange.AccountInstrument, 0, len(instruments))
		for _, instrument := range instruments {
			items = append(items, exchange.AccountInstrument{InstrumentID: string(instrument.ID), Instrument: instrument.Native()})
		}
		if err := indexer.SetAccountInstruments(items); err != nil {
			return err
		}
	}
	identity := instruments[0].Native().Identity
	subscription, err := socket.WatchAccountEvents(ctx, exchange.WatchAccountEventsRequest{
		Route: exchange.AccountRoute{
			AccountID: rt.id, RouteID: client.market.String(), Venue: client.market.Venue,
			Environment: exchange.EnvironmentLive, Product: client.market.Product, Settlement: client.market.Settlement,
			NativeFamily: exchange.NativeMarketFamily(identity.Venue, identity.Product, client.market.Settlement),
		},
		Options: exchange.WatchOptions{Buffer: 1024},
	})
	if err != nil {
		return err
	}
	defer subscription.Close()

	setConnected := func(connected bool) {
		rt.mu.Lock()
		changed := rt.connected[client.market] != connected
		rt.connected[client.market] = connected
		rt.mu.Unlock()
		if changed {
			rt.publishStatus()
		}
	}
	events, failures, statuses := subscription.Events(), subscription.Errors(), subscription.Status()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			if err != nil {
				setConnected(false)
				return err
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			switch status.State {
			case exchange.SubscriptionActive:
				setConnected(true)
				// A (re)connect may have missed events: refresh.
				rt.scheduleRefresh(client.market)
			case exchange.SubscriptionClosed:
				setConnected(false)
				return fmt.Errorf("private stream closed")
			default:
				setConnected(false)
			}
		case event, open := <-events:
			if !open {
				setConnected(false)
				return fmt.Errorf("private stream ended")
			}
			rt.handleEvent(client, event)
		}
	}
}

func (rt *runtime) handleEvent(client routeClient, event exchange.AccountEvent) {
	rt.mu.Lock()
	emit := rt.emit
	rt.mu.Unlock()
	id := venue.InstrumentID(event.InstrumentID)
	switch event.Kind {
	case exchange.AccountEventOrder:
		if event.Order != nil && emit != nil {
			emit(hub.Message{Channel: ChannelOrders, Account: rt.id, Data: projectOrder(id, event.Order.Order)}, false)
		}
	case exchange.AccountEventFill:
		if event.Fill != nil && emit != nil {
			emit(hub.Message{Channel: ChannelFills, Account: rt.id, Data: projectFill(id, event.Fill.Fill)}, false)
		}
		// Not every venue pushes positions; a fill always changes them.
		rt.scheduleRefresh(client.market)
	case exchange.AccountEventPosition:
		if event.Position != nil {
			rt.applyPositionEvent(client.market, id, *event.Position)
		}
	case exchange.AccountEventBalance:
		if event.Balance != nil {
			rt.applyBalanceEvent(client.market, *event.Balance)
		}
	}
}

// applyPositionEvent updates one instrument's rows. A snapshot event replaces
// all rows of that instrument; a delta event replaces only the slots present.
func (rt *runtime) applyPositionEvent(market venue.Market, id venue.InstrumentID, event exchange.PositionEvent) {
	rt.mu.Lock()
	current := rt.positions[market]
	kept := current[:0:0]
	incomingSlots := map[exchange.PositionSlot]bool{}
	for _, native := range event.Positions {
		incomingSlots[native.PositionSlot] = true
	}
	for _, position := range current {
		if position.Instrument != id {
			kept = append(kept, position)
			continue
		}
		if event.Kind != exchange.EventSnapshot && !incomingSlots[position.slot] {
			kept = append(kept, position)
		}
	}
	for _, native := range event.Positions {
		if position, ok := projectPosition(id, native); ok {
			kept = append(kept, position)
		}
	}
	sortPositions(kept)
	rt.positions[market] = kept
	rt.loaded[market] = true
	rt.mu.Unlock()
	rt.publishPositions()
}

func (rt *runtime) applyBalanceEvent(market venue.Market, event exchange.BalanceEvent) {
	rt.mu.Lock()
	byAsset := map[string]Balance{}
	for _, balance := range rt.balances[market] {
		byAsset[balance.Asset] = balance
	}
	for _, item := range event.Balances {
		if item.Total.IsZero() && item.Available.IsZero() && item.Locked.IsZero() {
			delete(byAsset, item.Asset)
			continue
		}
		byAsset[item.Asset] = Balance{Market: market.String(), Asset: item.Asset, Total: item.Total.String(), Available: item.Available.String(), Locked: item.Locked.String()}
	}
	balances := make([]Balance, 0, len(byAsset))
	for _, balance := range byAsset {
		balances = append(balances, balance)
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
	rt.balances[market] = balances
	rt.mu.Unlock()
	rt.publishBalances()
}
