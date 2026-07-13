package model

import (
	"testing"
	"time"

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
		"gate":        AccountIDGateDefault,
		"lighter":     AccountIDLighterDefault,
		"hyperliquid": AccountIDHyperliquidDefault,
		" aster ":     AccountIDAsterDefault,
		"NADO":        AccountIDNadoDefault,
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

func TestAccountStateSummaryValidationAndClone(t *testing.T) {
	now := time.Unix(50, 0)
	state := AccountState{
		AccountID: AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      AccountMargin,
		Summary: &AccountSummary{
			SettlementCurrency:  "USDT",
			Equity:              decimal.RequireFromString("-1.25"),
			AvailableCollateral: decimal.RequireFromString("0"),
			UpdatedAt:           now,
		},
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("negative equity should be accepted when collateral is non-negative: %v", err)
	}

	clone := CloneAccountState(state)
	if clone.Summary == nil {
		t.Fatal("clone summary is nil")
	}
	clone.Summary.SettlementCurrency = "USDC"
	clone.Summary.AvailableCollateral = decimal.RequireFromString("10")
	if state.Summary.SettlementCurrency != "USDT" {
		t.Fatalf("clone summary aliased original currency: %s", state.Summary.SettlementCurrency)
	}
	if !state.Summary.AvailableCollateral.IsZero() {
		t.Fatalf("clone summary aliased original collateral: %s", state.Summary.AvailableCollateral)
	}

	state.Summary.AvailableCollateral = decimal.RequireFromString("-0.01")
	if err := state.Validate(); err == nil {
		t.Fatal("negative available collateral should fail validation")
	}
	state.Summary.AvailableCollateral = decimal.Zero

	state.Summary.SettlementCurrency = ""
	if err := state.Validate(); err == nil {
		t.Fatal("missing settlement currency should fail validation")
	}
	state.Summary.SettlementCurrency = "USDT"

	state.Summary.UpdatedAt = time.Time{}
	if err := state.Validate(); err == nil {
		t.Fatal("missing summary timestamp should fail validation")
	}
}

func TestAccountStateSummaryNilCompatibility(t *testing.T) {
	state := AccountState{
		AccountID: AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      AccountCash,
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("nil summary should preserve existing account-state compatibility: %v", err)
	}
	clone := CloneAccountState(state)
	if clone.Summary != nil {
		t.Fatalf("nil summary clone=%+v, want nil", clone.Summary)
	}
}

func TestAccountStateEventIDUsesCanonicalInstant(t *testing.T) {
	instant := time.Date(2026, 7, 11, 0, 0, 0, 123, time.UTC)
	local := instant.In(time.FixedZone("test-zone", 8*60*60))
	utcID := AccountStateEventID("T", "T:acct", instant)
	localID := AccountStateEventID("T", "T:acct", local)
	if utcID != localID {
		t.Fatalf("same instant produced different event ids: utc=%q local=%q", utcID, localID)
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
		Reported: true,
		EventID:  AccountStateEventID("BINANCE", AccountIDBinanceDefault, now),
		TsEvent:  now,
		TsInit:   now,
	}
	fresh := AccountFreshness{LastAccountStateAt: now, LastReconciledAt: now, StaleAfter: time.Minute}
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err != nil {
		t.Fatalf("valid trading-ready state rejected: %v", err)
	}

	state.Reported = false
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("unreported state should reject trading-ready state")
	}
	state.Reported = true

	state.EventID = ""
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("missing event id should reject trading-ready state")
	}
	state.EventID = AccountStateEventID("BINANCE", AccountIDBinanceDefault, now)

	state.TsEvent = time.Time{}
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("missing event timestamp should reject trading-ready state")
	}
	state.TsEvent = now

	state.TsInit = time.Time{}
	if err := state.ValidateTradingReady(fresh, now.Add(time.Second)); err == nil {
		t.Fatal("missing init timestamp should reject trading-ready state")
	}
	state.TsInit = now

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
