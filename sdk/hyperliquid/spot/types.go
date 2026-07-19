package spot

import (
	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// Request Types

type PlaceOrderRequest struct {
	AssetID       int
	IsBuy         bool
	Price         float64
	Size          float64
	OrderType     OrderType
	ClientOrderID *string
}

// MarketOrderRequest is the official SDK-style protected IOC operation. Asset
// id and precision are resolved from fresh Spot metadata inside the SDK.
type MarketOrderRequest struct {
	Coin          string
	IsBuy         bool
	Size          float64
	ClientOrderID *string
}

type PlaceOrderResponse struct {
	Statuses []OrderStatus `json:"statuses"`
}

type OrderStatus struct {
	Resting *OrderResting `json:"resting,omitempty"`
	Filled  *OrderFilled  `json:"filled,omitempty"`
	Error   *string       `json:"error,omitempty"`
}

type OrderResting struct {
	Oid      int64   `json:"oid"`
	ClientID *string `json:"cloid"`
	Status   string  `json:"status"`
}

type OrderFilled struct {
	TotalSz string `json:"totalSz"`
	AvgPx   string `json:"avgPx"`
	Oid     int    `json:"oid"`
}

type OrderType struct {
	Limit   *OrderTypeLimit
	Trigger *OrderTypeTrigger
}

type OrderTypeLimit struct {
	Tif hyperliquid.Tif
}

type OrderTypeTrigger struct {
	IsMarket  bool
	TriggerPx float64
	Tpsl      hyperliquid.Tpsl
}

type CancelOrderRequest struct {
	AssetID int
	OrderID int64
}

// Response Types (Reused from generic or defined here if specific)
// Spot responses are likely similar to Perp "statuses".

type ModifyOrderRequest struct {
	Oid   *int64
	Cloid *string
	Order PlaceOrderRequest
}

type ModifyOrderResponse struct {
	Statuses []OrderStatus `json:"statuses"`
}

type CancelOrderResponse struct {
	Statuses hyperliquid.MixedArray `json:"statuses"`
}

type Order struct {
	Coin       string `json:"coin"`
	Side       string `json:"side"`
	LimitPx    string `json:"limitPx"`
	Sz         string `json:"sz"`
	Oid        int64  `json:"oid"`
	Cliod      string `json:"cloid"`
	Timestamp  int64  `json:"timestamp"`
	OrigSz     string `json:"origSz"`
	ReduceOnly bool   `json:"reduceOnly"`
	OrderType  string `json:"orderType"`
	Tif        string `json:"tif"`
	IsTrigger  bool   `json:"isTrigger"`
	TriggerPx  string `json:"triggerPx"`
}

type OrderStatusInfo struct {
	Coin            string `json:"coin"`
	Side            string `json:"side"`
	LimitPx         string `json:"limitPx"`
	Sz              string `json:"sz"`
	Oid             int64  `json:"oid"`
	Cliod           string `json:"cloid"`
	Timestamp       int64  `json:"timestamp"`
	StatusTimestamp int64  `json:"statusTimestamp"`
	OrigSz          string `json:"origSz"`
	Status          string `json:"status"`
	FilledSz        string `json:"filledSz"`
	AvgPx           string `json:"avgPx"`
	CancelReason    string `json:"cancelReason"`
	ReduceOnly      bool   `json:"reduceOnly"`
	HasReduceOnly   bool   `json:"-"`
	OrderType       string `json:"orderType"`
	Tif             string `json:"tif"`
	IsTrigger       bool   `json:"isTrigger"`
	TriggerPx       string `json:"triggerPx"`
}

type OrderStatusQueryResponse struct {
	OrderStatus OrderStatusInfo `json:"order"`
}

type UserFill = hyperliquid.WsUserFill

type OutcomeMeta struct {
	Outcomes  []Outcome `json:"outcomes"`
	Questions []any     `json:"questions,omitempty"`
}

type Outcome struct {
	Outcome     int               `json:"outcome"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	SideSpecs   []OutcomeSideSpec `json:"sideSpecs"`
}

type OutcomeSideSpec struct {
	Name string `json:"name"`
}
