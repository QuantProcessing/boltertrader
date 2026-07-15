package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func (c *Client) UserOpenOrders(ctx context.Context, user string) ([]Order, error) {
	req := map[string]string{
		"type": "frontendOpenOrders",
		"user": user,
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res []Order
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *Client) OrderStatus(ctx context.Context, user string, oid int64) (*OrderStatusInfo, error) {
	return c.orderStatus(ctx, user, oid)
}

// OrderStatusByCloid queries exact order state using Hyperliquid's 128-bit
// client order id. The existing numeric OrderStatus API remains unchanged.
func (c *Client) OrderStatusByCloid(ctx context.Context, user, cloid string) (*OrderStatusInfo, error) {
	return c.orderStatus(ctx, user, cloid)
}

func (c *Client) orderStatus(ctx context.Context, user string, oid any) (*OrderStatusInfo, error) {
	req := map[string]any{
		"type": "orderStatus",
		"user": user,
		"oid":  oid,
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res orderStatusWireResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if res.Status == "unknownOid" {
		return nil, fmt.Errorf("%w: %v", hyperliquid.ErrOrderNotFound, oid)
	}
	if res.Status != "order" || res.Order == nil {
		return nil, fmt.Errorf("hyperliquid spot: unexpected orderStatus response status %q", res.Status)
	}
	if err := validateOrderStatusIdentity(oid, res.Order.Order); err != nil {
		return nil, err
	}
	filled, err := filledSize(res.Order.Order.OrigSz, res.Order.Order.Sz)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid spot: decode orderStatus fill: %w", err)
	}
	return &OrderStatusInfo{
		Coin: res.Order.Order.Coin, Side: res.Order.Order.Side, LimitPx: res.Order.Order.LimitPx,
		Sz: res.Order.Order.Sz, Oid: res.Order.Order.Oid, Cliod: stringValue(res.Order.Order.Cliod),
		Timestamp: res.Order.Order.Timestamp, StatusTimestamp: res.Order.StatusTimestamp,
		OrigSz: res.Order.Order.OrigSz, Status: res.Order.Status, FilledSz: filled,
		ReduceOnly: boolValue(res.Order.Order.ReduceOnly), HasReduceOnly: res.Order.Order.ReduceOnly != nil,
		OrderType: res.Order.Order.OrderType, Tif: res.Order.Order.Tif,
		IsTrigger: res.Order.Order.IsTrigger, TriggerPx: res.Order.Order.TriggerPx,
	}, nil
}

type orderStatusWireResponse struct {
	Status string                 `json:"status"`
	Order  *orderStatusWireResult `json:"order"`
}

type orderStatusWireResult struct {
	Order           orderStatusWireOrder `json:"order"`
	Status          string               `json:"status"`
	StatusTimestamp int64                `json:"statusTimestamp"`
}

type orderStatusWireOrder struct {
	Coin       string  `json:"coin"`
	Side       string  `json:"side"`
	LimitPx    string  `json:"limitPx"`
	Sz         string  `json:"sz"`
	Oid        int64   `json:"oid"`
	Cliod      *string `json:"cloid"`
	Timestamp  int64   `json:"timestamp"`
	OrigSz     string  `json:"origSz"`
	ReduceOnly *bool   `json:"reduceOnly"`
	OrderType  string  `json:"orderType"`
	Tif        string  `json:"tif"`
	IsTrigger  bool    `json:"isTrigger"`
	TriggerPx  string  `json:"triggerPx"`
}

func validateOrderStatusIdentity(requested any, order orderStatusWireOrder) error {
	switch value := requested.(type) {
	case int64:
		if order.Oid != value {
			return fmt.Errorf("hyperliquid spot: orderStatus identity mismatch: requested oid %d, got %d", value, order.Oid)
		}
	case string:
		if order.Cliod == nil || !strings.EqualFold(*order.Cliod, value) {
			return fmt.Errorf("hyperliquid spot: orderStatus identity mismatch: requested cloid %q, got %q", value, stringValue(order.Cliod))
		}
	default:
		return fmt.Errorf("hyperliquid spot: unsupported orderStatus identity %T", requested)
	}
	return nil
}

func filledSize(origSz, remainingSz string) (string, error) {
	original, err := decimal.NewFromString(origSz)
	if err != nil {
		return "", err
	}
	remaining, err := decimal.NewFromString(remainingSz)
	if err != nil {
		return "", err
	}
	filled := original.Sub(remaining)
	if filled.IsNegative() {
		return "", fmt.Errorf("remaining size %s exceeds original size %s", remaining, original)
	}
	return filled.String(), nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func boolValue(value *bool) bool {
	return value != nil && *value
}

func (c *Client) placeOrder(ctx context.Context, req PlaceOrderRequest) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildPlaceOrderAction(req)
	if err != nil {
		return nil, err
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return nil, err
	}
	return c.PostAction(ctx, action, sig, nonce)
}

func (c *Client) PlaceOrder(ctx context.Context, req PlaceOrderRequest) (*OrderStatus, error) {
	statuses, err := c.PlaceOrders(ctx, []PlaceOrderRequest{req})
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, fmt.Errorf("place order failed: venue returned no order status")
	}
	return &statuses[0], nil
}

func (c *Client) placeOrders(ctx context.Context, reqs []PlaceOrderRequest) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildPlaceOrderAction(reqs...)
	if err != nil {
		return nil, err
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return nil, err
	}
	return c.PostAction(ctx, action, sig, nonce)
}

func (c *Client) PlaceOrders(ctx context.Context, reqs []PlaceOrderRequest) ([]OrderStatus, error) {
	data, err := c.placeOrders(ctx, reqs)
	if err != nil {
		return nil, err
	}
	res := new(hyperliquid.APIResponse[PlaceOrderResponse])
	if err := json.Unmarshal(data, res); err != nil {
		return nil, err
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("place orders failed: %s", res.FailureMessage())
	}
	if res.Response == nil {
		return nil, fmt.Errorf("place orders failed: missing response")
	}
	statuses := res.Response.Data.Statuses
	if len(statuses) == 0 {
		return nil, fmt.Errorf("place orders failed: venue returned no order status")
	}
	if len(statuses) != len(reqs) {
		return nil, fmt.Errorf("place orders failed: venue returned %d statuses for %d requests", len(statuses), len(reqs))
	}
	for i, status := range statuses {
		if err := validateCommandOrderStatus(status, reqs[i], "place order"); err != nil {
			return nil, err
		}
	}
	return statuses, nil
}

func (c *Client) modifyOrder(ctx context.Context, req ModifyOrderRequest) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildModifyOrderAction(req)
	if err != nil {
		return nil, err
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return nil, err
	}
	return c.PostAction(ctx, action, sig, nonce)
}

func (c *Client) ModifyOrder(ctx context.Context, req ModifyOrderRequest) (*OrderStatus, error) {
	data, err := c.modifyOrder(ctx, req)
	if err != nil {
		return nil, err
	}
	res := new(hyperliquid.APIResponse[ModifyOrderResponse])
	if err := json.Unmarshal(data, res); err != nil {
		return nil, err
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("modify order failed: %s", res.FailureMessage())
	}
	if res.Response == nil {
		return nil, fmt.Errorf("modify order failed: missing response")
	}
	if len(res.Response.Data.Statuses) == 0 {
		return nil, fmt.Errorf("modify order failed: venue returned no order status")
	}
	if len(res.Response.Data.Statuses) != 1 {
		return nil, fmt.Errorf("modify order failed: venue returned %d statuses for 1 request", len(res.Response.Data.Statuses))
	}
	status := res.Response.Data.Statuses[0]
	if err := validateCommandOrderStatus(status, req.Order, "modify order"); err != nil {
		return nil, err
	}
	return &status, nil
}

func (c *Client) cancelOrder(ctx context.Context, req CancelOrderRequest) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildCancelOrderAction(req)
	if err != nil {
		return nil, err
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return nil, err
	}
	return c.PostAction(ctx, action, sig, nonce)
}

func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*string, error) {
	statuses, err := c.CancelOrders(ctx, []CancelOrderRequest{req})
	if err != nil {
		return nil, err
	}
	if len(statuses) == 0 {
		return nil, fmt.Errorf("cancel order failed: venue returned no order status")
	}
	return &statuses[0], nil
}

func (c *Client) cancelOrders(ctx context.Context, reqs []CancelOrderRequest) ([]byte, error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := buildCancelOrdersAction(reqs)
	if err != nil {
		return nil, err
	}
	nonce := c.GetNextNonce()
	sig, err := c.SignL1Action(action, nonce)
	if err != nil {
		return nil, err
	}
	return c.PostAction(ctx, action, sig, nonce)
}

func (c *Client) CancelOrders(ctx context.Context, reqs []CancelOrderRequest) ([]string, error) {
	data, err := c.cancelOrders(ctx, reqs)
	if err != nil {
		return nil, err
	}
	res := new(hyperliquid.APIResponse[CancelOrderResponse])
	if err := json.Unmarshal(data, res); err != nil {
		return nil, err
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("cancel orders failed: %s", res.FailureMessage())
	}
	if res.Response == nil {
		return nil, fmt.Errorf("cancel orders failed: missing response")
	}
	if len(res.Response.Data.Statuses) == 0 {
		return nil, fmt.Errorf("cancel orders failed: venue returned no order status")
	}
	if len(res.Response.Data.Statuses) != len(reqs) {
		return nil, fmt.Errorf("cancel orders failed: venue returned %d statuses for %d requests", len(res.Response.Data.Statuses), len(reqs))
	}
	if err := res.Response.Data.Statuses.FirstError(); err != nil {
		return nil, err
	}
	statuses := make([]string, 0, len(res.Response.Data.Statuses))
	for _, raw := range res.Response.Data.Statuses {
		var status string
		_ = json.Unmarshal(raw, &status)
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func validateCommandOrderStatus(status OrderStatus, req PlaceOrderRequest, operation string) error {
	shapeCount := 0
	if status.Resting != nil {
		shapeCount++
	}
	if status.Filled != nil {
		shapeCount++
	}
	if status.Error != nil {
		shapeCount++
	}
	if shapeCount != 1 {
		return fmt.Errorf("%s failed: malformed venue status contains %d result shapes", operation, shapeCount)
	}
	if status.Error != nil {
		reason := strings.TrimSpace(*status.Error)
		if reason == "" {
			return fmt.Errorf("%s failed: malformed empty venue rejection", operation)
		}
		return &hyperliquid.OrderRejectedError{Reason: reason}
	}
	if status.Resting != nil && status.Resting.Oid <= 0 {
		return fmt.Errorf("%s failed: malformed resting venue order id %d", operation, status.Resting.Oid)
	}
	if status.Resting != nil && req.ClientOrderID != nil && status.Resting.ClientID != nil && !strings.EqualFold(*status.Resting.ClientID, *req.ClientOrderID) {
		return fmt.Errorf("%s failed: client order id mismatch: requested %q, got %q", operation, *req.ClientOrderID, *status.Resting.ClientID)
	}
	if status.Filled != nil {
		if status.Filled.Oid <= 0 {
			return fmt.Errorf("%s failed: malformed filled venue order id %d", operation, status.Filled.Oid)
		}
		totalSize, err := decimal.NewFromString(status.Filled.TotalSz)
		if err != nil || !totalSize.IsPositive() {
			return fmt.Errorf("%s failed: malformed filled total size %q", operation, status.Filled.TotalSz)
		}
		averagePrice, err := decimal.NewFromString(status.Filled.AvgPx)
		if err != nil || !averagePrice.IsPositive() {
			return fmt.Errorf("%s failed: malformed filled average price %q", operation, status.Filled.AvgPx)
		}
	}
	return nil
}
