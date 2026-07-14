package gate

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

type fakeReconnectHooks struct {
	started   func(error)
	recovered func()
}

func (f *fakeReconnectHooks) SetReconnectHooks(started func(error), recovered func()) {
	f.started = started
	f.recovered = recovered
}

func TestSpotAndFuturesPrivateGapsKeepIndependentIdentities(t *testing.T) {
	spot := &fakeReconnectHooks{}
	futures := &fakeReconnectHooks{}
	exec := newExecutionClient(nil, newInstrumentProvider(), nil, "GATE:test")
	adapter := &Adapter{exec: exec}
	adapter.bindPrivateGapHooks(spot, futures)
	if spot.started == nil || spot.recovered == nil || futures.started == nil || futures.recovered == nil {
		t.Fatal("spot and futures reconnect hooks were not registered")
	}

	spot.started(errors.New("spot socket closed"))
	futures.started(errors.New("futures socket closed"))
	spot.recovered()
	futures.recovered()

	want := []struct {
		streamID string
		phase    contract.StreamGapPhase
	}{
		{"gate:spot:private", contract.StreamGapStarted},
		{"gate:futures:private", contract.StreamGapStarted},
		{"gate:spot:private", contract.StreamGapRecovered},
		{"gate:futures:private", contract.StreamGapRecovered},
	}
	for _, expected := range want {
		env := <-exec.Events()
		event, ok := env.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("payload=%T, want StreamGapEvent", env.Payload)
		}
		if event.Venue != VenueName || event.AccountID != "GATE:test" || event.StreamID != expected.streamID || event.Generation != 1 || event.Phase != expected.phase {
			t.Fatalf("gap event=%+v, want stream=%s phase=%s generation=1", event, expected.streamID, expected.phase)
		}
	}
}
