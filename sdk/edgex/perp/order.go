package perp

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Order Operations

func (c *Client) PlaceOrder(ctx context.Context, params PlaceOrderParams, contract *Contract, quoteCoin *Coin) (*CreateOrderData, error) {
	// Parse decimals
	size, err := decimal.NewFromString(params.Quantity)
	if err != nil {
		return nil, fmt.Errorf("invalid quantity: %w", err)
	}

	priceStr := params.Price
	if priceStr == "" {
		priceStr = "0"
	}
	price, err := decimal.NewFromString(priceStr)
	if err != nil {
		return nil, fmt.Errorf("invalid price: %w", err)
	}

	// Factors
	syntheticFactorBig, _ := HexToBigInteger(contract.StarkExResolution)
	syntheticFactor := decimal.NewFromBigInt(syntheticFactorBig, 0)

	shiftFactorBig, _ := HexToBigInteger(quoteCoin.StarkExResolution)
	shiftFactor := decimal.NewFromBigInt(shiftFactorBig, 0)

	// Calculate amounts
	valueDm := price.Mul(size)
	amountSynthetic := size.Mul(syntheticFactor).IntPart()
	amountCollateral := valueDm.Mul(shiftFactor).IntPart()

	// Fee
	feeRate, _ := decimal.NewFromString("0.001") // Default
	if contract.DefaultTakerFeeRate != "" {
		if fr, err := decimal.NewFromString(contract.DefaultTakerFeeRate); err == nil {
			feeRate = fr
		}
	}
	limitFee := size.Mul(price).Mul(feeRate).Ceil()
	maxAmountFee := limitFee.Mul(shiftFactor).BigInt().Int64()

	// Nonce & Expiration
	clientOrderId := params.ClientOrderId
	if clientOrderId == "" {
		clientOrderId = GetRandomClientId()
	}
	nonce := CalcNonce(clientOrderId)

	expireTime := params.ExpireTime
	if expireTime == 0 {
		// Default to 28 days if not set, or standard default
		expireTime = time.Now().Add(time.Hour * 24 * 28).UnixMilli()
	}

	// L2 Expiration: expireTime + 8 days (matches body logic)
	l2ExpireTime := expireTime + (8 * 24 * 60 * 60 * 1000)
	l2ExpireHour := l2ExpireTime / (60 * 60 * 1000)

	// 4. Calculate Hash
	isBuying := params.Side == "BUY"
	// Asset IDs need to be passed as strings (hex or dec? CalcLimitOrderHash handles it)
	// contract.StarkExSyntheticAssetId is hex usually
	// quoteCoin.StarkExAssetId is hex usually

	msgHash := CalcLimitOrderHash(
		contract.StarkExSyntheticAssetId,
		quoteCoin.StarkExAssetId,
		quoteCoin.StarkExAssetId, // Fee asset is usually same as quote for linear perps
		isBuying,
		amountSynthetic,
		amountCollateral,
		maxAmountFee,
		nonce,
		ToBigInt(c.AccountID).Int64(), // AccountID is string in Client, need int64
		l2ExpireHour,
	)

	// 5. Sign
	sig, err := SignL2(c.starkPrivateKey, msgHash)
	if err != nil {
		return nil, fmt.Errorf("failed to sign order: %w", err)
	}

	// 6. Construct Request
	// TimeInForce defaults
	timeInForce := params.TimeInForce
	if timeInForce == "" {
		if params.Type == "MARKET" {
			timeInForce = "IMMEDIATE_OR_CANCEL"
		} else {
			timeInForce = "GOOD_TIL_CANCEL"
		}
	}

	reqBody := map[string]interface{}{
		"accountId":     c.AccountID,
		"contractId":    contract.ContractId,
		"price":         priceStr,
		"size":          params.Quantity,
		"type":          params.Type,
		"side":          params.Side,
		"timeInForce":   timeInForce,
		"clientOrderId": clientOrderId,
		"expireTime":    fmt.Sprintf("%d", expireTime),
		"l2Nonce":       fmt.Sprintf("%d", nonce),
		"l2Signature":   fmt.Sprintf("%s%s%s", sig.R, sig.S, sig.V),
		"l2ExpireTime":  fmt.Sprintf("%d", l2ExpireTime),
		"l2Value":       valueDm.String(),
		"l2Size":        params.Quantity,
		"l2LimitFee":    limitFee.String(),
		"reduceOnly":    params.ReduceOnly,
	}

	var resData CreateOrderData
	err = c.call(ctx, http.MethodPost, "/api/v1/private/order/createOrder", reqBody, true, &resData)
	if err != nil {
		return nil, err
	}

	return &resData, nil
}

func (c *Client) CancelOrder(ctx context.Context, orderId string) (*CancelOrderData, error) {
	params := map[string]interface{}{
		"accountId":   c.AccountID,
		"orderIdList": []string{orderId},
	}
	var resData CancelOrderData
	err := c.call(ctx, http.MethodPost, "/api/v1/private/order/cancelOrderById", params, true, &resData)
	return &resData, err
}

func (c *Client) CancelAllOrders(ctx context.Context) error {
	params := map[string]interface{}{
		"accountId": c.AccountID,
	}
	return c.call(ctx, http.MethodPost, "/api/v1/private/order/cancelAllOrder", params, true, nil)
}

func (c *Client) GetOrdersByIds(ctx context.Context, orderIds []string) ([]Order, error) {
	var res []Order
	params := map[string]interface{}{
		"accountId":   c.AccountID,
		"orderIdList": orderIds,
	}
	// Endpoint guessed from user link/pattern: getOrdersByAccountIdAndOrderIdsBatch
	// Path: /api/v1/private/order/getOrdersByAccountIdAndOrderIdsBatch
	err := c.call(ctx, http.MethodPost, "/api/v1/private/order/getOrdersByAccountIdAndOrderIdsBatch", params, true, &res)
	return res, err
}
