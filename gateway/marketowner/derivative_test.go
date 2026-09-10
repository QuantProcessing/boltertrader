package marketowner

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestDerivativeOwnersMergeRoutesAtNativeFamilyBoundary(t *testing.T) {
	capabilities, err := marketcapability.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	factory := &recordingDerivativeFactory{}
	owners, err := NewDerivativeOwners(factory, routeprofile.DefaultRegistry(), capabilities, staticDerivativeResolver{}, markethub.NewFactStore(nil, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owners.Close() })
	bybitOwners, bybitRoutes := 0, 0
	for _, owner := range owners.owners {
		if owner.key.Venue == exchange.VenueBybit {
			bybitOwners++
			if owner.key.NativeFamily != exchange.NativeFamilyLinear {
				t.Fatalf("Bybit owner family=%q", owner.key.NativeFamily)
			}
			bybitRoutes += len(owner.profiles)
		}
	}
	if bybitOwners != 1 || bybitRoutes != 2 {
		t.Fatalf("Bybit owners=%d routes=%d, want one owner with two projections", bybitOwners, bybitRoutes)
	}
	bybitBuilds := 0
	for _, call := range factory.calls {
		if call.profiles[0].Venue == string(exchange.VenueBybit) {
			bybitBuilds++
			if len(call.profiles) != 2 {
				t.Fatalf("Bybit physical feed projections=%d", len(call.profiles))
			}
		}
		wantCapabilities := 4
		if call.profiles[0].Venue == string(exchange.VenueBinance) ||
			call.profiles[0].Venue == string(exchange.VenueAster) ||
			call.profiles[0].Venue == string(exchange.VenueBitget) {
			wantCapabilities = 3
		}
		if len(call.capabilities) != wantCapabilities {
			t.Fatalf("%s capabilities=%d want=%d", call.profiles[0].ID, len(call.capabilities), wantCapabilities)
		}
		family := exchange.NativeMarketFamily(exchange.Venue(call.profiles[0].Venue), exchange.ProductPerp, call.profiles[0].SettlementAsset)
		for _, capability := range call.capabilities {
			if capability.NativeMarketFamily != family {
				t.Fatalf("%s received foreign capability %+v", call.profiles[0].ID, capability)
			}
			if call.profiles[0].Venue == string(exchange.VenueBitget) && capability.Dataset == exchange.DatasetOpenInterest {
				t.Fatalf("%s received single-instrument OI as a broad owner capability", call.profiles[0].ID)
			}
		}
	}
	if bybitBuilds != 1 {
		t.Fatalf("Bybit physical feed builds=%d, want 1", bybitBuilds)
	}
}

type blockingOwnerFeed struct {
	started  chan struct{}
	stopped  chan struct{}
	starts   atomic.Int64
	closes   atomic.Int64
	hot      atomic.Int64
	closeErr error
}

func (feed *blockingOwnerFeed) Bootstrap(ctx context.Context) (exchange.DerivativePatch, error) {
	feed.starts.Add(1)
	close(feed.started)
	<-ctx.Done()
	close(feed.stopped)
	return exchange.DerivativePatch{}, ctx.Err()
}
func (*blockingOwnerFeed) Stream(ctx context.Context, _ func(exchange.DerivativePatch)) error {
	return ctx.Err()
}
func (feed *blockingOwnerFeed) Close() error                  { feed.closes.Add(1); return feed.closeErr }
func (feed *blockingOwnerFeed) SetHotInstrument(string, bool) { feed.hot.Add(1) }

func TestDerivativeOwnersCloseCancelsStartupAndWaitsForRun(t *testing.T) {
	feed := &blockingOwnerFeed{started: make(chan struct{}), stopped: make(chan struct{}), closeErr: errors.New("close failed")}
	owners := &DerivativeOwners{owners: []derivativeVenueOwner{{feed: feed}}, facts: markethub.NewFactStore(nil, nil)}
	defer owners.Close()
	runDone := make(chan error, 1)
	go func() { runDone <- owners.Run(context.Background()) }()
	select {
	case <-feed.started:
	case <-time.After(time.Second):
		t.Fatal("owner never started")
	}
	if err := owners.Run(t.Context()); err == nil {
		t.Fatal("duplicate Run was accepted")
	}
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- owners.Close() }()
	}
	for range 2 {
		select {
		case err := <-closed:
			if !errors.Is(err, feed.closeErr) {
				t.Fatalf("close error=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Close did not cancel/wait for startup")
		}
	}
	select {
	case <-feed.stopped:
	default:
		t.Fatal("Close returned before native startup stopped")
	}
	if err := <-runDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error=%v", err)
	}
	owners.SetHot(hotset.Key{}, true)
	if feed.starts.Load() != 1 || feed.closes.Load() != 1 || feed.hot.Load() != 0 {
		t.Fatalf("lifecycle calls starts=%d closes=%d hot=%d", feed.starts.Load(), feed.closes.Load(), feed.hot.Load())
	}
	if err := owners.Run(t.Context()); err == nil {
		t.Fatal("closed owners restarted")
	}
}

func TestDerivativeOwnersClosedBeforeRunNeverStartsFeed(t *testing.T) {
	feed := &blockingOwnerFeed{started: make(chan struct{}), stopped: make(chan struct{})}
	owners := &DerivativeOwners{owners: []derivativeVenueOwner{{feed: feed}}}
	if err := owners.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owners.Run(t.Context()); err == nil {
		t.Fatal("Run succeeded after close")
	}
	if feed.starts.Load() != 0 || feed.closes.Load() != 1 {
		t.Fatal("closed feed was started or closed more than once")
	}
}

type derivativeFactoryCall struct {
	profiles     []routeprofile.Profile
	capabilities []exchange.MarketCapability
}

type recordingDerivativeFactory struct {
	mu    sync.Mutex
	calls []derivativeFactoryCall
}

func (factory *recordingDerivativeFactory) BuildDerivativeFeed(profiles []routeprofile.Profile, capabilities []exchange.MarketCapability, _ exchange.DerivativeInstrumentResolver) (exchange.DerivativeFeed, error) {
	factory.mu.Lock()
	factory.calls = append(factory.calls, derivativeFactoryCall{profiles: append([]routeprofile.Profile(nil), profiles...), capabilities: append([]exchange.MarketCapability(nil), capabilities...)})
	factory.mu.Unlock()
	return inertDerivativeFeed{}, nil
}

type inertDerivativeFeed struct{}

func (inertDerivativeFeed) Bootstrap(context.Context) (exchange.DerivativePatch, error) {
	return exchange.DerivativePatch{}, nil
}
func (inertDerivativeFeed) Stream(ctx context.Context, _ func(exchange.DerivativePatch)) error {
	<-ctx.Done()
	return ctx.Err()
}
func (inertDerivativeFeed) Close() error { return nil }

type staticDerivativeResolver struct{}

func (staticDerivativeResolver) ResolveDerivativeInstrument(context.Context, string, string, string) (exchange.DerivativeFact, bool) {
	return exchange.DerivativeFact{}, false
}
