package perp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestHyperliquidPerpConfiguredHIP3UnifiedSnapshotsUseSpotFundingAndClearPositions(t *testing.T) {
	provider := testProvider(t)
	standardCalls := 0
	dexCalls := 0
	spotCalls := 0
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		if r.URL.Path != "/info" {
			t.Fatalf("path=%s, want /info", r.URL.Path)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["type"] {
		case "clearinghouseState":
			dex, _ := req["dex"].(string)
			if dex == "" {
				standardCalls++
				if standardCalls == 1 {
					return perpAccountSnapshot("BTC", "0.01", "100", "88", "12", "7"), http.StatusOK
				}
				if standardCalls == 2 {
					return perpAccountSnapshot("BTC", "0.01", "100", "88", "12", "7"), http.StatusOK
				}
				return perpAccountSnapshot("", "", "100", "88", "12", "7"), http.StatusOK
			}
			if dex != "testdex" {
				t.Fatalf("clearinghouseState dex=%q, want testdex", dex)
			}
			dexCalls++
			if dexCalls == 1 {
				return perpAccountSnapshot("COIN", "2", "20", "15", "5", "2"), http.StatusOK
			}
			if dexCalls == 2 {
				return perpAccountSnapshot("COIN", "2", "20", "15", "5", "2"), http.StatusOK
			}
			return perpAccountSnapshot("", "", "20", "15", "5", "2"), http.StatusOK
		case "spotClearinghouseState":
			spotCalls++
			return `{"balances":[{"coin":"USDC","token":0,"hold":"1","total":"10","entryNtl":"0"},{"coin":"PURR","token":1,"hold":"0.5","total":"2","entryNtl":"0"}]}`, http.StatusOK
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return `{}`, http.StatusOK
	})
	acct := newAccountClient(rest, provider, clock.NewSimulatedClock(time.Unix(1700000000, 0)), "cross", d("1"), sdk.AccountAbstractionUnifiedAccount, model.AccountIDHyperliquidDefault).
		withHIP3Dexes([]string{" testdex ", "testdex", ""})

	state, err := acct.AccountState(context.Background())
	if err != nil {
		t.Fatalf("AccountState: %v", err)
	}
	if len(state.Balances) != 2 {
		t.Fatalf("balances=%+v, want unified spot USDC plus PURR", state.Balances)
	}
	if got := state.Balances[0]; got.Currency != "USDC" || !got.Total.Equal(d("10")) || !got.Free.Equal(d("9")) || !got.Locked.Equal(d("1")) {
		t.Fatalf("unified USDC=%+v, want spot total=10 free=9 locked=1", got)
	}
	if len(state.Margins) != 1 || !state.Margins[0].Initial.Equal(d("17")) || !state.Margins[0].Maintenance.Equal(d("9")) {
		t.Fatalf("margins=%+v, want aggregated USDC margin", state.Margins)
	}

	positions, err := acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if len(positions) != 2 {
		t.Fatalf("positions=%+v, want standard and HIP-3", positions)
	}
	if positions[0].InstrumentID != testPerpID() || positions[0].Side != enums.PosNet || !positions[0].Quantity.Equal(d("0.01")) {
		t.Fatalf("standard position=%+v", positions[0])
	}
	if positions[1].InstrumentID != testHIP3ID() || positions[1].Side != enums.PosNet || !positions[1].Quantity.Equal(d("2")) {
		t.Fatalf("HIP-3 position=%+v; unqualified DEX coin must resolve through configured dex", positions[1])
	}

	positions, err = acct.Positions(context.Background())
	if err != nil {
		t.Fatalf("flat Positions: %v", err)
	}
	if len(positions) != 0 {
		t.Fatalf("flat positions=%+v, want empty snapshot that clears prior standard and HIP-3 positions", positions)
	}
	if standardCalls != 3 || dexCalls != 3 || spotCalls != 1 {
		t.Fatalf("snapshot calls standard=%d dex=%d spot=%d, want 3/3/1 (configured dex deduplicated)", standardCalls, dexCalls, spotCalls)
	}
}

func TestHyperliquidPerpConfiguredHIP3StandardAccountStateRejectsSameCurrencyPools(t *testing.T) {
	provider := testProvider(t)
	standardCalls := 0
	dexCalls := 0
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch req["type"] {
		case "clearinghouseState":
			if req["dex"] == "testdex" {
				dexCalls++
				return perpAccountSnapshot("COIN", "2", "20", "15", "5", "2"), http.StatusOK
			}
			standardCalls++
			return perpAccountSnapshot("BTC", "0.01", "100", "88", "12", "7"), http.StatusOK
		case "spotClearinghouseState":
			return `{"balances":[{"coin":"USDC","token":0,"hold":"1","total":"10","entryNtl":"0"}]}`, http.StatusOK
		default:
			t.Fatalf("unexpected request: %s", body)
		}
		return `{}`, http.StatusOK
	})
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault, model.AccountIDHyperliquidDefault).
		withHIP3Dexes([]string{"testdex"})

	state, err := acct.AccountState(context.Background())
	if err == nil || !strings.Contains(err.Error(), "independent") {
		t.Fatalf("AccountState=%+v err=%v, want fail-closed independent pool error", state, err)
	}
	if state.AccountID != "" || len(state.Balances) != 0 || len(state.Margins) != 0 {
		t.Fatalf("partial AccountState escaped on pool collision: %+v", state)
	}
	if standardCalls != 1 || dexCalls != 1 {
		t.Fatalf("clearinghouse requests standard=%d dex=%d, want both snapshots before fail-closed classification", standardCalls, dexCalls)
	}
}

func TestHyperliquidPerpConfiguredHIP3SnapshotErrorsFailClosed(t *testing.T) {
	provider := testProvider(t)
	newAccount := func() *accountClient {
		rest := testREST(func(r *http.Request, body []byte) (string, int) {
			var req map[string]any
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			switch req["type"] {
			case "clearinghouseState":
				if req["dex"] == "testdex" {
					return `{"error":"forced dex failure"}`, http.StatusInternalServerError
				}
				return perpAccountSnapshot("BTC", "0.01", "100", "88", "12", "7"), http.StatusOK
			case "spotClearinghouseState":
				t.Fatal("spot snapshot must not be requested after configured DEX failure")
			default:
				t.Fatalf("unexpected request: %s", body)
			}
			return `{}`, http.StatusOK
		})
		return newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault, model.AccountIDHyperliquidDefault).
			withHIP3Dexes([]string{"testdex"})
	}

	if positions, err := newAccount().Positions(context.Background()); err == nil || positions != nil {
		t.Fatalf("Positions=%+v err=%v, want nil partial result and DEX error", positions, err)
	}
	if state, err := newAccount().AccountState(context.Background()); err == nil || state.AccountID != "" || len(state.Balances) != 0 || len(state.Margins) != 0 {
		t.Fatalf("AccountState=%+v err=%v, want zero partial result and DEX error", state, err)
	}
}

func TestHyperliquidPerpHIP3CollateralComesFromInstrumentSettlementMetadata(t *testing.T) {
	provider := instruments.NewRegistry(&model.Instrument{
		ID:          model.InstrumentID{Venue: venueName, Symbol: "testdex:COIN-USDT", Kind: enums.KindPerp},
		VenueSymbol: "testdex:COIN",
		Settle:      "USDT",
	})
	acct := newAccountClient(nil, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault).
		withHIP3Dexes([]string{"testdex"})

	got, err := acct.hip3Collateral("testdex")
	if err != nil {
		t.Fatalf("hip3Collateral: %v", err)
	}
	if got != "USDT" {
		t.Fatalf("HIP-3 collateral=%q, want instrument settle USDT", got)
	}
}

func TestHyperliquidPerpHIP3PositionNeverFallsBackToStandardSymbol(t *testing.T) {
	provider := instruments.NewRegistry(
		&model.Instrument{
			ID:          model.InstrumentID{Venue: venueName, Symbol: "COIN-USDC", Kind: enums.KindPerp},
			VenueSymbol: "COIN",
			Settle:      "USDC",
		},
		&model.Instrument{
			ID:          model.InstrumentID{Venue: venueName, Symbol: "testdex:OTHER-USDC", Kind: enums.KindPerp},
			VenueSymbol: "testdex:OTHER",
			Settle:      "USDC",
		},
	)
	rest := testREST(func(r *http.Request, body []byte) (string, int) {
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["dex"] == "testdex" {
			return perpAccountSnapshot("COIN", "2", "20", "15", "5", "2"), http.StatusOK
		}
		return perpAccountSnapshot("", "", "100", "88", "12", "7"), http.StatusOK
	})
	acct := newAccountClient(rest, provider, clock.NewRealClock(), "cross", d("1"), sdk.AccountAbstractionDefault).
		withHIP3Dexes([]string{"testdex"})

	positions, err := acct.Positions(context.Background())
	if err == nil || !strings.Contains(err.Error(), "testdex:COIN") {
		t.Fatalf("Positions=%+v err=%v, want unresolved dex-qualified position error", positions, err)
	}
	if positions != nil {
		t.Fatalf("partial/misidentified positions escaped: %+v", positions)
	}
}

func perpAccountSnapshot(coin, size, total, free, margin, maintenance string) string {
	positions := `[]`
	if strings.TrimSpace(coin) != "" {
		positions = `[{"position":{"coin":"` + coin + `","entryPx":"10","leverage":{"type":"cross","value":5},"szi":"` + size + `","unrealizedPnl":"1","marginUsed":"` + margin + `","positionValue":"20"}}]`
	}
	return `{"assetPositions":` + positions + `,"crossMarginSummary":{"accountValue":"` + total + `","totalMarginUsed":"` + margin + `","totalNtlPos":"20","totalRawUsd":"` + total + `"},"marginSummary":{"accountValue":"` + total + `","totalMarginUsed":"` + margin + `","totalNtlPos":"20","totalRawUsd":"` + total + `"},"time":1700000000000,"withdrawable":"` + free + `","crossMaintenanceMarginUsed":"` + maintenance + `"}`
}
