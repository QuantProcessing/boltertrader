package hyperliquid

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// PublicTrade is one recent public execution returned by the info endpoint.
type PublicTrade struct {
	Coin    string   `json:"coin"`
	Side    Side     `json:"side"`
	Price   string   `json:"px"`
	Size    string   `json:"sz"`
	Hash    string   `json:"hash"`
	Time    int64    `json:"time"`
	TradeID int64    `json:"tid"`
	Users   []string `json:"users"`
	TwapID  *int64   `json:"twapId,omitempty"`
}

// HistoricalOrder is one of the most recent terminal or active order records
// returned by Hyperliquid's historicalOrders info request.
type HistoricalOrder struct {
	Order           HistoricalOrderDetails `json:"order"`
	Status          string                 `json:"status"`
	StatusTimestamp int64                  `json:"statusTimestamp"`
}

type HistoricalOrderDetails struct {
	Coin             string  `json:"coin"`
	Side             Side    `json:"side"`
	LimitPrice       string  `json:"limitPx"`
	RemainingSize    string  `json:"sz"`
	OrderID          int64   `json:"oid"`
	ClientOrderID    *string `json:"cloid"`
	Timestamp        int64   `json:"timestamp"`
	OriginalSize     string  `json:"origSz"`
	ReduceOnly       bool    `json:"reduceOnly"`
	OrderType        string  `json:"orderType"`
	TimeInForce      string  `json:"tif"`
	IsTrigger        bool    `json:"isTrigger"`
	TriggerPrice     string  `json:"triggerPx"`
	TriggerCondition string  `json:"triggerCondition"`
}

// RecentTrades returns the venue-bounded recent public executions for coin.
func (c *Client) RecentTrades(ctx context.Context, coin string) ([]PublicTrade, error) {
	coin = strings.TrimSpace(coin)
	if coin == "" {
		return nil, ValidationError{Field: "coin", Message: "must not be empty"}
	}
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "recentTrades",
		"coin": coin,
	})
	if err != nil {
		return nil, err
	}
	var trades []PublicTrade
	if err := json.Unmarshal(data, &trades); err != nil {
		return nil, fmt.Errorf("hyperliquid recent trades: %w", err)
	}
	return trades, nil
}

// HistoricalOrders returns at most the venue-defined recent history window for
// user. Product filtering remains the responsibility of Spot/Perp callers.
func (c *Client) HistoricalOrders(ctx context.Context, user string) ([]HistoricalOrder, error) {
	user = strings.TrimSpace(user)
	if user == "" {
		return nil, ValidationError{Field: "user", Message: "must not be empty"}
	}
	data, err := c.Post(ctx, "/info", map[string]string{
		"type": "historicalOrders",
		"user": user,
	})
	if err != nil {
		return nil, err
	}
	var orders []HistoricalOrder
	if err := json.Unmarshal(data, &orders); err != nil {
		return nil, fmt.Errorf("hyperliquid historical orders: %w", err)
	}
	return orders, nil
}

const defaultMarketSlippage = 0.05

// ProtectedMarketPrice mirrors the official SDK market helper: apply a 5%
// directional protection, round to five significant figures, then apply the
// venue's product precision rule.
func ProtectedMarketPrice(mid float64, isBuy, isSpot bool, sizeDecimals int) (float64, error) {
	maxPriceDecimals := 6
	if isSpot {
		maxPriceDecimals = 8
	}
	if math.IsNaN(mid) || math.IsInf(mid, 0) || mid <= 0 {
		return 0, fmt.Errorf("%w: non-positive or non-finite mid", ErrMarketReferenceMalformed)
	}
	if sizeDecimals < 0 || sizeDecimals > maxPriceDecimals {
		return 0, fmt.Errorf("%w: invalid size decimals", ErrMarketReferenceMalformed)
	}

	protected := mid * (1 - defaultMarketSlippage)
	if isBuy {
		protected = mid * (1 + defaultMarketSlippage)
	}
	significant, err := strconv.ParseFloat(strconv.FormatFloat(protected, 'g', 5, 64), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid protected price", ErrMarketReferenceMalformed)
	}
	factor := math.Pow10(maxPriceDecimals - sizeDecimals)
	rounded := math.RoundToEven(significant*factor) / factor
	if math.IsNaN(rounded) || math.IsInf(rounded, 0) || rounded <= 0 {
		return 0, fmt.Errorf("%w: invalid rounded protected price", ErrMarketReferenceMalformed)
	}
	return rounded, nil
}
