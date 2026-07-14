package sdk

import (
	"context"
	"fmt"
	"strconv"
)

const maxPaginationPages = 1000

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
	return c.GetOrderHistoryFilteredScoped(ctx, category, symbol, "", "", "")
}

func (c *Client) GetOrderHistoryFiltered(ctx context.Context, category, symbol, orderID, orderLinkID string) ([]OrderRecord, error) {
	return c.GetOrderHistoryFilteredScoped(ctx, category, symbol, "", orderID, orderLinkID)
}

func (c *Client) GetOrderHistoryFilteredScoped(ctx context.Context, category, symbol, settleCoin, orderID, orderLinkID string) ([]OrderRecord, error) {
	var out []OrderRecord
	cursor := ""
	seenCursors := make(map[string]struct{})

	for page := 1; ; page++ {
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
		cursor, err = nextPaginationCursor("get order history", cursor, resp.Result.NextPageCursor, page, seenCursors)
		if err != nil {
			return nil, err
		}
	}
}

func (c *Client) GetRealtimeOrders(ctx context.Context, category, symbol, settleCoin, orderID, orderLinkID string, openOnly int) ([]OrderRecord, error) {
	var out []OrderRecord
	cursor := ""
	seenCursors := make(map[string]struct{})

	for page := 1; ; page++ {
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
		cursor, err = nextPaginationCursor("get realtime orders", cursor, resp.Result.NextPageCursor, page, seenCursors)
		if err != nil {
			return nil, err
		}
	}
}

func nextPaginationCursor(operation, current, next string, page int, seen map[string]struct{}) (string, error) {
	if next == current {
		return "", fmt.Errorf("bybit sdk: %s repeated cursor", operation)
	}
	if _, duplicate := seen[next]; duplicate {
		return "", fmt.Errorf("bybit sdk: %s repeated cursor", operation)
	}
	if page >= maxPaginationPages {
		return "", fmt.Errorf("bybit sdk: %s page limit %d exceeded", operation, maxPaginationPages)
	}
	seen[next] = struct{}{}
	return next, nil
}

type GetExecutionsRequest struct {
	Category    string
	Symbol      string
	OrderID     string
	OrderLinkID string
	StartMillis int64
	EndMillis   int64
	Limit       int
}

func (c *Client) GetExecutions(ctx context.Context, category, symbol, orderID, orderLinkID string) ([]ExecutionRecord, error) {
	records, _, err := c.getExecutions(ctx, GetExecutionsRequest{
		Category:    category,
		Symbol:      symbol,
		OrderID:     orderID,
		OrderLinkID: orderLinkID,
	}, false)
	return records, err
}

// GetExecutionsBounded follows execution-history cursors up to req.Limit rows.
// The boolean reports that additional venue rows remain beyond that hard cap.
func (c *Client) GetExecutionsBounded(ctx context.Context, req GetExecutionsRequest) ([]ExecutionRecord, bool, error) {
	return c.getExecutions(ctx, req, true)
}

func (c *Client) getExecutions(ctx context.Context, req GetExecutionsRequest, bounded bool) ([]ExecutionRecord, bool, error) {
	var out []ExecutionRecord
	cursor := ""
	overallLimit := req.Limit
	pageLimit := overallLimit
	if pageLimit <= 0 {
		pageLimit = 50
	}
	if pageLimit > 100 {
		pageLimit = 100
	}
	if bounded && overallLimit <= 0 {
		overallLimit = pageLimit
	}
	seenCursors := make(map[string]struct{})

	for pageNumber := 1; ; pageNumber++ {
		query := map[string]string{
			"category": req.Category,
			"limit":    strconv.Itoa(pageLimit),
			"cursor":   cursor,
		}
		if req.Symbol != "" {
			query["symbol"] = req.Symbol
		}
		if req.OrderID != "" {
			query["orderId"] = req.OrderID
		}
		if req.OrderLinkID != "" {
			query["orderLinkId"] = req.OrderLinkID
		}
		if req.StartMillis > 0 {
			query["startTime"] = strconv.FormatInt(req.StartMillis, 10)
		}
		if req.EndMillis > 0 {
			query["endTime"] = strconv.FormatInt(req.EndMillis, 10)
		}

		var resp responseEnvelope[ExecutionsResult]
		err := c.getPrivate(ctx, "/v5/execution/list", query, &resp)
		if err != nil {
			return nil, false, err
		}
		if resp.RetCode != 0 {
			return nil, false, fmt.Errorf("bybit sdk: get executions failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		page := resp.Result.List
		if bounded && len(out)+len(page) > overallLimit {
			page = page[:overallLimit-len(out)]
		}
		out = append(out, page...)
		if bounded {
			if len(out) >= overallLimit {
				return out, resp.Result.NextPageCursor != "" || len(resp.Result.List) > len(page), nil
			}
			if resp.Result.NextPageCursor == "" {
				return out, false, nil
			}
			remaining := overallLimit - len(out)
			pageLimit = remaining
			if pageLimit > 100 {
				pageLimit = 100
			}
		}
		if resp.Result.NextPageCursor == "" {
			return out, false, nil
		}
		next := resp.Result.NextPageCursor
		if _, duplicate := seenCursors[next]; duplicate || next == cursor {
			return nil, false, fmt.Errorf("bybit sdk: get executions repeated cursor %q", next)
		}
		if pageNumber >= maxPaginationPages {
			return nil, false, fmt.Errorf("bybit sdk: get executions page limit %d exceeded", maxPaginationPages)
		}
		seenCursors[next] = struct{}{}
		cursor = next
	}
}
