package sdk

import (
	"encoding/json"
	"strings"
)

type NumberString string

func (n *NumberString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	trimmed = strings.Trim(trimmed, `"`)
	*n = NumberString(trimmed)
	return nil
}

type responseEnvelope[T any] struct {
	RetCode int    `json:"retCode"`
	RetMsg  string `json:"retMsg"`
	Result  T      `json:"result"`
	Time    int64  `json:"time"`
}

type InstrumentsResult struct {
	Category       string       `json:"category"`
	List           []Instrument `json:"list"`
	NextPageCursor string       `json:"nextPageCursor"`
}

type Instrument struct {
	Symbol        string        `json:"symbol"`
	BaseCoin      string        `json:"baseCoin"`
	QuoteCoin     string        `json:"quoteCoin"`
	SettleCoin    string        `json:"settleCoin"`
	Status        string        `json:"status"`
	OptionsType   string        `json:"optionsType"`
	LaunchTime    string        `json:"launchTime"`
	DeliveryTime  string        `json:"deliveryTime"`
	DisplayName   string        `json:"displayName"`
	PriceScale    string        `json:"priceScale"`
	PriceFilter   PriceFilter   `json:"priceFilter"`
	LotSizeFilter LotSizeFilter `json:"lotSizeFilter"`
}

type PriceFilter struct {
	TickSize string `json:"tickSize"`
}

type LotSizeFilter struct {
	BasePrecision    string `json:"basePrecision"`
	QtyStep          string `json:"qtyStep"`
	MinOrderQty      string `json:"minOrderQty"`
	MinOrderAmt      string `json:"minOrderAmt"`
	MinNotionalValue string `json:"minNotionalValue"`
}

type TickersResult struct {
	Category string   `json:"category"`
	List     []Ticker `json:"list"`
}

type Ticker struct {
	Symbol              string `json:"symbol"`
	LastPrice           string `json:"lastPrice"`
	Bid1Price           string `json:"bid1Price"`
	Bid1Size            string `json:"bid1Size"`
	Bid1IV              string `json:"bid1Iv"`
	Ask1Price           string `json:"ask1Price"`
	Ask1Size            string `json:"ask1Size"`
	Ask1IV              string `json:"ask1Iv"`
	Volume24h           string `json:"volume24h"`
	Turnover24h         string `json:"turnover24h"`
	HighPrice24h        string `json:"highPrice24h"`
	LowPrice24h         string `json:"lowPrice24h"`
	IndexPrice          string `json:"indexPrice"`
	MarkPrice           string `json:"markPrice"`
	MarkIV              string `json:"markIv"`
	UnderlyingPrice     string `json:"underlyingPrice"`
	OpenInterest        string `json:"openInterest"`
	Delta               string `json:"delta"`
	Gamma               string `json:"gamma"`
	Vega                string `json:"vega"`
	Theta               string `json:"theta"`
	FundingRate         string `json:"fundingRate"`
	NextFundingTime     string `json:"nextFundingTime"`
	FundingIntervalHour string `json:"fundingIntervalHour"`
	Time                string `json:"time"`
	TS                  string `json:"ts"`
}

type OrderBook struct {
	Symbol string           `json:"s"`
	Bids   [][]NumberString `json:"b"`
	Asks   [][]NumberString `json:"a"`
	TS     int64            `json:"ts"`
	U      int64            `json:"u"`
}

type PublicTradesResult struct {
	Category string        `json:"category"`
	List     []PublicTrade `json:"list"`
}

type PublicTrade struct {
	ExecID string `json:"execId"`
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Size   string `json:"size"`
	Side   string `json:"side"`
	Time   string `json:"time"`
}

type KlinesResult struct {
	Category string   `json:"category"`
	Symbol   string   `json:"symbol"`
	List     []Candle `json:"list"`
}

type Candle [7]NumberString

func (c *Candle) UnmarshalJSON(data []byte) error {
	var raw []NumberString
	if err := jsonArrayUnmarshal(data, &raw); err != nil {
		return err
	}
	for i := 0; i < len(c) && i < len(raw); i++ {
		c[i] = raw[i]
	}
	return nil
}

type WalletBalanceResult struct {
	List []WalletAccount `json:"list"`
}

type WalletAccount struct {
	AccountType           string       `json:"accountType"`
	TotalEquity           string       `json:"totalEquity"`
	TotalAvailableBalance string       `json:"totalAvailableBalance"`
	TotalPerpUPL          string       `json:"totalPerpUPL"`
	TotalWalletBalance    string       `json:"totalWalletBalance"`
	Coin                  []WalletCoin `json:"coin"`
}

type WalletCoin struct {
	Coin           string `json:"coin"`
	Equity         string `json:"equity"`
	WalletBalance  string `json:"walletBalance"`
	Locked         string `json:"locked"`
	BorrowAmount   string `json:"borrowAmount"`
	SpotBorrow     string `json:"spotBorrow"`
	UnrealisedPnl  string `json:"unrealisedPnl"`
	CumRealisedPnl string `json:"cumRealisedPnl"`
	UsdValue       string `json:"usdValue"`
}

type UnifiedMarginStatus int

const (
	UnifiedMarginStatusClassic UnifiedMarginStatus = 1
	UnifiedMarginStatusUTA1    UnifiedMarginStatus = 3
	UnifiedMarginStatusUTA1Pro UnifiedMarginStatus = 4
	UnifiedMarginStatusUTA2    UnifiedMarginStatus = 5
	UnifiedMarginStatusUTA2Pro UnifiedMarginStatus = 6
)

type AccountMode string

const (
	AccountModeUnknown AccountMode = ""
	AccountModeClassic AccountMode = "CLASSIC"
	AccountModeUTA1    AccountMode = "UTA1"
	AccountModeUTA2    AccountMode = "UTA2"
)

type AccountInfo struct {
	UnifiedMarginStatus UnifiedMarginStatus `json:"unifiedMarginStatus"`
	MarginMode          string              `json:"marginMode"`
	IsMasterTrader      bool                `json:"isMasterTrader"`
	SpotHedgingStatus   string              `json:"spotHedgingStatus"`
	UpdatedTime         string              `json:"updatedTime"`
	DCPStatus           string              `json:"dcpStatus"`
	TimeWindow          int                 `json:"timeWindow"`
	SMPGroup            int                 `json:"smpGroup"`
}

func (a AccountInfo) AccountMode() AccountMode {
	switch a.UnifiedMarginStatus {
	case UnifiedMarginStatusClassic:
		return AccountModeClassic
	case UnifiedMarginStatusUTA1, UnifiedMarginStatusUTA1Pro:
		return AccountModeUTA1
	case UnifiedMarginStatusUTA2, UnifiedMarginStatusUTA2Pro:
		return AccountModeUTA2
	default:
		return AccountModeUnknown
	}
}

func (m AccountMode) IsUnified() bool {
	return m == AccountModeUTA1 || m == AccountModeUTA2
}

type FeeRatesResult struct {
	List []FeeRateRecord `json:"list"`
}

type FeeRateRecord struct {
	Symbol       string `json:"symbol"`
	MakerFeeRate string `json:"makerFeeRate"`
	TakerFeeRate string `json:"takerFeeRate"`
	BaseCoin     string `json:"baseCoin"`
}

type PositionsResult struct {
	NextPageCursor string           `json:"nextPageCursor"`
	List           []PositionRecord `json:"list"`
}

type PositionRecord struct {
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Size           string `json:"size"`
	AvgPrice       string `json:"avgPrice"`
	Leverage       string `json:"leverage"`
	UnrealisedPnl  string `json:"unrealisedPnl"`
	CumRealisedPnl string `json:"cumRealisedPnl"`
	LiqPrice       string `json:"liqPrice"`
}

type SetLeverageRequest struct {
	Category     string `json:"category"`
	Symbol       string `json:"symbol"`
	BuyLeverage  string `json:"buyLeverage"`
	SellLeverage string `json:"sellLeverage"`
}

type SwitchPositionModeRequest struct {
	Category string `json:"category"`
	Symbol   string `json:"symbol,omitempty"`
	Coin     string `json:"coin,omitempty"`
	Mode     int    `json:"mode"`
}

type BorrowSpotRequest struct {
	Coin   string `json:"coin"`
	Amount string `json:"amount"`
}

type BorrowSpotResult struct {
	Coin   string `json:"coin"`
	Amount string `json:"amount"`
}

type RepaySpotBorrowRequest struct {
	Coin          string `json:"coin"`
	Amount        string `json:"amount,omitempty"`
	RepaymentType string `json:"repaymentType,omitempty"`
}

type RepaySpotBorrowResult struct {
	ResultStatus string `json:"resultStatus"`
}

type SetMarginModeRequest struct {
	SetMarginMode string `json:"setMarginMode"`
}

type SetMarginModeResult struct {
	Reasons []SetMarginModeReason `json:"reasons"`
}

type SetMarginModeReason struct {
	ReasonCode string `json:"reasonCode"`
	ReasonMsg  string `json:"reasonMsg"`
}

type PlaceOrderRequest struct {
	Category              string `json:"category"`
	Symbol                string `json:"symbol"`
	Side                  string `json:"side"`
	OrderType             string `json:"orderType"`
	Qty                   string `json:"qty"`
	Price                 string `json:"price,omitempty"`
	TimeInForce           string `json:"timeInForce,omitempty"`
	ReduceOnly            bool   `json:"reduceOnly,omitempty"`
	OrderLinkID           string `json:"orderLinkId,omitempty"`
	MarketUnit            string `json:"marketUnit,omitempty"`
	SlippageToleranceType string `json:"slippageToleranceType,omitempty"`
	SlippageTolerance     string `json:"slippageTolerance,omitempty"`
	TakeProfit            string `json:"takeProfit,omitempty"`
	StopLoss              string `json:"stopLoss,omitempty"`
	TPTriggerBy           string `json:"tpTriggerBy,omitempty"`
	SLTriggerBy           string `json:"slTriggerBy,omitempty"`
	TPOrderType           string `json:"tpOrderType,omitempty"`
	SLOrderType           string `json:"slOrderType,omitempty"`
	TPTriggerPrice        string `json:"tpTriggerPrice,omitempty"`
	SLTriggerPrice        string `json:"slTriggerPrice,omitempty"`
	TPLimitPrice          string `json:"tpLimitPrice,omitempty"`
	SLLimitPrice          string `json:"slLimitPrice,omitempty"`
	CloseOnTrigger        bool   `json:"closeOnTrigger,omitempty"`
	IsLeverage            string `json:"isLeverage,omitempty"`
	PositionIdx           int    `json:"positionIdx,omitempty"`
	BBOSideType           string `json:"bboSideType,omitempty"`
	BBOLevel              string `json:"bboLevel,omitempty"`
	OrderIV               string `json:"orderIv,omitempty"`
	MMP                   bool   `json:"mmp,omitempty"`
}

type BatchPlaceOrderItem struct {
	Symbol                string `json:"symbol"`
	Side                  string `json:"side"`
	OrderType             string `json:"orderType"`
	Qty                   string `json:"qty"`
	Price                 string `json:"price,omitempty"`
	TimeInForce           string `json:"timeInForce,omitempty"`
	ReduceOnly            bool   `json:"reduceOnly,omitempty"`
	OrderLinkID           string `json:"orderLinkId,omitempty"`
	MarketUnit            string `json:"marketUnit,omitempty"`
	SlippageToleranceType string `json:"slippageToleranceType,omitempty"`
	SlippageTolerance     string `json:"slippageTolerance,omitempty"`
	TakeProfit            string `json:"takeProfit,omitempty"`
	StopLoss              string `json:"stopLoss,omitempty"`
	TPTriggerBy           string `json:"tpTriggerBy,omitempty"`
	SLTriggerBy           string `json:"slTriggerBy,omitempty"`
	TPOrderType           string `json:"tpOrderType,omitempty"`
	SLOrderType           string `json:"slOrderType,omitempty"`
	TPTriggerPrice        string `json:"tpTriggerPrice,omitempty"`
	SLTriggerPrice        string `json:"slTriggerPrice,omitempty"`
	TPLimitPrice          string `json:"tpLimitPrice,omitempty"`
	SLLimitPrice          string `json:"slLimitPrice,omitempty"`
	CloseOnTrigger        bool   `json:"closeOnTrigger,omitempty"`
	IsLeverage            string `json:"isLeverage,omitempty"`
	PositionIdx           int    `json:"positionIdx,omitempty"`
	BBOSideType           string `json:"bboSideType,omitempty"`
	BBOLevel              string `json:"bboLevel,omitempty"`
	OrderIV               string `json:"orderIv,omitempty"`
	MMP                   bool   `json:"mmp,omitempty"`
}

type BatchPlaceOrdersRequest struct {
	Category string                `json:"category"`
	Request  []BatchPlaceOrderItem `json:"request"`
}

type CancelOrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
}

type BatchCancelOrderItem struct {
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
}

type BatchCancelOrdersRequest struct {
	Category string                 `json:"category"`
	Request  []BatchCancelOrderItem `json:"request"`
}

type CancelAllOrdersRequest struct {
	Category   string `json:"category"`
	Symbol     string `json:"symbol,omitempty"`
	BaseCoin   string `json:"baseCoin,omitempty"`
	SettleCoin string `json:"settleCoin,omitempty"`
}

type AmendOrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
	Qty         string `json:"qty,omitempty"`
	Price       string `json:"price,omitempty"`
	OrderIV     string `json:"orderIv,omitempty"`
}

type BatchAmendOrderItem struct {
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
	Qty         string `json:"qty,omitempty"`
	Price       string `json:"price,omitempty"`
	OrderIV     string `json:"orderIv,omitempty"`
}

type BatchAmendOrdersRequest struct {
	Category string                `json:"category"`
	Request  []BatchAmendOrderItem `json:"request"`
}

type OrderActionResponse struct {
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
}

type BatchOrderActionResponse struct {
	Category    string `json:"category,omitempty"`
	Symbol      string `json:"symbol,omitempty"`
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
}

type BatchOrderActionResult struct {
	List []BatchOrderActionResponse `json:"list"`
}

type OrdersResult struct {
	List           []OrderRecord `json:"list"`
	NextPageCursor string        `json:"nextPageCursor"`
}

type OrderRecord struct {
	OrderID            string `json:"orderId"`
	OrderLinkID        string `json:"orderLinkId"`
	Symbol             string `json:"symbol"`
	Side               string `json:"side"`
	OrderType          string `json:"orderType"`
	TimeInForce        string `json:"timeInForce"`
	Price              string `json:"price"`
	Qty                string `json:"qty"`
	CumExecQty         string `json:"cumExecQty"`
	AvgPrice           string `json:"avgPrice"`
	OrderStatus        string `json:"orderStatus"`
	ReduceOnly         bool   `json:"reduceOnly"`
	CreatedTime        string `json:"createdTime"`
	UpdatedTime        string `json:"updatedTime"`
	CumExecFee         string `json:"cumExecFee"`
	ClosedPnl          string `json:"closedPnl"`
	TriggerPrice       string `json:"triggerPrice"`
	LastPriceOnCreated string `json:"lastPriceOnCreated"`
}

type ExecutionRecord struct {
	ExecID      string `json:"execId"`
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	ExecPrice   string `json:"execPrice"`
	ExecQty     string `json:"execQty"`
	ExecFee     string `json:"execFee"`
	FeeCurrency string `json:"feeCurrency"`
	IsMaker     bool   `json:"isMaker"`
	ExecTime    string `json:"execTime"`
}

type ExecutionsResult struct {
	List           []ExecutionRecord `json:"list"`
	NextPageCursor string            `json:"nextPageCursor"`
}

// OpenInterestEntry is one row of the open-interest history list.
type OpenInterestEntry struct {
	OpenInterest string `json:"openInterest"`
	Timestamp    string `json:"timestamp"`
}

// OpenInterestResult is the result payload of /v5/market/open-interest.
type OpenInterestResult struct {
	Category       string              `json:"category"`
	Symbol         string              `json:"symbol"`
	List           []OpenInterestEntry `json:"list"`
	NextPageCursor string              `json:"nextPageCursor,omitempty"`
}

// FundingHistoryEntry matches one row of /v5/market/funding/history result.list.
type FundingHistoryEntry struct {
	Symbol               string `json:"symbol"`
	FundingRate          string `json:"fundingRate"`
	FundingRateTimestamp string `json:"fundingRateTimestamp"`
}

// FundingHistoryResult is the result payload of /v5/market/funding/history.
type FundingHistoryResult struct {
	Category string                `json:"category"`
	List     []FundingHistoryEntry `json:"list"`
}

func jsonArrayUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}
