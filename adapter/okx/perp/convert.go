// Package perp is the OKX perpetual (SWAP) adapter. It implements the
// venue-neutral core/contract interfaces over sdk/okx, absorbing OKX's
// divergences from a typical REST venue:
//
//   - product boundary: first-phase runtime support is USDT-linear SWAP only;
//   - symbol identity: neutral "BTC-USDT" <-> OKX InstId "BTC-USDT-SWAP",
//     plus an integer InstIdCode carried on the instrument;
//   - TimeInForce is FOLDED into ordType ("limit"/"market"/"post_only"/"fok"/
//     "ioc") rather than a separate field — the single hardest mapping;
//   - margin mode (TdMode) is a per-order field (defaults to cross here);
//   - position size is Pos string + PosSide (translated to a signed decimal);
//   - the private websocket requires an op:"login" frame (handled by the SDK).
package perp

import (
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	"github.com/shopspring/decimal"
)

const venueName = "OKX"

// instType / suffix for USDT-margined perpetual swaps.
const (
	instTypeSwap = "SWAP"
	swapSuffix   = "-SWAP"

	defaultDerivativeTdMode = "cross"
	tdModeIsolated          = "isolated"
	usdtSettlement          = "USDT"
)

// neutralToInstID converts a neutral symbol ("BTC-USDT") to an OKX SWAP InstId
// ("BTC-USDT-SWAP").
func neutralToInstID(neutral string) string { return neutral + swapSuffix }

// instIDToNeutral strips the "-SWAP" suffix to recover the neutral symbol.
func instIDToNeutral(instID string) string {
	return strings.TrimSuffix(instID, swapSuffix)
}

func isSupportedUSDTLinearSwapInstID(instID string) bool {
	return strings.HasSuffix(strings.ToUpper(instID), "-"+usdtSettlement+swapSuffix)
}

func isSupportedUSDTLinearSwap(in *okxInstrumentShape) bool {
	if in == nil || in.instType() != instTypeSwap || !isSupportedUSDTLinearSwapInstID(in.instID()) {
		return false
	}
	if quote := in.quoteCcy(); quote != "" && quote != usdtSettlement {
		return false
	}
	if settle := in.settleCcy(); settle != "" && settle != usdtSettlement {
		return false
	}
	return true
}

type okxInstrumentShape struct {
	InstId    string
	InstType  string
	QuoteCcy  string
	SettCcy   string
	SettleCcy string
}

func (s *okxInstrumentShape) instID() string {
	return strings.ToUpper(strings.TrimSpace(s.InstId))
}

func (s *okxInstrumentShape) instType() string {
	return strings.ToUpper(strings.TrimSpace(s.InstType))
}

func (s *okxInstrumentShape) quoteCcy() string {
	return strings.ToUpper(strings.TrimSpace(s.QuoteCcy))
}

func (s *okxInstrumentShape) settleCcy() string {
	settle := strings.TrimSpace(s.SettleCcy)
	if settle == "" {
		settle = strings.TrimSpace(s.SettCcy)
	}
	return strings.ToUpper(settle)
}

func normalizeDerivativeTdMode(mode string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", defaultDerivativeTdMode:
		return defaultDerivativeTdMode, nil
	case tdModeIsolated:
		return tdModeIsolated, nil
	default:
		return "", fmt.Errorf("okx: unsupported derivative tdMode %q: %w", mode, errs.ErrNotSupported)
	}
}

// --- Side -------------------------------------------------------------------

func sideToOKX(s enums.OrderSide) (string, error) {
	switch s {
	case enums.SideBuy:
		return "buy", nil
	case enums.SideSell:
		return "sell", nil
	default:
		return "", fmt.Errorf("okx: unsupported side %v: %w", s, errs.ErrNotSupported)
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

// --- OrderType + TimeInForce (folded into ordType) --------------------------

// ordTypeToOKX folds (OrderType, TimeInForce) into OKX's single ordType field.
// This is the central OKX divergence the abstraction must absorb.
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
			return "", fmt.Errorf("okx: unsupported TIF %v: %w", tif, errs.ErrNotSupported)
		}
	default:
		// OKX trigger families are separate algo endpoints, out of the portable
		// contract for v1.
		return "", fmt.Errorf("okx: unsupported order type %v: %w", t, errs.ErrNotSupported)
	}
}

// ordTypeFromOKX recovers (OrderType, TimeInForce) from OKX's ordType.
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

// --- OrderStatus ------------------------------------------------------------

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

// --- PositionSide -----------------------------------------------------------

func positionSideToOKX(p enums.PositionSide) string {
	switch p {
	case enums.PosLong:
		return "long"
	case enums.PosShort:
		return "short"
	default:
		return "net"
	}
}

func positionSideFromOKX(s string) enums.PositionSide {
	switch strings.ToLower(s) {
	case "long":
		return enums.PosLong
	case "short":
		return enums.PosShort
	default:
		return enums.PosNet
	}
}

// --- LiquiditySide (from execType) ------------------------------------------

func liquidityFromExecType(execType string) enums.LiquiditySide {
	switch strings.ToUpper(execType) {
	case "M":
		return enums.LiqMaker
	case "T":
		return enums.LiqTaker
	default:
		return enums.LiqUnknown
	}
}

// --- decimal helper ---------------------------------------------------------

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
