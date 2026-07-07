package spot

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

type symbolResolver func(venueSymbol string) model.InstrumentID

func execEventsFromExecutionReport(ev *sdkspot.ExecutionReportEvent, resolve symbolResolver, accountID string) []contract.ExecEvent {
	id := resolve(ev.Symbol)
	ts := time.UnixMilli(ev.EventTime)
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     ev.ClientOrderID,
			Side:         sideFromBinance(ev.Side),
			Type:         orderTypeFromBinance(ev.OrderType),
			TIF:          tifFromBinance(ev.TimeInForce),
			Quantity:     dec(ev.Quantity),
			Price:        dec(ev.Price),
			TriggerPrice: dec(ev.StopPrice),
			PositionSide: enums.PosNet,
			ReduceOnly:   false,
		},
		VenueOrderID: itoa(ev.OrderID),
		Status:       statusFromBinance(ev.OrderStatus),
		FilledQty:    dec(ev.CumulativeFilledQuantity),
		AvgFillPrice: avgFillPrice(dec(ev.CumulativeFilledQuantity), dec(ev.CumulativeQuoteAssetTransactedQuantity)),
		UpdatedAt:    ts,
	}

	events := []contract.ExecEvent{contract.OrderEvent{Order: order}}
	if ev.ExecutionType == "TRADE" && dec(ev.LastExecutedQuantity).IsPositive() {
		liq := enums.LiqTaker
		if ev.IsMaker {
			liq = enums.LiqMaker
		}
		fill := model.Fill{
			AccountID:    accountID,
			InstrumentID: id,
			VenueOrderID: itoa(ev.OrderID),
			ClientID:     ev.ClientOrderID,
			TradeID:      itoa(ev.TradeID),
			Side:         sideFromBinance(ev.Side),
			Liquidity:    liq,
			Price:        dec(ev.LastExecutedPrice),
			Quantity:     dec(ev.LastExecutedQuantity),
			Fee:          dec(ev.CommissionAmount),
			FeeCurrency:  ev.CommissionAsset,
			Timestamp:    time.UnixMilli(ev.TransactionTime),
		}
		events = append(events, contract.FillEvent{Fill: fill})
	}
	if order.Status == enums.StatusRejected {
		events = append(events, contract.RejectEvent{ClientID: ev.ClientOrderID, Reason: ev.RejectReason})
	}
	return events
}

func execEventsFromOrderResponse(resp *sdkspot.OrderResponse, req model.OrderRequest) []contract.ExecEvent {
	order := orderFromResponse(resp, req)
	if resp.TransactTime > 0 {
		order.UpdatedAt = time.UnixMilli(resp.TransactTime)
	}
	events := []contract.ExecEvent{contract.OrderEvent{Order: order}}
	for _, fillResp := range resp.Fills {
		qty := dec(fillResp.Qty)
		if !qty.IsPositive() {
			continue
		}
		ts := order.UpdatedAt
		if ts.IsZero() {
			ts = time.Now()
		}
		events = append(events, contract.FillEvent{Fill: model.Fill{
			AccountID:    order.Request.AccountID,
			InstrumentID: order.Request.InstrumentID,
			VenueOrderID: order.VenueOrderID,
			ClientID:     order.Request.ClientID,
			TradeID:      itoa(fillResp.TradeID),
			Side:         order.Request.Side,
			Liquidity:    enums.LiqTaker,
			Price:        dec(fillResp.Price),
			Quantity:     qty,
			Fee:          dec(fillResp.Commission),
			FeeCurrency:  fillResp.CommissionAsset,
			Timestamp:    ts,
		}})
	}
	return events
}

func accountEventsFromAccountPosition(ev *sdkspot.AccountPositionEvent, accountID string) []contract.AccountEvent {
	ts := time.UnixMilli(ev.EventTime)
	out := make([]contract.AccountEvent, 0, len(ev.Balances))
	for _, b := range ev.Balances {
		free := dec(b.Free)
		locked := dec(b.Locked)
		out = append(out, contract.BalanceEvent{Balance: model.AccountBalance{
			AccountID: accountID,
			Currency:  b.Asset,
			Total:     free.Add(locked),
			Free:      free,
			Available: free,
			Locked:    locked,
			UpdatedAt: ts,
		}})
	}
	return out
}
