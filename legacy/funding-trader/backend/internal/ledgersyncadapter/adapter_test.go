package ledgersyncadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
)

func TestFundingFactResolverCallsStoreWithExactScopeAndUpstreamRef(t *testing.T) {
	t.Parallel()

	scope := ledgerfacts.Scope{
		PrincipalID:         "principal-1",
		Mode:                "test",
		AccountID:           "acct-1",
		InstrumentID:        "BTC-PERP",
		WindowStart:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.FixedZone("custom", 2*60*60)),
		WindowEnd:           time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC),
		PaginationTraversal: "forward",
	}
	store := &recordingFactStore{
		factID: "fact-123",
		found:  true,
	}
	resolver, err := NewFundingFactResolver(store)
	if err != nil {
		t.Fatalf("NewFundingFactResolver() error = %v", err)
	}

	factID, found, err := resolver.ResolveGatewayFundingFactID(context.Background(), scope, " upstream-ref ")
	if err != nil {
		t.Fatalf("ResolveGatewayFundingFactID() error = %v", err)
	}
	if factID != "fact-123" || !found {
		t.Fatalf("ResolveGatewayFundingFactID() = (%q, %v), want (%q, true)", factID, found, "fact-123")
	}
	if store.scope != scope {
		t.Fatalf("store scope = %#v, want exact %#v", store.scope, scope)
	}
	if store.upstreamRef != " upstream-ref " {
		t.Fatalf("store upstream ref = %q, want exact upstream ref", store.upstreamRef)
	}
}

func TestFundingFactResolverPropagatesStoreError(t *testing.T) {
	t.Parallel()

	storeErr := errors.New("store unavailable")
	resolver, err := NewFundingFactResolver(&recordingFactStore{err: storeErr})
	if err != nil {
		t.Fatalf("NewFundingFactResolver() error = %v", err)
	}

	_, _, err = resolver.ResolveGatewayFundingFactID(context.Background(), ledgerfacts.Scope{}, "source")
	if !errors.Is(err, storeErr) {
		t.Fatalf("ResolveGatewayFundingFactID() error = %v, want %v", err, storeErr)
	}
}

func TestPublisherPublishesLedgerSyncEventToAllScope(t *testing.T) {
	t.Parallel()

	stream := &recordingStreamPublisher{}
	publisher, err := NewPublisher(PublisherConfig{
		Stream:      stream,
		PrincipalID: "principal-1",
		Mode:        "live",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}

	err = publisher.Publish(context.Background(), ledgersync.Event{
		Topic: "ledger.funding_payment.append",
		Payload: map[string]any{
			"factId": "fact-1",
			"amount": float64(12),
		},
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if stream.input.Scope != "all" {
		t.Fatalf("input scope = %q, want all", stream.input.Scope)
	}
	if stream.input.PrincipalID != "principal-1" {
		t.Fatalf("input principal = %q, want principal-1", stream.input.PrincipalID)
	}
	if stream.input.Mode != "live" {
		t.Fatalf("input mode = %q, want live", stream.input.Mode)
	}
	if stream.input.Kind != "ledger.funding_payment.append" {
		t.Fatalf("input kind = %q, want ledger.funding_payment.append", stream.input.Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(stream.input.Data, &payload); err != nil {
		t.Fatalf("payload is not json: %v", err)
	}
	if payload["factId"] != "fact-1" || payload["amount"] != float64(12) {
		t.Fatalf("payload = %#v, want factId and amount", payload)
	}
}

func TestNewPublisherValidatesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config PublisherConfig
	}{
		{
			name: "missing stream",
			config: PublisherConfig{
				PrincipalID: "principal-1",
				Mode:        "test",
			},
		},
		{
			name: "missing principal",
			config: PublisherConfig{
				Stream: &recordingStreamPublisher{},
				Mode:   "test",
			},
		},
		{
			name: "invalid mode",
			config: PublisherConfig{
				Stream:      &recordingStreamPublisher{},
				PrincipalID: "principal-1",
				Mode:        "paper",
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewPublisher(tc.config); err == nil {
				t.Fatal("NewPublisher() error = nil, want validation error")
			}
		})
	}
}

func TestPublisherValidatesTopic(t *testing.T) {
	t.Parallel()

	stream := &recordingStreamPublisher{}
	publisher, err := NewPublisher(PublisherConfig{
		Stream:      stream,
		PrincipalID: "principal-1",
		Mode:        "test",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}

	err = publisher.Publish(context.Background(), ledgersync.Event{Topic: "   ", Payload: map[string]any{"ok": true}})
	if err == nil {
		t.Fatal("Publish() error = nil, want validation error")
	}
	if stream.called {
		t.Fatal("Publish() called stream for invalid topic")
	}
}

func TestPublisherPropagatesMarshalErrorWithoutPublishing(t *testing.T) {
	t.Parallel()

	stream := &recordingStreamPublisher{}
	publisher, err := NewPublisher(PublisherConfig{
		Stream:      stream,
		PrincipalID: "principal-1",
		Mode:        "test",
	})
	if err != nil {
		t.Fatalf("NewPublisher() error = %v", err)
	}

	err = publisher.Publish(context.Background(), ledgersync.Event{
		Topic:   "ledger.funding_payment.append",
		Payload: map[string]any{"bad": func() {}},
	})
	if err == nil {
		t.Fatal("Publish() error = nil, want marshal error")
	}
	if stream.called {
		t.Fatal("Publish() called stream after marshal failure")
	}
}

type recordingFactStore struct {
	scope       ledgerfacts.Scope
	upstreamRef string
	factID      string
	found       bool
	err         error
}

func (store *recordingFactStore) ResolveFundingFactID(_ context.Context, scope ledgerfacts.Scope, upstreamRef string) (string, bool, error) {
	store.scope = scope
	store.upstreamRef = upstreamRef
	return store.factID, store.found, store.err
}

type recordingStreamPublisher struct {
	called bool
	input  uistream.EventInput
	err    error
}

func (publisher *recordingStreamPublisher) Publish(_ context.Context, input uistream.EventInput) (uistream.StoredEvent, error) {
	publisher.called = true
	publisher.input = input
	return uistream.StoredEvent{}, publisher.err
}
