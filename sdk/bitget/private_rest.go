package sdk

import (
	"context"
	"fmt"
	"strconv"
)

const privatePaginationMaxPages = 1000

type openOrdersPage struct {
	List   []OrderRecord `json:"list"`
	EndID  string        `json:"endId"`
	Cursor string        `json:"cursor"`
}

type orderHistoryPage struct {
	List   []OrderRecord `json:"list"`
	Cursor string        `json:"cursor"`
}

func (c *Client) PlaceOrder(ctx context.Context, req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	var out responseEnvelope[PlaceOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/place-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: place order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) BatchPlaceOrders(ctx context.Context, req []PlaceOrderRequest) ([]PlaceOrderResponse, error) {
	var out responseEnvelope[[]PlaceOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/place-batch", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch place orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) CancelOrder(ctx context.Context, req *CancelOrderRequest) (*CancelOrderResponse, error) {
	var out responseEnvelope[CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: cancel order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) BatchCancelOrders(ctx context.Context, req []CancelOrderRequest) ([]CancelOrderResponse, error) {
	var out responseEnvelope[[]CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-batch", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch cancel orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) BatchModifyOrders(ctx context.Context, req []ModifyOrderRequest) ([]CancelOrderResponse, error) {
	var out responseEnvelope[[]CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/batch-modify-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: batch modify orders failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) CancelAllOrders(ctx context.Context, req *CancelAllOrdersRequest) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/trade/cancel-symbol-order", req, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: cancel all orders failed: %s %s", out.Code, out.Msg)
	}
	return nil
}

func (c *Client) ModifyOrder(ctx context.Context, req *ModifyOrderRequest) (*CancelOrderResponse, error) {
	var out responseEnvelope[CancelOrderResponse]
	err := c.postPrivate(ctx, "/api/v3/trade/modify-order", req, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: modify order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOrder(ctx context.Context, category, symbol, orderID, clientOID string) (*OrderRecord, error) {
	var out responseEnvelope[OrderRecord]
	err := c.getPrivate(ctx, "/api/v3/trade/order-info", map[string]string{
		"category":  category,
		"symbol":    symbol,
		"orderId":   orderID,
		"clientOid": clientOID,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get order failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOpenOrders(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	const (
		pageLimit    = 100
		overallLimit = 1000
	)
	var records []OrderRecord
	cursor := ""
	seenCursors := make(map[string]struct{})
	pageCount := 0
	for {
		pageCount++
		if pageCount > privatePaginationMaxPages {
			return nil, fmt.Errorf("bitget sdk: get open orders exceeded %d-page safety limit", privatePaginationMaxPages)
		}
		page, err := c.getOpenOrdersPage(ctx, category, symbol, strconv.Itoa(pageLimit), cursor)
		if err != nil {
			return nil, err
		}
		if len(records)+len(page.List) > overallLimit {
			return nil, fmt.Errorf("bitget sdk: get open orders exceeded %d-record safety limit", overallLimit)
		}
		records = append(records, page.List...)
		if page.Cursor == "" {
			return records, nil
		}
		if len(page.List) == 0 {
			return nil, fmt.Errorf("bitget sdk: get open orders returned empty page with non-terminal cursor %q", page.Cursor)
		}
		if page.Cursor == cursor {
			return nil, fmt.Errorf("bitget sdk: get open orders repeated cursor %q", page.Cursor)
		}
		if _, duplicate := seenCursors[page.Cursor]; duplicate {
			return nil, fmt.Errorf("bitget sdk: get open orders repeated cursor %q", page.Cursor)
		}
		seenCursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
	}
}

func (c *Client) getOpenOrdersPage(ctx context.Context, category, symbol, limit, cursor string) (*openOrdersPage, error) {
	var out responseEnvelope[openOrdersPage]
	err := c.getPrivate(ctx, "/api/v3/trade/unfilled-orders", map[string]string{
		"category": category,
		"symbol":   symbol,
		"limit":    limit,
		"cursor":   cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get open orders failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOrderHistory(ctx context.Context, category, symbol string) ([]OrderRecord, error) {
	page, err := c.getOrderHistoryPage(ctx, GetOrderHistoryRequest{Category: category, Symbol: symbol})
	if err != nil {
		return nil, err
	}
	return page.List, nil
}

// GetOrderHistoryBounded follows UTA cursor pages until the configured overall
// record cap is reached. The boolean reports that venue rows remain beyond that
// cap, allowing callers to retain a precise single-order fallback.
func (c *Client) GetOrderHistoryBounded(ctx context.Context, req GetOrderHistoryRequest) ([]OrderRecord, bool, error) {
	overallLimit := 100
	if req.Limit != "" {
		parsed, err := strconv.Atoi(req.Limit)
		if err != nil || parsed <= 0 {
			return nil, false, fmt.Errorf("bitget sdk: invalid order history limit %q", req.Limit)
		}
		overallLimit = parsed
	}
	if err := validateOrderHistoryWindow(req.StartTime, req.EndTime); err != nil {
		return nil, false, err
	}
	pageLimit := overallLimit
	if pageLimit > 100 {
		pageLimit = 100
	}
	cursor := req.Cursor
	seenCursors := make(map[string]struct{})
	if cursor != "" {
		seenCursors[cursor] = struct{}{}
	}
	var records []OrderRecord
	pageCount := 0
	for {
		pageCount++
		if pageCount > privatePaginationMaxPages {
			return nil, false, fmt.Errorf("bitget sdk: get order history exceeded %d-page safety limit", privatePaginationMaxPages)
		}
		pageReq := req
		pageReq.Limit = strconv.Itoa(pageLimit)
		pageReq.Cursor = cursor
		page, err := c.getOrderHistoryPage(ctx, pageReq)
		if err != nil {
			return nil, false, err
		}
		pageRecords := page.List
		if len(records)+len(pageRecords) > overallLimit {
			pageRecords = pageRecords[:overallLimit-len(records)]
		}
		records = append(records, pageRecords...)
		if len(records) >= overallLimit {
			return records, page.Cursor != "" || len(page.List) > len(pageRecords), nil
		}
		if page.Cursor == "" {
			return records, false, nil
		}
		if len(page.List) == 0 {
			return nil, false, fmt.Errorf("bitget sdk: get order history returned empty page with non-terminal cursor %q", page.Cursor)
		}
		if page.Cursor == cursor {
			return nil, false, fmt.Errorf("bitget sdk: get order history repeated cursor %q", page.Cursor)
		}
		if _, duplicate := seenCursors[page.Cursor]; duplicate {
			return nil, false, fmt.Errorf("bitget sdk: get order history repeated cursor %q", page.Cursor)
		}
		seenCursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
		pageLimit = overallLimit - len(records)
		if pageLimit > 100 {
			pageLimit = 100
		}
	}
}

func (c *Client) getOrderHistoryPage(ctx context.Context, req GetOrderHistoryRequest) (*orderHistoryPage, error) {
	var out responseEnvelope[orderHistoryPage]
	err := c.getPrivate(ctx, "/api/v3/trade/history-orders", map[string]string{
		"category":  req.Category,
		"symbol":    req.Symbol,
		"startTime": req.StartTime,
		"endTime":   req.EndTime,
		"limit":     req.Limit,
		"cursor":    req.Cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get order history failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func validateOrderHistoryWindow(startTime, endTime string) error {
	if startTime == "" || endTime == "" {
		return nil
	}
	start, err := strconv.ParseInt(startTime, 10, 64)
	if err != nil {
		return fmt.Errorf("bitget sdk: invalid order history startTime %q", startTime)
	}
	end, err := strconv.ParseInt(endTime, 10, 64)
	if err != nil {
		return fmt.Errorf("bitget sdk: invalid order history endTime %q", endTime)
	}
	if start > end {
		return fmt.Errorf("bitget sdk: order history startTime %d is after endTime %d", start, end)
	}
	const maxOrderHistoryWindowMillis = int64(30 * 24 * 60 * 60 * 1000)
	if end-start > maxOrderHistoryWindowMillis {
		return fmt.Errorf("bitget sdk: order history window exceeds 30 days")
	}
	return nil
}

func (c *Client) GetFills(ctx context.Context, req GetFillsRequest) ([]FillRecord, error) {
	page, err := c.getFillsPage(ctx, req)
	if err != nil {
		return nil, err
	}
	return page.List, nil
}

// GetFillsBounded follows cursor pages up to the overall row limit encoded in
// req.Limit. The boolean reports that venue rows remain beyond that hard cap.
func (c *Client) GetFillsBounded(ctx context.Context, req GetFillsRequest) ([]FillRecord, bool, error) {
	overallLimit := 100
	if req.Limit != "" {
		parsed, err := strconv.Atoi(req.Limit)
		if err != nil || parsed <= 0 {
			return nil, false, fmt.Errorf("bitget sdk: invalid fills limit %q", req.Limit)
		}
		overallLimit = parsed
	}
	pageLimit := overallLimit
	if pageLimit > 100 {
		pageLimit = 100
	}
	cursor := req.Cursor
	seenCursors := make(map[string]struct{})
	if cursor != "" {
		seenCursors[cursor] = struct{}{}
	}
	var records []FillRecord
	pageCount := 0
	for {
		pageCount++
		if pageCount > privatePaginationMaxPages {
			return nil, false, fmt.Errorf("bitget sdk: get fills exceeded %d-page safety limit", privatePaginationMaxPages)
		}
		pageReq := req
		pageReq.Limit = strconv.Itoa(pageLimit)
		pageReq.Cursor = cursor
		page, err := c.getFillsPage(ctx, pageReq)
		if err != nil {
			return nil, false, err
		}
		pageRecords := page.List
		if len(records)+len(pageRecords) > overallLimit {
			pageRecords = pageRecords[:overallLimit-len(records)]
		}
		records = append(records, pageRecords...)
		if len(records) >= overallLimit {
			return records, page.Cursor != "" || len(page.List) > len(pageRecords), nil
		}
		if page.Cursor == "" {
			return records, false, nil
		}
		if len(page.List) == 0 {
			return nil, false, fmt.Errorf("bitget sdk: get fills returned empty page with non-terminal cursor %q", page.Cursor)
		}
		if page.Cursor == cursor {
			return nil, false, fmt.Errorf("bitget sdk: get fills repeated cursor %q", page.Cursor)
		}
		if _, duplicate := seenCursors[page.Cursor]; duplicate {
			return nil, false, fmt.Errorf("bitget sdk: get fills repeated cursor %q", page.Cursor)
		}
		seenCursors[page.Cursor] = struct{}{}
		cursor = page.Cursor
		remaining := overallLimit - len(records)
		pageLimit = remaining
		if pageLimit > 100 {
			pageLimit = 100
		}
	}
}

func (c *Client) getFillsPage(ctx context.Context, req GetFillsRequest) (*FillList, error) {
	var out responseEnvelope[FillList]
	err := c.getPrivate(ctx, "/api/v3/trade/fills", map[string]string{
		"category":  req.Category,
		"orderId":   req.OrderID,
		"startTime": req.StartTime,
		"endTime":   req.EndTime,
		"limit":     req.Limit,
		"cursor":    req.Cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get fills failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountAssets(ctx context.Context) (*AccountAssets, error) {
	var out responseEnvelope[AccountAssets]
	err := c.getPrivate(ctx, "/api/v3/account/assets", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account assets failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountInfo(ctx context.Context) (*AccountInfo, error) {
	var out responseEnvelope[AccountInfo]
	err := c.getPrivate(ctx, "/api/v3/account/info", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account info failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountSettings(ctx context.Context) (*AccountSettings, error) {
	var out responseEnvelope[AccountSettings]
	err := c.getPrivate(ctx, "/api/v3/account/settings", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account settings failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetFundingAssets(ctx context.Context, coin string) ([]FundingAsset, error) {
	var out responseEnvelope[[]FundingAsset]
	err := c.getPrivate(ctx, "/api/v3/account/funding-assets", map[string]string{"coin": coin}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get funding assets failed: %s %s", out.Code, out.Msg)
	}
	return out.Data, nil
}

func (c *Client) GetFinancialRecords(ctx context.Context, req FinancialRecordsRequest) (*FinancialRecords, error) {
	var out responseEnvelope[FinancialRecords]
	err := c.getPrivate(ctx, "/api/v3/account/financial-records", map[string]string{
		"category":  req.Category,
		"coin":      req.Coin,
		"type":      req.Type,
		"startTime": req.StartTime,
		"endTime":   req.EndTime,
		"limit":     req.Limit,
		"cursor":    req.Cursor,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get financial records failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetAccountFeeRate(ctx context.Context, category, symbol string) (*AccountFeeRate, error) {
	var out responseEnvelope[AccountFeeRate]
	err := c.getPrivate(ctx, "/api/v3/account/fee-rate", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get account fee rate failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetSwitchStatus(ctx context.Context) (*SwitchStatus, error) {
	var out responseEnvelope[SwitchStatus]
	err := c.getPrivate(ctx, "/api/v3/account/switch-status", nil, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get switch status failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetMaxTransferable(ctx context.Context, coin string) (*MaxTransferable, error) {
	var out responseEnvelope[MaxTransferable]
	err := c.getPrivate(ctx, "/api/v3/account/max-transferable", map[string]string{"coin": coin}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get max transferable failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetOpenInterestLimit(ctx context.Context, category, symbol string) (*OpenInterestLimit, error) {
	var out responseEnvelope[OpenInterestLimit]
	err := c.getPrivate(ctx, "/api/v3/account/open-interest-limit", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get open interest limit failed: %s %s", out.Code, out.Msg)
	}
	return &out.Data, nil
}

func (c *Client) GetCurrentPositions(ctx context.Context, category, symbol string) ([]PositionRecord, error) {
	var out responseEnvelope[PositionList]
	err := c.getPrivate(ctx, "/api/v3/position/current-position", map[string]string{
		"category": category,
		"symbol":   symbol,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.Code != "00000" {
		return nil, fmt.Errorf("bitget sdk: get positions failed: %s %s", out.Code, out.Msg)
	}
	return out.Data.List, nil
}

func (c *Client) SetHoldMode(ctx context.Context, holdMode string) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/account/set-hold-mode", map[string]string{"holdMode": holdMode}, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: set hold mode failed: %s %s", out.Code, out.Msg)
	}
	return nil
}

func (c *Client) SetLeverage(ctx context.Context, req *SetLeverageRequest) error {
	var out responseEnvelope[any]
	err := c.postPrivate(ctx, "/api/v3/account/set-leverage", req, &out)
	if err != nil {
		return err
	}
	if out.Code != "00000" {
		return fmt.Errorf("bitget sdk: set leverage failed: %s %s", out.Code, out.Msg)
	}
	return nil
}
