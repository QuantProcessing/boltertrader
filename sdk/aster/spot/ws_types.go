package spot

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// WsEventHeader Common header for all websocket events
type WsEventHeader struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
}

var SingleEventMap = map[string]string{
	"depthUpdate":     "depth@100ms",  // only 100ms, its best choice
	"markPriceUpdate": "markPrice@1s", // only use 1s, its best than 3s
	"24hrTicker":      "ticker",
	"24hrMiniTicker":  "miniTicker",
	"bookTicker":      "bookTicker",
	"aggTrade":        "aggTrade",
	"trade":           "trade",
}

// BookTickerEvent
type BookTickerEvent struct {
	UpdateID     int64  `json:"u"`
	Symbol       string `json:"s"`
	BestBidPrice string `json:"b"`
	BestBidQty   string `json:"B"`
	BestAskPrice string `json:"a"`
	BestAskQty   string `json:"A"`
}

// DepthEvent
type DepthEvent struct {
	WsEventHeader
	FirstUpdateID int64      `json:"U"`
	FinalUpdateID int64      `json:"u"`
	Bids          [][]string `json:"b"`
	Asks          [][]string `json:"a"`
}

// AggTradeEvent
type AggTradeEvent struct {
	WsEventHeader
	AggTradeID   int64  `json:"a"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	FirstTradeID int64  `json:"f"`
	LastTradeID  int64  `json:"l"`
	TradeTime    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
	Ignore       bool   `json:"M"`
}

// KlineEvent
type KlineEvent struct {
	WsEventHeader
	Kline struct {
		StartTime           int64  `json:"t"`
		CloseTime           int64  `json:"T"`
		Symbol              string `json:"s"`
		Interval            string `json:"i"`
		FirstTradeID        int64  `json:"f"`
		LastTradeID         int64  `json:"L"`
		OpenPrice           string `json:"o"`
		ClosePrice          string `json:"c"`
		HighPrice           string `json:"h"`
		LowPrice            string `json:"l"`
		Volume              string `json:"v"`
		NumberOfTrades      int64  `json:"n"`
		IsClosed            bool   `json:"x"`
		QuoteVolume         string `json:"q"`
		TakerBuyBaseVolume  string `json:"V"`
		TakerBuyQuoteVolume string `json:"Q"`
		Ignore              string `json:"B"`
	} `json:"k"`
}

// ExecutionReportEvent
type ExecutionReportEvent struct {
	EventType                              string `json:"e"`
	EventTime                              int64  `json:"E"`
	Symbol                                 string `json:"s"`
	ClientOrderID                          string `json:"c"`
	Side                                   string `json:"S"`
	OrderType                              string `json:"o"`
	TimeInForce                            string `json:"f"`
	Quantity                               string `json:"q"`
	Price                                  string `json:"p"`
	StopPrice                              string `json:"P"`
	IcebergQuantity                        string `json:"F"`
	OrderListID                            int64  `json:"g"` // OCO
	OriginalClientOrderID                  string `json:"C"`
	ExecutionType                          string `json:"x"`
	OrderStatus                            string `json:"X"`
	RejectReason                           string `json:"r"`
	OrderID                                int64  `json:"i"`
	LastExecutedQuantity                   string `json:"l"`
	CumulativeFilledQuantity               string `json:"z"`
	LastExecutedPrice                      string `json:"L"`
	CommissionAmount                       string `json:"n"`
	CommissionAsset                        string `json:"N"`
	TransactionTime                        int64  `json:"T"`
	TradeID                                int64  `json:"t"`
	Ignore                                 int64  `json:"I"` // Ignore
	IsOrderWorking                         bool   `json:"w"` // Note: check case sensitive
	IsMaker                                bool   `json:"m"`
	Ignore2                                bool   `json:"M"`
	CreationTime                           int64  `json:"O"`
	CumulativeQuoteAssetTransactedQuantity string `json:"Z"`
	LastQuoteAssetTransactedQuantity       string `json:"Y"`
	QuoteOrderQuantity                     string `json:"Q"`

	// Working time might be missing or different key
	WorkingTime int64 `json:"W"`
	// SelfTradePreventionMode
	SelfTradePreventionMode string `json:"V"`
}

// AccountPositionEvent (OutboundAccountPosition)
type AccountPositionEvent struct {
	EventType         string `json:"e"`
	EventTime         int64  `json:"E"`
	LastAccountUpdate int64  `json:"u"`
	Balances          []struct {
		Asset  string `json:"a"`
		Free   string `json:"f"`
		Locked string `json:"l"`
	} `json:"B"`
}

// Helper to handle mixed number/string unmarshalling if needed
type FlexibleFloat string

func (f *FlexibleFloat) UnmarshalJSON(data []byte) error {
	// Try string first
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*f = FlexibleFloat(s)
		return nil
	}
	// Try number
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexibleFloat(fmt.Sprintf("%f", n)) // Or utilize appropriate precision
		return nil
	}
	return fmt.Errorf("cannot unmarshal to FlexibleFloat")
}

func (f FlexibleFloat) Float64() float64 {
	val, _ := strconv.ParseFloat(string(f), 64)
	return val
}
