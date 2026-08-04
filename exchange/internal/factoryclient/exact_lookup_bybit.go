package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func (client *bybitBase) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueBybit, client.meta.product)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if request.VenueOrderID != "" && !bybitNativeOrderID(request.VenueOrderID) {
		return exchange.ExactOrderLookupResult{}, invalid(client.meta, exactLookupOperation, "venue order id must be a positive decimal int64 or UUID")
	}
	native, _, err := client.symbols(instrument, exactLookupOperation)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}

	realtime, err := client.sdk.GetRealtimeOrders(ctx, client.category, native, client.settleCoin, request.VenueOrderID, request.ClientOrderID, 0)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, normErr(client.meta, exactLookupOperation, err)
	}
	result, found, err := client.bybitExactLookupResultFromRows(ctx, request, instrument, native, realtime)
	if err != nil || found {
		return result, err
	}

	history, err := client.sdk.GetOrderHistoryFilteredScoped(ctx, client.category, native, client.settleCoin, request.VenueOrderID, request.ClientOrderID)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, normErr(client.meta, exactLookupOperation, err)
	}
	result, found, err = client.bybitExactLookupResultFromRows(ctx, request, instrument, native, history)
	if err != nil || found {
		return result, err
	}
	return exactLookupUnconfirmed(request, exchange.VenueBybit, client.meta.product, instrument, "")
}

func (client *bybitBase) bybitExactLookupResultFromRows(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
	instrument exchange.Instrument,
	native string,
	rows []bybit.OrderRecord,
) (exchange.ExactOrderLookupResult, bool, error) {
	if len(rows) == 0 {
		return exchange.ExactOrderLookupResult{}, false, nil
	}
	if len(rows) != 1 {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response must contain exactly one row")
	}
	row := rows[0]
	if row.Symbol != native {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	if request.ClientOrderID != "" && row.OrderLinkID != request.ClientOrderID {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response client order id mismatch")
	}
	if request.VenueOrderID != "" && row.OrderID != request.VenueOrderID {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response order id mismatch")
	}
	orders, err := convertBybitOrders(ctx, client.meta, exactLookupOperation, rows, "")
	if err != nil {
		return exchange.ExactOrderLookupResult{}, false, err
	}
	if len(orders) != 1 {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response conversion mismatch")
	}
	status, err := exactPortableOrderStatus(orders[0].Status)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response has unsupported order status")
	}
	orders[0].Status = status
	if orders[0].NativeSymbol != instrument.Identity.NativeSymbol || orders[0].NativeID != instrument.Identity.NativeID {
		return exchange.ExactOrderLookupResult{}, false, malformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	result, err := exactLookupFound(request, exchange.VenueBybit, client.meta.product, instrument, orders[0])
	return result, err == nil, err
}

var (
	_ exchange.ExactOrderLookup = (*bybitSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*bybitPerpClient)(nil)
)
