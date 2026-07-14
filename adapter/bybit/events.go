package bybit

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

type privateRecordResolver func(category, symbol string) (model.InstrumentID, bool)

func (p *instrumentProvider) resolvePrivateRecord(category, symbol string) (model.InstrumentID, bool) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return model.InstrumentID{}, false
	}
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "spot":
		return p.ResolveVenueInstrument(symbol, enums.KindSpot, "")
	case "linear":
		if isBybitDatedLinearSymbol(symbol) {
			return model.InstrumentID{}, false
		}
		return p.ResolveVenueInstrument(symbol, enums.KindPerp, "")
	default:
		return model.InstrumentID{}, false
	}
}

func execEventsFromOrderMessage(msg *bybitsdk.WSOrderMessage, resolve privateRecordResolver, accountID string) []contract.ExecEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id, ok := resolve(record.Category, record.Symbol)
		if !ok {
			continue
		}
		order, err := orderFromBybitRecord(record, id, accountID)
		if err != nil {
			continue
		}
		out = append(out, contract.OrderEvent{Order: order})
	}
	return out
}

func execEventsFromExecutionMessage(msg *bybitsdk.WSExecutionMessage, resolve privateRecordResolver, accountID string) ([]contract.ExecEvent, error) {
	if msg == nil {
		return nil, nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		switch strings.ToLower(strings.TrimSpace(record.ExecType)) {
		case "trade":
		case "funding":
			continue
		default:
			return out, fmt.Errorf("bybit: unsupported private execution type %q category=%s symbol=%s", record.ExecType, record.Category, record.Symbol)
		}
		id, ok := resolve(record.Category, record.Symbol)
		if !ok {
			continue
		}
		fill := fillFromBybitExecution(record, id, accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out, nil
}

func accountEventsFromPositionMessage(msg *bybitsdk.WSPositionMessage, resolve privateRecordResolver, accountID string, now time.Time) []contract.AccountEvent {
	if msg == nil {
		return nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id, ok := resolve(record.Category, record.Symbol)
		if !ok {
			continue
		}
		pos, err := positionFromBybit(record, func(string) model.InstrumentID { return id }, accountID, now)
		if err != nil {
			continue
		}
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
