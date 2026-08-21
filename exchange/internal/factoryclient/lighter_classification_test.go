package factoryclient

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterListedTokenClassificationUsesExactMarketAndSymbol(t *testing.T) {
	instrument := newInstrument(
		exchange.VenueLighter, exchange.ProductPerp, "TSLA-USDC", "TSLA", "USDC", "USDC", "TSLA", "7",
		decimal.NewFromInt(1), decimal.NewFromInt(1), decimal.Zero, exchange.OptionalDecimal{}, oneContract(),
	)
	tokens := []lighter.ListedToken{
		{Symbol: "TSLA", Market: "SPOT", AssetType: "CRYPTO", Categories: []string{"L1"}},
		{Symbol: "TSLA", Market: "PERPS", AssetType: "RWA", Categories: []string{"STOCK"}},
	}
	lighterApplyListedTokenMetadata(&instrument, tokens)
	if instrument.Metadata.AssetClass != exchange.AssetClassEquity ||
		instrument.Metadata.ClassificationSource != "lighter.tokenlist" ||
		instrument.Metadata.RawAssetType != "RWA" {
		t.Fatalf("metadata = %+v", instrument.Metadata)
	}
}

func TestLighterUnknownOfficialCategoryRemainsRWAOther(t *testing.T) {
	if got := lighterAssetClass("RWA", []string{"NEW"}); got != exchange.AssetClassRWAOther {
		t.Fatalf("class = %q", got)
	}
	if got := lighterAssetClass("", []string{"STOCK"}); got != exchange.AssetClassUnknown {
		t.Fatalf("missing asset type was guessed as %q", got)
	}
}
