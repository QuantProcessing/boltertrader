package nado

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
)

// PreparedOrder contains the signed order and request ready for execution
type PreparedOrder struct {
	Tx           TxOrder
	Signature    string
	Digest       string
	EncodedOrder string
	Request      map[string]interface{}
	requestHash  [32]byte
}

// PrepareOrder builds and signs an order without sending it.
func (c *WsApiClient) PrepareOrder(ctx context.Context, input ClientOrderInput) (*PreparedOrder, error) {
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

	signature, digest, err := c.Signer.SignOrder(txOrderString, verifyingContract)
	if err != nil {
		return nil, err
	}
	encodedOrder, err := EncodeSignedOrder(txOrderString, signature)
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
	requestHash, err := hashPreparedRequest(req)
	if err != nil {
		return nil, err
	}

	return &PreparedOrder{
		Tx:           txOrderString,
		Signature:    signature,
		Digest:       digest,
		EncodedOrder: encodedOrder,
		Request:      req,
		requestHash:  requestHash,
	}, nil
}

// ExecutePreparedOrder executes a previously prepared order.
func (c *WsApiClient) ExecutePreparedOrder(ctx context.Context, order *PreparedOrder) (*PlaceOrderResponse, error) {
	if order == nil {
		return nil, fmt.Errorf("nado prepared order is required")
	}
	encoded, err := EncodeSignedOrder(order.Tx, order.Signature)
	if err != nil || encoded != order.EncodedOrder {
		return nil, fmt.Errorf("nado prepared order signed candidate mismatch")
	}
	requestHash, err := hashPreparedRequest(order.Request)
	if err != nil || requestHash != order.requestHash {
		return nil, fmt.Errorf("nado prepared order payload mismatch")
	}
	resp, err := c.Execute(ctx, order.Request, &order.Signature)
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

func hashPreparedRequest(request map[string]interface{}) ([32]byte, error) {
	var zero [32]byte
	if request == nil {
		return zero, fmt.Errorf("nado prepared order request is required")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return zero, fmt.Errorf("marshal nado prepared order request: %w", err)
	}
	return sha256.Sum256(encoded), nil
}
