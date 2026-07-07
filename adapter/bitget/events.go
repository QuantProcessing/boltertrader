package bitget

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func execEventsFromOrderMessage(msg *bitgetsdk.WSOrderMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		order := orderFromBitgetRecord(record, resolve(record.Symbol), accountID)
		// Bitget order WS carries cumulative execution fields while the fill WS
		// carries incremental executions. Keep order events state-only so runtime
		// fill accounting is driven by the fill stream and does not double count.
		order.FilledQty = decimal.Zero
		order.AvgFillPrice = decimal.Zero
		out = append(out, contract.OrderEvent{Order: order})
	}
	return out
}

func execEventsFromFillMessage(msg *bitgetsdk.WSFillMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		fill := fillFromBitget(record, resolve(record.Symbol), accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out
}

func accountEventsFromPositionMessage(msg *bitgetsdk.WSPositionMessage, resolve func(string) model.InstrumentID, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		out = append(out, contract.PositionEvent{Position: positionFromBitget(record, resolve, accountID, now)})
	}
	return out
}

func accountEventsFromAccountMessage(msg *bitgetsdk.WSAccountMessage, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	assets := &bitgetsdk.AccountAssets{Assets: msg.Data}
	balances := balancesFromAssets(assets, accountID, now)
	out := make([]contract.AccountEvent, 0, len(balances))
	for _, balance := range balances {
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	return out
}
