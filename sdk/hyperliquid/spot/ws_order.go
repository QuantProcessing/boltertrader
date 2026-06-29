package spot

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

// PlaceOrder via WS
func (c *WebsocketClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (chan hyperliquid.PostResult, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildPlaceOrderAction(req)
	if err != nil {
		return nil, err
	}

	nonce := c.GetNextNonce()
	sig, err := hyperliquid.SignL1Action(c.PrivateKey, action, c.Vault, nonce, nil, true)
	if err != nil {
		return nil, err
	}

	return c.PostAction(action, sig, nonce)
}

// CancelOrder via WS
func (c *WebsocketClient) CancelOrder(ctx context.Context, req CancelOrderRequest) (chan hyperliquid.PostResult, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildCancelOrderAction(req)
	if err != nil {
		return nil, err
	}

	nonce := c.GetNextNonce()
	sig, err := hyperliquid.SignL1Action(c.PrivateKey, action, c.Vault, nonce, nil, true)
	if err != nil {
		return nil, err
	}

	return c.PostAction(action, sig, nonce)
}

// Helpers (Duplicated from perp/action_helpers.go for now to avoid dependency on perp types)

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
			ReduceOnly: false, // Spot doesn't have ReduceOnly
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

// ModifyOrder via WS
func (c *WebsocketClient) ModifyOrder(ctx context.Context, req ModifyOrderRequest) (chan hyperliquid.PostResult, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildModifyOrderAction(req)
	if err != nil {
		return nil, err
	}

	nonce := c.GetNextNonce()
	sig, err := hyperliquid.SignL1Action(c.PrivateKey, action, c.Vault, nonce, nil, true)
	if err != nil {
		return nil, err
	}

	return c.PostAction(action, sig, nonce)
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
		ReduceOnly: false, // Spot doesn't have ReduceOnly
		OrderType:  orderType,
	}

	return hyperliquid.ModifyOrderAction{
		Type:  "modify",
		Oid:   wireOid,
		Order: order,
	}, nil
}
