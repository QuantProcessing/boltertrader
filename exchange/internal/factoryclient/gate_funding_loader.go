package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type gateDerivativeSnapshotLoader struct {
	settings Settings
}

func NewGateDerivativeSnapshotLoader(settings Settings) DerivativeSnapshotLoader {
	return gateDerivativeSnapshotLoader{settings: settings}
}

func (gateDerivativeSnapshotLoader) coordinatesDerivativeRequests() bool { return true }

// LoadDerivativeSnapshot joins Gate's ticker surface to its contract surface.
//
// Both calls are required, not one preferred over the other: the ticker carries
// the live rate, mark, index and open size, while the funding clock and the
// interval live only on the contract. Gate is the only venue where the two
// halves of a funding row are served by different endpoints, and dropping
// either would cost a column outright.
//
// The contract call is best-effort in the same sense as the other venues' —
// tickers alone still render a usable board, so a contract failure narrows the
// table rather than emptying it.
func (loader gateDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	sdk, _ := newGateSDK("", "", loader.settings)
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}

	tickers, err := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueGate, "all_tickers", "perpetual", func(callCtx context.Context) ([]gate.FuturesTicker, error) {
		return sdk.ListFuturesTickers(callCtx, gate.SettleUSDT, "")
	})
	if err != nil {
		return DerivativeSnapshot{}, gateNormalizeErr(meta, derivativeSnapshotOperation, err)
	}
	type contractInfo struct {
		interval   exchange.OptionalInt
		next       time.Time
		multiplier decimal.Decimal
	}
	contractsByName := map[string]contractInfo{}
	contracts, contractsErr := coordinatedMarketRequest(ctx, loader.settings, exchange.VenueGate, "contracts", "perpetual", func(callCtx context.Context) ([]gate.Contract, error) {
		return sdk.ListFuturesContracts(callCtx, gate.SettleUSDT)
	})
	if contractsErr == nil {
		for _, contract := range contracts {
			entry := contractInfo{}
			entry.multiplier, _ = decimal.NewFromString(contract.QuantoMultiplier)
			if contract.Status != "" && contract.Status != "trading" {
				contractsByName[contract.Name] = entry
				continue
			}
			// funding_interval is published in seconds; the board speaks hours,
			// and an interval that does not divide into whole hours is Gate
			// telling us something we cannot render, so it is dropped.
			if contract.FundingInterval > 0 && contract.FundingInterval%3600 == 0 {
				entry.interval = exchange.OptionalInt{Value: contract.FundingInterval / 3600, Valid: true}
			}
			if contract.FundingNextApply > 0 {
				entry.next = time.Unix(int64(contract.FundingNextApply), 0).UTC()
			}
			contractsByName[contract.Name] = entry
		}
	}

	entries := make([]DerivativeSnapshotEntry, 0, len(tickers))
	for _, row := range tickers {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		base, quote := gateSplitSymbol(row.Contract)
		contract := contractsByName[row.Contract]
		entries = append(entries, DerivativeSnapshotEntry{
			Symbol:          gateCanonicalSymbol(base, quote, row.Contract),
			NativeSymbol:    row.Contract,
			BaseAsset:       base,
			QuoteAsset:      quote,
			Rate:            rate,
			IntervalHours:   contract.interval,
			NextFundingTime: contract.next,
			MarkPrice:       fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:      fundingOptionalDecimal(row.IndexPrice),
			// total_size is Gate's open size in contracts. Keep that native value
			// for provenance, then multiply it by quanto_multiplier and mark price
			// for the cross-venue U-notional field.
			OpenInterest: fundingOptionalNonNegative(row.TotalSize),
			OpenInterestNotional: fundingOpenInterestNotional(
				row.TotalSize, row.MarkPrice, contract.multiplier,
			),
		})
	}
	return derivativeSnapshot(scope, entries), nil
}

type gateFundingHistoryLoader struct {
	settings Settings
}

func NewGateFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return gateFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one USDT-settled contract's funding_rate series.
//
// Gate is the one venue whose SDK takes the window as time.Time rather than as
// epoch milliseconds, and it is the reason the shared window helper hands back
// both forms: the request keeps the times, the helper validates them, and only
// the six millisecond venues consume its return.
//
// The settle currency is the same literal the board uses. Gate scopes futures
// by settlement in the path, and USDT is the only settlement configured here.
func (loader gateFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	if _, _, err := fundingHistoryWindow(request); err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk, _ := newGateSDK("", "", loader.settings)
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}

	records, err := sdk.ListFuturesFundingRateHistory(ctx, gate.SettleUSDT, fundingHistoryNative(request), request.Since, request.Until, fundingHistoryLimit(request, gateFundingHistoryMax))
	if err != nil {
		return exchange.FundingHistorySnapshot{}, gateNormalizeErr(meta, fundingHistoryOperation, err)
	}

	points := make([]exchange.FundingHistoryPoint, 0, len(records))
	for _, record := range records {
		rate, err := decimalOrSkip(record.Rate)
		if err != nil {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{
			// Gate stamps in seconds where the other nine use milliseconds.
			FundingTime: fundingHistorySecondsTime(record.Time),
			Rate:        rate,
		})
	}
	return fundingHistorySnapshot(request, points), nil
}
