package factoryclient

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestBybitTickerFactKeepsAbsentDeltaFieldsUntouched(t *testing.T) {
	observedAt := time.Date(2026, time.August, 20, 1, 2, 3, 0, time.UTC)
	fact, fields := bybitTickerFact(fundingTestIdentity(), bybit.Ticker{FundingRate: "0.0001"}, observedAt)
	if fields != exchange.FundingMarketFieldFunding || !fact.FundingRate.Valid || fact.MarkPrice.Valid || fact.OpenInterest.Valid {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestBitgetTickerFactUsesUTAOpenInterest(t *testing.T) {
	fact, fields := bitgetTickerFact(fundingTestIdentity(), bitget.Ticker{
		OpenInterest: "12.5", MarkPrice: "200", FundingRate: "0.0002",
	}, time.Now().UTC())
	if !fields.Has(exchange.FundingMarketFieldOpenInterest) ||
		!fields.Has(exchange.FundingMarketFieldOpenInterestNotional) ||
		!fact.OpenInterest.Valid || !fact.OpenInterest.Value.Equal(decimal.RequireFromString("12.5")) ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(2500)) ||
		fact.OpenInterestNotionalAsset != "USDT" {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestGateTickerFactAppliesContractMultiplier(t *testing.T) {
	fact, fields := gateTickerFact(fundingTestIdentity(), gate.FuturesTicker{
		Contract: "BTC_USDT", MarkPrice: "200", TotalSize: "10",
	}, decimal.RequireFromString("0.001"), time.Now().UTC())
	if !fields.Has(exchange.FundingMarketFieldOpenInterestNotional) ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestLighterMarketStatCarriesComparableFundingPeriodAndPrices(t *testing.T) {
	identity := fundingTestIdentity()
	identity.Venue = exchange.VenueLighter
	identity.QuoteAsset = "USDC"
	identity.SettleAsset = "USDC"
	observedAt := time.Date(2026, time.August, 21, 1, 2, 3, 0, time.UTC)
	fact, fields := lighterMarketStatFact(identity, lighterMarketStat{
		CurrentFundingRate: "0.00008", MarkPrice: "64000", IndexPrice: "63990", OpenInterest: "12.5",
	}, observedAt)
	if !fields.Has(exchange.FundingMarketFieldFundingInterval) ||
		!fact.FundingIntervalHours.Valid || fact.FundingIntervalHours.Value != lighterFundingQuoteHours ||
		!fact.MarkPrice.Valid || !fact.IndexPrice.Valid ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(800000)) {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestOpenInterestEnrichmentIsBoundedAndIsolatesSymbolFailure(t *testing.T) {
	identities := map[string]exchange.InstrumentIdentity{}
	marks := map[string]exchange.OptionalDecimal{}
	for _, symbol := range []string{"AUSDT", "BUSDT", "CUSDT", "DUSDT", "EUSDT"} {
		identity := fundingTestIdentity()
		identity.Symbol = symbol
		identity.NativeSymbol = symbol
		identities[symbol] = identity
		marks[symbol] = exchange.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true}
	}
	var marksMu sync.RWMutex
	var active atomic.Int32
	var maximum atomic.Int32
	var emitted atomic.Int32
	load := func(ctx context.Context, symbol string) (string, time.Time, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
		if symbol == "CUSDT" {
			return "", time.Time{}, errors.New("one symbol failed")
		}
		return "3", time.Now().UTC(), nil
	}
	runOpenInterestEnrichmentRound(context.Background(), exchange.CatalogScope{
		Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
		Product: exchange.ProductPerp, Settlement: "USDT",
	}, identities, marks, &marksMu, func(exchange.FundingMarketPatch) {
		emitted.Add(1)
	}, load, 2, 0)
	if maximum.Load() > 2 || maximum.Load() < 2 {
		t.Fatalf("maximum concurrency=%d", maximum.Load())
	}
	if emitted.Load() != 4 {
		t.Fatalf("successful emissions=%d", emitted.Load())
	}
}

func fundingTestIdentity() exchange.InstrumentIdentity {
	return exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC",
		QuoteAsset: "USDT", SettleAsset: "USDT",
	}
}
