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

func TestHubFansOutLiveEventsByPrincipalAccountAndKind(t *testing.T) {
	hub := newTestHub(t, 4)
	account, err := hub.Subscribe(t.Context(), SubscribeRequest{
		PrincipalID: "principal-a", AccountID: "acct-a", Kinds: []Kind{KindOrder},
	})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := hub.SubscribePrincipal(t.Context(), "principal-a", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(account.Close)
	t.Cleanup(principal.Close)

	publishTest(t, hub, "principal-a", "acct-a", KindBalance, 1, `{"asset":"USDT"}`)
	wanted := publishTest(t, hub, "principal-a", "acct-a", KindOrder, 2, `{"state":"open"}`)
	publishTest(t, hub, "principal-b", "acct-a", KindOrder, 3, `{"state":"hidden"}`)

	if got := receiveEvent(t, account); got.EventID != wanted.EventID || got.Kind != KindOrder {
		t.Fatalf("account event=%+v", got)
	}
	if got := receiveEvent(t, principal); got.Kind != KindBalance {
		t.Fatalf("principal first event=%+v", got)
	}
	if got := receiveEvent(t, principal); got.EventID != wanted.EventID {
		t.Fatalf("principal second event=%+v", got)
	}
	assertNoEvent(t, account)
	assertNoEvent(t, principal)
}

func TestHubIsLiveOnly(t *testing.T) {
	hub := newTestHub(t, 4)
	publishTest(t, hub, "principal-a", "acct-a", KindOrder, 1, `{"state":"open"}`)
	subscription, err := hub.Subscribe(t.Context(), SubscribeRequest{PrincipalID: "principal-a", AccountID: "acct-a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	assertNoEvent(t, subscription)
}

func TestHubPrincipalSubscriptionSurvivesAccountResync(t *testing.T) {
	observer := &recordingResyncObserver{}
	hub := newTestHubWithObserver(t, 4, observer)
	subscription, err := hub.SubscribePrincipal(t.Context(), "principal-a", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)

	if _, err := hub.PublishResync("principal-a", "acct-a", "venue_stream_gap"); err != nil {
		t.Fatal(err)
	}
	if got := receiveEvent(t, subscription); got.Kind != KindResyncRequired || got.AccountID != "acct-a" {
		t.Fatalf("resync event=%+v", got)
	}
	publishTest(t, hub, "principal-a", "acct-b", KindPosition, 1, `{"symbol":"BTC-USDT"}`)
	if got := receiveEvent(t, subscription); got.Kind != KindPosition || got.AccountID != "acct-b" {
		t.Fatalf("event after resync=%+v", got)
	}
	if len(observer.reasons) != 1 || observer.reasons[0] != "venue_stream_gap" {
		t.Fatalf("observer reasons=%v", observer.reasons)
	}
}

func TestHubClosesSlowConsumer(t *testing.T) {
	hub := newTestHub(t, 1)
	subscription, err := hub.Subscribe(t.Context(), SubscribeRequest{PrincipalID: "principal-a", AccountID: "acct-a"})
	if err != nil {
		t.Fatal(err)
	}
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
	hub := newTestHub(t, 4)
	subscription, err := hub.Subscribe(t.Context(), SubscribeRequest{PrincipalID: "principal-a", AccountID: "acct-a"})
	if err != nil {
		t.Fatal(err)
	}
	eventID, err := hub.identity.EventID()
	if err != nil {
		t.Fatal(err)
	}
	command := store.StoredCommand{
		CommandID: "018f0000-0000-7000-8000-000000000011", GatewayOrderID: "018f0000-0000-7000-8000-000000000012",
		PrincipalID: "principal-a", AccountID: "acct-a", InstrumentID: "binance:live:spot:BTC-USDT",
		ClientOrderID: "7", State: domain.StateOpen, ResourceVersion: 3, VenueOrderID: "venue-7", EventID: eventID,
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
	if payload.GatewayOrderID != command.GatewayOrderID || payload.State != domain.StateOpen || payload.VenueOrderID != "" {
		t.Fatalf("order payload=%+v", payload)
	}
}

type recordingResyncObserver struct{ reasons []string }

func (observer *recordingResyncObserver) ObserveResync(reason string) {
	observer.reasons = append(observer.reasons, reason)
}

func newTestHub(t *testing.T, sizes ...int) *Hub {
	subscriberBuffer := 4
	if len(sizes) > 0 {
		subscriberBuffer = sizes[len(sizes)-1]
	}
	return newTestHubWithObserver(t, subscriberBuffer, nil)
}

func newTestHubWithObserver(t *testing.T, subscriberBuffer int, observer ResyncObserver) *Hub {
	t.Helper()
	hub, err := NewHub(Options{
		SubscriberBuffer: subscriberBuffer, Observer: observer,
		Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{
			Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }, Random: strings.NewReader(strings.Repeat("a", 4096)),
		}),
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Close)
	return hub
}

func publishTest(t *testing.T, hub *Hub, principal domain.PrincipalID, account domain.AccountID, kind Kind, version domain.ResourceVersion, payload string) Event {
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

func subscribeTest(t *testing.T, hub *Hub, principal domain.PrincipalID, account domain.AccountID, legacyCursor string) *Subscription {
	t.Helper()
	if legacyCursor != "" {
		t.Fatal("account event replay cursors are no longer supported")
	}
	subscription, err := hub.Subscribe(t.Context(), SubscribeRequest{PrincipalID: principal, AccountID: account})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	return subscription
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
