package nado

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

type reconnectAwareAccountStream struct {
	connected bool
	started   func(error)
	recovered func()
	hookCalls int
}

func (s *reconnectAwareAccountStream) Connect() error { s.connected = true; return nil }
func (s *reconnectAwareAccountStream) Close()         { s.connected = false }
func (s *reconnectAwareAccountStream) IsConnected() bool {
	return s.connected
}
func (*reconnectAwareAccountStream) SubscribeOrders(*int64, func(*sdk.OrderUpdate)) error {
	return nil
}
func (*reconnectAwareAccountStream) SubscribeFills(*int64, func(*sdk.Fill)) error {
	return nil
}
func (*reconnectAwareAccountStream) SubscribePositions(*int64, func(*sdk.PositionChange)) error {
	return nil
}
func (s *reconnectAwareAccountStream) SetReconnectHooks(started func(error), recovered func()) {
	s.hookCalls++
	s.started = started
	s.recovered = recovered
}

func TestNadoSharedPrivateStreamEmitsOneGapPair(t *testing.T) {
	provider := nadoTestProvider()
	clk := clock.NewRealClock()
	exec := newExecutionClient(nil, provider, clk, enums.KindPerp, AccountIDUnified)
	acct := newAccountClient(nil, provider, clk, enums.KindPerp, AccountIDUnified)
	market := newMarketDataClient(nil, provider, clk, enums.KindPerp)
	backend := &reconnectAwareAccountStream{}
	exec.accountStream = backend
	acct.streamBackend = backend
	adapter := &Adapter{
		Market:    market,
		Execution: exec,
		Account:   acct,
		provider:  provider,
		exec:      exec,
		acct:      acct,
		market:    market,
		clk:       clk,
	}
	t.Cleanup(func() { _ = adapter.Close() })

	if err := adapter.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if backend.hookCalls != 1 || backend.started == nil || backend.recovered == nil {
		t.Fatalf("reconnect hooks calls=%d started=%v recovered=%v", backend.hookCalls, backend.started != nil, backend.recovered != nil)
	}
	backend.started(errors.New("connection lost"))
	backend.recovered()

	assertNadoGapEnvelope(t, exec.Events(), contract.StreamGapStarted)
	assertNadoGapEnvelope(t, exec.Events(), contract.StreamGapRecovered)
	select {
	case event := <-exec.Events():
		t.Fatalf("duplicate shared-stream gap envelope: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func assertNadoGapEnvelope(t *testing.T, events <-chan contract.ExecEnvelope, phase contract.StreamGapPhase) {
	t.Helper()
	select {
	case envelope := <-events:
		event, ok := envelope.Payload.(contract.StreamGapEvent)
		if !ok {
			t.Fatalf("payload=%T, want StreamGapEvent", envelope.Payload)
		}
		if event.Venue != VenueName || event.AccountID != AccountIDUnified || event.StreamID != "nado:account:private" || event.Generation != 1 || event.Phase != phase {
			t.Fatalf("gap event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s gap envelope", phase)
	}
}
