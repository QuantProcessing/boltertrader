package perp

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
)

// symbolResolver maps a Binance venue symbol to a neutral InstrumentID. The
// adapter backs this with the instrument provider; tests can supply a stub.
type symbolResolver func(venueSymbol string) model.InstrumentID

// execEventsFromOrderUpdate translates a Binance ORDER_TRADE_UPDATE into domain
// execution events: always an OrderEvent reflecting the new state, plus a
// FillEvent when the execution type is a trade.
func execEventsFromOrderUpdate(ev *sdkperp.OrderUpdateEvent, resolve symbolResolver, accountID string) []contract.ExecEvent {
	o := ev.Order
	id := resolve(o.Symbol)
	ts := time.UnixMilli(ev.EventTime)

	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     o.ClientOrderID,
			Side:         sideFromBinance(o.Side),
			Type:         orderTypeFromBinance(o.OrderType),
			TIF:          tifFromBinance(o.TimeInForce),
			Quantity:     dec(o.OriginalQty),
			Price:        dec(o.OriginalPrice),
			PositionSide: positionSideFromBinance(o.PositionSide),
			ReduceOnly:   o.IsReduceOnly,
		},
		VenueOrderID: itoa(o.OrderID),
		Status:       statusFromBinance(o.OrderStatus),
		FilledQty:    dec(o.AccumulatedFilledQty),
		AvgFillPrice: dec(o.AveragePrice),
		UpdatedAt:    ts,
	}

	events := []contract.ExecEvent{contract.OrderEvent{Order: order}}

	if o.ExecutionType == "TRADE" && dec(o.LastFilledQty).IsPositive() {
		liq := enums.LiqTaker
		if o.IsMaker {
			liq = enums.LiqMaker
		}
		fill := model.Fill{
			AccountID:    accountID,
			InstrumentID: id,
			VenueOrderID: itoa(o.OrderID),
			ClientID:     o.ClientOrderID,
			TradeID:      itoa(o.TradeID),
			Side:         sideFromBinance(o.Side),
			Liquidity:    liq,
			Price:        dec(o.LastFilledPrice),
			Quantity:     dec(o.LastFilledQty),
			Fee:          dec(o.Commission),
			FeeCurrency:  o.CommissionAsset,
			Timestamp:    time.UnixMilli(o.TradeTime),
		}
		events = append(events, contract.FillEvent{Fill: fill})
	}

	if order.Status == enums.StatusRejected {
		events = append(events, contract.RejectEvent{ClientID: o.ClientOrderID, Reason: o.OrderStatus})
	}

	return events
}

// accountEventsFromUpdate translates a Binance ACCOUNT_UPDATE into domain
// balance and position events.
func accountEventsFromUpdate(ev *sdkperp.AccountUpdateEvent, resolve symbolResolver, accountID string) []contract.AccountEvent {
	ts := time.UnixMilli(ev.EventTime)
	var out []contract.AccountEvent

	for _, b := range ev.UpdateData.Balances {
		free := dec(b.CrossWalletBalance)
		out = append(out, contract.BalanceEvent{Balance: model.AccountBalance{
			AccountID: accountID,
			Currency:  b.Asset,
			Total:     dec(b.WalletBalance),
			Free:      free,
			UpdatedAt: ts,
		}})
	}

	for _, p := range ev.UpdateData.Positions {
		qty := dec(p.PositionAmount)
		out = append(out, contract.PositionEvent{Position: model.Position{
			AccountID:     accountID,
			InstrumentID:  resolve(p.Symbol),
			Side:          positionSideFromBinance(p.PositionSide),
			Quantity:      qty,
			EntryPrice:    dec(p.EntryPrice),
			UnrealizedPnL: dec(p.UnrealizedPnL),
			UpdatedAt:     ts,
		}})
	}

	return out
}
