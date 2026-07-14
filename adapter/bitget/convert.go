package bitget

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func categoryForInstrument(inst *model.Instrument) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("bitget: instrument required: %w", errs.ErrSymbolNotFound)
	}
	switch inst.ID.Kind {
	case enums.KindSpot:
		return "SPOT", nil
	case enums.KindPerp:
		switch inst.Settle {
		case "USDT":
			return bitgetsdk.ProductTypeUSDTFutures, nil
		case "USDC":
			return bitgetsdk.ProductTypeUSDCFutures, nil
		default:
			return "", fmt.Errorf("bitget: unsupported settlement %q: %w", inst.Settle, errs.ErrNotSupported)
		}
	default:
		return "", fmt.Errorf("bitget: unsupported instrument kind %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
}

func sideToBitget(side enums.OrderSide) (string, error) {
	switch side {
	case enums.SideBuy:
		return "buy", nil
	case enums.SideSell:
		return "sell", nil
	default:
		return "", fmt.Errorf("bitget: unsupported side %s: %w", side, errs.ErrNotSupported)
	}
}

func sideFromBitget(side string) enums.OrderSide {
	switch strings.ToLower(side) {
	case "buy":
		return enums.SideBuy
	case "sell":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToBitget(t enums.OrderType) (string, error) {
	switch t {
	case enums.TypeMarket:
		return "market", nil
	case enums.TypeLimit:
		return "limit", nil
	default:
		return "", fmt.Errorf("bitget: unsupported order type %s: %w", t, errs.ErrNotSupported)
	}
}

func orderTypeFromBitget(value string) enums.OrderType {
	switch strings.ToLower(value) {
	case "market":
		return enums.TypeMarket
	case "limit":
		return enums.TypeLimit
	default:
		return enums.TypeUnknown
	}
}

func tifToBitget(tif enums.TimeInForce) (string, error) {
	switch tif {
	case enums.TifUnknown, enums.TifGTC:
		return "gtc", nil
	case enums.TifIOC:
		return "ioc", nil
	case enums.TifFOK:
		return "fok", nil
	case enums.TifGTX:
		return "post_only", nil
	default:
		return "", fmt.Errorf("bitget: unsupported TIF %s: %w", tif, errs.ErrNotSupported)
	}
}

func tifFromBitget(value string) enums.TimeInForce {
	switch strings.ToLower(value) {
	case "gtc", "normal":
		return enums.TifGTC
	case "ioc":
		return enums.TifIOC
	case "fok":
		return enums.TifFOK
	case "post_only":
		return enums.TifGTX
	default:
		return enums.TifUnknown
	}
}

func statusFromBitget(value string) enums.OrderStatus {
	switch strings.ToLower(value) {
	case "new", "live", "init":
		return enums.StatusNew
	case "partially_filled", "partial-fill":
		return enums.StatusPartiallyFilled
	case "filled", "full-fill":
		return enums.StatusFilled
	case "canceled", "cancelled":
		return enums.StatusCanceled
	case "rejected":
		return enums.StatusRejected
	case "expired":
		return enums.StatusExpired
	default:
		return enums.StatusUnknown
	}
}

func positionSideFromBitget(side string) enums.PositionSide {
	switch strings.ToLower(side) {
	case "long":
		return enums.PosLong
	case "short":
		return enums.PosShort
	default:
		return enums.PosNet
	}
}

func positionSideFromBitgetMode(kind enums.InstrumentKind, holdMode, side string) (enums.PositionSide, error) {
	if kind == enums.KindSpot {
		return enums.PosNet, nil
	}
	if kind != enums.KindPerp {
		return enums.PosNet, fmt.Errorf("bitget: unsupported instrument kind %s for position mode: %w", kind, errs.ErrNotSupported)
	}
	switch strings.ToLower(strings.TrimSpace(holdMode)) {
	case "one_way_mode", "single_hold":
		return enums.PosNet, nil
	case "hedge_mode", "double_hold":
		positionSide := positionSideFromBitget(side)
		if positionSide == enums.PosNet {
			return enums.PosNet, fmt.Errorf("bitget: hedge position direction %q is invalid", side)
		}
		return positionSide, nil
	default:
		return enums.PosNet, fmt.Errorf("bitget: derivative hold mode %q is invalid", holdMode)
	}
}

func orderRequestToBitget(req model.OrderRequest, inst *model.Instrument) (bitgetsdk.PlaceOrderRequest, error) {
	category, err := categoryForInstrument(inst)
	if err != nil {
		return bitgetsdk.PlaceOrderRequest{}, err
	}
	side, err := sideToBitget(req.Side)
	if err != nil {
		return bitgetsdk.PlaceOrderRequest{}, err
	}
	orderType, err := orderTypeToBitget(req.Type)
	if err != nil {
		return bitgetsdk.PlaceOrderRequest{}, err
	}
	tif := ""
	if req.Type == enums.TypeLimit {
		tif, err = tifToBitget(req.TIF)
		if err != nil {
			return bitgetsdk.PlaceOrderRequest{}, err
		}
	}
	reduceOnly := "no"
	if req.ReduceOnly {
		reduceOnly = "yes"
	}
	out := bitgetsdk.PlaceOrderRequest{
		Category:    category,
		Symbol:      inst.VenueSymbol,
		Side:        side,
		OrderType:   orderType,
		TimeInForce: tif,
		Qty:         req.Quantity.String(),
		Price:       decimalStringOrEmpty(req.Price),
		ClientOID:   req.ClientID,
		ReduceOnly:  reduceOnly,
	}
	if inst.ID.Kind == enums.KindPerp {
		out.MarginMode = "crossed"
		out.MarginCoin = inst.Settle
		switch req.PositionSide {
		case enums.PosNet:
			// One-way mode derives direction from side and uses reduceOnly for
			// closes. Sending hedge-only fields makes the returned holdSide look
			// authoritative even though the account is netted.
		case enums.PosLong:
			if req.ReduceOnly && req.Side != enums.SideSell {
				return bitgetsdk.PlaceOrderRequest{}, fmt.Errorf("bitget: reduce-only long-leg order must sell: %w", errs.ErrNotSupported)
			}
			out.PosSide = "long"
			out.ReduceOnly = ""
		case enums.PosShort:
			if req.ReduceOnly && req.Side != enums.SideBuy {
				return bitgetsdk.PlaceOrderRequest{}, fmt.Errorf("bitget: reduce-only short-leg order must buy: %w", errs.ErrNotSupported)
			}
			out.PosSide = "short"
			out.ReduceOnly = ""
		default:
			return bitgetsdk.PlaceOrderRequest{}, fmt.Errorf("bitget: unsupported position side %s: %w", req.PositionSide, errs.ErrNotSupported)
		}
	}
	return out, nil
}

func orderFromBitgetAction(resp *bitgetsdk.PlaceOrderResponse, req model.OrderRequest, now time.Time) model.Order {
	if req.AccountID == "" {
		req.AccountID = AccountIDUnified
	}
	if req.ClientID == "" {
		req.ClientID = resp.ClientOID
	}
	return model.Order{Request: req, VenueOrderID: resp.OrderID, Status: enums.StatusNew, CreatedAt: now, UpdatedAt: now}
}

func orderFromBitgetRecord(record bitgetsdk.OrderRecord, id model.InstrumentID, accountID string) (model.Order, error) {
	positionSide, err := positionSideFromBitgetMode(id.Kind, record.HoldMode, firstNonEmpty(record.PosSide, record.HoldSide))
	if err != nil {
		return model.Order{}, err
	}
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: id,
		ClientID:     record.ClientOID,
		Side:         sideFromBitget(record.Side),
		Type:         orderTypeFromBitget(record.OrderType),
		TIF:          tifFromBitget(record.TimeInForce),
		Quantity:     firstNonZero(dec(record.Qty), dec(record.Amount)),
		Price:        dec(record.Price),
		ReduceOnly:   strings.EqualFold(record.ReduceOnly, "yes"),
		PositionSide: positionSide,
	}
	return model.Order{
		Request:      req,
		VenueOrderID: record.OrderID,
		Status:       statusFromBitget(record.OrderStatus),
		FilledQty:    firstNonZero(dec(record.FilledQty), dec(record.FilledVolume), dec(record.CumExecQty)),
		AvgFillPrice: dec(record.AvgPrice),
		CreatedAt:    timeFromMillisString(record.CreatedTime),
		UpdatedAt:    timeFromMillisString(record.UpdatedTime),
	}, nil
}

func fillFromBitget(record bitgetsdk.FillRecord, id model.InstrumentID, accountID string) model.Fill {
	fee, feeCoin := bitgetFee(record.FeeDetail)
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: record.OrderID,
		ClientID:     record.ClientOID,
		TradeID:      firstNonEmpty(record.ExecID, record.ExecLinkID),
		Side:         sideFromBitget(record.Side),
		Liquidity:    enums.LiqUnknown,
		Price:        dec(record.ExecPrice),
		Quantity:     dec(record.ExecQty),
		Fee:          fee,
		FeeCurrency:  feeCoin,
		Timestamp:    firstNonZeroTime(timeFromMillisString(record.ExecTime), timeFromMillisString(record.CreatedTime)),
	}
}

func positionFromBitget(record bitgetsdk.PositionRecord, resolve func(string) model.InstrumentID, accountID string, now time.Time) (model.Position, error) {
	id := resolve(record.Symbol)
	rawSide := firstNonEmpty(record.PosSide, record.HoldSide)
	positionSide, err := positionSideFromBitgetMode(id.Kind, record.HoldMode, rawSide)
	if err != nil {
		return model.Position{}, err
	}
	qty := firstNonZero(dec(record.Qty), dec(record.Total), dec(record.Size)).Abs()
	if id.Kind == enums.KindPerp {
		switch positionSideFromBitget(rawSide) {
		case enums.PosLong:
		case enums.PosShort:
			qty = qty.Neg()
		default:
			return model.Position{}, fmt.Errorf("bitget: derivative position direction %q is invalid", rawSide)
		}
	}
	return model.Position{
		AccountID:     accountID,
		InstrumentID:  id,
		Side:          positionSide,
		Quantity:      qty,
		EntryPrice:    firstNonZero(dec(record.AvgPrice), dec(record.OpenPriceAvg), dec(record.AverageOpenPrice)),
		MarkPrice:     dec(record.MarkPrice),
		UnrealizedPnL: dec(firstNonEmpty(record.UnrealisedPnl, record.UnrealizedPL)),
		Leverage:      dec(record.Leverage),
		UpdatedAt:     firstNonZeroTime(timeFromMillisString(record.UpdatedTime), now),
	}, nil
}

func bitgetFee(details []bitgetsdk.FeeDetail) (decimal.Decimal, string) {
	if len(details) == 0 {
		return decimal.Zero, ""
	}
	return dec(details[0].Fee), details[0].FeeCoin
}

func timeFromMillisString(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func bitgetKinds(scope []enums.InstrumentKind) []enums.InstrumentKind {
	if len(scope) > 0 {
		return append([]enums.InstrumentKind(nil), scope...)
	}
	return []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}
}

func decimalStringOrEmpty(value decimal.Decimal) string {
	if value.IsZero() {
		return ""
	}
	return value.String()
}
