package streamgap

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

func TestReporterPairsGenerationsAndIgnoresDuplicates(t *testing.T) {
	var events []contract.StreamGapEvent
	reporter := New("GATE", "GATE:unified", "gate:private:spot", func(env contract.ExecEnvelope) bool {
		event, ok := env.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("payload=%T, want StreamGapEvent", env.Payload)
		}
		events = append(events, event)
		return true
	})

	reporter.Recovered("initial connect")
	reporter.Started("socket closed")
	reporter.Started("duplicate close")
	reporter.Recovered("subscriptions restored")
	reporter.Recovered("duplicate recovery")
	reporter.Started("socket closed again")
	reporter.Recovered("subscriptions restored again")

	if len(events) != 4 {
		t.Fatalf("events=%+v, want two paired generations", events)
	}
	wantPhases := []contract.StreamGapPhase{
		contract.StreamGapStarted,
		contract.StreamGapRecovered,
		contract.StreamGapStarted,
		contract.StreamGapRecovered,
	}
	for i, event := range events {
		wantGeneration := uint64(i/2 + 1)
		if event.Venue != "GATE" || event.AccountID != "GATE:unified" || event.StreamID != "gate:private:spot" ||
			event.Generation != wantGeneration || event.Phase != wantPhases[i] {
			t.Fatalf("event[%d]=%+v, want generation=%d phase=%s", i, event, wantGeneration, wantPhases[i])
		}
	}
}

func TestReporterRetainsActiveGapWhenEmitFails(t *testing.T) {
	var attempts int
	reporter := New("T", "acct", "private", func(contract.ExecEnvelope) bool {
		attempts++
		return attempts > 1
	})

	if reporter.Started("closed") {
		t.Fatal("failed started emission reported success")
	}
	if !reporter.Started("retry") {
		t.Fatal("started emission retry did not succeed")
	}
	if !reporter.Recovered("restored") {
		t.Fatal("recovered emission did not succeed")
	}
}
