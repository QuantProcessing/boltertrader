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
	Timezone   string       `json:"timezone"`
	ServerTime int64        `json:"serverTime"`
	Symbols    []SymbolInfo `json:"symbols"`
}

type SymbolInfo struct {
	Symbol             string                   `json:"symbol"`
	Status             string                   `json:"status"`
	BaseAsset          string                   `json:"baseAsset"`
	BaseAssetPrecision int                      `json:"baseAssetPrecision"`
	QuoteAsset         string                   `json:"quoteAsset"`
	QuotePrecision     int                      `json:"quotePrecision"`
	Filters            []map[string]interface{} `json:"filters"`
}

type ExecutionReport struct {
	EventType        string `json:"e"`
	EventTime        int64  `json:"E"`
	Symbol           string `json:"s"`
	ClientOrderID    string `json:"c"`
	Side             string `json:"S"`
	OrderType        string `json:"o"`
	TimeInForce      string `json:"f"`
	Quantity         string `json:"q"`
	Price            string `json:"p"`
	StopPrice        string `json:"P"`
	IcebergQuantity  string `json:"F"`
	OrderListID      int64  `json:"g"` // -1
	OriginalClientID string `json:"C"` // ""
	ExecutionType    string `json:"x"` // NEW, CANCELED, replaced, REJECTED, TRADE, EXPIRED
	OrderStatus      string `json:"X"` // NEW, PARTIALLY_FILLED, FILLED, CANCELED, REJECTED, EXPIRED
	RejectReason     string `json:"r"`
	OrderID          int64  `json:"i"`
	LastExecutedQty  string `json:"l"`
	CumulativeQty    string `json:"z"`
	LastExecPrice    string `json:"L"`
	Commission       string `json:"n"`
	CommissionAsset  string `json:"N"`
	TransactTime     int64  `json:"T"`
	TradeID          int64  `json:"t"`
	Ignore1          int64  `json:"I"` // Ignore
	IsOrderWorking   bool   `json:"w"`
	IsMaker          bool   `json:"m"`
	Ignore2          bool   `json:"M"` // Ignore
	CreationTime     int64  `json:"O"`
	CumQuoteQty      string `json:"Z"`
	LastQuoteQty     string `json:"Y"`
	QuoteOrderQty    string `json:"Q"`
}
