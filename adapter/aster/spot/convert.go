package spot

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

func sideToAster(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "BUY", nil
	case enums.SideSell:
		return "SELL", nil
	default:
		return "", fmt.Errorf("aster spot: unsupported side %s: %w", s, errs.ErrNotSupported)
	}
}

func mapAsterError(err error) error {
	if err == nil {
		return nil
	}
	var venueErr *astercommon.VenueError
	if !errors.As(err, &venueErr) {
		return err
	}
	switch {
	case venueErr.StatusCode() == http.StatusUnauthorized || venueErr.StatusCode() == http.StatusForbidden:
		return errors.Join(errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrAuthFailed), err)
	case venueErr.StatusCode() == http.StatusTooManyRequests:
		return errors.Join(errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrRateLimited), err)
	case venueErr.Code() == -1121:
		return errors.Join(errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrSymbolNotFound), err)
	case venueErr.Code() == -2011 || venueErr.Code() == -2013:
		return errors.Join(errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrOrderNotFound), err)
	case venueErr.Code() == -1013:
		return errors.Join(errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrInvalidPrecision), err)
	default:
		return err
	}
}

// mapAsterCommandError marks only documented, structured 4xx application
// codes as definitive venue command rejections. Transport failures, malformed
// envelopes, authentication/rate limits, and every 5xx remain ambiguous.
func mapAsterCommandError(err error) error {
	mapped := mapAsterError(err)
	var venueErr *astercommon.VenueError
	if !errors.As(err, &venueErr) || venueErr.StatusCode() < 400 || venueErr.StatusCode() >= 500 {
		return mapped
	}
	switch venueErr.StatusCode() {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return mapped
	}
	switch venueErr.Code() {
	case -1013, -1111, -1121, -2010, -2011, -2013, -2019, -2021, -2022:
		return errors.Join(contract.ErrVenueRejected, mapped)
	default:
		return mapped
	}
}

func sideFromAster(s string) enums.OrderSide {
	switch strings.ToUpper(s) {
	case "BUY":
		return enums.SideBuy
	case "SELL":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToAster(t enums.OrderType, tif enums.TimeInForce) (string, error) {
	switch t {
	case enums.TypeMarket:
		return "MARKET", nil
	case enums.TypeLimit:
		return "LIMIT", nil
	default:
		return "", fmt.Errorf("aster spot: unsupported order type %s: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromAster(s string) enums.OrderType {
	switch strings.ToUpper(s) {
	case "MARKET":
		return enums.TypeMarket
	case "LIMIT", "LIMIT_MAKER":
		return enums.TypeLimit
	default:
		return enums.TypeUnknown
	}
}

func tifToAster(t enums.TimeInForce) (string, error) {
	switch t {
	case enums.TifUnknown, enums.TifGTC:
		return "GTC", nil
	case enums.TifIOC:
		return "IOC", nil
	case enums.TifFOK:
		return "FOK", nil
	case enums.TifGTX:
		return "GTX", nil
	default:
		return "", fmt.Errorf("aster spot: unsupported TIF %s: %w", t, errs.ErrNotSupported)
	}
}

func tifFromAster(s string) enums.TimeInForce {
	switch strings.ToUpper(s) {
	case "GTC":
		return enums.TifGTC
	case "IOC":
		return enums.TifIOC
	case "FOK":
		return enums.TifFOK
	case "GTX":
		return enums.TifGTX
	default:
		return enums.TifUnknown
	}
}

func validateOrderRequest(req model.OrderRequest, inst *model.Instrument) error {
	if req.ReduceOnly {
		return fmt.Errorf("aster spot: reduce-only orders are not supported: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return fmt.Errorf("aster spot: position side is not supported: %w", errs.ErrNotSupported)
	}
	if inst == nil || inst.ID.Kind != enums.KindSpot || req.InstrumentID != inst.ID {
		return fmt.Errorf("aster spot: unsupported instrument: %w", errs.ErrNotSupported)
	}
	if !req.Quantity.IsPositive() {
		return fmt.Errorf("aster spot: quantity must be positive")
	}
	if !decimalMultiple(req.Quantity, inst.SizeStep) {
		return fmt.Errorf("aster spot: quantity %s is not a multiple of size step %s: %w", req.Quantity, inst.SizeStep, errs.ErrInvalidPrecision)
	}
	if inst.MinQty.IsPositive() && req.Quantity.LessThan(inst.MinQty) {
		return fmt.Errorf("aster spot: quantity %s is below minimum %s: %w", req.Quantity, inst.MinQty, errs.ErrMinQuantity)
	}
	switch req.Type {
	case enums.TypeLimit:
		if !req.Price.IsPositive() {
			return fmt.Errorf("aster spot: limit price must be positive")
		}
		if !decimalMultiple(req.Price, inst.PriceTick) {
			return fmt.Errorf("aster spot: price %s is not a multiple of tick %s: %w", req.Price, inst.PriceTick, errs.ErrInvalidPrecision)
		}
		if inst.MinNotional.IsPositive() && req.Price.Mul(req.Quantity).LessThan(inst.MinNotional) {
			return fmt.Errorf("aster spot: notional is below minimum %s: %w", inst.MinNotional, errs.ErrMinNotional)
		}
		if req.TIF == enums.TifUnknown {
			req.TIF = enums.TifGTC
		}
		if _, err := tifToAster(req.TIF); err != nil {
			return err
		}
	case enums.TypeMarket:
		if req.TIF != enums.TifUnknown {
			return fmt.Errorf("aster spot: market orders do not support TIF %s: %w", req.TIF, errs.ErrNotSupported)
		}
		if !req.Price.IsZero() {
			return fmt.Errorf("aster spot: market orders do not support limit price: %w", errs.ErrNotSupported)
		}
	default:
		if _, err := orderTypeToAster(req.Type, req.TIF); err != nil {
			return err
		}
	}
	if _, err := sideToAster(req.Side); err != nil {
		return err
	}
	return nil
}

func statusFromAster(s string) enums.OrderStatus {
	switch strings.ToUpper(s) {
	case "NEW":
		return enums.StatusNew
	case "PARTIALLY_FILLED":
		return enums.StatusPartiallyFilled
	case "FILLED":
		return enums.StatusFilled
	case "CANCELED":
		return enums.StatusCanceled
	case "REJECTED":
		return enums.StatusRejected
	case "EXPIRED", "EXPIRED_IN_MATCH":
		return enums.StatusExpired
	default:
		return enums.StatusUnknown
	}
}

func orderRequestToAster(req model.OrderRequest, inst *model.Instrument) (sdkspot.PlaceOrderParams, error) {
	if err := validateOrderRequest(req, inst); err != nil {
		return sdkspot.PlaceOrderParams{}, err
	}
	side, err := sideToAster(req.Side)
	if err != nil {
		return sdkspot.PlaceOrderParams{}, err
	}
	orderType, err := orderTypeToAster(req.Type, req.TIF)
	if err != nil {
		return sdkspot.PlaceOrderParams{}, err
	}
	tif, err := tifToAster(req.TIF)
	if err != nil {
		return sdkspot.PlaceOrderParams{}, err
	}
	p := sdkspot.PlaceOrderParams{
		Symbol:           inst.VenueSymbol,
		Side:             side,
		Type:             orderType,
		TimeInForce:      tif,
		Quantity:         decimalStringOrEmpty(req.Quantity),
		Price:            decimalStringOrEmpty(req.Price),
		NewClientOrderID: req.ClientID,
	}
	if req.Type == enums.TypeMarket {
		p.TimeInForce = ""
		p.Price = ""
	}
	return p, nil
}

func decimalMultiple(value, step decimal.Decimal) bool {
	if !step.IsPositive() {
		return false
	}
	return value.Mod(step).IsZero()
}

func orderFromResponse(r *sdkspot.OrderResponse, req model.OrderRequest, accountID string) model.Order {
	if req.AccountID == "" {
		req.AccountID = accountID
	}
	if req.ClientID == "" {
		req.ClientID = r.ClientOrderID
	}
	if req.Side == enums.SideUnknown {
		req.Side = sideFromAster(r.Side)
	}
	if req.Type == enums.TypeUnknown {
		req.Type = orderTypeFromAster(r.Type)
	}
	if req.TIF == enums.TifUnknown {
		req.TIF = tifFromAster(r.TimeInForce)
	}
	if req.Quantity.IsZero() {
		req.Quantity = dec(r.OrigQty)
	}
	if req.Price.IsZero() {
		req.Price = dec(r.Price)
	}
	req.PositionSide = enums.PosNet
	req.ReduceOnly = false
	return model.Order{
		Request:      req,
		VenueOrderID: strconv.FormatInt(r.OrderID, 10),
		Status:       statusFromAster(r.Status),
		FilledQty:    firstNonZero(dec(r.ExecutedQty), dec(r.CumQty)),
		AvgFillPrice: firstNonZero(dec(r.AvgPrice), avgFillPrice(firstNonZero(dec(r.ExecutedQty), dec(r.CumQty)), dec(r.CumQuote))),
		UpdatedAt:    timeFromMillisPtr(firstNonNilInt64(r.UpdateTime, r.Time)),
	}
}

func validateOrderResponseDecimals(r *sdkspot.OrderResponse) error {
	if r == nil {
		return fmt.Errorf("aster spot: order response is required")
	}
	for field, raw := range map[string]string{
		"origQty":     r.OrigQty,
		"price":       r.Price,
		"executedQty": r.ExecutedQty,
		"cumQty":      r.CumQty,
		"cumQuote":    r.CumQuote,
		"avgPrice":    r.AvgPrice,
	} {
		if err := validateSDKDecimal(field, raw); err != nil {
			return fmt.Errorf("aster spot: order response: %w", err)
		}
	}
	return nil
}

func validateSubmitOrderResponseIdentity(r *sdkspot.OrderResponse, req model.OrderRequest, inst *model.Instrument) error {
	if r == nil || inst == nil {
		return fmt.Errorf("aster spot: submit response and instrument are required")
	}
	if strings.TrimSpace(r.Symbol) == "" || r.Symbol != inst.VenueSymbol {
		return fmt.Errorf("aster spot: submit response symbol mismatch: requested %q, got %q", inst.VenueSymbol, r.Symbol)
	}
	if strings.TrimSpace(r.ClientOrderID) == "" || r.ClientOrderID != req.ClientID {
		return fmt.Errorf("aster spot: submit response client order id mismatch: requested %q, got %q", req.ClientID, r.ClientOrderID)
	}
	if r.OrderID <= 0 {
		return fmt.Errorf("aster spot: submit response venue order id must be positive, got %d", r.OrderID)
	}
	return nil
}

func validateSDKDecimal(field, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if _, err := decimal.NewFromString(raw); err != nil {
		return fmt.Errorf("%s malformed %q: %w", field, raw, errs.ErrInvalidPrecision)
	}
	return nil
}

func parseRequiredSDKDecimal(field, raw string) (decimal.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, fmt.Errorf("%s is required: %w", field, errs.ErrInvalidPrecision)
	}
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return decimal.Zero, fmt.Errorf("%s malformed %q: %w", field, raw, errs.ErrInvalidPrecision)
	}
	return value, nil
}

func fillFromTrade(t sdkspot.Trade, id model.InstrumentID, accountID, clientID string) model.Fill {
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: strconv.FormatInt(t.OrderID, 10),
		ClientID:     clientID,
		TradeID:      strconv.FormatInt(t.ID, 10),
		Side:         sideFromAster(t.Side),
		Liquidity:    liquidityFromMaker(t.Maker),
		Price:        dec(t.Price),
		Quantity:     dec(t.Qty),
		Fee:          dec(t.Commission),
		FeeCurrency:  t.CommissionAsset,
		Timestamp:    timeFromMillis(t.Time),
	}
}

func liquidityFromMaker(maker bool) enums.LiquiditySide {
	if maker {
		return enums.LiqMaker
	}
	return enums.LiqTaker
}

func dec(s string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return v
}

func decimalStringOrEmpty(v decimal.Decimal) string {
	if v.IsZero() {
		return ""
	}
	return v.String()
}

func firstNonZero(values ...decimal.Decimal) decimal.Decimal {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return decimal.Zero
}

func avgFillPrice(qty, quote decimal.Decimal) decimal.Decimal {
	if qty.IsZero() {
		return decimal.Zero
	}
	return quote.Div(qty)
}

func timeFromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func timeFromMillisPtr(ms *int64) time.Time {
	if ms == nil {
		return time.Time{}
	}
	return timeFromMillis(*ms)
}

func firstNonNilInt64(values ...*int64) *int64 {
	for _, v := range values {
		if v != nil && *v != 0 {
			return v
		}
	}
	return nil
}
