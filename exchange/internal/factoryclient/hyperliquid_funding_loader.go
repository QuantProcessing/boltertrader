package factoryclient

import (
	"context"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
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
	base := newHyperliquidPublicSDK(loader.settings)
	directory, _, err := hyperliquidPerpDirectory(ctx, base, false, true)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingBoardOperation, err, nil)
	}
	entries := make([]exchange.FundingBoardEntry, 0)
	for _, dex := range directory {
		if scope.Settlement != "" && dex.collateral != scope.Settlement {
			continue
		}
		if dex.full == nil || len(dex.full.Meta.Universe) != len(dex.full.AssetCtxs) {
			return exchange.FundingBoardSnapshot{}, hlMalformed(exchange.ProductPerp, fundingBoardOperation, "hyperliquid returned mismatched universe and asset context lengths")
		}
		for index, universe := range dex.full.Meta.Universe {
			if !hyperliquidValidPerpCoin(universe.Name, dex.name) {
				continue
			}
			asset := dex.full.AssetCtxs[index]
			rate, err := decimalOrSkip(asset.Funding)
			if err != nil {
				continue
			}
			symbol := hyperliquidUnderlying(universe.Name) + "-" + dex.collateral
			if dex.index > 0 {
				symbol = universe.Name + "-" + dex.collateral
			}
			entries = append(entries, exchange.FundingBoardEntry{
				Symbol:       symbol,
				NativeSymbol: universe.Name,
				// The HIP-3 dex index is part of the asset id. Keeping the same
				// identity as the catalog is what makes the row executable.
				NativeID:      strconv.Itoa(hyperliquidAssetID(dex.index, index)),
				BaseAsset:     hyperliquidUnderlying(universe.Name),
				QuoteAsset:    dex.collateral,
				Rate:          rate,
				IntervalHours: exchange.OptionalInt{Value: 1, Valid: true},
				MarkPrice:     fundingOptionalDecimal(asset.MarkPx),
				IndexPrice:    fundingOptionalDecimal(asset.OraclePx),
				OpenInterest:  fundingOptionalNonNegative(asset.OpenInterest),
				OpenInterestNotional: fundingOpenInterestNotional(
					asset.OpenInterest, asset.MarkPx, decimal.NewFromInt(1),
				),
			})
		}
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type hyperliquidFundingHistoryLoader struct {
	settings Settings
}

func NewHyperliquidFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return hyperliquidFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one coin's settled rates, walking forward.
//
// Hyperliquid takes a window but no row limit, and answers with at most five
// hundred settlements counted from the start of the window. At its hourly
// clock that is under twenty-one days, so a single thirty-day call returns the
// oldest stretch and stops nine days before now — the opposite end from the one
// a funding chart is read for. Each pass therefore resumes from the newest
// settlement it received, one millisecond on, until the window is covered.
//
// Overlapping pages are safe: the shared snapshot builder drops repeated
// stamps, so a venue that includes its cursor row again costs a duplicate
// rather than a doubled point. The loop also stops if a pass fails to advance
// the cursor, which is the only way a forward walk could fail to terminate.
//
// The coin, not a pair, is the identifier: Hyperliquid addresses perpetuals by
// bare base asset, which is why the board files NativeSymbol separately from
// the canonical BTC-USDC spelling.
func (loader hyperliquidFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	client := hyperliquidperp.NewClient(newHyperliquidPublicSDK(loader.settings))
	native := fundingHistoryNative(request)
	dex := ""
	if prefix, _, ok := strings.Cut(native, ":"); ok {
		dex = prefix
	}

	points := []exchange.FundingHistoryPoint{}
	for page, cursor := 0, since; page < hyperliquidFundingHistoryMaxPages; page++ {
		rows, err := client.GetFundingRateHistoryForDex(ctx, dex, native, cursor, until)
		if err != nil {
			return exchange.FundingHistorySnapshot{}, hlNormalizeQueryErr(exchange.ProductPerp, fundingHistoryOperation, err, nil)
		}
		if len(rows) == 0 {
			break
		}
		newest := cursor
		for _, row := range rows {
			if row.Time > newest {
				newest = row.Time
			}
			rate, err := decimalOrSkip(row.FundingRate)
			if err != nil {
				continue
			}
			points = append(points, exchange.FundingHistoryPoint{
				FundingTime: fundingHistoryMillisTime(row.Time),
				Rate:        rate,
			})
		}
		if len(rows) < hyperliquidFundingHistoryPage || newest <= cursor {
			break
		}
		cursor = newest + 1
	}
	return fundingHistorySnapshot(request, points), nil
}
