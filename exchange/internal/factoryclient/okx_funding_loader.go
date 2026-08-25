package factoryclient

import (
	"context"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

type okxDerivativeSnapshotLoader struct {
	settlement string
	settings   Settings
	client     okxDerivativeSnapshotClient
}

type okxDerivativeSnapshotClient interface {
	GetAllFundingRates(context.Context) ([]okx.FundingRate, error)
	GetMarkPrices(context.Context, string) ([]okx.MarkPrice, error)
	GetOpenInterests(context.Context, string) ([]okx.OpenInterest, error)
	GetIndexTickers(context.Context, string) ([]okx.IndexTicker, error)
}

func NewOKXDerivativeSnapshotLoader(settlement string, settings Settings) DerivativeSnapshotLoader {
	return okxDerivativeSnapshotLoader{settlement: settlement, settings: settings}
}

func (okxDerivativeSnapshotLoader) coordinatesDerivativeRequests() bool { return true }

// LoadDerivativeSnapshot reads every swap's funding rate in one instId=ANY call.
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
func (loader okxDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	client := loader.client
	if client == nil {
		client = newOKXPublicSDK(loader.settings)
	}

	rows, err := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueOKX, "funding_rates", "swap", client.GetAllFundingRates)
	if err != nil {
		return DerivativeSnapshot{}, okxNormalizeErr(exchange.ProductPerp, derivativeSnapshotOperation, err)
	}
	marks, _ := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueOKX, "mark_prices", "swap", func(callCtx context.Context) ([]okx.MarkPrice, error) {
		return client.GetMarkPrices(callCtx, okxSwapType)
	})
	openInterests, _ := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueOKX, "open_interest", "swap", func(callCtx context.Context) ([]okx.OpenInterest, error) {
		return client.GetOpenInterests(callCtx, okxSwapType)
	})
	indexes, _ := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueOKX, "index_tickers", "swap", func(callCtx context.Context) ([]okx.IndexTicker, error) {
		return client.GetIndexTickers(callCtx, loader.settlement)
	})
	markByInstrument := make(map[string]okx.MarkPrice, len(marks))
	for _, row := range marks {
		markByInstrument[row.InstId] = row
	}
	oiByInstrument := make(map[string]okx.OpenInterest, len(openInterests))
	for _, row := range openInterests {
		oiByInstrument[row.InstId] = row
	}
	indexByInstrument := make(map[string]okx.IndexTicker, len(indexes))
	for _, row := range indexes {
		indexByInstrument[row.InstId] = row
	}

	entries := make([]DerivativeSnapshotEntry, 0, len(rows))
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
		entry := DerivativeSnapshotEntry{
			Symbol:       row.InstrumentID,
			NativeSymbol: row.InstrumentID,
			BaseAsset:    base,
			QuoteAsset:   loader.settlement,
			Rate:         rate,
		}
		entry.FundingObservedAt = fundingStringMillisOrNow(row.Ts)
		if mark, ok := markByInstrument[row.InstrumentID]; ok {
			entry.MarkPrice = fundingOptionalDecimal(mark.MarkPx)
			entry.MarkObservedAt = fundingStringMillisOrNow(mark.Ts)
			entry.PriceObservedAt = entry.MarkObservedAt
		}
		indexID := strings.TrimSuffix(row.InstrumentID, "-SWAP")
		if index, ok := indexByInstrument[indexID]; ok {
			entry.IndexPrice = fundingOptionalDecimal(index.IdxPx)
			at := fundingStringMillisOrNow(index.Ts)
			entry.IndexObservedAt = at
			if at.After(entry.PriceObservedAt) {
				entry.PriceObservedAt = at
			}
		}
		if oi, ok := oiByInstrument[row.InstrumentID]; ok {
			entry.OpenInterest = fundingOptionalNonNegative(firstNonEmpty(oi.OICcy, oi.OI))
			entry.OpenInterestObservedAt = fundingStringMillisOrNow(oi.Ts)
			if notional := fundingOptionalNonNegative(oi.OIUsd); notional.Valid {
				entry.OpenInterestNotional = notional
				entry.OpenInterestNotionalAsset = "USD"
			}
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
	return derivativeSnapshot(scope, entries), nil
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
