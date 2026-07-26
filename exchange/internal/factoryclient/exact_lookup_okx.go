package factoryclient

import (
	"context"
	"errors"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func (client *okxSpotClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueOKX, exchange.ProductSpot)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if err := okxReady(ctx, exchange.ProductSpot, exactLookupOperation, client.sdk); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if err := okxValidateSpotInstrument(instrument); err != nil {
		return exchange.ExactOrderLookupResult{}, okxInvalid(exchange.ProductSpot, exactLookupOperation, err.Error())
	}
	return okxLookupExactOrder(ctx, client.sdk, exchange.ProductSpot, instrument, request, decimal.NewFromInt(1))
}

func (client *okxPerpClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueOKX, exchange.ProductPerp)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	meta, err := client.okxPerpMeta(ctx, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	return okxLookupExactOrder(ctx, client.sdk, exchange.ProductPerp, instrument, request, meta.contractValue)
}

func okxLookupExactOrder(
	ctx context.Context,
	sdk *okx.Client,
	product exchange.Product,
	instrument exchange.Instrument,
	request exchange.ExactOrderLookupRequest,
	multiplier decimal.Decimal,
) (exchange.ExactOrderLookupResult, error) {
	rows, err := sdk.GetOrder(ctx, instrument.Identity.NativeSymbol, request.VenueOrderID, request.ClientOrderID)
	if err != nil {
		if okxIsExactOrderNotFound(err) {
			return exactLookupNotFound(request, exchange.VenueOKX, product, instrument)
		}
		return exchange.ExactOrderLookupResult{}, okxNormalizeErr(product, exactLookupOperation, err)
	}
	if len(rows) != 1 {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, "exact order lookup response must contain exactly one row")
	}
	row := rows[0]
	if row.InstId != instrument.Identity.NativeSymbol {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	if request.ClientOrderID != "" && row.ClOrdId != request.ClientOrderID {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, "exact order lookup response client order id mismatch")
	}
	if request.VenueOrderID != "" && row.OrdId != request.VenueOrderID {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, "exact order lookup response order id mismatch")
	}
	order, err := okxOrder(row, multiplier)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, err.Error())
	}
	status, err := exactPortableOrderStatus(order.Status)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, okxMalformed(product, exactLookupOperation, "exact order lookup response has unsupported order status")
	}
	order.Status = status
	if order.NativeID == "" {
		order.NativeID = instrument.Identity.NativeID
	}
	return exactLookupFound(request, exchange.VenueOKX, product, instrument, order)
}

func okxIsExactOrderNotFound(err error) bool {
	var apiErr *okx.APIError
	return errors.As(err, &apiErr) && apiErr.Code == "51603"
}
