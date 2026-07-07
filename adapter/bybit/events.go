package bybit

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func execEventsFromOrderMessage(msg *bybitsdk.WSOrderMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id := resolve(record.Symbol)
		out = append(out, contract.OrderEvent{Order: orderFromBybitRecord(record, id, accountID)})
	}
	return out
}

func execEventsFromExecutionMessage(msg *bybitsdk.WSExecutionMessage, resolve func(string) model.InstrumentID, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id := resolve(record.Symbol)
		fill := fillFromBybitExecution(record, id, accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out
}

func accountEventsFromPositionMessage(msg *bybitsdk.WSPositionMessage, resolve func(string) model.InstrumentID, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		pos := positionFromBybit(record, resolve, accountID, now)
		out = append(out, contract.PositionEvent{Position: pos})
	}
	return out
}

func accountEventsFromWalletMessage(msg *bybitsdk.WSWalletMessage, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	wallet := &bybitsdk.WalletBalanceResult{List: make([]bybitsdk.WalletAccount, 0, len(msg.Data))}
	for _, account := range msg.Data {
		wallet.List = append(wallet.List, bybitsdk.WalletAccount{
			AccountType: account.AccountType,
			Coin:        account.Coins,
		})
	}
	balances := balancesFromWallet(wallet, accountID, now)
	out := make([]contract.AccountEvent, 0, len(balances))
	for _, balance := range balances {
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	return out
}
