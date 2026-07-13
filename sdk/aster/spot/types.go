package spot

import "encoding/json"

// APIError represents an Aster API error.
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
	Timezone        string            `json:"timezone"`
	ServerTime      int64             `json:"serverTime"`
	RateLimits      []RateLimit       `json:"rateLimits"`
	ExchangeFilters []json.RawMessage `json:"exchangeFilters"`
	Assets          []AssetInfo       `json:"assets"`
	Symbols         []SymbolInfo      `json:"symbols"`
}

type RateLimit struct {
	RateLimitType string `json:"rateLimitType"`
	Interval      string `json:"interval"`
	IntervalNum   int    `json:"intervalNum"`
	Limit         int    `json:"limit"`
}

type AssetInfo struct {
	Asset string `json:"asset"`
}

type SymbolInfo struct {
	Symbol               string         `json:"symbol"`
	Status               string         `json:"status"`
	BaseAsset            string         `json:"baseAsset"`
	QuoteAsset           string         `json:"quoteAsset"`
	PricePrecision       int            `json:"pricePrecision"`
	QuantityPrecision    int            `json:"quantityPrecision"`
	BaseAssetPrecision   int            `json:"baseAssetPrecision"`
	QuotePrecision       int            `json:"quotePrecision"`
	Filters              []SymbolFilter `json:"filters"`
	OrderTypes           []string       `json:"orderTypes"`
	TimeInForce          []string       `json:"timeInForce"`
	IcebergAllowed       bool           `json:"icebergAllowed"`
	OCOAllowed           bool           `json:"ocoAllowed"`
	IsSpotTradingAllowed *bool          `json:"isSpotTradingAllowed"`
}

type SymbolFilter struct {
	FilterType        string `json:"filterType"`
	MinPrice          string `json:"minPrice,omitempty"`
	MaxPrice          string `json:"maxPrice,omitempty"`
	TickSize          string `json:"tickSize,omitempty"`
	MinQty            string `json:"minQty,omitempty"`
	MaxQty            string `json:"maxQty,omitempty"`
	StepSize          string `json:"stepSize,omitempty"`
	Limit             *int   `json:"limit,omitempty"`
	MinNotional       string `json:"minNotional,omitempty"`
	MaxNotional       string `json:"maxNotional,omitempty"`
	AvgPriceMins      *int   `json:"avgPriceMins,omitempty"`
	ApplyMinToMarket  *bool  `json:"applyMinToMarket,omitempty"`
	ApplyMaxToMarket  *bool  `json:"applyMaxToMarket,omitempty"`
	MultiplierDown    string `json:"multiplierDown,omitempty"`
	MultiplierUp      string `json:"multiplierUp,omitempty"`
	MultiplierDecimal string `json:"multiplierDecimal,omitempty"`
	BidMultiplierUp   string `json:"bidMultiplierUp,omitempty"`
	BidMultiplierDown string `json:"bidMultiplierDown,omitempty"`
	AskMultiplierUp   string `json:"askMultiplierUp,omitempty"`
	AskMultiplierDown string `json:"askMultiplierDown,omitempty"`
}

type WsDepthEvent = DepthEvent
