package orderstate

import (
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func IsTerminal(s enums.OrderStatus) bool {
	switch s {
	case enums.StatusFilled, enums.StatusCanceled, enums.StatusRejected, enums.StatusExpired:
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
	appliedQty := fill.Quantity
	if order.Request.Quantity.IsPositive() {
		remaining := order.Request.Quantity.Sub(oldQty)
		if !remaining.IsPositive() {
			appliedQty = decimal.Zero
		} else if appliedQty.GreaterThan(remaining) {
			appliedQty = remaining
		}
	}
	newQty := oldQty.Add(appliedQty)
	if fill.Price.IsPositive() && appliedQty.IsPositive() && newQty.IsPositive() {
		if oldQty.IsPositive() && order.AvgFillPrice.IsPositive() {
			oldNotional := order.AvgFillPrice.Mul(oldQty)
			newNotional := fill.Price.Mul(appliedQty)
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
	if venueUpdateOlder(incoming.UpdatedAt, existing.UpdatedAt) {
		return promoteCompleteFill(enrichNewerOrder(existing, incoming))
	}
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
	return promoteCompleteFill(out)
}

func promoteCompleteFill(order model.Order) model.Order {
	if order.Request.Quantity.IsPositive() && order.FilledQty.GreaterThanOrEqual(order.Request.Quantity) {
		order.Status = enums.StatusFilled
	}
	return order
}

func enrichNewerOrder(newer, older model.Order) model.Order {
	out := newer
	if out.Request.AccountID == "" {
		out.Request.AccountID = older.Request.AccountID
	}
	if out.Request.InstrumentID.Symbol == "" {
		out.Request.InstrumentID = older.Request.InstrumentID
	}
	if out.Request.ClientID == "" {
		out.Request.ClientID = older.Request.ClientID
	}
	if out.Request.Side == enums.SideUnknown {
		out.Request.Side = older.Request.Side
	}
	if out.Request.Type == enums.TypeUnknown {
		out.Request.Type = older.Request.Type
	}
	if out.Request.TIF == enums.TifUnknown {
		out.Request.TIF = older.Request.TIF
	}
	if out.Request.Quantity.IsZero() {
		out.Request.Quantity = older.Request.Quantity
	}
	if out.Request.Price.IsZero() {
		out.Request.Price = older.Request.Price
	}
	if out.Request.TriggerPrice.IsZero() {
		out.Request.TriggerPrice = older.Request.TriggerPrice
	}
	if out.Request.ActivationPrice.IsZero() {
		out.Request.ActivationPrice = older.Request.ActivationPrice
	}
	if out.Request.TrailingOffsetBps.IsZero() {
		out.Request.TrailingOffsetBps = older.Request.TrailingOffsetBps
	}
	if !out.Request.ReduceOnly && older.Request.ReduceOnly {
		out.Request.ReduceOnly = true
	}
	if out.Request.Venue == nil {
		out.Request.Venue = older.Request.Venue
	}
	if out.VenueOrderID == "" {
		out.VenueOrderID = older.VenueOrderID
	}
	if out.CreatedAt.IsZero() {
		out.CreatedAt = older.CreatedAt
	}
	if older.FilledQty.GreaterThan(out.FilledQty) {
		out.FilledQty = older.FilledQty
		out.AvgFillPrice = older.AvgFillPrice
	} else if out.AvgFillPrice.IsZero() {
		out.AvgFillPrice = older.AvgFillPrice
	}
	if out.RejectReason == "" {
		out.RejectReason = older.RejectReason
	}
	return out
}

func venueUpdateOlder(incoming, current time.Time) bool {
	return !incoming.IsZero() && !current.IsZero() && incoming.Before(current)
}

// FillKey returns the venue-scoped idempotency identity for a reported trade.
// Order aliases are deliberately excluded because they can be learned after
// the first observation; conflicting aliases for one venue trade must not make
// that trade apply twice.
func FillKey(fill model.Fill) string {
	if fill.TradeID == "" {
		return ""
	}
	return strings.Join([]string{
		fill.AccountID,
		fill.InstrumentID.String(),
		fill.TradeID,
	}, "\x00")
}

// MergeSnapshot applies a venue snapshot observed at observedAt. Identity and
// sparse request fields are enriched from the existing order, while cumulative
// fill quantity and status come from the snapshot unless the cache already has
// an event newer than the snapshot's observation time.
func MergeSnapshot(existing, incoming model.Order, observedAt time.Time) model.Order {
	if observedAt.IsZero() && !incoming.FilledQty.IsPositive() {
		out := Merge(existing, incoming)
		if existing.FilledQty.GreaterThan(incoming.FilledQty) {
			out.Status = existing.Status
			out.UpdatedAt = existing.UpdatedAt
		}
		return promoteCompleteFill(out)
	}
	if !observedAt.IsZero() && !existing.UpdatedAt.IsZero() && existing.UpdatedAt.After(observedAt) {
		return promoteCompleteFill(enrichNewerOrder(existing, incoming))
	}
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
	if out.Request.TIF == enums.TifUnknown {
		out.Request.TIF = existing.Request.TIF
	}
	if out.Request.Quantity.IsZero() {
		out.Request.Quantity = existing.Request.Quantity
	}
	if out.Request.Price.IsZero() {
		out.Request.Price = existing.Request.Price
	}
	if out.Request.TriggerPrice.IsZero() {
		out.Request.TriggerPrice = existing.Request.TriggerPrice
	}
	if out.Request.ActivationPrice.IsZero() {
		out.Request.ActivationPrice = existing.Request.ActivationPrice
	}
	if out.Request.TrailingOffsetBps.IsZero() {
		out.Request.TrailingOffsetBps = existing.Request.TrailingOffsetBps
	}
	if !out.Request.ReduceOnly && existing.Request.ReduceOnly {
		out.Request.ReduceOnly = true
	}
	if out.Request.Venue == nil {
		out.Request.Venue = existing.Request.Venue
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
	return promoteCompleteFill(out)
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
