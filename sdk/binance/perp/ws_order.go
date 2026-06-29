package perp

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
	if p.PositionSide != "" {
		params["positionSide"] = p.PositionSide
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
	if p.StopPrice != "" {
		params["stopPrice"] = p.StopPrice
	}
	if p.NewClientOrderID != "" {
		params["newClientOrderId"] = p.NewClientOrderID
	}
	if p.ReduceOnly {
		params["reduceOnly"] = "true"
	}
	if p.ClosePosition {
		params["closePosition"] = "true"
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

func (c *WsAPIClient) ModifyOrderWS(apiKey, secretKey string, p ModifyOrderParams, id string) (*OrderResponse, error) {
	ts := Timestamp()
	params := map[string]interface{}{
		"symbol":    p.Symbol,
		"side":      p.Side,
		"timestamp": ts,
		"apiKey":    apiKey,
	}
	if p.OrderID != 0 {
		params["orderId"] = p.OrderID
	}
	if p.OrigClientOrderID != "" {
		params["origClientOrderId"] = p.OrigClientOrderID
	}
	if p.Quantity != "" {
		params["quantity"] = p.Quantity
	}
	if p.Price != "" {
		params["price"] = p.Price
	}
	if p.PriceMatch != "" {
		params["priceMatch"] = p.PriceMatch
	}

	q := BuildQueryString(params)
	sig := GenerateSignature(secretKey, q)

	// Add signature to params
	params["signature"] = sig

	req := map[string]interface{}{
		"id":     id,
		"method": "order.modify",
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

// Cancel Order WS

func (c *WsAPIClient) CancelOrderWS(apiKey, secretKey string, p CancelOrderParams, id string) (*OrderResponse, error) {
	ts := Timestamp()
	params := map[string]interface{}{
		"symbol":    p.Symbol,
		"timestamp": ts,
		"apiKey":    apiKey,
	}
	if p.OrderID != "" {
		params["orderId"] = p.OrderID
	}
	if p.OrigClientOrderID != "" {
		params["origClientOrderId"] = p.OrigClientOrderID
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

// Cancel All Orders WS

func (c *WsAPIClient) CancelAllOrdersWS(apiKey, secretKey string, p CancelAllOrdersParams, id string) error {
	ts := Timestamp()
	params := map[string]interface{}{
		"symbol":    p.Symbol,
		"timestamp": ts,
		"apiKey":    apiKey,
	}

	q := BuildQueryString(params)
	sig := GenerateSignature(secretKey, q)

	// Add signature to params
	params["signature"] = sig

	req := map[string]interface{}{
		"id":     id,
		"method": "order.cancelAll",
		"params": params,
	}

	respData, err := c.SendRequest(id, req)
	if err != nil {
		return err
	}

	var resp struct {
		Error *struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("API error: code=%d, msg=%s", resp.Error.Code, resp.Error.Msg)
	}
	return nil
}
