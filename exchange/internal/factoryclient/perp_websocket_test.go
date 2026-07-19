package factoryclient

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type fakePerpWSBackend struct {
	fakePublicWSBackend
	referenceMu        sync.Mutex
	startReferenceCall int
	stopReferenceCall  int
	referenceCallbacks streamCallbacks[perpReferenceEvent]
}

func (backend *fakePerpWSBackend) StartReference(
	_ context.Context,
	_ string,
	callbacks streamCallbacks[perpReferenceEvent],
) (func() error, error) {
	backend.referenceMu.Lock()
	defer backend.referenceMu.Unlock()
	backend.startReferenceCall++
	backend.referenceCallbacks = callbacks
	return func() error {
		backend.referenceMu.Lock()
		backend.stopReferenceCall++
		backend.referenceMu.Unlock()
		return nil
	}, nil
}

func TestPerpWebSocketSharesOneNativeReferenceStream(t *testing.T) {
	backend := &fakePerpWSBackend{}
	socket := newPerpWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductPerp},
		backend,
	)
	mark, err := socket.WatchMarkPrice(context.Background(), exchange.WatchRequest{
		Instrument: "ETH",
	})
	if err != nil {
		t.Fatal(err)
	}
	funding, err := socket.WatchFundingRate(context.Background(), exchange.WatchRequest{
		Instrument: "ETH",
	})
	if err != nil {
		t.Fatal(err)
	}
	if backend.startReferenceCall != 1 {
		t.Fatalf("reference starts = %d, want 1", backend.startReferenceCall)
	}

	at := time.UnixMilli(1_700_000_000_000).UTC()
	backend.referenceCallbacks.Event(perpReferenceEvent{
		MarkPrice: exchange.MarkPriceEvent{
			Instrument: "ETH",
			Price:      decimal.RequireFromString("3000.25"),
			Time:       at,
		},
		MarkValid: true,
	})
	if got := <-mark.Events(); !got.Price.Equal(decimal.RequireFromString("3000.25")) {
		t.Fatalf("mark = %+v", got)
	}
	select {
	case got := <-funding.Events():
		t.Fatalf("mark-only native event leaked to funding subscribers: %+v", got)
	default:
	}

	backend.referenceCallbacks.Event(perpReferenceEvent{
		FundingRate: exchange.FundingRateEvent{
			Instrument:  "ETH",
			Rate:        decimal.RequireFromString("0.0001"),
			EffectiveAt: at,
		},
		FundingValid: true,
	})
	if got := <-funding.Events(); !got.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("funding = %+v", got)
	}
	select {
	case got := <-mark.Events():
		t.Fatalf("funding-only native event leaked to mark subscribers: %+v", got)
	default:
	}

	if err := mark.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopReferenceCall != 0 {
		t.Fatal("closing mark stopped funding's shared native stream")
	}
	if err := funding.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.stopReferenceCall != 1 {
		t.Fatalf("reference stops = %d, want 1", backend.stopReferenceCall)
	}
}

func TestPerpWebSocketCloseCancelsPendingReferenceStartupWithoutDeadlock(t *testing.T) {
	backend := newBlockingStartupWSBackend()
	socket := newPerpWebSocket(
		clientMeta{venue: exchange.VenueLighter, product: exchange.ProductPerp},
		backend,
	)
	watchResult := make(chan error, 1)
	go func() {
		_, err := socket.WatchMarkPrice(context.Background(), exchange.WatchRequest{
			Instrument: "ETH",
		})
		watchResult <- err
	}()
	<-backend.referenceStarted

	closeResult := make(chan error, 1)
	go func() { closeResult <- socket.Close() }()
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked behind pending reference startup")
	}
	select {
	case err := <-watchResult:
		if err == nil {
			t.Fatal("pending WatchMarkPrice succeeded after client close")
		}
	case <-time.After(time.Second):
		t.Fatal("pending WatchMarkPrice did not observe client close")
	}
}
