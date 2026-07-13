package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
)

// PlaceOrder executes the venue's raw place_order API over Gateway WebSocket.
// It intentionally does not apply the adapter/runtime pre-trade safety envelope.
func (c *WsApiClient) PlaceOrder(ctx context.Context, input ClientOrderInput) (*PlaceOrderResponse, error) {
	input, price, amount, err := c.prepareOrderWrite(ctx, input)
	if err != nil {
		return nil, err
	}

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

	id := nextGatewayRequestID()
	placeOrderReq := map[string]interface{}{
		"product_id": input.ProductId,
		"order":      orderMap,
		"signature":  signature,
		"id":         id,
	}
	if input.SpotLeverage != nil {
		placeOrderReq["spot_leverage"] = *input.SpotLeverage
	}
	if input.BorrowMargin != nil {
		placeOrderReq["borrow_margin"] = *input.BorrowMargin
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
	if err := validateNadoCancelInput(input); err != nil {
		return nil, err
	}
	nonceInt := GetNonce()
	nonceStr := strconv.FormatInt(nonceInt, 10)

	txCancel := TxCancelOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: input.ProductIds,
		Digests:    input.Digests,
		Nonce:      nonceStr,
	}

	verifyingContract, err := c.endpointAddress(ctx)
	if err != nil {
		return nil, err
	}
	signature, err := c.Signer.SignCancelOrders(txCancel, verifyingContract)
	if err != nil {
		return nil, err
	}

	tx := ExecTransaction[TxCancelOrders]{
		Tx:        txCancel,
		Signature: signature,
		ID:        nextGatewayRequestID(),
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
	if err := validateNadoCancelInput(cancelInput); err != nil {
		return nil, err
	}
	placeInput, price, amount, err := c.prepareOrderWrite(ctx, placeInput)
	if err != nil {
		return nil, err
	}

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
	if placeInput.SpotLeverage != nil {
		placeOrderObj["spot_leverage"] = *placeInput.SpotLeverage
	}
	if placeInput.BorrowMargin != nil {
		placeOrderObj["borrow_margin"] = *placeInput.BorrowMargin
	}

	// 2. Prepare Cancel Order
	cancelNonce := strconv.FormatInt(GetNonce(), 10)

	txCancel := TxCancelOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: cancelInput.ProductIds,
		Digests:    cancelInput.Digests,
		Nonce:      cancelNonce,
	}

	cancelVerifyingContract, err := c.endpointAddress(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover cancel contract: %w", err)
	}
	cancelSignature, err := c.Signer.SignCancelOrders(txCancel, cancelVerifyingContract)
	if err != nil {
		return nil, fmt.Errorf("sign cancel orders: %w", err)
	}

	// 3. Construct Request
	cancelAndPlaceReq := map[string]interface{}{
		"id":               nextGatewayRequestID(),
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

func (c *WsApiClient) CancelProductOrders(ctx context.Context, productIDs []int64) (*CancelProductOrdersResponse, error) {
	if c.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	txCancel := TxCancelProductOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: productIDs,
		Nonce:      strconv.FormatInt(GetNonce(), 10),
	}

	verifyingContract, err := c.endpointAddress(ctx)
	if err != nil {
		return nil, err
	}
	signature, err := c.Signer.SignCancelProductOrders(txCancel, verifyingContract)
	if err != nil {
		return nil, err
	}

	tx := ExecTransaction[TxCancelProductOrders]{
		Tx:        txCancel,
		Signature: signature,
		ID:        nextGatewayRequestID(),
	}

	req := map[string]interface{}{
		"cancel_product_orders": tx,
	}

	resp, err := c.Execute(ctx, req, &signature)
	if err != nil {
		return nil, err
	}
	var cancelResp CancelProductOrdersResponse
	if len(resp.Data) > 0 {
		if err := json.Unmarshal(resp.Data, &cancelResp); err != nil {
			return nil, err
		}
	}
	return &cancelResp, nil
}

func (c *WsApiClient) endpointAddress(ctx context.Context) (string, error) {
	if c.restClient == nil {
		return "", fmt.Errorf("nado ws api client: rest client is required")
	}
	contract, err := c.restClient.ensureContracts(ctx)
	if err != nil {
		return "", fmt.Errorf("nado ws api contracts discovery: %w", err)
	}
	return contract.EndpointAddress, nil
}

func (c *WsApiClient) prepareOrderWrite(ctx context.Context, input ClientOrderInput) (ClientOrderInput, *big.Int, *big.Int, error) {
	if c.restClient == nil {
		return input, nil, nil, fmt.Errorf("nado ws api client: rest client is required")
	}
	if _, err := c.restClient.ensureContracts(ctx); err != nil {
		return input, nil, nil, fmt.Errorf("nado ws api contracts discovery: %w", err)
	}
	product, err := c.restClient.ResolveProduct(ctx, input.ProductId)
	if err != nil {
		return input, nil, nil, err
	}
	return prepareNadoOrderInput(product, input)
}
