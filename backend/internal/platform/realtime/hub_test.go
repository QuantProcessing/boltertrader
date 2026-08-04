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
