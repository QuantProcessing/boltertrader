package account

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

func TestBuildAccountStateMergesPerpAndSpotWithoutDoubleCountingUSDC(t *testing.T) {
	now := time.Unix(1700000000, 0)
	state, err := BuildAccountState(StateInput{
		AccountID:         model.AccountIDHyperliquidDefault,
		AccountMode:       sdk.AccountAbstractionDefault,
		Perp:              perpState("100", "88", "12", "7"),
		Spot:              spotState(rawSpotBalance("USDC", "10", "1.5"), rawSpotBalance("PURR", "2", "0.5")),
		ProductScope:      []enums.InstrumentKind{enums.KindSpot, enums.KindPerp},
		Now:               now,
		Details:           map[string]string{"account_address": "0xabc"},
		AccountModeSource: "userAbstraction",
	})
	if err != nil {
		t.Fatalf("BuildAccountState: %v", err)
	}
	if state.AccountID != model.AccountIDHyperliquidDefault || state.Venue != "HYPERLIQUID" || state.Type != model.AccountMargin || state.BaseCurrency != "USDC" {
		t.Fatalf("unexpected account identity/type: %+v", state)
	}
	usdc := mustBalance(t, state, "USDC")
	if !usdc.Total.Equal(d("100")) || !usdc.Free.Equal(d("88")) || !usdc.Locked.Equal(d("12")) {
		t.Fatalf("USDC balance=%+v, want total=100 free=88 locked=12", usdc)
	}
	purr := mustBalance(t, state, "PURR")
	if !purr.Total.Equal(d("2")) || !purr.Free.Equal(d("1.5")) || !purr.Locked.Equal(d("0.5")) {
		t.Fatalf("PURR balance=%+v, want total=2 free=1.5 locked=0.5", purr)
	}
	if len(state.Margins) != 1 || state.Margins[0].Currency != "USDC" || !state.Margins[0].Initial.Equal(d("12")) || !state.Margins[0].Maintenance.Equal(d("7")) {
		t.Fatalf("margins=%+v, want USDC initial=12 maintenance=7", state.Margins)
	}
	if state.ModeInfo.AccountMode != "default" || state.ModeInfo.MarginMode != "cross" || state.ModeInfo.PositionMode != "net" || state.ModeInfo.CollateralMode != "single_usdc" {
		t.Fatalf("mode info=%+v", state.ModeInfo)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("state invalid: %v", err)
	}
	if err := state.ModeInfo.ValidateVerified(); err != nil {
		t.Fatalf("mode info invalid: %v", err)
	}
}

func TestBuildAccountStateUsesSpotUSDCWhenPerpSummaryIsEmpty(t *testing.T) {
	state, err := BuildAccountState(StateInput{
		AccountID:   model.AccountIDHyperliquidDefault,
		AccountMode: sdk.AccountAbstractionDefault,
		Perp:        perpState("0", "", "0", "0"),
		Spot:        spotState(rawSpotBalance("USDC", "10", "1.5")),
		Now:         time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("BuildAccountState: %v", err)
	}
	usdc := mustBalance(t, state, "USDC")
	if !usdc.Total.Equal(d("10")) || !usdc.Free.Equal(d("8.5")) || !usdc.Locked.Equal(d("1.5")) {
		t.Fatalf("USDC balance=%+v, want spot-derived", usdc)
	}
	if len(state.Margins) != 0 {
		t.Fatalf("margins=%+v, want none for zero perp summary", state.Margins)
	}
}

func TestBuildAccountStateMapsAccountAbstractions(t *testing.T) {
	tests := []struct {
		mode       sdk.AccountAbstraction
		collateral string
	}{
		{sdk.AccountAbstractionDefault, "single_usdc"},
		{sdk.AccountAbstractionUnifiedAccount, "unified"},
		{sdk.AccountAbstractionPortfolioMargin, "portfolio_margin"},
	}
	for _, tt := range tests {
		state, err := BuildAccountState(StateInput{
			AccountID:   model.AccountIDHyperliquidDefault,
			AccountMode: tt.mode,
			Perp:        perpState("1", "1", "0", "0"),
			Spot:        spotState(),
			Now:         time.Unix(1700000000, 0),
		})
		if err != nil {
			t.Fatalf("BuildAccountState(%s): %v", tt.mode, err)
		}
		if state.ModeInfo.AccountMode != string(tt.mode) || state.ModeInfo.CollateralMode != tt.collateral {
			t.Fatalf("mode=%s modeInfo=%+v, want collateral=%s", tt.mode, state.ModeInfo, tt.collateral)
		}
	}
}

func TestBuildAccountStateFailsClosedForPartialOrMalformedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		in   StateInput
	}{
		{
			name: "missing perp",
			in: StateInput{
				AccountID:   model.AccountIDHyperliquidDefault,
				AccountMode: sdk.AccountAbstractionDefault,
				Spot:        spotState(),
			},
		},
		{
			name: "missing spot",
			in: StateInput{
				AccountID:   model.AccountIDHyperliquidDefault,
				AccountMode: sdk.AccountAbstractionDefault,
				Perp:        perpState("1", "1", "0", "0"),
			},
		},
		{
			name: "unknown account mode",
			in: StateInput{
				AccountID:   model.AccountIDHyperliquidDefault,
				AccountMode: sdk.AccountAbstractionUnknown,
				Perp:        perpState("1", "1", "0", "0"),
				Spot:        spotState(),
			},
		},
		{
			name: "bad perp number",
			in: StateInput{
				AccountID:   model.AccountIDHyperliquidDefault,
				AccountMode: sdk.AccountAbstractionDefault,
				Perp:        perpState("bad", "1", "0", "0"),
				Spot:        spotState(),
			},
		},
		{
			name: "bad spot number",
			in: StateInput{
				AccountID:   model.AccountIDHyperliquidDefault,
				AccountMode: sdk.AccountAbstractionDefault,
				Perp:        perpState("1", "1", "0", "0"),
				Spot:        spotState(rawSpotBalance("USDC", "10", "bad")),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.Now = time.Unix(1700000000, 0)
			if _, err := BuildAccountState(tt.in); err == nil {
				t.Fatal("BuildAccountState err=nil, want fail-closed error")
			}
		})
	}
}

func perpState(totalRawUSD, withdrawable, marginUsed, maintenance string) *sdkperp.PerpPosition {
	var state sdkperp.PerpPosition
	state.CrossMarginSummary.TotalRawUsd = totalRawUSD
	state.CrossMarginSummary.TotalMarginUsed = marginUsed
	state.MarginSummary.TotalRawUsd = totalRawUSD
	state.MarginSummary.TotalMarginUsed = marginUsed
	state.CrossMaintenanceMarginUsed = maintenance
	state.Withdrawable = withdrawable
	state.Time = 1700000000000
	return &state
}

func spotState(balances ...sdk.SpotBalance) *sdk.SpotClearinghouseState {
	return &sdk.SpotClearinghouseState{Balances: balances}
}

func rawSpotBalance(coin, total, hold string) sdk.SpotBalance {
	return sdk.SpotBalance{Coin: coin, Total: total, Hold: hold}
}

func mustBalance(t *testing.T, state model.AccountState, currency string) model.AccountBalance {
	t.Helper()
	for _, bal := range state.Balances {
		if bal.Currency == currency {
			return bal
		}
	}
	t.Fatalf("missing %s balance in %+v", currency, state.Balances)
	return model.AccountBalance{}
}

func d(s string) decimal.Decimal {
	return decimal.RequireFromString(s)
}
