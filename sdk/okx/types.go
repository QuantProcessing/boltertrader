package okx

// BaseResponse is the standard response wrapper for OKX API.
// generic type T allows flexible data parsing.
type BaseResponse[T any] struct {
	Code    string `json:"code"`
	Message string `json:"msg"`
	Data    []T    `json:"data"`
}

// Method represents HTTP methods
type Method string

const (
	MethodGet  Method = "GET"
	MethodPost Method = "POST"
)

// Environment type
type Environment string

const (
	Production Environment = "0"
	Simulated  Environment = "1"
)

// --- Account ---

type BalanceDetail struct {
	AutoLendStatus        string `json:"autoLendStatus"`
	AutoLendMtAmt         string `json:"autoLendMtAmt"`
	AvailBal              string `json:"availBal"`
	AvailEq               string `json:"availEq"`
	BorrowFroz            string `json:"borrowFroz"`
	CashBal               string `json:"cashBal"`
	Ccy                   string `json:"ccy"`
	CrossLiab             string `json:"crossLiab"`
	ColRes                string `json:"colRes"`
	CollateralEnabled     bool   `json:"collateralEnabled"`
	CollateralRestrict    bool   `json:"collateralRestrict"`
	ColBorrAutoConversion string `json:"colBorrAutoConversion"`
	DisEq                 string `json:"disEq"`
	Eq                    string `json:"eq"`
	EqUsd                 string `json:"eqUsd"`
	SmtSyncEq             string `json:"smtSyncEq"`
	SpotCopyTradingEq     string `json:"spotCopyTradingEq"`
	FixedBal              string `json:"fixedBal"`
	FrozenBal             string `json:"frozenBal"`
	FrpType               string `json:"frpType"`
	Imr                   string `json:"imr"`
	Interest              string `json:"interest"`
	IsoEq                 string `json:"isoEq"`
	IsoLiab               string `json:"isoLiab"`
	IsoUpl                string `json:"isoUpl"`
	Liab                  string `json:"liab"`
	MaxLoan               string `json:"maxLoan"`
	MgnRatio              string `json:"mgnRatio"`
	Mmr                   string `json:"mmr"`
	NotionalLever         string `json:"notionalLever"`
	OrdFrozen             string `json:"ordFrozen"`
	RewardBal             string `json:"rewardBal"`
	SpotInUseAmt          string `json:"spotInUseAmt"`
	ClSpotInUseAmt        string `json:"clSpotInUseAmt"`
	MaxSpotInUse          string `json:"maxSpotInUse"`
	SpotIsoBal            string `json:"spotIsoBal"`
	StgyEq                string `json:"stgyEq"`
	Twap                  string `json:"twap"`
	UTime                 string `json:"uTime"`
	Upl                   string `json:"upl"`
	UplLiab               string `json:"uplLiab"`
	SpotBal               string `json:"spotBal"`
	OpenAvgPx             string `json:"openAvgPx"`
	AccAvgPx              string `json:"accAvgPx"`
	SpotUpl               string `json:"spotUpl"`
	SpotUplRatio          string `json:"spotUplRatio"`
	TotalPnl              string `json:"totalPnl"`
	TotalPnlRatio         string `json:"totalPnlRatio"`
}

type Balance struct {
	AdjEq                 string          `json:"adjEq"`
	AvailEq               string          `json:"availEq"`
	BorrowFroz            string          `json:"borrowFroz"`
	Delta                 string          `json:"delta"`
	DeltaLever            string          `json:"deltaLever"`
	DeltaNeutralStatus    string          `json:"deltaNeutralStatus"`
	Details               []BalanceDetail `json:"details"`
	Imr                   string          `json:"imr"`
	IsoEq                 string          `json:"isoEq"`
	MgnRatio              string          `json:"mgnRatio"`
	Mmr                   string          `json:"mmr"`
	NotionalUsd           string          `json:"notionalUsd"`
	NotionalUsdForBorrow  string          `json:"notionalUsdForBorrow"`
	NotionalUsdForFutures string          `json:"notionalUsdForFutures"`
	NotionalUsdForOption  string          `json:"notionalUsdForOption"`
	NotionalUsdForSwap    string          `json:"notionalUsdForSwap"`
	OrdFroz               string          `json:"ordFroz"`
	TotalEq               string          `json:"totalEq"`
	UTime                 string          `json:"uTime"`
	Upl                   string          `json:"upl"`
}

type Position struct {
	Adl                    string        `json:"adl"`
	AvailPos               string        `json:"availPos"`
	AvgPx                  string        `json:"avgPx"`
	BaseBal                string        `json:"baseBal"`
	BaseBorrowed           string        `json:"baseBorrowed"`
	BaseInterest           string        `json:"baseInterest"`
	BePx                   string        `json:"bePx"`
	BizRefId               string        `json:"bizRefId"`
	BizRefType             string        `json:"bizRefType"`
	CTime                  string        `json:"cTime"`
	Ccy                    string        `json:"ccy"`
	ClSpotInUseAmt         string        `json:"clSpotInUseAmt"`
	CloseOrderAlgo         []interface{} `json:"closeOrderAlgo"`
	DeltaBS                string        `json:"deltaBS"`
	DeltaPA                string        `json:"deltaPA"`
	Fee                    string        `json:"fee"`
	FundingFee             string        `json:"fundingFee"`
	GammaBS                string        `json:"gammaBS"`
	GammaPA                string        `json:"gammaPA"`
	HedgedPos              string        `json:"hedgedPos"`
	IdxPx                  string        `json:"idxPx"`
	Imr                    string        `json:"imr"`
	InstId                 string        `json:"instId"`
	InstType               string        `json:"instType"`
	Interest               string        `json:"interest"`
	Last                   string        `json:"last"`
	Lever                  string        `json:"lever"`
	Liab                   string        `json:"liab"`
	LiabCcy                string        `json:"liabCcy"`
	LiqPenalty             string        `json:"liqPenalty"`
	LiqPx                  string        `json:"liqPx"`
	Margin                 string        `json:"margin"`
	MarkPx                 string        `json:"markPx"`
	MaxSpotInUseAmt        string        `json:"maxSpotInUseAmt"`
	MgnMode                MgnMode       `json:"mgnMode"`
	MgnRatio               string        `json:"mgnRatio"`
	Mmr                    string        `json:"mmr"`
	NotionalUsd            string        `json:"notionalUsd"`
	OptVal                 string        `json:"optVal"`
	PendingCloseOrdLiabVal string        `json:"pendingCloseOrdLiabVal"`
	Pnl                    string        `json:"pnl"`
	Pos                    string        `json:"pos"`
	PosCcy                 string        `json:"posCcy"`
	PosId                  string        `json:"posId"`
	PosSide                PosSide       `json:"posSide"`
	QuoteBal               string        `json:"quoteBal"`
	QuoteBorrowed          string        `json:"quoteBorrowed"`
	QuoteInterest          string        `json:"quoteInterest"`
	RealizedPnl            string        `json:"realizedPnl"`
	SpotInUseAmt           string        `json:"spotInUseAmt"`
	SpotInUseCcy           string        `json:"spotInUseCcy"`
	ThetaBS                string        `json:"thetaBS"`
	ThetaPA                string        `json:"thetaPA"`
	TradeId                string        `json:"tradeId"`
	UTime                  string        `json:"uTime"`
	Upl                    string        `json:"upl"`
	UplLastPx              string        `json:"uplLastPx"`
	UplRatio               string        `json:"uplRatio"`
	UplRatioLastPx         string        `json:"uplRatioLastPx"`
	UsdPx                  string        `json:"usdPx"`
	VegaBS                 string        `json:"vegaBS"`
	VegaPA                 string        `json:"vegaPA"`
	NonSettleAvgPx         string        `json:"nonSettleAvgPx"`
	SettledPnl             string        `json:"settledPnl"`
}

type AccountConfig struct {
	AcctLv              string   `json:"acctLv"`
	AcctStpMode         string   `json:"acctStpMode"`
	AutoLoan            bool     `json:"autoLoan"`
	CtIsoMode           string   `json:"ctIsoMode"`
	EnableSpotBorrow    bool     `json:"enableSpotBorrow"`
	GreeksType          string   `json:"greeksType"`
	FeeType             string   `json:"feeType"`
	Ip                  string   `json:"ip"`
	Type                string   `json:"type"`
	KycLv               string   `json:"kycLv"`
	Label               string   `json:"label"`
	Level               string   `json:"level"`
	LevelTmp            string   `json:"levelTmp"`
	LiquidationGear     string   `json:"liquidationGear"`
	MainUid             string   `json:"mainUid"`
	MgnIsoMode          string   `json:"mgnIsoMode"`
	OpAuth              string   `json:"opAuth"`
	Perm                string   `json:"perm"`
	PosMode             string   `json:"posMode"`
	RoleType            string   `json:"roleType"`
	SpotBorrowAutoRepay bool     `json:"spotBorrowAutoRepay"`
	SpotOffsetType      string   `json:"spotOffsetType"`
	SpotRoleType        string   `json:"spotRoleType"`
	SpotTraderInsts     []string `json:"spotTraderInsts"`
	StgyType            string   `json:"stgyType"`
	TraderInsts         []string `json:"traderInsts"`
	Uid                 string   `json:"uid"`
	SettleCcy           string   `json:"settleCcy"`
	SettleCcyList       []string `json:"settleCcyList"`
}

type AccountLevel string

const (
	AccountLevelUnknown              AccountLevel = ""
	AccountLevelSimple               AccountLevel = "simple"
	AccountLevelSingleCurrencyMargin AccountLevel = "single_currency_margin"
	AccountLevelMultiCurrencyMargin  AccountLevel = "multi_currency_margin"
	AccountLevelPortfolioMargin      AccountLevel = "portfolio_margin"
)

func (c AccountConfig) AccountLevel() AccountLevel {
	switch c.AcctLv {
	case "1":
		return AccountLevelSimple
	case "2":
		return AccountLevelSingleCurrencyMargin
	case "3":
		return AccountLevelMultiCurrencyMargin
	case "4":
		return AccountLevelPortfolioMargin
	default:
		return AccountLevelUnknown
	}
}

type PositionMode struct {
	PosMode string `json:"posMode"`
}

type SetLeverage struct {
	Lever   int    `json:"lever"`
	MgnMode string `json:"mgnMode"`
	InstId  string `json:"instId"`
	PosSide string `json:"posSide"`
}

type TradeFee struct {
	Category  string     `json:"category"`
	InstType  string     `json:"instType"`
	FeeGroups []FeeGroup `json:"feeGroup"`
	Ts        string     `json:"ts"`
}
type FeeGroup struct {
	GroupId string `json:"groupId"`
	Maker   string `json:"maker"`
	Taker   string `json:"taker"`
}

// --- Market ---

type Ticker struct {
	InstType  string `json:"instType"`
	InstId    string `json:"instId"`
	Last      string `json:"last"`
	LastSz    string `json:"lastSz"`
	AskPx     string `json:"askPx"`
	AskSz     string `json:"askSz"`
	BidPx     string `json:"bidPx"`
	BidSz     string `json:"bidSz"`
	Open24h   string `json:"open24h"`
	High24h   string `json:"high24h"`
	Low24h    string `json:"low24h"`
	VolCcy24h string `json:"volCcy24h"` // base volume, 以币为单位
	Vol24h    string `json:"vol24h"`    // not quote, 张数
	Ts        string `json:"ts"`
	SodUtc0   string `json:"sodUtc0"`
	SodUtc8   string `json:"sodUtc8"`
}

type OrderBook struct {
	Asks [][]string `json:"asks"` // [price, size, trash, numOrders]
	Bids [][]string `json:"bids"`
	Ts   string     `json:"ts"`
}

type Instrument struct {
	InstId            string   `json:"instId"`
	GroupId           string   `json:"groupId"`
	Uly               string   `json:"uly"`
	InstFamily        string   `json:"instFamily"`
	BaseCcy           string   `json:"baseCcy"`
	QuoteCcy          string   `json:"quoteCcy"`
	TradeQuoteCcyList []string `json:"tradeQuoteCcyList"`
	SettCcy           string   `json:"settCcy"`
	SettleCcy         string   `json:"settleCcy"`
	CtVal             string   `json:"ctVal"`
	CtMult            string   `json:"ctMult"`
	CtValCcy          string   `json:"ctValCcy"`
	OptType           string   `json:"optType"`
	Stk               string   `json:"stk"`
	ListTime          string   `json:"listTime"`
	ExpTime           string   `json:"expTime"`
	Leverage          string   `json:"leverage"`
	TickSz            string   `json:"tickSz"`
	LotSz             string   `json:"lotSz"`
	MinSz             string   `json:"minSz"`
	InstType          string   `json:"instType"`
	RuleType          string   `json:"ruleType"`
	State             string   `json:"state"`
	InstIdCode        *int64   `json:"instIdCode"`
	InstCategory      string   `json:"instCategory"`
}

type SpreadInstrument struct {
	SprdId   string      `json:"sprdId"`
	BaseCcy  string      `json:"baseCcy"`
	QuoteCcy string      `json:"quoteCcy"`
	TickSz   string      `json:"tickSz"`
	LotSz    string      `json:"lotSz"`
	MinSz    string      `json:"minSz"`
	State    string      `json:"state"`
	Legs     []SpreadLeg `json:"legs"`
}

type SpreadLeg struct {
	InstId   string `json:"instId"`
	InstType string `json:"instType"`
	Side     string `json:"side"`
	Sz       string `json:"sz"`
}

// [0] ts
// [1] open
// [2] high
// [3] low
// [4] close
// [5] volume size
// [6] volume ccy
// [7] volume ccy quote
// [8] confirm: 0 (not finish) or 1 (finish)
type Candle [9]string

type PublicTrade struct {
	InstId  string `json:"instId"`
	TradeId string `json:"tradeId"`
	Px      string `json:"px"`
	Sz      string `json:"sz"`
	Side    string `json:"side"`
	Ts      string `json:"ts"`
}

type FundingRate struct {
	InstrumentType  string `json:"instType"`
	InstrumentID    string `json:"instId"`
	FundingRate     string `json:"fundingRate"`
	NextFundingRate string `json:"nextFundingRate"`
	FundingTime     string `json:"fundingTime"`
	NextFundingTime string `json:"nextFundingTime"`
	Premium         string `json:"premium"`
	SettFundingRate string `json:"settFundingRate"`
	SettState       string `json:"settState"`
	Ts              string `json:"ts"`
}

type MarkPrice struct {
	InstType string `json:"instType"`
	InstId   string `json:"instId"`
	MarkPx   string `json:"markPx"`
	Ts       string `json:"ts"`
}

type IndexTicker struct {
	InstId  string `json:"instId"`
	IdxPx   string `json:"idxPx"`
	High24  string `json:"high24h"`
	Low24   string `json:"low24h"`
	Open24  string `json:"open24h"`
	SodUtc0 string `json:"sodUtc0"`
	SodUtc8 string `json:"sodUtc8"`
	Ts      string `json:"ts"`
}

type OptionSummary struct {
	InstType   string `json:"instType"`
	InstId     string `json:"instId"`
	Uly        string `json:"uly"`
	InstFamily string `json:"instFamily"`
	DeltaBS    string `json:"deltaBS"`
	DeltaPA    string `json:"deltaPA"`
	GammaBS    string `json:"gammaBS"`
	GammaPA    string `json:"gammaPA"`
	VegaBS     string `json:"vegaBS"`
	VegaPA     string `json:"vegaPA"`
	ThetaBS    string `json:"thetaBS"`
	ThetaPA    string `json:"thetaPA"`
	MarkVol    string `json:"markVol"`
	BidVol     string `json:"bidVol"`
	AskVol     string `json:"askVol"`
	FwdPx      string `json:"fwdPx"`
	OI         string `json:"oi"`
	Ts         string `json:"ts"`
}

type EstimatedPrice struct {
	InstType string `json:"instType"`
	InstId   string `json:"instId"`
	SettlePx string `json:"settlePx"`
	Ts       string `json:"ts"`
}

// --- Order ---

type OrderRequest struct {
	InstId         string            `json:"instId"`
	InstIdCode     *int64            `json:"instIdCode,omitempty"`
	TdMode         string            `json:"tdMode"` // cross, isolated, cash (spot)
	ClOrdId        *string           `json:"clOrdId,omitempty"`
	Side           string            `json:"side"`              // buy, sell
	PosSide        *string           `json:"posSide,omitempty"` // long, short, net (required for long/short mode)
	OrdType        string            `json:"ordType"`           // market, limit, etc.
	Sz             string            `json:"sz"`
	Px             *string           `json:"px,omitempty"`
	Ccy            *string           `json:"ccy,omitempty"`
	TgtCcy         *string           `json:"tgtCcy,omitempty"`
	Tag            *string           `json:"tag,omitempty"`
	ReduceOnly     *bool             `json:"reduceOnly,omitempty"`
	AttachAlgoOrds []AttachAlgoOrder `json:"attachAlgoOrds,omitempty"`
}

type SpreadOrderRequest struct {
	SprdId  string  `json:"sprdId"`
	ClOrdId *string `json:"clOrdId,omitempty"`
	Side    string  `json:"side"`
	OrdType string  `json:"ordType"`
	Sz      string  `json:"sz"`
	Px      *string `json:"px,omitempty"`
	Tag     *string `json:"tag,omitempty"`
}

type AttachAlgoOrder struct {
	AttachAlgoClOrdId string `json:"attachAlgoClOrdId,omitempty"`
	TpTriggerPx       string `json:"tpTriggerPx,omitempty"`
	TpOrdPx           string `json:"tpOrdPx,omitempty"`
	TpTriggerPxType   string `json:"tpTriggerPxType,omitempty"`
	SlTriggerPx       string `json:"slTriggerPx,omitempty"`
	SlOrdPx           string `json:"slOrdPx,omitempty"`
	SlTriggerPxType   string `json:"slTriggerPxType,omitempty"`
}

type AlgoOrderRequest struct {
	InstId          string            `json:"instId"`
	TdMode          string            `json:"tdMode"`
	Side            string            `json:"side"`
	OrdType         string            `json:"ordType"`
	Sz              string            `json:"sz"`
	AlgoClOrdId     *string           `json:"algoClOrdId,omitempty"`
	PosSide         *string           `json:"posSide,omitempty"`
	ReduceOnly      *bool             `json:"reduceOnly,omitempty"`
	TriggerPx       *string           `json:"triggerPx,omitempty"`
	OrderPx         *string           `json:"orderPx,omitempty"`
	TriggerPxType   *string           `json:"triggerPxType,omitempty"`
	TpTriggerPx     *string           `json:"tpTriggerPx,omitempty"`
	TpOrdPx         *string           `json:"tpOrdPx,omitempty"`
	TpTriggerPxType *string           `json:"tpTriggerPxType,omitempty"`
	SlTriggerPx     *string           `json:"slTriggerPx,omitempty"`
	SlOrdPx         *string           `json:"slOrdPx,omitempty"`
	SlTriggerPxType *string           `json:"slTriggerPxType,omitempty"`
	CallbackRatio   *string           `json:"callbackRatio,omitempty"`
	CallbackSpread  *string           `json:"callbackSpread,omitempty"`
	ActivePx        *string           `json:"activePx,omitempty"`
	AttachAlgoOrds  []AttachAlgoOrder `json:"attachAlgoOrds,omitempty"`
	Tag             *string           `json:"tag,omitempty"`
	CloseFraction   *string           `json:"closeFraction,omitempty"`
}

type AlgoOrderID struct {
	AlgoId      string `json:"algoId"`
	AlgoClOrdId string `json:"algoClOrdId"`
	Tag         string `json:"tag,omitempty"`
	SCode       string `json:"sCode"`
	SMsg        string `json:"sMsg"`
	ReqId       string `json:"reqId,omitempty"`
	Ts          string `json:"ts,omitempty"`
}

type AlgoOrder struct {
	ActualPx          string            `json:"actualPx"`
	ActualSide        string            `json:"actualSide"`
	ActualSz          string            `json:"actualSz"`
	AlgoClOrdId       string            `json:"algoClOrdId"`
	AlgoId            string            `json:"algoId"`
	AttachAlgoClOrdId string            `json:"attachAlgoClOrdId"`
	AttachAlgoOrds    []AttachAlgoOrder `json:"attachAlgoOrds"`
	AvgPx             string            `json:"avgPx"`
	CTime             string            `json:"cTime"`
	CallbackRatio     string            `json:"callbackRatio"`
	CallbackSpread    string            `json:"callbackSpread"`
	CancelType        string            `json:"cancelType"`
	Category          string            `json:"category"`
	Ccy               string            `json:"ccy"`
	ClOrdId           string            `json:"clOrdId"`
	FailCode          string            `json:"failCode"`
	InstId            string            `json:"instId"`
	InstType          string            `json:"instType"`
	IsTradeBorrowMode string            `json:"isTradeBorrowMode"`
	Lever             string            `json:"lever"`
	OrdId             string            `json:"ordId"`
	OrdIdList         []string          `json:"ordIdList"`
	OrdPx             string            `json:"ordPx"`
	OrdType           string            `json:"ordType"`
	OrderPx           string            `json:"orderPx"`
	PosSide           PosSide           `json:"posSide"`
	PxLimit           string            `json:"pxLimit"`
	PxSpread          string            `json:"pxSpread"`
	PxVar             string            `json:"pxVar"`
	ReduceOnly        string            `json:"reduceOnly"`
	Side              Side              `json:"side"`
	SlOrdPx           string            `json:"slOrdPx"`
	SlTriggerPx       string            `json:"slTriggerPx"`
	SlTriggerPxType   string            `json:"slTriggerPxType"`
	State             string            `json:"state"`
	Sz                string            `json:"sz"`
	SzLimit           string            `json:"szLimit"`
	Tag               string            `json:"tag"`
	TdMode            TdMode            `json:"tdMode"`
	TgtCcy            string            `json:"tgtCcy"`
	TimeInterval      string            `json:"timeInterval"`
	TpOrdPx           string            `json:"tpOrdPx"`
	TpTriggerPx       string            `json:"tpTriggerPx"`
	TpTriggerPxType   string            `json:"tpTriggerPxType"`
	TriggerPx         string            `json:"triggerPx"`
	TriggerPxType     string            `json:"triggerPxType"`
	UTime             string            `json:"uTime"`
}

type AlgoCancelRequest struct {
	AlgoId string `json:"algoId"`
	InstId string `json:"instId"`
}

type AmendAlgoOrderRequest struct {
	AlgoId       string  `json:"algoId"`
	InstId       string  `json:"instId"`
	NewSz        *string `json:"newSz,omitempty"`
	NewPx        *string `json:"newPx,omitempty"`
	NewTriggerPx *string `json:"newTriggerPx,omitempty"`
	ReqId        *string `json:"reqId,omitempty"`
}

type ModifyOrderRequest struct {
	InstId     string  `json:"instId"`
	InstIdCode *int64  `json:"instIdCode,omitempty"`
	OrdId      *string `json:"ordId,omitempty"`
	ClOrdId    *string `json:"clOrdId,omitempty"`
	NewSz      *string `json:"newSz,omitempty"`
	NewPx      *string `json:"newPx,omitempty"`
	CxlOnFail  *bool   `json:"cxlOnFail,omitempty"`
	ReqId      *string `json:"reqId,omitempty"`
}

type CancelOrderRequest struct {
	InstId     string  `json:"instId"`
	InstIdCode *int64  `json:"instIdCode,omitempty"`
	OrdId      *string `json:"ordId,omitempty"`
	ClOrdId    *string `json:"clOrdId,omitempty"`
}

type SpreadCancelRequest struct {
	SprdId  string `json:"sprdId"`
	OrdId   string `json:"ordId,omitempty"`
	ClOrdId string `json:"clOrdId,omitempty"`
}

type SpreadMassCancelRequest struct {
	SprdId string `json:"sprdId"`
}

type OrderId struct {
	OrdId   string  `json:"ordId"`
	ClOrdId string  `json:"clOrdId"`
	Tag     *string `json:"tag,omitempty"`
	SCode   string  `json:"sCode"`
	SMsg    string  `json:"sMsg"`
	SubCode string  `json:"subCode,omitempty"`
	Ts      string  `json:"ts"`
}

type ClosePosition struct {
	ClOrdId string `json:"clOrdId"`
	InstId  string `json:"instId"`
	PosSide string `json:"posSide"`
	Tag     string `json:"tag"`
}

type Order struct {
	AccFillSz          string          `json:"accFillSz"`
	AlgoClOrdId        string          `json:"algoClOrdId"`
	AlgoId             string          `json:"algoId"`
	AttachAlgoClOrdId  string          `json:"attachAlgoClOrdId"`
	AttachAlgoOrds     []int           `json:"attachAlgoOrds"`
	AvgPx              string          `json:"avgPx"`
	CTime              string          `json:"cTime"`
	CancelSource       string          `json:"cancelSource"`
	CancelSourceReason string          `json:"cancelSourceReason"`
	Category           string          `json:"category"`
	Ccy                string          `json:"ccy"`
	ClOrdId            string          `json:"clOrdId"`
	ExecType           string          `json:"execType"`
	Fee                string          `json:"fee"`
	FeeCcy             string          `json:"feeCcy"`
	FillPx             string          `json:"fillPx"`
	FillSz             string          `json:"fillSz"`
	FillTime           string          `json:"fillTime"`
	InstId             string          `json:"instId"`
	InstType           string          `json:"instType"`
	IsTpLimit          string          `json:"isTpLimit"`
	Lever              string          `json:"lever"`
	LinkedAlgoOrder    LinkedAlgoOrder `json:"linkedAlgoOrder"`
	OrdId              string          `json:"ordId"`
	OrdType            OrderType       `json:"ordType"`
	Pnl                string          `json:"pnl"`
	PosSide            PosSide         `json:"posSide"`
	Px                 string          `json:"px"`
	PxType             string          `json:"pxType"`
	PxUsd              string          `json:"pxUsd"`
	PxVol              string          `json:"pxVol"`
	QuickMgnType       string          `json:"quickMgnType"`
	Rebate             string          `json:"rebate"`
	RebateCcy          string          `json:"rebateCcy"`
	ReduceOnly         string          `json:"reduceOnly"`
	Side               Side            `json:"side"`
	SlOrdPx            string          `json:"slOrdPx"`
	SlTriggerPx        string          `json:"slTriggerPx"`
	SlTriggerPxType    string          `json:"slTriggerPxType"`
	Source             string          `json:"source"`
	State              OrderStatus     `json:"state"`
	StpId              string          `json:"stpId"`
	StpMode            string          `json:"stpMode"`
	Sz                 string          `json:"sz"`
	Tag                string          `json:"tag"`
	TdMode             TdMode          `json:"tdMode"`
	TgtCcy             string          `json:"tgtCcy"`
	TpOrdPx            string          `json:"tpOrdPx"`
	TpTriggerPx        string          `json:"tpTriggerPx"`
	TpTriggerPxType    string          `json:"tpTriggerPxType"`
	TradeId            string          `json:"tradeId"`
	TradeQuoteCcy      string          `json:"tradeQuoteCcy"`
	UTime              string          `json:"uTime"`
}

type SpreadOrder struct {
	SprdId    string      `json:"sprdId"`
	OrdId     string      `json:"ordId"`
	ClOrdId   string      `json:"clOrdId"`
	State     OrderStatus `json:"state"`
	Side      Side        `json:"side"`
	OrdType   OrderType   `json:"ordType"`
	Sz        string      `json:"sz"`
	AccFillSz string      `json:"accFillSz"`
	FillPx    string      `json:"fillPx"`
	FillSz    string      `json:"fillSz"`
	TradeId   string      `json:"tradeId"`
	Px        string      `json:"px"`
	AvgPx     string      `json:"avgPx"`
	CTime     string      `json:"cTime"`
	UTime     string      `json:"uTime"`
}

type LinkedAlgoOrder struct {
	AlgoId string `json:"algoId"`
}

type Fill struct {
	InstType string  `json:"instType"`
	InstId   string  `json:"instId"`
	TradeId  string  `json:"tradeId"`
	OrdId    string  `json:"ordId"`
	ClOrdId  string  `json:"clOrdId"`
	BillId   string  `json:"billId"`
	Side     Side    `json:"side"`
	PosSide  PosSide `json:"posSide"`
	FillPx   string  `json:"fillPx"`
	FillSz   string  `json:"fillSz"`
	Fee      string  `json:"fee"`
	FeeCcy   string  `json:"feeCcy"`
	ExecType string  `json:"execType"`
	Ts       string  `json:"ts"`
}

type SpreadFill struct {
	SprdId  string `json:"sprdId"`
	TradeId string `json:"tradeId"`
	OrdId   string `json:"ordId"`
	ClOrdId string `json:"clOrdId"`
	Side    Side   `json:"side"`
	FillPx  string `json:"fillPx"`
	FillSz  string `json:"fillSz"`
	Fee     string `json:"fee"`
	FeeCcy  string `json:"feeCcy"`
	Ts      string `json:"ts"`
}

type OrderType string

const (
	OrderTypeLimit           OrderType = "limit"
	OrderTypeMarket          OrderType = "market"
	OrderTypePostOnly        OrderType = "post_only"
	OrderTypeFok             OrderType = "fok"
	OrderTypeIoc             OrderType = "ioc"
	OrderTypeOptimalLimitIoc OrderType = "optimal_limit_ioc"
	OrderTypeOpFok           OrderType = "op_fok"
	OrderTypeMmp             OrderType = "mmp"
	OrderTypeMmpAndPostOnly  OrderType = "mmp_and_post_only"
)

type AlgoOrderType string

const (
	AlgoOrderTypeConditional   AlgoOrderType = "conditional"
	AlgoOrderTypeOCO           AlgoOrderType = "oco"
	AlgoOrderTypeTrigger       AlgoOrderType = "trigger"
	AlgoOrderTypeMoveOrderStop AlgoOrderType = "move_order_stop"
	AlgoOrderTypeIceberg       AlgoOrderType = "iceberg"
	AlgoOrderTypeTWAP          AlgoOrderType = "twap"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type PosSide string

const (
	PosSideLong  PosSide = "long"
	PosSideShort PosSide = "short"
	PosSideNet   PosSide = "net" // 在单向持仓模式下填这个
)

type TdMode string // 交易模式

const (
	TdModeCross    TdMode = "cross"
	TdModeIsolated TdMode = "isolated"
	TdModeCash     TdMode = "cash"
)

type OrderStatus string

const (
	OrderStatusLive            OrderStatus = "live"
	OrderStatusPartiallyFilled OrderStatus = "partially_filled"
	OrderStatusFilled          OrderStatus = "filled"
	OrderStatusCanceled        OrderStatus = "canceled"
	OrderStatusMmpCanceled     OrderStatus = "mmp_canceled" // 做市商保护机制的自动撤单
)

type MgnMode string

const (
	MgnModeCross    MgnMode = "cross"
	MgnModeIsolated MgnMode = "isolated"
	MgnModeCash     MgnMode = "cash"
)
