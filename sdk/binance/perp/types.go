package perp

import (
	"errors"
	"net/http"
)

var ErrWSOutcomeUnknown = errors.New("binance perp ws-api outcome unknown after request write")

// APIError represents a Binance API error
type APIError struct {
	Code       int    `json:"code"`
	Message    string `json:"msg"`
	HTTPStatus int    `json:"-"`
}

func (e *APIError) Error() string {
	return e.Message
}

// IsDefinitiveOrderRejection reports whether err is a parsed Binance order or
// request-business error carried by a 400 response. Transport failures,
// authentication failures, temporary/unknown 10xx errors, rate limits,
// malformed envelopes, other 4xx responses, and 5xx responses remain outside
// the definitive venue-rejection taxonomy.
func IsDefinitiveOrderRejection(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr == nil || apiErr.Code == 0 || apiErr.Message == "" {
		return false
	}
	return apiErr.HTTPStatus == http.StatusBadRequest && isDefinitiveOrderErrorCode(apiErr.Code)
}

func isDefinitiveOrderErrorCode(code int) bool {
	switch code {
	case -1013, -1014, -1015, -1020,
		-1100, -1101, -1102, -1103, -1104, -1105, -1106,
		-1111, -1114, -1115, -1116, -1117, -1118, -1119,
		-1121, -1128, -1130, -1136,
		-2010, -2011, -2012, -2013, -2016, -2018, -2019,
		-2020, -2021, -2022, -2023, -2024, -2025, -2026, -2027, -2028,
		-4000, -4001, -4002, -4003, -4004, -4005, -4006, -4007,
		-4013, -4014, -4015, -4016, -4020, -4022, -4023, -4024,
		-4031, -4032, -4045, -4058, -4060, -4061, -4062,
		-4087, -4088, -4104, -4105, -4106, -4107, -4109,
		-4116, -4117, -4118, -4120, -4131, -4135, -4137, -4138,
		-4139, -4140, -4141, -4142, -4144, -4164, -4183, -4184,
		-4189, -4192:
		return true
	default:
		return false
	}
}

func isAuthenticationError(status, code int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden ||
		code == -1002 || code == -1022 || code == -2014 || code == -2015
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
		NumberOfTrades      int64  `json:"n"`
		TakerBuyBaseVolume  string `json:"V"`
		TakerBuyQuoteVolume string `json:"Q"`
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
