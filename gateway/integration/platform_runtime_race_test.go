package integration_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/order"
)

func TestPlatformRuntimeConcurrentReadsObserveWholeGenerations(t *testing.T) {
	harness := newPlatformHarness(t, account.RuntimeModeTest)
	first := platformSnapshot(t, "race-first", "account-race", 1, 1, rawOpaqueRef, spotRoute(), perpRoute())
	harness.promote(t, first)
	router, err := order.NewAccountRouter(harness.manager)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsFound := make(chan error, 64)
	var readers sync.WaitGroup
	for index := 0; index < 8; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for iteration := 0; iteration < 300; iteration++ {
				active := harness.manager.ActiveSnapshot(first.AccountID)
				if active == nil || len(active.Routes) != 2 {
					errorsFound <- fmt.Errorf("partial active snapshot: %+v", active)
					return
				}
				if active.Fence.RuntimeGeneration != 1 && active.Fence.RuntimeGeneration != 2 {
					errorsFound <- fmt.Errorf("unexpected generation: %+v", active.Fence)
					return
				}
				if _, ok := active.Routes[domain.RouteID("spot")]; !ok {
					errorsFound <- fmt.Errorf("spot route absent: %+v", active.Routes)
					return
				}
				if _, ok := active.Routes[domain.RouteID("perp-usdt")]; !ok {
					errorsFound <- fmt.Errorf("perp route absent: %+v", active.Routes)
					return
				}
				if _, err := router.Route(t.Context(), first.AccountID, "binance:live:spot:BTC-USDT"); err != nil {
					errorsFound <- fmt.Errorf("route during promotion: %w", err)
					return
				}
				_ = harness.manager.AccountIDs()
				_ = harness.manager.Runtimes()
			}
		}()
	}

	close(start)
	second := platformSnapshot(t, "race-second", first.AccountID, 2, 2, alternateOpaqueRef().RawForTrustedBoundary(), spotRoute(), perpRoute())
	harness.promote(t, second)
	readers.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if final := harness.manager.ActiveSnapshot(first.AccountID); final == nil || final.Fence != second.Fence || len(final.Routes) != 2 {
		t.Fatalf("final active=%+v", final)
	}
}
