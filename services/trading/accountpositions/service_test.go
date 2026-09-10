package accountpositions

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceKeepsHealthyAccountsWhenSiblingSnapshotFails(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{
		accounts: []Account{testAccount("acct-a", "Binance"), testAccount("acct-b", "OKX")},
		snapshots: map[string]SourceSnapshot{
			"acct-a": testSourceSnapshot("acct-a", "0.1", "long", now),
		},
		errors: map[string]error{"acct-b": errors.New("venue unavailable")},
	}
	service, err := NewService(source, func() time.Time { return now })
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

func TestServiceKeepsFailedInitialSnapshotUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	source := &fakeSource{
		accounts: []Account{testAccount("acct-a", "Binance")},
		errors:   map[string]error{"acct-a": errors.New("position snapshot unavailable")},
	}
	service, err := NewService(source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	initial, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Accounts) != 1 || initial.Accounts[0].Freshness != FreshnessUnavailable {
		t.Fatalf("initial=%+v", initial)
	}
	after, err := service.Snapshot(context.Background(), "principal-a", Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if after.Accounts[0].Freshness != FreshnessUnavailable || after.Accounts[0].RouteHealth != RouteUnavailable || len(after.Items) != 0 {
		t.Fatalf("after=%+v", after)
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
	}, now)
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

func testAccount(accountID, venue string) Account {
	return Account{
		AccountID: accountID, Label: accountID, Venue: venue, Environment: "live", Status: "active",
		Routes: []AccountRoute{{RouteID: "perp-usdt", Product: "perp", SettlementAsset: "USDT"}},
	}
}

func testSourceSnapshot(accountID, quantity, slot string, now time.Time) SourceSnapshot {
	return SourceSnapshot{
		AccountID: accountID, Freshness: FreshnessLive, VerifiedAt: now,
		Items: []SourceItem{{
			AccountID: accountID, RouteID: "perp-usdt", InstrumentID: "binance:live:perp:USDT:BTC-USDT",
			PositionSlot: slot, ResourceVersion: 1, ObservedAt: now, VerifiedAt: now,
			Position: SourcePosition{PositionSlot: slot, Side: "buy", Quantity: quantity, EntryPrice: "70000", MarkPrice: "71000", UnrealizedPnL: "100"},
		}},
	}
}

func TestServiceReadsCurrentVenuePositionsWithoutHistory(t *testing.T) {
	now := time.Now().UTC()
	source := &fakeSource{accounts: []Account{testAccount("acct-a", "Binance")}, snapshots: map[string]SourceSnapshot{"acct-a": testSourceSnapshot("acct-a", "2", "long", now)}}
	service, err := NewService(source, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Snapshot(t.Context(), "owner", Filter{})
	if err != nil || len(first.Items) != 1 || first.Items[0].Quantity != "2" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// An externally closed position disappears on the next successful read.
	source.snapshots["acct-a"] = SourceSnapshot{AccountID: "acct-a", Freshness: FreshnessLive, VerifiedAt: now}
	flat, err := service.Snapshot(t.Context(), "owner", Filter{})
	if err != nil || len(flat.Items) != 0 || flat.Accounts[0].Freshness != FreshnessLive {
		t.Fatalf("flat=%+v err=%v", flat, err)
	}
	source.errors = map[string]error{"acct-a": errors.New("venue unavailable")}
	failed, err := service.Snapshot(t.Context(), "owner", Filter{})
	if err != nil || failed.Accounts[0].Freshness != FreshnessUnavailable || failed.Accounts[0].WriteReady {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	// Local deletion removes the account from the next query, with no tombstone or replay.
	source.accounts = nil
	removed, err := service.Snapshot(t.Context(), "owner", Filter{})
	if err != nil || len(removed.Accounts) != 0 || len(removed.Items) != 0 {
		t.Fatalf("removed=%+v err=%v", removed, err)
	}
}
