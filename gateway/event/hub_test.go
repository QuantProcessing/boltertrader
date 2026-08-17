package event

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
)

func TestHubReportsResyncReasonsToBoundedObserver(t *testing.T) {
	observer := &recordingResyncObserver{}
	hub, err := NewHub(Options{
		Capacity: 2, SubscriberBuffer: 2, Observer: observer,
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
			Now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
			Random: strings.NewReader(strings.Repeat("a", 4096)),
		}),
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)

	if _, err := hub.PublishResync("principal-a", "acct-a", "venue_stream_gap"); err != nil {
		t.Fatal(err)
	}
	if len(observer.reasons) != 1 || observer.reasons[0] != "venue_stream_gap" {
		t.Fatalf("observer reasons = %v", observer.reasons)
	}
}

type recordingResyncObserver struct {
	reasons []string
}

func (observer *recordingResyncObserver) ObserveResync(reason string) {
	observer.reasons = append(observer.reasons, reason)
}

func TestHubSequencesPerAccountAndNeverLeaksAcrossSubscriptions(t *testing.T) {
	hub := newTestHub(t, 4, 4)
	first := subscribeTest(t, hub, "principal-a", "acct-a", "")
	second := subscribeTest(t, hub, "principal-a", "acct-b", "")

	publishTest(t, hub, "principal-a", "acct-a", KindBalance, 1, `{"asset":"USDT"}`)
	publishTest(t, hub, "principal-a", "acct-b", KindPosition, 1, `{"symbol":"BTC-USDT"}`)
	publishTest(t, hub, "principal-a", "acct-a", KindOrder, 2, `{"state":"open"}`)

	firstEvent := receiveEvent(t, first)
	secondEvent := receiveEvent(t, first)
	if firstEvent.AccountID != "acct-a" || firstEvent.Sequence != 1 ||
		secondEvent.AccountID != "acct-a" || secondEvent.Sequence != 2 {
		t.Fatalf("acct-a sequence events=%+v %+v", firstEvent, secondEvent)
	}
	onlySecond := receiveEvent(t, second)
	if onlySecond.AccountID != "acct-b" || onlySecond.Sequence != 1 {
		t.Fatalf("acct-b event=%+v", onlySecond)
	}
	assertNoEvent(t, first)
	assertNoEvent(t, second)
}

func TestHubReplaysCurrentBootCursorAndFiltersKinds(t *testing.T) {
	hub := newTestHub(t, 8, 8)
	first := publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	publishTest(t, hub, "principal-a", "acct-a", KindBalance, 2, `{"asset":"USDT"}`)
	third := publishTest(t, hub, "principal-a", "acct-a", KindFill, 3, `{"fill_id":"1"}`)

	subscription, err := hub.Subscribe(context.Background(), SubscribeRequest{
		PrincipalID: "principal-a",
		AccountID:   "acct-a",
		Cursor:      first.Cursor(),
		Kinds:       []Kind{KindFill},
		Buffer:      8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)

	replayed := receiveEvent(t, subscription)
	if replayed.EventID != third.EventID || replayed.Kind != KindFill || replayed.Sequence != 3 {
		t.Fatalf("replayed event=%+v", replayed)
	}
	assertNoEvent(t, subscription)
}

func TestHubReplaysEveryBufferedEventEvenWhenBacklogExceedsLiveBuffer(t *testing.T) {
	hub := newTestHub(t, 128, 4)
	first := publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	for version := domain.ResourceVersion(2); version <= 20; version++ {
		publishTest(t, hub, "principal-a", "acct-a", KindOrder, version, `{"state":"open"}`)
	}
	subscription, err := hub.Subscribe(context.Background(), SubscribeRequest{
		PrincipalID: "principal-a", AccountID: "acct-a",
		Cursor: first.Cursor(), Buffer: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	for wantSequence := uint64(2); wantSequence <= 20; wantSequence++ {
		if event := receiveEvent(t, subscription); event.Sequence != wantSequence {
			t.Fatalf("replay sequence=%d, want %d", event.Sequence, wantSequence)
		}
	}
	assertNoEvent(t, subscription)
}

func TestHubNeverLeaksSameAccountEventsAcrossPrincipals(t *testing.T) {
	hub := newTestHub(t, 8, 8)
	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	publishTest(t, hub, "principal-b", "acct-a", KindOrder, 1, `{"state":"open"}`)
	assertNoEvent(t, subscription)
	want := publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	if got := receiveEvent(t, subscription); got.EventID != want.EventID {
		t.Fatalf("event=%+v want=%+v", got, want)
	}
}

func TestHubEmitsResyncRequiredForRestartEvictionAndFutureCursor(t *testing.T) {
	tests := []struct {
		name   string
		cursor func(*Hub) string
	}{
		{
			name: "different boot",
			cursor: func(*Hub) string {
				return "018f0000-0000-7000-8000-000000000099:1"
			},
		},
		{
			name: "evicted sequence",
			cursor: func(hub *Hub) string {
				return string(hub.BootID()) + ":1"
			},
		},
		{
			name: "future sequence",
			cursor: func(hub *Hub) string {
				return string(hub.BootID()) + ":99"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := newTestHub(t, 2, 4)
			for version := domain.ResourceVersion(1); version <= 4; version++ {
				publishTest(t, hub, "principal-a", "acct-a", KindOrder, version, `{"state":"open"}`)
			}
			subscription, err := hub.Subscribe(context.Background(), SubscribeRequest{
				PrincipalID: "principal-a",
				AccountID:   "acct-a",
				Cursor:      test.cursor(hub),
				Buffer:      4,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(subscription.Close)

			event := receiveEvent(t, subscription)
			if event.Kind != KindResyncRequired {
				t.Fatalf("event=%+v", event)
			}
			if event.Sequence != 5 {
				t.Fatalf("resync sequence=%d, want next account sequence 5", event.Sequence)
			}
			var payload ResyncPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			wantResources := []string{
				"/v1/accounts/acct-a",
				"/v1/orders?account_id=acct-a",
				"/v1/fills?account_id=acct-a",
				"/v1/balances?account_id=acct-a",
				"/v1/positions?account_id=acct-a",
			}
			if strings.Join(payload.Resources, "\n") != strings.Join(wantResources, "\n") ||
				payload.Reason == "" {
				t.Fatalf("resync payload=%+v", payload)
			}
		})
	}
}

func TestHubPublishesLiveResyncAndClosesAffectedSubscribers(t *testing.T) {
	hub := newTestHub(t, 8, 4)
	affected := subscribeTest(t, hub, "principal-a", "acct-a", "")
	unaffected := subscribeTest(t, hub, "principal-a", "acct-b", "")

	event, err := hub.PublishResync("principal-a", "acct-a", "venue_stream_gap")
	if err != nil {
		t.Fatal(err)
	}
	got := receiveEvent(t, affected)
	if got.EventID != event.EventID || got.Kind != KindResyncRequired {
		t.Fatalf("resync event=%+v", got)
	}
	var payload ResyncPayload
	if err := json.Unmarshal(got.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Reason != "venue_stream_gap" {
		t.Fatalf("resync payload=%+v", payload)
	}
	select {
	case _, open := <-affected.Events():
		if open {
			t.Fatal("affected subscription remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("affected subscription was not closed")
	}
	assertNoEvent(t, unaffected)
}

func TestHubNeverFiltersReplayedResyncRequired(t *testing.T) {
	hub := newTestHub(t, 8, 4)
	first := publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	if _, err := hub.PublishResync("principal-a", "acct-a", "venue_stream_gap"); err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(context.Background(), SubscribeRequest{
		PrincipalID: "principal-a",
		AccountID:   "acct-a",
		Cursor:      first.Cursor(),
		Kinds:       []Kind{KindFill},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	if event := receiveEvent(t, subscription); event.Kind != KindResyncRequired {
		t.Fatalf("filtered replay event=%+v, want resync_required", event)
	}
}

func TestHubClosesSlowConsumerWithStableReason(t *testing.T) {
	hub := newTestHub(t, 8, 1)
	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	publishTest(t, hub, "principal-a", "acct-a", KindOrder, 2, `{"state":"filled"}`)

	select {
	case err := <-subscription.Errors():
		if !errors.Is(err, ErrSlowConsumer) {
			t.Fatalf("slow consumer error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow consumer was not closed")
	}
}

func TestHubPublishesCommittedOrderWithDurableIdentityAndVersion(t *testing.T) {
	hub := newTestHub(t, 4, 4)
	subscription := subscribeTest(t, hub, "principal-a", "acct-a", "")
	eventID, err := hub.identity.EventID()
	if err != nil {
		t.Fatal(err)
	}
	command := store.StoredCommand{
		CommandID:       "018f0000-0000-7000-8000-000000000011",
		GatewayOrderID:  "018f0000-0000-7000-8000-000000000012",
		PrincipalID:     "principal-a",
		AccountID:       "acct-a",
		InstrumentID:    "binance:live:spot:BTC-USDT",
		ClientOrderID:   "7",
		State:           domain.StateOpen,
		ResourceVersion: 3,
		VenueOrderID:    "venue-7",
		EventID:         eventID,
	}
	if err := hub.PublishCommitted(context.Background(), command); err != nil {
		t.Fatal(err)
	}

	event := receiveEvent(t, subscription)
	if event.EventID != eventID || event.ResourceVersion != 3 || event.Kind != KindOrder {
		t.Fatalf("committed event=%+v", event)
	}
	var payload OrderPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.GatewayOrderID != command.GatewayOrderID || payload.State != domain.StateOpen ||
		payload.VenueOrderID != "" {
		t.Fatalf("order payload=%+v", payload)
	}
}

func newTestHub(t *testing.T, capacity, subscriberBuffer int) *Hub {
	t.Helper()
	hub, err := NewHub(Options{
		Capacity: capacity, SubscriberBuffer: subscriberBuffer,
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
			Now:    func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
			Random: strings.NewReader(strings.Repeat("a", 4096)),
		}),
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func subscribeTest(t *testing.T, hub *Hub, principal domain.PrincipalID, account domain.AccountID, cursor string) *Subscription {
	t.Helper()
	subscription, err := hub.Subscribe(context.Background(), SubscribeRequest{
		PrincipalID: principal, AccountID: account, Cursor: cursor,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	return subscription
}

func publishTest(
	t *testing.T,
	hub *Hub,
	principal domain.PrincipalID,
	account domain.AccountID,
	kind Kind,
	version domain.ResourceVersion,
	payload string,
) Event {
	t.Helper()
	eventID, err := hub.identity.EventID()
	if err != nil {
		t.Fatal(err)
	}
	event, err := hub.Publish(PublishRequest{
		PrincipalID: principal, AccountID: account, EventID: eventID,
		ResourceVersion: version, Kind: kind, Payload: json.RawMessage(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func receiveEvent(t *testing.T, subscription *Subscription) Event {
	t.Helper()
	select {
	case event, ok := <-subscription.Events():
		if !ok {
			t.Fatal("subscription closed before event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}

func assertNoEvent(t *testing.T, subscription *Subscription) {
	t.Helper()
	select {
	case event := <-subscription.Events():
		t.Fatalf("unexpected event=%+v", event)
	case <-time.After(20 * time.Millisecond):
	}
}
