package perp

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

type fakePerpPrivateWS struct {
	started   func(error)
	recovered func()
}

func (f *fakePerpPrivateWS) SubscribeOrderUpdate(func(*sdkperp.OrderUpdateEvent))     {}
func (f *fakePerpPrivateWS) SubscribeAccountUpdate(func(*sdkperp.AccountUpdateEvent)) {}
func (f *fakePerpPrivateWS) Connect() error                                           { return nil }
func (f *fakePerpPrivateWS) Close()                                                   {}
func (f *fakePerpPrivateWS) SetReconnectHooks(started func(error), recovered func()) {
	f.started = started
	f.recovered = recovered
}

func TestPrivateReconnectHooksBridgeIntoExecutionEvents(t *testing.T) {
	ws := &fakePerpPrivateWS{}
	provider := newInstrumentProvider()
	exec := newExecutionClient(nil, provider, nil, "BINANCE:test")
	acct := newAccountClient(nil, provider, nil, "BINANCE:test")
	adapter := &Adapter{
		Execution: exec,
		Account:   acct,
		provider:  provider,
		exec:      exec,
		acct:      acct,
		testHooks: &perpAdapterTestHooks{
			accountWSFactory: func(context.Context) perpAccountWebsocket { return ws },
		},
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.started == nil || ws.recovered == nil {
		t.Fatal("Start did not register private reconnect hooks")
	}

	ws.started(errors.New("socket closed"))
	assertBinancePerpGap(t, <-exec.Events(), contract.StreamGapStarted)
	ws.recovered()
	assertBinancePerpGap(t, <-exec.Events(), contract.StreamGapRecovered)
}

func assertBinancePerpGap(t *testing.T, env contract.ExecEnvelope, phase contract.StreamGapPhase) {
	t.Helper()
	event, ok := env.Payload.(contract.StreamGapEvent)
	if !ok {
		t.Fatalf("payload=%T, want StreamGapEvent", env.Payload)
	}
	if event.Venue != venueName || event.AccountID != "BINANCE:test" || event.StreamID != "binance:perp:private" || event.Generation != 1 || event.Phase != phase {
		t.Fatalf("gap event=%+v", event)
	}
}
