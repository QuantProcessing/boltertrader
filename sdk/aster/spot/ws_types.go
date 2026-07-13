package spot

// WsEventHeader Common header for all websocket events
type WsEventHeader struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
}

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
	TransactionTime   int64      `json:"T"`
	FirstUpdateID     int64      `json:"U"`
	FinalUpdateID     int64      `json:"u"`
	FinalUpdateIDLast int64      `json:"pu"`
	Bids              [][]string `json:"b"`
	Asks              [][]string `json:"a"`
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

type TradeEvent struct {
	WsEventHeader
	TradeID      int64  `json:"t"`
	Price        string `json:"p"`
	Quantity     string `json:"q"`
	TradeTime    int64  `json:"T"`
	IsBuyerMaker bool   `json:"m"`
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
	EventType                              string  `json:"e"`
	EventTime                              int64   `json:"E"`
	Symbol                                 string  `json:"s"`
	ClientOrderID                          string  `json:"c"`
	Side                                   string  `json:"S"`
	OrderType                              string  `json:"o"`
	TimeInForce                            string  `json:"f"`
	Quantity                               string  `json:"q"`
	Price                                  string  `json:"p"`
	AveragePrice                           string  `json:"ap"`
	StopPrice                              string  `json:"P"`
	ExecutionType                          string  `json:"x"`
	OrderStatus                            string  `json:"X"`
	OrderID                                int64   `json:"i"`
	LastExecutedQuantity                   string  `json:"l"`
	CumulativeFilledQuantity               string  `json:"z"`
	LastExecutedPrice                      string  `json:"L"`
	CommissionAmount                       string  `json:"n"`
	CommissionAsset                        *string `json:"N"`
	TransactionTime                        int64   `json:"T"`
	TradeID                                int64   `json:"t"`
	IsMaker                                bool    `json:"m"`
	OriginalOrderType                      string  `json:"ot"`
	CreationTime                           int64   `json:"O"`
	CumulativeQuoteAssetTransactedQuantity string  `json:"Z"`
	LastQuoteAssetTransactedQuantity       string  `json:"Y"`
	QuoteOrderQuantity                     string  `json:"Q"`
}

// AccountPositionEvent (OutboundAccountPosition)
type AccountPositionEvent struct {
	EventType         string `json:"e"`
	EventTime         int64  `json:"E"`
	LastAccountUpdate int64  `json:"T"`
	Reason            string `json:"m"`
	Balances          []struct {
		Asset  string `json:"a"`
		Free   string `json:"f"`
		Locked string `json:"l"`
	} `json:"B"`
}
