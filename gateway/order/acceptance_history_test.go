package order

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

func TestConfirmedOrderRemainsUncertainAfterRepeatedAbsenceAndRestart(t *testing.T) {
	for _, keepVenueID := range []bool{true, false} {
		t.Run(map[bool]string{true: "venue_identity", false: "acceptance_history_only"}[keepVenueID], func(t *testing.T) {
			f := newCoordinatorFixture(t)
			ctx := context.Background()
			if _, err := f.coordinator.RecoverAccount(ctx, f.accountID); err != nil {
				t.Fatal(err)
			}
			placed, err := f.coordinator.Place(ctx, f.placeRequest())
			if err != nil || placed.State != domain.StateOpen {
				t.Fatalf("place=%+v err=%v", placed, err)
			}
			f.driver.mu.Lock()
			f.driver.orders = nil
			f.driver.lookupFallback = notFoundExactResult(f.instrument, placed.ClientOrderID)
			f.driver.mu.Unlock()
			if !keepVenueID {
				f.store.mu.Lock()
				f.store.byCommand[placed.CommandID].command.VenueOrderID = ""
				f.store.mu.Unlock()
			}
			for range 3 {
				restarted := newTestCoordinator(t, f.store, f.router, f.publisher, Hooks{})
				report, err := restarted.RecoverAccount(ctx, f.accountID)
				if !errors.Is(err, ErrRecoveryIncomplete) || report.WriteReady {
					t.Fatalf("absence unlocked account: %+v err=%v", report, err)
				}
				command := f.store.onlyCommand(t)
				if command.State != domain.StateUncertain || !command.AcceptanceObserved {
					t.Fatalf("acceptance lost: %+v", command)
				}
				if f.store.taskCount() != 1 {
					t.Fatal("uncertain order lost its reconciliation task")
				}
			}
			if f.driver.PlaceCalls() != 1 {
				t.Fatal("reconciliation repeated placement")
			}
		})
	}
}
