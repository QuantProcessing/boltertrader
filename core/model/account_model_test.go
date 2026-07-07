package model

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

func TestAccountTypeValid(t *testing.T) {
	if !AccountCash.Valid() || !AccountMargin.Valid() {
		t.Fatal("cash and margin account types should be valid")
	}
	if AccountTypeUnknown.Valid() {
		t.Fatal("unknown account type should be invalid")
	}
}

func TestDefaultAccountIDForVenue(t *testing.T) {
	tests := map[string]string{
		"binance":     AccountIDBinanceDefault,
		" OKX ":       AccountIDOKXDefault,
		"bybit":       AccountIDBybitDefault,
		"bitget":      AccountIDBitgetDefault,
		"lighter":     AccountIDLighterDefault,
		"hyperliquid": AccountIDHyperliquidDefault,
	}
	for venue, want := range tests {
		if got := DefaultAccountIDForVenue(venue); got != want {
			t.Fatalf("DefaultAccountIDForVenue(%q)=%q, want %q", venue, got, want)
		}
	}
	if got := DefaultAccountIDForVenue("unknown"); got != "" {
		t.Fatalf("unknown venue default=%q, want empty", got)
	}
}

func TestAccountBalanceFreeMigrationCompatibility(t *testing.T) {
	old := AccountBalance{
		Currency:  "USDT",
		Total:     decimal.RequireFromString("100"),
		Available: decimal.RequireFromString("80"),
		Locked:    decimal.RequireFromString("20"),
	}
	if got := old.FreeOrAvailable(); !got.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("FreeOrAvailable()=%s, want 80", got)
	}
	if !old.CashInvariantOK() {
		t.Fatal("cash invariant should use Available while Free is absent")
	}
	normalized := old.Normalized()
	if !normalized.Free.Equal(old.Available) {
		t.Fatalf("normalized Free=%s, want %s", normalized.Free, old.Available)
	}

	next := AccountBalance{
		Currency: "USDT",
		Total:    decimal.RequireFromString("100"),
		Free:     decimal.RequireFromString("75"),
		Locked:   decimal.RequireFromString("25"),
	}
	if got := next.FreeOrAvailable(); !got.Equal(decimal.RequireFromString("75")) {
		t.Fatalf("FreeOrAvailable()=%s, want 75", got)
	}
	normalized = next.Normalized()
	if !normalized.Available.Equal(next.Free) {
		t.Fatalf("normalized Available=%s, want %s", normalized.Available, next.Free)
	}
}

func TestAccountStateValidateTradingReady(t *testing.T) {
	now := time.Unix(100, 0)
	state := AccountState{
		AccountID: AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      AccountCash,
		Balances: []AccountBalance{{
			Currency: "USDT",
			Total:    decimal.RequireFromString("100"),
			Free:     decimal.RequireFromString("100"),
		}},
		ModeInfo: AccountModeInfo{
			Venue:          "BINANCE",
			AccountID:      AccountIDBinanceDefault,
			AccountMode:    "spot",
			MarginMode:     "none",
			PositionMode:   "net",
			CollateralMode: "cash",
			ProductScope:   []enums.InstrumentKind{enums.KindSpot},
			Verified:       true,
			VerifiedAt:     now,
			Source:         "fixture",
		},
		Reported: true,
		TsEvent:  now,
		TsInit:   now,
	}
	fresh := AccountFreshness{LastAccountStateAt: now, LastReconciledAt: now, StaleAfter: time.Minute}
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err != nil {
		t.Fatalf("valid trading-ready state rejected: %v", err)
	}

	state.ModeInfo.Verified = false
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("unverified mode info should reject trading-ready state")
	}
	state.ModeInfo.Verified = true
	state.ModeInfo.ProductScope = nil
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("verified mode info without product scope should reject trading-ready state")
	}
	state.ModeInfo.ProductScope = []enums.InstrumentKind{enums.KindSpot}

	state.AccountID = ""
	if err := state.Validate(); err == nil {
		t.Fatal("missing account id should fail validation")
	}
}

func TestAccountFreshnessRejectsStaleOrDisabled(t *testing.T) {
	now := time.Unix(200, 0)
	fresh := AccountFreshness{LastAccountStateAt: now, StaleAfter: time.Second}
	if err := fresh.ValidateTradingReady(now.Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("fresh account rejected: %v", err)
	}
	if err := fresh.ValidateTradingReady(now.Add(2 * time.Second)); err == nil {
		t.Fatal("stale account should fail trading-ready validation")
	}
	fresh.StaleAfter = 0
	if err := fresh.ValidateTradingReady(now); err == nil {
		t.Fatal("zero StaleAfter should fail trading-ready validation")
	}
}

func TestAccountFreshnessUsesReconciliationAsSnapshotFreshness(t *testing.T) {
	now := time.Unix(300, 0)
	fresh := AccountFreshness{
		LastAccountStateAt: now.Add(-time.Hour),
		LastReconciledAt:   now,
		StaleAfter:         time.Second,
	}
	if got := fresh.LastFreshAt(); !got.Equal(now) {
		t.Fatalf("LastFreshAt=%s, want reconciliation time %s", got, now)
	}
	if err := fresh.ValidateTradingReady(now.Add(500 * time.Millisecond)); err != nil {
		t.Fatalf("recently reconciled account rejected: %v", err)
	}
	if err := fresh.ValidateTradingReady(now.Add(2 * time.Second)); err == nil {
		t.Fatal("reconciled account should stale after stale-after elapses")
	}
}

func TestMarginBalanceValidateRejectsNegative(t *testing.T) {
	mb := MarginBalance{Currency: "USDT", Initial: decimal.RequireFromString("-1")}
	if err := mb.Validate(); err == nil {
		t.Fatal("negative initial margin should fail validation")
	}
	mb.Initial = decimal.Zero
	mb.Maintenance = decimal.RequireFromString("-0.1")
	if err := mb.Validate(); err == nil {
		t.Fatal("negative maintenance margin should fail validation")
	}
}
