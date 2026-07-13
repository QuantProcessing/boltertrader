package perp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	"github.com/shopspring/decimal"
)

type symbolResolver func(venueSymbol string) (model.InstrumentID, bool)

func execEventsFromOrderUpdate(ev *sdkperp.OrderUpdateEvent, resolve symbolResolver, accountID string) ([]contract.ExecEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("aster perp: order update event is required")
	}
	o := ev.Order
	id, ok := resolve(o.Symbol)
	if !ok || id.Symbol == "" {
		return nil, fmt.Errorf("aster perp: order update symbol %q is unresolved", o.Symbol)
	}
	if ev.EventTime <= 0 || ev.TransactionTime <= 0 {
		return nil, fmt.Errorf("aster perp: order update source timestamp is required")
	}
	if o.ClientOrderID == "" || o.OrderID == 0 {
		return nil, fmt.Errorf("aster perp: order update client and order ids are required")
	}
	if side := strings.ToUpper(strings.TrimSpace(o.PositionSide)); side != "" && side != "BOTH" {
		return nil, fmt.Errorf("aster perp: order update hedge position side %q is unsupported", o.PositionSide)
	}
	for field, raw := range map[string]string{
		"originalQty":          o.OriginalQty,
		"originalPrice":        o.OriginalPrice,
		"averagePrice":         o.AveragePrice,
		"lastFilledQty":        o.LastFilledQty,
		"accumulatedFilledQty": o.AccumulatedFilledQty,
		"lastFilledPrice":      o.LastFilledPrice,
		"commission":           o.Commission,
		"realizedProfit":       o.RealizedProfit,
		"stopPrice":            o.StopPrice,
		"activationPrice":      o.ActivationPrice,
		"callbackRate":         o.CallbackRate,
	} {
		if err := validateSDKDecimal(field, raw); err != nil {
			return nil, fmt.Errorf("aster perp: order update %d: %w", o.OrderID, err)
		}
	}
	if strings.TrimSpace(o.ExecutionType) == "" {
		return nil, fmt.Errorf("aster perp: order update execution type is required")
	}
	side := sideFromAster(o.Side)
	if side == enums.SideUnknown {
		return nil, fmt.Errorf("aster perp: order update side %q is unsupported", o.Side)
	}
	orderType := orderTypeFromAster(o.OrderType)
	if orderType == enums.TypeUnknown {
		return nil, fmt.Errorf("aster perp: order update type %q is unsupported", o.OrderType)
	}
	tif := tifFromAster(o.TimeInForce)
	if tif == enums.TifUnknown {
		return nil, fmt.Errorf("aster perp: order update TIF %q is unsupported", o.TimeInForce)
	}
	status := statusFromAster(o.OrderStatus)
	if status == enums.StatusUnknown {
		return nil, fmt.Errorf("aster perp: order update status %q is unsupported", o.OrderStatus)
	}
	if qty, err := parseRequiredSDKDecimal("originalQty", o.OriginalQty); err != nil || !qty.IsPositive() {
		return nil, fmt.Errorf("aster perp: order update quantity must be positive")
	}
	if price, err := parseRequiredSDKDecimal("originalPrice", o.OriginalPrice); err != nil || price.IsNegative() {
		return nil, fmt.Errorf("aster perp: order update price is invalid")
	}
	cumulative := dec(o.AccumulatedFilledQty)
	lastQty := dec(o.LastFilledQty)
	if cumulative.IsNegative() || lastQty.IsNegative() || cumulative.GreaterThan(dec(o.OriginalQty)) {
		return nil, fmt.Errorf("aster perp: order update fill quantities are invalid")
	}
	if dec(o.AveragePrice).IsNegative() || dec(o.LastFilledPrice).IsNegative() || dec(o.Commission).IsNegative() {
		return nil, fmt.Errorf("aster perp: order update price or fee is invalid")
	}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     o.ClientOrderID,
			Side:         side,
			Type:         orderType,
			TIF:          tif,
			Quantity:     dec(o.OriginalQty),
			Price:        dec(o.OriginalPrice),
			PositionSide: positionSideFromAster(o.PositionSide, dec(o.OriginalQty)),
			ReduceOnly:   o.IsReduceOnly,
		},
		VenueOrderID: strconv.FormatInt(o.OrderID, 10),
		Status:       status,
		FilledQty:    dec(o.AccumulatedFilledQty),
		AvgFillPrice: dec(o.AveragePrice),
		UpdatedAt:    firstNonZeroTime(timeFromMillis(o.TradeTime), timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
	}
	out := []contract.ExecEvent{contract.OrderEvent{Order: order}}
	switch o.ExecutionType {
	case "NEW", "CANCELED", "CALCULATED", "EXPIRED", "TRADE", "AMENDMENT":
	default:
		return nil, fmt.Errorf("aster perp: order update execution type %q is unsupported", o.ExecutionType)
	}
	if o.ExecutionType == "TRADE" {
		if !lastQty.IsPositive() || !dec(o.LastFilledPrice).IsPositive() {
			return nil, fmt.Errorf("aster perp: order update trade quantity and price must be positive")
		}
		if o.TradeID == 0 || o.TradeTime <= 0 {
			return nil, fmt.Errorf("aster perp: order update trade id and timestamp are required")
		}
		liquidity := enums.LiqTaker
		if o.IsMaker {
			liquidity = enums.LiqMaker
		}
		out = append(out, contract.FillEvent{Fill: model.Fill{
			AccountID:    accountID,
			InstrumentID: id,
			VenueOrderID: order.VenueOrderID,
			ClientID:     o.ClientOrderID,
			TradeID:      strconv.FormatInt(o.TradeID, 10),
			Side:         side,
			Liquidity:    liquidity,
			Price:        dec(o.LastFilledPrice),
			Quantity:     dec(o.LastFilledQty),
			Fee:          dec(o.Commission),
			FeeCurrency:  o.CommissionAsset,
			Timestamp:    firstNonZeroTime(timeFromMillis(o.TradeTime), timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
		}})
	}
	if order.Status == enums.StatusRejected {
		out = append(out, contract.RejectEvent{ClientID: o.ClientOrderID, Reason: o.OrderStatus})
	}
	return out, nil
}

func execEnvelopesFromOrderUpdate(ev *sdkperp.OrderUpdateEvent, resolve symbolResolver, accountID string) ([]contract.ExecEnvelope, error) {
	events, err := execEventsFromOrderUpdate(ev, resolve, accountID)
	if err != nil {
		return nil, err
	}
	out := make([]contract.ExecEnvelope, 0, len(events))
	for _, event := range events {
		meta := contract.EventMeta{
			EventID:   execEventID(ev, event),
			Source:    contract.SourceAdapterStream,
			Venue:     VenueName,
			AccountID: accountID,
			Sequence:  uint64(ev.Order.OrderID),
			TsVenue:   firstNonZeroTime(timeFromMillis(ev.Order.TradeTime), timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
			Flags:     contract.EventFlagFromStream,
		}
		out = append(out, contract.NewExecEnvelopeWithMeta(event, meta))
	}
	return out, nil
}

func execEventID(ev *sdkperp.OrderUpdateEvent, event contract.ExecEvent) model.EventID {
	kind := "exec"
	tradeID := strconv.FormatInt(ev.Order.TradeID, 10)
	switch event.(type) {
	case contract.OrderEvent:
		kind = "order"
	case contract.FillEvent:
		kind = "fill"
	case contract.RejectEvent:
		kind = "reject"
	}
	return model.EventID(fmt.Sprintf("aster|perp|%s|%s|%d|%s|%s|%s|%s|%s|%s|%d",
		kind, ev.Order.Symbol, ev.Order.OrderID, ev.Order.ClientOrderID, ev.Order.ExecutionType,
		ev.Order.OrderStatus, ev.Order.AccumulatedFilledQty, ev.Order.LastFilledQty, tradeID, ev.TransactionTime))
}

func accountEventsFromUpdate(ev *sdkperp.AccountUpdateEvent, resolve symbolResolver, accountID string) ([]contract.AccountEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("aster perp: account update event is required")
	}
	ts := firstNonZeroTime(timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime))
	if ts.IsZero() {
		return nil, fmt.Errorf("aster perp: account update timestamp is required")
	}
	var out []contract.AccountEvent
	for _, row := range ev.UpdateData.Balances {
		if row.Asset == "" {
			return nil, fmt.Errorf("aster perp: account update balance asset is required")
		}
		total, err := parseNonNegativeDecimal("walletBalance", row.WalletBalance)
		if err != nil {
			return nil, fmt.Errorf("aster perp: account update %s: %w", row.Asset, err)
		}
		if _, err := parseNonNegativeDecimal("crossWalletBalance", row.CrossWalletBalance); err != nil {
			return nil, fmt.Errorf("aster perp: account update %s: %w", row.Asset, err)
		}
		balance := model.AccountBalance{AccountID: accountID, Currency: row.Asset, Total: total, UpdatedAt: ts}
		out = append(out, contract.BalanceEvent{Balance: balance})
	}
	for _, row := range ev.UpdateData.Positions {
		id, ok := resolve(row.Symbol)
		if !ok || id.Symbol == "" {
			return nil, fmt.Errorf("aster perp: account update position symbol %q is unresolved", row.Symbol)
		}
		if side := strings.ToUpper(strings.TrimSpace(row.PositionSide)); side != "" && side != "BOTH" {
			return nil, fmt.Errorf("aster perp: account update hedge position side %q is unsupported", row.PositionSide)
		}
		for field, raw := range map[string]string{
			"positionAmount": row.PositionAmount,
			"entryPrice":     row.EntryPrice,
			"unrealizedPnL":  row.UnrealizedPnL,
		} {
			if err := validateSDKDecimal(field, raw); err != nil {
				return nil, fmt.Errorf("aster perp: account update position %s: %w", row.Symbol, err)
			}
		}
		out = append(out, contract.PositionEvent{Position: model.Position{
			AccountID:     accountID,
			InstrumentID:  id,
			Side:          positionSideFromAster(row.PositionSide, dec(row.PositionAmount)),
			Quantity:      dec(row.PositionAmount),
			EntryPrice:    dec(row.EntryPrice),
			UnrealizedPnL: dec(row.UnrealizedPnL),
			UpdatedAt:     ts,
		}})
	}
	return out, nil
}

func parseNonNegativeDecimal(field, raw string) (decimal.Decimal, error) {
	value, err := parseRequiredSDKDecimal(field, raw)
	if err != nil {
		return decimal.Zero, err
	}
	if value.IsNegative() {
		return decimal.Zero, fmt.Errorf("%s is negative", field)
	}
	return value, nil
}
