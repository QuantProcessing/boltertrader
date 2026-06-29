package hyperliquid

import "encoding/json"

type WsMessage struct {
	Channel string          `json:"channel"`
	Data    json.RawMessage `json:"data"`
}

type WsSubscribeRequest struct {
	Method       string `json:"method"`
	Subscription any    `json:"subscription"`
}

type WsSubscriptionResponse struct {
	Method       string          `json:"method"`
	Subscription json.RawMessage `json:"subscription"`
}

type WsL2Book struct {
	Coin   string      `json:"coin"`
	Levels [][]WsLevel `json:"levels"`
	Time   int64       `json:"time"`
}

type WsLevel struct {
	Px string `json:"px"`
	Sz string `json:"sz"`
	N  int    `json:"n"`
}

type WsTrade struct {
	Coin  string   `json:"coin"`
	Side  string   `json:"side"`
	Px    string   `json:"px"`
	Sz    string   `json:"sz"`
	Hash  string   `json:"hash"`
	Time  int64    `json:"time"`
	Tid   int64    `json:"tid"` // Trade ID
	Users []string `json:"users"`
}

type WsUserFill struct {
	Coin          string         `json:"coin"`
	Px            string         `json:"px"`
	Sz            string         `json:"sz"`
	Side          string         `json:"side"`
	Time          int64          `json:"time"`
	StartPosition string         `json:"startPosition"`
	Dir           string         `json:"dir"`
	ClosedPnl     string         `json:"closedPnl"`
	Hash          string         `json:"hash"`
	Oid           int64          `json:"oid"`
	Crossed       bool           `json:"crossed"`
	Fee           string         `json:"fee"`
	FeeToken      string         `json:"feeToken"`
	Tid           int64          `json:"tid"`
	BuilderFee    string         `json:"builderFee,omitempty"`
	Liquidation   *WsLiquidation `json:"liquidation,omitempty"`
}

type WsUserFills struct {
	IsSnapshot bool         `json:"isSnapshot,omitempty"`
	User       string       `json:"user"`
	Fills      []WsUserFill `json:"fills"`
}

type WsLiquidation struct {
	Lid                    int64  `json:"lid"`
	Liquidator             string `json:"liquidator"`
	LiquidatedUser         string `json:"liquidated_user"`
	LiquidatedNtlPos       string `json:"liquidated_ntl_pos"`
	LiquidatedAccountValue string `json:"liquidated_account_value"`
}

type WsNonUserCancel struct {
	Coin string `json:"coin"`
	Oid  int64  `json:"oid"`
}

type WsUserEvent struct {
	Fills         []WsUserFill      `json:"fills,omitempty"`
	Funding       *WsUserFunding    `json:"funding,omitempty"`
	Liquidation   *WsLiquidation    `json:"liquidation,omitempty"`
	NonUserCancel []WsNonUserCancel `json:"nonUserCancel,omitempty"`
}

type WsOrder struct {
	Coin      string `json:"coin"`
	Side      string `json:"side"`
	LimitPx   string `json:"limitPx"`
	Sz        string `json:"sz"`
	Oid       int64  `json:"oid"`
	Timestamp int64  `json:"timestamp"`
	OrigSz    string `json:"origSz"`
	Cliod     string `json:"cloid"`
}

type WsOrderUpdate struct {
	Order           WsOrder          `json:"order"`
	Status          OrderStatusValue `json:"status"`
	StatusTimestamp int64            `json:"statusTimestamp"`
}

// OrderStatusValue represents the status string returned by the Hyperliquid API.
// Used in both REST and WS order responses for perp and spot markets.
// See: https://hyperliquid.gitbook.io/hyperliquid-docs
type OrderStatusValue string

const (
	StatusOpen                    OrderStatusValue = "open"
	StatusFilled                  OrderStatusValue = "filled"
	StatusCanceled                OrderStatusValue = "canceled"
	StatusTriggered               OrderStatusValue = "triggered"
	StatusRejected                OrderStatusValue = "rejected"
	StatusMarginCanceled          OrderStatusValue = "marginCanceled"
	StatusVaultWithdrawalCanceled OrderStatusValue = "vaultWithdrawalCanceled"
	StatusOpenInterestCapCanceled OrderStatusValue = "openInterestCapCanceled"
	StatusSelfTradeCanceled       OrderStatusValue = "selfTradeCanceled"
	StatusReduceOnlyCanceled      OrderStatusValue = "reduceOnlyCanceled"
	StatusSiblingFilledCanceled   OrderStatusValue = "siblingFilledCanceled"
	StatusDelistedCanceled        OrderStatusValue = "delistedCanceled"
	StatusLiquidatedCanceled      OrderStatusValue = "liquidatedCanceled"
	StatusScheduledCancel         OrderStatusValue = "scheduledCancel"
	StatusTickRejected            OrderStatusValue = "tickRejected"
	StatusMinTradeNtlRejected     OrderStatusValue = "minTradeNtlRejected"
)

type WsUserFunding struct {
	Time        int64  `json:"time"`
	Coin        string `json:"coin"`
	Usdc        string `json:"usdc"`
	Szi         string `json:"szi"`
	FundingRate string `json:"fundingRate"`
}

type WsUserLiquidations struct {
	Tid  int64  `json:"tid"`
	Coin string `json:"coin"`
	Usdc string `json:"usdc"`
	Sz   string `json:"sz"`
	Px   string `json:"px"`
	Time int64  `json:"time"`
}

type WsUserNonFundingLedgerUpdates struct {
	Time int64  `json:"time"`
	Coin string `json:"coin"`
	Usdc string `json:"usdc"`
	Type string `json:"type"` // e.g. "deposit", "withdraw", "transfer", "internal_transfer", "subaccount_transfer"
}

type WsCandle struct {
	T      int64  `json:"t"` // Open time
	TClose int64  `json:"T"` // Close time
	S      string `json:"s"` // Symbol/Coin
	I      string `json:"i"` // Interval
	O      string `json:"o"` // Open
	C      string `json:"c"` // Close
	H      string `json:"h"` // High
	L      string `json:"l"` // Low
	V      string `json:"v"` // Volume
	N      int64  `json:"n"` // Number of trades
}

type WsBbo struct {
	Coin string    `json:"coin"`
	Time int64     `json:"time"`
	Bbo  []WsLevel `json:"bbo"`
}

type WsAllMids struct {
	Mids map[string]string `json:"mids"`
}

type WsAllDexsAssetCtxs struct {
	Ctxs map[string][]WsAssetCtx `json:"ctxs"`
}

type WsActiveAssetCtx struct {
	Coin string     `json:"coin"`
	Ctx  WsAssetCtx `json:"ctx"`
}

type WsAssetCtx struct {
	Funding      string   `json:"funding"`
	OpenInterest string   `json:"openInterest"`
	MarkPx       string   `json:"markPx"`
	OraclePx     string   `json:"oraclePx"`
	MidPx        string   `json:"midPx"`
	PrevDayPx    string   `json:"prevDayPx"`
	Premium      string   `json:"premium"`
	DayNtlVlm    string   `json:"dayNtlVlm"`
	DayBaseVlm   string   `json:"dayBaseVlm"`
	ImpactPxs    []string `json:"impactPxs,omitempty"`
}

type WsPostRequest struct {
	Method  string               `json:"method"`
	ID      int64                `json:"id"`
	Request WsPostRequestPayload `json:"request"`
}

type WsPostRequestPayload struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type WsPostResponse struct {
	ID       int64                 `json:"id"`
	Response WsPostResponsePayload `json:"response"`
}

type WsPostResponsePayload struct {
	Type    string          `json:"type"` // "info", "action", "error"
	Payload json.RawMessage `json:"payload"`
}

type PostResult struct {
	Response WsPostResponsePayload
	Error    error
}
