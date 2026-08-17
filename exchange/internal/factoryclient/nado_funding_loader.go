package factoryclient

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type nadoFundingBoardLoader struct {
	settings Settings
}

func NewNadoFundingBoardLoader(settings Settings) exchange.FundingBoardLoader {
	return nadoFundingBoardLoader{settings: settings}
}

// LoadFundingBoard resolves Nado's product ids back into symbols.
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
// `update_time` is epoch seconds, not the milliseconds every other venue on
// this board sends.
func (loader nadoFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	base := newNadoBase(exchange.ProductPerp, "", "", Settings{
		Endpoint:          loader.settings.Endpoint,
		WebSocketEndpoint: loader.settings.WebSocketEndpoint,
		HTTPClient:        loader.settings.HTTPClient,
	})

	products, err := base.discover(ctx, fundingBoardOperation)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, err
	}
	byProductID := make(map[string]nadoProduct, len(products))
	for _, product := range products {
		byProductID[strconv.FormatInt(product.productID, 10)] = product
	}

	rates, err := base.sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, base.normalize(fundingBoardOperation, err)
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(rates))
	for productID, row := range rates {
		product, known := byProductID[productID]
		if !known {
			continue
		}
		rate, err := nadoX18(row.FundingRateX18)
		if err != nil {
			continue
		}
		entry := exchange.FundingBoardEntry{
			Symbol:        product.canonical,
			NativeSymbol:  product.native,
			NativeID:      productID,
			BaseAsset:     product.base,
			QuoteAsset:    product.quote,
			Rate:          rate,
			IntervalHours: exchange.OptionalInt{Value: 24, Valid: true},
		}
		if seconds, err := strconv.ParseInt(row.UpdateTime, 10, 64); err == nil && seconds > 0 {
			entry.NextFundingTime = time.Unix(seconds, 0).UTC()
		}
		entries = append(entries, entry)
	}
	return fundingBoardSnapshot(scope, entries), nil
}
