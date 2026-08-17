package publicconn

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

type connectorFactoryStub struct {
	mu     sync.Mutex
	builds map[routeprofile.Key]int
	next   func(routeprofile.Key) Connector
}

type factoryFunc func(context.Context, routeprofile.Key) (Connector, error)

func (build factoryFunc) Build(ctx context.Context, key routeprofile.Key) (Connector, error) {
	return build(ctx, key)
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

type periodicRefreshConnector struct {
	mu         sync.Mutex
	calls      int
	closes     int
	called     chan int
	recovering chan struct{}
	recover    chan struct{}
	instrument exchange.Instrument
}

func (connector *periodicRefreshConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	connector.mu.Lock()
	connector.calls++
	call := connector.calls
	connector.mu.Unlock()
	connector.called <- call
	switch call {
	case 2:
		return nil, errors.New("public refresh failed")
	case 3:
		close(connector.recovering)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-connector.recover:
		}
	}
	return []exchange.Instrument{connector.instrument}, nil
}

func (connector *periodicRefreshConnector) Close() error {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	connector.closes++
	return nil
}

type blockingCloseConnector struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingCloseConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	return nil, nil
}

func (connector *blockingCloseConnector) Close() error {
	connector.once.Do(func() { close(connector.entered) })
	<-connector.release
	return nil
}

func TestTwoAccountsShareOnePublicConnector(t *testing.T) {
	factory := &connectorFactoryStub{}
	registry, err := NewRegistry(factory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
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

func TestRegistryPeriodicRefreshMarksStaleFailureAndRecovers(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
	connector := &periodicRefreshConnector{
		called:     make(chan int, 8),
		recovering: make(chan struct{}),
		recover:    make(chan struct{}),
		instrument: publicTestInstrument(exchange.ProductPerp, "USDT"),
	}
	registry, err := newRegistry(
		factoryFunc(func(_ context.Context, candidate routeprofile.Key) (Connector, error) {
			if candidate != key {
				return nil, ErrConnectorUnavailable
			}
			return connector, nil
		}),
		routeprofile.DefaultRegistry(),
		registryOptions{refreshInterval: 10 * time.Millisecond, catalogMaxAge: 25 * time.Millisecond, now: time.Now},
	)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	if _, err := registry.Refresh(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if call := awaitRefreshCall(t, connector.called); call != 1 {
		t.Fatalf("initial refresh call=%d want=1", call)
	}
	if call := awaitRefreshCall(t, connector.called); call != 2 {
		t.Fatalf("failed periodic refresh call=%d want=2", call)
	}
	select {
	case <-connector.recovering:
	case <-time.After(time.Second):
		t.Fatal("periodic recovery refresh did not start")
	}
	deadline := time.Now().Add(time.Second)
	for !lease.Status().Stale && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	failed := lease.Status()
	if !failed.Stale || failed.Reason != catalog.ReasonCatalogStale || failed.LastError != catalog.ReasonRefreshFailed {
		t.Fatalf("failed periodic refresh status=%+v", failed)
	}

	close(connector.recover)
	deadline = time.Now().Add(time.Second)
	for lease.Status().Generation < 2 && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	recovered := lease.Status()
	if recovered.Stale || !recovered.ReadReady || recovered.Generation != 2 || recovered.Reason != "" || recovered.LastError != "" {
		t.Fatalf("recovered periodic refresh status=%+v", recovered)
	}
}

func TestRegistrySharesAndCancelsOnePeriodicRefreshLifecyclePerEntry(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
	connector := &periodicRefreshConnector{
		called:     make(chan int, 8),
		recovering: make(chan struct{}),
		recover:    make(chan struct{}),
		instrument: publicTestInstrument(exchange.ProductPerp, "USDT"),
	}
	factory := &connectorFactoryStub{next: func(routeprofile.Key) Connector { return connector }}
	registry, err := newRegistry(
		factory,
		routeprofile.DefaultRegistry(),
		registryOptions{refreshInterval: 10 * time.Millisecond, catalogMaxAge: time.Minute, now: time.Now},
	)
	if err != nil {
		t.Fatal(err)
	}
	one, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	two, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if call := awaitRefreshCall(t, connector.called); call != 1 {
		t.Fatalf("first periodic refresh call=%d want=1", call)
	}
	one.Release()
	if call := awaitRefreshCall(t, connector.called); call != 2 {
		t.Fatalf("periodic refresh after non-final release call=%d want=2", call)
	}
	if call := awaitRefreshCall(t, connector.called); call != 3 {
		t.Fatalf("blocking periodic refresh call=%d want=3", call)
	}
	select {
	case <-connector.recovering:
	case <-time.After(time.Second):
		t.Fatal("blocking periodic refresh did not start")
	}
	two.Release()

	connector.mu.Lock()
	callsAfterRelease := connector.calls
	closes := connector.closes
	connector.mu.Unlock()
	if callsAfterRelease != 3 || closes != 1 || factory.BuildCalls(key) != 1 {
		t.Fatalf("calls=%d closes=%d builds=%d", callsAfterRelease, closes, factory.BuildCalls(key))
	}
	select {
	case call := <-connector.called:
		t.Fatalf("periodic refresh continued after final release: call=%d", call)
	case <-time.After(30 * time.Millisecond):
	}
}

func awaitRefreshCall(t *testing.T, calls <-chan int) int {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for connector refresh")
		return 0
	}
}

func TestAcquireWaitsForFinalReleaseCleanupBeforeBuildingReplacement(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	closing := &blockingCloseConnector{entered: make(chan struct{}), release: make(chan struct{})}
	builtFirst := false
	factory := &connectorFactoryStub{next: func(routeprofile.Key) Connector {
		if !builtFirst {
			builtFirst = true
			return closing
		}
		return &connectorStub{}
	}}
	registry, err := NewRegistry(factory, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	first, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		first.Release()
		close(released)
	}()
	select {
	case <-closing.entered:
	case <-time.After(time.Second):
		t.Fatal("final release did not begin connector close")
	}

	acquired := make(chan Lease, 1)
	acquireErr := make(chan error, 1)
	go func() {
		lease, err := registry.Acquire(context.Background(), key)
		acquired <- lease
		acquireErr <- err
	}()
	select {
	case lease := <-acquired:
		if lease != nil {
			lease.Release()
		}
		t.Fatal("replacement acquired before prior connector closed")
	case <-time.After(20 * time.Millisecond):
	}
	if builds := factory.BuildCalls(key); builds != 1 {
		t.Fatalf("factory builds during close=%d want=1", builds)
	}

	close(closing.release)
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("final release did not complete")
	}
	var replacement Lease
	select {
	case replacement = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("replacement acquire did not resume")
	}
	if err := <-acquireErr; err != nil || replacement == nil {
		t.Fatalf("replacement lease=%v err=%v", replacement, err)
	}
	if builds := factory.BuildCalls(key); builds != 2 {
		t.Fatalf("factory builds after close=%d want=2", builds)
	}
	replacement.Release()
}

func TestLeaseResolvesCanonicalInstrumentFromSharedSnapshot(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
	canonical := publicTestInstrument(exchange.ProductPerp, "USDT")
	connector := &scriptedConnector{instruments: []exchange.Instrument{canonical}}
	registry, err := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()

	snapshot, err := registry.Refresh(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := lease.Resolve(context.Background(), domain.InstrumentID(snapshot.Instruments[0].ID))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Identity != canonical.Identity {
		t.Fatalf("resolved identity=%+v want=%+v", resolved.Identity, canonical.Identity)
	}
}

func TestRegistryCoalescesConcurrentRefreshesAndKeepsSiblingProfilesIsolated(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
	otherKey := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
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
	deadline := time.Now().Add(time.Second)
	joined := false
	for time.Now().Before(deadline) {
		registry.mu.Lock()
		waiters := registry.entries[key].refreshWaiters
		registry.mu.Unlock()
		if waiters == 1 {
			joined = true
			break
		}
		runtime.Gosched()
	}
	close(connector.release)
	if !joined {
		t.Fatal("second refresh did not join the in-flight refresh")
	}
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

// TestRegistryRejectsUntrustedProfileBeforeFactory pins the ordering that makes
// the trusted registry worth consulting: a key it does not carry must be refused
// before the factory is asked to build anything.
//
// The key below was once a test-mode key naming a `.live.` profile id — an
// untrusted mode/profile pairing. Modes are gone, so the untrusted thing is now
// simply a profile id the registry has no row for. Either way the factory must
// see zero build calls, because reaching it means an unvetted key decided which
// endpoints get dialed.
func TestRegistryRejectsUntrustedProfileBeforeFactory(t *testing.T) {
	factory := &connectorFactoryStub{}
	registry, _ := NewRegistry(factory, routeprofile.DefaultRegistry())
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdc.v1"}
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
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
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
	spotKey := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	perpKey := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdt.v1"}
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
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
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

func TestRegistryRejectsAcquireAndRefreshAfterClose(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	connector := &scriptedConnector{}
	factory := scriptedFactory{key: connector}
	registry, _ := NewRegistry(factory, routeprofile.DefaultRegistry())
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatal(err)
	}
	lease.Release()
	if _, err := registry.Acquire(context.Background(), key); !errors.Is(err, ErrConnectorUnavailable) {
		t.Fatalf("Acquire after Close error=%v", err)
	}
	if _, err := registry.Refresh(context.Background(), key); !errors.Is(err, ErrConnectorUnavailable) {
		t.Fatalf("Refresh after Close error=%v", err)
	}
}

func TestLastLeaseReleaseWaitsForInflightRefreshBeforeClosingConnector(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	connector := &scriptedConnector{
		entered: make(chan struct{}), release: make(chan struct{}),
		instruments: []exchange.Instrument{publicTestInstrument(exchange.ProductSpot, "")},
	}
	registry, _ := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := registry.Refresh(context.Background(), key)
		refreshDone <- refreshErr
	}()
	<-connector.entered

	releaseDone := make(chan struct{})
	go func() {
		lease.Release()
		close(releaseDone)
	}()
	assertStillRefreshingAndOpen(t, connector, releaseDone)
	close(connector.release)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	<-releaseDone
	assertConnectorCloses(t, connector, 1)
}

func TestRegistryCloseWaitsForInflightRefreshAndIsIdempotent(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	connector := &scriptedConnector{
		entered: make(chan struct{}), release: make(chan struct{}),
		instruments: []exchange.Instrument{publicTestInstrument(exchange.ProductSpot, "")},
	}
	registry, _ := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	if _, err := registry.Acquire(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := registry.Refresh(context.Background(), key)
		refreshDone <- refreshErr
	}()
	<-connector.entered

	closeDone := make(chan error, 1)
	go func() { closeDone <- registry.Close() }()
	var registryCloseDone <-chan struct{}
	for registryCloseDone == nil {
		registry.mu.Lock()
		if registry.closed {
			registryCloseDone = registry.closeDone
		}
		registry.mu.Unlock()
	}
	select {
	case <-registryCloseDone:
		t.Fatal("registry close completed before in-flight refresh")
	default:
	}
	assertConnectorCloses(t, connector, 0)
	close(connector.release)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	assertConnectorCloses(t, connector, 1)
}

func TestRegistryCloseJoinsCleanupAlreadyOwnedByLastRelease(t *testing.T) {
	key := routeprofile.Key{Venue: "binance", RouteProfileID: "binance.spot.v1"}
	connector := &scriptedConnector{
		entered: make(chan struct{}), release: make(chan struct{}),
		instruments: []exchange.Instrument{publicTestInstrument(exchange.ProductSpot, "")},
	}
	registry, _ := NewRegistry(scriptedFactory{key: connector}, routeprofile.DefaultRegistry())
	lease, err := registry.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := registry.Refresh(context.Background(), key)
		refreshDone <- refreshErr
	}()
	<-connector.entered
	releaseDone := make(chan struct{})
	go func() {
		lease.Release()
		close(releaseDone)
	}()
	for {
		registry.mu.Lock()
		current := registry.entries[key]
		closing := current != nil && current.closing
		registry.mu.Unlock()
		if closing {
			break
		}
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- registry.Close() }()
	var registryCloseDone <-chan struct{}
	for registryCloseDone == nil {
		registry.mu.Lock()
		if registry.closed {
			registryCloseDone = registry.closeDone
		}
		registry.mu.Unlock()
	}
	select {
	case <-registryCloseDone:
		t.Fatal("registry close completed before detached release cleanup")
	default:
	}
	assertConnectorCloses(t, connector, 0)
	close(connector.release)
	if err := <-refreshDone; err != nil {
		t.Fatal(err)
	}
	<-releaseDone
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	assertConnectorCloses(t, connector, 1)
}

func assertStillRefreshingAndOpen[T any](t *testing.T, connector *scriptedConnector, done <-chan T) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("cleanup completed before refresh")
	default:
	}
	assertConnectorCloses(t, connector, 0)
}

func assertConnectorCloses(t *testing.T, connector *scriptedConnector, want int) {
	t.Helper()
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if connector.closes != want {
		t.Fatalf("connector closes=%d want=%d", connector.closes, want)
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
