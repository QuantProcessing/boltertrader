package nado

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func orderRequestToNado(req model.OrderRequest, inst *model.Instrument, productID int64) (sdk.ClientOrderInput, error) {
	if inst == nil || productID < 0 {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: instrument/product id required")
	}
	if req.InstrumentID != inst.ID {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: order instrument does not match resolved instrument")
	}
	if !req.TriggerPrice.IsZero() || !req.ActivationPrice.IsZero() || !req.TrailingOffsetBps.IsZero() {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: trigger, activation, and trailing order fields are not supported: %w", contract.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: hedge position side is not supported: %w", contract.ErrNotSupported)
	}
	side, err := sideToNado(req.Side)
	if err != nil {
		return sdk.ClientOrderInput{}, err
	}
	orderType, err := orderTypeToNado(req.Type)
	if err != nil {
		return sdk.ClientOrderInput{}, err
	}
	postOnly := false
	if req.Type == enums.TypeMarket && req.TIF != enums.TifUnknown && req.TIF != enums.TifIOC {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: market orders require IOC semantics: %w", contract.ErrNotSupported)
	}
	switch req.TIF {
	case enums.TifUnknown, enums.TifGTC:
	case enums.TifIOC:
		orderType = sdk.OrderTypeIOC
	case enums.TifFOK:
		orderType = sdk.OrderTypeFOK
	case enums.TifGTX:
		if req.Type != enums.TypeLimit {
			return sdk.ClientOrderInput{}, fmt.Errorf("nado: post-only requires a limit order: %w", contract.ErrNotSupported)
		}
		postOnly = true
	default:
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: unsupported TIF %s: %w", req.TIF, contract.ErrNotSupported)
	}
	if req.InstrumentID.Kind == enums.KindSpot && req.ReduceOnly {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: reduce-only spot orders are not supported: %w", contract.ErrNotSupported)
	}
	price, err := exactStepString(req.Price, inst.PriceTick, "price")
	if err != nil {
		return sdk.ClientOrderInput{}, err
	}
	amount, err := exactStepString(req.Quantity, inst.SizeStep, "quantity")
	if err != nil {
		return sdk.ClientOrderInput{}, err
	}
	if inst.MinQty.IsPositive() && req.Quantity.LessThan(inst.MinQty) {
		return sdk.ClientOrderInput{}, fmt.Errorf("nado: quantity %s below minimum %s", req.Quantity, inst.MinQty)
	}
	out := sdk.ClientOrderInput{
		ProductId:  productID,
		Price:      price,
		Amount:     amount,
		Side:       side,
		OrderType:  orderType,
		ReduceOnly: req.ReduceOnly,
		PostOnly:   postOnly,
	}
	if req.InstrumentID.Kind == enums.KindSpot {
		noLeverage := false
		out.SpotLeverage = &noLeverage
	}
	return out, nil
}

func sideToNado(side enums.OrderSide) (sdk.OrderSide, error) {
	switch side {
	case enums.SideBuy:
		return sdk.OrderSideBuy, nil
	case enums.SideSell:
		return sdk.OrderSideSell, nil
	default:
		return "", fmt.Errorf("nado: unsupported side %s: %w", side, contract.ErrNotSupported)
	}
}

func sideFromNado(side sdk.OrderSide) enums.OrderSide {
	switch side {
	case sdk.OrderSideBuy:
		return enums.SideBuy
	case sdk.OrderSideSell:
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToNado(t enums.OrderType) (sdk.OrderType, error) {
	switch t {
	case enums.TypeLimit:
		return sdk.OrderTypeLimit, nil
	case enums.TypeMarket:
		return sdk.OrderTypeIOC, nil
	default:
		return "", fmt.Errorf("nado: unsupported order type %s: %w", t, contract.ErrNotSupported)
	}
}

func orderTypeFromNado(value string) enums.OrderType {
	switch strings.ToLower(value) {
	case string(sdk.OrderTypeLimit), "":
		return enums.TypeLimit
	case string(sdk.OrderTypeMarket), string(sdk.OrderTypeIOC), string(sdk.OrderTypeFOK):
		return enums.TypeMarket
	default:
		return enums.TypeUnknown
	}
}

func tifFromNado(value sdk.OrderType) enums.TimeInForce {
	switch value {
	case sdk.OrderTypeIOC:
		return enums.TifIOC
	case sdk.OrderTypeFOK:
		return enums.TifFOK
	default:
		return enums.TifGTC
	}
}

func statusFromNadoReason(reason sdk.OrderUpdateReason) enums.OrderStatus {
	switch reason {
	case sdk.OrderReasonPlaced:
		return enums.StatusNew
	case sdk.OrderReasonFilled:
		return enums.StatusFilled
	case sdk.OrderReasonCancelled:
		return enums.StatusCanceled
	default:
		return enums.StatusUnknown
	}
}

func orderFromNadoRecord(record sdk.Order, id model.InstrumentID, accountID string) (model.Order, error) {
	amount, err := parseDecimalRequired(record.Amount, "order amount")
	if err != nil {
		return model.Order{}, err
	}
	unfilled, err := parseDecimalRequired(record.UnfilledAmount, "order unfilled amount")
	if err != nil {
		return model.Order{}, err
	}
	price, err := parseX18Required(record.PriceX18, "order price")
	if err != nil {
		return model.Order{}, err
	}
	qty := amount.Abs()
	filled := qty.Sub(unfilled.Abs())
	if filled.IsNegative() {
		return model.Order{}, fmt.Errorf("nado: order unfilled amount exceeds original amount")
	}
	side := enums.SideBuy
	if amount.IsNegative() {
		side = enums.SideSell
	}
	orderType := orderTypeFromNado(record.OrderType)
	reduceOnly, err := appendixReduceOnly(record.Appendix)
	if err != nil {
		return model.Order{}, err
	}
	status := enums.StatusNew
	if !filled.IsZero() {
		status = enums.StatusPartiallyFilled
	}
	if unfilled.IsZero() {
		status = enums.StatusFilled
	}
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			Side:         side,
			Type:         orderType,
			TIF:          tifFromNado(sdk.OrderType(record.OrderType)),
			Quantity:     qty,
			Price:        price,
			PositionSide: enums.PosNet,
			ReduceOnly:   reduceOnly,
		},
		VenueOrderID: record.Digest,
		Status:       status,
		FilledQty:    filled,
		CreatedAt:    timeFromMillis(record.PlacedAt),
		UpdatedAt:    timeFromMillis(record.PlacedAt),
	}, nil
}

func archiveOrderFromNadoRecord(record sdk.ArchiveOrder, id model.InstrumentID, accountID string, submitted *model.OrderRequest, knownTerminal enums.OrderStatus) (model.Order, error) {
	amount, err := parseX18Required(record.Amount, "archive order amount")
	if err != nil {
		return model.Order{}, err
	}
	qty := amount.Abs()
	if !qty.IsPositive() {
		return model.Order{}, fmt.Errorf("nado: archive order quantity must be positive")
	}
	filled, err := parseX18Required(record.BaseFilled, "archive order base filled")
	if err != nil {
		return model.Order{}, err
	}
	filled = filled.Abs()
	if !filled.IsPositive() {
		return model.Order{}, fmt.Errorf("nado: archive order must contain a positive matched quantity")
	}
	if filled.GreaterThan(qty) {
		return model.Order{}, fmt.Errorf("nado: archive order filled quantity %s exceeds quantity %s", filled, qty)
	}
	price, err := parseX18Required(record.PriceX18, "archive order price")
	if err != nil {
		return model.Order{}, err
	}
	if !price.IsPositive() {
		return model.Order{}, fmt.Errorf("nado: archive order price must be positive")
	}
	quoteFilled, err := parseX18Required(record.QuoteFilled, "archive order quote filled")
	if err != nil {
		return model.Order{}, err
	}
	side := enums.SideBuy
	if amount.IsNegative() {
		side = enums.SideSell
	}
	reduceOnly, err := appendixReduceOnly(record.Appendix)
	if err != nil {
		return model.Order{}, err
	}
	request := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: id,
		Side:         side,
		Type:         enums.TypeUnknown,
		TIF:          enums.TifUnknown,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
		ReduceOnly:   reduceOnly,
	}
	if submitted != nil {
		request = *submitted
		if request.AccountID != "" && request.AccountID != accountID {
			return model.Order{}, fmt.Errorf("nado: archive order account does not match submitted order")
		}
		if request.InstrumentID != id {
			return model.Order{}, fmt.Errorf("nado: archive order instrument does not match submitted order")
		}
		if request.Side != side {
			return model.Order{}, fmt.Errorf("nado: archive order side does not match submitted order")
		}
		if request.Quantity.IsPositive() && !request.Quantity.Equal(qty) {
			return model.Order{}, fmt.Errorf("nado: archive order quantity %s does not match submitted quantity %s", qty, request.Quantity)
		}
	}
	status := nadoArchiveOrderStatus(filled, qty, request.TIF, knownTerminal)
	createdAt := timeFromString(record.FirstFillTimestamp)
	updatedAt := timeFromString(record.LastFillTimestamp)
	if updatedAt.IsZero() {
		return model.Order{}, fmt.Errorf("nado: archive order last fill timestamp %q is invalid", record.LastFillTimestamp)
	}
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	if updatedAt.Before(createdAt) {
		return model.Order{}, fmt.Errorf("nado: archive order last fill timestamp precedes first fill timestamp")
	}
	return model.Order{
		Request:      request,
		VenueOrderID: strings.TrimSpace(record.Digest),
		Status:       status,
		FilledQty:    filled,
		AvgFillPrice: quoteFilled.Abs().Div(filled),
		CreatedAt:    createdAt,
		UpdatedAt:    updatedAt,
	}, nil
}

func nadoArchiveOrderStatus(filled, quantity decimal.Decimal, tif enums.TimeInForce, knownTerminal enums.OrderStatus) enums.OrderStatus {
	if filled.Equal(quantity) {
		return enums.StatusFilled
	}
	if tif == enums.TifIOC || tif == enums.TifFOK {
		return enums.StatusCanceled
	}
	switch knownTerminal {
	case enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return knownTerminal
	default:
		return enums.StatusUnknown
	}
}

func fillFromNado(fill sdk.Fill, id model.InstrumentID, accountID, feeCurrency string) (model.Fill, error) {
	if strings.TrimSpace(fill.OrderDigest) == "" {
		return model.Fill{}, fmt.Errorf("nado: fill order digest is required")
	}
	if strings.TrimSpace(fill.SubmissionIdx) == "" {
		return model.Fill{}, fmt.Errorf("nado: fill submission index is required")
	}
	price, err := parseX18Required(fill.Price, "fill price")
	if err != nil {
		return model.Fill{}, err
	}
	if !price.IsPositive() {
		return model.Fill{}, fmt.Errorf("nado: fill price must be positive")
	}
	qty, err := parseX18Required(fill.FilledQty, "fill quantity")
	if err != nil {
		return model.Fill{}, err
	}
	if !qty.IsPositive() {
		return model.Fill{}, fmt.Errorf("nado: fill quantity must be positive")
	}
	fee, err := parseX18Required(fill.Fee, "fill fee")
	if err != nil {
		return model.Fill{}, err
	}
	ts := timeFromString(fill.Timestamp)
	if ts.IsZero() {
		return model.Fill{}, fmt.Errorf("nado: fill timestamp %q is invalid", fill.Timestamp)
	}
	side := enums.SideSell
	if fill.IsBid {
		side = enums.SideBuy
	}
	liq := enums.LiqMaker
	if fill.IsTaker {
		liq = enums.LiqTaker
	}
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: fill.OrderDigest,
		TradeID:      fill.SubmissionIdx,
		Side:         side,
		Liquidity:    liq,
		Price:        price,
		Quantity:     qty,
		Fee:          fee,
		FeeCurrency:  feeCurrency,
		Timestamp:    ts,
	}, nil
}

func feeFromX18(value string) (decimal.Decimal, error) {
	return parseX18Required(value, "fee")
}

func parseX18Required(value, field string) (decimal.Decimal, error) {
	d, err := parseDecimalRequired(value, field)
	if err != nil {
		return decimal.Zero, err
	}
	return d.Shift(-18), nil
}

func parseDecimalRequired(value, field string) (decimal.Decimal, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return decimal.Zero, fmt.Errorf("nado: %s is required", field)
	}
	d, err := decimal.NewFromString(value)
	if err != nil {
		return decimal.Zero, fmt.Errorf("nado: invalid %s %q: %w", field, value, err)
	}
	return d, nil
}

func decimalPlaces(step decimal.Decimal) int {
	if !step.IsPositive() {
		return 0
	}
	text := step.String()
	if _, frac, ok := strings.Cut(text, "."); ok {
		return len(frac)
	}
	return 0
}

func exactStepString(value, step decimal.Decimal, field string) (string, error) {
	if !value.IsPositive() {
		return "", fmt.Errorf("nado: %s must be positive", field)
	}
	if !step.IsPositive() {
		return "", fmt.Errorf("nado: %s increment is unavailable", field)
	}
	if !value.Mod(step).IsZero() {
		return "", fmt.Errorf("nado: %s %s is not an exact multiple of %s", field, value, step)
	}
	return value.String(), nil
}

func timeFromMillis(value int64) time.Time {
	if value <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(value)
}

func timeFromString(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	if millis, err := strconv.ParseInt(value, 10, 64); err == nil {
		if millis < 1_000_000_000 {
			return time.Time{}
		}
		switch {
		case millis >= 1_000_000_000_000_000_000:
			return time.Unix(0, millis)
		case millis >= 1_000_000_000_000_000:
			return time.UnixMicro(millis)
		case millis >= 1_000_000_000_000:
			return time.UnixMilli(millis)
		default:
			return time.Unix(millis, 0)
		}
	}
	if ts, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return ts
	}
	return time.Time{}
}

func appendixReduceOnly(appendix string) (bool, error) {
	appendix = strings.TrimSpace(appendix)
	if appendix == "" {
		return false, fmt.Errorf("nado: order appendix is required")
	}
	base := 10
	value := appendix
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		base = 16
		value = value[2:]
	}
	parsed, ok := new(big.Int).SetString(value, base)
	if !ok || parsed.Sign() < 0 {
		return false, fmt.Errorf("nado: invalid order appendix %q", appendix)
	}
	return parsed.Bit(sdk.AppendixOffsetReduceOnly) == 1, nil
}
