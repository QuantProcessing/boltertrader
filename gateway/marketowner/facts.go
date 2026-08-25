package marketowner

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
)

// Facts projects the resident venue feed state onto canonical catalog
// identities. It performs no venue I/O: both inputs are memory snapshots owned
// by their respective resident producers.
type Facts struct {
	Board   *fundingboard.Source
	Markets *publicmarket.Source
}

func (source Facts) Snapshot(ctx context.Context) ([]exchange.DerivativeFact, error) {
	if source.Board == nil || source.Markets == nil {
		return nil, fmt.Errorf("derivative facts unavailable")
	}
	board, err := source.Board.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	markets, err := source.Markets.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	return projectDerivativeFacts(board, markets), nil
}

func projectDerivativeFacts(board fundingboard.Snapshot, markets publicmarket.Snapshot) []exchange.DerivativeFact {
	facts := make([]exchange.DerivativeFact, 0)
	for _, venue := range board.Venues {
		for _, entry := range venue.Entries {
			instrument, ok := markets.MatchFundingEntry(venue.RouteProfileID, entry)
			if !ok {
				// A market fact without canonical identity cannot be safely
				// subscribed to or used for an order preview.
				continue
			}
			fact := exchange.DerivativeFact{
				InstrumentID:              string(instrument.Catalog.ID),
				RouteProfileID:            venue.RouteProfileID,
				Instrument:                instrument.Canonical.Identity,
				Environment:               exchange.EnvironmentLive,
				FundingIntervalHours:      entry.IntervalHours,
				NextFundingTime:           entry.NextFundingTime,
				OpenInterestNotionalAsset: entry.OpenInterestNotionalAsset,
			}
			if entry.RateValid && !entry.FundingObservedAt.IsZero() {
				fact.FundingRate, fact.FundingObservedAt = exchange.OptionalDecimal{Value: entry.Rate, Valid: true}, entry.FundingObservedAt.UTC()
			}
			if entry.MarkPrice.Valid && !entry.MarkObservedAt.IsZero() {
				fact.MarkPrice, fact.MarkObservedAt = entry.MarkPrice, entry.MarkObservedAt.UTC()
			}
			if entry.IndexPrice.Valid && !entry.IndexObservedAt.IsZero() {
				fact.IndexPrice, fact.IndexObservedAt = entry.IndexPrice, entry.IndexObservedAt.UTC()
			}
			if !entry.OpenInterestObservedAt.IsZero() && (entry.OpenInterest.Valid || entry.OpenInterestNotional.Valid) {
				fact.OpenInterest, fact.OpenInterestNotional = entry.OpenInterest, entry.OpenInterestNotional
				fact.OpenInterestObservedAt = entry.OpenInterestObservedAt.UTC()
			}
			if err := fact.Validate(); err != nil {
				continue
			}
			facts = append(facts, fact)
		}
	}
	return facts
}
