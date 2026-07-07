package spot

import (
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

type instResolver interface {
	resolveInstID(instID string) model.InstrumentID
}

func orderFromOKX(o *okx.Order, r instResolver, accountID string) model.Order {
	otype, tif := ordTypeFromOKX(string(o.OrdType))
	return model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: r.resolveInstID(o.InstId),
			ClientID:     o.ClOrdId,
			Side:         sideFromOKX(string(o.Side)),
			Type:         otype,
			TIF:          tif,
			Quantity:     dec(o.Sz),
			Price:        dec(o.Px),
			PositionSide: enums.PosNet,
			ReduceOnly:   false,
		},
		VenueOrderID: o.OrdId,
		Status:       statusFromOKX(string(o.State)),
		FilledQty:    dec(o.AccFillSz),
		AvgFillPrice: firstNonZero(dec(o.AvgPx), dec(o.FillPx)),
		UpdatedAt:    parseMillis(o.UTime),
	}
}

func execEventsFromOrder(o *okx.Order, r instResolver, accountID string) []contract.ExecEvent {
	if o == nil || (o.InstType != "" && o.InstType != instTypeSpot) {
		return nil
	}
	id := r.resolveInstID(o.InstId)
	order := orderFromOKX(o, r, accountID)
	events := []contract.ExecEvent{contract.OrderEvent{Order: order}}

	if dec(o.FillSz).IsPositive() {
		events = append(events, contract.FillEvent{Fill: model.Fill{
			AccountID:    accountID,
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

	if order.Status == enums.StatusCanceled && o.CancelSourceReason != "" {
		events = append(events, contract.RejectEvent{ClientID: o.ClOrdId, Reason: o.CancelSourceReason})
	}

	return events
}

func liquidityFromExecType(execType string) enums.LiquiditySide {
	switch execType {
	case "M":
		return enums.LiqMaker
	case "T":
		return enums.LiqTaker
	default:
		return enums.LiqUnknown
	}
}
