package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/shopspring/decimal"
)

func TestPositionProjectionPreservesHedgeSlotsAndPublishesExplicitRemoval(t *testing.T) {
	hub := newTestHub(t, 16, 16)
	now := time.Date(2026, 8, 24, 8, 0, 0, 0, time.UTC)
	projection, err := NewPositionProjection(hub, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	accountID := domain.AccountID("acct-a")
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	if err := projection.Apply("principal-a", accountID, "perp-usdt", instrumentID, exchange.PositionEvent{
		Kind: exchange.EventSnapshot, Time: now.Add(-time.Second), Positions: []exchange.Position{
			{PositionSlot: exchange.PositionSlotLong, Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.1")},
			{PositionSlot: exchange.PositionSlotShort, Side: exchange.SideSell, Quantity: decimal.RequireFromString("0.2")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projection.Snapshot("principal-a", accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 2 || snapshot.Freshness != PositionLive || snapshot.VerifiedAt != now {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if snapshot.Items[1].Position.Quantity.String() != "-0.2" {
		t.Fatalf("short quantity=%s, want -0.2", snapshot.Items[1].Position.Quantity)
	}

	subscription, err := hub.Subscribe(t.Context(), SubscribeRequest{PrincipalID: "principal-a", AccountID: accountID})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	now = now.Add(time.Second)
	if err := projection.Apply("principal-a", accountID, "perp-usdt", instrumentID, exchange.PositionEvent{
		Kind: exchange.EventSnapshot, Time: now, Positions: []exchange.Position{
			{PositionSlot: exchange.PositionSlotLong, Side: exchange.SideBuy, Quantity: decimal.RequireFromString("0.15")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	first := receiveEvent(t, subscription)
	second := receiveEvent(t, subscription)
	operations := map[PositionOperation]bool{}
	for _, event := range []Event{first, second} {
		var payload PositionPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		operations[payload.Operation] = true
	}
	if !operations[PositionUpsert] || !operations[PositionRemove] {
		t.Fatalf("position operations=%v", operations)
	}
	snapshot, err = projection.Snapshot("principal-a", accountID)
	if err != nil || len(snapshot.Items) != 1 || snapshot.Items[0].PositionSlot != exchange.PositionSlotLong {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestPositionProjectionCompleteEmptySnapshotRemovesLastKnownRows(t *testing.T) {
	hub := newTestHub(t, 8, 8)
	projection, err := NewPositionProjection(hub, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	accountID := domain.AccountID("acct-a")
	instrumentID := domain.InstrumentID("okx:live:perp:USDT:BTC-USDT")
	if _, err := projection.ReplaceAccount("principal-a", accountID, []PositionSeed{{
		RouteID: "perp-usdt", InstrumentID: instrumentID,
		Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.NewFromInt(1)},
	}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := projection.ReplaceAccount("principal-a", accountID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := projection.Snapshot("principal-b", accountID); err == nil {
		t.Fatal("cross-principal snapshot succeeded")
	}
}

func TestPositionProjectionSynchronizeDoesNotBlockAndCannotOverwriteNewerStreamUpdate(t *testing.T) {
	hub := newTestHub(t, 8, 8)
	projection, err := NewPositionProjection(hub, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	accountID := domain.AccountID("acct-a")
	instrumentID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	loaderStarted := make(chan struct{})
	releaseLoader := make(chan struct{})
	syncDone := make(chan error, 1)
	go func() {
		_, syncErr := projection.SynchronizeAccount("principal-a", accountID, func() ([]PositionSeed, error) {
			close(loaderStarted)
			<-releaseLoader
			return []PositionSeed{{
				RouteID: "perp-usdt", InstrumentID: instrumentID,
				Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.NewFromInt(1)},
			}}, nil
		})
		syncDone <- syncErr
	}()
	<-loaderStarted

	updateDone := make(chan error, 1)
	go func() {
		updateErr := projection.Apply("principal-a", accountID, "perp-usdt", instrumentID, exchange.PositionEvent{
			Kind: exchange.EventDelta,
			Positions: []exchange.Position{{
				Side: exchange.SideBuy, Quantity: decimal.NewFromInt(2),
			}},
		})
		updateDone <- updateErr
	}()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream update was blocked by the in-flight REST snapshot")
	}
	close(releaseLoader)
	if err := <-syncDone; err != nil {
		t.Fatal(err)
	}
	snapshot, err := projection.Snapshot("principal-a", accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Position.Quantity.String() != "2" {
		t.Fatalf("snapshot=%+v, want newer stream update to win", snapshot)
	}
}

func TestPositionProjectionInfersHedgeSlotsAcrossCompleteRESTSnapshot(t *testing.T) {
	hub := newTestHub(t, 8, 8)
	projection, err := NewPositionProjection(hub, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	instrumentID := domain.InstrumentID("legacy:live:perp:USDT:BTC-USDT")
	snapshot, err := projection.ReplaceAccount("principal-a", "acct-a", []PositionSeed{
		{RouteID: "perp-usdt", InstrumentID: instrumentID, Position: exchange.Position{Side: exchange.SideBuy, Quantity: decimal.NewFromInt(1)}},
		{RouteID: "perp-usdt", InstrumentID: instrumentID, Position: exchange.Position{Side: exchange.SideSell, Quantity: decimal.NewFromInt(2)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 2 || snapshot.Items[0].PositionSlot != exchange.PositionSlotLong || snapshot.Items[1].PositionSlot != exchange.PositionSlotShort {
		t.Fatalf("items=%+v", snapshot.Items)
	}
}
