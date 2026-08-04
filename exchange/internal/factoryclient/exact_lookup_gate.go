package factoryclient

import (
	"context"
	"errors"
	"strings"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
)

func (client *gateSpotClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueGate, exchange.ProductSpot)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	native, err := gateRequiredNativeInstrument(client.meta, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	lookupID := gateExactLookupOrderID(request)
	row, err := client.sdk.GetSpotOrder(ctx, lookupID, native)
	if err != nil {
		if gateExactLookupNotFound(err) {
			return exactLookupNotFound(request, exchange.VenueGate, exchange.ProductSpot, instrument)
		}
		return exchange.ExactOrderLookupResult{}, gateNormalizeErr(client.meta, exactLookupOperation, err)
	}
	if row == nil {
		return exchange.ExactOrderLookupResult{}, gateMalformed(client.meta, exactLookupOperation, "missing exact order response")
	}
	if err := gateValidateSpotOrders(client.meta, exactLookupOperation, []gate.Order{*row}); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if row.CurrencyPair != native {
		return exchange.ExactOrderLookupResult{}, gateMalformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	order := gateSpotOrder(*row, instrument)
	return gateExactLookupFound(client.meta, request, exchange.ProductSpot, instrument, order)
}

func (client *gatePerpClient) LookupExactOrder(ctx context.Context, request exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueGate, exchange.ProductPerp)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	native, err := gateRequiredNativeInstrument(client.meta, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	multiplier, err := client.gateContractMultiplier(exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	lookupID := gateExactLookupOrderID(request)
	row, err := client.sdk.GetFuturesOrderByID(ctx, client.settle, lookupID)
	if err != nil {
		if gateExactLookupNotFound(err) {
			return exactLookupNotFound(request, exchange.VenueGate, exchange.ProductPerp, instrument)
		}
		return exchange.ExactOrderLookupResult{}, gateNormalizeErr(client.meta, exactLookupOperation, err)
	}
	if row == nil {
		return exchange.ExactOrderLookupResult{}, gateMalformed(client.meta, exactLookupOperation, "missing exact order response")
	}
	if err := gateValidateFuturesOrders(client.meta, exactLookupOperation, []gate.FuturesOrder{*row}); err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	if row.Contract != native {
		return exchange.ExactOrderLookupResult{}, gateMalformed(client.meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	order := gateFuturesOrder(*row, instrument, multiplier)
	return gateExactLookupFound(client.meta, request, exchange.ProductPerp, instrument, order)
}

func gateExactLookupOrderID(request exchange.ExactOrderLookupRequest) string {
	if request.ClientOrderID != "" {
		return gateClientOrderID(request.ClientOrderID)
	}
	return request.VenueOrderID
}

func gateExactLookupFound(
	meta clientMeta,
	request exchange.ExactOrderLookupRequest,
	product exchange.Product,
	instrument exchange.Instrument,
	order exchange.Order,
) (exchange.ExactOrderLookupResult, error) {
	status, err := gateExactPortableOrderStatus(order)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, gateMalformed(meta, exactLookupOperation, "exact order lookup response has unsupported order status")
	}
	order.Status = status
	if request.ClientOrderID != "" && order.ClientOrderID != request.ClientOrderID {
		return exchange.ExactOrderLookupResult{}, gateMalformed(meta, exactLookupOperation, "exact order lookup response client order id mismatch")
	}
	if request.VenueOrderID != "" && order.OrderID != request.VenueOrderID {
		return exchange.ExactOrderLookupResult{}, gateMalformed(meta, exactLookupOperation, "exact order lookup response order id mismatch")
	}
	if order.NativeSymbol != instrument.Identity.NativeSymbol || order.NativeID != instrument.Identity.NativeID {
		return exchange.ExactOrderLookupResult{}, gateMalformed(meta, exactLookupOperation, "exact order lookup response instrument mismatch")
	}
	return exactLookupFound(request, exchange.VenueGate, product, instrument, order)
}

func gateExactPortableOrderStatus(order exchange.Order) (string, error) {
	if order.Quantity.IsPositive() {
		if order.Filled.GreaterThanOrEqual(order.Quantity) {
			return "filled", nil
		}
		if order.Filled.IsPositive() {
			return "partially_filled", nil
		}
	} else if order.Filled.IsPositive() {
		return "partially_filled", nil
	}

	raw := order.Status
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "_new", "_update", "open":
		return "open", nil
	case "filled", "succeeded":
		return "filled", nil
	case "failed", "small", "depth_not_enough", "trader_not_enough":
		return "rejected", nil
	case "ioc", "poc", "stp", "liquidated", "auto_deleveraging", "auto_deleveraged", "reduce_only", "position_close", "position_closed", "reduce_out", "cb", "expired":
		return "canceled", nil
	default:
		return exactPortableOrderStatus(raw)
	}
}

func gateExactLookupNotFound(err error) bool {
	var apiErr *gate.APIError
	return errors.As(err, &apiErr) && apiErr != nil && strings.EqualFold(apiErr.Label, "ORDER_NOT_FOUND")
}

var (
	_ exchange.ExactOrderLookup = (*gateSpotClient)(nil)
	_ exchange.ExactOrderLookup = (*gatePerpClient)(nil)
)
