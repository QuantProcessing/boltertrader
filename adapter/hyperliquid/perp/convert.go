package perp

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

const venueName = instruments.VenueName

func sideToHL(s enums.OrderSide) (bool, error) {
	switch s {
	case enums.SideBuy:
		return true, nil
	case enums.SideSell:
		return false, nil
	default:
		return false, fmt.Errorf("hyperliquid perp: unsupported side %v: %w", s, errs.ErrNotSupported)
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
		return sdk.TifFok, nil
	case enums.TifGTX:
		return sdk.TifAlo, nil
	default:
		return "", fmt.Errorf("hyperliquid perp: unsupported TIF %v: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeToHL(req model.OrderRequest) (sdkperp.OrderType, error) {
	switch req.Type {
	case enums.TypeLimit:
		tif, err := tifToHL(req.TIF)
		if err != nil {
			return sdkperp.OrderType{}, err
		}
		return sdkperp.OrderType{Limit: &sdkperp.OrderTypeLimit{Tif: tif}}, nil
	case enums.TypeStopMarket, enums.TypeStopLimit, enums.TypeMarketIfTouched, enums.TypeLimitIfTouched:
		if req.TriggerPrice.IsZero() {
			return sdkperp.OrderType{}, fmt.Errorf("hyperliquid perp: trigger order requires TriggerPrice: %w", errs.ErrNotSupported)
		}
		if req.Price.IsZero() {
			return sdkperp.OrderType{}, fmt.Errorf("hyperliquid perp: trigger order requires limit/aggressive Price for Hyperliquid wire order: %w", errs.ErrNotSupported)
		}
		tpsl := sdk.StopLoss
		if req.Type == enums.TypeMarketIfTouched || req.Type == enums.TypeLimitIfTouched {
			tpsl = sdk.TakeProfit
		}
		isMarket := req.Type == enums.TypeStopMarket || req.Type == enums.TypeMarketIfTouched
		triggerPx, _ := req.TriggerPrice.Float64()
		return sdkperp.OrderType{Trigger: &sdkperp.OrderTypeTrigger{IsMarket: isMarket, TriggerPx: triggerPx, Tpsl: tpsl}}, nil
	case enums.TypeMarket:
		if req.Price.IsZero() {
			return sdkperp.OrderType{}, fmt.Errorf("hyperliquid perp: market orders require an explicit aggressive Price safety bound: %w", errs.ErrNotSupported)
		}
		return sdkperp.OrderType{Limit: &sdkperp.OrderTypeLimit{Tif: sdk.TifIoc}}, nil
	default:
		return sdkperp.OrderType{}, fmt.Errorf("hyperliquid perp: unsupported order type %v: %w", req.Type, errs.ErrNotSupported)
	}
}

func statusFromHL(s string) enums.OrderStatus {
	switch sdk.OrderStatusValue(s) {
	case sdk.StatusOpen:
		return enums.StatusNew
	case sdk.StatusFilled:
		return enums.StatusFilled
	case sdk.StatusTriggered:
		return enums.StatusTriggered
	case sdk.StatusRejected, sdk.StatusTickRejected, sdk.StatusMinTradeNtlRejected:
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
		return 0, fmt.Errorf("hyperliquid perp: invalid venue order id %q: %w", raw, err)
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

func normalizeMarginMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "cross":
		return "cross", nil
	case "isolated":
		return "isolated", nil
	default:
		return "", fmt.Errorf("hyperliquid perp: unsupported margin mode %q: %w", mode, errs.ErrNotSupported)
	}
}

func isCrossMarginMode(mode string) bool { return mode != "isolated" }

func leverageInt(leverage decimal.Decimal) (int, error) {
	if !leverage.IsPositive() {
		return 0, fmt.Errorf("hyperliquid perp: leverage must be positive: %w", errs.ErrNotSupported)
	}
	return int(leverage.IntPart()), nil
}
