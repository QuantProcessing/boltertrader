package spot

// APIError represents a Binance API error
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"msg"`
}

func (e *APIError) Error() string {
	return e.Message
}

// Common response types

type ServerTimeResponse struct {
	ServerTime int64 `json:"serverTime"`
}

type ExchangeInfoResponse struct {
	Timezone   string `json:"timezone"`
	ServerTime int64  `json:"serverTime"`
	Symbols    []struct {
		Symbol             string                   `json:"symbol"`
		Status             string                   `json:"status"`
		BaseAsset          string                   `json:"baseAsset"`
		QuoteAsset         string                   `json:"quoteAsset"`
		PricePrecision     int                      `json:"pricePrecision"`
		QuantityPrecision  int                      `json:"quantityPrecision"`
		BaseAssetPrecision int                      `json:"baseAssetPrecision"`
		QuotePrecision     int                      `json:"quotePrecision"`
		Filters            []map[string]interface{} `json:"filters"`
	} `json:"symbols"`
}

// WebSocket Events

type WsDepthEvent struct {
	EventType         string          `json:"e"`
	EventTime         int64           `json:"E"`
	TransactionTime   int64           `json:"T"`
	Symbol            string          `json:"s"`
	FirstUpdateID     int64           `json:"U"`
	FinalUpdateID     int64           `json:"u"`
	FinalUpdateIDLast int64           `json:"pu"` // Spot has pu? Yes usually.
	Bids              [][]interface{} `json:"b"`
	Asks              [][]interface{} `json:"a"`
}

type WsBookTickerEvent struct {
	EventType    string `json:"e"`
	EventTime    int64  `json:"E"`
	UpdateID     int64  `json:"u"`
	Symbol       string `json:"s"`
	BestBidPrice string `json:"b"`
	BestBidQty   string `json:"B"`
	BestAskPrice string `json:"a"`
	BestAskQty   string `json:"A"`
}

type WsAggTradeEvent struct {
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

type WsKlineEvent struct {
	EventType string `json:"e"`
	EventTime int64  `json:"E"`
	Symbol    string `json:"s"`
	Kline     struct {
		StartTime           int64  `json:"t"`
		EndTime             int64  `json:"T"`
		Symbol              string `json:"s"`
		Interval            string `json:"i"`
		FirstTradeID        int64  `json:"f"`
		LastTradeID         int64  `json:"L"`
		OpenPrice           string `json:"o"`
		ClosePrice          string `json:"c"`
		HighPrice           string `json:"h"`
		LowPrice            string `json:"l"`
		Volume              string `json:"v"`
		QuoteVolume         string `json:"q"`
		IsClosed            bool   `json:"x"`
		QuoteAssetVolume    string `json:"Q"`
		TakerBuyBaseVolume  string `json:"V"`
		TakerBuyQuoteVolume string `json:"n"`
	} `json:"k"`
}

type WsExecutionReportEvent struct {
	EventType           string `json:"e"`
	EventTime           int64  `json:"E"`
	Symbol              string `json:"s"`
	ClientOrderID       string `json:"c"`
	Side                string `json:"S"`
	OrderType           string `json:"o"`
	TimeInForce         string `json:"f"`
	Quantity            string `json:"q"`
	Price               string `json:"p"`
	StopPrice           string `json:"P"`
	IcebergQuantity     string `json:"F"`
	OrderListID         int64  `json:"g"`
	OriginalClientID    string `json:"C"` // Original client order ID; This is the ID of the order being canceled
	ExecutionType       string `json:"x"`
	OrderStatus         string `json:"X"`
	RejectReason        string `json:"r"`
	OrderID             int64  `json:"i"`
	LastExecutedQty     string `json:"l"`
	CumulativeFilledQty string `json:"z"`
	LastExecutedPrice   string `json:"L"`
	CommissionAmount    string `json:"n"`
	CommissionAsset     string `json:"N"`
	TransactionTime     int64  `json:"T"`
	TradeID             int64  `json:"t"`
	Ignore              int64  `json:"I"`
	IsOrderWorking      bool   `json:"w"`
	IsMaker             bool   `json:"m"`
	CreationTime        int64  `json:"O"`
	CumulativeQuoteQty  string `json:"Z"`
	LastQuoteQty        string `json:"Y"`
	QuoteOrderQty       string `json:"Q"`
}

type WsAccountUpdateEvent struct {
	EventType            string `json:"e"`
	EventTime            int64  `json:"E"`
	MakerCommissionRate  int64  `json:"m"`
	TakerCommissionRate  int64  `json:"t"`
	BuyerCommissionRate  int64  `json:"b"`
	SellerCommissionRate int64  `json:"s"`
	CanTrade             bool   `json:"T"`
	CanWithdraw          bool   `json:"W"`
	CanDeposit           bool   `json:"D"`
	LastUpdateTime       int64  `json:"u"`
	Balances             []struct {
		Asset  string `json:"a"`
		Free   string `json:"f"`
		Locked string `json:"l"`
	} `json:"B"`
}
