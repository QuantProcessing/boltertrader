package lighter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	p2 "github.com/elliottech/poseidon_crypto/hash/poseidon2_goldilocks"
	ethCommon "github.com/ethereum/go-ethereum/common"
)

// PlaceOrder places a new order
func (c *Client) PlaceOrder(ctx context.Context, req CreateOrderRequest) (*CreateOrderResponse, error) {
	params, err := c.BuildCreateOrderTx(ctx, req)
	if err != nil {
		return nil, err
	}
	respData, err := c.PostForm(ctx, "/api/v1/sendTx", params, true)
	if err != nil {
		return nil, err
	}

	var res CreateOrderResponse
	if err := json.Unmarshal(respData, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// BuildCreateOrderTx builds a signed create-order transaction payload without sending it.
func (c *Client) BuildCreateOrderTx(ctx context.Context, req CreateOrderRequest) (map[string]string, error) {
	nonce, err := c.GetNextNonce(ctx)
	if err != nil {
		return nil, err
	}

	info := &CreateOrderInfo{
		AccountIndex:     c.AccountIndex,
		ApiKeyIndex:      uint32(c.KeyIndex),
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
		ExpiredAt:        time.Now().Add(time.Hour * 24 * 28).UnixMilli(), // Default expiry
	}

	hash, err := HashCreateOrder(c.ChainId, info)
	if err != nil {
		return nil, fmt.Errorf("failed to hash order: %w", err)
	}

	signature, err := c.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return nil, fmt.Errorf("failed to sign order: %w", err)
	}

	// Create the payload structure expected by the API
	type OrderPayload struct {
		*CreateOrderInfo
		Sig        []byte `json:"Sig"`
		SignedHash string `json:"-"`
	}

	payloadInfo := &OrderPayload{
		CreateOrderInfo: info,
		Sig:             signature,
		SignedHash:      ethCommon.Bytes2Hex(hash),
	}

	jsonBytes, err := json.Marshal(payloadInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal order info: %w", err)
	}

	params := map[string]string{
		"tx_type": fmt.Sprintf("%d", TxTypeCreateOrder),
		"tx_info": string(jsonBytes),
	}

	return params, nil
}

// CancelOrder cancels an order
func (c *Client) CancelOrder(ctx context.Context, req CancelOrderRequest) (*CancelOrderResponse, error) {
	nonce, err := c.GetNextNonce(ctx)
	if err != nil {
		return nil, err
	}

	info := &CancelOrderInfo{
		AccountIndex: c.AccountIndex,
		ApiKeyIndex:  uint32(c.KeyIndex),
		MarketIndex:  uint32(req.MarketId),
		Index:        req.OrderId,
		Nonce:        nonce,
		ExpiredAt:    time.Now().Add(time.Hour * 24 * 7).UnixMilli(),
	}

	hash, err := HashCancelOrder(c.ChainId, info)
	if err != nil {
		return nil, fmt.Errorf("failed to hash cancel: %w", err)
	}

	signature, err := c.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return nil, fmt.Errorf("failed to sign cancel: %w", err)
	}

	type CancelPayload struct {
		*CancelOrderInfo
		Sig        []byte `json:"Sig"`
		SignedHash string `json:"-"`
	}

	payloadInfo := &CancelPayload{
		CancelOrderInfo: info,
		Sig:             signature,
		SignedHash:      ethCommon.Bytes2Hex(hash),
	}

	jsonBytes, err := json.Marshal(payloadInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cancel info: %w", err)
	}

	params := map[string]string{
		"tx_type": fmt.Sprintf("%d", TxTypeCancelOrder),
		"tx_info": string(jsonBytes),
	}

	respData, err := c.PostForm(ctx, "/api/v1/sendTx", params, true)
	if err != nil {
		return nil, err
	}

	var res CancelOrderResponse
	if err := json.Unmarshal(respData, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// ModifyOrderRequest represents a modify order request
type ModifyOrderRequest struct {
	MarketId     int
	OrderIndex   int64
	BaseAmount   int64
	Price        uint32
	TriggerPrice uint32
}

// ModifyOrder modifies an existing order
func (c *Client) ModifyOrder(ctx context.Context, req ModifyOrderRequest) (*ModifyOrderResponse, error) {
	// Get nonce
	nonce, err := c.GetNextNonce(ctx)
	if err != nil {
		return nil, err
	}

	// Build modify info
	info := &ModifyOrderInfo{
		AccountIndex: c.AccountIndex,
		ApiKeyIndex:  uint32(c.KeyIndex),
		MarketIndex:  uint32(req.MarketId),
		Index:        req.OrderIndex,
		BaseAmount:   req.BaseAmount,
		Price:        req.Price,
		TriggerPrice: req.TriggerPrice,
		Nonce:        nonce,
		ExpiredAt:    time.Now().Add(time.Hour * 24 * 7).UnixMilli(),
	}

	// Hash and sign
	hash, err := HashModifyOrder(c.ChainId, info)
	if err != nil {
		return nil, fmt.Errorf("failed to hash modify order: %w", err)
	}

	signature, err := c.KeyManager.Sign(hash, p2.NewPoseidon2())
	if err != nil {
		return nil, fmt.Errorf("failed to sign modify order: %w", err)
	}

	info.Sig = signature
	info.SignedHash = ethCommon.Bytes2Hex(hash)

	// Serialize tx_info
	txInfoBytes, err := json.Marshal(info)
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		"tx_type": fmt.Sprintf("%d", TxTypeModifyOrder),
		"tx_info": string(txInfoBytes),
	}

	data, err := c.PostForm(ctx, "/api/v1/sendTx", params, true)
	if err != nil {
		return nil, err
	}

	var res ModifyOrderResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}

	return &res, nil
}

// SendTxBatch sends a batch of transactions
func (c *Client) SendTxBatch(ctx context.Context, txs []map[string]string) (*SendTxBatchResponse, error) {
	// Payload for batch is usually "txs": [...]
	payload := map[string]interface{}{
		"txs": txs,
	}

	data, err := c.Post(ctx, "/api/v1/sendTxBatch", payload, true)
	if err != nil {
		return nil, err
	}

	var res SendTxBatchResponse
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
