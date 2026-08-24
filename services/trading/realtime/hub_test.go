package realtime

import (
	"errors"
	"testing"
	"time"
)

func TestHubReplaysAfterOpaqueCursor(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	hub, err := NewHub(4, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := hub.Publish("catalog.updated", map[string]string{"revision": "one"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := hub.Publish("market_data.updated", map[string]string{"instrumentId": "btc-usdt"})
	if err != nil {
		t.Fatal(err)
	}
	events, err := hub.Replay(first.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].EventID != second.EventID || events[0].Sequence != first.Sequence+1 {
		t.Fatalf("Replay() = %#v", events)
	}
}

func TestHubRequiresResnapshotForExpiredCursor(t *testing.T) {
	hub, err := NewHub(2, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := hub.Publish("one", map[string]bool{"ready": true})
	_, _ = hub.Publish("two", map[string]bool{"ready": true})
	_, _ = hub.Publish("three", map[string]bool{"ready": true})
	if _, err := hub.Replay(first.Cursor); !errors.Is(err, ErrResnapshotRequired) {
		t.Fatalf("Replay() error = %v, want ErrResnapshotRequired", err)
	}
}

func TestHubRejectsPayloadsThatCannotBeJSONEncoded(t *testing.T) {
	hub, _ := NewHub(2, time.Now)
	if _, err := hub.Publish("invalid", make(chan int)); err == nil {
		t.Fatal("Publish() error = nil")
	}
}

func TestHubScopesPrivateReplayAndLiveEvents(t *testing.T) {
	hub, err := NewHub(8, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	publicEvent, _ := hub.Publish("catalog.updated", map[string]string{"revision": "one"})
	privateA, _ := hub.PublishScoped("principal-a", "command.accepted", map[string]string{"commandId": "command-a"})
	_, _ = hub.PublishScoped("principal-b", "command.accepted", map[string]string{"commandId": "command-b"})

	replayed, err := hub.ReplayScoped("", "principal-a")
	if err != nil || len(replayed) != 2 || replayed[0].EventID != publicEvent.EventID || replayed[1].EventID != privateA.EventID {
		t.Fatalf("ReplayScoped()=%+v err=%v", replayed, err)
	}

	updates, unsubscribe := hub.SubscribeScoped(2, "principal-a")
	defer unsubscribe()
	_, _ = hub.PublishScoped("principal-b", "fill.recorded", map[string]string{"fillId": "fill-b"})
	liveA, _ := hub.PublishScoped("principal-a", "fill.recorded", map[string]string{"fillId": "fill-a"})
	select {
	case event := <-updates:
		if event.EventID != liveA.EventID {
			t.Fatalf("SubscribeScoped() event=%+v, want %+v", event, liveA)
		}
	case <-time.After(time.Second):
		t.Fatal("SubscribeScoped() did not deliver scoped event")
	}
}

func TestHubReplayAndSubscribeSharesOneBoundary(t *testing.T) {
	hub, _ := NewHub(8, time.Now)
	first, _ := hub.PublishScoped("principal-a", "portfolio.position.upsert", map[string]string{"quantity": "1"})
	replayed, updates, unsubscribe, err := hub.ReplayAndSubscribeScoped(first.Cursor, 2, "principal-a")
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	if len(replayed) != 0 {
		t.Fatalf("replayed=%+v", replayed)
	}
	second, _ := hub.PublishScoped("principal-a", "portfolio.position.upsert", map[string]string{"quantity": "2"})
	if event := <-updates; event.EventID != second.EventID {
		t.Fatalf("live event=%+v, want=%+v", event, second)
	}
}

func TestHubClosesSlowSubscriberInsteadOfSilentlyDroppingCursor(t *testing.T) {
	hub, _ := NewHub(8, time.Now)
	updates, unsubscribe := hub.SubscribeScoped(1, "principal-a")
	defer unsubscribe()
	first, _ := hub.PublishScoped("principal-a", "one", map[string]bool{"ready": true})
	second, _ := hub.PublishScoped("principal-a", "two", map[string]bool{"ready": true})
	if event, open := <-updates; !open || event.EventID != first.EventID {
		t.Fatalf("buffered event=%+v open=%v", event, open)
	}
	if _, open := <-updates; open {
		t.Fatal("slow subscriber remained open after overflow")
	}
	replayed, err := hub.ReplayScoped(first.Cursor, "principal-a")
	if err != nil || len(replayed) != 1 || replayed[0].EventID != second.EventID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}
