package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

// UserOpenOrders
func (c *Client) UserOpenOrders(ctx context.Context, user string) ([]Order, error) {
	return c.UserOpenOrdersForDex(ctx, user, "")
}

// UserOpenOrdersForDex uses frontendOpenOrders because openOrders omits
// original size and immutable order semantics required by the execution
// contract. HIP-3 orders are scoped by dex name.
func (c *Client) UserOpenOrdersForDex(ctx context.Context, user, dex string) ([]Order, error) {
	req := map[string]string{"type": "frontendOpenOrders", "user": user}
	if dex != "" {
		req["dex"] = dex
	}
	data, err := c.Post(ctx, "/info", req)
	if err != nil {
		return nil, err
	}
	var res []Order
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	if dex != "" {
		for i := range res {
			if res[i].Coin != "" && !strings.Contains(res[i].Coin, ":") {
				res[i].Coin = dex + ":" + res[i].Coin
			}
		}
	}
	return res, nil
}

// OrderStatus
func (c *Client) OrderStatus(ctx context.Context, user string, oid int64) (*OrderStatusInfo, error) {
	return c.orderStatus(ctx, user, oid)
}

// OrderStatusByCloid queries exact order state by Hyperliquid cloid while
// preserving the existing numeric OrderStatus API.
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
		return nil, fmt.Errorf("hyperliquid perp: unexpected orderStatus response status %q", res.Status)
	}
	if err := validateOrderStatusIdentity(oid, res.Order.Order); err != nil {
		return nil, err
	}
	filled, err := filledSize(res.Order.Order.OrigSz, res.Order.Order.Sz)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid perp: decode orderStatus fill: %w", err)
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
			return fmt.Errorf("hyperliquid perp: orderStatus identity mismatch: requested oid %d, got %d", value, order.Oid)
		}
	case string:
		if order.Cliod == nil || !strings.EqualFold(*order.Cliod, value) {
			return fmt.Errorf("hyperliquid perp: orderStatus identity mismatch: requested cloid %q, got %q", value, stringValue(order.Cliod))
		}
	default:
		return fmt.Errorf("hyperliquid perp: unsupported orderStatus identity %T", requested)
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

// Transaction Helpers

func (c *Client) placeOrder(ctx context.Context, req PlaceOrderRequest) (data []byte, err error) {
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

func (c *Client) placeOrders(ctx context.Context, reqs []PlaceOrderRequest) (data []byte, err error) {
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
	err = json.Unmarshal(data, res)
	if err != nil {
		return nil, err
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("place orders failed: %s", res.FailureMessage())
	}
	if res.Response == nil {
		return nil, fmt.Errorf("place orders failed: missing response")
	}
	for _, status := range res.Response.Data.Statuses {
		if status.Error != nil {
			return nil, &hyperliquid.OrderRejectedError{Reason: *status.Error}
		}
	}
	return res.Response.Data.Statuses, nil
}

// Modify

func (c *Client) newModifyOrdersAction(orders []ModifyOrderRequest) (hyperliquid.BatchModifyAction, error) {
	modifies := make([]hyperliquid.ModifyOrderAction, len(orders))
	for i, req := range orders {
		modify, err := buildModifyOrderAction(req)
		if err != nil {
			return hyperliquid.BatchModifyAction{}, fmt.Errorf("failed to create modify request %d: %w", i, err)
		}
		modify.Type = ""
		modifies[i] = modify
	}

	return hyperliquid.BatchModifyAction{
		Type:     "batchModify",
		Modifies: modifies,
	}, nil
}

func (c *Client) modifyOrders(ctx context.Context, req []ModifyOrderRequest) (data []byte, err error) {
	if c.PrivateKey == nil {
		return nil, hyperliquid.ErrCredentialsRequired
	}
	action, err := c.newModifyOrdersAction(req)
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
	data, err := c.modifyOrders(ctx, []ModifyOrderRequest{req})
	if err != nil {
		return nil, err
	}
	res := new(hyperliquid.APIResponse[ModifyOrderResponse])
	err = json.Unmarshal(data, res)
	if err != nil {
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
	status := res.Response.Data.Statuses[0]
	if status.Error != nil {
		return nil, fmt.Errorf("modify order failed: %s", *status.Error)
	}
	return &status, nil
}

// Cancel Order

func (c *Client) cancelOrder(ctx context.Context, req CancelOrderRequest) (data []byte, err error) {
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

func (c *Client) cancelOrders(ctx context.Context, reqs []CancelOrderRequest) (data []byte, err error) {
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
	err = json.Unmarshal(data, res)
	if err != nil {
		return nil, err
	}
	if res.Status != "ok" {
		return nil, fmt.Errorf("cancel orders failed: %s", res.FailureMessage())
	}
	if res.Response == nil {
		return nil, fmt.Errorf("cancel orders failed: missing response")
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
