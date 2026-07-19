package factoryclient

import (
	"errors"
	"reflect"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestBackendLifecyclePairsGapRecoveryAndMarksResync(t *testing.T) {
	var statuses []backendStatus
	var resyncs int
	lifecycle := newBackendLifecycle()
	remove := lifecycle.Register("book:BTC", func(status backendStatus) {
		statuses = append(statuses, status)
	}, func() {
		resyncs++
	})
	lifecycle.Started(errors.New("socket closed"))
	lifecycle.Started(errors.New("duplicate"))
	lifecycle.Recovered("subscriptions confirmed")
	lifecycle.Recovered("duplicate")
	remove()
	lifecycle.Started(errors.New("after remove"))

	gotStates := make([]exchange.SubscriptionState, 0, len(statuses))
	gotPhases := make([]exchange.GapPhase, 0, len(statuses))
	for _, status := range statuses {
		gotStates = append(gotStates, status.State)
		gotPhases = append(gotPhases, status.Phase)
		if status.Generation != 1 {
			t.Fatalf("generation = %d, want 1", status.Generation)
		}
	}
	if !reflect.DeepEqual(gotStates, []exchange.SubscriptionState{
		exchange.SubscriptionGap,
		exchange.SubscriptionResyncing,
		exchange.SubscriptionActive,
	}) {
		t.Fatalf("states = %v", gotStates)
	}
	if !reflect.DeepEqual(gotPhases, []exchange.GapPhase{
		exchange.GapStarted,
		"",
		exchange.GapRecovered,
	}) {
		t.Fatalf("phases = %v", gotPhases)
	}
	if resyncs != 1 {
		t.Fatalf("resync marks = %d, want 1", resyncs)
	}
}

func TestBackendLifecycleCanSynthesizeLateReconnectBoundary(t *testing.T) {
	var statuses []backendStatus
	lifecycle := newBackendLifecycle()
	lifecycle.Register("trades:BTC", func(status backendStatus) {
		statuses = append(statuses, status)
	}, nil)
	lifecycle.SynthesizedRecovery("transport reconnected")
	if len(statuses) != 3 ||
		statuses[0].Phase != exchange.GapStarted ||
		statuses[2].Phase != exchange.GapRecovered {
		t.Fatalf("statuses = %+v", statuses)
	}
}
