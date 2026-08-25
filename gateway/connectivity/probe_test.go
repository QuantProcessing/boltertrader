package connectivity

import (
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestProbeStateMachineUsesThreeFailuresAndThreeSuccessRecovery(t *testing.T) {
	state := newMachine(exchange.VenueBinance)
	now := time.Date(2026, 8, 25, 3, 0, 0, 0, time.UTC)
	if got := state.observe(now, 100*time.Millisecond, nil).State; got != StateOnline {
		t.Fatalf("first success = %s", got)
	}
	for index := 0; index < 2; index++ {
		if got := state.observe(now, 0, errors.New("down")).State; got != StateDelayed {
			t.Fatalf("failure %d = %s", index+1, got)
		}
	}
	if got := state.observe(now, 0, errors.New("down")).State; got != StateOffline {
		t.Fatalf("third failure = %s", got)
	}
	for index := 1; index <= 2; index++ {
		if got := state.observe(now, 100*time.Millisecond, nil).State; got != StateDelayed {
			t.Fatalf("recovery %d = %s", index, got)
		}
	}
	if got := state.observe(now, 100*time.Millisecond, nil).State; got != StateOnline {
		t.Fatalf("third recovery = %s", got)
	}
}

func TestProbeEWMAKeepsSlowVenueDelayed(t *testing.T) {
	state := newMachine(exchange.VenueOKX)
	now := time.Now().UTC()
	if got := state.observe(now, 2*time.Second, nil).State; got != StateDelayed {
		t.Fatalf("slow first success = %s", got)
	}
	for range 3 {
		state.observe(now, 900*time.Millisecond, nil)
	}
	if state.snapshot.EWMALatency < time.Second || state.snapshot.State != StateDelayed {
		t.Fatalf("snapshot = %#v", state.snapshot)
	}
}

func TestDefaultNadoProbeUsesDocumentedStatusQuery(t *testing.T) {
	check, ok := DefaultHTTPCheckers(nil)[exchange.VenueNado].(HTTPCheck)
	if !ok {
		t.Fatal("Nado checker is not an HTTP check")
	}
	if want := "https://gateway.prod.nado.xyz/v1/query?type=status"; check.URL != want {
		t.Fatalf("Nado probe URL = %q, want %q", check.URL, want)
	}
}
