package spot

import "time"

const (
	PongInterval         = 3 * time.Minute
	ReconnectWait        = 1 * time.Second
	MaxReconnectAttempts = 10
)

var ArrayEventMap = map[string]string{
	"depthUpdate": "!depth@arr@100ms", // Verify if this is correct for Spot. Spot uses <symbol>@depth<levels> usually. But for array?
	// Spot doesn't really have "array" streams for all tickers the same way Perp does for everything.
	// Spot has !ticker@arr, !miniTicker@arr.
	"24hrTicker":     "!ticker@arr",
	"24hrMiniTicker": "!miniTicker@arr",
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

// DepthEvent from ws_dispatchers.go
type DepthEvent struct {
	EventType     string     `json:"e"`
	EventTime     int64      `json:"E"`
	Symbol        string     `json:"s"`
	FirstUpdateID int64      `json:"U"`
	FinalUpdateID int64      `json:"u"`
	Bids          [][]string `json:"b"`
	Asks          [][]string `json:"a"`
}

type WsDepthEvent struct {
	EventType         string     `json:"e"`
	EventTime         int64      `json:"E"`
	TransactionTime   int64      `json:"T"`
	Symbol            string     `json:"s"`
	FirstUpdateID     int64      `json:"U"`
	FinalUpdateID     int64      `json:"u"`
	FinalUpdateIDLast int64      `json:"pu"`
	Bids              [][]string `json:"b"`
	Asks              [][]string `json:"a"`
}

// TradeEvent from ws_dispatchers.go
type TradeEvent struct {
	EventType     string `json:"e"`
	EventTime     int64  `json:"E"`
	Symbol        string `json:"s"`
	TradeID       int64  `json:"t"`
	Price         string `json:"p"`
	Quantity      string `json:"q"`
	BuyerOrderID  int64  `json:"b"`
	SellerOrderID int64  `json:"a"`
	TradeTime     int64  `json:"T"`
	IsBuyerMaker  bool   `json:"m"`
	Ignore        bool   `json:"M"`
}

// KlineEvent from ws_dispatchers.go
type KlineEvent struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Kline     struct {
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

// ExecutionReportEvent from ws_dispatchers.go (with fields that caused issues removed or fixed)
// We will use the robust definition we discovered during debugging if possible,
// or just standard string/int/float pointers if unsure.
// But for now, let's use the one from `ws_dispatchers.go` but be careful.
// Actually, `ws_dispatchers.go` had `EventType` `json:"e"`.
// Let's copy it but maybe use `json.Number` or `string` for ambiguous fields if needed.
// However, the issue before was "number into .e" which was strange.
// Let's use `interface{}` for potentially varying fields if we want, but better to be strict if possible.
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
	OrderListID                            int64  `json:"g"`
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
	Ignore                                 int64  `json:"I"`
	IsOrderWorking                         bool   `json:"w"`
	IsMaker                                bool   `json:"m"`
	Ignore2                                bool   `json:"M"`
	CreationTime                           int64  `json:"O"`
	CumulativeQuoteAssetTransactedQuantity string `json:"Z"`
	LastQuoteAssetTransactedQuantity       string `json:"Y"`
	QuoteOrderQuantity                     string `json:"Q"`
	WorkingTime                            int64  `json:"W"`
	SelfTradePreventionMode                string `json:"V"`
}

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

// BookTickerEvent
type BookTickerEvent struct {
	UpdateID     int64  `json:"u"`
	Symbol       string `json:"s"`
	BestBidPrice string `json:"b"`
	BestBidQty   string `json:"B"`
	BestAskPrice string `json:"a"`
	BestAskQty   string `json:"A"`
}

// AggTradeEvent
type AggTradeEvent struct {
	EventType    string `json:"e"`
	EventTime    int64  `json:"E"`
	Symbol       string `json:"s"`
	AggTradeID   int64  `json:"a"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	FirstTradeID int64  `json:"f"`
	LastTradeID  int64  `json:"l"`
	TradeTime    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
	Ignore       bool   `json:"M"`
}

// WsMiniTickerEvent
type WsMiniTickerEvent struct {
	EventType   string `json:"e"`
	EventTime   int64  `json:"E"`
	Symbol      string `json:"s"`
	ClosePrice  string `json:"c"`
	OpenPrice   string `json:"o"`
	HighPrice   string `json:"h"`
	LowPrice    string `json:"l"`
	Volume      string `json:"v"`
	QuoteVolume string `json:"q"`
}
