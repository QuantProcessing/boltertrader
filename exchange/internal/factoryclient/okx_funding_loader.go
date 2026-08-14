package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
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
	sdk := okx.NewClient()
	if loader.settings.Environment == "demo" {
		sdk.WithEnvironment(okx.Simulated)
	} else {
		sdk.WithEnvironment(okx.Production)
	}
	if loader.settings.Endpoint != "" {
		sdk.WithBaseURL(loader.settings.Endpoint)
	}
	if loader.settings.HTTPClient != nil {
		sdk.WithHTTPClient(loader.settings.HTTPClient)
	}

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
