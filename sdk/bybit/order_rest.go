package sdk

import (
	"context"
	"fmt"
	"strconv"
)

func (c *Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*OrderActionResponse, error) {
	var resp responseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/create", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: place order failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) BatchPlaceOrders(ctx context.Context, req BatchPlaceOrdersRequest) (*BatchOrderActionResult, error) {
	var resp responseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/create-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: batch place orders failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*OrderActionResponse, error) {
	var resp responseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/cancel", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: cancel order failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) BatchCancelOrders(ctx context.Context, req BatchCancelOrdersRequest) (*BatchOrderActionResult, error) {
	var resp responseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/cancel-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: batch cancel orders failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) CancelAllOrders(ctx context.Context, req CancelAllOrdersRequest) error {
	var resp responseEnvelope[map[string]any]
	err := c.postPrivate(ctx, "/v5/order/cancel-all", req, &resp)
	if err != nil {
		return err
	}
	if resp.RetCode != 0 {
		return fmt.Errorf("bybit sdk: cancel all orders failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return nil
}

func (c *Client) AmendOrder(ctx context.Context, req AmendOrderRequest) (*OrderActionResponse, error) {
	var resp responseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/amend", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: amend order failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) BatchAmendOrders(ctx context.Context, req BatchAmendOrdersRequest) (*BatchOrderActionResult, error) {
	var resp responseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/amend-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	if resp.RetCode != 0 {
		return nil, fmt.Errorf("bybit sdk: batch amend orders failed: %d %s", resp.RetCode, resp.RetMsg)
	}
	return &resp.Result, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	return c.GetRealtimeOrders(ctx, category, symbol, "", "", "", 0)
}

func (c *Client) GetOrderHistory(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	return c.GetOrderHistoryFiltered(ctx, category, symbol, "", "")
}

func (c *Client) GetOrderHistoryFiltered(ctx context.Context, category, symbol, orderID, orderLinkID string) ([]OrderRecord, error) {
	var out []OrderRecord
	cursor := ""

	for {
		query := map[string]string{
			"category": category,
			"limit":    strconv.Itoa(50),
			"cursor":   cursor,
		}
		if symbol != "" {
			query["symbol"] = symbol
		}
		if orderID != "" {
			query["orderId"] = orderID
		}
		if orderLinkID != "" {
			query["orderLinkId"] = orderLinkID
		}

		var resp responseEnvelope[OrdersResult]
		err := c.getPrivate(ctx, "/v5/order/history", query, &resp)
		if err != nil {
			return nil, err
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit sdk: get order history failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		out = append(out, resp.Result.List...)
		if resp.Result.NextPageCursor == "" || orderID != "" || orderLinkID != "" {
			return out, nil
		}
		cursor = resp.Result.NextPageCursor
	}
}

func (c *Client) GetRealtimeOrders(ctx context.Context, category, symbol, settleCoin, orderID, orderLinkID string, openOnly int) ([]OrderRecord, error) {
	var out []OrderRecord
	cursor := ""

	for {
		query := map[string]string{
			"category": category,
			"limit":    strconv.Itoa(50),
			"cursor":   cursor,
		}
		if symbol != "" {
			query["symbol"] = symbol
		}
		if settleCoin != "" {
			query["settleCoin"] = settleCoin
		}
		if orderID != "" {
			query["orderId"] = orderID
		}
		if orderLinkID != "" {
			query["orderLinkId"] = orderLinkID
		}
		if openOnly >= 0 {
			query["openOnly"] = fmt.Sprintf("%d", openOnly)
		}

		var resp responseEnvelope[OrdersResult]
		err := c.getPrivate(ctx, "/v5/order/realtime", query, &resp)
		if err != nil {
			return nil, err
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit sdk: get realtime orders failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		out = append(out, resp.Result.List...)
		if resp.Result.NextPageCursor == "" || orderID != "" || orderLinkID != "" {
			return out, nil
		}
		cursor = resp.Result.NextPageCursor
	}
}

func (c *Client) GetExecutions(ctx context.Context, category, symbol, orderID, orderLinkID string) ([]ExecutionRecord, error) {
	var out []ExecutionRecord
	cursor := ""

	for {
		query := map[string]string{
			"category": category,
			"limit":    strconv.Itoa(50),
			"cursor":   cursor,
		}
		if symbol != "" {
			query["symbol"] = symbol
		}
		if orderID != "" {
			query["orderId"] = orderID
		}
		if orderLinkID != "" {
			query["orderLinkId"] = orderLinkID
		}

		var resp responseEnvelope[ExecutionsResult]
		err := c.getPrivate(ctx, "/v5/execution/list", query, &resp)
		if err != nil {
			return nil, err
		}
		if resp.RetCode != 0 {
			return nil, fmt.Errorf("bybit sdk: get executions failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		out = append(out, resp.Result.List...)
		if resp.Result.NextPageCursor == "" || orderID != "" || orderLinkID != "" {
			return out, nil
		}
		cursor = resp.Result.NextPageCursor
	}
}
