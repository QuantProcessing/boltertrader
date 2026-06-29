package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	p2 "github.com/elliottech/poseidon_crypto/hash/poseidon2_goldilocks"
	ethCommon "github.com/ethereum/go-ethereum/common"
)

// TxResponse represents a WebSocket transaction response from Lighter
type TxResponse struct {
	ID                       string          `json:"id"`
	Type                     string          `json:"type"`
	Code                     int             `json:"code"`              // 200 = success, others = error
	Message                  string          `json:"message,omitempty"` // Error message if code != 200
	TxHash                   string          `json:"tx_hash,omitempty"`
	PredictedExecutionTimeMs int64           `json:"predicted_execution_time_ms,omitempty"`
	Data                     json.RawMessage `json:"data,omitempty"`

	TxError *TxError `json:"error,omitempty"`
}

type TxError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// IsSuccess returns true if the response indicates success
func (r *TxResponse) IsSuccess() bool {
	return r.Code == 200
}

// Error returns the error message if the response is not successful
func (r *TxResponse) Error() string {
	if r.TxError != nil {
		return fmt.Sprintf("tx error: %d %s", r.TxError.Code, r.TxError.Message)
	}
	if r.IsSuccess() {
		return ""
	}
	if r.Message != "" {
		return r.Message
	}
	return fmt.Sprintf("transaction failed with code %d", r.Code)
}

// sendTxMsg 构建并发送交易消息
type txMsg struct {
	Type string    `json:"type"`
	Data txMsgData `json:"data"`
}

type txMsgData struct {
	ID     string      `json:"id,omitempty"`
	TxType int         `json:"tx_type"`
	TxInfo interface{} `json:"tx_info"`
}

// sendTx sends a transaction via WebSocket and waits for the server to acknowledge.
// The server echoes back the request ID, allowing us to match request → response.
// This validates the order at the gateway level; WatchOrders confirms execution.
func (c *WebsocketClient) sendTx(ctx context.Context, requestID string, txType int, txInfo interface{}) (*TxResponse, error) {
	// Register for response before sending
	respChan := c.RegisterPendingRequest(requestID)
	defer c.UnregisterPendingRequest(requestID)

	msg := txMsg{
		Type: "jsonapi/sendtx",
		Data: txMsgData{
			ID:     requestID,
			TxType: txType,
			TxInfo: txInfo,
		},
	}

	if err := c.Send(msg); err != nil {
		return nil, err
	}

	// Wait for gateway response
	select {
	case resp := <-respChan:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		c.Logger.Warnw("tx response timeout", "requestID", requestID)
		// Return nil response — order may still succeed, WatchOrders will confirm
		return nil, nil
	}
}

// PlaceOrder places a new order via WebSocket
func (c *WebsocketClient) PlaceOrder(ctx context.Context, client *Client, req CreateOrderRequest) (string, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return "", err
	}

	info := &CreateOrderInfo{
		AccountIndex:     client.AccountIndex,
		ApiKeyIndex:      uint32(client.KeyIndex),
		MarketIndex:      uint32(req.MarketId),
		ClientOrderIndex: req.ClientOrderId,
		BaseAmount:       req.BaseAmount,
		Price:            req.Price,
		IsAsk:            req.IsAsk,
		Type:             req.OrderType,
		TimeInForce:      req.TimeInForce,
		ReduceOnly:       req.ReduceOnly,
		TriggerPrice:     req.TriggerPrice,
		OrderExpiry:      req.OrderExpiry,
		Nonce:            nonce,
		ExpiredAt:        time.Now().Add(time.Minute * 10).UnixMilli(),
	}

	hash, err := HashCreateOrder(client.ChainId, info)
	if err != nil {
		return "", fmt.Errorf("failed to hash order: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return "", fmt.Errorf("failed to sign order: %w", err)
	}

	type OrderPayload struct {
		*CreateOrderInfo
		Sig        []byte `json:"Sig"`
		SignedHash string `json:"-"`
	}

	payload := &OrderPayload{
		CreateOrderInfo: info,
		Sig:             signature,
		SignedHash:      ethCommon.Bytes2Hex(hash),
	}

	requestID := fmt.Sprintf("order_%s", ethCommon.Bytes2Hex(hash)[:8])

	resp, err := c.sendTx(ctx, requestID, TxTypeCreateOrder, payload)
	if err != nil {
		return ethCommon.Bytes2Hex(hash), err
	}
	if resp != nil && !resp.IsSuccess() {
		client.InvalidateNonce()
		return ethCommon.Bytes2Hex(hash), fmt.Errorf("order rejected: %s", resp.Error())
	}

	return ethCommon.Bytes2Hex(hash), nil
}

// CancelOrder cancels an order via WebSocket
func (c *WebsocketClient) CancelOrder(ctx context.Context, client *Client, req CancelOrderRequest) (string, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return "", err
	}

	info := &CancelOrderInfo{
		AccountIndex: client.AccountIndex,
		ApiKeyIndex:  uint32(client.KeyIndex),
		MarketIndex:  uint32(req.MarketId),
		Index:        req.OrderId,
		Nonce:        nonce,
		ExpiredAt:    time.Now().Add(time.Hour * 24 * 7).UnixMilli(),
	}

	hash, err := HashCancelOrder(client.ChainId, info)
	if err != nil {
		return "", fmt.Errorf("failed to hash cancel: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return "", fmt.Errorf("failed to sign cancel: %w", err)
	}

	type CancelPayload struct {
		*CancelOrderInfo
		Sig        []byte `json:"Sig"`
		SignedHash string `json:"-"`
	}

	payload := &CancelPayload{
		CancelOrderInfo: info,
		Sig:             signature,
		SignedHash:      ethCommon.Bytes2Hex(hash),
	}

	requestID := fmt.Sprintf("cancel_%s", ethCommon.Bytes2Hex(hash)[:8])

	resp, err := c.sendTx(ctx, requestID, TxTypeCancelOrder, payload)
	if err != nil {
		return ethCommon.Bytes2Hex(hash), err
	}
	if resp != nil && !resp.IsSuccess() {
		client.InvalidateNonce()
		return ethCommon.Bytes2Hex(hash), fmt.Errorf("cancel rejected: %s", resp.Error())
	}

	return ethCommon.Bytes2Hex(hash), nil
}

// ModifyOrder modifies an order via WebSocket
func (c *WebsocketClient) ModifyOrder(ctx context.Context, client *Client, req ModifyOrderRequest) (string, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return "", err
	}

	info := &ModifyOrderInfo{
		AccountIndex: client.AccountIndex,
		ApiKeyIndex:  uint32(client.KeyIndex),
		MarketIndex:  uint32(req.MarketId),
		Index:        req.OrderIndex,
		BaseAmount:   req.BaseAmount,
		Price:        req.Price,
		TriggerPrice: req.TriggerPrice,
		Nonce:        nonce,
		ExpiredAt:    time.Now().Add(time.Hour * 24 * 7).UnixMilli(),
	}

	hash, err := HashModifyOrder(client.ChainId, info)
	if err != nil {
		return "", fmt.Errorf("failed to hash modify order: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return "", fmt.Errorf("failed to sign modify order: %w", err)
	}

	info.Sig = signature
	info.SignedHash = ethCommon.Bytes2Hex(hash)

	requestID := fmt.Sprintf("modify_%s", ethCommon.Bytes2Hex(hash)[:8])

	resp, err := c.sendTx(ctx, requestID, TxTypeModifyOrder, info)
	if err != nil {
		return ethCommon.Bytes2Hex(hash), err
	}
	if resp != nil && !resp.IsSuccess() {
		client.InvalidateNonce()
		return ethCommon.Bytes2Hex(hash), fmt.Errorf("modify rejected: %s", resp.Error())
	}

	return ethCommon.Bytes2Hex(hash), nil
}

// CancelAllOrders cancels all orders via WebSocket
func (c *WebsocketClient) CancelAllOrders(ctx context.Context, client *Client, req CancelAllOrdersRequest) (string, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return "", err
	}

	info := &CancelAllOrdersInfo{
		AccountIndex: client.AccountIndex,
		ApiKeyIndex:  uint32(client.KeyIndex),
		TimeInForce:  CancelAllTifImmediate, // 0 = ImmediateCancelAll
		Time:         0,                     // Required to be 0 for Immediate
		Nonce:        nonce,
		ExpiredAt:    time.Now().Add(time.Hour * 24 * 7).UnixMilli(),
	}

	hash, err := HashCancelAllOrders(client.ChainId, info)
	if err != nil {
		return "", fmt.Errorf("failed to hash cancel all: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return "", fmt.Errorf("failed to sign cancel all: %w", err)
	}

	type CancelAllPayload struct {
		*CancelAllOrdersInfo
		Sig        []byte `json:"Sig"`
		SignedHash string `json:"-"`
	}

	payload := &CancelAllPayload{
		CancelAllOrdersInfo: info,
		Sig:                 signature,
		SignedHash:          ethCommon.Bytes2Hex(hash),
	}

	requestID := fmt.Sprintf("cancelall_%s", ethCommon.Bytes2Hex(hash)[:8])

	resp, err := c.sendTx(ctx, requestID, TxTypeCancelAllOrders, payload)
	if err != nil {
		return ethCommon.Bytes2Hex(hash), err
	}
	if resp != nil && !resp.IsSuccess() {
		client.InvalidateNonce()
		return ethCommon.Bytes2Hex(hash), fmt.Errorf("cancel all rejected: %s", resp.Error())
	}

	return ethCommon.Bytes2Hex(hash), nil
}
