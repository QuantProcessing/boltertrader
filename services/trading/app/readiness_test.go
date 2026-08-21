package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
)

type readinessCatalogSource struct{}

func (readinessCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{Revision: "test", ObservedAt: time.Now().UTC()}, nil
}

type deadlineProbe struct {
	mu         sync.Mutex
	marketTTL  time.Duration
	runtimeTTL time.Duration
}

func (probe *deadlineProbe) CheckMarketDataRoute(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	probe.mu.Lock()
	probe.marketTTL = time.Until(deadline)
	probe.mu.Unlock()
	return nil
}

func (probe *deadlineProbe) CheckRuntimeRoute(ctx context.Context) error {
	deadline, _ := ctx.Deadline()
	probe.mu.Lock()
	probe.runtimeTTL = time.Until(deadline)
	probe.mu.Unlock()
	return nil
}

func TestReadinessBoundsInternalRouteProbesBelowHostDeadline(t *testing.T) {
	projection, err := catalog.NewProjection(readinessCatalogSource{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := projection.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	probe := &deadlineProbe{}
	readiness := newReadiness(Dependencies{MarketDataProbe: probe, RuntimeProbe: probe}, projection)
	status := readiness.Status()
	if !status.Components["publicData"].Ready || !status.Components["accounts"].Ready {
		t.Fatalf("readiness probes = %#v", status.Components)
	}
	probe.mu.Lock()
	defer probe.mu.Unlock()
	for name, ttl := range map[string]time.Duration{"market data": probe.marketTTL, "runtime": probe.runtimeTTL} {
		if ttl < 2*time.Second || ttl > readinessProbeTimeout {
			t.Fatalf("%s probe deadline = %v, want %v budget", name, ttl, readinessProbeTimeout)
		}
	}
}
