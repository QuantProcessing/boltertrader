package perp

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
	Timezone        string        `json:"timezone"`
	ServerTime      int64         `json:"serverTime"`
	RateLimits      []interface{} `json:"rateLimits"`
	ExchangeFilters []interface{} `json:"exchangeFilters"`
	Symbols         []SymbolInfo  `json:"symbols"`
}

type SymbolInfo struct {
	Symbol                string                   `json:"symbol"`
	Pair                  string                   `json:"pair"`
	ContractType          string                   `json:"contractType"`
	DeliveryDate          int64                    `json:"deliveryDate"`
	OnboardDate           int64                    `json:"onboardDate"`
	Status                string                   `json:"status"`
	MaintMarginPercent    string                   `json:"maintMarginPercent"`
	RequiredMarginPercent string                   `json:"requiredMarginPercent"`
	BaseAsset             string                   `json:"baseAsset"`
	QuoteAsset            string                   `json:"quoteAsset"`
	MarginAsset           string                   `json:"marginAsset"`
	PricePrecision        int                      `json:"pricePrecision"`
	QuantityPrecision     int                      `json:"quantityPrecision"`
	BaseAssetPrecision    int                      `json:"baseAssetPrecision"`
	QuotePrecision        int                      `json:"quotePrecision"`
	UnderlyingType        string                   `json:"underlyingType"`
	UnderlyingSubType     []string                 `json:"underlyingSubType"`
	SettlePlan            int64                    `json:"settlePlan"`
	TriggerProtect        string                   `json:"triggerProtect"`
	Filters               []map[string]interface{} `json:"filters"`
	OrderType             []string                 `json:"orderType"`
	TimeInForce           []string                 `json:"timeInForce"`
	LiquidationFee        string                   `json:"liquidationFee"`
	MarketTakeBound       string                   `json:"marketTakeBound"`
}

// WebSocket Events

type WsMarkPriceEvent struct {
	EventType            string `json:"e"`
	EventTime            int64  `json:"E"`
	Symbol               string `json:"s"`
	MarkPrice            string `json:"p"`
	IndexPrice           string `json:"i"`
	EstimatedSettlePrice string `json:"P"`
	FundingRate          string `json:"r"`
	NextFundingTime      int64  `json:"T"`
}

type WsDepthEvent struct {
	EventType         string          `json:"e"`
	EventTime         int64           `json:"E"`
	TransactionTime   int64           `json:"T"`
	Symbol            string          `json:"s"`
	FirstUpdateID     int64           `json:"U"`
	FinalUpdateID     int64           `json:"u"`
	FinalUpdateIDLast int64           `json:"pu"`
	Bids              [][]interface{} `json:"b"`
	Asks              [][]interface{} `json:"a"`
}

type WsBookTickerEvent struct {
	EventType    string `json:"e"`
	EventTime    int64  `json:"E"` // Note: bookTicker doesn't always have "e"
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

type FeeRateResponse struct {
	Symbol              string `json:"symbol"`
	MakerCommissionRate string `json:"makerCommissionRate"`
	TakerCommissionRate string `json:"takerCommissionRate"`
	RpiCommissionRate   string `json:"rpiCommissionRate"`
}

// FundingInfo contains funding rate configuration from fundingInfo endpoint
type FundingInfo struct {
	Symbol                   string `json:"symbol"`
	AdjustedFundingRateCap   string `json:"adjustedFundingRateCap"`
	AdjustedFundingRateFloor string `json:"adjustedFundingRateFloor"`
	FundingIntervalHours     int64  `json:"fundingIntervalHours"`
}

// FundingRateData contains funding rate information from premiumIndex endpoint.
type FundingRateData struct {
	Symbol               string `json:"symbol"`
	MarkPrice            string `json:"markPrice"`
	IndexPrice           string `json:"indexPrice"`
	EstimatedSettlePrice string `json:"estimatedSettlePrice"`
	LastFundingRate      string `json:"lastFundingRate"`
	InterestRate         string `json:"interestRate"`
	NextFundingTime      int64  `json:"nextFundingTime"`
	Time                 int64  `json:"time"`
}

type TimeInForce string

const (
	TimeInForce_GTC TimeInForce = "GTC"
	TimeInForce_IOC TimeInForce = "IOC"
	TimeInForce_FOK TimeInForce = "FOK"
	TimeInForce_GTX TimeInForce = "GTX"
)

type OrderType string

const (
	OrderType_LIMIT                OrderType = "LIMIT"
	OrderType_MARKET               OrderType = "MARKET"
	OrderType_STOP_MARKET          OrderType = "STOP_MARKET"
	OrderType_STOP_LIMIT           OrderType = "STOP"
	OrderType_TAKE_PROFIT_MARKET   OrderType = "TAKE_PROFIT_MARKET"
	OrderType_TAKE_PROFIT_LIMIT    OrderType = "TAKE_PROFIT"
	OrderType_TRAILING_STOP_MARKET OrderType = "TRAILING_STOP_MARKET"
)
