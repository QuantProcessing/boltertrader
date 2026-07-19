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

type Instrument struct {
	Symbol             string `json:"symbol"`
	Category           string `json:"category"`
	BaseCoin           string `json:"baseCoin"`
	QuoteCoin          string `json:"quoteCoin"`
	MinOrderQty        string `json:"minOrderQty"`
	MaxOrderQty        string `json:"maxOrderQty"`
	MinOrderAmount     string `json:"minOrderAmount"`
	PricePrecision     string `json:"pricePrecision"`
	QuantityPrecision  string `json:"quantityPrecision"`
	QuotePrecision     string `json:"quotePrecision"`
	PriceMultiplier    string `json:"priceMultiplier"`
	QuantityMultiplier string `json:"quantityMultiplier"`
	MakerFeeRate       string `json:"makerFeeRate"`
	TakerFeeRate       string `json:"takerFeeRate"`
	FundInterval       string `json:"fundInterval"`
	Status             string `json:"status"`
}

type PlaceOrderRequest struct {
	Category     string `json:"category"`
	Symbol       string `json:"symbol"`
	Qty          string `json:"qty"`
	Price        string `json:"price,omitempty"`
	Side         string `json:"side"`
	TradeSide    string `json:"tradeSide,omitempty"`
	OrderType    string `json:"orderType"`
	TimeInForce  string `json:"timeInForce,omitempty"`
	MarginMode   string `json:"marginMode,omitempty"`
	MarginCoin   string `json:"marginCoin,omitempty"`
	ClientOID    string `json:"clientOid,omitempty"`
	ReduceOnly   string `json:"reduceOnly,omitempty"`
	PosSide      string `json:"posSide,omitempty"`
	STPMode      string `json:"stpMode,omitempty"`
	TPTriggerBy  string `json:"tpTriggerBy,omitempty"`
	SLTriggerBy  string `json:"slTriggerBy,omitempty"`
	TakeProfit   string `json:"takeProfit,omitempty"`
	StopLoss     string `json:"stopLoss,omitempty"`
	TPOrderType  string `json:"tpOrderType,omitempty"`
	SLOrderType  string `json:"slOrderType,omitempty"`
	TPLimitPrice string `json:"tpLimitPrice,omitempty"`
	SLLimitPrice string `json:"slLimitPrice,omitempty"`
}

type PlaceOrderResponse struct {
	OrderID   string `json:"orderId"`
	ClientOID string `json:"clientOid"`
	Code      string `json:"code,omitempty"`
	Msg       string `json:"msg,omitempty"`
}

type CancelOrderRequest struct {
	Category  string `json:"category"`
	Symbol    string `json:"symbol"`
	OrderID   string `json:"orderId,omitempty"`
	ClientOID string `json:"clientOid,omitempty"`
}

type CancelOrderResponse struct {
	OrderID   string `json:"orderId"`
	ClientOID string `json:"clientOid"`
	Code      string `json:"code,omitempty"`
	Msg       string `json:"msg,omitempty"`
}

type CancelAllOrdersRequest struct {
	Category string `json:"category"`
	Symbol   string `json:"symbol"`
}

type ModifyOrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	OrderID     string `json:"orderId,omitempty"`
	ClientOID   string `json:"clientOid,omitempty"`
	NewQty      string `json:"qty,omitempty"`
	NewPrice    string `json:"price,omitempty"`
	NewClientID string `json:"newClientOid,omitempty"`
	AutoCancel  string `json:"autoCancel,omitempty"`
}

type OrderRecord struct {
	OrderID      string      `json:"orderId"`
	ClientOID    string      `json:"clientOid"`
	Symbol       string      `json:"symbol"`
	Category     string      `json:"category"`
	Side         string      `json:"side"`
	OrderType    string      `json:"orderType"`
	TimeInForce  string      `json:"timeInForce"`
	Price        string      `json:"price"`
	Qty          string      `json:"qty"`
	Amount       string      `json:"amount"`
	BaseVolume   string      `json:"baseVolume"`
	FilledQty    string      `json:"filledQty"`
	FilledVolume string      `json:"filledVolume"`
	CumExecQty   string      `json:"cumExecQty"`
	CumExecValue string      `json:"cumExecValue"`
	OrderStatus  string      `json:"orderStatus"`
	ReduceOnly   string      `json:"reduceOnly"`
	PosSide      string      `json:"posSide"`
	HoldSide     string      `json:"holdSide"`
	HoldMode     string      `json:"holdMode"`
	TradeSide    string      `json:"tradeSide"`
	MarginMode   string      `json:"marginMode"`
	MarginCoin   string      `json:"marginCoin"`
	AvgPrice     string      `json:"avgPrice"`
	Fee          string      `json:"fee"`
	TotalProfit  string      `json:"totalProfit"`
	CreatedTime  string      `json:"createdTime"`
	UpdatedTime  string      `json:"updatedTime"`
	CTime        string      `json:"cTime"`
	UTime        string      `json:"uTime"`
	DelegateType string      `json:"delegateType"`
	StpMode      string      `json:"stpMode"`
	FeeDetail    []FeeDetail `json:"feeDetail"`
}

type OrderList struct {
	List  []OrderRecord `json:"list"`
	EndID string        `json:"endId"`
}

// GetOrderHistoryRequest scopes UTA order-history retrieval. Limit is the
// overall record cap used by GetOrderHistoryBounded; each venue request is
// automatically capped at Bitget's 100-row page maximum.
type GetOrderHistoryRequest struct {
	Category  string
	Symbol    string
	StartTime string
	EndTime   string
	Limit     string
	Cursor    string
}

type GetFillsRequest struct {
	Category  string
	OrderID   string
	StartTime string
	EndTime   string
	Limit     string
	Cursor    string
}

type FillList struct {
	List   []FillRecord `json:"list"`
	Cursor string       `json:"cursor"`
}

type AccountAsset struct {
	Coin      string `json:"coin"`
	Available string `json:"available"`
	Frozen    string `json:"frozen"`
	Locked    string `json:"locked"`
	Equity    string `json:"equity"`
	USDTValue string `json:"usdtValue"`
	USDValue  string `json:"usdValue"`
	Bonus     string `json:"bonus"`
}

type AccountAssets struct {
	AccountEquity    string         `json:"accountEquity"`
	UsdtEquity       string         `json:"usdtEquity"`
	Available        string         `json:"available"`
	UnrealizedPL     string         `json:"unrealizedPL"`
	Coupon           string         `json:"coupon"`
	UnionTotalMargin string         `json:"unionTotalMargin"`
	Assets           []AccountAsset `json:"assets"`
}

type AccountInfo struct {
	UserID      string       `json:"userId"`
	InviterID   string       `json:"inviterId"`
	ParentID    NumberString `json:"parentId"`
	ChannelCode string       `json:"channelCode"`
	Channel     string       `json:"channel"`
	IPs         string       `json:"ips"`
	PermType    string       `json:"permType"`
	Permissions []string     `json:"permissions"`
	RegisTime   string       `json:"regisTime"`
}

type AccountSettings struct {
	AccountMode    string                 `json:"accountMode"`
	AssetMode      string                 `json:"assetMode"`
	AccountLevel   string                 `json:"accountLevel"`
	HoldMode       string                 `json:"holdMode"`
	SymbolSettings []AccountSymbolSetting `json:"symbolSettings"`
}

type AccountSymbolSetting struct {
	Symbol     string `json:"symbol"`
	Category   string `json:"category"`
	MarginMode string `json:"marginMode"`
}

type FundingAsset struct {
	Coin      string `json:"coin"`
	Available string `json:"available"`
	Frozen    string `json:"frozen"`
	Balance   string `json:"balance"`
}

type FinancialRecordsRequest struct {
	Category  string
	Coin      string
	Type      string
	StartTime string
	EndTime   string
	Limit     string
	Cursor    string
}

type FinancialRecords struct {
	List   []FinancialRecord `json:"list"`
	Cursor string            `json:"cursor"`
}

type FinancialRecord struct {
	Category string `json:"category"`
	ID       string `json:"id"`
	Symbol   string `json:"symbol"`
	Coin     string `json:"coin"`
	Type     string `json:"type"`
	Amount   string `json:"amount"`
	Fee      string `json:"fee"`
	Balance  string `json:"balance"`
	TS       string `json:"ts"`
}

type AccountFeeRate struct {
	MakerFeeRate string `json:"makerFeeRate"`
	TakerFeeRate string `json:"takerFeeRate"`
}

type SwitchStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type MaxTransferable struct {
	Coin              string `json:"coin"`
	MaxTransfer       string `json:"maxTransfer"`
	BorrowMaxTransfer string `json:"borrowMaxTransfer"`
}

type OpenInterestLimit struct {
	Symbol           string `json:"symbol"`
	SingleUserLimit  string `json:"singleUserLimit"`
	MasterSubLimit   string `json:"masterSubLimit"`
	MarketMakerLimit string `json:"marketMakerLimit"`
}

type PositionRecord struct {
	Symbol           string `json:"symbol"`
	Category         string `json:"category"`
	MarginCoin       string `json:"marginCoin"`
	PosSide          string `json:"posSide"`
	HoldSide         string `json:"holdSide"`
	HoldMode         string `json:"holdMode"`
	Qty              string `json:"qty"`
	Total            string `json:"total"`
	Size             string `json:"size"`
	Available        string `json:"available"`
	Frozen           string `json:"frozen"`
	AverageOpenPrice string `json:"averageOpenPrice"`
	OpenPriceAvg     string `json:"openPriceAvg"`
	AvgPrice         string `json:"avgPrice"`
	MarkPrice        string `json:"markPrice"`
	LiquidationPrice string `json:"liquidationPrice"`
	LiqPrice         string `json:"liqPrice"`
	Leverage         string `json:"leverage"`
	MarginMode       string `json:"marginMode"`
	UnrealisedPnl    string `json:"unrealisedPnl"`
	UnrealizedPL     string `json:"unrealizedPL"`
	AchievedProfits  string `json:"achievedProfits"`
	CurRealisedPnl   string `json:"curRealisedPnl"`
	PositionStatus   string `json:"positionStatus"`
	CreatedTime      string `json:"createdTime"`
	UpdatedTime      string `json:"updatedTime"`
}

type PositionList struct {
	List []PositionRecord `json:"list"`
}

type SetLeverageRequest struct {
	Symbol        string `json:"symbol"`
	Category      string `json:"category"`
	Leverage      string `json:"leverage"`
	Coin          string `json:"coin,omitempty"`
	PosSide       string `json:"posSide,omitempty"`
	MarginMode    string `json:"marginMode,omitempty"`
	LongLeverage  string `json:"longLeverage,omitempty"`
	ShortLeverage string `json:"shortLeverage,omitempty"`
}

type FeeDetail struct {
	FeeCoin string `json:"feeCoin"`
	Fee     string `json:"fee"`
}

type FillRecord struct {
	Category    string      `json:"category"`
	OrderID     string      `json:"orderId"`
	ClientOID   string      `json:"clientOid"`
	ExecID      string      `json:"execId"`
	ExecLinkID  string      `json:"execLinkId"`
	Symbol      string      `json:"symbol"`
	OrderType   string      `json:"orderType"`
	Side        string      `json:"side"`
	HoldSide    string      `json:"holdSide"`
	TradeSide   string      `json:"tradeSide"`
	ExecPrice   string      `json:"execPrice"`
	ExecQty     string      `json:"execQty"`
	ExecValue   string      `json:"execValue"`
	ExecPnl     string      `json:"execPnl"`
	TradeScope  string      `json:"tradeScope"`
	FeeDetail   []FeeDetail `json:"feeDetail"`
	ExecTime    string      `json:"execTime"`
	CreatedTime string      `json:"createdTime"`
	UpdatedTime string      `json:"updatedTime"`
	IsRPI       string      `json:"isRPI"`
}

type Ticker struct {
	Category            string `json:"category"`
	Symbol              string `json:"symbol"`
	Timestamp           string `json:"ts"`
	LastPrice           string `json:"lastPrice"`
	OpenPrice24h        string `json:"openPrice24h"`
	HighPrice24h        string `json:"highPrice24h"`
	LowPrice24h         string `json:"lowPrice24h"`
	Ask1Price           string `json:"ask1Price"`
	Bid1Price           string `json:"bid1Price"`
	Ask1Size            string `json:"ask1Size"`
	Bid1Size            string `json:"bid1Size"`
	Volume24h           string `json:"volume24h"`
	Turnover24h         string `json:"turnover24h"`
	IndexPrice          string `json:"indexPrice"`
	MarkPrice           string `json:"markPrice"`
	FundingRate         string `json:"fundingRate"`
	FundingRateInterval string `json:"fundingRateInterval"`
	NextFundingTime     string `json:"nextFundingTime"`
}

type OrderBook struct {
	Asks [][]NumberString `json:"a"`
	Bids [][]NumberString `json:"b"`
	TS   string           `json:"ts"`
}

type PublicFill struct {
	ExecID     string `json:"execId"`
	ExecLinkID string `json:"execLinkId"`
	Price      string `json:"price"`
	Size       string `json:"size"`
	Side       string `json:"side"`
	Timestamp  string `json:"ts"`
}

func (p *PublicFill) UnmarshalJSON(data []byte) error {
	type publicFill PublicFill
	var raw struct {
		publicFill
		V3ExecID    string `json:"i"`
		V3Price     string `json:"p"`
		V3Size      string `json:"v"`
		V3Side      string `json:"S"`
		V3Timestamp string `json:"T"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*p = PublicFill(raw.publicFill)
	if p.ExecID == "" {
		p.ExecID = raw.V3ExecID
	}
	if p.Price == "" {
		p.Price = raw.V3Price
	}
	if p.Size == "" {
		p.Size = raw.V3Size
	}
	if p.Side == "" {
		p.Side = raw.V3Side
	}
	if p.Timestamp == "" {
		p.Timestamp = raw.V3Timestamp
	}
	return nil
}

// OpenInterest matches /api/v2/mix/market/open-interest data.
type OpenInterest struct {
	List []OpenInterestEntry `json:"openInterestList"`
	TS   string              `json:"ts"`
}

type OpenInterestEntry struct {
	Symbol string `json:"symbol"`
	Size   string `json:"size"` // base-asset units
}

// HistoryFundRateEntry matches one element of /api/v2/mix/market/history-fund-rate.
type HistoryFundRateEntry struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime string `json:"fundingTime"`
}

type CurrentFundRateEntry struct {
	Symbol              string `json:"symbol"`
	FundingRate         string `json:"fundingRate"`
	FundingRateInterval string `json:"fundingRateInterval"`
	NextUpdate          string `json:"nextUpdate"`
	MinFundingRate      string `json:"minFundingRate"`
	MaxFundingRate      string `json:"maxFundingRate"`
	RequestTime         int64  `json:"-"`
}

type Candle [7]NumberString

func (c *Candle) UnmarshalJSON(data []byte) error {
	if strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		var row struct {
			Start    NumberString `json:"start"`
			Open     NumberString `json:"open"`
			High     NumberString `json:"high"`
			Low      NumberString `json:"low"`
			Close    NumberString `json:"close"`
			Volume   NumberString `json:"volume"`
			Turnover NumberString `json:"turnover"`
		}
		if err := json.Unmarshal(data, &row); err != nil {
			return err
		}
		*c = Candle{row.Start, row.Open, row.High, row.Low, row.Close, row.Volume, row.Turnover}
		return nil
	}
	var raw []NumberString
	if err := jsonArrayUnmarshal(data, &raw); err != nil {
		return err
	}
	for i := 0; i < len(c) && i < len(raw); i++ {
		c[i] = raw[i]
	}
	return nil
}

func jsonArrayUnmarshal(data []byte, out any) error {
	return json.Unmarshal(data, out)
}
