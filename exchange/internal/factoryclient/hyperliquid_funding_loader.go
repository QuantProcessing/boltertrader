package factoryclient

import (
	"context"
	"strconv"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
)

type hyperliquidFundingBoardLoader struct {
	settings Settings
}

func NewHyperliquidFundingBoardLoader(settings Settings) exchange.FundingBoardLoader {
	return hyperliquidFundingBoardLoader{settings: settings}
}

// LoadFundingBoard reads the perp universe and its asset contexts together.
//
// This deliberately uses metaAndAssetCtxs rather than the bare predictedFundings
// call the plan named: the same response carries mark, oracle and open interest
// alongside the rate, so one request fills four columns instead of one. The
// catalog loader pairs it with a second `GetPrepMeta` call to recover tick sizes,
// which a board has no use for, so that call is skipped here.
//
// Hyperliquid's `funding` is an HOURLY rate, unlike the eight-hourly convention
// almost everywhere else. The interval is therefore stated rather than parsed —
// there is no field to read it from, and leaving it blank would let the board
// compare an hourly rate against an eight-hourly one as if they were the same
// number.
func (loader hyperliquidFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	base := hyperliquidbase.NewClient()
	if loader.settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquidbase.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquidbase.EnvironmentMainnet)
	}
	if loader.settings.Endpoint != "" {
		base.BaseURL = loader.settings.Endpoint
	}
	if loader.settings.HTTPClient != nil {
		base.Http = loader.settings.HTTPClient
	}

	full, err := hyperliquidperp.NewClient(base).GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	if full == nil || len(full.Meta.Universe) != len(full.AssetCtxs) {
		return exchange.FundingBoardSnapshot{}, hlMalformed(exchange.ProductPerp, fundingBoardOperation, "hyperliquid returned mismatched universe and asset context lengths")
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(full.Meta.Universe))
	for index, universe := range full.Meta.Universe {
		if hlRejectCoin(universe.Name) {
			continue
		}
		asset := full.AssetCtxs[index]
		rate, err := decimalOrSkip(asset.Funding)
		if err != nil {
			continue
		}
		entries = append(entries, exchange.FundingBoardEntry{
			Symbol:       universe.Name + "-USDC",
			NativeSymbol: universe.Name,
			// The universe index is Hyperliquid's asset id, and the catalog
			// loader files instruments under it. Keeping the same identity here
			// is what lets a board row join to a tradable instrument.
			NativeID:      strconv.Itoa(index),
			BaseAsset:     universe.Name,
			QuoteAsset:    "USDC",
			Rate:          rate,
			IntervalHours: exchange.OptionalInt{Value: 1, Valid: true},
			MarkPrice:     fundingOptionalDecimal(asset.MarkPx),
			IndexPrice:    fundingOptionalDecimal(asset.OraclePx),
			OpenInterest:  fundingOptionalNonNegative(asset.OpenInterest),
		})
	}
	return fundingBoardSnapshot(scope, entries), nil
}
