package grvt

import (
	"fmt"
)

// Environment constants
const (
	EdgeURL       = "https://edge.grvt.io"
	TradeDataURL  = "https://trades.grvt.io"
	MarketDataURL = "https://market-data.grvt.io"
	ChainID       = "325"
)

// Common Types

type GrvtError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

func (e *GrvtError) Error() string {
	return fmt.Sprintf("GRVT Error %d: %s", e.Code, e.Message)
}

// Auth Types

type LoginRequest struct {
	ApiKey string `json:"api_key"`
}

// Order Types

type TimeInForce string

const (
	GTT TimeInForce = "GOOD_TILL_TIME"
	AON TimeInForce = "ALL_OR_NONE"
	IOC TimeInForce = "IMMEDIATE_OR_CANCEL"
	FOK TimeInForce = "FILL_OR_KILL"
)

// SignTimeInForce maps string TIF to integer for signing
var SignTimeInForceMap = map[TimeInForce]int{
	GTT: 1,
	AON: 2,
	IOC: 3,
	FOK: 4,
}

// API Requests/Responses
// create order

type CreateOrderRequest struct {
	Order OrderRequest `json:"o"`
}

type OrderRequest struct {
	SubAccountID uint64         `json:"sa"`
	IsMarket     bool           `json:"im"`
	TimeInForce  TimeInForce    `json:"tif"`
	PostOnly     bool           `json:"po"`
	ReduceOnly   bool           `json:"ro"`
	Legs         []OrderLeg     `json:"l"`
	Signature    OrderSignature `json:"s"`
	Metadata     OrderMetadata  `json:"m"`
}

type OrderLeg struct {
	Instrument    string `json:"i"`
	Size          string `json:"s"`
	LimitPrice    string `json:"lp"`
	IsBuyintAsset bool   `json:"ib"`
}

type OrderSignature struct {
	Signer     string `json:"s"`
	R          string `json:"r"`
	S          string `json:"s1"`
	V          int    `json:"v"`
	Expiration string `json:"e"`
	Nonce      uint32 `json:"n"`
	ChainID    string `json:"ci"`
}

type OrderMetadata struct {
	ClientOrderID string       `json:"co"`
	CreatedTime   string       `json:"ct"`
	Tigger        OrderTrigger `json:"t"`
	Broker        string       `json:"b"`
}

type OrderTrigger struct {
	TriggerType string    `json:"tt"`
	Tpsl        OrderTpsl `json:"tpsl"`
}

type OrderTpsl struct {
	TriggerBy     string `json:"tb"`
	TriggerPrice  string `json:"tp"`
	ClosePosition bool   `json:"cp"`
}

type CreateOrderResponse struct {
	Result Order `json:"r"`
}

type Order struct {
	OrderID      string         `json:"oi"`
	SubAccountID string         `json:"sa"`
	IsMarket     bool           `json:"im"`
	TimeInForce  TimeInForce    `json:"tif"`
	PostOnly     bool           `json:"po"`
	ReduceOnly   bool           `json:"ro"`
	Legs         []OrderLeg     `json:"l"`
	Signature    OrderSignature `json:"s"`
	Metadata     OrderMetadata  `json:"m"`
	State        OrderState     `json:"s1"`
}

type OrderState struct {
	Status       OrderStatus `json:"s"`
	RejectReason string      `json:"rr"`
	BookSize     []string    `json:"bs"`
	TradedSize   []string    `json:"ts"`
	UpdateTime   string      `json:"ut"`
	AvgFillPrice []string    `json:"af"`
}

// get open orders
type GetOpenOrdersRequest struct {
	SubAccountID string    `json:"sa"`
	Kind         *[]string `json:"k,omitempty"`
	Base         *[]string `json:"b,omitempty"`
	Quote        *[]string `json:"q,omitempty"`
}

type GetOpenOrdersResponse struct {
	Result []Order `json:"r"`
}

// cancel order

type CancelOrderRequest struct {
	SubAccountID  string  `json:"sa"`
	OrderID       *string `json:"oi,omitempty"`
	ClientOrderID *string `json:"co,omitempty"`
	TimeToLiveMS  *string `json:"tt,omitempty"`
}

type CancelOrderResponse struct {
	Result struct {
		Ack bool `json:"a"` // GRVT returns bool, not string
	} `json:"r"`
}

// cancel all order

type CancelAllOrderRequest struct {
	SubAccountID string    `json:"sa"`
	Kind         *[]string `json:"k,omitempty"`
	Base         *[]string `json:"b,omitempty"`
	Quote        *[]string `json:"q,omitempty"`
}

type CancelAllOrderResponse struct {
	Result struct {
		Ack bool `json:"a"` // GRVT returns bool, not string
	} `json:"r"`
}

// set leverage

type SetLeverageRequest struct {
	SubAccountID string `json:"sa"`
	Instrument   string `json:"i"`
	Leverage     int    `json:"l"`
}

type SetLeverageResponse struct {
	Success string `json:"s"`
}

// get all initial leverage

type GetAllInitialLeverageRequest struct {
	SubAccountID string `json:"sa"`
}

type GetAllInitialLeverageResponse struct {
	Results []Leverage `json:"r"`
}

type Leverage struct {
	Instrument  string `json:"i"`
	Leverage    string `json:"l"`
	MinLeverage string `json:"ml"`
	MaxLeverage string `json:"ml1"`
}

// account summary

type GetAccountSummaryRequest struct {
	SubAccountID string `json:"sa"`
}

type GetAccountSummaryResponse struct {
	Result AccountSummary `json:"r"`
}

type GetFundingAccountSummaryResponse struct {
	Result FundingAccountSummary `json:"r"`
	Tier   Tier                  `json:"t"`
}
type FundingAccountSummary struct {
	MainAccountId    string            `json:"ma"`
	TotalEquity      string            `json:"te"`
	SpotBalances     []SpotBalance     `json:"sb"`
	VaultInvestments []VaultInvestment `json:"vi"`
}
type SpotBalance struct {
	Currency   string `json:"c"`
	Balance    string `json:"b"`
	IndexPrice string `json:"ip"`
}
type VaultInvestment struct {
	VaultId             string `json:"vi"`
	NumLpTokens         string `json:"nl"`
	SharePrice          string `json:"sp"`
	UsdNotionalInvested string `json:"un"`
}
type Tier struct {
	Tier            int64 `json:"t"`
	FuturesTakerFee int64 `json:"ft"`
	FuturesMakerFee int64 `json:"fm"`
	OptionsTakerFee int64 `json:"ot"`
	OptionsMakerFee int64 `json:"om"`
}

type AccountSummary struct {
	EventTime         string `json:"et"`
	SubAccountID      string `json:"sa"`
	MarginType        string `json:"mt"`
	SettleCurrency    string `json:"sc"`
	UnrealizedPnl     string `json:"up"`
	TotalEquity       string `json:"te"`
	InitialMargin     string `json:"im"`
	MaintenanceMargin string `json:"mm"`
	AvailableBalance  string `json:"ab"`
	SpotBalance       []struct {
		Currency   string `json:"c"`
		Balance    string `json:"b"`
		IndexPrice string `json:"ip"`
	} `json:"sb"`
	Position                       []Position `json:"p"`
	SettleIndexPrice               string     `json:"si"`
	IsValue                        *bool      `json:"iv"`
	VaultImAdditions               string     `json:"vi"`
	DeriskMargin                   string     `json:"dm"`
	DeriskToMaintenanceMarginRatio string     `json:"dt"`
}

type Position struct {
	EventTime                        string `json:"et"`
	SubAccountID                     string `json:"sa"`
	Instrument                       string `json:"i"`
	Size                             string `json:"s"`
	Notional                         string `json:"n"`
	EntryPrice                       string `json:"ep"`
	ExitPrice                        string `json:"ep1"`
	MarkPrice                        string `json:"mp"`
	UnrealizedPnl                    string `json:"up"`
	RealizedPnl                      string `json:"rp"`
	TotalPnl                         string `json:"tp"`
	Roi                              string `json:"r"`
	QuoteIndexPrice                  string `json:"qi"`
	EstLiquidationPrice              string `json:"el"`
	Leverage                         string `json:"l"`
	CumulativeFee                    string `json:"cf"`
	CumulativeRealizedFundingPayment string `json:"cr"`
}

type GetOrderBookRequest struct {
	Instrument string `json:"i"`
	Depth      int    `json:"d,omitempty"`
}

type OrderBookLevel struct {
	Price     string `json:"p"`
	Size      string `json:"s"`
	NumOrders int64  `json:"no"`
}

type GetOrderBookResponse struct {
	Result OrderBook `json:"r"`
}

type OrderBook struct {
	Instrument string           `json:"i"`
	Bids       []OrderBookLevel `json:"b"`
	Asks       []OrderBookLevel `json:"a"`
	EventTime  string           `json:"et"`
}

type GetMiniTickerRequest struct {
	Instrument string `json:"i"`
}

type Instrument struct {
	Instrument               string   `json:"i"`
	InstrumentHash           string   `json:"ih"`
	Base                     string   `json:"b"`
	Quote                    string   `json:"q"`
	Kind                     string   `json:"k"`
	Venues                   []string `json:"v"`
	SettlementPeriod         string   `json:"sp1"`
	BaseDecimals             int      `json:"bd"`
	QuoteDecimals            int      `json:"qd"`
	TickSize                 string   `json:"ts"`
	MinSize                  string   `json:"ms"`
	CreateTime               string   `json:"ct"`
	MaxPositionSize          string   `json:"mp"`
	FundingIntervalHours     int64    `json:"fi"`
	AdjustedFundingRateCap   string   `json:"af"`
	AdjustedFundingRateFloor string   `json:"af1"`
}

type GetInstrumentsResponse struct {
	Result []Instrument `json:"r"`
}

type GetMiniTickerResponse struct {
	Result MiniTicker `json:"r"`
}

type MiniTicker struct {
	EventTime    string `json:"et"`
	Instrument   string `json:"i"`
	MarkPrice    string `json:"mp"`
	IndexPrice   string `json:"ip"`
	LastPrice    string `json:"lp"`
	MidPrice     string `json:"mp1"`
	BestBidPrice string `json:"bb"`
	BestBidSize  string `json:"bb1"`
	BestAskPrice string `json:"ba"`
	BestAskSize  string `json:"ba1"`
}

type GetTickerRequest struct {
	Instrument string `json:"i"`
}

type GetTickerResponse struct {
	Result Ticker `json:"r"`
}

type Ticker struct {
	EventTime    string `json:"et"`
	Instrument   string `json:"i"`
	MarkPrice    string `json:"mp"`
	IndexPrice   string `json:"ip"`
	LastPrice    string `json:"lp"`
	MidPrice     string `json:"mp1"`
	BestBidPrice string `json:"bb"`
	BestBidSize  string `json:"bb1"`
	BestAskPrice string `json:"ba"`
	BestAskSize  string `json:"ba1"`
	// more mini ticker
	// DEPRECATED: Use FundingRate instead
	FundingRate8hCurr string `json:"fr"`
	// DEPRECATED: Use FundingRate instead
	FundingRate8hAvg string `json:"fr1"`
	InterestRate     string `json:"ir"`
	ForwardPrice     string `json:"fp"`
	BuyVolume24hB    string `json:"bv"`
	SellVolume24hB   string `json:"sv"`
	BuyVolume24hQ    string `json:"bv1"`
	SellVolume24hQ   string `json:"sv1"`
	HighPrice        string `json:"hp"`
	LowPrice         string `json:"lp1"`
	OpenPrice        string `json:"op"`
	OpenInterest     string `json:"oi"`
	LongShortRatio   string `json:"ls"`
	// FundingRate is the funding rate applied over funding_interval_hours
	// (interval ending at next_funding_time), NOT per hour
	FundingRate     string `json:"fr2"`
	NextFundingTime string `json:"nf"`
}

type GetTradeRequest struct {
	Instrument string `json:"i"`
	Limit      int    `json:"l"`
}

type GetTradeResponse struct {
	Result []Trade `json:"r"`
}

type Trade struct {
	EventTime    string  `json:"et"`
	Instrument   string  `json:"i"`
	IsTakerBuyer bool    `json:"it"`
	Size         string  `json:"s"`
	Price        string  `json:"p"`
	MarkPrice    string  `json:"mp"`
	IndexPrice   string  `json:"ip"`
	InterestRate float64 `json:"ir"`
	ForwardPrice string  `json:"fp"`
	TradeId      string  `json:"ti"`
	Venue        string  `json:"v"`
	IsRpi        bool    `json:"ip1"`
}

type GetKLineRequest struct {
	Instrument string  `json:"i"`
	Interval   string  `json:"i1"`
	KlineType  string  `json:"t"`
	StartTime  *string `json:"st,omitempty"`
	EndTime    *string `json:"et,omitempty"`
	Limit      *int64  `json:"l,omitempty"`
	Cursor     *string `json:"c,omitempty"`
}

type GetKLineResponse struct {
	Result []KLine `json:"r"`
	Next   string  `json:"n"`
}

type KLine struct {
	OpenTime   string `json:"ot"`
	CloseTime  string `json:"ct"`
	Open       string `json:"o"`
	High       string `json:"h"`
	Low        string `json:"l"`
	Close      string `json:"c"`
	VolumeB    string `json:"vb"`
	VolumeQ    string `json:"vq"`
	Traders    int64  `json:"t"`
	Instrument string `json:"i"`
}

type GetFundingRateRequest struct {
	Instrument string  `json:"i"`
	StartTime  *string `json:"start_time,omitempty"`
	EndTime    *string `json:"end_time,omitempty"`
	Limit      *int64  `json:"limit,omitempty"`
	Cursor     *string `json:"cursor,omitempty"`
	AggType    *string `json:"agg_type,omitempty"`
}

type GetFundingRateResponse struct {
	Result []FundingRate `json:"r"`
	Next   string        `json:"n"`
}

type FundingRate struct {
	Instrument           string  `json:"i"`
	FundingRate          float64 `json:"fr"`
	FundingTime          string  `json:"ft"`
	MarkPrice            string  `json:"mp"`
	FundingIntervalHours string  `json:"fi"`
}

type AccountSummaryResponse struct {
	Result struct {
		AccountValue    string `json:"account_value"`
		TotalMarginUsed string `json:"total_margin_used"`
		TotalNtlPos     string `json:"total_ntl_pos"`
		TotalRawUsd     string `json:"total_raw_usd"`
		Withdrawable    string `json:"withdrawable"`
	} `json:"result"`
}

type GetPositionsResponse struct {
	Result []Position `json:"result"`
}

type OrderStatus string

const (
	OrderStatusPending   = "PENDING"
	OrderStatusOpen      = "OPEN"
	OrderStatusFilled    = "FILLED"
	OrderStatusRejected  = "REJECTED"
	OrderStatusCancelled = "CANCELLED"
)
