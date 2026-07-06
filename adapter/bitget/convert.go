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
		out.PosSide = bitgetPerpPosSide(req.Side, req.ReduceOnly)
		if req.ReduceOnly {
			out.TradeSide = "close"
			out.ReduceOnly = ""
		}
	}
	return out, nil
}

func bitgetPerpPosSide(side enums.OrderSide, reduceOnly bool) string {
	switch {
	case reduceOnly && side == enums.SideBuy:
		return "short"
	case reduceOnly && side == enums.SideSell:
		return "long"
	case side == enums.SideSell:
		return "short"
	default:
		return "long"
	}
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

func orderFromBitgetRecord(record bitgetsdk.OrderRecord, id model.InstrumentID, accountID string) model.Order {
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
		PositionSide: positionSideFromBitget(firstNonEmpty(record.PosSide, record.HoldSide)),
	}
	return model.Order{
		Request:      req,
		VenueOrderID: record.OrderID,
		Status:       statusFromBitget(record.OrderStatus),
		FilledQty:    firstNonZero(dec(record.FilledQty), dec(record.FilledVolume), dec(record.CumExecQty)),
		AvgFillPrice: dec(record.AvgPrice),
		CreatedAt:    timeFromMillisString(record.CreatedTime),
		UpdatedAt:    timeFromMillisString(record.UpdatedTime),
	}
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

func positionFromBitget(record bitgetsdk.PositionRecord, resolve func(string) model.InstrumentID, accountID string, now time.Time) model.Position {
	qty := firstNonZero(dec(record.Qty), dec(record.Total), dec(record.Size))
	if positionSideFromBitget(firstNonEmpty(record.PosSide, record.HoldSide)) == enums.PosShort {
		qty = qty.Neg()
	}
	return model.Position{
		AccountID:     accountID,
		InstrumentID:  resolve(record.Symbol),
		Side:          positionSideFromBitget(firstNonEmpty(record.PosSide, record.HoldSide)),
		Quantity:      qty,
		EntryPrice:    firstNonZero(dec(record.AvgPrice), dec(record.OpenPriceAvg), dec(record.AverageOpenPrice)),
		MarkPrice:     dec(record.MarkPrice),
		UnrealizedPnL: dec(record.UnrealizedPL),
		Leverage:      dec(record.Leverage),
		UpdatedAt:     firstNonZeroTime(timeFromMillisString(record.UpdatedTime), now),
	}
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
