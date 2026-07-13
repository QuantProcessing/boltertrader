package perp

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

func sideToAster(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "BUY", nil
	case enums.SideSell:
		return "SELL", nil
	default:
		return "", fmt.Errorf("aster perp: unsupported side %s: %w", s, errs.ErrNotSupported)
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
		return errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrAuthFailed)
	case venueErr.StatusCode() == http.StatusTooManyRequests:
		return errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrRateLimited)
	case venueErr.Code() == -1121:
		return errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrSymbolNotFound)
	case venueErr.Code() == -2011 || venueErr.Code() == -2013:
		return errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrOrderNotFound)
	case venueErr.Code() == -1013:
		return errs.NewExchangeError(VenueName, strconv.Itoa(venueErr.Code()), venueErr.Message(), errs.ErrInvalidPrecision)
	default:
		return err
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

func orderTypeToAster(t enums.OrderType, _ enums.TimeInForce) (sdkperp.OrderType, error) {
	switch t {
	case enums.TypeMarket:
		return sdkperp.OrderType_MARKET, nil
	case enums.TypeLimit:
		return sdkperp.OrderType_LIMIT, nil
	default:
		return "", fmt.Errorf("aster perp: unsupported order type %s: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromAster(s string) enums.OrderType {
	switch strings.ToUpper(s) {
	case "MARKET":
		return enums.TypeMarket
	case "LIMIT":
		return enums.TypeLimit
	default:
		return enums.TypeUnknown
	}
}

func tifToAster(t enums.TimeInForce) (sdkperp.TimeInForce, error) {
	switch t {
	case enums.TifUnknown, enums.TifGTC:
		return sdkperp.TimeInForce_GTC, nil
	case enums.TifIOC:
		return sdkperp.TimeInForce_IOC, nil
	case enums.TifFOK:
		return sdkperp.TimeInForce_FOK, nil
	case enums.TifGTX:
		return sdkperp.TimeInForce_GTX, nil
	default:
		return "", fmt.Errorf("aster perp: unsupported TIF %s: %w", t, errs.ErrNotSupported)
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

func positionSideToAster(side enums.PositionSide) (string, error) {
	switch side {
	case enums.PosNet:
		return "BOTH", nil
	default:
		return "", fmt.Errorf("aster perp: first-phase one-way mode only supports net position side, got %s: %w", side, errs.ErrNotSupported)
	}
}

func validateOrderRequest(req model.OrderRequest, inst *model.Instrument) error {
	if req.Venue != nil {
		return fmt.Errorf("aster perp: venue-specific order options are not supported: %w", errs.ErrNotSupported)
	}
	if inst == nil || inst.ID.Kind != enums.KindPerp || req.InstrumentID != inst.ID || !strings.EqualFold(inst.Settle, "USDT") || inst.PositionMode != model.NetOnly {
		return fmt.Errorf("aster perp: unsupported instrument: %w", errs.ErrNotSupported)
	}
	if req.PositionSide != enums.PosNet {
		return fmt.Errorf("aster perp: first-phase one-way mode only supports net position side: %w", errs.ErrNotSupported)
	}
	if !req.Quantity.IsPositive() {
		return fmt.Errorf("aster perp: quantity must be positive")
	}
	if !decimalMultiple(req.Quantity, inst.SizeStep) {
		return fmt.Errorf("aster perp: quantity %s is not a multiple of size step %s: %w", req.Quantity, inst.SizeStep, errs.ErrInvalidPrecision)
	}
	if inst.MinQty.IsPositive() && req.Quantity.LessThan(inst.MinQty) {
		return fmt.Errorf("aster perp: quantity %s is below minimum %s: %w", req.Quantity, inst.MinQty, errs.ErrMinQuantity)
	}
	switch req.Type {
	case enums.TypeLimit:
		if !req.Price.IsPositive() {
			return fmt.Errorf("aster perp: limit price must be positive")
		}
		if !decimalMultiple(req.Price, inst.PriceTick) {
			return fmt.Errorf("aster perp: price %s is not a multiple of tick %s: %w", req.Price, inst.PriceTick, errs.ErrInvalidPrecision)
		}
		if inst.MinNotional.IsPositive() && req.Price.Mul(req.Quantity).LessThan(inst.MinNotional) {
			return fmt.Errorf("aster perp: notional is below minimum %s: %w", inst.MinNotional, errs.ErrMinNotional)
		}
		if req.TIF == enums.TifUnknown {
			req.TIF = enums.TifGTC
		}
		if _, err := tifToAster(req.TIF); err != nil {
			return err
		}
	case enums.TypeMarket:
		if req.TIF != enums.TifUnknown {
			return fmt.Errorf("aster perp: market orders do not support TIF %s: %w", req.TIF, errs.ErrNotSupported)
		}
		if !req.Price.IsZero() {
			return fmt.Errorf("aster perp: market orders do not support limit price: %w", errs.ErrNotSupported)
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

func orderRequestToAster(req model.OrderRequest, inst *model.Instrument) (sdkperp.PlaceOrderParams, error) {
	if err := validateOrderRequest(req, inst); err != nil {
		return sdkperp.PlaceOrderParams{}, err
	}
	side, err := sideToAster(req.Side)
	if err != nil {
		return sdkperp.PlaceOrderParams{}, err
	}
	orderType, err := orderTypeToAster(req.Type, req.TIF)
	if err != nil {
		return sdkperp.PlaceOrderParams{}, err
	}
	tif, err := tifToAster(req.TIF)
	if err != nil {
		return sdkperp.PlaceOrderParams{}, err
	}
	posSide, err := positionSideToAster(req.PositionSide)
	if err != nil {
		return sdkperp.PlaceOrderParams{}, err
	}
	p := sdkperp.PlaceOrderParams{Symbol: inst.VenueSymbol, Side: side, PositionSide: posSide, Type: orderType, TimeInForce: tif, Quantity: decimalStringOrEmpty(req.Quantity), Price: decimalStringOrEmpty(req.Price), NewClientOrderID: req.ClientID, ReduceOnly: req.ReduceOnly}
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

func orderFromResponse(r *sdkperp.OrderResponse, req model.OrderRequest, accountID string) model.Order {
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
	req.ReduceOnly = r.ReduceOnly
	return model.Order{Request: req, VenueOrderID: strconv.FormatInt(r.OrderID, 10), Status: statusFromAster(r.Status), FilledQty: firstNonZero(dec(r.ExecutedQty), dec(r.CumQty)), AvgFillPrice: firstNonZero(dec(r.AvgPrice), avgFillPrice(firstNonZero(dec(r.ExecutedQty), dec(r.CumQty)), dec(r.CumQuote))), UpdatedAt: timeFromMillis(r.UpdateTime)}
}

func validateOrderResponseDecimals(r *sdkperp.OrderResponse) error {
	if r == nil {
		return fmt.Errorf("aster perp: order response is required")
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
			return fmt.Errorf("aster perp: order response: %w", err)
		}
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

func fillFromTrade(t sdkperp.Trade, id model.InstrumentID, accountID, clientID string) model.Fill {
	return model.Fill{AccountID: accountID, InstrumentID: id, VenueOrderID: strconv.FormatInt(t.OrderID, 10), ClientID: clientID, TradeID: strconv.FormatInt(t.ID, 10), Side: sideFromAster(t.Side), Liquidity: liquidityFromMaker(t.Maker), Price: dec(t.Price), Quantity: dec(t.Qty), Fee: dec(t.Commission), FeeCurrency: t.CommissionAsset, Timestamp: timeFromMillis(t.Time)}
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
