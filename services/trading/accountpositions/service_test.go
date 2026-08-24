package accountpositions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/realtime"
)

func TestServiceKeepsHealthyAccountsWhenSiblingSnapshotFails(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{
		accounts: []Account{testAccount("acct-a", "Binance"), testAccount("acct-b", "OKX")},
		snapshots: map[string]SourceSnapshot{
			"acct-a": testSourceSnapshot("acct-a", "boot-a", "0.1", "long", now),
		},
		errors: map[string]error{"acct-b": errors.New("venue unavailable")},
	}
	hub, _ := realtime.NewHub(32, func() time.Time { return now })
	service, err := NewService(source, NewMemoryStore(), hub, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].AccountID != "acct-a" || snapshot.Items[0].PositionSlot != "long" {
		t.Fatalf("items=%+v", snapshot.Items)
	}
	if len(snapshot.Accounts) != 2 || snapshot.Summary.StaleAccountCount != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	for _, account := range snapshot.Accounts {
		if account.AccountID == "acct-b" && account.Freshness != FreshnessUnavailable {
			t.Fatalf("failed account=%+v", account)
		}
	}
}

func TestServiceDoesNotPromoteFailedInitialSnapshotWhenStreamConnects(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{
		accounts: []Account{testAccount("acct-a", "Binance")},
		errors:   map[string]error{"acct-a": errors.New("position snapshot unavailable")},
	}
	hub, _ := realtime.NewHub(8, func() time.Time { return now })
	service, err := NewService(source, NewMemoryStore(), hub, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Accounts) != 1 || initial.Accounts[0].Freshness != FreshnessUnavailable || initial.Accounts[0].SourceCursor != "" {
		t.Fatalf("initial=%+v", initial)
	}
	if err := service.applySourceEvent(context.Background(), "principal-a", "acct-a", SourceEvent{Kind: SourceEventConnected}); !errors.Is(err, ErrResyncRequired) {
		t.Fatalf("connected event error=%v", err)
	}
	service.markAccountStale(context.Background(), "principal-a", "acct-a", "event_resync_required")
	after, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Accounts[0].Freshness != FreshnessUnavailable || after.Accounts[0].RouteHealth != RouteUnavailable || len(after.Items) != 0 {
		t.Fatalf("after=%+v", after)
	}
}

func TestServiceAppliesGatewayPositionEventWithStableSlotAndRevision(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{accounts: []Account{testAccount("acct-a", "Binance")}, snapshots: map[string]SourceSnapshot{
		"acct-a": testSourceSnapshot("acct-a", "boot-a", "0.1", "long", now),
	}}
	hub, _ := realtime.NewHub(32, func() time.Time { return now })
	service, _ := NewService(source, NewMemoryStore(), hub, func() time.Time { return now })
	first, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	err = service.applySourceEvent(context.Background(), "principal-a", "acct-a", SourceEvent{
		Kind: SourceEventPosition, BootID: "boot-a", Sequence: 2, Cursor: "boot-a:2", ResourceVersion: 2,
		Operation: OperationUpsert, AccountID: "acct-a", RouteID: "perp-usdt",
		InstrumentID: "binance:live:perp:USDT:BTC-USDT", PositionSlot: "long", OccurredAt: now,
		Position: SourcePosition{PositionSlot: "long", Side: "buy", Quantity: "0.2", EntryPrice: "70000", MarkPrice: "71000", UnrealizedPnL: "200"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if second.ProjectionRevision <= first.ProjectionRevision || second.Cursor == first.Cursor || second.Items[0].Quantity != "0.2" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

func TestServiceHydratesLastKnownRowsAsStaleAndKeepsPrincipalsIsolated(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	state := AccountState{Account: testAccount("acct-a", "Binance"), Freshness: FreshnessLive, RouteHealth: RouteLive, WriteReady: true, ReceivedAt: now}
	item := Item{Key: Key{AccountID: "acct-a", RouteID: "perp-usdt", InstrumentID: "binance:live:perp:USDT:BTC-USDT", PositionSlot: "net"}, Quantity: "1", Freshness: FreshnessLive, ReceivedAt: now}
	if err := store.ReplaceAccount(context.Background(), "principal-a", state, []Item{item}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{listErr: errors.New("directory down")}
	hub, _ := realtime.NewHub(32, func() time.Time { return now })
	service, _ := NewService(source, store, hub, func() time.Time { return now })
	snapshot, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Items) != 1 || snapshot.Items[0].Freshness != FreshnessStale || snapshot.Items[0].WriteReady {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, err := service.Snapshot(context.Background(), "principal-b", Filter{}); err == nil {
		t.Fatal("unrelated principal read persisted rows")
	}
}

func TestServiceKeepsRemovalTombstoneAgainstRegressedResourceVersion(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{accounts: []Account{testAccount("acct-a", "Binance")}, snapshots: map[string]SourceSnapshot{
		"acct-a": testSourceSnapshot("acct-a", "boot-a", "0.1", "long", now),
	}}
	hub, _ := realtime.NewHub(32, func() time.Time { return now })
	service, _ := NewService(source, NewMemoryStore(), hub, func() time.Time { return now })
	if _, err := service.Snapshot(context.Background(), "principal-a", Filter{}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	remove := SourceEvent{
		Kind: SourceEventPosition, BootID: "boot-a", Sequence: 2, Cursor: "boot-a:2", ResourceVersion: 2,
		Operation: OperationRemove, AccountID: "acct-a", RouteID: "perp-usdt",
		InstrumentID: "binance:live:perp:USDT:BTC-USDT", PositionSlot: "long", OccurredAt: now,
		Position: SourcePosition{PositionSlot: "long", Quantity: "0", EntryPrice: "0", MarkPrice: "0", UnrealizedPnL: "0"},
	}
	if err := service.applySourceEvent(context.Background(), "principal-a", "acct-a", remove); err != nil {
		t.Fatal(err)
	}
	regressed := remove
	regressed.Sequence, regressed.Cursor, regressed.ResourceVersion, regressed.Operation = 3, "boot-a:3", 1, OperationUpsert
	regressed.Position = SourcePosition{PositionSlot: "long", Side: "buy", Quantity: "0.1", EntryPrice: "70000", MarkPrice: "71000", UnrealizedPnL: "100"}
	if err := service.applySourceEvent(context.Background(), "principal-a", "acct-a", regressed); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil || len(snapshot.Items) != 0 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}

func TestServicePropagatesDelayedSnapshotAsDegradedAndNotWriteReady(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	delayed := testSourceSnapshot("acct-a", "boot-a", "1", "net", now)
	delayed.Freshness = FreshnessDelayed
	source := &fakeSource{accounts: []Account{testAccount("acct-a", "Binance")}, snapshots: map[string]SourceSnapshot{"acct-a": delayed}}
	hub, _ := realtime.NewHub(8, func() time.Time { return now })
	service, _ := NewService(source, NewMemoryStore(), hub, func() time.Time { return now })
	snapshot, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Accounts[0].RouteHealth != RouteDegraded || snapshot.Accounts[0].WriteReady || snapshot.Items[0].Freshness != FreshnessDelayed {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestProjectSourceItemRejectsInvalidOptionalDecimals(t *testing.T) {
	now := time.Now().UTC()
	account := testAccount("acct-a", "Binance")
	state := AccountState{Account: account, Freshness: FreshnessLive, RouteHealth: RouteLive, VerifiedAt: now}
	_, err := projectSourceItem(account, state, SourceItem{
		AccountID: "acct-a", RouteID: "perp-usdt", InstrumentID: "binance:live:perp:USDT:BTC-USDT", PositionSlot: "net",
		ResourceVersion: 1, ObservedAt: now, VerifiedAt: now,
		Position: SourcePosition{PositionSlot: "net", Side: "buy", Quantity: "1", EntryPrice: "1", MarkPrice: "1", UnrealizedPnL: "0", Leverage: OptionalDecimal{Valid: true, Value: "not-a-decimal"}},
	}, "boot-a", 1, now)
	if err == nil {
		t.Fatal("invalid leverage was accepted")
	}
}

type fakeSource struct {
	accounts  []Account
	snapshots map[string]SourceSnapshot
	errors    map[string]error
	listErr   error
}

func (source *fakeSource) ListAccounts(context.Context, string) ([]Account, error) {
	return append([]Account(nil), source.accounts...), source.listErr
}

func (source *fakeSource) PositionSnapshot(_ context.Context, _ string, accountID string) (SourceSnapshot, error) {
	if err := source.errors[accountID]; err != nil {
		return SourceSnapshot{}, err
	}
	return source.snapshots[accountID], nil
}

func (*fakeSource) WatchPositionEvents(ctx context.Context, _, _, _ string, _ func(SourceEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func testAccount(accountID, venue string) Account {
	return Account{
		AccountID: accountID, Label: accountID, Venue: venue, Environment: "live", Status: "active",
		Routes: []AccountRoute{{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT", RequestedState: "active"}},
	}
}

func testSourceSnapshot(accountID, bootID, quantity, slot string, now time.Time) SourceSnapshot {
	return SourceSnapshot{
		AccountID: accountID, BootID: bootID, Sequence: 1, Cursor: bootID + ":1", Freshness: FreshnessLive, VerifiedAt: now,
		Items: []SourceItem{{
			AccountID: accountID, RouteID: "perp-usdt", InstrumentID: "binance:live:perp:USDT:BTC-USDT",
			PositionSlot: slot, ResourceVersion: 1, ObservedAt: now, VerifiedAt: now,
			Position: SourcePosition{PositionSlot: slot, Side: "buy", Quantity: quantity, EntryPrice: "70000", MarkPrice: "71000", UnrealizedPnL: "100"},
		}},
	}
}
