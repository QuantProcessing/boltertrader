package bybit

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

func categoryForInstrument(inst *model.Instrument) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("bybit: instrument required: %w", errs.ErrSymbolNotFound)
	}
	switch inst.ID.Kind {
	case enums.KindSpot:
		return "spot", nil
	case enums.KindPerp:
		return "linear", nil
	default:
		return "", fmt.Errorf("bybit: unsupported instrument kind %s: %w", inst.ID.Kind, errs.ErrNotSupported)
	}
}

func sideToBybit(side enums.OrderSide) (string, error) {
	switch side {
	case enums.SideBuy:
		return "Buy", nil
	case enums.SideSell:
		return "Sell", nil
	default:
		return "", fmt.Errorf("bybit: unsupported side %s: %w", side, errs.ErrNotSupported)
	}
}

func sideFromBybit(side string) enums.OrderSide {
	switch strings.ToLower(side) {
	case "buy":
		return enums.SideBuy
	case "sell":
		return enums.SideSell
	default:
		return enums.SideUnknown
	}
}

func orderTypeToBybit(req model.OrderRequest) (string, error) {
	switch req.Type {
	case enums.TypeMarket:
		return "Market", nil
	case enums.TypeLimit:
		return "Limit", nil
	default:
		return "", fmt.Errorf("bybit: unsupported order type %s: %w", req.Type, errs.ErrNotSupported)
	}
}

func orderTypeFromBybit(value string) enums.OrderType {
	switch strings.ToLower(value) {
	case "market":
		return enums.TypeMarket
	case "limit":
		return enums.TypeLimit
	default:
		return enums.TypeUnknown
	}
}

func tifToBybit(tif enums.TimeInForce) (string, error) {
	switch tif {
	case enums.TifUnknown, enums.TifGTC:
		return "GTC", nil
	case enums.TifIOC:
		return "IOC", nil
	case enums.TifFOK:
		return "FOK", nil
	case enums.TifGTX:
		return "PostOnly", nil
	default:
		return "", fmt.Errorf("bybit: unsupported TIF %s: %w", tif, errs.ErrNotSupported)
	}
}

func tifFromBybit(value string) enums.TimeInForce {
	switch strings.ToLower(value) {
	case "gtc":
		return enums.TifGTC
	case "ioc":
		return enums.TifIOC
	case "fok":
		return enums.TifFOK
	case "postonly":
		return enums.TifGTX
	default:
		return enums.TifUnknown
	}
}

func statusFromBybit(value string) enums.OrderStatus {
	switch strings.ToLower(value) {
	case "new", "created", "untriggered", "triggered":
		return enums.StatusNew
	case "partiallyfilled":
		return enums.StatusPartiallyFilled
	case "filled":
		return enums.StatusFilled
	case "cancelled", "canceled":
		return enums.StatusCanceled
	case "rejected":
		return enums.StatusRejected
	case "deactivated":
		return enums.StatusExpired
	default:
		return enums.StatusUnknown
	}
}

func positionSideToBybit(side enums.PositionSide) int {
	switch side {
	case enums.PosLong:
		return 1
	case enums.PosShort:
		return 2
	default:
		return 0
	}
}

func positionSideFromBybit(positionIdx int) (enums.PositionSide, error) {
	switch positionIdx {
	case 0:
		return enums.PosNet, nil
	case 1:
		return enums.PosLong, nil
	case 2:
		return enums.PosShort, nil
	default:
		return enums.PosNet, fmt.Errorf("bybit: unsupported positionIdx %d", positionIdx)
	}
}

func signedPositionQty(side, size string) decimal.Decimal {
	qty := dec(size)
	if strings.EqualFold(side, "Sell") || strings.EqualFold(side, "Short") {
		return qty.Neg()
	}
	return qty
}

func orderRequestToBybit(req model.OrderRequest, inst *model.Instrument) (bybitsdk.PlaceOrderRequest, error) {
	category, err := categoryForInstrument(inst)
	if err != nil {
		return bybitsdk.PlaceOrderRequest{}, err
	}
	side, err := sideToBybit(req.Side)
	if err != nil {
		return bybitsdk.PlaceOrderRequest{}, err
	}
	orderType, err := orderTypeToBybit(req)
	if err != nil {
		return bybitsdk.PlaceOrderRequest{}, err
	}
	out := bybitsdk.PlaceOrderRequest{
		Category:    category,
		Symbol:      inst.VenueSymbol,
		Side:        side,
		OrderType:   orderType,
		Qty:         req.Quantity.String(),
		ReduceOnly:  req.ReduceOnly,
		OrderLinkID: req.ClientID,
		PositionIdx: positionSideToBybit(req.PositionSide),
	}
	if !req.Price.IsZero() {
		out.Price = req.Price.String()
	}
	if req.Type == enums.TypeLimit {
		tif, err := tifToBybit(req.TIF)
		if err != nil {
			return bybitsdk.PlaceOrderRequest{}, err
		}
		out.TimeInForce = tif
	}
	return out, nil
}

func orderFromBybitAction(resp *bybitsdk.OrderActionResponse, req model.OrderRequest, now time.Time) model.Order {
	if req.AccountID == "" {
		req.AccountID = AccountIDUnified
	}
	if req.ClientID == "" {
		req.ClientID = resp.OrderLinkID
	}
	return model.Order{
		Request:      req,
		VenueOrderID: resp.OrderID,
		Status:       enums.StatusNew,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func orderFromBybitRecord(record bybitsdk.OrderRecord, id model.InstrumentID, accountID string) (model.Order, error) {
	positionSide, err := positionSideFromBybit(record.PositionIdx)
	if err != nil {
		return model.Order{}, err
	}
	req := model.OrderRequest{
		AccountID:    accountID,
		InstrumentID: id,
		ClientID:     record.OrderLinkID,
		Side:         sideFromBybit(record.Side),
		Type:         orderTypeFromBybit(record.OrderType),
		TIF:          tifFromBybit(record.TimeInForce),
		Quantity:     dec(record.Qty),
		Price:        dec(record.Price),
		PositionSide: positionSide,
		ReduceOnly:   record.ReduceOnly,
	}
	return model.Order{
		Request:      req,
		VenueOrderID: record.OrderID,
		Status:       statusFromBybit(record.OrderStatus),
		FilledQty:    dec(record.CumExecQty),
		AvgFillPrice: dec(record.AvgPrice),
		CreatedAt:    timeFromMillisString(record.CreatedTime),
		UpdatedAt:    timeFromMillisString(record.UpdatedTime),
	}, nil
}

func fillFromBybitExecution(record bybitsdk.ExecutionRecord, id model.InstrumentID, accountID string) model.Fill {
	liq := enums.LiqTaker
	if record.IsMaker {
		liq = enums.LiqMaker
	}
	return model.Fill{
		AccountID:    accountID,
		InstrumentID: id,
		VenueOrderID: record.OrderID,
		ClientID:     record.OrderLinkID,
		TradeID:      record.ExecID,
		Side:         sideFromBybit(record.Side),
		Liquidity:    liq,
		Price:        dec(record.ExecPrice),
		Quantity:     dec(record.ExecQty),
		Fee:          dec(record.ExecFee),
		FeeCurrency:  record.FeeCurrency,
		Timestamp:    timeFromMillisString(record.ExecTime),
	}
}

func positionFromBybit(record bybitsdk.PositionRecord, resolve func(string) model.InstrumentID, accountID string, now time.Time) (model.Position, error) {
	positionSide, err := positionSideFromBybit(record.PositionIdx)
	if err != nil {
		return model.Position{}, err
	}
	updated := now
	if updated.IsZero() {
		updated = time.Now()
	}
	return model.Position{
		AccountID:     accountID,
		InstrumentID:  resolve(record.Symbol),
		Side:          positionSide,
		Quantity:      signedPositionQty(record.Side, record.Size),
		EntryPrice:    dec(record.AvgPrice),
		UnrealizedPnL: dec(record.UnrealisedPnl),
		Leverage:      dec(record.Leverage),
		UpdatedAt:     updated,
	}, nil
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

func bybitKinds(scope []enums.InstrumentKind) []enums.InstrumentKind {
	if len(scope) > 0 {
		return append([]enums.InstrumentKind(nil), scope...)
	}
	return []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}
}
