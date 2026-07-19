package sdk

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

type utaTradeRequest struct {
	Op       string           `json:"op"`
	ID       string           `json:"id"`
	Category string           `json:"category,omitempty"`
	Topic    string           `json:"topic"`
	Args     []map[string]any `json:"args"`
}

type utaTradeResponse struct {
	Event    string        `json:"event"`
	ID       string        `json:"id"`
	Category string        `json:"category"`
	Topic    string        `json:"topic"`
	Args     []utaTradeAck `json:"args"`
	Code     NumberString  `json:"code"`
	Msg      string        `json:"msg"`
	ConnID   string        `json:"connId"`
	TS       string        `json:"ts"`
}

type utaTradeAck struct {
	Symbol    string `json:"symbol"`
	OrderID   string `json:"orderId"`
	ClientOID string `json:"clientOid"`
	CTime     string `json:"cTime"`
}

func (c *PrivateWSClient) PlaceUTAOrderWS(req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	return c.PlaceUTAOrderWSContext(context.Background(), req)
}

func (c *PrivateWSClient) PlaceUTAOrderWSContext(ctx context.Context, req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	resp, err := c.sendUTATradeRequest(ctx, "place-order", req.Category, map[string]any{
		"symbol":       req.Symbol,
		"orderType":    req.OrderType,
		"qty":          req.Qty,
		"price":        req.Price,
		"side":         req.Side,
		"timeInForce":  req.TimeInForce,
		"clientOid":    req.ClientOID,
		"reduceOnly":   normalizeUTAWSBool(req.ReduceOnly),
		"posSide":      req.PosSide,
		"stpMode":      req.STPMode,
		"tpTriggerBy":  req.TPTriggerBy,
		"slTriggerBy":  req.SLTriggerBy,
		"takeprofit":   req.TakeProfit,
		"stoploss":     req.StopLoss,
		"tpOrderType":  req.TPOrderType,
		"slOrderType":  req.SLOrderType,
		"tpLimitPrice": req.TPLimitPrice,
		"slLimitPrice": req.SLLimitPrice,
		"marginMode":   req.MarginMode,
	})
	if err != nil {
		return nil, err
	}
	ack := firstUTAAck(resp)
	return &PlaceOrderResponse{OrderID: ack.OrderID, ClientOID: ack.ClientOID}, nil
}

func (c *PrivateWSClient) CancelUTAOrderWS(req *CancelOrderRequest) (*CancelOrderResponse, error) {
	return c.CancelUTAOrderWSContext(context.Background(), req)
}

func (c *PrivateWSClient) CancelUTAOrderWSContext(ctx context.Context, req *CancelOrderRequest) (*CancelOrderResponse, error) {
	resp, err := c.sendUTATradeRequest(ctx, "cancel-order", req.Category, map[string]any{
		"orderId":   req.OrderID,
		"clientOid": req.ClientOID,
	})
	if err != nil {
		return nil, err
	}
	ack := firstUTAAck(resp)
	return &CancelOrderResponse{OrderID: ack.OrderID, ClientOID: ack.ClientOID}, nil
}

func (c *PrivateWSClient) sendUTATradeRequest(ctx context.Context, topic, category string, params map[string]any) (*utaTradeResponse, error) {
	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	req := utaTradeRequest{
		Op:       "trade",
		ID:       id,
		Category: normalizeUTACategory(category),
		Topic:    topic,
		Args:     []map[string]any{pruneUTATradeArgs(params)},
	}

	respBytes, err := c.sendRequestContext(ctx, id, req)
	if err != nil {
		return nil, err
	}

	var resp utaTradeResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, err
	}
	if !isWSSuccessCode(resp.Code) {
		return nil, newResponseError("private websocket "+topic, string(resp.Code), "venue rejected request")
	}
	return &resp, nil
}

func pruneUTATradeArgs(params map[string]any) map[string]any {
	pruned := make(map[string]any, len(params))
	for key, value := range params {
		switch v := value.(type) {
		case string:
			if v != "" {
				pruned[key] = v
			}
		default:
			if value != nil {
				pruned[key] = value
			}
		}
	}
	return pruned
}

func firstUTAAck(resp *utaTradeResponse) utaTradeAck {
	if resp == nil || len(resp.Args) == 0 {
		return utaTradeAck{}
	}
	return resp.Args[0]
}

func normalizeUTACategory(category string) string {
	return strings.ToLower(strings.TrimSpace(category))
}

func normalizeUTAWSBool(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes":
		return "YES"
	case "no":
		return "NO"
	default:
		return value
	}
}
