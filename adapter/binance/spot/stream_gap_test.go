package spot

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/contract"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

type fakeSpotPrivateWS struct {
	started   func(error)
	recovered func()
}

func (f *fakeSpotPrivateWS) SubscribeExecutionReport(func(*sdkspot.ExecutionReportEvent)) {}
func (f *fakeSpotPrivateWS) SubscribeAccountPosition(func(*sdkspot.AccountPositionEvent)) {}
func (f *fakeSpotPrivateWS) Connect() error                                               { return nil }
func (f *fakeSpotPrivateWS) Close()                                                       {}
func (f *fakeSpotPrivateWS) SetReconnectHooks(started func(error), recovered func()) {
	f.started = started
	f.recovered = recovered
}

func TestPrivateReconnectHooksBridgeIntoExecutionEvents(t *testing.T) {
	ws := &fakeSpotPrivateWS{}
	provider := newInstrumentProvider()
	exec := newExecutionClient(nil, provider, nil, "BINANCE:test")
	acct := newAccountClient(nil, provider, nil, "BINANCE:test")
	adapter := &Adapter{
		Execution: exec,
		Account:   acct,
		provider:  provider,
		exec:      exec,
		acct:      acct,
		testHooks: &spotAdapterTestHooks{
			accountWSFactory: func() spotAccountWebsocket { return ws },
		},
	}
	if err := adapter.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ws.started == nil || ws.recovered == nil {
		t.Fatal("Start did not register private reconnect hooks")
	}

	ws.started(errors.New("socket closed"))
	assertBinanceSpotGap(t, <-exec.Events(), contract.StreamGapStarted)
	ws.recovered()
	assertBinanceSpotGap(t, <-exec.Events(), contract.StreamGapRecovered)
}

func assertBinanceSpotGap(t *testing.T, env contract.ExecEnvelope, phase contract.StreamGapPhase) {
	t.Helper()
	event, ok := env.Payload.(contract.StreamGapEvent)
	if !ok {
		t.Fatalf("payload=%T, want StreamGapEvent", env.Payload)
	}
	if event.Venue != venueName || event.AccountID != "BINANCE:test" || event.StreamID != "binance:spot:private" || event.Generation != 1 || event.Phase != phase {
		t.Fatalf("gap event=%+v", event)
	}
}
