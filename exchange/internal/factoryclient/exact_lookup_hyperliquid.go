package factoryclient

import (
	"context"
	"errors"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

func (client *hyperliquidSpotClient) LookupExactOrder(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueHyperliquid, exchange.ProductSpot)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	meta, err := client.spotMeta(ctx, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	trackedCtx, tracker := hyperliquidWithRequestTracker(ctx)
	var row *hyperliquidspot.OrderStatusInfo
	if request.ClientOrderID != "" {
		row, err = client.sdk.OrderStatusByCloid(trackedCtx, client.sdk.AccountAddr, hlNativeClientOrderID(request.ClientOrderID))
	} else {
		orderID, parseErr := exactLookupPositiveInt64(request.VenueOrderID, exchange.VenueHyperliquid, exchange.ProductSpot, "venue order id")
		if parseErr != nil {
			return exchange.ExactOrderLookupResult{}, parseErr
		}
		row, err = client.sdk.OrderStatus(trackedCtx, client.sdk.AccountAddr, orderID)
	}
	if err != nil {
		if errors.Is(err, hyperliquid.ErrOrderNotFound) {
			return exactLookupNotFound(request, exchange.VenueHyperliquid, exchange.ProductSpot, instrument)
		}
		return exchange.ExactOrderLookupResult{}, hlNormalizeQueryErr(exchange.ProductSpot, exactLookupOperation, err, tracker)
	}
	order, err := hlExactSpotOrder(meta, row)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, hlMalformed(exchange.ProductSpot, exactLookupOperation, "malformed exact order response")
	}
	return exactLookupFound(request, exchange.VenueHyperliquid, exchange.ProductSpot, instrument, order)
}

func (client *hyperliquidPerpClient) LookupExactOrder(
	ctx context.Context,
	request exchange.ExactOrderLookupRequest,
) (exchange.ExactOrderLookupResult, error) {
	instrument, err := exactLookupInstrument(ctx, request, exchange.VenueHyperliquid, exchange.ProductPerp)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	meta, err := client.perpMeta(ctx, exactLookupOperation, instrument)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, err
	}
	trackedCtx, tracker := hyperliquidWithRequestTracker(ctx)
	var row *hyperliquidperp.OrderStatusInfo
	if request.ClientOrderID != "" {
		row, err = client.sdk.OrderStatusByCloid(trackedCtx, client.sdk.AccountAddr, hlNativeClientOrderID(request.ClientOrderID))
	} else {
		orderID, parseErr := exactLookupPositiveInt64(request.VenueOrderID, exchange.VenueHyperliquid, exchange.ProductPerp, "venue order id")
		if parseErr != nil {
			return exchange.ExactOrderLookupResult{}, parseErr
		}
		row, err = client.sdk.OrderStatus(trackedCtx, client.sdk.AccountAddr, orderID)
	}
	if err != nil {
		if errors.Is(err, hyperliquid.ErrOrderNotFound) {
			return exactLookupNotFound(request, exchange.VenueHyperliquid, exchange.ProductPerp, instrument)
		}
		return exchange.ExactOrderLookupResult{}, hlNormalizeQueryErr(exchange.ProductPerp, exactLookupOperation, err, tracker)
	}
	order, err := hlExactPerpOrder(meta, row)
	if err != nil {
		return exchange.ExactOrderLookupResult{}, hlMalformed(exchange.ProductPerp, exactLookupOperation, "malformed exact order response")
	}
	return exactLookupFound(request, exchange.VenueHyperliquid, exchange.ProductPerp, instrument, order)
}

func hlExactSpotOrder(meta hyperliquidMarketMeta, row *hyperliquidspot.OrderStatusInfo) (exchange.Order, error) {
	if row == nil {
		return exchange.Order{}, errors.New("missing order")
	}
	return hlExactOrder(meta, hlExactOrderInfo{
		coin: row.Coin, side: row.Side, limitPrice: row.LimitPx, remainingSize: row.Sz,
		orderID: row.Oid, clientOrderID: row.Cliod, timestamp: row.Timestamp,
		originalSize: row.OrigSz, status: row.Status, statusTimestamp: row.StatusTimestamp,
		reduceOnly: row.ReduceOnly, orderType: row.OrderType, timeInForce: row.Tif,
		isTrigger: row.IsTrigger, triggerPrice: row.TriggerPx,
	})
}

func hlExactPerpOrder(meta hyperliquidMarketMeta, row *hyperliquidperp.OrderStatusInfo) (exchange.Order, error) {
	if row == nil {
		return exchange.Order{}, errors.New("missing order")
	}
	return hlExactOrder(meta, hlExactOrderInfo{
		coin: row.Coin, side: row.Side, limitPrice: row.LimitPx, remainingSize: row.Sz,
		orderID: row.Oid, clientOrderID: row.Cliod, timestamp: row.Timestamp,
		originalSize: row.OrigSz, status: row.Status, statusTimestamp: row.StatusTimestamp,
		reduceOnly: row.ReduceOnly, orderType: row.OrderType, timeInForce: row.Tif,
		isTrigger: row.IsTrigger, triggerPrice: row.TriggerPx,
	})
}

type hlExactOrderInfo struct {
	coin, side, limitPrice, remainingSize string
	orderID                               int64
	clientOrderID                         string
	timestamp                             int64
	originalSize                          string
	status                                string
	statusTimestamp                       int64
	reduceOnly                            bool
	orderType, timeInForce                string
	isTrigger                             bool
	triggerPrice                          string
}

func hlExactOrder(meta hyperliquidMarketMeta, info hlExactOrderInfo) (exchange.Order, error) {
	if info.coin != meta.nativeCoin {
		return exchange.Order{}, errors.New("order instrument mismatch")
	}
	var clientOrderID *string
	if info.clientOrderID != "" {
		clientOrderID = &info.clientOrderID
	}
	order, err := hlHistoricalOrder(meta, hyperliquid.HistoricalOrder{
		Order: hyperliquid.HistoricalOrderDetails{
			Coin: info.coin, Side: hyperliquid.Side(info.side), LimitPrice: info.limitPrice,
			RemainingSize: info.remainingSize, OrderID: info.orderID, ClientOrderID: clientOrderID,
			Timestamp: info.timestamp, OriginalSize: info.originalSize, ReduceOnly: info.reduceOnly,
			OrderType: info.orderType, TimeInForce: info.timeInForce, IsTrigger: info.isTrigger,
			TriggerPrice: info.triggerPrice,
		},
		Status:          info.status,
		StatusTimestamp: info.statusTimestamp,
	})
	if err != nil {
		return exchange.Order{}, err
	}
	order.Status, err = exactPortableOrderStatus(order.Status)
	if err != nil {
		return exchange.Order{}, err
	}
	return order, nil
}
