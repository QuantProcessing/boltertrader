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

// WSCommandOutcome records the mutation send boundary without exposing raw
// venue payloads. Sent means the complete WebSocket frame write succeeded.
type WSCommandOutcome struct {
	TransactionHash string
	Sent            bool
	Code            int
}

func (outcome WSCommandOutcome) Accepted() bool {
	return outcome.Sent && outcome.Code == 200 && outcome.TransactionHash != ""
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
func (c *WebsocketClient) sendTx(ctx context.Context, requestID string, txType int, txInfo interface{}) (*TxResponse, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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
		return nil, false, err
	}

	// Wait for gateway response
	timeout := c.TxResponseTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	select {
	case resp, ok := <-respChan:
		if !ok || resp == nil {
			return nil, true, ErrWSOutcomeUnknown
		}
		return resp, true, nil
	case <-ctx.Done():
		return nil, true, ctx.Err()
	case <-time.After(timeout):
		c.Logger.Warnw("tx response timeout", "requestID", requestID)
		return nil, true, ErrWSOutcomeUnknown
	}
}

// PlaceOrder places a new order via WebSocket
func (c *WebsocketClient) PlaceOrder(ctx context.Context, client *Client, req CreateOrderRequest) (string, error) {
	outcome, err := c.PlaceOrderOutcome(ctx, client, req)
	return outcome.TransactionHash, err
}

// PlaceOrderOutcome preserves gateway rejection and post-send ambiguity for
// callers that need truthful mutation acknowledgement semantics.
func (c *WebsocketClient) PlaceOrderOutcome(ctx context.Context, client *Client, req CreateOrderRequest) (WSCommandOutcome, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return WSCommandOutcome{}, err
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
		return WSCommandOutcome{}, fmt.Errorf("failed to hash order: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return WSCommandOutcome{}, fmt.Errorf("failed to sign order: %w", err)
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
	outcome := WSCommandOutcome{TransactionHash: ethCommon.Bytes2Hex(hash)}

	resp, sent, err := c.sendTx(ctx, requestID, TxTypeCreateOrder, payload)
	outcome.Sent = sent
	if err != nil {
		return outcome, err
	}
	outcome.Code = resp.Code
	if !resp.IsSuccess() {
		client.InvalidateNonce()
		return outcome, fmt.Errorf("%w: code=%d", ErrOrderRejected, resp.Code)
	}

	return outcome, nil
}

// CancelOrder cancels an order via WebSocket
func (c *WebsocketClient) CancelOrder(ctx context.Context, client *Client, req CancelOrderRequest) (string, error) {
	outcome, err := c.CancelOrderOutcome(ctx, client, req)
	return outcome.TransactionHash, err
}

// CancelOrderOutcome is the cancel counterpart of PlaceOrderOutcome.
func (c *WebsocketClient) CancelOrderOutcome(ctx context.Context, client *Client, req CancelOrderRequest) (WSCommandOutcome, error) {
	nonce, err := client.GetNextNonce(ctx)
	if err != nil {
		return WSCommandOutcome{}, err
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
		return WSCommandOutcome{}, fmt.Errorf("failed to hash cancel: %w", err)
	}

	signature, err := client.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return WSCommandOutcome{}, fmt.Errorf("failed to sign cancel: %w", err)
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
	outcome := WSCommandOutcome{TransactionHash: ethCommon.Bytes2Hex(hash)}

	resp, sent, err := c.sendTx(ctx, requestID, TxTypeCancelOrder, payload)
	outcome.Sent = sent
	if err != nil {
		return outcome, err
	}
	outcome.Code = resp.Code
	if !resp.IsSuccess() {
		client.InvalidateNonce()
		return outcome, fmt.Errorf("%w: code=%d", ErrOrderRejected, resp.Code)
	}

	return outcome, nil
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

	resp, _, err := c.sendTx(ctx, requestID, TxTypeModifyOrder, info)
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

	resp, _, err := c.sendTx(ctx, requestID, TxTypeCancelAllOrders, payload)
	if err != nil {
		return ethCommon.Bytes2Hex(hash), err
	}
	if resp != nil && !resp.IsSuccess() {
		client.InvalidateNonce()
		return ethCommon.Bytes2Hex(hash), fmt.Errorf("cancel all rejected: %s", resp.Error())
	}

	return ethCommon.Bytes2Hex(hash), nil
}
