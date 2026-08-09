package publicconn

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

type connectorFactoryStub struct {
	mu     sync.Mutex
	builds map[routeprofile.Key]int
	next   func(routeprofile.Key) Connector
}

func (factory *connectorFactoryStub) Build(_ context.Context, key routeprofile.Key) (Connector, error) {
	factory.mu.Lock()
	if factory.builds == nil {
		factory.builds = make(map[routeprofile.Key]int)
	}
	factory.builds[key]++
	next := factory.next
	factory.mu.Unlock()
	if next == nil {
		return &connectorStub{}, nil
	}
	return next(key), nil
}

func (factory *connectorFactoryStub) BuildCalls(key routeprofile.Key) int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.builds[key]
}

type connectorStub struct{ closed bool }

func (*connectorStub) Refresh(context.Context) ([]exchange.Instrument, error) { return nil, nil }
func (connector *connectorStub) Close() error                                 { connector.closed = true; return nil }

type refreshConnector struct {
	started     chan struct{}
	release     chan struct{}
	instruments []exchange.Instrument
	err         error
	calls       atomic.Int64
}

func (connector *refreshConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	if connector.calls.Add(1) == 1 && connector.started != nil {
		close(connector.started)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-connector.release:
	}
	if connector.err != nil {
		return nil, connector.err
	}
	out := append([]exchange.Instrument(nil), connector.instruments...)
	return out, nil
}

func (*refreshConnector) Close() error { return nil }

func TestTwoAccountsShareOnePublicConnector(t *testing.T) {
	factory := &connectorFactoryStub{}
	registry, err := NewRegistry(factory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.demo.v1"}
	one, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	two, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if one.ConnectorID() != two.ConnectorID() || factory.BuildCalls(key) != 1 {
		t.Fatalf("one=%s two=%s builds=%d", one.ConnectorID(), two.ConnectorID(), factory.BuildCalls(key))
	}
	one.Release()
	two.Release()
}

func TestRegistryCoalescesConcurrentRefreshesAndKeepsSiblingProfilesIsolated(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.demo.v1"}
	otherKey := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.spot.demo.v1"}
	connector := &refreshConnector{
		started: make(chan struct{}),
		release: make(chan struct{}),
		instruments: []exchange.Instrument{testInstrument(
			exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "BTCUSDT", "USDT",
		)},
	}
	other := &refreshConnector{
		started:     make(chan struct{}),
		release:     closedChannel(),
		err:         errors.New("refresh failed"),
		instruments: []exchange.Instrument{testInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "ETHUSDT", "")},
	}
	factory := &connectorFactoryStub{next: func(profileKey routeprofile.Key) Connector {
		switch profileKey {
		case key:
			return connector
		case otherKey:
			return other
		default:
			return &connectorStub{}
		}
	}}
	registry, err := NewRegistry(factory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	otherLease, err := registry.Acquire(context.Background(), otherKey)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	defer otherLease.Release()

	type refreshResult struct {
		snapshot catalog.Snapshot
		err      error
	}
	firstDone := make(chan refreshResult, 1)
	secondDone := make(chan refreshResult, 1)
	go func() {
		snapshot, err := registry.Refresh(context.Background(), key)
		firstDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	<-connector.started
	go func() {
		snapshot, err := registry.Refresh(context.Background(), key)
		secondDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	close(connector.release)
	first := <-firstDone
	second := <-secondDone
	if first.err != nil || second.err != nil {
		t.Fatalf("first=%v second=%v", first.err, second.err)
	}
	if connector.calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want 1", connector.calls.Load())
	}
	if factory.BuildCalls(key) != 1 {
		t.Fatalf("build calls=%d, want 1", factory.BuildCalls(key))
	}
	before, err := lease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if before.Generation == 0 {
		t.Fatal("expected published snapshot generation")
	}
	if _, err := registry.Refresh(context.Background(), otherKey); err == nil {
		t.Fatal("expected failing profile refresh error")
	}
	after, err := lease.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision || after.Generation != before.Generation {
		t.Fatalf("sibling profile stale isolation broken: before=%+v after=%+v", before, after)
	}
}

func TestRegistryRejectsUntrustedModeProfileBeforeFactory(t *testing.T) {
	factory := &connectorFactoryStub{}
	registry, _ := NewRegistry(factory, routeprofile.DefaultRegistry())
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.live.v1"}
	if _, err := registry.Acquire(context.Background(), key); err == nil || factory.BuildCalls(key) != 0 {
		t.Fatalf("err=%v builds=%d", err, factory.BuildCalls(key))
	}
}

func closedChannel() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func testInstrument(venue exchange.Venue, product exchange.Product, symbol, nativeSymbol, nativeID, settlement string) exchange.Instrument {
	quote := "USDT"
	if settlement != "" {
		quote = settlement
	}
	base := symbol
	if dash := len(symbol) - len(quote) - 1; dash > 0 && symbol[dash] == '-' {
		base = symbol[:dash]
	}
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: venue, Product: product, Symbol: symbol, BaseAsset: base, QuoteAsset: quote,
			SettleAsset: settlement, NativeSymbol: nativeSymbol, NativeID: nativeID,
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
	}
}

type scriptedConnector struct {
	mu          sync.Mutex
	refreshes   int
	closes      int
	instruments []exchange.Instrument
	err         error
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
}

func (connector *scriptedConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	connector.mu.Lock()
	connector.refreshes++
	connector.mu.Unlock()
	if connector.entered != nil {
		connector.once.Do(func() { close(connector.entered) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-connector.release:
		}
	}
	return connector.instruments, connector.err
}

func (connector *scriptedConnector) Close() error {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	connector.closes++
	return nil
}

type scriptedFactory map[routeprofile.Key]*scriptedConnector

func (factory scriptedFactory) Build(_ context.Context, key routeprofile.Key) (Connector, error) {
	connector := factory[key]
	if connector == nil {
		return nil, ErrConnectorUnavailable
	}
	return connector, nil
}

func TestConcurrentRefreshesCoalescePerProfile(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.demo.v1"}
	connector := &scriptedConnector{instruments: []exchange.Instrument{publicTestInstrument(exchange.ProductPerp, "USDT")}, entered: make(chan struct{}), release: make(chan struct{})}
	registry, _ := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	results := make(chan error, 2)
	go func() { _, err := registry.Refresh(context.Background(), key); results <- err }()
	<-connector.entered
	go func() { _, err := registry.Refresh(context.Background(), key); results <- err }()
	for {
		registry.mu.Lock()
		waiters := registry.entries[key].refreshWaiters
		registry.mu.Unlock()
		if waiters == 1 {
			break
		}
	}
	close(connector.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.refreshes != 1 {
		t.Fatalf("connector refreshes=%d want=1", connector.refreshes)
	}
}

func TestStaleConnectorAffectsOnlyItsProfile(t *testing.T) {
	spotKey := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.spot.demo.v1"}
	perpKey := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdt.demo.v1"}
	factory := scriptedFactory{
		spotKey: {err: errors.New("public canary failure")},
		perpKey: {instruments: []exchange.Instrument{publicTestInstrument(exchange.ProductPerp, "USDT")}},
	}
	registry, _ := NewRegistry(factory, routeprofile.DefaultRegistry())
	spotLease, err := registry.Acquire(context.Background(), spotKey)
	if err != nil {
		t.Fatal(err)
	}
	defer spotLease.Release()
	perpLease, err := registry.Acquire(context.Background(), perpKey)
	if err != nil {
		t.Fatal(err)
	}
	defer perpLease.Release()
	if _, err := registry.Refresh(context.Background(), spotKey); !errors.Is(err, ErrConnectorUnavailable) {
		t.Fatalf("spot refresh err=%v", err)
	}
	if snapshot, err := registry.Refresh(context.Background(), perpKey); err != nil || len(snapshot.Instruments) != 1 {
		t.Fatalf("perp snapshot=%+v err=%v", snapshot, err)
	}
}

func TestRegistryCloseClosesLeasedConnectorOnce(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.spot.demo.v1"}
	connector := &scriptedConnector{}
	registry, _ := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.closes != 1 {
		t.Fatalf("connector closes=%d want=1", connector.closes)
	}
}

func publicTestInstrument(product exchange.Product, settlement string) exchange.Instrument {
	return exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: product, Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
		SettleAsset: settlement, NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
	}, Rules: exchange.InstrumentRules{
		PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
	}}
}
