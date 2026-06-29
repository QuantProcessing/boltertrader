package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
)

// PlaceOrder executes a new order via Gateway WebSocket.
func (c *WsApiClient) PlaceOrder(ctx context.Context, input ClientOrderInput) (*PlaceOrderResponse, error) {
	// 1. Prepare Order Data
	price := ToX18(input.Price)
	amount := ToX18(input.Amount)

	if input.Side == OrderSideSell {
		amount.Neg(amount)
	}

	nonce := strconv.FormatInt(GetNonce(), 10)
	expirationInt := int64(4000000000)
	appendixStr := BuildAppendix(input)

	txOrderString := TxOrder{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductId:  uint32(input.ProductId),
		Amount:     amount.String(),
		PriceX18:   price.String(),
		Nonce:      nonce,
		Expiration: strconv.FormatInt(expirationInt, 10),
		Appendix:   appendixStr,
	}

	verifyingContract := GenOrderVerifyingContract(input.ProductId)

	signature, _, err := c.Signer.SignOrder(txOrderString, verifyingContract)
	if err != nil {
		return nil, err
	}

	orderMap := map[string]interface{}{
		"sender":     txOrderString.Sender,
		"priceX18":   txOrderString.PriceX18,
		"amount":     txOrderString.Amount,
		"expiration": txOrderString.Expiration,
		"nonce":      txOrderString.Nonce,
		"appendix":   txOrderString.Appendix,
	}

	id := rand.Int63()
	placeOrderReq := map[string]interface{}{
		"product_id": input.ProductId,
		"order":      orderMap,
		"signature":  signature,
		"id":         id,
	}

	req := map[string]interface{}{
		"place_order": placeOrderReq,
	}

	resp, err := c.Execute(ctx, req, &signature)
	if err != nil {
		return nil, err
	}

	var placeResp PlaceOrderResponse
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &placeResp); err != nil {
			return nil, err
		}
	}
	return &placeResp, nil
}

// CancelOrders cancels specific orders by their digests (IDs).
func (c *WsApiClient) CancelOrders(ctx context.Context, input CancelOrdersInput) (*CancelOrdersResponse, error) {
	nonceInt := GetNonce()
	nonceStr := strconv.FormatInt(nonceInt, 10)

	txCancel := TxCancelOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: input.ProductIds,
		Digests:    input.Digests,
		Nonce:      nonceStr,
	}

	verifyingContract := EndpointAddress
	signature, err := c.Signer.SignCancelOrders(txCancel, verifyingContract)
	if err != nil {
		return nil, err
	}

	tx := ExecTransaction[TxCancelOrders]{
		Tx:        txCancel,
		Signature: signature,
	}

	req := map[string]interface{}{
		"cancel_orders": tx,
	}

	resp, err := c.Execute(ctx, req, &signature)
	if err != nil {
		return nil, err
	}

	var cancelResp CancelOrdersResponse
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &cancelResp); err != nil {
			return nil, err
		}
	}
	return &cancelResp, nil
}

// CancelAndPlace executes a cancel and place order in a single transaction via WebSocket.
func (c *WsApiClient) CancelAndPlace(ctx context.Context, cancelInput CancelOrdersInput, placeInput ClientOrderInput) (*PlaceOrderResponse, error) {
	// 1. Prepare Place Order
	price := ToX18(placeInput.Price)
	amount := ToX18(placeInput.Amount)

	if placeInput.Side == OrderSideSell {
		amount.Neg(amount)
	}

	placeNonce := strconv.FormatInt(GetNonce(), 10)
	expirationInt := int64(4000000000)
	appendixStr := BuildAppendix(placeInput)

	txOrderString := TxOrder{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductId:  uint32(placeInput.ProductId),
		Amount:     amount.String(),
		PriceX18:   price.String(),
		Nonce:      placeNonce,
		Expiration: strconv.FormatInt(expirationInt, 10),
		Appendix:   appendixStr,
	}

	placeVerifyingContract := GenOrderVerifyingContract(placeInput.ProductId)
	placeSignature, _, err := c.Signer.SignOrder(txOrderString, placeVerifyingContract)
	if err != nil {
		return nil, fmt.Errorf("sign place order: %w", err)
	}

	placeOrderMap := map[string]interface{}{
		"sender":     txOrderString.Sender,
		"priceX18":   txOrderString.PriceX18,
		"amount":     txOrderString.Amount,
		"expiration": txOrderString.Expiration,
		"nonce":      txOrderString.Nonce,
		"appendix":   txOrderString.Appendix,
	}

	placeOrderObj := map[string]interface{}{
		"product_id": placeInput.ProductId,
		"order":      placeOrderMap,
		"signature":  placeSignature,
	}

	// 2. Prepare Cancel Order
	cancelNonce := strconv.FormatInt(GetNonce(), 10)

	txCancel := TxCancelOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: cancelInput.ProductIds,
		Digests:    cancelInput.Digests,
		Nonce:      cancelNonce,
	}

	cancelVerifyingContract := EndpointAddress
	cancelSignature, err := c.Signer.SignCancelOrders(txCancel, cancelVerifyingContract)
	if err != nil {
		return nil, fmt.Errorf("sign cancel orders: %w", err)
	}

	// 3. Construct Request
	cancelAndPlaceReq := map[string]interface{}{
		"cancel_tx":        txCancel,
		"cancel_signature": cancelSignature,
		"place_order":      placeOrderObj,
	}

	req := map[string]interface{}{
		"cancel_and_place": cancelAndPlaceReq,
	}

	// 4. Execute
	resp, err := c.Execute(ctx, req, &placeSignature)
	if err != nil {
		return nil, err
	}

	var placeResp PlaceOrderResponse
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &placeResp); err != nil {
			return nil, err
		}
	}
	return &placeResp, nil
}

// WsCancelProductOrders cancels orders via WS
// Deprecated: Use CancelOrders instead.
func (c *WsApiClient) WsCancelProductOrders(txCancel TxCancelProductOrders) (*WsResponse, error) {
	signer, err := NewSigner(c.privateKey)
	if err != nil {
		return nil, err
	}

	verifyingContract := EndpointAddress
	signature, err := signer.SignCancelProductOrders(txCancel, verifyingContract)
	if err != nil {
		return nil, err
	}

	tx := ExecTransaction[TxCancelProductOrders]{
		Tx:        txCancel,
		Signature: signature,
	}

	req := map[string]interface{}{
		"cancel_product_orders": tx,
	}

	return c.Execute(c.ctx, req, nil)
}
