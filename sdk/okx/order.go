package okx

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// PlaceOrder submits a new order.
func (c *Client) PlaceOrder(ctx context.Context, req *OrderRequest) ([]OrderId, error) {
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/trade/order", req, true)
}

// PlaceAlgoOrder submits a venue-native strategy/algo order.
func (c *Client) PlaceAlgoOrder(ctx context.Context, req *AlgoOrderRequest) ([]AlgoOrderID, error) {
	return Request[AlgoOrderID](c, ctx, MethodPost, "/api/v5/trade/order-algo", req, true)
}

// PlaceSpreadOrder submits an OKX Nitro spread order.
func (c *Client) PlaceSpreadOrder(ctx context.Context, req *SpreadOrderRequest) ([]OrderId, error) {
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/sprd/order", req, true)
}

// ModifyOrder amends an incomplete order.
func (c *Client) ModifyOrder(ctx context.Context, req *ModifyOrderRequest) ([]OrderId, error) {
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/trade/amend-order", req, true)
}

// AmendAlgoOrder amends a venue-native strategy/algo order.
func (c *Client) AmendAlgoOrder(ctx context.Context, req *AmendAlgoOrderRequest) ([]AlgoOrderID, error) {
	return Request[AlgoOrderID](c, ctx, MethodPost, "/api/v5/trade/amend-algos", req, true)
}

// CancelOrder cancels an incomplete order.
func (c *Client) CancelOrder(ctx context.Context, instId, ordId, clOrdId string) ([]OrderId, error) {
	req := map[string]string{
		"instId": instId,
	}
	if ordId != "" {
		req["ordId"] = ordId
	}
	if clOrdId != "" {
		req["clOrdId"] = clOrdId
	}

	return Request[OrderId](c, ctx, MethodPost, "/api/v5/trade/cancel-order", req, true)
}

// CancelSpreadOrder cancels an incomplete OKX Nitro spread order.
func (c *Client) CancelSpreadOrder(ctx context.Context, sprdId, ordId, clOrdId string) ([]OrderId, error) {
	req := SpreadCancelRequest{SprdId: sprdId, OrdId: ordId, ClOrdId: clOrdId}
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/sprd/cancel-order", req, true)
}

// CancelAlgoOrders cancels venue-native strategy/algo orders.
func (c *Client) CancelAlgoOrders(ctx context.Context, reqs []AlgoCancelRequest) ([]AlgoOrderID, error) {
	return Request[AlgoOrderID](c, ctx, MethodPost, "/api/v5/trade/cancel-algos", reqs, true)
}

// CancelAdvanceAlgoOrders cancels venue-native trailing/advanced strategy orders.
func (c *Client) CancelAdvanceAlgoOrders(ctx context.Context, reqs []AlgoCancelRequest) ([]AlgoOrderID, error) {
	return Request[AlgoOrderID](c, ctx, MethodPost, "/api/v5/trade/cancel-advance-algos", reqs, true)
}

// CancelOrders cancels multiple orders (max 20).
func (c *Client) CancelOrders(ctx context.Context, reqs []CancelOrderRequest) ([]OrderId, error) {
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/trade/cancel-batch-orders", reqs, true)
}

// CancelAllSpreadOrders mass-cancels incomplete orders for one OKX Nitro spread.
func (c *Client) CancelAllSpreadOrders(ctx context.Context, sprdId string) ([]OrderId, error) {
	return Request[OrderId](c, ctx, MethodPost, "/api/v5/sprd/mass-cancel", SpreadMassCancelRequest{SprdId: sprdId}, true)
}

// ClosePosition closes a position.
func (c *Client) ClosePosition(ctx context.Context, instId, mgnMode string) ([]ClosePosition, error) {
	req := map[string]string{
		"instId":  instId,
		"mgnMode": mgnMode,
		"autoCxl": "true", // default true: auto cancel incomplete orders
	}

	return Request[ClosePosition](c, ctx, MethodPost, "/api/v5/trade/close-position", req, true)
}

// GetOrder retrieves order details.
func (c *Client) GetOrder(ctx context.Context, instId, ordId, clOrdId string) ([]Order, error) {
	params := url.Values{}
	params.Add("instId", instId)
	if ordId != "" {
		params.Add("ordId", ordId)
	}
	if clOrdId != "" {
		params.Add("clOrdId", clOrdId)
	}

	path := "/api/v5/trade/order"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[Order](c, ctx, MethodGet, path, nil, true)
}

// GetSpreadOrder retrieves one OKX Nitro spread order.
func (c *Client) GetSpreadOrder(ctx context.Context, sprdId, ordId, clOrdId string) ([]SpreadOrder, error) {
	params := url.Values{}
	params.Add("sprdId", sprdId)
	if ordId != "" {
		params.Add("ordId", ordId)
	}
	if clOrdId != "" {
		params.Add("clOrdId", clOrdId)
	}

	path := "/api/v5/sprd/order"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[SpreadOrder](c, ctx, MethodGet, path, nil, true)
}

// GetAlgoOrder retrieves a single strategy/algo order by algo ID or client algo ID.
func (c *Client) GetAlgoOrder(ctx context.Context, algoId, algoClOrdId string) ([]AlgoOrder, error) {
	params := url.Values{}
	if algoId != "" {
		params.Add("algoId", algoId)
	}
	if algoClOrdId != "" {
		params.Add("algoClOrdId", algoClOrdId)
	}

	path := "/api/v5/trade/order-algo"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[AlgoOrder](c, ctx, MethodGet, path, nil, true)
}

// GetOrders retrieves pending orders.
// instType: SPOT, MARGIN, SWAP, FUTURES, OPTION
// instId: optional, Instrument ID
func (c *Client) GetOrders(ctx context.Context, instType, instId *string) ([]Order, error) {
	params := url.Values{}
	if instType != nil {
		params.Add("instType", *instType)
	}
	if instId != nil {
		params.Add("instId", *instId)
	}

	path := "/api/v5/trade/orders-pending"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[Order](c, ctx, MethodGet, path, nil, true)
}

// GetSpreadOrders retrieves pending OKX Nitro spread orders.
func (c *Client) GetSpreadOrders(ctx context.Context, sprdId *string) ([]SpreadOrder, error) {
	params := url.Values{}
	if sprdId != nil {
		params.Add("sprdId", *sprdId)
	}

	path := "/api/v5/sprd/orders-pending"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[SpreadOrder](c, ctx, MethodGet, path, nil, true)
}

// GetPendingAlgoOrders retrieves pending strategy/algo orders.
func (c *Client) GetPendingAlgoOrders(ctx context.Context, instType, instId, ordType, algoId, algoClOrdId string) ([]AlgoOrder, error) {
	ordType = strings.TrimSpace(ordType)
	if ordType == "" {
		return nil, fmt.Errorf("okx: pending algo order ordType is required")
	}
	params := url.Values{}
	if instType != "" {
		params.Add("instType", instType)
	}
	if instId != "" {
		params.Add("instId", instId)
	}
	params.Add("ordType", ordType)
	if algoId != "" {
		params.Add("algoId", algoId)
	}
	if algoClOrdId != "" {
		params.Add("algoClOrdId", algoClOrdId)
	}

	path := "/api/v5/trade/orders-algo-pending"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[AlgoOrder](c, ctx, MethodGet, path, nil, true)
}

// GetFills retrieves recently-filled transaction details.
func (c *Client) GetFills(ctx context.Context, instType, instId, ordId *string, limit int) ([]Fill, error) {
	params := url.Values{}
	if instType != nil {
		params.Add("instType", *instType)
	}
	if instId != nil {
		params.Add("instId", *instId)
	}
	if ordId != nil {
		params.Add("ordId", *ordId)
	}
	if limit > 0 {
		params.Add("limit", strconv.Itoa(limit))
	}

	path := "/api/v5/trade/fills"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[Fill](c, ctx, MethodGet, path, nil, true)
}

// GetSpreadTrades retrieves OKX Nitro spread trades.
func (c *Client) GetSpreadTrades(ctx context.Context, sprdId, ordId *string, limit int) ([]SpreadFill, error) {
	params := url.Values{}
	if sprdId != nil {
		params.Add("sprdId", *sprdId)
	}
	if ordId != nil {
		params.Add("ordId", *ordId)
	}
	if limit > 0 {
		params.Add("limit", strconv.Itoa(limit))
	}

	path := "/api/v5/sprd/trades"
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	return Request[SpreadFill](c, ctx, MethodGet, path, nil, true)
}
