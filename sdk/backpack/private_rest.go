package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const defaultRecvWindow = int64(5000)

func (c *Client) signedDo(ctx context.Context, method, path, instruction string, query map[string]string, body any, out any) error {
	query = filterEmptyParams(query)
	timestamp := time.Now().UnixMilli()
	headers, err := buildSignedHeaders(c.apiKey, c.privateKey, instruction, query, timestamp, defaultRecvWindow)
	if err != nil {
		return err
	}

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	q := u.Query()
	for key, value := range query {
		if value != "" {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()

	var bodyReader *bytes.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(payload)
	} else {
		bodyReader = bytes.NewReader(nil)
	}

	req, err := http.NewRequestWithContext(ctx, method, u.String(), bodyReader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("backpack sdk: %s %s returned %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) signedDoBatch(ctx context.Context, method, path, instruction string, params []map[string]string, body any, out any) error {
	timestamp := time.Now().UnixMilli()
	headers, err := buildBatchSignedHeaders(c.apiKey, c.privateKey, instruction, params, timestamp, defaultRecvWindow)
	if err != nil {
		return err
	}

	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("backpack sdk: %s %s returned %s", method, path, resp.Status)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) GetAccount(ctx context.Context) (*AccountSettings, error) {
	var out AccountSettings
	err := c.signedDo(ctx, http.MethodGet, "/api/v1/account", "accountQuery", nil, nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) GetBalances(ctx context.Context) (map[string]CapitalBalance, error) {
	var out map[string]CapitalBalance
	err := c.signedDo(ctx, http.MethodGet, "/api/v1/capital", "balanceQuery", nil, nil, &out)
	return out, err
}

func (c *Client) GetOpenOrders(ctx context.Context, marketType, symbol string) ([]Order, error) {
	query := map[string]string{
		"marketType": marketType,
		"symbol":     symbol,
	}
	var out []Order
	err := c.signedDo(ctx, http.MethodGet, "/api/v1/orders", "orderQueryAll", query, nil, &out)
	return out, err
}

func (c *Client) GetOpenPositions(ctx context.Context, symbol string) ([]Position, error) {
	query := map[string]string{
		"symbol": symbol,
	}
	var out []Position
	err := c.signedDo(ctx, http.MethodGet, "/api/v1/position", "positionQuery", query, nil, &out)
	return out, err
}

func (c *Client) GetFillHistory(ctx context.Context, req FillHistoryRequest) ([]Fill, error) {
	query := map[string]string{
		"marketType":    req.MarketType,
		"orderId":       req.OrderID,
		"symbol":        req.Symbol,
		"fillType":      req.FillType,
		"sortDirection": req.SortDirection,
	}
	if req.Limit > 0 {
		query["limit"] = strconv.Itoa(req.Limit)
	}
	if req.Offset > 0 {
		query["offset"] = strconv.Itoa(req.Offset)
	}
	var out []Fill
	err := c.signedDo(ctx, http.MethodGet, "/wapi/v1/history/fills", "fillHistoryQueryAll", query, nil, &out)
	return out, err
}

func (c *Client) PlaceOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	var out Order
	err := c.signedDo(ctx, http.MethodPost, "/api/v1/order", "orderExecute", createOrderSignParams(req), req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) PlaceOrders(ctx context.Context, reqs []CreateOrderRequest) ([]Order, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("backpack sdk: PlaceOrders requires at least one order")
	}
	signParams := make([]map[string]string, 0, len(reqs))
	for _, req := range reqs {
		signParams = append(signParams, createOrderSignParams(req))
	}
	var out []Order
	err := c.signedDoBatch(ctx, http.MethodPost, "/api/v1/orders", "orderExecute", signParams, reqs, &out)
	return out, err
}

// ExecuteOrder is a compatibility wrapper for the preferred PlaceOrder name.
func (c *Client) ExecuteOrder(ctx context.Context, req CreateOrderRequest) (*Order, error) {
	return c.PlaceOrder(ctx, req)
}

func createOrderSignParams(req CreateOrderRequest) map[string]string {
	signParams := map[string]string{
		"symbol":    req.Symbol,
		"side":      req.Side,
		"orderType": req.OrderType,
		"quantity":  req.Quantity,
	}
	if req.Price != "" {
		signParams["price"] = req.Price
	}
	if req.TimeInForce != "" {
		signParams["timeInForce"] = req.TimeInForce
	}
	if req.ReduceOnly {
		signParams["reduceOnly"] = "true"
	}
	if req.ClientID != 0 {
		signParams["clientId"] = strconv.FormatUint(uint64(req.ClientID), 10)
	}
	return signParams
}

func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*Order, error) {
	signParams := map[string]string{
		"orderId": req.OrderID,
		"symbol":  req.Symbol,
	}
	var out Order
	err := c.signedDo(ctx, http.MethodDelete, "/api/v1/order", "orderCancel", signParams, req, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelOpenOrders(ctx context.Context, symbol, marketType string) error {
	signParams := map[string]string{
		"symbol":     symbol,
		"marketType": marketType,
	}
	return c.signedDo(ctx, http.MethodDelete, "/api/v1/orders", "orderCancelAll", signParams, signParams, nil)
}
