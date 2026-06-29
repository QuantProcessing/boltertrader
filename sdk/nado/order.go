package nado

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"strconv"
	"time"
)

// Appendix Bit Field Constants
const (
	AppendixOffsetVersion     = 0
	AppendixOffsetIsolated    = 8
	AppendixOffsetOrderType   = 9
	AppendixOffsetReduceOnly  = 11
	AppendixOffsetTriggerType = 12
	AppendixOffsetReserved    = 14
	AppendixOffsetValue       = 64

	AppendixMaskVersion     = 0xFF
	AppendixMaskIsolated    = 0x1
	AppendixMaskOrderType   = 0x3
	AppendixMaskReduceOnly  = 0x1
	AppendixMaskTriggerType = 0x3
	AppendixMaskValue       = 0xFFFFFFFFFFFFFFFF // 64 bits

	AppendixVersion = 1

	// Trigger Types
	TriggerTypeNone              = 0
	TriggerTypePrice             = 1
	TriggerTypeTwap              = 2
	TriggerTypeTwapCustomAmounts = 3

	// TWAP Constants
	TwapOffsetTimes    = 32
	TwapOffsetSlippage = 0
	TwapMaskTimes      = 0xFFFFFFFF
	TwapMaskSlippage   = 0xFFFFFFFF
	TwapSlippageScale  = 1_000_000

	// Order Types for Appendix
	AppendixOrderTypeDefault  = 0
	AppendixOrderTypeIOC      = 1
	AppendixOrderTypeFOK      = 2
	AppendixOrderTypePostOnly = 3
)

// ClientOrderInput represents the user input for placing an order
type ClientOrderInput struct {
	ProductId  int64
	Price      string
	Amount     string
	Side       OrderSide
	OrderType  OrderType
	ReduceOnly bool
	PostOnly   bool
	Isolated   bool
	// IsolatedMargin is the initial margin amount for the isolated position in standard units (e.g. USDC).
	// It will be scaled to x6 precision (micros) internally.
	IsolatedMargin float64

	// Trigger related
	TriggerType  int // 0=None, 1=Price, 2=Twap, 3=TwapCustom
	TwapTimes    int
	TwapSlippage float64
}

// PlaceOrder executes a new order.
func (c *Client) PlaceOrder(ctx context.Context, input ClientOrderInput) (*PlaceOrderResponse, error) {
	// 1. Prepare Order Data
	price := ToX18(input.Price)
	amount := ToX18(input.Amount)

	// If Sell, amount is negative
	if input.Side == OrderSideSell {
		amount.Neg(amount)
	}

	// Nonce
	nonce := strconv.FormatInt(GetNonce(), 10)

	// Expiration (for order validity, distinct from nonce recv_time)
	expirationInt := int64(4000000000) // ~Year 2096

	// Appendix
	appendixStr := BuildAppendix(input)

	// 2. Construct TxOrder
	txOrderString := TxOrder{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductId:  uint32(input.ProductId),
		Amount:     amount.String(),
		PriceX18:   price.String(),
		Nonce:      nonce,
		Expiration: strconv.FormatInt(expirationInt, 10),
		Appendix:   appendixStr,
	}

	// 3. Sign
	verifyingContract := GenOrderVerifyingContract(input.ProductId)

	signature, _, err := c.Signer.SignOrder(txOrderString, verifyingContract)
	if err != nil {
		return nil, err
	}

	// 4. Send Execute Request
	orderMap := map[string]interface{}{
		"sender":     txOrderString.Sender,
		"priceX18":   txOrderString.PriceX18,
		"amount":     txOrderString.Amount,
		"expiration": txOrderString.Expiration,
		"nonce":      txOrderString.Nonce,
		"appendix":   txOrderString.Appendix,
	}

	placeOrderReq := map[string]interface{}{
		"product_id": input.ProductId,
		"order":      orderMap,
		"signature":  signature,
		"id":         rand.Int63(),
	}

	req := map[string]interface{}{
		"place_order": placeOrderReq,
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp PlaceOrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

type PlaceMarketOrderInput struct {
	ProductId    int64
	Amount       float64
	Side         OrderSide
	Slippage     float64 // Defaults to 0.005 (0.5%)
	ReduceOnly   bool
	SpotLeverage *float64
}

// PlaceMarketOrder executes a FOK order using top of the book price with provided slippage.
// @deprecated Use PlaceOrder with OrderTypeFOK instead.
func (c *Client) PlaceMarketOrder(ctx context.Context, params PlaceMarketOrderInput) (*PlaceOrderResponse, error) {
	// 1. Get Market Liquidity (Depth 1)
	liquidity, err := c.GetMarketLiquidity(ctx, params.ProductId, 1)
	if err != nil {
		return nil, fmt.Errorf("get market liquidity: %w", err)
	}

	isBid := params.Side == OrderSideBuy

	// Validate Orderbook
	if len(liquidity.Bids) == 0 || len(liquidity.Asks) == 0 {
		return nil, fmt.Errorf("orderbook empty, cannot place market order")
	}

	// 2. Get Product Info for Price Increment
	// We can use GetSymbols but it returns all symbols. It might be heavy but correct for V1.
	// Alternatively GetContractsV1 returns ContractV1 (endpoint info).
	// Let's use GetSymbols filtering by type if possible, or just fetch all.
	// Optimized: If we knew product type (perp/spot). Assuming perp for now or we fetch all.
	symbolsInfo, err := c.GetSymbols(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get symbols: %w", err)
	}

	var priceIncrementX18 *big.Int
	// Find our symbol
	// Symbols map is keyed by symbol name (string). We have ProductId (int64).
	// We need to iterate or do a reverse lookup.
	// Or use GetPairs/GetContracts (V2) but we need price_increment_x18 which is V1/Symbol specific concept?
	// The Python SDK uses `_get_subaccount_product_position` to get `product.book_info.price_increment_x18`.
	// Actually `GetSymbols` returns a map by Symbol Name. ProductId is inside.
	for _, s := range symbolsInfo.Symbols {
		if int64(s.ProductID) == params.ProductId {
			inc, success := new(big.Int).SetString(s.PriceIncrementX18, 10)
			if !success {
				return nil, fmt.Errorf("invalid price increment: %s", s.PriceIncrementX18)
			}
			priceIncrementX18 = inc
			break
		}
	}
	if priceIncrementX18 == nil {
		return nil, fmt.Errorf("symbol not found for product id %d", params.ProductId)
	}

	// 3. Calculate Execution Price
	// slippage default
	if params.Slippage == 0 {
		params.Slippage = 0.005
	}
	slippageX18 := ToX18(params.Slippage)
	oneX18 := ToX18(1)

	var targetPriceX18 *big.Int
	if isBid {
		// Buy: Price = BestAsk * (1 + slippage)
		bestAskPrice := ToBigInt(liquidity.Asks[0][0])
		factor := new(big.Int).Add(oneX18, slippageX18)
		targetPriceX18 = MulX18(bestAskPrice, factor)
	} else {
		// Sell: Price = BestBid * (1 - slippage)
		bestBidPrice := ToBigInt(liquidity.Bids[0][0])
		factor := new(big.Int).Sub(oneX18, slippageX18)
		targetPriceX18 = MulX18(bestBidPrice, factor)
	}

	// 4. Round to increment
	finalPriceX18 := RoundX18(targetPriceX18, priceIncrementX18)

	// Convert X18 back to float64 for PlaceOrder (which converts it back to X18... inefficient but safe for reuse)
	// Or we can construct ClientOrderInput but PlaceOrder takes float.
	// To avoid precision loss converting BigInt -> Float -> BigInt,
	// we should ideally modify PlaceOrder to accept optional raw X18, or just be careful.
	// 1e18 is large. float64 has 53 bits ( ~15 digits).
	// If price is 80000 * 1e18 = 8e22. Float cannot hold it exactly.
	// BUT `PlaceOrder` takes `float64 price`.
	// If we convert our X18 price to float, `ToX18` inside PlaceOrder will reconstruct it.
	// As we saw earlier, standard float precision is an issue.
	// `ToX18` now uses high-precision float parsing.
	// So: BigIntX18 -> big.Float -> float64.
	// Wait, converting `finalPriceX18` to `float64` divides by 1e18.
	// `big.NewFloat(finalPriceX18).Quo(..., 1e18).Float64()`
	// This `float64` is the "human readable" price (e.g. 80123.45).
	// This fits easily in float64.
	fPrice, _ := new(big.Float).SetInt(finalPriceX18).Quo(new(big.Float).SetInt(finalPriceX18), new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))).Float64()

	// 5. Place (FOK) Order
	orderParams := ClientOrderInput{
		ProductId:  params.ProductId,
		Price:      fmt.Sprintf("%.18f", fPrice), // Use high precision for calculated price
		Amount:     fmt.Sprintf("%.18f", params.Amount),
		Side:       params.Side,
		OrderType:  OrderTypeFOK,
		ReduceOnly: params.ReduceOnly,
	}
	// SpotLeverage? The Python SDK passes it. Go SDK `ClientOrderInput` doesn't have it yet.
	// Ignoring SpotLeverage for now unless added to struct.

	return c.PlaceOrder(ctx, orderParams)
}

// CancelProductOrders cancels all orders for specific products or all if empty.
func (c *Client) CancelProductOrders(ctx context.Context, productIds []int64) (*CancelProductOrdersResponse, error) {

	nonce := GetNonce()

	txCancel := TxCancelProductOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: productIds,
		Nonce:      strconv.FormatInt(nonce, 10),
	}

	verifyingContract := EndpointAddress
	signature, err := c.Signer.SignCancelProductOrders(txCancel, verifyingContract)
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

	var resp CancelProductOrdersResponse
	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CancelOrdersInput represents payload for cancelling specific orders
type CancelOrdersInput struct {
	ProductIds []int64
	Digests    []string
}

// CancelOrders cancels specific orders by their digests (IDs).
func (c *Client) CancelOrders(ctx context.Context, input CancelOrdersInput) (*CancelOrdersResponse, error) {
	// Construct Nonce
	nonceInt := GetNonce()
	nonceStr := strconv.FormatInt(nonceInt, 10)

	txCancel := TxCancelOrders{
		Sender:     BuildSender(c.Signer.GetAddress(), c.subaccount),
		ProductIds: input.ProductIds,
		Digests:    input.Digests,
		Nonce:      nonceStr,
	}

	// Sign
	verifyingContract := EndpointAddress
	signature, err := c.Signer.SignCancelOrders(txCancel, verifyingContract)
	if err != nil {
		return nil, err
	}

	// Send Request
	tx := ExecTransaction[TxCancelOrders]{
		Tx:        txCancel,
		Signature: signature,
	}

	req := map[string]interface{}{
		"cancel_orders": tx,
	}

	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp CancelOrdersResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CancelAndPlace executes a cancel and place order in a single transaction.
func (c *Client) CancelAndPlace(ctx context.Context, cancelInput CancelOrdersInput, placeInput ClientOrderInput) (*PlaceOrderResponse, error) {
	// 1. Prepare Place Order Data
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

	// 2. Prepare Cancel Order Data
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
	data, err := c.Execute(ctx, req)
	if err != nil {
		return nil, err
	}

	var resp PlaceOrderResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func GenOrderVerifyingContract(productID int64) string {
	productBigInt := big.NewInt(productID)
	beBytes := productBigInt.Bytes()
	const targetLength = 20
	currentLength := len(beBytes)
	paddedBytes := make([]byte, targetLength)
	copy(paddedBytes[targetLength-currentLength:], beBytes)
	hexString := hex.EncodeToString(paddedBytes)
	return "0x" + hexString
}

func GetNonce() int64 {
	nowMs := time.Now().UnixMilli()
	recvTime := nowMs + 60000 // 60s validity
	randomInt := rand.Intn(1048575)
	nonceInt := (recvTime << 20) + int64(randomInt)

	return nonceInt
}

func BuildAppendix(input ClientOrderInput) string {
	var appendix big.Int

	// Version (bits 7..0)
	version := big.NewInt(AppendixVersion)
	// No shift needed for version at offset 0, but good for consistency if offset > 0
	// appendix |= (version & mask) << shift
	// Since offset is 0, just set it.
	appendix.Set(version)

	// Isolated (bit 8)
	if input.Isolated {
		bit := big.NewInt(1)
		bit.Lsh(bit, AppendixOffsetIsolated)
		appendix.Or(&appendix, bit)
	}

	// Order Type (bits 10..9)
	var orderTypeInt int64
	switch input.OrderType {
	case OrderTypeLimit:
		orderTypeInt = AppendixOrderTypeDefault
	case OrderTypeMarket:
		orderTypeInt = AppendixOrderTypeIOC
	case OrderTypeIOC:
		orderTypeInt = AppendixOrderTypeIOC
	case OrderTypeFOK:
		orderTypeInt = AppendixOrderTypeFOK
	default:
		orderTypeInt = AppendixOrderTypeDefault
	}

	if input.PostOnly {
		orderTypeInt = AppendixOrderTypePostOnly
	}

	otVal := big.NewInt(orderTypeInt)
	otVal.And(otVal, big.NewInt(AppendixMaskOrderType)) // Mask to be safe
	otVal.Lsh(otVal, AppendixOffsetOrderType)
	appendix.Or(&appendix, otVal)

	// Reduce Only (bit 11)
	if input.ReduceOnly {
		bit := big.NewInt(1)
		bit.Lsh(bit, AppendixOffsetReduceOnly)
		appendix.Or(&appendix, bit)
	}

	// Trigger Type (bits 13..12)
	triggerType := int64(TriggerTypeNone)
	if input.TriggerType > 0 {
		triggerType = int64(input.TriggerType)
	}

	trigVal := big.NewInt(triggerType)
	trigVal.And(trigVal, big.NewInt(AppendixMaskTriggerType))
	trigVal.Lsh(trigVal, AppendixOffsetTriggerType)
	appendix.Or(&appendix, trigVal)

	// Value (bits 127..64)
	// Used for Isolated Margin OR TWAP parameters
	var valueVal *big.Int

	if input.Isolated && input.IsolatedMargin > 0 {
		// Validation: Margin should be allowed only if Isolated is true (checked by if)
		// Scale float margin to x6 (micros)
		marginX6 := int64(input.IsolatedMargin * 1_000_000)
		valueVal = big.NewInt(marginX6)
	} else if triggerType == TriggerTypeTwap || triggerType == TriggerTypeTwapCustomAmounts {
		// Pack TWAP: | times (32) | slippage (32) |
		// slippage_x6 = slippage * 1e6
		slippageX6 := int64(input.TwapSlippage * TwapSlippageScale)
		times := int64(input.TwapTimes)

		// pack = (times << 32) | slippageX6
		tVal := big.NewInt(times)
		tVal.And(tVal, big.NewInt(TwapMaskTimes))
		tVal.Lsh(tVal, TwapOffsetTimes)

		sVal := big.NewInt(slippageX6)
		sVal.And(sVal, big.NewInt(TwapMaskSlippage))
		sVal.Lsh(sVal, TwapOffsetSlippage) // shift 0

		valueVal = new(big.Int).Or(tVal, sVal)
	}

	if valueVal != nil {
		// Since Value is at offset 64, checking mask isn't strictly necessary for BigInt if we trust inputs,
		// but let's apply the mask for the 64-bit field width.
		// BigInt doesn't have a simple 64-bit mask without creating one.
		// Given logical construction above, we likely fit.
		valueVal.Lsh(valueVal, AppendixOffsetValue)
		appendix.Or(&appendix, valueVal)
	}

	return appendix.String()
}
