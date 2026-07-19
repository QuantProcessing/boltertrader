package exchange

import (
	"reflect"
	"testing"
)

func TestSubscriptionExposesTypedLifecycleStatus(t *testing.T) {
	subscription := reflect.TypeOf((*Subscription[BookEvent])(nil)).Elem()
	assertMethodNames(t, subscription, []string{
		"Close", "Errors", "Events", "ID", "Status",
	})
}

func TestWebSocketStrictIntersectionIncludesPrivateStreamsAndTrading(t *testing.T) {
	spot := reflect.TypeOf((*SpotWebSocket)(nil)).Elem()
	for _, required := range []string{
		"WatchCandles", "WatchOrders", "WatchFills", "WatchBalances",
		"PlaceOrder", "CancelOrder",
	} {
		if _, exists := spot.MethodByName(required); !exists {
			t.Fatalf("SpotWebSocket must expose native common-intersection method %s", required)
		}
	}

	perp := reflect.TypeOf((*PerpWebSocket)(nil)).Elem()
	if _, exists := perp.MethodByName("WatchPositions"); !exists {
		t.Fatal("PerpWebSocket must expose native common-intersection method WatchPositions")
	}
}

func TestStreamLifecycleStatesAreExplicit(t *testing.T) {
	if SubscriptionConnecting != "connecting" ||
		SubscriptionActive != "active" ||
		SubscriptionGap != "gap" ||
		SubscriptionResyncing != "resyncing" ||
		SubscriptionClosed != "closed" {
		t.Fatal("subscription lifecycle constants changed")
	}
	if GapStarted != "started" || GapRecovered != "recovered" {
		t.Fatal("gap phase constants changed")
	}
}
