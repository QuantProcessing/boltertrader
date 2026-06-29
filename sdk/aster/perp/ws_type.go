package perp

import "time"

const (
	PongInterval         = 1 * time.Minute
	ReconnectWait        = 1 * time.Second
	MaxReconnectAttempts = 10
)

var ArrayEventMap = map[string]string{
	"markPriceUpdate": "!markPrice@arr@1s", // only use 1s, its best choice
	"24hrTicker":      "!ticker@arr",
	"24hrMiniTicker":  "!miniTicker@arr",
}

// special handle kline and !bookTicker
var SingleEventMap = map[string]string{
	"depthUpdate":     "depth@100ms",  // only 100ms, its best choice
	"markPriceUpdate": "markPrice@1s", // only use 1s, its best than 3s
	"24hrTicker":      "ticker",
	"24hrMiniTicker":  "miniTicker",
	"bookTicker":      "bookTicker",
	"aggTrade":        "aggTrade",
	"trade":           "trade",
}

type OrderUpdateEvent struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	TransactionTime int64  `json:"T"`
	Order           struct {
		Symbol               string `json:"s"`
		ClientOrderID        string `json:"c"`
		Side                 string `json:"S"`
		OrderType            string `json:"o"`
		TimeInForce          string `json:"f"`
		OriginalQty          string `json:"q"`
		OriginalPrice        string `json:"p"`
		AveragePrice         string `json:"ap"`
		StopPrice            string `json:"sp"`
		ExecutionType        string `json:"x"`
		OrderStatus          string `json:"X"`
		OrderID              int64  `json:"i"`
		LastFilledQty        string `json:"l"`
		AccumulatedFilledQty string `json:"z"`
		LastFilledPrice      string `json:"L"`
		CommissionAsset      string `json:"N"`
		Commission           string `json:"n"`
		TradeTime            int64  `json:"T"`
		TradeID              int64  `json:"t"`
		BidsNotional         string `json:"b"`
		AsksNotional         string `json:"a"`
		IsMaker              bool   `json:"m"`
		IsReduceOnly         bool   `json:"R"`
		WorkingType          string `json:"wt"`
		OriginalType         string `json:"ot"`
		PositionSide         string `json:"ps"`
		ClosePosition        bool   `json:"cp"`
		ActivationPrice      string `json:"AP"`
		CallbackRate         string `json:"cr"`
		RealizedProfit       string `json:"rp"`
	} `json:"o"`
}

type AccountUpdateEvent struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	TransactionTime int64  `json:"T"`
	UpdateData      struct {
		EventReasonType string `json:"m"`
		Balances        []struct {
			Asset              string `json:"a"`
			WalletBalance      string `json:"wb"`
			CrossWalletBalance string `json:"cw"`
			BalanceChange      string `json:"bc"`
		} `json:"B"`
		Positions []struct {
			Symbol              string `json:"s"`
			PositionAmount      string `json:"pa"`
			EntryPrice          string `json:"ep"`
			AccumulatedRealized string `json:"cr"`
			UnrealizedPnL       string `json:"up"`
			MarginType          string `json:"mt"`
			IsolatedWallet      string `json:"iw"`
			PositionSide        string `json:"ps"`
		} `json:"P"`
	} `json:"a"`
}

type TradeLiteEvent struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	TransactionTime int64  `json:"T"`
	Symbol          string `json:"s"`
	OriginalQty     string `json:"q"`
	OriginalPrice   string `json:"p"`
	IsMaker         bool   `json:"m"`
	ClientOrderID   string `json:"c"`
	Side            string `json:"S"`
	LastFilledPrice string `json:"L"`
	LastFilledQty   string `json:"l"`
	TradeID         int64  `json:"t"`
	OrderID         int64  `json:"i"`
}

type AccountConfigUpdateEvent struct {
	EventType       string `json:"e"`
	EventTime       int64  `json:"E"`
	TransactionTime int64  `json:"T"`
	AccountConfig   struct {
		Symbol   string `json:"s"`
		Leverage int    `json:"l"`
	} `json:"ac"`
	MultiAssetsConfig struct {
		MultiAssets bool `json:"j"`
	} `json:"ai"`
}
