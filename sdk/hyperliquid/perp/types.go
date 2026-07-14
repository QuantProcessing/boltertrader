package perp

import (
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// --- Action Wire Types moved to hyperliquid/action_types.go ---
// OrderStatusValue type and constants are in parent hyperliquid/sdk package (ws_types.go)

// --- High Level Types (from order.go) ---

type PlaceOrderRequest struct {
	AssetID       int
	IsBuy         bool
	Price         float64
	Size          float64
	ReduceOnly    bool
	ClientOrderID *string
	OrderType     OrderType
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

type ModifyOrderRequest struct {
	Oid   *int64
	Cloid *string
	Order PlaceOrderRequest
}

type ModifyOrderResponse struct {
	Statuses []OrderStatus `json:"statuses"`
}

type CancelOrderRequest struct {
	AssetID int
	OrderID int64
}

type CancelOrderResponse struct {
	Statuses hyperliquid.MixedArray `json:"statuses"`
}

// --- From client.go ---

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

type UserFill struct {
	Coin          string `json:"coin"`
	Px            string `json:"px"`
	Sz            string `json:"sz"`
	Side          string `json:"side"`
	Time          int64  `json:"time"`
	StartPosition string `json:"startPosition"`
	Dir           string `json:"dir"`
	ClosedPnl     string `json:"closedPnl"`
	Hash          string `json:"hash"`
	Oid           int64  `json:"oid"`
	Crossed       bool   `json:"crossed"`
	Fee           string `json:"fee"`
	FeeToken      string `json:"feeToken"`
	Tid           int64  `json:"tid"`
}

// OrderStatusInfo (renamed from client.go because OrderStatus is used above for place response statuses... waiting, conflict?)
// client.go had OrderStatusInfo.
// PlaceOrderResponse has OrderStatus.
// I will keep OrderStatusInfo for the query response.

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

// OrderStatusQueryResponse
type OrderStatusQueryResponse struct {
	OrderStatus OrderStatusInfo `json:"order"`
}

// PrepMeta
type PrepMeta struct {
	Universe []struct {
		Name        string `json:"name"`
		SzDecimals  int    `json:"szDecimals"`
		MaxLeverage int    `json:"maxLeverage"`
	} `json:"universe"`
	CollateralToken int `json:"collateralToken,omitempty"`
}

type PerpDex struct {
	Index    int    `json:"-"`
	Name     string `json:"name"`
	FullName string `json:"fullName,omitempty"`
}

// PerpPosition
type PerpPosition struct {
	AssetPositions []struct {
		Position struct {
			Coin       string `json:"coin"`
			CumFunding struct {
				AllTime     string `json:"allTime"`
				SinceOpen   string `json:"sinceOpen"`
				SinceChange string `json:"sinceChange"`
			} `json:"cumFunding"`
			EntryPx  string `json:"entryPx"`
			Leverage struct {
				RawUsd string `json:"rawUsd"`
				Type   string `json:"type"`
				Value  int    `json:"value"`
			} `json:"leverage"`
			LiquidationPx  string `json:"liquidationPx"`
			MarginUsed     string `json:"marginUsed"`
			MaxLeverage    int    `json:"maxLeverage"`
			PositionValue  string `json:"positionValue"`
			ReturnOnEquity string `json:"returnOnEquity"`
			Szi            string `json:"szi"`
			UnrealizedPnl  string `json:"unrealizedPnl"`
		} `json:"position"`
		Type string `json:"type"`
	} `json:"assetPositions"`
	CrossMaintenanceMarginUsed string `json:"crossMaintenanceMarginUsed"`
	CrossMarginSummary         struct {
		AccountValue    string `json:"accountValue"`
		TotalMarginUsed string `json:"totalMarginUsed"`
		TotalNtlPos     string `json:"totalNtlPos"`
		TotalRawUsd     string `json:"totalRawUsd"`
	} `json:"crossMarginSummary"`
	MarginSummary struct {
		AccountValue    string `json:"accountValue"`
		TotalMarginUsed string `json:"totalMarginUsed"`
		TotalNtlPos     string `json:"totalNtlPos"`
		TotalRawUsd     string `json:"totalRawUsd"`
	} `json:"marginSummary"`
	Time         int64  `json:"time"`
	Withdrawable string `json:"withdrawable"`
}

// UpdateLeverage
type UpdateLeverageRequest struct {
	AssetID  int
	IsCross  bool
	Leverage int
}

type UpdateLeverageResponse struct {
	Type string `json:"type"`
}

// UpdateIsolatedMargin
type UpdateIsolatedMarginRequest struct {
	AssetID int
	IsBuy   bool
	Amount  float64
}

type UpdateIsolatedMarginResponse struct {
	Type string `json:"type"`
}

// MetaAndAssetCtxs - Response for metaAndAssetCtxs endpoint
type MetaAndAssetCtxsResponse []AssetContext

type AssetContext struct {
	Funding      string   `json:"funding"` // Current funding rate (hourly)
	MarkPx       string   `json:"markPx"`  // Mark price
	OpenInterest string   `json:"openInterest"`
	PrevDayPx    string   `json:"prevDayPx"`
	DayNtlVlm    string   `json:"dayNtlVlm"` // Daily notional volume
	Premium      string   `json:"premium"`
	OraclePx     string   `json:"oraclePx"`
	MidPx        string   `json:"midPx"`
	ImpactPxs    []string `json:"impactPxs,omitempty"`
	DayBaseVlm   string   `json:"dayBaseVlm,omitempty"` // Daily base volume
}

// FundingRate is a single asset context's funding fields plus the matched coin.
type FundingRate struct {
	Coin     string `json:"coin"`
	Funding  string `json:"funding"`
	MarkPx   string `json:"markPx"`
	OraclePx string `json:"oraclePx"`
	Premium  string `json:"premium"`
}
