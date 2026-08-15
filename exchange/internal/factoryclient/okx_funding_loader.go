package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type okxFundingBoardLoader struct {
	settlement string
	settings   Settings
}

func NewOKXFundingBoardLoader(settlement string, settings Settings) exchange.FundingBoardLoader {
	return okxFundingBoardLoader{settlement: settlement, settings: settings}
}

// LoadFundingBoard reads every swap's funding rate in one instId=ANY call.
//
// OKX is the one venue whose canonical symbol is already its native one — the
// catalog loader files swaps under their raw instId (BTC-USDT-SWAP), so no
// translation happens here and none should be added: inventing BTC-USDT would
// file these rates under a symbol no other OKX row uses.
//
// The interval is not published per-instrument, but the two settlement stamps
// are, and their difference is the interval. Deriving it costs nothing and is
// exact, where a hardcoded eight hours would be wrong for the instruments OKX
// settles hourly.
func (loader okxFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk := newOKXPublicSDK(loader.settings)

	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, okxNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		if row.InstrumentType != "" && !stringsEqualFold(row.InstrumentType, okxSwapType) {
			continue
		}
		base := okxSwapBase(row.InstrumentID)
		if base == "" {
			continue
		}
		// Deliberately not okxDecimal, which reads a blank as zero. That is right
		// for an order field OKX leaves empty when it does not apply, and wrong
		// here: a blank rate means unknown, and a board that renders it as
		// 0.0000% offers the operator a spread against a number OKX never quoted.
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		entry := exchange.FundingBoardEntry{
			Symbol:       row.InstrumentID,
			NativeSymbol: row.InstrumentID,
			BaseAsset:    base,
			QuoteAsset:   loader.settlement,
			Rate:         rate,
		}
		settles, err := okxOptionalMillis(row.FundingTime)
		if err == nil {
			entry.NextFundingTime = settles
		}
		if next, err := okxOptionalMillis(row.NextFundingTime); err == nil && !next.IsZero() && !settles.IsZero() {
			if hours := int64(next.Sub(settles).Hours()); hours > 0 {
				entry.IntervalHours = exchange.OptionalInt{Value: hours, Valid: true}
			}
		}
		entries = append(entries, entry)
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type okxFundingHistoryLoader struct {
	settings Settings
}

func NewOKXFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return okxFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one swap's settled rates.
//
// OKX's two cursors are named for the row, not for the clock: `before` selects
// rows newer than the stamp and `after` selects rows older than it, so the
// window maps across inverted. Passing them the other way round returns an
// empty list rather than an error, which is exactly the failure that looks like
// "this instrument has no history".
//
// The realised rate is preferred over the quoted one. On the history endpoint
// OKX publishes both — what it predicted and what it actually charged — and a
// chart of what was paid is the one an operator can reconcile against a funding
// statement. The prediction is the fallback for the rows where OKX leaves the
// realised field blank.
func (loader okxFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk := newOKXPublicSDK(loader.settings)

	rows, err := sdk.GetFundingRateHistory(ctx, fundingHistoryNative(request), since, until, fundingHistoryLimit(request, okxFundingHistoryMax))
	if err != nil {
		return exchange.FundingHistorySnapshot{}, okxNormalizeErr(exchange.ProductPerp, fundingHistoryOperation, err)
	}

	points := make([]exchange.FundingHistoryPoint, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(row.RealizedRate)
		if err != nil {
			if rate, err = decimalOrSkip(row.FundingRate); err != nil {
				continue
			}
		}
		settled, err := okxOptionalMillis(row.FundingTime)
		if err != nil || settled.IsZero() {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{FundingTime: settled, Rate: rate})
	}
	return fundingHistorySnapshot(request, points), nil
}
