package sdk

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const maxPaginationPages = 1000

const maxOrderHistoryWindowMillis = int64(7 * 24 * time.Hour / time.Millisecond)

func (c *Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*OrderActionResponse, error) {
	var resp commandResponseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/create", req, &resp)
	if err != nil {
		return nil, err
	}
	result, err := commandResult("place order", resp)
	if err != nil {
		return nil, err
	}
	if err := validateOrderActionResult("place order", result, "", req.OrderLinkID); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) BatchPlaceOrders(ctx context.Context, req BatchPlaceOrdersRequest) (*BatchOrderActionResult, error) {
	var resp commandResponseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/create-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	return commandResult("batch place orders", resp)
}

func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*OrderActionResponse, error) {
	var resp commandResponseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/cancel", req, &resp)
	if err != nil {
		return nil, err
	}
	result, err := commandResult("cancel order", resp)
	if err != nil {
		return nil, err
	}
	if err := validateOrderActionResult("cancel order", result, req.OrderID, req.OrderLinkID); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) BatchCancelOrders(ctx context.Context, req BatchCancelOrdersRequest) (*BatchOrderActionResult, error) {
	var resp commandResponseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/cancel-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	return commandResult("batch cancel orders", resp)
}

func (c *Client) CancelAllOrders(ctx context.Context, req CancelAllOrdersRequest) error {
	var resp commandResponseEnvelope[map[string]any]
	err := c.postPrivate(ctx, "/v5/order/cancel-all", req, &resp)
	if err != nil {
		return err
	}
	_, err = commandResult("cancel all orders", resp)
	return err
}

func (c *Client) AmendOrder(ctx context.Context, req AmendOrderRequest) (*OrderActionResponse, error) {
	var resp commandResponseEnvelope[OrderActionResponse]
	err := c.postPrivate(ctx, "/v5/order/amend", req, &resp)
	if err != nil {
		return nil, err
	}
	result, err := commandResult("amend order", resp)
	if err != nil {
		return nil, err
	}
	if err := validateOrderActionResult("amend order", result, req.OrderID, req.OrderLinkID); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *Client) BatchAmendOrders(ctx context.Context, req BatchAmendOrdersRequest) (*BatchOrderActionResult, error) {
	var resp commandResponseEnvelope[BatchOrderActionResult]
	err := c.postPrivate(ctx, "/v5/order/amend-batch", req, &resp)
	if err != nil {
		return nil, err
	}
	return commandResult("batch amend orders", resp)
}

func (c *Client) GetOpenOrders(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	return c.GetRealtimeOrders(ctx, category, symbol, "", "", "", 0)
}

func (c *Client) GetOrderHistory(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	return c.GetOrderHistoryWithRequest(ctx, GetOrderHistoryRequest{Category: category, Symbol: symbol})
}

func (c *Client) GetOrderHistoryFiltered(ctx context.Context, category, symbol, orderID, orderLinkID string) ([]OrderRecord, error) {
	return c.GetOrderHistoryWithRequest(ctx, GetOrderHistoryRequest{
		Category:    category,
		Symbol:      symbol,
		OrderID:     orderID,
		OrderLinkID: orderLinkID,
	})
}

func (c *Client) GetOrderHistoryFilteredScoped(ctx context.Context, category, symbol, settleCoin, orderID, orderLinkID string) ([]OrderRecord, error) {
	return c.GetOrderHistoryWithRequest(ctx, GetOrderHistoryRequest{
		Category:    category,
		Symbol:      symbol,
		SettleCoin:  settleCoin,
		OrderID:     orderID,
		OrderLinkID: orderLinkID,
	})
}

type GetOrderHistoryRequest struct {
	Category    string
	Symbol      string
	SettleCoin  string
	OrderID     string
	OrderLinkID string
	StartMillis int64
	EndMillis   int64
}

func (c *Client) GetOrderHistoryWithRequest(ctx context.Context, req GetOrderHistoryRequest) ([]OrderRecord, error) {
	maxInt := int(^uint(0) >> 1)
	var out []OrderRecord
	_, err := c.scanOrderHistory(ctx, req, maxInt, func(records []OrderRecord) (bool, error) {
		out = append(out, records...)
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ScanOrderHistory visits order-history pages until the visitor reports that
// its targets are complete, the venue exhausts the cursor, or maxRecords have
// been visited. saturated is true only when maxRecords stopped an otherwise
// incomplete cursor traversal.
func (c *Client) ScanOrderHistory(
	ctx context.Context,
	req GetOrderHistoryRequest,
	maxRecords int,
	visit func([]OrderRecord) (bool, error),
) (saturated bool, err error) {
	if maxRecords <= 0 {
		return false, fmt.Errorf("bybit sdk: order history record limit must be positive")
	}
	if visit == nil {
		return false, fmt.Errorf("bybit sdk: order history visitor is nil")
	}
	return c.scanOrderHistory(ctx, req, maxRecords, visit)
}

func (c *Client) scanOrderHistory(
	ctx context.Context,
	req GetOrderHistoryRequest,
	maxRecords int,
	visit func([]OrderRecord) (bool, error),
) (bool, error) {
	if req.StartMillis > 0 && req.EndMillis > 0 {
		if req.EndMillis < req.StartMillis {
			return false, fmt.Errorf("bybit sdk: order history end time precedes start time")
		}
		if req.EndMillis-req.StartMillis > maxOrderHistoryWindowMillis {
			return false, fmt.Errorf("bybit sdk: order history window cannot exceed seven days")
		}
	}

	cursor := ""
	seenCursors := make(map[string]struct{})
	visited := 0

	for page := 1; ; page++ {
		pageLimit := 50
		if remaining := maxRecords - visited; remaining < pageLimit {
			pageLimit = remaining
		}
		query := map[string]string{
			"category": req.Category,
			"limit":    strconv.Itoa(pageLimit),
			"cursor":   cursor,
		}
		if req.Symbol != "" {
			query["symbol"] = req.Symbol
		}
		if req.SettleCoin != "" {
			query["settleCoin"] = req.SettleCoin
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

		var resp responseEnvelope[OrdersResult]
		err := c.getPrivate(ctx, "/v5/order/history", query, &resp)
		if err != nil {
			return false, err
		}
		if resp.RetCode != 0 {
			return false, fmt.Errorf("bybit sdk: get order history failed: %d %s", resp.RetCode, resp.RetMsg)
		}

		records := resp.Result.List
		truncated := len(records) > pageLimit
		if truncated {
			records = records[:pageLimit]
		}
		visited += len(records)
		done, err := visit(records)
		if err != nil {
			return false, err
		}
		if done {
			return false, nil
		}
		if truncated {
			return true, nil
		}
		if resp.Result.NextPageCursor == "" || req.OrderID != "" || req.OrderLinkID != "" {
			return false, nil
		}
		if visited >= maxRecords {
			return true, nil
		}
		cursor, err = nextPaginationCursor("get order history", cursor, resp.Result.NextPageCursor, page, seenCursors)
		if err != nil {
			return false, err
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
