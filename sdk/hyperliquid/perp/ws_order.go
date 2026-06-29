package perp

import (
	"context"

	"github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

// PlaceOrder via WS
func (c *WebsocketClient) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (chan hyperliquid.PostResult, error) {
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

// ModifyOrder via WS
func (c *WebsocketClient) ModifyOrder(ctx context.Context, req ModifyOrderRequest) (chan hyperliquid.PostResult, error) {
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
