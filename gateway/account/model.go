package account

import (
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// Position is one open position. Size is always positive; Side says which way.
type Position struct {
	Instrument       venue.InstrumentID `json:"inst"`
	Side             string             `json:"side"` // long | short
	Size             string             `json:"size"`
	EntryPrice       string             `json:"entryPrice"`
	MarkPrice        string             `json:"markPrice,omitempty"`
	UnrealizedPnL    string             `json:"unrealizedPnl"`
	LiquidationPrice string             `json:"liquidationPrice,omitempty"`
	Leverage         string             `json:"leverage,omitempty"`
	Margin           string             `json:"margin,omitempty"`
	slot             exchange.PositionSlot
}

// Balance is one asset balance of one market of the account.
type Balance struct {
	Market    string `json:"market"`
	Asset     string `json:"asset"`
	Total     string `json:"total"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
}

// Order is the current state of one venue order.
type Order struct {
	Instrument    venue.InstrumentID `json:"inst"`
	OrderID       string             `json:"orderId"`
	ClientOrderID string             `json:"clientOrderId,omitempty"`
	Side          string             `json:"side"`
	Type          string             `json:"type"`
	Price         string             `json:"price,omitempty"`
	Quantity      string             `json:"qty"`
	Filled        string             `json:"filled"`
	AvgPrice      string             `json:"avgPrice,omitempty"`
	TimeInForce   string             `json:"tif,omitempty"`
	ReduceOnly    bool               `json:"reduceOnly,omitempty"`
	Status        string             `json:"status"`
	CreatedAt     int64              `json:"createdAt,omitempty"`
	UpdatedAt     int64              `json:"updatedAt,omitempty"`
}

// Fill is one execution.
type Fill struct {
	Instrument    venue.InstrumentID `json:"inst"`
	FillID        string             `json:"fillId"`
	OrderID       string             `json:"orderId"`
	ClientOrderID string             `json:"clientOrderId,omitempty"`
	Side          string             `json:"side"`
	Price         string             `json:"price"`
	Quantity      string             `json:"qty"`
	Fee           string             `json:"fee"`
	FeeAsset      string             `json:"feeAsset,omitempty"`
	Liquidity     string             `json:"liquidity,omitempty"`
	Time          int64              `json:"ts"`
}

func projectPosition(id venue.InstrumentID, position exchange.Position) (Position, bool) {
	if position.Quantity.IsZero() {
		return Position{}, false
	}
	side := "long"
	switch {
	case position.PositionSlot == exchange.PositionSlotShort:
		side = "short"
	case position.PositionSlot == exchange.PositionSlotLong:
		side = "long"
	case position.Quantity.IsNegative() || position.Side == exchange.SideSell:
		side = "short"
	}
	result := Position{
		Instrument:    id,
		Side:          side,
		Size:          position.Quantity.Abs().String(),
		EntryPrice:    position.EntryPrice.String(),
		UnrealizedPnL: position.UnrealizedPnL.String(),
		slot:          position.PositionSlot,
	}
	if position.MarkPrice.IsPositive() {
		result.MarkPrice = position.MarkPrice.String()
	}
	if position.LiquidationPrice.Valid && position.LiquidationPrice.Value.IsPositive() {
		result.LiquidationPrice = position.LiquidationPrice.Value.String()
	}
	if position.Leverage.Valid && position.Leverage.Value.IsPositive() {
		result.Leverage = position.Leverage.Value.String()
	}
	if position.MarginUsed.Valid {
		result.Margin = position.MarginUsed.Value.String()
	}
	return result, true
}

func projectOrder(id venue.InstrumentID, order exchange.Order) Order {
	result := Order{
		Instrument:    id,
		OrderID:       order.OrderID,
		ClientOrderID: order.ClientOrderID,
		Side:          string(order.Side),
		Type:          string(order.Type),
		Quantity:      order.Quantity.String(),
		Filled:        order.Filled.String(),
		TimeInForce:   tifFromPolicy(order.LimitPolicy),
		ReduceOnly:    order.ReduceOnly,
		Status:        NormalizeStatus(order.Status),
	}
	if order.LimitPrice.IsPositive() {
		result.Price = order.LimitPrice.String()
	}
	if order.AverageFillPrice.Valid && order.AverageFillPrice.Value.IsPositive() {
		result.AvgPrice = order.AverageFillPrice.Value.String()
	}
	if !order.CreatedAt.IsZero() {
		result.CreatedAt = order.CreatedAt.UnixMilli()
	}
	if !order.UpdatedAt.IsZero() {
		result.UpdatedAt = order.UpdatedAt.UnixMilli()
	}
	return result
}

func projectFill(id venue.InstrumentID, fill exchange.Fill) Fill {
	return Fill{
		Instrument: id, FillID: fill.FillID, OrderID: fill.OrderID, ClientOrderID: fill.ClientOrderID,
		Side: string(fill.Side), Price: fill.Price.String(), Quantity: fill.Quantity.String(),
		Fee: fill.Fee.String(), FeeAsset: fill.FeeAsset, Liquidity: string(fill.Liquidity), Time: unixMilli(fill.Time),
	}
}

func unixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UnixMilli()
}

func tifFromPolicy(policy exchange.LimitPolicy) string {
	switch policy {
	case exchange.LimitPolicyIOC:
		return "ioc"
	case exchange.LimitPolicyPostOnly:
		return "post_only"
	case exchange.LimitPolicyResting:
		return "gtc"
	}
	return ""
}

// NormalizeStatus maps venue status words onto:
// open | partially_filled | filled | canceled | rejected | pending
func NormalizeStatus(raw string) string {
	switch status := normalizeWord(raw); status {
	case "new", "live", "open", "resting", "untriggered", "created":
		return "open"
	case "partially_filled", "partiallyfilled", "partial", "partially_filled_canceled":
		if status == "partially_filled_canceled" {
			return "canceled"
		}
		return "partially_filled"
	case "filled", "closed", "finished":
		return "filled"
	case "rejected", "failed":
		return "rejected"
	case "accepted", "accepted_pending", "pending", "in_progress", "pending_new":
		return "pending"
	default:
		if containsWord(status, "cancel") || status == "expired" {
			return "canceled"
		}
		if containsWord(status, "reject") {
			return "rejected"
		}
		if status == "" {
			return "open"
		}
		return status
	}
}

func normalizeWord(value string) string {
	out := make([]byte, 0, len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z':
			out = append(out, c+'a'-'A')
		case c == '-' || c == ' ':
			out = append(out, '_')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

func containsWord(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
