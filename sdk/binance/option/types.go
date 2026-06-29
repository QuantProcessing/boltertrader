package option

// ExchangeInfoResponse is /eapi/v1/exchangeInfo.
type ExchangeInfoResponse struct {
	Timezone        string           `json:"timezone"`
	ServerTime      int64            `json:"serverTime"`
	OptionContracts []OptionContract `json:"optionContracts"`
	OptionAssets    []OptionAsset    `json:"optionAssets"`
	OptionSymbols   []OptionSymbol   `json:"optionSymbols"`
}

// OptionContract describes one underlying (e.g. BTC) and its quote asset.
type OptionContract struct {
	BaseAsset   string `json:"baseAsset"`
	QuoteAsset  string `json:"quoteAsset"`
	Underlying  string `json:"underlying"` // e.g. "BTCUSDT"
	SettleAsset string `json:"settleAsset"`
	ID          int64  `json:"id,omitempty"`
}

type OptionAsset struct {
	Name string `json:"name"`
	ID   int64  `json:"id,omitempty"`
}

// OptionSymbol is one tradable option instrument.
// Wire format of Symbol: "BTC-251226-100000-C".
type OptionSymbol struct {
	Symbol               string `json:"symbol"`
	Side                 string `json:"side"` // "CALL" or "PUT"
	StrikePrice          string `json:"strikePrice"`
	Underlying           string `json:"underlying"`
	Unit                 int64  `json:"unit"` // 1 (contract size in underlying)
	MakerFeeRate         string `json:"makerFeeRate"`
	TakerFeeRate         string `json:"takerFeeRate"`
	MinQty               string `json:"minQty"`
	MaxQty               string `json:"maxQty"`
	InitialMargin        string `json:"initialMargin"`
	MaintenanceMargin    string `json:"maintenanceMargin"`
	MinInitialMargin     string `json:"minInitialMargin"`
	MinMaintenanceMargin string `json:"minMaintenanceMargin"`
	PriceScale           int    `json:"priceScale"`
	QuantityScale        int    `json:"quantityScale"`
	ExpiryDate           int64  `json:"expiryDate"` // ms epoch
	ID                   int64  `json:"id,omitempty"`
	QuoteAsset           string `json:"quoteAsset"`
	ContractStatus       string `json:"contractStatus,omitempty"`
}

// MarkResponse is /eapi/v1/mark — one entry per option symbol queried.
type MarkResponse []MarkEntry

type MarkEntry struct {
	Symbol           string `json:"symbol"`
	MarkPrice        string `json:"markPrice"`
	BidIV            string `json:"bidIV"`
	AskIV            string `json:"askIV"`
	MarkIV           string `json:"markIV"`
	Delta            string `json:"delta"`
	Theta            string `json:"theta"`
	Gamma            string `json:"gamma"`
	Vega             string `json:"vega"`
	HighPriceLimit   string `json:"highPriceLimit"`
	LowPriceLimit    string `json:"lowPriceLimit"`
	RiskFreeInterest string `json:"riskFreeInterest"`
}

// PositionResponse is /eapi/v1/position.
type PositionResponse []PositionEntry

type PositionEntry struct {
	EntryPrice    string `json:"entryPrice"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"` // "LONG" or "SHORT"
	Quantity      string `json:"quantity"`
	ReducibleQty  string `json:"reducibleQty"`
	MarkValue     string `json:"markValue"`
	Ror           string `json:"ror"`
	UnrealizedPNL string `json:"unrealizedPNL"`
	MarkPrice     string `json:"markPrice"`
	StrikePrice   string `json:"strikePrice"`
	PositionCost  string `json:"positionCost"`
	ExpiryDate    int64  `json:"expiryDate"`
	PriceScale    int    `json:"priceScale"`
	QuantityScale int    `json:"quantityScale"`
	OptionSide    string `json:"optionSide"` // "CALL" or "PUT"
	QuoteAsset    string `json:"quoteAsset"`
	Time          int64  `json:"time"`
}

// AccountResponse is /eapi/v1/account.
type AccountResponse struct {
	Asset       []AccountAsset `json:"asset"`
	GreekRecord []AccountGreek `json:"greek"`
	Time        int64          `json:"time"`
	RiskLevel   string         `json:"riskLevel"`
}

type AccountAsset struct {
	Asset         string `json:"asset"`
	MarginBalance string `json:"marginBalance"`
	Equity        string `json:"equity"`
	Available     string `json:"available"`
	Locked        string `json:"locked"`
	UnrealizedPNL string `json:"unrealizedPNL"`
}

type AccountGreek struct {
	Underlying string `json:"underlying"`
	Delta      string `json:"delta"`
	Gamma      string `json:"gamma"`
	Theta      string `json:"theta"`
	Vega       string `json:"vega"`
}

// OrderRequest is used when placing an order.
type OrderRequest struct {
	Symbol        string // option symbol, e.g. "BTC-251226-100000-C"
	Side          string // "BUY" or "SELL"
	Type          string // "LIMIT" or "MARKET"
	Quantity      string // contracts
	Price         string // premium, required for LIMIT
	TimeInForce   string // "GTC", "IOC", "FOK"
	ReduceOnly    bool
	PostOnly      bool
	ClientOrderID string // user-defined ID
}

// OrderResponse — POST/GET /eapi/v1/order.
type OrderResponse struct {
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	ExecutedQty   string `json:"executedQty"`
	Fee           string `json:"fee"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	TimeInForce   string `json:"timeInForce"`
	ReduceOnly    bool   `json:"reduceOnly"`
	PostOnly      bool   `json:"postOnly"`
	CreateTime    int64  `json:"createTime"`
	UpdateTime    int64  `json:"updateTime"`
	Status        string `json:"status"` // "ACCEPTED", "REJECTED", "PARTIALLY_FILLED", "FILLED", "CANCELLED"
	AvgPrice      string `json:"avgPrice"`
	ClientOrderID string `json:"clientOrderId"`
}

// ErrorResponse — Binance error envelope.
type ErrorResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}
