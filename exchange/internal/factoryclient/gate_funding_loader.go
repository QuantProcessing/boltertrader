package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
)

type gateFundingBoardLoader struct {
	settings Settings
}

func NewGateFundingBoardLoader(settings Settings) exchange.FundingBoardLoader {
	return gateFundingBoardLoader{settings: settings}
}

// LoadFundingBoard joins Gate's ticker surface to its contract surface.
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
func (loader gateFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk, _ := newGateSDK("", "", loader.settings)
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}

	tickers, err := sdk.ListFuturesTickers(ctx, gate.SettleUSDT, "")
	if err != nil {
		return exchange.FundingBoardSnapshot{}, gateNormalizeErr(meta, fundingBoardOperation, err)
	}
	type clock struct {
		interval exchange.OptionalInt
		next     time.Time
	}
	clocks := map[string]clock{}
	if contracts, err := sdk.ListFuturesContracts(ctx, gate.SettleUSDT); err == nil {
		for _, contract := range contracts {
			if contract.Status != "" && contract.Status != "trading" {
				continue
			}
			entry := clock{}
			// funding_interval is published in seconds; the board speaks hours,
			// and an interval that does not divide into whole hours is Gate
			// telling us something we cannot render, so it is dropped.
			if contract.FundingInterval > 0 && contract.FundingInterval%3600 == 0 {
				entry.interval = exchange.OptionalInt{Value: contract.FundingInterval / 3600, Valid: true}
			}
			if contract.FundingNextApply > 0 {
				entry.next = time.Unix(int64(contract.FundingNextApply), 0).UTC()
			}
			clocks[contract.Name] = entry
		}
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(tickers))
	for _, row := range tickers {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		base, quote := gateSplitSymbol(row.Contract)
		next := clocks[row.Contract]
		entries = append(entries, exchange.FundingBoardEntry{
			Symbol:          gateCanonicalSymbol(base, quote, row.Contract),
			NativeSymbol:    row.Contract,
			BaseAsset:       base,
			QuoteAsset:      quote,
			Rate:            rate,
			IntervalHours:   next.interval,
			NextFundingTime: next.next,
			MarkPrice:       fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:      fundingOptionalDecimal(row.IndexPrice),
			// total_size is Gate's open size in contracts, which is what its own
			// interface shows as open interest. It is not a notional, and the
			// board does not pretend otherwise — the column is labelled in
			// contracts for every venue that answers in them.
			OpenInterest: fundingOptionalNonNegative(row.TotalSize),
		})
	}
	return fundingBoardSnapshot(scope, entries), nil
}
