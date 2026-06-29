package nado

import (
	"encoding/json"
)

// WebSocket Message Types

type WsMessage struct {
	Type      string          `json:"type"` // Used for inference if available
	Channel   string          `json:"channel,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Status    string          `json:"status,omitempty"`
	Error     string          `json:"error,omitempty"`
	ErrorCode int             `json:"error_code,omitempty"`
	// Auth specific
	Method string `json:"method,omitempty"`
	Id     int64  `json:"id,omitempty"`
}

type WsResponse struct {
	Id          *int64          `json:"id,omitempty"`
	Signature   *string         `json:"signature,omitempty"`
	Status      string          `json:"status"`
	Error       string          `json:"error,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Data        json.RawMessage `json:"data,omitempty"`
	RequestType string          `json:"request_type,omitempty"`
}

type WsAuthRequest struct {
	Method    string       `json:"method"` // "authenticate"
	Id        int64        `json:"id"`
	Tx        TxStreamAuth `json:"tx"`
	Signature string       `json:"signature"`
}

type SubscriptionRequest struct {
	Method string       `json:"method"` // "subscribe" or "unsubscribe"
	Stream StreamParams `json:"stream"`
	Id     int64        `json:"id"`
}

type StreamParams struct {
	Type        string `json:"type"`
	ProductId   *int64 `json:"product_id"` // nullable
	Subaccount  string `json:"subaccount,omitempty"`
	Granularity int32  `json:"granularity,omitempty"`
}

// Data Payloads

type OrderUpdate struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	ProductId int64  `json:"product_id"`
	Digest    string `json:"digest"`
	Amount    string `json:"amount"`
	Reason    string `json:"reason"`
	Id        int64  `json:"id"`
}

// Use Ticker struct from types.go for BestBidOffer if compatible
// Use Trade struct from types.go
// Use OrderBook struct from types.go

type Fill struct {
	Digest    string `json:"digest,omitempty"`
	OrderID   string `json:"order_id,omitempty"`
	TradeId   string `json:"trade_id"`
	ProductId int64  `json:"product_id"`
	Price     string `json:"price"`
	Size      string `json:"size"`
	Side      string `json:"side"`
	Fee       string `json:"fee"`
	Time      int64  `json:"time"`
}

type PositionChange struct {
	ProductId  int64  `json:"product_id"`
	Amount     string `json:"amount"`
	EntryPrice string `json:"entry_price"`
	Side       string `json:"side"`
}

type FundingPayment struct {
	Type                      string `json:"type"`
	Timestamp                 string `json:"timestamp"`
	ProductId                 int64  `json:"product_id"`
	PaymentAmount             string `json:"payment_amount"`
	OpenInterest              string `json:"open_interest"`
	CumulativeFundingLongX18  string `json:"cumulative_funding_long_x18"`
	CumulativeFundingShortX18 string `json:"cumulative_funding_short_x18"`
	Dt                        int64  `json:"dt"`
}

type FundingRate struct {
	Type           string `json:"type"`
	Timestamp      string `json:"timestamp"`
	ProductId      int64  `json:"product_id"`
	FundingRateX18 string `json:"funding_rate_x18"`
	UpdateTime     int64  `json:"update_time"`
}

type Candlestick struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	ProductId   int64  `json:"product_id"`
	Granularity int32  `json:"granularity"`
	OpenX18     string `json:"open_x18"`
	HighX18     string `json:"high_x18"`
	LowX18      string `json:"low_x18"`
	CloseX18    string `json:"close_x18"`
	Volume      string `json:"volume"`
}
