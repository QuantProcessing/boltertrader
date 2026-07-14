package perp

import (
	"fmt"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// Shared helper functions for building actions (used by both HTTP and WS clients)

func buildPlaceOrderAction(orders ...PlaceOrderRequest) (hyperliquid.CreateOrderAction, error) {
	orderRequest := make([]hyperliquid.OrderWire, len(orders))
	for i, order := range orders {
		price, err := hyperliquid.FloatToString(order.Price)
		if err != nil {
			return hyperliquid.CreateOrderAction{}, err
		}
		size, err := hyperliquid.FloatToString(order.Size)
		if err != nil {
			return hyperliquid.CreateOrderAction{}, err
		}
		orderType := hyperliquid.OrderTypeWire{}
		if order.OrderType.Limit != nil {
			if err := validateLimitTIF(order.OrderType.Limit.Tif); err != nil {
				return hyperliquid.CreateOrderAction{}, err
			}
			orderType.Limit = &hyperliquid.OrderTypeWireLimit{
				Tif: order.OrderType.Limit.Tif,
			}
		}
		if order.OrderType.Trigger != nil {
			triggerPrice, err := hyperliquid.FloatToString(order.OrderType.Trigger.TriggerPx)
			if err != nil {
				return hyperliquid.CreateOrderAction{}, err
			}
			orderType.Trigger = &hyperliquid.OrderTypeWireTrigger{
				IsMarket:  order.OrderType.Trigger.IsMarket,
				TriggerPx: triggerPrice,
				Tpsl:      order.OrderType.Trigger.Tpsl,
			}
		}
		orderRequest[i] = hyperliquid.OrderWire{
			Asset:      order.AssetID,
			IsBuy:      order.IsBuy,
			LimitPx:    price,
			Size:       size,
			ReduceOnly: order.ReduceOnly,
			OrderType:  orderType,
			Cloid:      order.ClientOrderID,
		}
	}

	return hyperliquid.CreateOrderAction{
		Type:     "order",
		Orders:   orderRequest,
		Grouping: string(hyperliquid.GroupingNA),
		Builder:  nil,
	}, nil
}

func buildCancelOrderAction(req CancelOrderRequest) (hyperliquid.CancelOrderAction, error) {
	return buildCancelOrdersAction([]CancelOrderRequest{req})
}

func buildCancelOrdersAction(reqs []CancelOrderRequest) (hyperliquid.CancelOrderAction, error) {
	cancels := make([]hyperliquid.CancelOrderWire, 0, len(reqs))
	for _, req := range reqs {
		cancels = append(cancels, hyperliquid.CancelOrderWire{
			Asset:   req.AssetID,
			OrderId: req.OrderID,
		})
	}
	return hyperliquid.CancelOrderAction{
		Type:    "cancel",
		Cancels: cancels,
	}, nil
}

func buildModifyOrderAction(req ModifyOrderRequest) (hyperliquid.ModifyOrderAction, error) {
	var wireOid any
	switch {
	case req.Oid != nil && req.Cloid != nil:
		return hyperliquid.ModifyOrderAction{}, fmt.Errorf("modify request must specify only one of Oid or Cloid")
	case req.Oid != nil:
		wireOid = *req.Oid
	case req.Cloid != nil:
		wireOid = *req.Cloid
	default:
		return hyperliquid.ModifyOrderAction{}, fmt.Errorf("modify request must specify either Oid or Cloid")
	}

	priceWire, err := hyperliquid.FloatToString(req.Order.Price)
	if err != nil {
		return hyperliquid.ModifyOrderAction{}, fmt.Errorf("failed to wire price: %w", err)
	}

	sizeWire, err := hyperliquid.FloatToString(req.Order.Size)
	if err != nil {
		return hyperliquid.ModifyOrderAction{}, fmt.Errorf("failed to wire size: %w", err)
	}

	orderType := hyperliquid.OrderTypeWire{}
	if req.Order.OrderType.Limit != nil {
		if err := validateLimitTIF(req.Order.OrderType.Limit.Tif); err != nil {
			return hyperliquid.ModifyOrderAction{}, err
		}
		orderType.Limit = &hyperliquid.OrderTypeWireLimit{
			Tif: req.Order.OrderType.Limit.Tif,
		}
	}
	if req.Order.OrderType.Trigger != nil {
		triggerPrice, err := hyperliquid.FloatToString(req.Order.OrderType.Trigger.TriggerPx)
		if err != nil {
			return hyperliquid.ModifyOrderAction{}, err
		}
		orderType.Trigger = &hyperliquid.OrderTypeWireTrigger{
			IsMarket:  req.Order.OrderType.Trigger.IsMarket,
			TriggerPx: triggerPrice,
			Tpsl:      req.Order.OrderType.Trigger.Tpsl,
		}
	}

	order := hyperliquid.OrderWire{
		Asset:      req.Order.AssetID,
		IsBuy:      req.Order.IsBuy,
		LimitPx:    priceWire,
		Size:       sizeWire,
		ReduceOnly: req.Order.ReduceOnly,
		OrderType:  orderType,
		Cloid:      req.Order.ClientOrderID,
	}

	return hyperliquid.ModifyOrderAction{
		Type:  "modify",
		Oid:   wireOid,
		Order: order,
	}, nil
}

func validateLimitTIF(tif hyperliquid.Tif) error {
	switch tif {
	case hyperliquid.TifGtc, hyperliquid.TifIoc, hyperliquid.TifAlo:
		return nil
	default:
		return fmt.Errorf("unsupported limit TIF %q: Hyperliquid accepts only Gtc, Ioc, or Alo", tif)
	}
}
