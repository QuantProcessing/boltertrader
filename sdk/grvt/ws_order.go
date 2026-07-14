package grvt

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func (c *WebsocketClient) ensureInstruments(ctx context.Context) error {
	c.instrumentsMu.RLock()
	if len(c.instruments) > 0 {
		c.instrumentsMu.RUnlock()
		return nil
	}
	c.instrumentsMu.RUnlock()

	c.instrumentsMu.Lock()
	defer c.instrumentsMu.Unlock()

	// Double check
	if len(c.instruments) > 0 {
		return nil
	}

	instruments, err := c.client.GetInstruments(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch instruments: %w", err)
	}

	for _, inst := range instruments {
		c.instruments[inst.Instrument] = inst
	}
	return nil
}

func (c *WebsocketClient) PlaceOrder(ctx context.Context, req *OrderRequest) (*CreateOrderResponse, error) {
	if !c.auth {
		return nil, fmt.Errorf("client not authenticated")
	}

	// 1. Ensure instruments are loaded for signing
	if err := c.ensureInstruments(ctx); err != nil {
		return nil, err
	}

	// Default Nonce/Expiration
	if req.Signature.Nonce == 0 {
		req.Signature.Nonce = uint32(time.Now().Unix())
	}
	if req.Signature.Expiration == "" {
		req.Signature.Expiration = strconv.FormatInt(time.Now().Add(1*time.Hour).UnixNano(), 10)
	}

	// 2. Sign if not signed or if explicit signing required
	// Assuming req.Signature might be empty or we should always re-sign?
	// The caller might not have keys. The client has keys.
	// We'll sign using client's keys.
	c.instrumentsMu.RLock()
	instruments := c.instruments
	c.instrumentsMu.RUnlock()

	// Clone map for safety? SignOrder takes map.
	// We can pass the map directly if SignOrder only reads.
	// SignOrder reads.

	if err := SignOrder(req, c.client.PrivateKey, c.client.ChainID, instruments); err != nil {
		return nil, fmt.Errorf("failed to sign order: %w", err)
	}

	// 3. Construct params (Lite Protocol: Short keys)
	// We map manually to WsCreateOrderParams (now defined with short keys in ws_type.go)
	// to ensure SubAccountID is string as required by the endpoint.
	legs := make([]WsOrderLeg, len(req.Legs))
	for i, l := range req.Legs {
		legs[i] = WsOrderLeg{
			Instrument:       l.Instrument,
			Size:             l.Size,
			LimitPrice:       l.LimitPrice,
			IsBuyingContract: l.IsBuyintAsset,
		}
	}

	wsParams := WsCreateOrderParams{
		Order: WsOrderBody{
			SubAccountID: fmt.Sprintf("%d", req.SubAccountID),
			IsMarket:     req.IsMarket,
			TimeInForce:  req.TimeInForce,
			PostOnly:     req.PostOnly,
			ReduceOnly:   req.ReduceOnly,
			Legs:         legs,
			Signature: WsOrderSignature{
				Signer:     req.Signature.Signer,
				R:          req.Signature.R,
				S:          req.Signature.S,
				V:          req.Signature.V,
				Expiration: req.Signature.Expiration,
				Nonce:      req.Signature.Nonce,
				// ChainID seems present in user example 'ci'
				ChainID: req.Signature.ChainID,
			},
			Metadata: WsOrderMetadata{
				ClientOrderID: req.Metadata.ClientOrderID,
				CreatedTime:   fmt.Sprintf("%d", time.Now().UnixNano()), // User example has 'ct'
			},
		},
	}

	// 4. Send RPC
	resp, err := c.SendRPC("v1/create_order", wsParams)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, formatWSRPCError(resp.Error)
	}

	// 5. Parse Result
	var result CreateOrderResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

func (c *WebsocketClient) CancelOrder(ctx context.Context, req *CancelOrderRequest) (*CancelOrderResponse, error) {
	if !c.auth {
		return nil, fmt.Errorf("client not authenticated")
	}

	// method: "v1/cancel_order"
	resp, err := c.SendRPC("v1/cancel_order", req)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, formatWSRPCError(resp.Error)
	}

	var result CancelOrderResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

func (c *WebsocketClient) CancelAllOrders(ctx context.Context, req *CancelAllOrderRequest) (*CancelAllOrderResponse, error) {
	if !c.auth {
		return nil, fmt.Errorf("client not authenticated")
	}

	// method: "v1/cancel_all_orders"
	resp, err := c.SendRPC("v1/cancel_all_orders", req)
	if err != nil {
		return nil, err
	}

	if resp.Error != nil {
		return nil, formatWSRPCError(resp.Error)
	}

	var result CancelAllOrderResponse
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal result: %w", err)
	}

	return &result, nil
}

func formatWSRPCError(rpcErr *WsRpcError) error {
	const signatureMismatch = "signature does not match payload"
	if strings.Contains(strings.ToLower(rpcErr.Message), signatureMismatch) {
		return fmt.Errorf("rpc request rejected with code %d: %s", rpcErr.Code, signatureMismatch)
	}
	return fmt.Errorf("rpc request rejected with code %d", rpcErr.Code)
}
