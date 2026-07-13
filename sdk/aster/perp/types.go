package perp

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
	FuturesType     string            `json:"futuresType"`
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
	Asset             string          `json:"asset"`
	MarginAvailable   bool            `json:"marginAvailable"`
	AutoAssetExchange *StringOrNumber `json:"autoAssetExchange"`
}

// StringOrNumber preserves decimal API fields which Aster documents as JSON
// numbers but may emit as quoted decimals.
type StringOrNumber string

func (v *StringOrNumber) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return fmt.Errorf("aster perp: empty string-or-number value")
	}
	var value string
	if strings.HasPrefix(trimmed, `"`) {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	} else {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return err
		}
		number, ok := decoded.(json.Number)
		if !ok {
			return fmt.Errorf("aster perp: expected string or number")
		}
		value = number.String()
	}
	*v = StringOrNumber(value)
	return nil
}

func (v StringOrNumber) String() string {
	return string(v)
}

type SymbolInfo struct {
	Symbol                string         `json:"symbol"`
	Pair                  string         `json:"pair"`
	ContractType          string         `json:"contractType"`
	DeliveryDate          int64          `json:"deliveryDate"`
	OnboardDate           int64          `json:"onboardDate"`
	Status                string         `json:"status"`
	MaintMarginPercent    string         `json:"maintMarginPercent"`
	RequiredMarginPercent string         `json:"requiredMarginPercent"`
	BaseAsset             string         `json:"baseAsset"`
	QuoteAsset            string         `json:"quoteAsset"`
	MarginAsset           string         `json:"marginAsset"`
	PricePrecision        int            `json:"pricePrecision"`
	QuantityPrecision     int            `json:"quantityPrecision"`
	BaseAssetPrecision    int            `json:"baseAssetPrecision"`
	QuotePrecision        int            `json:"quotePrecision"`
	UnderlyingType        string         `json:"underlyingType"`
	UnderlyingSubType     []string       `json:"underlyingSubType"`
	SettlePlan            int64          `json:"settlePlan"`
	TriggerProtect        string         `json:"triggerProtect"`
	Filters               []SymbolFilter `json:"filters"`
	OrderTypes            []string       `json:"orderTypes"`
	TimeInForce           []string       `json:"timeInForce"`
	LiquidationFee        string         `json:"liquidationFee"`
	MarketTakeBound       string         `json:"marketTakeBound"`
}

func (s *SymbolInfo) UnmarshalJSON(data []byte) error {
	type symbolInfoAlias SymbolInfo
	var decoded symbolInfoAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if len(decoded.OrderTypes) == 0 {
		var official struct {
			OrderTypes []string `json:"OrderType"`
		}
		if err := json.Unmarshal(data, &official); err != nil {
			return err
		}
		decoded.OrderTypes = official.OrderTypes
	}
	*s = SymbolInfo(decoded)
	return nil
}

type SymbolFilter struct {
	FilterType        string          `json:"filterType"`
	MinPrice          string          `json:"minPrice,omitempty"`
	MaxPrice          string          `json:"maxPrice,omitempty"`
	TickSize          string          `json:"tickSize,omitempty"`
	MinQty            string          `json:"minQty,omitempty"`
	MaxQty            string          `json:"maxQty,omitempty"`
	StepSize          string          `json:"stepSize,omitempty"`
	Limit             *int            `json:"limit,omitempty"`
	Notional          string          `json:"notional,omitempty"`
	MultiplierUp      string          `json:"multiplierUp,omitempty"`
	MultiplierDown    string          `json:"multiplierDown,omitempty"`
	MultiplierDecimal *StringOrNumber `json:"multiplierDecimal,omitempty"`
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
		NumberOfTrades      int64  `json:"n"`
		QuoteVolume         string `json:"q"`
		IsClosed            bool   `json:"x"`
		TakerBuyBaseVolume  string `json:"V"`
		TakerBuyQuoteVolume string `json:"Q"`
		Ignore              string `json:"B"`
	} `json:"k"`
}

type FeeRateResponse struct {
	Symbol              string `json:"symbol"`
	MakerCommissionRate string `json:"makerCommissionRate"`
	TakerCommissionRate string `json:"takerCommissionRate"`
	RpiCommissionRate   string `json:"rpiCommissionRate"`
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
	TimeInForce_GTC    TimeInForce = "GTC"
	TimeInForce_IOC    TimeInForce = "IOC"
	TimeInForce_FOK    TimeInForce = "FOK"
	TimeInForce_GTX    TimeInForce = "GTX"
	TimeInForce_HIDDEN TimeInForce = "HIDDEN"
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
