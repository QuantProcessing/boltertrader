// Package perp is the Binance USD-M perpetual adapter: it implements the
// venue-neutral core/contract interfaces by wrapping sdk/binance/perp and
// translating between Binance's native representation and the domain model.
//
// All Binance-specific knowledge (string enums, raw filter maps, signed
// position amounts, listenKey lifecycle) is absorbed here so the runtime never
// sees it.
package perp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/shopspring/decimal"
)

const venueName = "BINANCE"

// --- Side -------------------------------------------------------------------

func sideToBinance(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "BUY", nil
	case enums.SideSell:
		return "SELL", nil
	default:
		return "", fmt.Errorf("binance: unsupported side %v: %w", s, errs.ErrNotSupported)
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

// --- OrderType --------------------------------------------------------------

func orderTypeToBinance(t enums.OrderType) (string, error) {
	switch t {
	case enums.TypeMarket:
		return "MARKET", nil
	case enums.TypeLimit:
		return "LIMIT", nil
	case enums.TypeStopMarket:
		return "STOP_MARKET", nil
	case enums.TypeStopLimit:
		return "STOP", nil
	case enums.TypeTakeProfitMarket:
		return "TAKE_PROFIT_MARKET", nil
	case enums.TypeTakeProfitLimit:
		return "TAKE_PROFIT", nil
	default:
		return "", fmt.Errorf("binance: unsupported order type %v: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromBinance(s string) enums.OrderType {
	switch strings.ToUpper(s) {
	case "MARKET":
		return enums.TypeMarket
	case "LIMIT":
		return enums.TypeLimit
	case "STOP_MARKET":
		return enums.TypeStopMarket
	case "STOP":
		return enums.TypeStopLimit
	case "TAKE_PROFIT_MARKET":
		return enums.TypeTakeProfitMarket
	case "TAKE_PROFIT":
		return enums.TypeTakeProfitLimit
	default:
		return enums.TypeUnknown
	}
}

// --- TimeInForce ------------------------------------------------------------

func tifToBinance(t enums.TimeInForce) (string, error) {
	switch t {
	case enums.TifGTC:
		return "GTC", nil
	case enums.TifIOC:
		return "IOC", nil
	case enums.TifFOK:
		return "FOK", nil
	case enums.TifGTX:
		return "GTX", nil
	default:
		return "", fmt.Errorf("binance: unsupported TIF %v: %w", t, errs.ErrNotSupported)
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
	case "GTX":
		return enums.TifGTX
	default:
		return enums.TifUnknown
	}
}

// --- OrderStatus ------------------------------------------------------------

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

// --- PositionSide -----------------------------------------------------------

func positionSideToBinance(p enums.PositionSide) string {
	switch p {
	case enums.PosLong:
		return "LONG"
	case enums.PosShort:
		return "SHORT"
	default:
		return "BOTH"
	}
}

func positionSideFromBinance(s string) enums.PositionSide {
	switch strings.ToUpper(s) {
	case "LONG":
		return enums.PosLong
	case "SHORT":
		return enums.PosShort
	default:
		return enums.PosNet
	}
}

// --- decimal helper ---------------------------------------------------------

// dec parses a Binance numeric string, treating "" as zero. A malformed value
// yields zero rather than an error, matching the SDK's lenient string contract.
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

// itoa renders an int64 venue id as a string.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// typeNeedsTIF reports whether a TimeInForce applies to this order type. Binance
// accepts timeInForce only on limit-family orders.
func typeNeedsTIF(t enums.OrderType) bool {
	switch t {
	case enums.TypeLimit, enums.TypeStopLimit, enums.TypeTakeProfitLimit:
		return true
	default:
		return false
	}
}
