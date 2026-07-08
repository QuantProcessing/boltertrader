package gate

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func execEventsFromSpotOrderMessage(msg *gatesdk.SpotOrderMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Orders))
	for _, record := range msg.Orders {
		order := orderFromGateSpotRecord(record, resolve(record.CurrencyPair), accountID)
		order.FilledQty = decimal.Zero
		order.AvgFillPrice = decimal.Zero
		out = append(out, contract.OrderEvent{Order: order})
	}
	return out
}

func execEventsFromSpotUserTradeMessage(msg *gatesdk.SpotUserTradeMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Trades))
	for _, record := range msg.Trades {
		fill := fillFromGateSpotTrade(record, resolve(record.CurrencyPair), accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out
}

func accountEventsFromSpotBalanceMessage(msg *gatesdk.SpotBalanceMessage, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Balances))
	for _, record := range msg.Balances {
		balance := balanceFromSpotBalance(record, accountID, now)
		if balance.Currency != "" {
			out = append(out, contract.BalanceEvent{Balance: balance})
		}
	}
	return out
}

func execEventsFromFuturesOrderMessage(msg *gatesdk.FuturesOrderMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Orders))
	for _, record := range msg.Orders {
		order := orderFromGateFuturesRecord(record, resolve(record.Contract), accountID)
		order.FilledQty = decimal.Zero
		order.AvgFillPrice = decimal.Zero
		out = append(out, contract.OrderEvent{Order: order})
	}
	return out
}

func execEventsFromFuturesUserTradeMessage(msg *gatesdk.FuturesUserTradeMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Trades))
	for _, record := range msg.Trades {
		fill := fillFromGateFuturesTrade(record, resolve(record.Contract), accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out
}

func accountEventsFromFuturesPositionMessage(msg *gatesdk.FuturesPositionMessage, resolve func(string) model.InstrumentID, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Positions))
	for _, record := range msg.Positions {
		pos := positionFromGate(record, resolve, accountID, now)
		if pos.InstrumentID.Symbol != "" {
			out = append(out, contract.PositionEvent{Position: pos})
		}
	}
	return out
}

func accountEventsFromFuturesBalanceMessage(msg *gatesdk.FuturesBalanceMessage, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Balances))
	for _, record := range msg.Balances {
		currency := record.Currency
		if currency == "" {
			currency = "USDT"
		}
		total := dec(record.Total)
		balance := model.AccountBalance{
			AccountID: accountID,
			Currency:  currency,
			Total:     total,
			Free:      total,
			Available: total,
			UpdatedAt: firstNonZeroTime(timeFromMillis(record.TimeMS), timeFromSeconds(record.Time), now),
		}
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	return out
}
