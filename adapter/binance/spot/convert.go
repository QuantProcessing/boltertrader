// Package spot is the Binance Spot adapter: it implements the venue-neutral
// core/contract interfaces by wrapping sdk/binance/spot and translating between
// Binance's native representation and the domain model.
package spot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/shopspring/decimal"
)

const venueName = "BINANCE"

func sideToBinance(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "BUY", nil
	case enums.SideSell:
		return "SELL", nil
	default:
		return "", fmt.Errorf("binance spot: unsupported side %v: %w", s, errs.ErrNotSupported)
	}
}

func sideFromBinance(s string) enums.OrderSide {
	switch strings.ToUpper(s) {
	case "BUY":
		return enums.SideBuy
	case "SELL":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToBinance(t enums.OrderType, tif enums.TimeInForce) (string, error) {
	if t == enums.TypeLimit && tif == enums.TifGTX {
		return "LIMIT_MAKER", nil
	}
	switch t {
	case enums.TypeMarket:
		return "MARKET", nil
	case enums.TypeLimit:
		return "LIMIT", nil
	case enums.TypeStopMarket:
		return "STOP_LOSS", nil
	case enums.TypeStopLimit:
		return "STOP_LOSS_LIMIT", nil
	case enums.TypeMarketIfTouched:
		return "TAKE_PROFIT", nil
	case enums.TypeLimitIfTouched:
		return "TAKE_PROFIT_LIMIT", nil
	default:
		return "", fmt.Errorf("binance spot: unsupported order type %v: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromBinance(s string) enums.OrderType {
	switch strings.ToUpper(s) {
	case "MARKET":
		return enums.TypeMarket
	case "LIMIT", "LIMIT_MAKER":
		return enums.TypeLimit
	case "STOP_LOSS":
		return enums.TypeStopMarket
	case "STOP_LOSS_LIMIT":
		return enums.TypeStopLimit
	case "TAKE_PROFIT":
		return enums.TypeMarketIfTouched
	case "TAKE_PROFIT_LIMIT":
		return enums.TypeLimitIfTouched
	default:
		return enums.TypeUnknown
	}
}

func tifToBinance(t enums.TimeInForce) (string, error) {
	switch t {
	case enums.TifGTC:
		return "GTC", nil
	case enums.TifIOC:
		return "IOC", nil
	case enums.TifFOK:
		return "FOK", nil
	default:
		return "", fmt.Errorf("binance spot: unsupported TIF %v: %w", t, errs.ErrNotSupported)
	}
}

func tifFromBinance(s string) enums.TimeInForce {
	switch strings.ToUpper(s) {
	case "GTC":
		return enums.TifGTC
	case "IOC":
		return enums.TifIOC
	case "FOK":
		return enums.TifFOK
	default:
		return enums.TifUnknown
	}
}

func statusFromBinance(s string) enums.OrderStatus {
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

func dec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func typeNeedsTIF(t enums.OrderType, nativeType string) bool {
	if strings.EqualFold(nativeType, "LIMIT_MAKER") {
		return false
	}
	switch t {
	case enums.TypeLimit, enums.TypeStopLimit, enums.TypeLimitIfTouched:
		return true
	default:
		return false
	}
}
