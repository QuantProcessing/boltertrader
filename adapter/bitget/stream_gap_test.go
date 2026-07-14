package bitget

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

func TestPrivateReconnectHooksBridgeIntoExecutionEvents(t *testing.T) {
	ws := &fakeReconnectHooks{}
	exec := newExecutionClient(nil, newInstrumentProvider(), nil, "BITGET:test")
	adapter := &Adapter{exec: exec}
	adapter.bindPrivateGapHooks(ws)
	if ws.started == nil || ws.recovered == nil {
		t.Fatal("private reconnect hooks were not registered")
	}

	ws.started(errors.New("socket closed"))
	assertBitgetGap(t, <-exec.Events(), contract.StreamGapStarted)
	ws.recovered()
	assertBitgetGap(t, <-exec.Events(), contract.StreamGapRecovered)
}

func assertBitgetGap(t *testing.T, env contract.ExecEnvelope, phase contract.StreamGapPhase) {
	t.Helper()
	event, ok := env.Payload.(contract.StreamGapEvent)
	if !ok {
		t.Fatalf("payload=%T, want StreamGapEvent", env.Payload)
	}
	if event.Venue != VenueName || event.AccountID != "BITGET:test" || event.StreamID != "bitget:uta:private" || event.Generation != 1 || event.Phase != phase {
		t.Fatalf("gap event=%+v", event)
	}
}
