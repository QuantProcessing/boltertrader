package factoryclient

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type nadoDerivativeSnapshotLoader struct {
	settings Settings
}

func NewNadoDerivativeSnapshotLoader(settings Settings) DerivativeSnapshotLoader {
	return nadoDerivativeSnapshotLoader{settings: settings}
}

// LoadDerivativeSnapshot resolves Nado's product ids back into symbols.
//
// Nado keys its bulk funding response by numeric product id and sends no symbol
// at all, so a board row is only nameable after a discovery pass. That pass is
// the same one the catalog loader runs, and it is required rather than
// best-effort: without it every row would be titled with an integer.
//
// Two conversions are load-bearing and neither is guessable from the field
// names. `funding_rate_x18` is a rate scaled by 10^18, and it is quoted over a
// TWENTY-FOUR hour period rather than the eight-hour convention the other
// venues use — so the interval is stated as 24 and the number is left as Nado
// quoted it, letting the board's own per-period normalisation do the comparing.
// `update_time` is only the observation clock for the bulk funding response;
// it is not a settlement timestamp. The next settlement, mark, index and open
// interest are enriched from Nado's all-contracts endpoint when that endpoint
// is available. A failed enrichment never turns valid funding data into an
// unavailable board.
func (loader nadoDerivativeSnapshotLoader) LoadDerivativeSnapshot(ctx context.Context, scope exchange.CatalogScope) (DerivativeSnapshot, error) {
	base := newNadoBase(exchange.ProductPerp, "", "", Settings{
		Endpoint:          loader.settings.Endpoint,
		WebSocketEndpoint: loader.settings.WebSocketEndpoint,
		HTTPClient:        loader.settings.HTTPClient,
	})

	products, err := base.discover(ctx, derivativeSnapshotOperation)
	if err != nil {
		return DerivativeSnapshot{}, err
	}
	byProductID := make(map[string]nadoProduct, len(products))
	for _, product := range products {
		byProductID[strconv.FormatInt(product.productID, 10)] = product
	}

	rates, err := base.sdk.GetAllFundingRates(ctx)
	if err != nil {
		return DerivativeSnapshot{}, base.normalize(derivativeSnapshotOperation, err)
	}
	contracts, _ := base.sdk.GetContracts(ctx, nil)
	contractsByProductID := make(map[int64]nadoContractFields, len(contracts))
	for _, row := range contracts {
		contractsByProductID[int64(row.ProductID)] = nadoContractFields{
			markPrice:                row.MarkPrice,
			indexPrice:               row.IndexPrice,
			openInterest:             row.OpenInterest,
			openInterestNotional:     row.OpenInterestUsd,
			nextFundingRateTimestamp: row.NextFundingRateTimestamp,
		}
	}

	entries := make([]DerivativeSnapshotEntry, 0, len(rates))
	for productID, row := range rates {
		product, known := byProductID[productID]
		if !known {
			continue
		}
		rate, err := nadoX18(row.FundingRateX18)
		if err != nil {
			continue
		}
		entry := DerivativeSnapshotEntry{
			Symbol:        product.canonical,
			NativeSymbol:  product.native,
			NativeID:      productID,
			BaseAsset:     product.base,
			QuoteAsset:    product.quote,
			Rate:          rate,
			IntervalHours: exchange.OptionalInt{Value: 24, Valid: true},
		}
		if contract, ok := contractsByProductID[product.productID]; ok {
			entry.MarkPrice = nadoOptionalPositive(contract.markPrice)
			entry.IndexPrice = nadoOptionalPositive(contract.indexPrice)
			entry.OpenInterest = nadoOptionalNonNegative(contract.openInterest)
			entry.OpenInterestNotional = nadoOptionalNonNegative(contract.openInterestNotional)
			if entry.OpenInterestNotional.Valid {
				entry.OpenInterestNotionalAsset = "USD"
			}
			entry.NextFundingTime = nadoFundingTimestamp(contract.nextFundingRateTimestamp)
		}
		entries = append(entries, entry)
	}
	return derivativeSnapshot(scope, entries), nil
}

type nadoContractFields struct {
	markPrice                *float64
	indexPrice               *float64
	openInterest             *float64
	openInterestNotional     *float64
	nextFundingRateTimestamp *int64
}

func nadoOptionalPositive(value *float64) exchange.OptionalDecimal {
	if value == nil || *value <= 0 {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: decimal.NewFromFloat(*value), Valid: true}
}

func nadoOptionalNonNegative(value *float64) exchange.OptionalDecimal {
	if value == nil || *value < 0 {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: decimal.NewFromFloat(*value), Valid: true}
}

func nadoFundingTimestamp(value *int64) time.Time {
	if value == nil || *value <= 0 {
		return time.Time{}
	}
	if *value >= 1_000_000_000_000 {
		return time.UnixMilli(*value).UTC()
	}
	return time.Unix(*value, 0).UTC()
}
