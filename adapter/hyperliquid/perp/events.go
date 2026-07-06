package perp

import (
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	"github.com/shopspring/decimal"
)

func execEventsFromOrderUpdate(update sdk.WsOrderUpdate, provider *instruments.Registry, accountID ...string) []contract.ExecEvent {
	id, ok := provider.ResolveVenueSymbol(update.Order.Coin)
	if !ok {
		return nil
	}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    firstAccountID(accountID),
			InstrumentID: id,
			ClientID:     update.Order.Cliod,
			Side:         sideFromHL(update.Order.Side),
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     dec(update.Order.OrigSz),
			Price:        dec(update.Order.LimitPx),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: fmt.Sprint(update.Order.Oid),
		Status:       statusFromHL(string(update.Status)),
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
		liq := enums.LiqMaker
		if fill.Crossed {
			liq = enums.LiqTaker
		}
		out = append(out, contract.FillEvent{Fill: model.Fill{
			AccountID:    firstAccountID(accountID),
			InstrumentID: id,
			VenueOrderID: fmt.Sprint(fill.Oid),
			TradeID:      fmt.Sprint(fill.Tid),
			Side:         sideFromHL(fill.Side),
			Liquidity:    liq,
			Price:        dec(fill.Px),
			Quantity:     dec(fill.Sz),
			Fee:          dec(fill.Fee).Abs(),
			FeeCurrency:  fill.FeeToken,
			Timestamp:    parseMillis(fill.Time),
		}})
	}
	return out
}

func accountEventsFromPerpPosition(state *sdkperp.PerpPosition, provider *instruments.Registry, clk clock.Clock, accountID ...string) []contract.AccountEvent {
	if state == nil {
		return nil
	}
	var out []contract.AccountEvent
	if balance, ok := balanceFromPerpPosition(state, clk, accountID...); ok {
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	now := clk.Now()
	for _, pos := range positionsFromPerpPosition(state, provider, now, accountID...) {
		if pos.Quantity.IsZero() {
			continue
		}
		out = append(out, contract.PositionEvent{Position: pos})
	}
	return out
}

func balanceFromPerpPosition(state *sdkperp.PerpPosition, clk clock.Clock, accountID ...string) (model.AccountBalance, bool) {
	if state == nil {
		return model.AccountBalance{}, false
	}
	total := dec(state.MarginSummary.AccountValue)
	locked := dec(state.MarginSummary.TotalMarginUsed)
	if total.IsZero() && state.CrossMarginSummary.AccountValue != "" {
		total = dec(state.CrossMarginSummary.AccountValue)
	}
	if locked.IsZero() && state.CrossMarginSummary.TotalMarginUsed != "" {
		locked = dec(state.CrossMarginSummary.TotalMarginUsed)
	}
	available := dec(state.Withdrawable)
	if total.IsZero() && locked.IsZero() && available.IsZero() {
		return model.AccountBalance{}, false
	}
	updatedAt := parseMillis(state.Time)
	if updatedAt.IsZero() {
		updatedAt = clk.Now()
	}
	return model.AccountBalance{
		AccountID: firstAccountID(accountID),
		Currency:  "USDC",
		Total:     total,
		Available: available,
		Locked:    locked,
		UpdatedAt: updatedAt,
	}, true
}

func positionsFromPerpPosition(state *sdkperp.PerpPosition, provider *instruments.Registry, fallbackTime time.Time, accountID ...string) []model.Position {
	if state == nil {
		return nil
	}
	updatedAt := parseMillis(state.Time)
	if updatedAt.IsZero() {
		updatedAt = fallbackTime
	}
	out := make([]model.Position, 0, len(state.AssetPositions))
	for _, raw := range state.AssetPositions {
		coin := raw.Position.Coin
		if coin == "" {
			continue
		}
		id, ok := provider.ResolveVenueSymbol(coin)
		if !ok {
			continue
		}
		qty := dec(raw.Position.Szi)
		if qty.IsZero() {
			continue
		}
		out = append(out, model.Position{
			AccountID:     firstAccountID(accountID),
			InstrumentID:  id,
			Side:          enums.PosNet,
			Quantity:      qty,
			EntryPrice:    dec(raw.Position.EntryPx),
			MarkPrice:     decimal.Zero,
			UnrealizedPnL: dec(raw.Position.UnrealizedPnl),
			Leverage:      decimal.NewFromInt(int64(raw.Position.Leverage.Value)),
			UpdatedAt:     updatedAt,
		})
	}
	return out
}
