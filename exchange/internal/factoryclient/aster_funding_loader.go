package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type asterFundingBoardLoader struct {
	settlement string
	settings   Settings
}

func NewAsterFundingBoardLoader(settlement string, settings Settings) exchange.FundingBoardLoader {
	return asterFundingBoardLoader{settlement: settlement, settings: settings}
}

// LoadFundingBoard reads Aster's credential-free all-market premium-index
// surface. Aster uses the Binance wire shape but its v3 endpoint family, so it
// has its own loader instead of being filed under Binance by convenience.
func (loader asterFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk, err := newAsterPerpPublicSDK(loader.settings)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, asterErr(exchange.ProductPerp, fundingBoardOperation, exchange.KindInvalidConfig, err.Error())
	}
	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, asterNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}
	config, err := sdk.GetFundingInfo(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, asterNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}
	intervals := make(map[string]int64, len(config))
	for _, row := range config {
		if row.Symbol != "" && row.FundingIntervalHours > 0 {
			intervals[row.Symbol] = row.FundingIntervalHours
		}
	}
	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		base, quote, ok := binanceSplitPerpSymbol(row.Symbol)
		if !ok || quote != loader.settlement {
			continue
		}
		rate, err := decimalOrSkip(row.LastFundingRate)
		if err != nil {
			continue
		}
		entry := exchange.FundingBoardEntry{
			Symbol:       base + "-" + quote,
			NativeSymbol: row.Symbol,
			BaseAsset:    base,
			QuoteAsset:   quote,
			Rate:         rate,
			MarkPrice:    fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:   fundingOptionalDecimal(row.IndexPrice),
		}
		if interval := intervals[row.Symbol]; interval > 0 {
			entry.IntervalHours = exchange.OptionalInt{Value: interval, Valid: true}
		}
		if row.NextFundingTime > 0 {
			entry.NextFundingTime = time.UnixMilli(row.NextFundingTime).UTC()
		}
		entries = append(entries, entry)
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type asterFundingHistoryLoader struct {
	settings Settings
}

func NewAsterFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return asterFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads Aster's public V3 settled-rate endpoint on demand.
// It deliberately shares no state with the resident current-facts feed: opening
// a chart is the only operation that should spend this history request.
func (loader asterFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk, err := newAsterPerpPublicSDK(loader.settings)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, asterErr(exchange.ProductPerp, fundingHistoryOperation, exchange.KindInvalidConfig, err.Error())
	}
	rows, err := sdk.GetFundingRateHistory(ctx, fundingHistoryNative(request), since, until, fundingHistoryLimit(request, asterFundingHistoryMax))
	if err != nil {
		return exchange.FundingHistorySnapshot{}, asterNormalizeErr(exchange.ProductPerp, fundingHistoryOperation, err)
	}
	points := make([]exchange.FundingHistoryPoint, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{
			FundingTime: fundingHistoryMillisTime(row.FundingTime),
			Rate:        rate,
		})
	}
	return fundingHistorySnapshot(request, points), nil
}
