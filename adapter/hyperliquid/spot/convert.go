package spot

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

const venueName = "HYPERLIQUID"

func sideToHL(s enums.OrderSide) (bool, error) {
	switch s {
	case enums.SideBuy:
		return true, nil
	case enums.SideSell:
		return false, nil
	default:
		return false, fmt.Errorf("hyperliquid spot: unsupported side %v: %w", s, errs.ErrNotSupported)
	}
}

func sideFromHL(s string) enums.OrderSide {
	switch strings.ToUpper(s) {
	case "B", "BUY":
		return enums.SideBuy
	case "A", "SELL":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func tifToHL(t enums.TimeInForce) (sdk.Tif, error) {
	switch t {
	case enums.TifUnknown, enums.TifGTC:
		return sdk.TifGtc, nil
	case enums.TifIOC:
		return sdk.TifIoc, nil
	case enums.TifFOK:
		return "", fmt.Errorf("hyperliquid spot: FOK is not supported by the venue wire API: %w", errs.ErrNotSupported)
	case enums.TifGTX:
		return sdk.TifAlo, nil
	default:
		return "", fmt.Errorf("hyperliquid spot: unsupported TIF %v: %w", t, errs.ErrNotSupported)
	}
}

func statusFromHL(status string) enums.OrderStatus {
	switch sdk.OrderStatusValue(status) {
	case sdk.StatusOpen:
		return enums.StatusNew
	case sdk.StatusFilled:
		return enums.StatusFilled
	case sdk.StatusTriggered:
		return enums.StatusTriggered
	case sdk.StatusRejected, sdk.StatusTickRejected, sdk.StatusMinTradeNtlRejected,
		sdk.StatusPerpMarginRejected, sdk.StatusReduceOnlyRejected,
		sdk.StatusBadAloPxRejected, sdk.StatusIocCancelRejected,
		sdk.StatusBadTriggerPxRejected, sdk.StatusMarketOrderNoLiquidityRejected,
		sdk.StatusPositionIncreaseAtOpenInterestCapRejected,
		sdk.StatusPositionFlipAtOpenInterestCapRejected,
		sdk.StatusTooAggressiveAtOpenInterestCapRejected,
		sdk.StatusOpenInterestIncreaseRejected,
		sdk.StatusInsufficientSpotBalanceRejected, sdk.StatusOracleRejected,
		sdk.StatusPerpMaxPositionRejected:
		return enums.StatusRejected
	case sdk.StatusCanceled, sdk.StatusMarginCanceled, sdk.StatusVaultWithdrawalCanceled,
		sdk.StatusOpenInterestCapCanceled, sdk.StatusSelfTradeCanceled,
		sdk.StatusReduceOnlyCanceled, sdk.StatusSiblingFilledCanceled,
		sdk.StatusDelistedCanceled, sdk.StatusLiquidatedCanceled, sdk.StatusScheduledCancel:
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

func decimalFloat64(v decimal.Decimal) float64 {
	f, _ := v.Float64()
	return f
}

func parseMillis(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func parseVenueOrderID(raw string) (int64, error) {
	oid, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hyperliquid spot: invalid venue order id %q: %w", raw, err)
	}
	return oid, nil
}

func intervalDuration(interval string) (time.Duration, bool) {
	if interval == "" {
		return 0, false
	}
	unit := interval[len(interval)-1]
	n, err := strconv.Atoi(interval[:len(interval)-1])
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	default:
		return 0, false
	}
}
