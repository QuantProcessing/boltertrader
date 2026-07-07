package orderstate

import (
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
)

func IsTerminal(s enums.OrderStatus) bool {
	switch s {
	case enums.StatusUnknown, enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return true
	default:
		return false
	}
}

func ApplyFill(order model.Order, fill model.Fill, fallbackTime time.Time) model.Order {
	if order.VenueOrderID == "" {
		order.VenueOrderID = fill.VenueOrderID
	}
	if order.Request.ClientID == "" {
		order.Request.ClientID = fill.ClientID
	}
	if order.Request.AccountID == "" {
		order.Request.AccountID = fill.AccountID
	}
	if order.Request.InstrumentID.Symbol == "" {
		order.Request.InstrumentID = fill.InstrumentID
	}
	if order.Request.Side == enums.SideUnknown {
		order.Request.Side = fill.Side
	}
	oldQty := order.FilledQty
	newQty := oldQty.Add(fill.Quantity)
	if fill.Price.IsPositive() && fill.Quantity.IsPositive() && newQty.IsPositive() {
		if oldQty.IsPositive() && order.AvgFillPrice.IsPositive() {
			oldNotional := order.AvgFillPrice.Mul(oldQty)
			newNotional := fill.Price.Mul(fill.Quantity)
			order.AvgFillPrice = oldNotional.Add(newNotional).Div(newQty)
		} else {
			order.AvgFillPrice = fill.Price
		}
	}
	order.FilledQty = newQty
	if !order.Request.Quantity.IsZero() && newQty.GreaterThanOrEqual(order.Request.Quantity) {
		order.Status = enums.StatusFilled
	} else if !IsTerminal(order.Status) {
		order.Status = enums.StatusPartiallyFilled
	}
	order.UpdatedAt = fill.Timestamp
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = fallbackTime
	}
	return order
}

func Merge(existing, incoming model.Order) model.Order {
	out := incoming
	if out.Request.ClientID == "" {
		out.Request.ClientID = existing.Request.ClientID
	}
	if out.Request.AccountID == "" {
		out.Request.AccountID = existing.Request.AccountID
	}
	if out.Request.InstrumentID.Symbol == "" {
		out.Request.InstrumentID = existing.Request.InstrumentID
	}
	if out.Request.Side == enums.SideUnknown {
		out.Request.Side = existing.Request.Side
	}
	if out.Request.Type == enums.TypeUnknown {
		out.Request.Type = existing.Request.Type
	}
	if out.Request.Quantity.IsZero() {
		out.Request.Quantity = existing.Request.Quantity
	}
	if out.Request.Price.IsZero() {
		out.Request.Price = existing.Request.Price
	}
	if out.Request.PositionSide == enums.PosNet && requestIsSparse(incoming.Request) {
		out.Request.PositionSide = existing.Request.PositionSide
	}
	if out.VenueOrderID == "" {
		out.VenueOrderID = existing.VenueOrderID
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = existing.CreatedAt
	}
	if out.UpdatedAt.IsZero() {
		out.UpdatedAt = existing.UpdatedAt
	}
	if existing.FilledQty.GreaterThan(out.FilledQty) {
		out.FilledQty = existing.FilledQty
		out.AvgFillPrice = existing.AvgFillPrice
	} else if existing.FilledQty.Equal(out.FilledQty) && out.AvgFillPrice.IsZero() {
		out.AvgFillPrice = existing.AvgFillPrice
	}
	if preserveStatus(existing, incoming) {
		out.Status = existing.Status
		if !existing.UpdatedAt.IsZero() {
			out.UpdatedAt = existing.UpdatedAt
		}
		if out.RejectReason == "" {
			out.RejectReason = existing.RejectReason
		}
	}
	return out
}

func FillKey(fill model.Fill) string {
	if fill.TradeID == "" {
		return ""
	}
	return strings.Join([]string{
		fill.AccountID,
		fill.InstrumentID.String(),
		fill.ClientID,
		fill.VenueOrderID,
		fill.TradeID,
	}, "\x00")
}

func preserveStatus(existing, incoming model.Order) bool {
	if existing.Status == enums.StatusFilled && incoming.Status != enums.StatusFilled {
		return true
	}
	if IsTerminal(existing.Status) && !IsTerminal(incoming.Status) {
		return true
	}
	if !IsTerminal(existing.Status) || !IsTerminal(incoming.Status) || existing.Status == incoming.Status {
		return false
	}
	if existing.Status == enums.StatusUnknown && incoming.Status != enums.StatusUnknown {
		return false
	}
	if incoming.Status == enums.StatusFilled {
		return false
	}
	if incoming.Status == enums.StatusUnknown {
		return true
	}
	if incoming.UpdatedAt.After(existing.UpdatedAt) {
		return false
	}
	return terminalRank(existing.Status) >= terminalRank(incoming.Status)
}

func terminalRank(status enums.OrderStatus) int {
	switch status {
	case enums.StatusFilled:
		return 4
	case enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
		return 3
	case enums.StatusUnknown:
		return 1
	default:
		return 0
	}
}

func requestIsSparse(req model.OrderRequest) bool {
	return req.InstrumentID.Symbol == "" &&
		req.ClientID == "" &&
		req.Side == enums.SideUnknown &&
		req.Type == enums.TypeUnknown &&
		req.TIF == enums.TifUnknown &&
		req.Quantity.IsZero() &&
		req.Price.IsZero() &&
		req.TriggerPrice.IsZero() &&
		req.ActivationPrice.IsZero() &&
		req.TrailingOffsetBps.IsZero() &&
		!req.ReduceOnly &&
		req.Venue == nil
}
