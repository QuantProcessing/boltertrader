package factoryclient

import (
	"context"
	"strconv"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
)

func (client *lighterSpotClient) LookupExactOrder(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
) (exchange.ExactOrderLookupResult, error) {
	return lighterLookupExactOrder(ctx, request, client.sdk, client.state, exchange.ProductSpot, lighterSpot)
}

func (client *lighterPerpClient) LookupExactOrder(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
) (exchange.ExactOrderLookupResult, error) {
	return lighterLookupExactOrder(ctx, request, client.sdk, client.state, exchange.ProductPerp, lighterPerp)
}

func lighterLookupExactOrder(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
	sdk *lighter.Client,
	state *lighterRESTState,
	product exchange.Product,
	marketType string,
) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueLighter, product)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	meta, err := lighterMeta(ctx, sdk, state, exactLookupOperation, product, marketType, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}

	active, err := sdk.GetAccountActiveOrders(ctx, meta.marketID)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, lighterNormalizeErr(product, exactLookupOperation, err)
	}
	if active == nil || active.Code != 200 {
		return exchange.ExactOrderLookupResult{}, lighterMalformed(product, exactLookupOperation, "active orders response is malformed")
	}
	order, found, err := lighterExactOrderFromRows(active.Orders, request, meta)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, lighterMalformed(product, exactLookupOperation, err.Error())
	}
	if found {
		return exactLookupFound(request, exchange.VenueLighter, product, instrument, order)
	}

	inactive, err := sdk.GetInactiveOrders(ctx, &meta.marketID, 100)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, lighterNormalizeErr(product, exactLookupOperation, err)
	}
	if inactive == nil || inactive.Code != 200 {
		return exchange.ExactOrderLookupResult{}, lighterMalformed(product, exactLookupOperation, "inactive orders response is malformed")
	}
	order, found, err = lighterExactOrderFromRows(inactive.Orders, request, meta)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, lighterMalformed(product, exactLookupOperation, err.Error())
	}
	if found {
		return exactLookupFound(request, exchange.VenueLighter, product, instrument, order)
	}
	return exactLookupUnconfirmed(request, exchange.VenueLighter, product, instrument, inactive.NextCursor)
}

func lighterExactOrderFromRows(
	rows []*lighter.Order,
	request exchange.ExactOrderLookupRequest,
	meta lighterMarketMeta,
) (exchange.Order, bool, error) {
	var match *lighter.Order
	for _, row := range rows {
		if row == nil {
			return exchange.Order{}, false, strconv.ErrSyntax
		}
		if row.MarketIndex != meta.marketID {
			return exchange.Order{}, false, strconv.ErrRange
		}
		if lighterOrderOutsideExchangeSubset(row) || !lighterExactOrderMatches(row, request) {
			continue
		}
		if match != nil {
			return exchange.Order{}, false, strconv.ErrRange
		}
		match = row
	}
	if match == nil {
		return exchange.Order{}, false, nil
	}
	order, err := lighterOrder(match, meta)
	if err != nil {
		return exchange.Order{}, false, err
	}
	order.Status, err = exactPortableOrderStatus(order.Status)
	if err != nil {
		return exchange.Order{}, false, err
	}
	return order, true, nil
}

func lighterExactOrderMatches(row *lighter.Order, request exchange.ExactOrderLookupRequest) bool {
	if request.ClientOrderID != "" {
		return lighterOrderClientID(row.ClientOrderIndex) == request.ClientOrderID
	}
	return strconv.FormatInt(row.OrderIndex, 10) == request.VenueOrderID
}
