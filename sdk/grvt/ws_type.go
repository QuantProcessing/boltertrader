package grvt

import "encoding/json"

const (
	WssMarketURL   = "wss://market-data.grvt.io/ws/lite"
	WssTradeURL    = "wss://trades.grvt.io/ws/lite"
	WssTradeRpcURL = "wss://trades.grvt.io/ws/lite"

	// market streams
	StreamMiniTickerSnap  = "v1.mini.s"
	StreamMiniTickerDelta = "v1.mini.d"
	StreamTickerSnap      = "v1.ticker.s"
	StreamTickerDelta     = "v1.ticker.d"
	StreamOrderbookSnap   = "v1.book.s"
	StreamOrderbookDelta  = "v1.book.d"
	StreamTrade           = "v1.trade"
	StreamKline           = "v1.candle"

	// account stream
	StreamOrderUpdate = "v1.order"
	StreamOrderState  = "v1.state"
	StreamOrderCancel = "v1.cancel"

	// fill
	StreamFill      = "v1.fill"
	StreamPositions = "v1.position"

	// transfer
	StreamDeposit    = "v1.deposit"
	StreamTransfer   = "v1.transfer"
	StreamWithdrawal = "v1.withdrawal"
)

type WsRequest struct {
	JsonRpc string           `json:"j"`
	Method  string           `json:"m"`
	Params  *WsRequestParams `json:"p"`
	Id      int64            `json:"i"`
}

type WsRequestParams struct {
	Stream    string   `json:"s,omitempty"`
	Selectors []string `json:"s1,omitempty"`
}

type WsLiteRpcRequest struct {
	JsonRpc string      `json:"j"`
	Method  string      `json:"m"`
	Params  interface{} `json:"p"`
	Id      uint32      `json:"i"`
}

type WsRpcRequest struct {
	JsonRpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	Id      uint64      `json:"id"`
}

type WsRpcResponse struct {
	JsonRpc string          `json:"j"`
	Id      uint32          `json:"i"` // Use uint32 to match Request
	Result  json.RawMessage `json:"r,omitempty"`
	Error   *WsRpcError     `json:"e,omitempty"`
}

type WsRpcError struct {
	Code    int    `json:"c"`
	Message string `json:"m"`
	Data    string `json:"d,omitempty"` // Guessing 'd' for data
}

type WsCreateOrderParams struct {
	Order WsOrderBody `json:"o"`
}

type WsOrderBody struct {
	SubAccountID string           `json:"sa"`
	IsMarket     bool             `json:"im"`
	TimeInForce  TimeInForce      `json:"ti"`
	PostOnly     bool             `json:"po"`
	ReduceOnly   bool             `json:"ro"`
	Legs         []WsOrderLeg     `json:"l"`
	Signature    WsOrderSignature `json:"s"`
	Metadata     WsOrderMetadata  `json:"m"`
}

type WsOrderLeg struct {
	Instrument       string `json:"i"`
	Size             string `json:"s"`
	LimitPrice       string `json:"lp"`
	IsBuyingContract bool   `json:"ib"`
}

type WsOrderSignature struct {
	Signer     string `json:"s"`
	R          string `json:"r"`
	S          string `json:"s1"`
	V          int    `json:"v"`
	Expiration string `json:"e"`
	Nonce      uint32 `json:"n"`
	ChainID    string `json:"ci"`
}

type WsOrderMetadata struct {
	ClientOrderID string `json:"co"`
	CreatedTime   string `json:"ct,omitempty"`
}

type WsRequestResponse struct {
	JsonRpc string `json:"j"`
	Method  string `json:"m"`
	Result  struct {
		Stream              string   `json:"s"`
		Subs                []string `json:"subs"`
		Unsubs              []string `json:"unsubs"`
		NumSnapshots        []int64  `json:"num_snapshots"`
		FirstSequenceNumber []int64  `json:"first_sequence_number"`
	} `json:"r"`
	Id int64 `json:"i"`
}

type WsFeeData[T any] struct {
	Stream         string `json:"s"`
	Selector       string `json:"s1"`
	SequenceNumber string `json:"sn"`
	Feed           T      `json:"f"`
}

type WsOrderState struct {
	OrderID       string     `json:"oi"`
	ClientOrderID string     `json:"co"`
	OrderState    OrderState `json:"os"`
}

type WsCancelOrderStatus struct {
	SubAccountID  string `json:"sa"`
	ClientOrderID string `json:"co"`
	OrderID       string `json:"oi"`
	Reason        string `json:"r"`
	UpdateTime    string `json:"ut"`
	CancelStatus  string `json:"cs"`
}

type WsFill struct {
	EventTime     string  `json:"et"`
	SubAccountID  string  `json:"sa"`
	Instrument    string  `json:"i"`
	IsBuyer       bool    `json:"ib"`
	IsTaker       bool    `json:"it"`
	Size          string  `json:"s"`
	Price         string  `json:"p"`
	MarkPrice     string  `json:"mp"`
	IndexPrice    string  `json:"ip"`
	InterestRate  float64 `json:"ir"`
	ForwardPrice  string  `json:"fp"`
	RealizedPnl   string  `json:"rp"`
	Fee           string  `json:"f"`
	FeeRate       string  `json:"fr"`
	TradeID       string  `json:"ti"`
	OrderID       string  `json:"oi"`
	Venue         string  `json:"v"`
	ClientOrderID string  `json:"co"`
	Signer        string  `json:"s1"`
	Broker        string  `json:"b"`
	IsRpi         bool    `json:"ir1"`
}

type WsDeposit struct {
	TxHash      string `json:"th"`
	ToAccountID string `json:"ta"`
	Currency    string `json:"c"`
	NumTokens   string `json:"nt"`
}

type WsTransfer struct {
	TxId             string         `json:"ti"`
	FromAccountID    string         `json:"fa"`
	FromSubAccountID string         `json:"fs"`
	ToAccountID      string         `json:"ta"`
	ToSubAccountID   string         `json:"ts"`
	Currency         string         `json:"c"`
	NumTokens        string         `json:"nt"`
	Signature        OrderSignature `json:"s"`
	EventTime        string         `json:"et"`
	TransferType     string         `json:"tt"`
}

type WsWithdrawal struct {
	FromAccountID string         `json:"fa"`
	ToEthAddress  string         `json:"te"`
	Currency      string         `json:"c"`
	NumTokens     string         `json:"nt"`
	Signature     OrderSignature `json:"s"`
}

type KlineInterval string

const (
	KlineInterval1m  KlineInterval = "CI_1_M"
	KlineInterval3m  KlineInterval = "CI_3_M"
	KlineInterval5m  KlineInterval = "CI_5_M"
	KlineInterval15m KlineInterval = "CI_15_M"
	KlineInterval30m KlineInterval = "CI_30_M"
	KlineInterval1h  KlineInterval = "CI_1_H"
	KlineInterval2h  KlineInterval = "CI_2_H"
	KlineInterval4h  KlineInterval = "CI_4_H"
	KlineInterval8h  KlineInterval = "CI_8_H"
	KlineInterval12h KlineInterval = "CI_12_H"
	KlineInterval1d  KlineInterval = "CI_1_D"
	KlineInterval3d  KlineInterval = "CI_3_D"
	KlineInterval5d  KlineInterval = "CI_5_D"
	KlineInterval1w  KlineInterval = "CI_1_W"
	KlineInterval2w  KlineInterval = "CI_2_W"
	KlineInterval3w  KlineInterval = "CI_3_W"
	KlineInterval4w  KlineInterval = "CI_4_W"
)

type KlineType string

const (
	KlineTypeTrade KlineType = "TRADE" // tracks trade prices
	KlineTypeMark  KlineType = "MARK"  // tracks mark prices
	KlineTypeIndex KlineType = "INDEX" // tracks index prices
	KlineTypeMid   KlineType = "MID"   // tracks book mid prices
)

type TradeLimit int

const (
	TradeLimit50   TradeLimit = 50
	TradeLimit200  TradeLimit = 200
	TradeLimit500  TradeLimit = 500
	TradeLimit1000 TradeLimit = 1000
)

type OrderBookDeltaRate int

const (
	OrderBookDeltaRate50   OrderBookDeltaRate = 50
	OrderBookDeltaRate100  OrderBookDeltaRate = 100
	OrderBookDeltaRate500  OrderBookDeltaRate = 500
	OrderBookDeltaRate1000 OrderBookDeltaRate = 1000
)

type OrderBookSnapRate int

const (
	OrderBookSnapRate500  OrderBookSnapRate = 500
	OrderBookSnapRate1000 OrderBookSnapRate = 1000
)

type OrderBookSnapDepth int

const (
	OrderBookSnapDepth10  OrderBookSnapDepth = 10
	OrderBookSnapDepth50  OrderBookSnapDepth = 50
	OrderBookSnapDepth100 OrderBookSnapDepth = 100
	OrderBookSnapDepth500 OrderBookSnapDepth = 500
)

type TickerDeltaRate int

const (
	TickerDeltaRate100  TickerDeltaRate = 100
	TickerDeltaRate200  TickerDeltaRate = 200
	TickerDeltaRate500  TickerDeltaRate = 500
	TickerDeltaRate1000 TickerDeltaRate = 1000
	TickerDeltaRate5000 TickerDeltaRate = 5000
)

type TickerSnapRate int

const (
	TickerSnapRate500  TickerSnapRate = 500
	TickerSnapRate1000 TickerSnapRate = 1000
	TickerSnapRate5000 TickerSnapRate = 5000
)

type MiniTickerDeltaRate int

const (
	MiniTickerDeltaRate0    MiniTickerDeltaRate = 0
	MiniTickerDeltaRate50   MiniTickerDeltaRate = 50
	MiniTickerDeltaRate100  MiniTickerDeltaRate = 100
	MiniTickerDeltaRate200  MiniTickerDeltaRate = 200
	MiniTickerDeltaRate500  MiniTickerDeltaRate = 500
	MiniTickerDeltaRate1000 MiniTickerDeltaRate = 1000
	MiniTickerDeltaRate5000 MiniTickerDeltaRate = 5000
)

type MiniTickerSnapRate int

const (
	MiniTickerSnapRate200  MiniTickerSnapRate = 200
	MiniTickerSnapRate500  MiniTickerSnapRate = 500
	MiniTickerSnapRate1000 MiniTickerSnapRate = 1000
	MiniTickerSnapRate5000 MiniTickerSnapRate = 5000
)
