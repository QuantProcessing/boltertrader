package sdk

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type classicTradeRequest struct {
	Op   string              `json:"op"`
	Args []classicTradeEntry `json:"args"`
}

type classicTradeEntry struct {
	ID       string         `json:"id"`
	InstType string         `json:"instType"`
	InstID   string         `json:"instId"`
	Channel  string         `json:"channel"`
	Params   map[string]any `json:"params"`
}

type classicTradeResponse struct {
	Event string            `json:"event"`
	Arg   []classicTradeAck `json:"arg"`
	Code  NumberString      `json:"code"`
	Msg   string            `json:"msg"`
}

type classicTradeAck struct {
	ID       string `json:"id"`
	InstType string `json:"instType"`
	InstID   string `json:"instId"`
	Channel  string `json:"channel"`
	Params   struct {
		OrderID   string `json:"orderId"`
		ClientOID string `json:"clientOid"`
	} `json:"params"`
}

func (c *PrivateWSClient) PlaceClassicSpotOrderWS(req *PlaceOrderRequest) (*PlaceOrderResponse, error) {
	resp, err := c.sendClassicTradeRequest(req.Symbol, "SPOT", "place-order", map[string]any{
		"orderType": req.OrderType,
		"side":      req.Side,
		"size":      req.Qty,
		"price":     req.Price,
		"force":     req.TimeInForce,
		"clientOid": req.ClientOID,
	})
	if err != nil {
		return nil, err
	}
	return &PlaceOrderResponse{
		OrderID:   firstClassicAck(resp).Params.OrderID,
		ClientOID: firstClassicAck(resp).Params.ClientOID,
	}, nil
}

func (c *PrivateWSClient) CancelClassicSpotOrderWS(symbol, orderID, clientOID string) (*CancelOrderResponse, error) {
	resp, err := c.sendClassicTradeRequest(symbol, "SPOT", "cancel-order", map[string]any{
		"orderId":   orderID,
		"clientOid": clientOID,
	})
	if err != nil {
		return nil, err
	}
	return &CancelOrderResponse{
		OrderID:   firstClassicAck(resp).Params.OrderID,
		ClientOID: firstClassicAck(resp).Params.ClientOID,
	}, nil
}

func (c *PrivateWSClient) PlaceClassicPerpOrderWS(req *PlaceOrderRequest, instType, marginCoin string) (*PlaceOrderResponse, error) {
	params := map[string]any{
		"orderType":  req.OrderType,
		"side":       req.Side,
		"size":       req.Qty,
		"price":      req.Price,
		"force":      req.TimeInForce,
		"clientOid":  req.ClientOID,
		"marginCoin": marginCoin,
		"marginMode": req.MarginMode,
		"tradeSide":  req.TradeSide,
	}
	if !strings.EqualFold(req.ReduceOnly, "no") {
		params["reduceOnly"] = req.ReduceOnly
	}
	resp, err := c.sendClassicTradeRequest(req.Symbol, instType, "place-order", params)
	if err != nil {
		return nil, err
	}
	return &PlaceOrderResponse{
		OrderID:   firstClassicAck(resp).Params.OrderID,
		ClientOID: firstClassicAck(resp).Params.ClientOID,
	}, nil
}

func (c *PrivateWSClient) CancelClassicPerpOrderWS(symbol, instType, marginCoin, orderID, clientOID string) (*CancelOrderResponse, error) {
	resp, err := c.sendClassicTradeRequest(symbol, instType, "cancel-order", map[string]any{
		"orderId":    orderID,
		"clientOid":  clientOID,
		"marginCoin": marginCoin,
	})
	if err != nil {
		return nil, err
	}
	return &CancelOrderResponse{
		OrderID:   firstClassicAck(resp).Params.OrderID,
		ClientOID: firstClassicAck(resp).Params.ClientOID,
	}, nil
}

func (c *PrivateWSClient) sendClassicTradeRequest(instID, instType, channel string, params map[string]any) (*classicTradeResponse, error) {
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

	id := strconv.FormatInt(time.Now().UnixNano(), 10)
	req := classicTradeRequest{
		Op: "trade",
		Args: []classicTradeEntry{{
			ID:       id,
			InstType: instType,
			InstID:   instID,
			Channel:  channel,
			Params:   pruned,
		}},
	}

	respBytes, err := c.sendRequest(id, req)
	if err != nil {
		return nil, err
	}

	var resp classicTradeResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, err
	}
	if !isWSSuccessCode(resp.Code) {
		return nil, fmt.Errorf("bitget private ws: %s failed: %s %s", channel, resp.Code, resp.Msg)
	}
	return &resp, nil
}

func firstClassicAck(resp *classicTradeResponse) classicTradeAck {
	if resp == nil || len(resp.Arg) == 0 {
		return classicTradeAck{}
	}
	return resp.Arg[0]
}

func isWSSuccessCode(code NumberString) bool {
	switch strings.TrimSpace(string(code)) {
	case "", "0", "00000":
		return true
	default:
		return false
	}
}
