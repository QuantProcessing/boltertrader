package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type binanceDerivativeSnapshotLoader struct {
	settlement string
	settings   Settings
}

func NewBinanceDerivativeSnapshotLoader(settlement string, settings Settings) DerivativeSnapshotLoader {
	return binanceDerivativeSnapshotLoader{settlement: settlement, settings: settings}
}

// LoadDerivativeSnapshot reads Binance's premiumIndex surface plus its funding clock.
//
// Two calls rather than one because Binance splits the answer: premiumIndex
// carries the rate and the next settlement, fundingInfo carries the interval,
// and an interval is only published for the symbols that deviate from eight
// hours. A symbol missing from fundingInfo is therefore not an error — it is
// Binance saying "the default", which we do not invent here; the board renders
// the gap and the caller decides.
//
// Open interest is absent by construction: Binance only serves it one symbol at
// a time, and paying several hundred requests for one column is not a trade a
// board should make.
func (loader binanceDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	sdk := newBinancePerpPublicSDK(loader.settings)

	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return DerivativeSnapshot{}, binancePerpNormalizeError(derivativeSnapshotOperation, err)
	}
	intervals := map[string]int64{}
	if info, err := sdk.GetFundingInfo(ctx); err == nil {
		for _, row := range info {
			if row.FundingIntervalHours > 0 {
				intervals[row.Symbol] = row.FundingIntervalHours
			}
		}
	}

	entries := make([]DerivativeSnapshotEntry, 0, len(rows))
	for _, row := range rows {
		// premiumIndex answers for delivery futures too, and those carry a
		// quarterly expiry in the symbol. Splitting on the quote asset drops
		// them: BTCUSDT_240628 has no quote suffix to find.
		base, quote, ok := binanceSplitPerpSymbol(row.Symbol)
		if !ok || quote != loader.settlement {
			continue
		}
		rate, err := positiveOrNegativeDecimal(row.LastFundingRate)
		if err != nil {
			continue
		}
		entry := DerivativeSnapshotEntry{
			Symbol:       base + "-" + quote,
			NativeSymbol: row.Symbol,
			BaseAsset:    base,
			QuoteAsset:   quote,
			Rate:         rate,
			MarkPrice:    fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:   fundingOptionalDecimal(row.IndexPrice),
		}
		if hours, ok := intervals[row.Symbol]; ok {
			entry.IntervalHours = exchange.OptionalInt{Value: hours, Valid: true}
		}
		if row.NextFundingTime > 0 {
			entry.NextFundingTime = time.UnixMilli(row.NextFundingTime).UTC()
		}
		entries = append(entries, entry)
	}
	return derivativeSnapshot(scope, entries), nil
}

// binanceSplitPerpSymbol recovers base and quote from a concatenated symbol.
//
// It mirrors the assertion the catalog loader makes against exchangeInfo
// (margin asset equals quote asset, quote is USDT or USDC) without the metadata
// to check it against — premiumIndex returns a bare symbol string. Trying the
// longer suffix first matters: USDC and USDT are the same length, but a future
// quote that is a suffix of another would silently mis-split.
func binanceSplitPerpSymbol(symbol string) (string, string, bool) {
	for _, quote := range []string{"USDT", "USDC"} {
		if len(symbol) > len(quote) && symbol[len(symbol)-len(quote):] == quote {
			return symbol[:len(symbol)-len(quote)], quote, true
		}
	}
	return "", "", false
}

type binanceFundingHistoryLoader struct {
	settings Settings
}

func NewBinanceFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return binanceFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads /fapi/v1/fundingRate for one symbol.
//
// No settlement filter, unlike the board: the caller names one contract by its
// native symbol, and the symbol itself already decides what it settles in.
//
// Binance is the most generous of the ten here — a thousand rows in one call
// covers thirty days even at hourly settlement — so this is the venue where the
// single-call premise is never in doubt.
func (loader binanceFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk := newBinancePerpPublicSDK(loader.settings)

	rows, err := sdk.GetFundingRateHistory(ctx, fundingHistoryNative(request), since, until, fundingHistoryLimit(request, binanceFundingHistoryMax))
	if err != nil {
		return exchange.FundingHistorySnapshot{}, binancePerpNormalizeError(fundingHistoryOperation, err)
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
