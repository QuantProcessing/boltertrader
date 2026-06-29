package standx

import "encoding/json"

// WSRequest Standard structure
type WSRequest struct {
	SessionID string            `json:"session_id,omitempty"` // For order correlation
	RequestID string            `json:"request_id,omitempty"`
	Method    string            `json:"method,omitempty"` // auth:login, order:new, subscribe
	Header    map[string]string `json:"header,omitempty"` // x-request-*
	Params    interface{}       `json:"params,omitempty"` // Can be JSON string or object depending on usage
}

// SubscriptionRequest
// { "subscribe": { "channel": "depth_book", "symbol": "BTC-USD" } }
type SubscriptionRequest struct {
	Subscribe SubscribeParams `json:"subscribe"`
}

type SubscribeParams struct {
	Channel string `json:"channel"`
	Symbol  string `json:"symbol,omitempty"`
}

type SubscriptionAuthRequest struct {
	Auth SubscribeAuthParams `json:"auth"`
}

type SubscribeAuthParams struct {
	Token   string                 `json:"token"`
	Streams []SubscribeAuthChannel `json:"streams"`
}

type SubscribeAuthChannel struct {
	Channel string `json:"channel"`
}

// WSResponse Standard structure
type WSResponse struct {
	Seq       int             `json:"seq,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	Symbol    string          `json:"symbol,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`   // Payload
	Method    string          `json:"method,omitempty"` // For auth response
	Code      int             `json:"code,omitempty"`
	Message   string          `json:"message,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
}

type WsAuthResponse struct {
	Code int    `json:"code,omitempty"`
	Msg  string `json:"msg,omitempty"`
}

// Channel Data Types

type WSDepthData struct {
	Asks   [][]string `json:"asks"`
	Bids   [][]string `json:"bids"`
	Symbol string     `json:"symbol"`
}

type WSPriceData struct {
	Base       string    `json:"base"`
	IndexPrice string    `json:"index_price"`
	LastPrice  string    `json:"last_price"`
	MarkPrice  string    `json:"mark_price"`
	MidPrice   string    `json:"mid_price"`
	Spread     [2]string `json:"spread"`
	Quote      string    `json:"quote"`
	Symbol     string    `json:"symbol"`
	Time       string    `json:"time"`
}

type WSTradeData struct {
	// Fill in based on "trade" channel if needed
}

type WSOrderUpdate struct {
	// "order" channel
	// Maps to Order structure
}

type WSPositionUpdate struct {
	// "position" channel
	// Maps to Position structure
}

type WsApiResponse struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}
