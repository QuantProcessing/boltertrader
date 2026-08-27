package marketowner

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
)

type bookSubscription struct {
	events chan exchange.BookEvent
	errors chan error
	status chan exchange.StreamStatusEvent
}

func (subscription *bookSubscription) ID() string                        { return "book" }
func (subscription *bookSubscription) Events() <-chan exchange.BookEvent { return subscription.events }
func (subscription *bookSubscription) Errors() <-chan error              { return subscription.errors }
func (subscription *bookSubscription) Status() <-chan exchange.StreamStatusEvent {
	return subscription.status
}
func (subscription *bookSubscription) Close() error { return nil }

func TestForwardBookPreservesSnapshotAndDeltaKinds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	subscription := &bookSubscription{
		events: make(chan exchange.BookEvent, 2), errors: make(chan error), status: make(chan exchange.StreamStatusEvent),
	}
	output, failures := make(chan markethub.Message, 2), make(chan error, 1)
	go forwardBook(ctx, subscription, output, failures)
	subscription.events <- exchange.BookEvent{Kind: exchange.EventSnapshot, Sequence: "1", Time: time.Now().UTC()}
	subscription.events <- exchange.BookEvent{Kind: exchange.EventDelta, Sequence: "2", Previous: "1", Time: time.Now().UTC()}
	if first := <-output; first.Kind != markethub.MessageSnapshot {
		t.Fatalf("snapshot kind = %s", first.Kind)
	}
	if second := <-output; second.Kind != markethub.MessageDelta {
		t.Fatalf("delta kind = %s", second.Kind)
	}
}
