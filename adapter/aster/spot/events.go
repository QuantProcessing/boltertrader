package spot

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdkspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
	"github.com/shopspring/decimal"
)

type symbolResolver func(venueSymbol string) (model.InstrumentID, bool)

func execEventsFromExecutionReport(ev *sdkspot.ExecutionReportEvent, resolve symbolResolver, accountID string) ([]contract.ExecEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("aster spot: execution report is required")
	}
	id, ok := resolve(ev.Symbol)
	if !ok || id.Symbol == "" {
		return nil, fmt.Errorf("aster spot: execution report symbol %q is unresolved", ev.Symbol)
	}
	if ev.EventTime <= 0 || ev.TransactionTime <= 0 {
		return nil, fmt.Errorf("aster spot: execution report source timestamp is required")
	}
	if ev.ClientOrderID == "" || ev.OrderID == 0 {
		return nil, fmt.Errorf("aster spot: execution report client and order ids are required")
	}
	for field, raw := range map[string]string{
		"quantity":                 ev.Quantity,
		"price":                    ev.Price,
		"averagePrice":             ev.AveragePrice,
		"lastExecutedQuantity":     ev.LastExecutedQuantity,
		"cumulativeFilledQuantity": ev.CumulativeFilledQuantity,
		"lastExecutedPrice":        ev.LastExecutedPrice,
		"commissionAmount":         ev.CommissionAmount,
		"cumulativeQuote":          ev.CumulativeQuoteAssetTransactedQuantity,
		"stopPrice":                ev.StopPrice,
	} {
		if err := validateSDKDecimal(field, raw); err != nil {
			return nil, fmt.Errorf("aster spot: execution report %d: %w", ev.OrderID, err)
		}
	}
	if strings.TrimSpace(ev.ExecutionType) == "" {
		return nil, fmt.Errorf("aster spot: execution report execution type is required")
	}
	side := sideFromAster(ev.Side)
	if side == enums.SideUnknown {
		return nil, fmt.Errorf("aster spot: execution report side %q is unsupported", ev.Side)
	}
	orderType := orderTypeFromAster(ev.OrderType)
	if orderType == enums.TypeUnknown {
		return nil, fmt.Errorf("aster spot: execution report type %q is unsupported", ev.OrderType)
	}
	tif := tifFromAster(ev.TimeInForce)
	if tif == enums.TifUnknown {
		return nil, fmt.Errorf("aster spot: execution report TIF %q is unsupported", ev.TimeInForce)
	}
	status := statusFromAster(ev.OrderStatus)
	if status == enums.StatusUnknown {
		return nil, fmt.Errorf("aster spot: execution report status %q is unsupported", ev.OrderStatus)
	}
	if qty, err := parseRequiredSDKDecimal("quantity", ev.Quantity); err != nil || !qty.IsPositive() {
		return nil, fmt.Errorf("aster spot: execution report quantity must be positive")
	}
	if price, err := parseRequiredSDKDecimal("price", ev.Price); err != nil || price.IsNegative() {
		return nil, fmt.Errorf("aster spot: execution report price is invalid")
	}
	cumulative := dec(ev.CumulativeFilledQuantity)
	lastQty := dec(ev.LastExecutedQuantity)
	if cumulative.IsNegative() || lastQty.IsNegative() || cumulative.GreaterThan(dec(ev.Quantity)) {
		return nil, fmt.Errorf("aster spot: execution report fill quantities are invalid")
	}
	if dec(ev.AveragePrice).IsNegative() || dec(ev.LastExecutedPrice).IsNegative() || dec(ev.CommissionAmount).IsNegative() {
		return nil, fmt.Errorf("aster spot: execution report price or fee is invalid")
	}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    accountID,
			InstrumentID: id,
			ClientID:     ev.ClientOrderID,
			Side:         side,
			Type:         orderType,
			TIF:          tif,
			Quantity:     dec(ev.Quantity),
			Price:        dec(ev.Price),
			TriggerPrice: dec(ev.StopPrice),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: strconv.FormatInt(ev.OrderID, 10),
		Status:       status,
		FilledQty:    dec(ev.CumulativeFilledQuantity),
		AvgFillPrice: firstNonZero(dec(ev.AveragePrice), avgFillPrice(dec(ev.CumulativeFilledQuantity), dec(ev.CumulativeQuoteAssetTransactedQuantity))),
		UpdatedAt:    firstNonZeroTime(timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
	}
	out := []contract.ExecEvent{contract.OrderEvent{Order: order}}
	switch ev.ExecutionType {
	case "NEW", "CANCELED", "REPLACED", "REJECTED", "TRADE", "EXPIRED", "TRADE_PREVENTION":
	default:
		return nil, fmt.Errorf("aster spot: execution report execution type %q is unsupported", ev.ExecutionType)
	}
	if ev.ExecutionType == "TRADE" {
		if !lastQty.IsPositive() || !dec(ev.LastExecutedPrice).IsPositive() {
			return nil, fmt.Errorf("aster spot: execution report trade quantity and price must be positive")
		}
		if ev.TradeID == 0 {
			return nil, fmt.Errorf("aster spot: execution report trade id is required")
		}
		feeCurrency := ""
		if ev.CommissionAsset != nil {
			feeCurrency = *ev.CommissionAsset
		}
		liquidity := enums.LiqTaker
		if ev.IsMaker {
			liquidity = enums.LiqMaker
		}
		out = append(out, contract.FillEvent{Fill: model.Fill{
			AccountID:    accountID,
			InstrumentID: id,
			VenueOrderID: order.VenueOrderID,
			ClientID:     ev.ClientOrderID,
			TradeID:      strconv.FormatInt(ev.TradeID, 10),
			Side:         side,
			Liquidity:    liquidity,
			Price:        dec(ev.LastExecutedPrice),
			Quantity:     dec(ev.LastExecutedQuantity),
			Fee:          dec(ev.CommissionAmount),
			FeeCurrency:  feeCurrency,
			Timestamp:    firstNonZeroTime(timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
		}})
	}
	if order.Status == enums.StatusRejected {
		out = append(out, contract.RejectEvent{ClientID: ev.ClientOrderID, Reason: ev.OrderStatus})
	}
	return out, nil
}

func execEnvelopesFromExecutionReport(ev *sdkspot.ExecutionReportEvent, resolve symbolResolver, accountID string) ([]contract.ExecEnvelope, error) {
	events, err := execEventsFromExecutionReport(ev, resolve, accountID)
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
			Sequence:  uint64(ev.OrderID),
			TsVenue:   firstNonZeroTime(timeFromMillis(ev.TransactionTime), timeFromMillis(ev.EventTime)),
			Flags:     contract.EventFlagFromStream,
		}
		out = append(out, contract.NewExecEnvelopeWithMeta(event, meta))
	}
	return out, nil
}

func execEventID(ev *sdkspot.ExecutionReportEvent, event contract.ExecEvent) model.EventID {
	kind := "exec"
	switch event.(type) {
	case contract.OrderEvent:
		kind = "order"
	case contract.FillEvent:
		kind = "fill"
	case contract.RejectEvent:
		kind = "reject"
	}
	return model.EventID(fmt.Sprintf("aster|spot|%s|%s|%d|%s|%s|%s|%s|%s|%d|%d",
		kind, ev.Symbol, ev.OrderID, ev.ClientOrderID, ev.ExecutionType, ev.OrderStatus,
		ev.CumulativeFilledQuantity, ev.LastExecutedQuantity, ev.TradeID, ev.TransactionTime))
}

func accountEventsFromAccountPosition(ev *sdkspot.AccountPositionEvent, accountID string) ([]contract.AccountEvent, error) {
	if ev == nil {
		return nil, fmt.Errorf("aster spot: account position event is required")
	}
	ts := firstNonZeroTime(timeFromMillis(ev.LastAccountUpdate), timeFromMillis(ev.EventTime))
	if ts.IsZero() {
		return nil, fmt.Errorf("aster spot: account position timestamp is required")
	}
	out := make([]contract.AccountEvent, 0, len(ev.Balances))
	for _, row := range ev.Balances {
		if row.Asset == "" {
			return nil, fmt.Errorf("aster spot: account position balance asset is required")
		}
		free, err := parseNonNegativeDecimal("free", row.Free)
		if err != nil {
			return nil, fmt.Errorf("aster spot: account position %s: %w", row.Asset, err)
		}
		locked, err := parseNonNegativeDecimal("locked", row.Locked)
		if err != nil {
			return nil, fmt.Errorf("aster spot: account position %s: %w", row.Asset, err)
		}
		balance := model.AccountBalance{AccountID: accountID, Currency: row.Asset, Total: free.Add(locked), Free: free, Locked: locked, UpdatedAt: ts}
		if err := balance.ValidateCash(); err != nil {
			return nil, err
		}
		out = append(out, contract.BalanceEvent{Balance: balance})
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
