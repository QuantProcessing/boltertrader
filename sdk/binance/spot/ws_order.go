package spot

import (
	"encoding/json"
	"fmt"
)

// WebSocket API (v3) for Orders

func (c *WsAPIClient) PlaceOrderWS(apiKey, secretKey string, p PlaceOrderParams, id string) (*OrderResponse, error) {
	ts := Timestamp()
	// Build query string for signature
	params := map[string]interface{}{
		"symbol":    p.Symbol,
		"side":      p.Side,
		"type":      p.Type,
		"timestamp": ts,
		"apiKey":    apiKey,
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = p.TimeInForce
	}
	if p.Quantity != "" {
		params["quantity"] = p.Quantity
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.NewClientOrderID != "" {
		params["newClientOrderId"] = p.NewClientOrderID
	}
	if p.StopPrice != "" {
		params["stopPrice"] = p.StopPrice
	}
	if p.IcebergQty != "" {
		params["icebergQty"] = p.IcebergQty
	}
	if p.NewOrderRespType != "" {
		params["newOrderRespType"] = p.NewOrderRespType
	}

	// Sign
	q := BuildQueryString(params)
	sig := GenerateSignature(secretKey, q)

	// Add signature to params
	params["signature"] = sig

	req := map[string]interface{}{
		"id":     id,
		"method": "order.place",
		"params": params,
	}

	respData, err := c.SendRequest(id, req)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result OrderResponse `json:"result"`
		Error  *struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", resp.Error.Code, resp.Error.Msg)
	}
	return &resp.Result, nil
}

// Modify Order WS

func (c *WsAPIClient) ModifyOrderWS(apiKey, secretKey string, p CancelReplaceOrderParams, id string) (*CancelReplaceOrderResponse, error) {
	ts := Timestamp()
	params := map[string]interface{}{
		"symbol":            p.Symbol,
		"side":              p.Side,
		"type":              p.Type,
		"cancelReplaceMode": p.CancelReplaceMode,
		"quantity":          p.Quantity,
		"timestamp":         ts,
		"apiKey":            apiKey,
	}
	if p.TimeInForce != "" {
		params["timeInForce"] = p.TimeInForce
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.CancelOrderID != 0 {
		params["cancelOrderId"] = p.CancelOrderID
	}
	if p.CancelOrigClientOrderID != "" {
		params["cancelOrigClientOrderId"] = p.CancelOrigClientOrderID
	}
	if p.NewClientOrderID != "" {
		params["newClientOrderId"] = p.NewClientOrderID
	}
	if p.StopPrice != "" {
		params["stopPrice"] = p.StopPrice
	}
	if p.IcebergQty != "" {
		params["icebergQty"] = p.IcebergQty
	}
	if p.NewOrderRespType != "" {
		params["newOrderRespType"] = p.NewOrderRespType
	}

	q := BuildQueryString(params)
	sig := GenerateSignature(secretKey, q)

	// Add signature to params
	params["signature"] = sig

	req := map[string]interface{}{
		"id":     id,
		"method": "order.cancelReplace",
		"params": params,
	}

	respData, err := c.SendRequest(id, req)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result CancelReplaceOrderResponse `json:"result"`
		Error  *struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", resp.Error.Code, resp.Error.Msg)
	}
	return &resp.Result, nil
}

// Cancel Order WS

func (c *WsAPIClient) CancelOrderWS(apiKey, secretKey string, symbol string, orderID int64, origClientOrderID string, id string) (*OrderResponse, error) {
	ts := Timestamp()
	params := map[string]interface{}{
		"symbol":    symbol,
		"timestamp": ts,
		"apiKey":    apiKey,
	}
	if orderID != 0 {
		params["orderId"] = orderID
	}
	if origClientOrderID != "" {
		params["origClientOrderId"] = origClientOrderID
	}

	q := BuildQueryString(params)
	sig := GenerateSignature(secretKey, q)

	// Add signature to params
	params["signature"] = sig

	req := map[string]interface{}{
		"id":     id,
		"method": "order.cancel",
		"params": params,
	}

	respData, err := c.SendRequest(id, req)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Result OrderResponse `json:"result"`
		Error  *struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("API error: code=%d, msg=%s", resp.Error.Code, resp.Error.Msg)
	}
	return &resp.Result, nil
}
