package okx

import "encoding/json"

// WsEvent represents standard websocket event messages (login, subscribe).
type WsEvent struct {
	Event string `json:"event"`
	Code  string `json:"code"`
	Msg   string `json:"msg"`
}

// WsPushData generic struct for pushed data.
type WsPushData[T any] struct {
	Arg    map[string]string `json:"arg"`
	Data   []T               `json:"data"`
	Action string            `json:"action,omitempty"` // for order updates: snapshot/update? Actually OKX doesn't use action field much for orders but for depth.
}

// Common args keys
const (
	ArgChannel    = "channel"
	ArgInstFamily = "instFamily"
	ArgInstId     = "instId"
	ArgInstType   = "instType"
)

// WsOrder push data structure is same as OrderDetails
type WsOrderData struct {
	// Reusing OrderDetails from types.go if possible or defining new
	// OKX WS Order push structure is very similar to REST
	Order
}

// WsPosition push data
type WsPositionData struct {
	Position
}

// WsSubscribeReq is used for subscribe requests
type WsSubscribeArgs struct {
	Channel    string `json:"channel"`
	InstType   string `json:"instType,omitempty"`
	InstFamily string `json:"instFamily,omitempty"`
	InstId     string `json:"instId,omitempty"`
}

// WsSubscribeRes is used for subscribe responses
type WsSubscribeRes struct {
	// request/response map by id
	ID    *string          `json:"id,omitempty"`
	Event *string          `json:"event,omitempty"`
	Arg   *WsSubscribeArgs `json:"arg,omitempty"`
	// if code not nil, throw error
	Code *string `json:"code,omitempty"`
	Msg  *string `json:"msg,omitempty"`
	// push data
	Data *json.RawMessage `json:"data,omitempty"`
}
