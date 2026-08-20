package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
)

// LookupExactOrder gives Bitget the same bounded, instrument-scoped recovery
// primitive as the other writable venues. The gateway requires this interface
// before it admits a route because an ambiguous place/cancel result must be
// reconciled by one exact venue identity, never inferred from a truncated
// account-wide page.
func (client *bitgetBase) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueBitget, client.meta.product)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	native, _, err := client.symbols(instrument, exactLookupOperation)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	row, err := client.sdk.GetOrder(ctx, client.bitgetPrivateCategory(), native, request.VenueOrderID, request.ClientOrderID)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, normErr(client.meta, exactLookupOperation, err)
	}
	if row == nil {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response is missing")
	}
	if row.Symbol != native {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	if request.ClientOrderID != "" && row.ClientOID != request.ClientOrderID {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response client order id mismatch")
	}
	if request.VenueOrderID != "" && row.OrderID != request.VenueOrderID {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response order id mismatch")
	}
	orders, err := convertBitgetOrders(ctx, client.meta, exactLookupOperation, []bitget.OrderRecord{*row}, native)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if len(orders) != 1 {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response conversion mismatch")
	}
	status, err := exactPortableOrderStatus(orders[0].Status)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response has unsupported order status")
	}
	orders[0].Status = status
	if orders[0].NativeSymbol != instrument.Identity.NativeSymbol || orders[0].NativeID != instrument.Identity.NativeID {
		return exchange.ExactOrderLookupResult{}, malformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	return exactLookupFound(request, exchange.VenueBitget, client.meta.product, instrument, orders[0])
}

var (
	_ exchange.ExactOrderLookup = (*bitgetSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*bitgetPerpClient)(nil)
)
