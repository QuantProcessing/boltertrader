package spot

import (
	"fmt"
	"time"

	hlaccount "github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/account"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/ordersemantics"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func accountEventsFromSpotState(state sdk.SpotClearinghouseState, now time.Time, accountID string) ([]contract.AccountEvent, error) {
	balances, err := hlaccount.SpotBalances(accountID, state, now)
	if err != nil {
		return nil, fmt.Errorf("hyperliquid spot: %w", err)
	}
	out := make([]contract.AccountEvent, 0, len(balances))
	for _, balance := range balances {
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	return out, nil
}

func execEventsFromOrderUpdate(update sdk.WsOrderUpdate, provider *instruments.Registry, accountID ...string) []contract.ExecEvent {
	id, ok := provider.ResolveVenueSymbol(update.Order.Coin)
	if !ok {
		return nil
	}
	original, err := decimal.NewFromString(update.Order.OrigSz)
	if err != nil || original.IsNegative() {
		return nil
	}
	remaining, err := decimal.NewFromString(update.Order.Sz)
	if err != nil || remaining.IsNegative() || remaining.GreaterThan(original) {
		return nil
	}
	orderType, tif, triggerPrice := ordersemantics.FromWire(update.Order.OrderType, update.Order.Tif, update.Order.IsTrigger, update.Order.TriggerPx)
	reduceOnly := update.Order.ReduceOnly != nil && *update.Order.ReduceOnly
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    firstAccountID(accountID),
			InstrumentID: id,
			ClientID:     update.Order.Cliod,
			Side:         sideFromHL(update.Order.Side),
			Type:         orderType,
			TIF:          tif,
			Quantity:     original,
			Price:        dec(update.Order.LimitPx),
			TriggerPrice: triggerPrice,
			PositionSide: enums.PosNet,
			ReduceOnly:   reduceOnly,
		},
		VenueOrderID: fmt.Sprint(update.Order.Oid),
		Status:       statusFromHL(string(update.Status)),
		FilledQty:    original.Sub(remaining),
		UpdatedAt:    parseMillis(update.StatusTimestamp),
		CreatedAt:    parseMillis(update.Order.Timestamp),
	}
	return []contract.ExecEvent{contract.OrderEvent{Order: order}}
}

func execEventsFromUserFills(fills sdk.WsUserFills, provider *instruments.Registry, accountID ...string) []contract.ExecEvent {
	out := make([]contract.ExecEvent, 0, len(fills.Fills))
	for _, fill := range fills.Fills {
		id, ok := provider.ResolveVenueSymbol(fill.Coin)
		if !ok {
			continue
		}
		liquidity := enums.LiqMaker
		if fill.Crossed {
			liquidity = enums.LiqTaker
		}
		out = append(out, contract.FillEvent{Fill: model.Fill{
			AccountID:    firstAccountID(accountID),
			InstrumentID: id,
			VenueOrderID: fmt.Sprint(fill.Oid),
			TradeID:      fmt.Sprint(fill.Tid),
			Side:         sideFromHL(fill.Side),
			Liquidity:    liquidity,
			Price:        dec(fill.Px),
			Quantity:     dec(fill.Sz),
			Fee:          dec(fill.Fee),
			FeeCurrency:  fill.FeeToken,
			Timestamp:    parseMillis(fill.Time),
		}})
	}
	return out
}
