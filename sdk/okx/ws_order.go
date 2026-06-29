package okx

import (
	"encoding/json"
	"fmt"
	"time"
)

// WsOrderRequest represents the structure for placing/cancelling orders via WS.
type WsOrderOp struct {
	Id   string        `json:"id"` // Request ID
	Op   string        `json:"op"` // "order", "batch-orders", "cancel-order", "batch-cancel-orders"
	Args []interface{} `json:"args"`
}

type wsOrderRequest struct {
	InstIdCode int64   `json:"instIdCode"`
	TdMode     string  `json:"tdMode"`
	ClOrdId    *string `json:"clOrdId,omitempty"`
	Side       string  `json:"side"`
	PosSide    *string `json:"posSide,omitempty"`
	OrdType    string  `json:"ordType"`
	Sz         string  `json:"sz"`
	Px         *string `json:"px,omitempty"`
	Ccy        *string `json:"ccy,omitempty"`
	TgtCcy     *string `json:"tgtCcy,omitempty"`
	Tag        *string `json:"tag,omitempty"`
	ReduceOnly *bool   `json:"reduceOnly,omitempty"`
}

type wsModifyOrderRequest struct {
	InstIdCode int64   `json:"instIdCode"`
	OrdId      *string `json:"ordId,omitempty"`
	ClOrdId    *string `json:"clOrdId,omitempty"`
	NewSz      *string `json:"newSz,omitempty"`
	NewPx      *string `json:"newPx,omitempty"`
	CxlOnFail  *bool   `json:"cxlOnFail,omitempty"`
	ReqId      *string `json:"reqId,omitempty"`
}

type wsCancelOrderRequest struct {
	InstIdCode int64   `json:"instIdCode"`
	OrdId      *string `json:"ordId,omitempty"`
	ClOrdId    *string `json:"clOrdId,omitempty"`
}

// PlaceOrderWS places an order via WebSocket.
func (c *WSClient) PlaceOrderWS(req *OrderRequest) (*OrderId, error) {
	if req == nil {
		return nil, fmt.Errorf("order request is required")
	}
	if req.InstIdCode == nil {
		return nil, fmt.Errorf("instIdCode is required for OKX WS order requests")
	}

	// User should handle tracking via clOrdId.

	// Use int64 for internal tracking
	idInt := time.Now().UnixNano()
	idStr := fmt.Sprintf("%d", idInt)

	wsReq := wsOrderRequest{
		InstIdCode: *req.InstIdCode,
		TdMode:     req.TdMode,
		ClOrdId:    req.ClOrdId,
		Side:       req.Side,
		PosSide:    req.PosSide,
		OrdType:    req.OrdType,
		Sz:         req.Sz,
		Px:         req.Px,
		Ccy:        req.Ccy,
		TgtCcy:     req.TgtCcy,
		Tag:        req.Tag,
		ReduceOnly: req.ReduceOnly,
	}

	op := WsOrderOp{
		Id:   idStr,
		Op:   "order",
		Args: []interface{}{wsReq},
	}

	// Create channel for response
	successCh, errorCh := c.AddPendingRequest(idInt)
	defer c.RemovePendingRequest(idInt)

	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(op)
	c.WriteMu.Unlock()
	if err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case msg := <-successCh:
		// Parse result
		var resp struct {
			Code string    `json:"code"`
			Msg  string    `json:"msg"`
			Data []OrderId `json:"data"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse WS response: %w", err)
		}
		// Double check code (though SuccessCh implies Success in handleMessage logic?
		// handleMessage checks code!=0. But let's be safe)
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx ws error: code=%s msg=%s data=%v", resp.Code, resp.Msg, resp.Data)
		}
		if len(resp.Data) > 0 {
			if err := validateWSActionResult("order", resp.Data[0]); err != nil {
				return &resp.Data[0], err
			}
			return &resp.Data[0], nil
		}
		return nil, nil // No data but success?

	case msg := <-errorCh:
		var resp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &resp)
		return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, string(msg))

	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for order response")
	}
}

// CancelOrderWS cancels an order via WebSocket.
func (c *WSClient) CancelOrderWS(instIdCode int64, ordId, clOrdId *string) (*OrderId, error) {
	req := wsCancelOrderRequest{
		InstIdCode: instIdCode,
		OrdId:      ordId,
		ClOrdId:    clOrdId,
	}

	// Use int64 for internal tracking
	idInt := time.Now().UnixNano()
	idStr := fmt.Sprintf("%d", idInt)

	op := WsOrderOp{
		Id:   idStr,
		Op:   "cancel-order",
		Args: []interface{}{req},
	}

	// Create channel for response
	successCh, errorCh := c.AddPendingRequest(idInt)
	defer c.RemovePendingRequest(idInt)

	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(op)
	c.WriteMu.Unlock()
	if err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case msg := <-successCh:
		// Parse result
		var resp struct {
			Code string    `json:"code"`
			Msg  string    `json:"msg"`
			Data []OrderId `json:"data"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse WS response: %w", err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)
		}
		if len(resp.Data) > 0 {
			if err := validateWSActionResult("cancel", resp.Data[0]); err != nil {
				return &resp.Data[0], err
			}
			return &resp.Data[0], nil
		}
		return nil, nil // Success

	case msg := <-errorCh:
		var resp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &resp)
		return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)

	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for cancel response")
	}
}

// ModifyOrderWS amends an order via WebSocket.
func (c *WSClient) ModifyOrderWS(req *ModifyOrderRequest) (*OrderId, error) {
	if req == nil {
		return nil, fmt.Errorf("modify order request is required")
	}
	if req.InstIdCode == nil {
		return nil, fmt.Errorf("instIdCode is required for OKX WS amend requests")
	}

	// Use int64 for internal tracking
	idInt := time.Now().UnixNano()
	idStr := fmt.Sprintf("%d", idInt)

	wsReq := wsModifyOrderRequest{
		InstIdCode: *req.InstIdCode,
		OrdId:      req.OrdId,
		ClOrdId:    req.ClOrdId,
		NewSz:      req.NewSz,
		NewPx:      req.NewPx,
		CxlOnFail:  req.CxlOnFail,
		ReqId:      req.ReqId,
	}

	op := WsOrderOp{
		Id:   idStr,
		Op:   "amend-order",
		Args: []interface{}{wsReq},
	}

	// Create channel for response
	successCh, errorCh := c.AddPendingRequest(idInt)
	defer c.RemovePendingRequest(idInt)

	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(op)
	c.WriteMu.Unlock()
	if err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case msg := <-successCh:
		// Parse result
		var resp struct {
			Code string    `json:"code"`
			Msg  string    `json:"msg"`
			Data []OrderId `json:"data"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse WS response: %w", err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)
		}
		if len(resp.Data) > 0 {
			if err := validateWSActionResult("amend", resp.Data[0]); err != nil {
				return &resp.Data[0], err
			}
			return &resp.Data[0], nil
		}
		return nil, nil // Success but no data?

	case msg := <-errorCh:
		var resp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &resp)
		return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)

	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for modify response")
	}
}

// CancelOrdersWS cancels a batch of orders via WebSocket.
func (c *WSClient) CancelOrdersWS(reqs []CancelOrderRequest) ([]OrderId, error) {
	// Use int64 for internal tracking
	idInt := time.Now().UnixNano()
	idStr := fmt.Sprintf("%d", idInt)

	wsReqs := make([]interface{}, len(reqs))
	for i, r := range reqs {
		if r.InstIdCode == nil {
			return nil, fmt.Errorf("instIdCode is required for OKX WS batch cancel requests")
		}
		wsReqs[i] = wsCancelOrderRequest{
			InstIdCode: *r.InstIdCode,
			OrdId:      r.OrdId,
			ClOrdId:    r.ClOrdId,
		}
	}

	op := WsOrderOp{
		Id:   idStr,
		Op:   "batch-cancel-orders",
		Args: wsReqs,
	}

	// Create channel for response
	successCh, errorCh := c.AddPendingRequest(idInt)
	defer c.RemovePendingRequest(idInt)

	c.WriteMu.Lock()
	err := c.Conn.WriteJSON(op)
	c.WriteMu.Unlock()
	if err != nil {
		return nil, err
	}

	// Wait for response
	select {
	case msg := <-successCh:
		// Parse result
		var resp struct {
			Code string    `json:"code"`
			Msg  string    `json:"msg"`
			Data []OrderId `json:"data"`
		}
		if err := json.Unmarshal(msg, &resp); err != nil {
			return nil, fmt.Errorf("failed to parse WS response: %w", err)
		}
		if resp.Code != "0" {
			return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)
		}
		for _, result := range resp.Data {
			if err := validateWSActionResult("batch-cancel", result); err != nil {
				return resp.Data, err
			}
		}
		return resp.Data, nil

	case msg := <-errorCh:
		var resp struct {
			Code string `json:"code"`
			Msg  string `json:"msg"`
		}
		json.Unmarshal(msg, &resp)
		return nil, fmt.Errorf("okx ws error: code=%s msg=%s", resp.Code, resp.Msg)

	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("timeout waiting for batch cancel response")
	}
}

func validateWSActionResult(action string, result OrderId) error {
	if result.SCode == "" || result.SCode == "0" {
		return nil
	}
	if result.SubCode != "" {
		return fmt.Errorf("okx ws %s rejected: sCode=%s subCode=%s sMsg=%s", action, result.SCode, result.SubCode, result.SMsg)
	}
	return fmt.Errorf("okx ws %s rejected: sCode=%s sMsg=%s", action, result.SCode, result.SMsg)
}
