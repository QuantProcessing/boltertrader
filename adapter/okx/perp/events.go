package perp

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

// instResolver maps an OKX InstId to a neutral InstrumentID.
type instResolver interface {
	resolveInstID(instID string) model.InstrumentID
}

// parseMillis parses an OKX millisecond timestamp string into a time.Time.
func parseMillis(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	return time.UnixMilli(dec(s).IntPart())
}

// orderFromOKX translates an OKX Order (REST or ws push) into a domain Order.
func orderFromOKX(o *okx.Order, r instResolver) model.Order {
	otype, tif := ordTypeFromOKX(string(o.OrdType))
	return model.Order{
		Request: model.OrderRequest{
			InstrumentID: r.resolveInstID(o.InstId),
			ClientID:     o.ClOrdId,
			Side:         sideFromOKX(string(o.Side)),
			Type:         otype,
			TIF:          tif,
			Quantity:     dec(o.Sz),
			Price:        dec(o.Px),
			PositionSide: positionSideFromOKX(string(o.PosSide)),
		},
		VenueOrderID: o.OrdId,
		Status:       statusFromOKX(string(o.State)),
		FilledQty:    dec(o.AccFillSz),
		AvgFillPrice: dec(o.AvgPx),
		UpdatedAt:    parseMillis(o.UTime),
	}
}

// execEventsFromOrder translates an OKX order push into domain execution
// events: an OrderEvent always, plus a FillEvent when the push reports a new
// fill (execType + fillSz present).
func execEventsFromOrder(o *okx.Order, r instResolver) []contract.ExecEvent {
	id := r.resolveInstID(o.InstId)
	order := orderFromOKX(o, r)
	events := []contract.ExecEvent{contract.OrderEvent{Order: order}}

	if dec(o.FillSz).IsPositive() {
		events = append(events, contract.FillEvent{Fill: model.Fill{
			InstrumentID: id,
			VenueOrderID: o.OrdId,
			ClientID:     o.ClOrdId,
			TradeID:      o.TradeId,
			Side:         sideFromOKX(string(o.Side)),
			Liquidity:    liquidityFromExecType(o.ExecType),
			Price:        dec(o.FillPx),
			Quantity:     dec(o.FillSz),
			Fee:          dec(o.Fee).Abs(),
			FeeCurrency:  o.FeeCcy,
			Timestamp:    parseMillis(o.FillTime),
		}})
	}

	if order.Status == enums.StatusCanceled && o.CancelSource != "" {
		// Surface explicit rejects (e.g. post-only that would take) as rejects.
		if o.CancelSourceReason != "" {
			events = append(events, contract.RejectEvent{ClientID: o.ClOrdId, Reason: o.CancelSourceReason})
		}
	}

	return events
}

// accountEventsFromPosition translates an OKX position push into a domain
// PositionEvent with a SIGNED quantity (negative for short).
func accountEventsFromPosition(p *okx.Position, r instResolver) []contract.AccountEvent {
	qty := dec(p.Pos)
	if positionSideFromOKX(string(p.PosSide)) == enums.PosShort && qty.IsPositive() {
		qty = qty.Neg()
	}
	return []contract.AccountEvent{contract.PositionEvent{Position: model.Position{
		InstrumentID:  r.resolveInstID(p.InstId),
		Side:          positionSideFromOKX(string(p.PosSide)),
		Quantity:      qty,
		EntryPrice:    dec(p.AvgPx),
		MarkPrice:     dec(p.MarkPx),
		UnrealizedPnL: dec(p.Upl),
		Leverage:      dec(p.Lever),
		UpdatedAt:     parseMillis(p.UTime),
	}}}
}
