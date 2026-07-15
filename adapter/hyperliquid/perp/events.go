package perp

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/instruments"
	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/ordersemantics"
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
			Fee:          dec(fill.Fee),
			FeeCurrency:  fill.FeeToken,
			Timestamp:    parseMillis(fill.Time),
		}})
	}
	return out
}

func accountEventsFromPerpPosition(state *sdkperp.PerpPosition, provider *instruments.Registry, clk clock.Clock, accountID ...string) []contract.AccountEvent {
	return accountEventsFromPerpPositionForMode(state, provider, clk, sdk.AccountAbstractionDefault, accountID...)
}

func accountEventsFromPerpPositionForMode(state *sdkperp.PerpPosition, provider *instruments.Registry, clk clock.Clock, accountMode sdk.AccountAbstraction, accountID ...string) []contract.AccountEvent {
	if state == nil {
		return nil
	}
	var out []contract.AccountEvent
	if !accountMode.UsesSpotClearinghouseState() {
		if balance, ok := balanceFromPerpPosition(state, clk, accountID...); ok {
			out = append(out, contract.BalanceEvent{Balance: balance})
		}
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
		Free:      available,
		Locked:    locked,
		UpdatedAt: updatedAt,
	}, true
}

func positionsFromPerpPosition(state *sdkperp.PerpPosition, provider *instruments.Registry, fallbackTime time.Time, accountID ...string) []model.Position {
	out, _ := positionsFromPerpPositionScoped(state, provider, fallbackTime, "", false, accountID...)
	return out
}

func positionsFromPerpPositionForDex(state *sdkperp.PerpPosition, provider *instruments.Registry, fallbackTime time.Time, dex string, accountID ...string) ([]model.Position, error) {
	return positionsFromPerpPositionScoped(state, provider, fallbackTime, dex, true, accountID...)
}

func positionsFromPerpPositionScoped(state *sdkperp.PerpPosition, provider *instruments.Registry, fallbackTime time.Time, dex string, strictDex bool, accountID ...string) ([]model.Position, error) {
	if state == nil {
		return nil, nil
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
		qty := dec(raw.Position.Szi)
		if qty.IsZero() {
			continue
		}
		var id model.InstrumentID
		var ok bool
		if strictDex {
			var qualified string
			id, qualified, ok = resolveHIP3PositionInstrument(provider, coin, dex)
			if !ok {
				return nil, fmt.Errorf("unresolved nonzero HIP-3 position instrument %s", qualified)
			}
		} else {
			id, ok = provider.ResolveVenueSymbol(coin)
			if !ok {
				continue
			}
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
	return out, nil
}

func resolveHIP3PositionInstrument(provider *instruments.Registry, coin, dex string) (model.InstrumentID, string, bool) {
	coin = strings.TrimSpace(coin)
	dex = strings.TrimSpace(dex)
	if coin == "" || dex == "" {
		return model.InstrumentID{}, coin, false
	}
	qualified := dex + ":" + coin
	if rawDex, symbol, ok := strings.Cut(coin, ":"); ok {
		if !strings.EqualFold(strings.TrimSpace(rawDex), dex) {
			return model.InstrumentID{}, coin, false
		}
		qualified = dex + ":" + symbol
	}
	id, ok := provider.ResolveVenueSymbol(qualified)
	return id, qualified, ok
}
