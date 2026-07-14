package bitget

import (
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func execEventsFromOrderMessage(msg *bitgetsdk.WSOrderMessage, resolve func(string, string) (model.InstrumentID, bool), accountID string) ([]contract.ExecEvent, error) {
	if msg == nil || resolve == nil {
		return nil, nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id, ok := resolve(record.Category, record.Symbol)
		if !ok {
			continue
		}
		order, err := orderFromBitgetRecord(record, id, accountID)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid private order position semantics category=%s symbol=%s: %w", record.Category, record.Symbol, err)
		}
		// Bitget order WS carries cumulative execution fields while the fill WS
		// carries incremental executions. Keep order events state-only so runtime
		// fill accounting is driven by the fill stream and does not double count.
		order.FilledQty = decimal.Zero
		order.AvgFillPrice = decimal.Zero
		out = append(out, contract.OrderEvent{Order: order})
	}
	return out, nil
}

func execEventsFromFillMessage(msg *bitgetsdk.WSFillMessage, resolve func(string, string) (model.InstrumentID, bool), accountID string) []contract.ExecEvent {
	if msg == nil || resolve == nil {
		return nil
	}
	out := make([]contract.ExecEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		id, ok := resolve(record.Category, record.Symbol)
		if !ok {
			continue
		}
		fill := fillFromBitget(record, id, accountID)
		if fill.Quantity.IsPositive() {
			out = append(out, contract.FillEvent{Fill: fill})
		}
	}
	return out
}

func accountEventsFromPositionMessage(msg *bitgetsdk.WSPositionMessage, resolve func(string, string) (model.InstrumentID, bool), accountID string, now time.Time) ([]contract.AccountEvent, error) {
	if msg == nil || resolve == nil {
		return nil, nil
	}
	out := make([]contract.AccountEvent, 0, len(msg.Data))
	for _, record := range msg.Data {
		category := positionCategoryFromBitget(record)
		id, ok := resolve(category, record.Symbol)
		if !ok {
			continue
		}
		position, err := positionFromBitget(record, func(string) model.InstrumentID { return id }, accountID, now)
		if err != nil {
			return nil, fmt.Errorf("bitget: invalid private position semantics category=%s symbol=%s: %w", category, record.Symbol, err)
		}
		out = append(out, contract.PositionEvent{Position: position})
	}
	return out, nil
}

func positionCategoryFromBitget(record bitgetsdk.PositionRecord) string {
	explicit := normalizeVenueSymbol(record.Category)
	inferred := ""
	switch normalizeVenueSymbol(record.MarginCoin) {
	case "USDT":
		inferred = bitgetsdk.ProductTypeUSDTFutures
	case "USDC":
		inferred = bitgetsdk.ProductTypeUSDCFutures
	case "":
	default:
		inferred = "COIN-FUTURES"
	}
	if explicit != "" {
		if inferred != "" && explicit != inferred {
			return ""
		}
		return explicit
	}
	return inferred
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
