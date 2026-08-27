package factoryclient

import (
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
	if fields != nativeDerivativeFieldFunding || !fact.FundingRate.Valid || fact.MarkPrice.Valid || fact.OpenInterest.Valid {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestBitgetTickerFactUsesUTAOpenInterest(t *testing.T) {
	fact, fields := bitgetTickerFact(fundingTestIdentity(), bitget.Ticker{
		OpenInterest: "12.5", MarkPrice: "200", FundingRate: "0.0002",
	}, time.Now().UTC())
	if !fields.Has(nativeDerivativeFieldOpenInterest) ||
		!fields.Has(nativeDerivativeFieldOpenInterestNotional) ||
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
	if !fields.Has(nativeDerivativeFieldOpenInterestNotional) ||
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
	if !fields.Has(nativeDerivativeFieldFundingInterval) ||
		!fact.FundingIntervalHours.Valid || fact.FundingIntervalHours.Value != lighterFundingQuoteHours ||
		!fact.MarkPrice.Valid || !fact.IndexPrice.Valid ||
		!fact.OpenInterestNotional.Valid || !fact.OpenInterestNotional.Value.Equal(decimal.NewFromInt(800000)) {
		t.Fatalf("fact=%+v fields=%b", fact, fields)
	}
}

func TestDerivativeSnapshotDoesNotClaimAbsentOpenInterest(t *testing.T) {
	fields := nativeDerivativeSnapshotFields([]nativeDerivativeFact{{
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		MarkPrice:   exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
		IndexPrice:  exchange.OptionalDecimal{Value: decimal.NewFromInt(99), Valid: true},
	}})
	if !fields.Has(nativeDerivativeFieldIdentity) || !fields.Has(nativeDerivativeFieldFunding) || !fields.Has(nativeDerivativeFieldMarkPrice) || !fields.Has(nativeDerivativeFieldIndexPrice) {
		t.Fatalf("snapshot fields missing available values: %b", fields)
	}
	if fields.Has(nativeDerivativeFieldOpenInterest) || fields.Has(nativeDerivativeFieldOpenInterestNotional) {
		t.Fatalf("snapshot claims absent open interest: %b", fields)
	}
}

func TestGateSubscriptionFactsAreStableAndTrackMembership(t *testing.T) {
	facts := []nativeDerivativeFact{
		{Instrument: exchange.InstrumentIdentity{NativeSymbol: "ETH_USDT"}},
		{Instrument: exchange.InstrumentIdentity{NativeSymbol: "BTC_USDT"}},
	}
	identities, _, payload := gateSubscriptionFacts(facts)
	if len(identities) != 2 || len(payload) != 2 || payload[0] != "BTC_USDT" || payload[1] != "ETH_USDT" {
		t.Fatalf("gate subscription facts = identities:%+v payload:%+v", identities, payload)
	}
	if !sameStrings(payload, []string{"BTC_USDT", "ETH_USDT"}) || sameStrings(payload, []string{"BTC_USDT"}) {
		t.Fatal("gate catalog membership comparison is unstable")
	}
}

func fundingTestIdentity() exchange.InstrumentIdentity {
	return exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC",
		QuoteAsset: "USDT", SettleAsset: "USDT",
	}
}
