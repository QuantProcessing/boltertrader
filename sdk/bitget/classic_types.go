package sdk

import "encoding/json"

type classicResponseEnvelope[T any] struct {
	Code        string `json:"code"`
	Msg         string `json:"msg"`
	Message     string `json:"message"`
	RequestTime int64  `json:"requestTime"`
	Data        T      `json:"data"`
}

func (e classicResponseEnvelope[T]) errorMessage() string {
	if e.Msg != "" {
		return e.Msg
	}
	return e.Message
}

type FlexibleFeeDetails []FeeDetail

func (f *FlexibleFeeDetails) UnmarshalJSON(data []byte) error {
	if string(data) == `""` || string(data) == "null" {
		*f = nil
		return nil
	}
	var details []FeeDetail
	if err := json.Unmarshal(data, &details); err == nil {
		*f = FlexibleFeeDetails(details)
		return nil
	}
	var ignored string
	if err := json.Unmarshal(data, &ignored); err == nil {
		*f = nil
		return nil
	}
	return json.Unmarshal(data, &details)
}

type ClassicSpotAsset struct {
	Coin           string `json:"coin"`
	Available      string `json:"available"`
	Frozen         string `json:"frozen"`
	Locked         string `json:"locked"`
	LimitAvailable string `json:"limitAvailable"`
	UTime          string `json:"uTime"`
}

type ClassicSpotOrderRecord struct {
	UserID           string             `json:"userId"`
	Symbol           string             `json:"symbol"`
	InstID           string             `json:"instId"`
	OrderID          string             `json:"orderId"`
	ClientOID        string             `json:"clientOid"`
	Price            string             `json:"price"`
	Size             string             `json:"size"`
	NewSize          string             `json:"newSize"`
	OrderType        string             `json:"orderType"`
	Force            string             `json:"force"`
	Side             string             `json:"side"`
	Status           string             `json:"status"`
	FillPrice        string             `json:"fillPrice"`
	FillFee          string             `json:"fillFee"`
	FillFeeCoin      string             `json:"fillFeeCoin"`
	PriceAvg         string             `json:"priceAvg"`
	BasePrice        string             `json:"basePrice"`
	BaseVolume       string             `json:"baseVolume"`
	AccBaseVolume    string             `json:"accBaseVolume"`
	QuoteVolume      string             `json:"quoteVolume"`
	EnterPointSource string             `json:"enterPointSource"`
	FeeDetail        FlexibleFeeDetails `json:"feeDetail"`
	OrderSource      string             `json:"orderSource"`
	CancelReason     string             `json:"cancelReason"`
	CTime            string             `json:"cTime"`
	UTime            string             `json:"uTime"`
}

type ClassicMixOrderRecord struct {
	Symbol                        string      `json:"symbol"`
	InstID                        string      `json:"instId"`
	Size                          string      `json:"size"`
	OrderID                       string      `json:"orderId"`
	ClientOID                     string      `json:"clientOid"`
	BaseVolume                    string      `json:"baseVolume"`
	AccBaseVolume                 string      `json:"accBaseVolume"`
	Fee                           string      `json:"fee"`
	Price                         string      `json:"price"`
	PriceAvg                      string      `json:"priceAvg"`
	Status                        string      `json:"status"`
	Side                          string      `json:"side"`
	Force                         string      `json:"force"`
	TotalProfits                  string      `json:"totalProfits"`
	PosSide                       string      `json:"posSide"`
	MarginCoin                    string      `json:"marginCoin"`
	QuoteVolume                   string      `json:"quoteVolume"`
	Leverage                      string      `json:"leverage"`
	MarginMode                    string      `json:"marginMode"`
	ReduceOnly                    string      `json:"reduceOnly"`
	EnterPointSource              string      `json:"enterPointSource"`
	TradeSide                     string      `json:"tradeSide"`
	PosMode                       string      `json:"posMode"`
	OrderType                     string      `json:"orderType"`
	OrderSource                   string      `json:"orderSource"`
	CTime                         string      `json:"cTime"`
	UTime                         string      `json:"uTime"`
	PresetStopSurplusPrice        string      `json:"presetStopSurplusPrice"`
	PresetStopSurplusType         string      `json:"presetStopSurplusType"`
	PresetStopSurplusExecutePrice string      `json:"presetStopSurplusExecutePrice"`
	PresetStopLossPrice           string      `json:"presetStopLossPrice"`
	PresetStopLossType            string      `json:"presetStopLossType"`
	PresetStopLossExecutePrice    string      `json:"presetStopLossExecutePrice"`
	FeeDetail                     []FeeDetail `json:"feeDetail"`
}

type ClassicMixOrderList struct {
	EntrustedList []ClassicMixOrderRecord `json:"entrustedList"`
	EndID         string                  `json:"endId"`
}

type ClassicMixAccount struct {
	MarginCoin            string       `json:"marginCoin"`
	Locked                string       `json:"locked"`
	Available             string       `json:"available"`
	CrossedMaxAvailable   string       `json:"crossedMaxAvailable"`
	IsolatedMaxAvailable  string       `json:"isolatedMaxAvailable"`
	MaxTransferOut        string       `json:"maxTransferOut"`
	AccountEquity         string       `json:"accountEquity"`
	UsdtEquity            string       `json:"usdtEquity"`
	BtcEquity             string       `json:"btcEquity"`
	CrossedRiskRate       string       `json:"crossedRiskRate"`
	CrossedMarginLeverage NumberString `json:"crossedMarginLeverage"`
	IsolatedLongLever     NumberString `json:"isolatedLongLever"`
	IsolatedShortLever    NumberString `json:"isolatedShortLever"`
	MarginMode            string       `json:"marginMode"`
	PosMode               string       `json:"posMode"`
	UnrealizedPL          string       `json:"unrealizedPL"`
	Coupon                string       `json:"coupon"`
	CrossedUnrealizedPL   string       `json:"crossedUnrealizedPL"`
	IsolatedUnrealizedPL  string       `json:"isolatedUnrealizedPL"`
	Grant                 string       `json:"grant"`
	AssetMode             string       `json:"assetMode"`
}

type ClassicMixPositionRecord struct {
	PosID            string `json:"posId"`
	Symbol           string `json:"symbol"`
	InstID           string `json:"instId"`
	MarginCoin       string `json:"marginCoin"`
	MarginSize       string `json:"marginSize"`
	MarginMode       string `json:"marginMode"`
	HoldSide         string `json:"holdSide"`
	PosMode          string `json:"posMode"`
	Total            string `json:"total"`
	Available        string `json:"available"`
	Frozen           string `json:"frozen"`
	OpenPriceAvg     string `json:"openPriceAvg"`
	Leverage         string `json:"leverage"`
	AchievedProfits  string `json:"achievedProfits"`
	UnrealizedPL     string `json:"unrealizedPL"`
	LiquidationPrice string `json:"liquidationPrice"`
	KeepMarginRate   string `json:"keepMarginRate"`
	MarkPrice        string `json:"markPrice"`
	MarginRate       string `json:"marginRate"`
	BreakEvenPrice   string `json:"breakEvenPrice"`
	TotalFee         string `json:"totalFee"`
	DeductedFee      string `json:"deductedFee"`
	AssetMode        string `json:"assetMode"`
	AutoMargin       string `json:"autoMargin"`
	CTime            string `json:"cTime"`
	UTime            string `json:"uTime"`
}

type ClassicFillFeeDetail struct {
	FeeCoin           string `json:"feeCoin"`
	Deduction         string `json:"deduction"`
	TotalDeductionFee string `json:"totalDeductionFee"`
	TotalFee          string `json:"totalFee"`
}

type ClassicMixFillRecord struct {
	OrderID     string                 `json:"orderId"`
	ClientOID   string                 `json:"clientOid"`
	TradeID     string                 `json:"tradeId"`
	Symbol      string                 `json:"symbol"`
	InstID      string                 `json:"instId"`
	Side        string                 `json:"side"`
	OrderType   string                 `json:"orderType"`
	PosMode     string                 `json:"posMode"`
	Price       string                 `json:"price"`
	BaseVolume  string                 `json:"baseVolume"`
	QuoteVolume string                 `json:"quoteVolume"`
	Profit      string                 `json:"profit"`
	TradeSide   string                 `json:"tradeSide"`
	TradeScope  string                 `json:"tradeScope"`
	FeeDetail   []ClassicFillFeeDetail `json:"feeDetail"`
	CTime       string                 `json:"cTime"`
	UTime       string                 `json:"uTime"`
}

type ClassicSpotFillRecord struct {
	OrderID    string                 `json:"orderId"`
	ClientOID  string                 `json:"clientOid"`
	TradeID    string                 `json:"tradeId"`
	Symbol     string                 `json:"symbol"`
	InstID     string                 `json:"instId"`
	OrderType  string                 `json:"orderType"`
	Side       string                 `json:"side"`
	PriceAvg   string                 `json:"priceAvg"`
	Size       string                 `json:"size"`
	Amount     string                 `json:"amount"`
	TradeScope string                 `json:"tradeScope"`
	FeeDetail  []ClassicFillFeeDetail `json:"feeDetail"`
	CTime      string                 `json:"cTime"`
	UTime      string                 `json:"uTime"`
}

type ClassicWSOrderMessage struct {
	Arg    WSArg                   `json:"arg"`
	Action string                  `json:"action"`
	Data   []ClassicMixOrderRecord `json:"data"`
}

type ClassicWSSpotOrderMessage struct {
	Arg    WSArg                    `json:"arg"`
	Action string                   `json:"action"`
	Data   []ClassicSpotOrderRecord `json:"data"`
}

type ClassicWSPositionMessage struct {
	Arg    WSArg                      `json:"arg"`
	Action string                     `json:"action"`
	Data   []ClassicMixPositionRecord `json:"data"`
}

type ClassicWSFillMessage struct {
	Arg    WSArg                  `json:"arg"`
	Action string                 `json:"action"`
	Data   []ClassicMixFillRecord `json:"data"`
}

type ClassicWSSpotFillMessage struct {
	Arg    WSArg                   `json:"arg"`
	Action string                  `json:"action"`
	Data   []ClassicSpotFillRecord `json:"data"`
}

func DecodeClassicWSOrderMessage(payload []byte) (*ClassicWSOrderMessage, error) {
	var msg ClassicWSOrderMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeClassicWSSpotOrderMessage(payload []byte) (*ClassicWSSpotOrderMessage, error) {
	var msg ClassicWSSpotOrderMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeClassicWSPositionMessage(payload []byte) (*ClassicWSPositionMessage, error) {
	var msg ClassicWSPositionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeClassicWSFillMessage(payload []byte) (*ClassicWSFillMessage, error) {
	var msg ClassicWSFillMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeClassicWSSpotFillMessage(payload []byte) (*ClassicWSSpotFillMessage, error) {
	var msg ClassicWSSpotFillMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}
