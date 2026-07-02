// Package spot is the OKX Spot cash adapter. It implements the venue-neutral
// core/contract interfaces over sdk/okx for SPOT only: no margin, leverage,
// derivative position side, or reduce-only semantics are exposed.
package spot

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/shopspring/decimal"
)

const (
	venueName    = "OKX"
	instTypeSpot = "SPOT"
	spotTdMode   = "cash"
)

func sideToOKX(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "buy", nil
	case enums.SideSell:
		return "sell", nil
	default:
		return "", fmt.Errorf("okx spot: unsupported side %v: %w", s, errs.ErrNotSupported)
	}
}

func sideFromOKX(s string) enums.OrderSide {
	switch strings.ToLower(s) {
	case "buy":
		return enums.SideBuy
	case "sell":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func ordTypeToOKX(t enums.OrderType, tif enums.TimeInForce) (string, error) {
	switch t {
	case enums.TypeMarket:
		return "market", nil
	case enums.TypeLimit:
		switch tif {
		case enums.TifGTC, enums.TifUnknown:
			return "limit", nil
		case enums.TifIOC:
			return "ioc", nil
		case enums.TifFOK:
			return "fok", nil
		case enums.TifGTX:
			return "post_only", nil
		default:
			return "", fmt.Errorf("okx spot: unsupported TIF %v: %w", tif, errs.ErrNotSupported)
		}
	default:
		return "", fmt.Errorf("okx spot: unsupported order type %v: %w", t, errs.ErrNotSupported)
	}
}

func ordTypeFromOKX(s string) (enums.OrderType, enums.TimeInForce) {
	switch strings.ToLower(s) {
	case "market":
		return enums.TypeMarket, enums.TifUnknown
	case "limit":
		return enums.TypeLimit, enums.TifGTC
	case "ioc":
		return enums.TypeLimit, enums.TifIOC
	case "fok":
		return enums.TypeLimit, enums.TifFOK
	case "post_only":
		return enums.TypeLimit, enums.TifGTX
	default:
		return enums.TypeUnknown, enums.TifUnknown
	}
}

func statusFromOKX(s string) enums.OrderStatus {
	switch strings.ToLower(s) {
	case "live":
		return enums.StatusNew
	case "partially_filled":
		return enums.StatusPartiallyFilled
	case "filled":
		return enums.StatusFilled
	case "canceled", "mmp_canceled":
		return enums.StatusCanceled
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

func firstNonZero(values ...decimal.Decimal) decimal.Decimal {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return decimal.Zero
}

func parseMillis(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	return time.UnixMilli(dec(s).IntPart())
}
