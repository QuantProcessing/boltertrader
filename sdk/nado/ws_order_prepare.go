package nado

import (
	"context"
	"encoding/json"
	"math/rand"
	"strconv"
)

// PreparedOrder contains the signed order and request ready for execution
type PreparedOrder struct {
	Tx        TxOrder
	Signature string
	Digest    string
	Request   map[string]interface{}
}

// PrepareOrder builds and signs an order without sending it.
func (c *WsApiClient) PrepareOrder(ctx context.Context, input ClientOrderInput) (*PreparedOrder, error) {
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

	signature, digest, err := c.Signer.SignOrder(txOrderString, verifyingContract)
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

	return &PreparedOrder{
		Tx:        txOrderString,
		Signature: signature,
		Digest:    digest,
		Request:   req,
	}, nil
}

// ExecutePreparedOrder executes a previously prepared order.
func (c *WsApiClient) ExecutePreparedOrder(ctx context.Context, order *PreparedOrder) (*PlaceOrderResponse, error) {
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
