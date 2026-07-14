package account

import (
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestBuildAccountStateStandardRejectsSameCurrencyPerpPools(t *testing.T) {
	_, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionDefault,
		Perp:        perpState("100", "88", "12", "7"),
		PerpDexes: []PerpDexState{{
			Dex:        "usdc-dex",
			Collateral: "USDC",
			State:      perpState("20", "15", "5", "2"),
		}},
		Spot: spotState(rawSpotBalance("USDC", "10", "1")),
		Now:  time.Unix(1700000000, 0),
	})
	if err == nil || !strings.Contains(err.Error(), "independent") {
		t.Fatalf("BuildAccountState err=%v, want fail-closed independent same-currency pool error", err)
	}
}

func TestBuildAccountStateStandardKeepsDistinctPerpCollateralCurrencies(t *testing.T) {
	now := time.Unix(1700000000, 0)
	state, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionDefault,
		Perp:        perpState("100", "88", "12", "7"),
		PerpDexes:   []PerpDexState{{Dex: "usdt-dex", Collateral: "USDT", State: perpState("50", "45", "5", "3")}},
		Spot: spotState(
			rawSpotBalance("USDC", "10", "1"),
			rawSpotBalance("USDT", "9", "1"),
			rawSpotBalance("PURR", "2", "0.5"),
		),
		Now: now,
	})
	if err != nil {
		t.Fatalf("BuildAccountState: %v", err)
	}

	usdc := mustBalance(t, state, "USDC")
	if !usdc.Total.Equal(d("100")) || !usdc.Free.Equal(d("88")) || !usdc.Locked.Equal(d("12")) {
		t.Fatalf("USDC balance=%+v, want default perp pool", usdc)
	}
	usdt := mustBalance(t, state, "USDT")
	if !usdt.Total.Equal(d("50")) || !usdt.Free.Equal(d("45")) || !usdt.Locked.Equal(d("5")) {
		t.Fatalf("USDT balance=%+v, want DEX total=50 free=45 locked=5", usdt)
	}
	purr := mustBalance(t, state, "PURR")
	if !purr.Total.Equal(d("2")) || !purr.Free.Equal(d("1.5")) || !purr.Locked.Equal(d("0.5")) {
		t.Fatalf("PURR balance=%+v, want spot-derived", purr)
	}
	if len(state.Balances) != 3 {
		t.Fatalf("balances=%+v, want one deduplicated row per currency", state.Balances)
	}

	if len(state.Margins) != 2 {
		t.Fatalf("margins=%+v, want USDC and USDT", state.Margins)
	}
	if state.Margins[0].Currency != "USDC" || !state.Margins[0].Initial.Equal(d("12")) || !state.Margins[0].Maintenance.Equal(d("7")) {
		t.Fatalf("USDC margin=%+v, want initial=12 maintenance=7", state.Margins[0])
	}
	if state.Margins[1].Currency != "USDT" || !state.Margins[1].Initial.Equal(d("5")) || !state.Margins[1].Maintenance.Equal(d("3")) {
		t.Fatalf("USDT margin=%+v, want initial=5 maintenance=3", state.Margins[1])
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state invalid: %v", err)
	}
}

func TestBuildAccountStateUnifiedUsesSpotFundingWithoutRepeatingPerpEquity(t *testing.T) {
	now := time.Unix(1700000000, 0)
	state, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionUnifiedAccount,
		Perp:        perpState("100", "88", "12", "7"),
		PerpDexes: []PerpDexState{
			{Dex: "usdc-dex", Collateral: "USDC", State: perpState("20", "15", "5", "2")},
			{Dex: "usdt-dex", Collateral: "USDT", State: perpState("50", "45", "5", "3")},
		},
		Spot: spotState(
			rawSpotBalance("USDC", "10", "1"),
			rawSpotBalance("USDT", "9", "1"),
			rawSpotBalance("PURR", "2", "0.5"),
		),
		Now: now,
	})
	if err != nil {
		t.Fatalf("BuildAccountState: %v", err)
	}

	usdc := mustBalance(t, state, "USDC")
	if !usdc.Total.Equal(d("10")) || !usdc.Free.Equal(d("9")) || !usdc.Locked.Equal(d("1")) {
		t.Fatalf("USDC balance=%+v, want spot unified funding only", usdc)
	}
	usdt := mustBalance(t, state, "USDT")
	if !usdt.Total.Equal(d("9")) || !usdt.Free.Equal(d("8")) || !usdt.Locked.Equal(d("1")) {
		t.Fatalf("USDT balance=%+v, want spot unified funding only", usdt)
	}
	if len(state.Balances) != 3 {
		t.Fatalf("balances=%+v, want three spot-funded currencies", state.Balances)
	}
	if len(state.Margins) != 2 {
		t.Fatalf("margins=%+v, want aggregated USDC and USDT usage", state.Margins)
	}
	if state.Margins[0].Currency != "USDC" || !state.Margins[0].Initial.Equal(d("17")) || !state.Margins[0].Maintenance.Equal(d("9")) {
		t.Fatalf("USDC margin=%+v, want initial=17 maintenance=9", state.Margins[0])
	}
	if state.Margins[1].Currency != "USDT" || !state.Margins[1].Initial.Equal(d("5")) || !state.Margins[1].Maintenance.Equal(d("3")) {
		t.Fatalf("USDT margin=%+v, want initial=5 maintenance=3", state.Margins[1])
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state invalid: %v", err)
	}
}

func TestBuildAccountStatePortfolioMarginUsesSpotFundingWithoutRepeatingPerpEquity(t *testing.T) {
	state, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionPortfolioMargin,
		Perp:        perpState("100", "88", "12", "7"),
		PerpDexes: []PerpDexState{{
			Dex:        "usdc-dex",
			Collateral: "USDC",
			State:      perpState("20", "15", "5", "2"),
		}},
		Spot: spotState(rawSpotBalance("USDC", "10", "1")),
		Now:  time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("BuildAccountState: %v", err)
	}
	usdc := mustBalance(t, state, "USDC")
	if !usdc.Total.Equal(d("10")) || !usdc.Free.Equal(d("9")) || !usdc.Locked.Equal(d("1")) {
		t.Fatalf("USDC balance=%+v, want portfolio spot funding only", usdc)
	}
	if len(state.Margins) != 1 || !state.Margins[0].Initial.Equal(d("17")) || !state.Margins[0].Maintenance.Equal(d("9")) {
		t.Fatalf("margins=%+v, want aggregated perp usage without repeated equity", state.Margins)
	}
}

func TestBuildAccountStateFailsClosedForMalformedPerpDexSnapshot(t *testing.T) {
	_, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionDefault,
		Perp:        perpState("100", "88", "12", "7"),
		PerpDexes: []PerpDexState{{
			Dex:        "bad-dex",
			Collateral: "USDT",
			State:      perpState("bad", "45", "5", "3"),
		}},
		Spot: spotState(),
		Now:  time.Unix(1700000000, 0),
	})
	if err == nil {
		t.Fatal("BuildAccountState err=nil, want fail-closed DEX parse error")
	}
}
