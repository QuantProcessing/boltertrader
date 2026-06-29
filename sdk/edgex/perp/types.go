package perp

import "encoding/json"

// Common Response
type APIResponse struct {
	Code    string          `json:"code"`
	Message string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
}

// Exchange Info
// Exchange Info (Meta Data)
type ExchangeInfo struct {
	Global       Global     `json:"global"`
	CoinList     []Coin     `json:"coinList"`
	ContractList []Contract `json:"contractList"`
	MultiChain   MultiChain `json:"multiChain"`
}

type Global struct {
	AppName                      string `json:"appName"`
	AppEnv                       string `json:"appEnv"`
	AppOnlySignOn                string `json:"appOnlySignOn"`
	FeeAccountId                 string `json:"feeAccountId"`
	FeeAccountL2Key              string `json:"feeAccountL2Key"`
	PoolAccountId                string `json:"poolAccountId"`
	PoolAccountL2Key             string `json:"poolAccountL2Key"`
	FastWithdrawAccountId        string `json:"fastWithdrawAccountId"`
	FastWithdrawAccountL2Key     string `json:"fastWithdrawAccountL2Key"`
	FastWithdrawMaxAmount        string `json:"fastWithdrawMaxAmount"`
	FastWithdrawRegistryAddress  string `json:"fastWithdrawRegistryAddress"`
	StarkExChainId               string `json:"starkExChainId"`
	StarkExContractAddress       string `json:"starkExContractAddress"`
	StarkExCollateralCoin        Coin   `json:"starkExCollateralCoin"`
	StarkExMaxFundingRate        int    `json:"starkExMaxFundingRate"`
	StarkExOrdersTreeHeight      int    `json:"starkExOrdersTreeHeight"`
	StarkExPositionsTreeHeight   int    `json:"starkExPositionsTreeHeight"`
	StarkExFundingValidityPeriod int    `json:"starkExFundingValidityPeriod"`
	StarkExPriceValidityPeriod   int    `json:"starkExPriceValidityPeriod"`
	MaintenanceReason            string `json:"maintenanceReason"`
}

type Coin struct {
	CoinId            string `json:"coinId"`
	CoinName          string `json:"coinName"`
	StepSize          string `json:"stepSize"`
	ShowStepSize      string `json:"showStepSize"`
	IconUrl           string `json:"iconUrl"`
	StarkExAssetId    string `json:"starkExAssetId"`
	StarkExResolution string `json:"starkExResolution"`
}

type Contract struct {
	ContractId                      string     `json:"contractId"`
	ContractName                    string     `json:"contractName"`
	BaseCoinId                      string     `json:"baseCoinId"`
	QuoteCoinId                     string     `json:"quoteCoinId"`
	TickSize                        string     `json:"tickSize"`
	StepSize                        string     `json:"stepSize"`
	MinOrderSize                    string     `json:"minOrderSize"`
	MaxOrderSize                    string     `json:"maxOrderSize"`
	MaxOrderBuyPriceRatio           string     `json:"maxOrderBuyPriceRatio"`
	MinOrderSellPriceRatio          string     `json:"minOrderSellPriceRatio"`
	MaxPositionSize                 string     `json:"maxPositionSize"`
	RiskTierList                    []RiskTier `json:"riskTierList"`
	DefaultTakerFeeRate             string     `json:"defaultTakerFeeRate"`
	DefaultMakerFeeRate             string     `json:"defaultMakerFeeRate"`
	DefaultLeverage                 string     `json:"defaultLeverage"`
	LiquidateFeeRate                string     `json:"liquidateFeeRate"`
	EnableTrade                     bool       `json:"enableTrade"`
	EnableDisplay                   bool       `json:"enableDisplay"`
	EnableOpenPosition              bool       `json:"enableOpenPosition"`
	FundingInterestRate             string     `json:"fundingInterestRate"`
	FundingImpactMarginNotional     string     `json:"fundingImpactMarginNotional"`
	FundingMaxRate                  string     `json:"fundingMaxRate"`
	FundingMinRate                  string     `json:"fundingMinRate"`
	FundingRateIntervalMin          string     `json:"fundingRateIntervalMin"`
	DisplayDigitMerge               string     `json:"displayDigitMerge"`
	DisplayMaxLeverage              string     `json:"displayMaxLeverage"`
	DisplayMinLeverage              string     `json:"displayMinLeverage"`
	DisplayNewIcon                  bool       `json:"displayNewIcon"`
	DisplayHotIcon                  bool       `json:"displayHotIcon"`
	MatchServerName                 string     `json:"matchServerName"`
	StarkExSyntheticAssetId         string     `json:"starkExSyntheticAssetId"`
	StarkExResolution               string     `json:"starkExResolution"`
	StarkExOraclePriceQuorum        string     `json:"starkExOraclePriceQuorum"`
	StarkExOraclePriceSignedAssetId []string   `json:"starkExOraclePriceSignedAssetId"`
	StarkExOraclePriceSigner        []string   `json:"starkExOraclePriceSigner"`
}

type RiskTier struct {
	Tier                    int    `json:"tier"`
	PositionValueUpperBound string `json:"positionValueUpperBound"`
	MaxLeverage             string `json:"maxLeverage"`
	MaintenanceMarginRate   string `json:"maintenanceMarginRate"`
	StarkExRisk             string `json:"starkExRisk"`
	StarkExUpperBound       string `json:"starkExUpperBound"`
}

type MultiChain struct {
	CoinId      string  `json:"coinId"`
	MaxWithdraw string  `json:"maxWithdraw"`
	MinWithdraw string  `json:"minWithdraw"`
	MinDeposit  string  `json:"minDeposit"`
	ChainList   []Chain `json:"chainList"`
}

type Chain struct {
	Chain              string  `json:"chain"`
	ChainId            string  `json:"chainId"`
	ChainIconUrl       string  `json:"chainIconUrl"`
	ContractAddress    string  `json:"contractAddress"`
	DepositGasFeeLess  bool    `json:"depositGasFeeLess"`
	FeeLess            bool    `json:"feeLess"`
	FeeRate            string  `json:"feeRate"`
	GasLess            bool    `json:"gasLess"`
	GasToken           string  `json:"gasToken"`
	MinFee             string  `json:"minFee"`
	RpcUrl             string  `json:"rpcUrl"`
	WebTxUrl           string  `json:"webTxUrl"`
	WithdrawGasFeeLess bool    `json:"withdrawGasFeeLess"`
	TokenList          []Token `json:"tokenList"`
	TxConfirm          string  `json:"txConfirm"`
	BlockTime          string  `json:"blockTime"`
	AllowAaDeposit     bool    `json:"allowAaDeposit"`
	AllowAaWithdraw    bool    `json:"allowAaWithdraw"`
	AppRpcUrl          string  `json:"appRpcUrl"`
}

type Token struct {
	TokenAddress   string `json:"tokenAddress"`
	Decimals       string `json:"decimals"`
	IconUrl        string `json:"iconUrl"`
	Token          string `json:"token"`
	PullOff        bool   `json:"pullOff"`
	WithdrawEnable bool   `json:"withdrawEnable"`
	UseFixedRate   bool   `json:"useFixedRate"`
	FixedRate      string `json:"fixedRate"`
}

type ActiveOrders struct {
	DataList []Order `json:"dataList"`
}

// Order
type Order struct {
	Id                        string           `json:"id"`
	UserId                    string           `json:"userId"`
	AccountId                 string           `json:"accountId"`
	CoinId                    string           `json:"coinId"`
	ContractId                string           `json:"contractId"`
	Side                      Side             `json:"side"`
	Price                     string           `json:"price"`
	Size                      string           `json:"size"`
	ClientOrderId             string           `json:"clientOrderId"`
	Type                      OrderType        `json:"type"`
	TimeInForce               string           `json:"timeInForce"`
	ReduceOnly                bool             `json:"reduceOnly"`
	TriggerPrice              string           `json:"triggerPrice"`
	TriggerPriceType          TriggerPriceType `json:"triggerPriceType"`
	ExpireTime                string           `json:"expireTime"`
	SourceKey                 string           `json:"sourceKey"`
	IsPositionTpsl            bool             `json:"isPositionTpsl"`
	IsLiquidate               bool             `json:"isLiquidate"`
	IsDeleverage              bool             `json:"isDeleverage"`
	OpenTpslParentOrderId     string           `json:"openTpslParentOrderId"`
	IsSetOpenTp               bool             `json:"isSetOpenTp"`
	OpenTp                    Tpsl             `json:"openTp"`
	IsSetOpenSl               bool             `json:"isSetOpenSl"`
	OpenSl                    Tpsl             `json:"openSl"`
	IsWithoutMatch            bool             `json:"isWithoutMatch"`
	WithoutMatchFillSize      string           `json:"withoutMatchFillSize"`
	WithoutMatchFillValue     string           `json:"withoutMatchFillValue"`
	WithoutMatchPeerAccountId string           `json:"withoutMatchPeerAccountId"`
	WithoutMatchPeerOrderId   string           `json:"withoutMatchPeerOrderId"`
	MaxLeverage               string           `json:"maxLeverage"`
	TakerFeeRate              string           `json:"takerFeeRate"`
	MakerFeeRate              string           `json:"makerFeeRate"`
	LiquidateFeeRate          string           `json:"liquidateFeeRate"`
	MarketLimitPrice          string           `json:"marketLimitPrice"`
	MarketLimitValue          string           `json:"marketLimitValue"`
	L2Nonce                   string           `json:"l2Nonce"`
	L2Value                   string           `json:"l2Value"`
	L2Size                    string           `json:"l2Size"`
	L2LimitFee                string           `json:"l2LimitFee"`
	L2ExpireTime              string           `json:"l2ExpireTime"`
	L2Signature               L2Signature      `json:"l2Signature"`
	ExtraType                 string           `json:"extraType"`
	ExtraDataJson             string           `json:"extraDataJson"`
	Status                    OrderStatus      `json:"status"`
	MatchSequenceId           string           `json:"matchSequenceId"`
	TriggerTime               string           `json:"triggerTime"`
	TriggerPriceTime          string           `json:"triggerPriceTime"`
	TriggerPriceValue         string           `json:"triggerPriceValue"`
	CancelReason              string           `json:"cancelReason"`
	CumFillSize               string           `json:"cumFillSize"`
	CumFillValue              string           `json:"cumFillValue"`
	CumFillFee                string           `json:"cumFillFee"`
	MaxFillPrice              string           `json:"maxFillPrice"`
	MinFillPrice              string           `json:"minFillPrice"`
	CumLiquidateFee           string           `json:"cumLiquidateFee"`
	CumRealizePnl             string           `json:"cumRealizePnl"`
	CumMatchSize              string           `json:"cumMatchSize"`
	CumMatchValue             string           `json:"cumMatchValue"`
	CumMatchFee               string           `json:"cumMatchFee"`
	CumFailSize               string           `json:"cumFailSize"`
	CumFailValue              string           `json:"cumFailValue"`
	CumFailFee                string           `json:"cumFailFee"`
	CumApprovedSize           string           `json:"cumApprovedSize"`
	CumApprovedValue          string           `json:"cumApprovedValue"`
	CumApprovedFee            string           `json:"cumApprovedFee"`
	CreatedTime               string           `json:"createdTime"`
	UpdatedTime               string           `json:"updatedTime"`
}

type Tpsl struct {
	Side             string      `json:"side"`
	Price            string      `json:"price"`
	Size             string      `json:"size"`
	ClientOrderId    string      `json:"clientOrderId"`
	TriggerPrice     string      `json:"triggerPrice"`
	TriggerPriceType string      `json:"triggerPriceType"`
	ExpireTime       string      `json:"expireTime"`
	L2Nonce          string      `json:"l2Nonce"`
	L2Value          string      `json:"l2Value"`
	L2Size           string      `json:"l2Size"`
	L2LimitFee       string      `json:"l2LimitFee"`
	L2ExpireTime     string      `json:"l2ExpireTime"`
	L2Signature      L2Signature `json:"l2Signature"`
}

type OrderFillTransaction struct {
	Id                      string `json:"id"`
	UserId                  string `json:"userId"`
	AccountId               string `json:"accountId"`
	CoinId                  string `json:"coinId"`
	ContractId              string `json:"contractId"`
	OrderId                 string `json:"orderId"`
	OrderSide               string `json:"orderSide"`
	FillSize                string `json:"fillSize"`
	FillValue               string `json:"fillValue"`
	FillFee                 string `json:"fillFee"`
	FillPrice               string `json:"fillPrice"`
	LiquidateFee            string `json:"liquidateFee"`
	RealizePnl              string `json:"realizePnl"`
	Direction               string `json:"direction"`
	IsPositionTpsl          bool   `json:"isPositionTpsl"`
	IsLiquidate             bool   `json:"isLiquidate"`
	IsDeleverage            bool   `json:"isDeleverage"`
	IsWithoutMatch          bool   `json:"isWithoutMatch"`
	MatchSequenceId         string `json:"matchSequenceId"`
	MatchIndex              int    `json:"matchIndex"`
	MatchTime               string `json:"matchTime"`
	MatchAccountId          string `json:"matchAccountId"`
	MatchOrderId            string `json:"matchOrderId"`
	MatchFillId             string `json:"matchFillId"`
	PositionTransactionId   string `json:"positionTransactionId"`
	CollateralTransactionId string `json:"collateralTransactionId"`
	ExtraType               string `json:"extraType"`
	ExtraDataJson           string `json:"extraDataJson"`
	CensorStatus            string `json:"censorStatus"`
	CensorTxId              string `json:"censorTxId"`
	CensorTime              string `json:"censorTime"`
	CensorFailCode          string `json:"censorFailCode"`
	CensorFailReason        string `json:"censorFailReason"`
	L2TxId                  string `json:"l2TxId"`
	L2RejectTime            string `json:"l2RejectTime"`
	L2RejectCode            string `json:"l2RejectCode"`
	L2RejectReason          string `json:"l2RejectReason"`
	L2ApprovedTime          string `json:"l2ApprovedTime"`
	CreatedTime             string `json:"createdTime"`
	UpdatedTime             string `json:"updatedTime"`
}

// Account
type Account struct {
	Id                        string                  `json:"id"`
	UserId                    string                  `json:"userId"`
	EthAddress                string                  `json:"ethAddress"`
	L2Key                     string                  `json:"l2Key"`
	L2KeyYCoordinate          string                  `json:"l2KeyYCoordinate"`
	ClientAccountId           string                  `json:"clientAccountId"`
	IsSystemAccount           bool                    `json:"isSystemAccount"`
	DefaultTradeSetting       TradeSetting            `json:"defaultTradeSetting"`
	ContractIdToTradeSetting  map[string]TradeSetting `json:"contractIdToTradeSetting"`
	MaxLeverageLimit          string                  `json:"maxLeverageLimit"`
	CreateOrderPerMinuteLimit int                     `json:"createOrderPerMinuteLimit"`
	CreateOrderDelayMillis    int                     `json:"createOrderDelayMillis"`
	ExtraType                 string                  `json:"extraType"`
	ExtraDataJson             string                  `json:"extraDataJson"`
	Status                    string                  `json:"status"`
	IsLiquidating             bool                    `json:"isLiquidating"`
	CreatedTime               string                  `json:"createdTime"`
	UpdatedTime               string                  `json:"updatedTime"`
}

// Account Asset Info
type AccountAsset struct {
	Account                  AccountInfo            `json:"account"`                  // 账户信息
	CollateralList           []Collateral           `json:"collateralList"`           // 资金流向记录（充提、交易、手续费）
	PositionList             []PositionInfo         `json:"positionList"`             // 仓位详情（开仓价、平仓价、P&L）
	Version                  string                 `json:"version"`                  // 数据版本号（用于并发控制）
	PositionAssetList        []PositionAsset        `json:"positionAssetList"`        // 仓位风险评估（清算价、破产价）
	CollateralAssetModelList []CollateralAssetModel `json:"collateralAssetModelList"` // 保证金风险模型
	OraclePriceList          []interface{}          `json:"oraclePriceList"`          // Empty in example, using interface{}
}

type AccountInfo struct {
	Id                        string                  `json:"id"`
	UserId                    string                  `json:"userId"`
	EthAddress                string                  `json:"ethAddress"`
	L2Key                     string                  `json:"l2Key"`
	L2KeyYCoordinate          string                  `json:"l2KeyYCoordinate"`
	ClientAccountId           string                  `json:"clientAccountId"`
	IsSystemAccount           bool                    `json:"isSystemAccount"`
	DefaultTradeSetting       TradeSetting            `json:"defaultTradeSetting"`      // 默认交易设置
	ContractIdToTradeSetting  map[string]TradeSetting `json:"contractIdToTradeSetting"` // 特定合约的交易设置, 留空 = 使用默认
	MaxLeverageLimit          string                  `json:"maxLeverageLimit"`
	CreateOrderPerMinuteLimit int                     `json:"createOrderPerMinuteLimit"`
	CreateOrderDelayMillis    int                     `json:"createOrderDelayMillis"`
	ExtraType                 string                  `json:"extraType"`
	ExtraDataJson             string                  `json:"extraDataJson"`
	Status                    string                  `json:"status"`
	IsLiquidating             bool                    `json:"isLiquidating"`
	CreatedTime               string                  `json:"createdTime"`
	UpdatedTime               string                  `json:"updatedTime"`
}

type TradeSetting struct {
	IsSetFeeRate     bool   `json:"isSetFeeRate"`     // 是否自定义费率
	TakerFeeRate     string `json:"takerFeeRate"`     // 吃单手续费
	MakerFeeRate     string `json:"makerFeeRate"`     // 挂单手续费
	IsSetFeeDiscount bool   `json:"isSetFeeDiscount"` // 是否自定义折扣
	TakerFeeDiscount string `json:"takerFeeDiscount"` // 吃单手续费折扣
	MakerFeeDiscount string `json:"makerFeeDiscount"` // 挂单手续费折扣
	IsSetMaxLeverage bool   `json:"isSetMaxLeverage"` // 为该合约设置了杠杆限制
	MaxLeverage      string `json:"maxLeverage"`      // 最大杠杆
}

type Collateral struct {
	UserId                 string `json:"userId"`
	AccountId              string `json:"accountId"`
	CoinId                 string `json:"coinId"`                 // 币种 ID
	Amount                 string `json:"amount"`                 // 当前余额
	LegacyAmount           string `json:"legacyAmount"`           // 旧系统兼容字段
	CumDepositAmount       string `json:"cumDepositAmount"`       // 累计充值金额
	CumWithdrawAmount      string `json:"cumWithdrawAmount"`      // 累计提现金额
	CumTransferInAmount    string `json:"cumTransferInAmount"`    // 累计转入金额
	CumTransferOutAmount   string `json:"cumTransferOutAmount"`   // 累计转出金额
	CumPositionBuyAmount   string `json:"cumPositionBuyAmount"`   // 累计持仓买入金额
	CumPositionSellAmount  string `json:"cumPositionSellAmount"`  // 累计持仓卖出金额
	CumFillFeeAmount       string `json:"cumFillFeeAmount"`       // 累计手续费
	CumFundingFeeAmount    string `json:"cumFundingFeeAmount"`    // 累计资金费
	CumFillFeeIncomeAmount string `json:"cumFillFeeIncomeAmount"` // 累计手续费收入
	CreatedTime            string `json:"createdTime"`            // 创建时间
	UpdatedTime            string `json:"updatedTime"`            // 更新时间
}

type CollateralTransaction struct {
	Id                     string `json:"id"`
	UserId                 string `json:"userId"`
	AccountId              string `json:"accountId"`
	CoinId                 string `json:"coinId"`
	Type                   string `json:"type"`
	DeltaAmount            string `json:"deltaAmount"`
	DeltaLegacyAmount      string `json:"deltaLegacyAmount"`
	BeforeAmount           string `json:"beforeAmount"`
	BeforeLegacyAmount     string `json:"beforeLegacyAmount"`
	FillCloseSize          string `json:"fillCloseSize"`
	FillCloseValue         string `json:"fillCloseValue"`
	FillCloseFee           string `json:"fillCloseFee"`
	FillOpenSize           string `json:"fillOpenSize"`
	FillOpenValue          string `json:"fillOpenValue"`
	FillOpenFee            string `json:"fillOpenFee"`
	FillPrice              string `json:"fillPrice"`
	LiquidateFee           string `json:"liquidateFee"`
	RealizePnl             string `json:"realizePnl"`
	IsLiquidate            bool   `json:"isLiquidate"`
	IsDeleverage           bool   `json:"isDeleverage"`
	FundingTime            string `json:"fundingTime"`
	FundingRate            string `json:"fundingRate"`
	FundingIndexPrice      string `json:"fundingIndexPrice"`
	FundingOraclePrice     string `json:"fundingOraclePrice"`
	FundingPositionSize    string `json:"fundingPositionSize"`
	DepositId              string `json:"depositId"`
	WithdrawId             string `json:"withdrawId"`
	TransferInId           string `json:"transferInId"`
	TransferOutId          string `json:"transferOutId"`
	TransferReason         string `json:"transferReason"`
	OrderId                string `json:"orderId"`
	OrderFillTransactionId string `json:"orderFillTransactionId"`
	OrderAccountId         string `json:"orderAccountId"`
	PositionContractId     string `json:"positionContractId"`
	PositionTransactionId  string `json:"positionTransactionId"`
	ForceWithdrawId        string `json:"forceWithdrawId"`
	ForceTradeId           string `json:"forceTradeId"`
	ExtraType              string `json:"extraType"`
	ExtraDataJson          string `json:"extraDataJson"`
	CensorStatus           string `json:"censorStatus"`
	CensorTxId             string `json:"censorTxId"`
	CensorTime             string `json:"censorTime"`
	CensorFailCode         string `json:"censorFailCode"`
	CensorFailReason       string `json:"censorFailReason"`
	L2TxId                 string `json:"l2TxId"`
	L2RejectTime           string `json:"l2RejectTime"`
	L2RejectCode           string `json:"l2RejectCode"`
	L2RejectReason         string `json:"l2RejectReason"`
	L2ApprovedTime         string `json:"l2ApprovedTime"`
	CreatedTime            string `json:"createdTime"`
	UpdatedTime            string `json:"updatedTime"`
}

type PositionInfo struct {
	UserId               string       `json:"userId"`
	AccountId            string       `json:"accountId"`
	CoinId               string       `json:"coinId"`        // 币种 ID
	ContractId           string       `json:"contractId"`    // 合约 ID
	OpenSize             string       `json:"openSize"`      // 持仓数量
	OpenValue            string       `json:"openValue"`     // 持仓价值
	OpenFee              string       `json:"openFee"`       // 开仓时支付的手续费
	FundingFee           string       `json:"fundingFee"`    // 待收/代付的资金费
	LongTermCount        int          `json:"longTermCount"` // 长期仓位数量
	LongTermStat         PositionStat `json:"longTermStat"`  // 累计统计
	LongTermCreatedTime  string       `json:"longTermCreatedTime"`
	LongTermUpdatedTime  string       `json:"longTermUpdatedTime"`
	ShortTermCount       int          `json:"shortTermCount"`
	ShortTermStat        PositionStat `json:"shortTermStat"`
	ShortTermCreatedTime string       `json:"shortTermCreatedTime"`
	ShortTermUpdatedTime string       `json:"shortTermUpdatedTime"`
	LongTotalStat        PositionStat `json:"longTotalStat"`
	ShortTotalStat       PositionStat `json:"shortTotalStat"`
	CreatedTime          string       `json:"createdTime"`
	UpdatedTime          string       `json:"updatedTime"`
}

type PositionStat struct {
	CumOpenSize     string `json:"cumOpenSize"`
	CumOpenValue    string `json:"cumOpenValue"`
	CumOpenFee      string `json:"cumOpenFee"`
	CumCloseSize    string `json:"cumCloseSize"`
	CumCloseValue   string `json:"cumCloseValue"`
	CumCloseFee     string `json:"cumCloseFee"`
	CumFundingFee   string `json:"cumFundingFee"`
	CumLiquidateFee string `json:"cumLiquidateFee"`
}

type PositionAsset struct {
	UserId                   string `json:"userId"`
	AccountId                string `json:"accountId"`
	CoinId                   string `json:"coinId"`
	ContractId               string `json:"contractId"`
	PositionValue            string `json:"positionValue"`            // 仓位价值
	MaxLeverage              string `json:"maxLeverage"`              // 最大杠杆
	InitialMarginRequirement string `json:"initialMarginRequirement"` // 初始保证金杠杆
	StarkExRiskRate          string `json:"starkExRiskRate"`
	StarkExRiskValue         string `json:"starkExRiskValue"`
	AvgEntryPrice            string `json:"avgEntryPrice"`   // 平均开仓价格
	LiquidatePrice           string `json:"liquidatePrice"`  // 清算价格
	BankruptPrice            string `json:"bankruptPrice"`   // 破产价格
	WorstClosePrice          string `json:"worstClosePrice"` // 最坏平仓价格
	UnrealizePnl             string `json:"unrealizePnl"`    // 未实现盈亏
	TermRealizePnl           string `json:"termRealizePnl"`  // 期初未实现盈亏
	TotalRealizePnl          string `json:"totalRealizePnl"` // 总未实现盈亏
}

type PositionTerm struct {
	UserId          string `json:"userId"`
	AccountId       string `json:"accountId"`
	CoinId          string `json:"coinId"`
	ContractId      string `json:"contractId"`
	TermCount       int    `json:"termCount"`
	CumOpenSize     string `json:"cumOpenSize"`
	CumOpenValue    string `json:"cumOpenValue"`
	CumOpenFee      string `json:"cumOpenFee"`
	CumCloseSize    string `json:"cumCloseSize"`
	CumCloseValue   string `json:"cumCloseValue"`
	CumCloseFee     string `json:"cumCloseFee"`
	CumFundingFee   string `json:"cumFundingFee"`
	CumLiquidateFee string `json:"cumLiquidateFee"`
	CreatedTime     string `json:"createdTime"`
	UpdatedTime     string `json:"updatedTime"`
	CurrentLeverage string `json:"currentLeverage"`
}

type CollateralAssetModel struct {
	UserId                   string `json:"userId"`
	AccountId                string `json:"accountId"`
	CoinId                   string `json:"coinId"`
	TotalEquity              string `json:"totalEquity"`              // 总权益
	TotalPositionValueAbs    string `json:"totalPositionValueAbs"`    // 仓位价值
	InitialMarginRequirement string `json:"initialMarginRequirement"` // 初始保证金需求
	StarkExRiskValue         string `json:"starkExRiskValue"`         // StarkEx 系统的风险估值
	PendingWithdrawAmount    string `json:"pendingWithdrawAmount"`    // 待提现冻结
	PendingTransferOutAmount string `json:"pendingTransferOutAmount"` // 待转出冻结
	OrderFrozenAmount        string `json:"orderFrozenAmount"`        // 挂单冻结
	AvailableAmount          string `json:"availableAmount"`          // 可用保证金
}

// Position Transaction
type PositionTransaction struct {
	Id                      string `json:"id"`
	UserId                  string `json:"userId"`
	AccountId               string `json:"accountId"`
	CoinId                  string `json:"coinId"`
	ContractId              string `json:"contractId"`
	Type                    string `json:"type"`
	DeltaOpenSize           string `json:"deltaOpenSize"`
	DeltaOpenValue          string `json:"deltaOpenValue"`
	DeltaOpenFee            string `json:"deltaOpenFee"`
	DeltaFundingFee         string `json:"deltaFundingFee"`
	BeforeOpenSize          string `json:"beforeOpenSize"`
	BeforeOpenValue         string `json:"beforeOpenValue"`
	BeforeOpenFee           string `json:"beforeOpenFee"`
	BeforeFundingFee        string `json:"beforeFundingFee"`
	FillCloseSize           string `json:"fillCloseSize"`
	FillCloseValue          string `json:"fillCloseValue"`
	FillCloseFee            string `json:"fillCloseFee"`
	FillOpenSize            string `json:"fillOpenSize"`
	FillOpenValue           string `json:"fillOpenValue"`
	FillOpenFee             string `json:"fillOpenFee"`
	FillPrice               string `json:"fillPrice"`
	LiquidateFee            string `json:"liquidateFee"`
	RealizePnl              string `json:"realizePnl"`
	IsLiquidate             bool   `json:"isLiquidate"`
	IsDeleverage            bool   `json:"isDeleverage"`
	FundingTime             string `json:"fundingTime"`
	FundingRate             string `json:"fundingRate"`
	FundingIndexPrice       string `json:"fundingIndexPrice"`
	FundingOraclePrice      string `json:"fundingOraclePrice"`
	FundingPositionSize     string `json:"fundingPositionSize"`
	OrderId                 string `json:"orderId"`
	OrderFillTransactionId  string `json:"orderFillTransactionId"`
	CollateralTransactionId string `json:"collateralTransactionId"`
	ForceTradeId            string `json:"forceTradeId"`
	ExtraType               string `json:"extraType"`
	ExtraDataJson           string `json:"extraDataJson"`
	CensorStatus            string `json:"censorStatus"`
	CensorTxId              string `json:"censorTxId"`
	CensorTime              string `json:"censorTime"`
	CensorFailCode          string `json:"censorFailCode"`
	CensorFailReason        string `json:"censorFailReason"`
	L2TxId                  string `json:"l2TxId"`
	L2RejectTime            string `json:"l2RejectTime"`
	L2RejectCode            string `json:"l2RejectCode"`
	L2RejectReason          string `json:"l2RejectReason"`
	L2ApprovedTime          string `json:"l2ApprovedTime"`
	CreatedTime             string `json:"createdTime"`
	UpdatedTime             string `json:"updatedTime"`
}

type PositionTransactionResponse struct {
	DataList           []PositionTransaction `json:"dataList"`
	NextPageOffsetData string                `json:"nextPageOffsetData"`
}

// Ticker
type Ticker struct {
	ContractId         string `json:"contractId"`
	ContractName       string `json:"contractName"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	Trades             string `json:"trades"`
	Size               string `json:"size"`
	Value              string `json:"value"`
	High               string `json:"high"`
	Low                string `json:"low"`
	Open               string `json:"open"`
	Close              string `json:"close"`
	HighTime           string `json:"highTime"`
	LowTime            string `json:"lowTime"`
	StartTime          string `json:"startTime"`
	EndTime            string `json:"endTime"`
	LastPrice          string `json:"lastPrice"`
	IndexPrice         string `json:"indexPrice"`
	OraclePrice        string `json:"oraclePrice"`
	OpenInterest       string `json:"openInterest"`
	FundingRate        string `json:"fundingRate"`
	FundingTime        string `json:"fundingTime"`
	NextFundingTime    string `json:"nextFundingTime"`
}

// Place Order Params
type PlaceOrderParams struct {
	ContractId    string `json:"contractId"`
	Side          string `json:"side"`
	Type          string `json:"type"`
	Quantity      string `json:"quantity"`
	Price         string `json:"price,omitempty"`
	ClientOrderId string `json:"clientOrderId,omitempty"`
	TimeInForce   string `json:"timeInForce,omitempty"` // GOOD_TIL_CANCEL/FILL_OR_KILL/IMMEDIATE_OR_CANCEL/POST_ONLY
	ReduceOnly    bool   `json:"reduceOnly,omitempty"`
	ExpireTime    int64  `json:"expireTime,omitempty"`
}

type L2Signature struct {
	R string `json:"r"`
	S string `json:"s"`
	V string `json:"v,omitempty"`
}

type CreateOrderData struct {
	OrderId string `json:"orderId"`
}

type ResultCreateOrder struct {
	Code       string           `json:"code"`
	Data       *CreateOrderData `json:"data"`
	ErrorParam interface{}      `json:"errorParam"`
	ErrorMsg   string           `json:"msg"`
}

type CancelOrderData struct {
	CancelResultMap map[string]interface{} `json:"cancelResultMap"`
}

type UpdateLeverageSettingResponse struct {
	// Empty data usually
}

type OrderBook struct {
	StartVersion string  `json:"startVersion"`
	EndVersion   string  `json:"endVersion"`
	Level        int     `json:"level"`
	ContractId   string  `json:"contractId"`
	ContractName string  `json:"contractName"`
	Asks         []Level `json:"asks"`
	Bids         []Level `json:"bids"`
}

type Level struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type GetKlineResponse struct {
	DataList           []Kline `json:"dataList"`
	NextPageOffsetData string  `json:"nextPageOffsetData"`
}

type Kline struct {
	KlineId       string    `json:"klineId"`
	ContractId    string    `json:"contractId"`
	ContractName  string    `json:"contractName"`
	KlineType     KlineType `json:"klineType"`
	KlineTime     string    `json:"klineTime"`
	PriceType     PriceType `json:"priceType"`
	Trades        string    `json:"trades"`
	Size          string    `json:"size"`
	Value         string    `json:"value"`
	High          string    `json:"high"`
	Low           string    `json:"low"`
	Open          string    `json:"open"`
	Close         string    `json:"close"`
	MakerBuySize  string    `json:"makerBuySize"`
	MakerBuyValue string    `json:"makerBuyValue"`
}

// kline type
type KlineType string

const (
	KlineTypeUnknownKlineType KlineType = "UNKNOWN_KLINE_TYPE"
	KlineTypeMinute1          KlineType = "MINUTE_1"
	KlineTypeMinute5          KlineType = "MINUTE_5"
	KlineTypeMinute15         KlineType = "MINUTE_15"
	KlineTypeMinute30         KlineType = "MINUTE_30"
	KlineTypeHour1            KlineType = "HOUR_1"
	KlineTypeHour2            KlineType = "HOUR_2"
	KlineTypeHour4            KlineType = "HOUR_4"
	KlineTypeHour6            KlineType = "HOUR_6"
	KlineTypeHour8            KlineType = "HOUR_8"
	KlineTypeHour12           KlineType = "HOUR_12"
	KlineTypeDay1             KlineType = "DAY_1"
	KlineTypeWeek1            KlineType = "WEEK_1"
	KlineTypeMonth1           KlineType = "MONTH_1"
	KlineTypeUnrecognized     KlineType = "UNRECOGNIZED"
)

// price type
type PriceType string

const (
	PriceTypeUnknownPriceType PriceType = "UNKNOWN_PRICE_TYPE"
	PriceTypeOraclePrice      PriceType = "ORACLE_PRICE"
	PriceTypeIndexPrice       PriceType = "INDEX_PRICE"
	PriceTypeLastPrice        PriceType = "LAST_PRICE"
	PriceTypeAsk1Price        PriceType = "ASK1_PRICE"
	PriceTypeBid1Price        PriceType = "BID1_PRICE"
	PriceTypeOpenInterest     PriceType = "OPEN_INTEREST"
	PriceTypeUnrecognized     PriceType = "UNRECOGNIZED"
)

type GetExchangeLongShortRatioResponse struct {
	ExchangeLongShortRatioList []ExchangeLongShortRatio `json:"exchangeLongShortRatioList"`
	AllRangeList               []string                 `json:"allRangeList"`
}

type ExchangeLongShortRatio struct {
	Range       string `json:"range"`
	ContractId  string `json:"contractId"`
	Exchange    string `json:"exchange"`
	BuyRatio    string `json:"buyRatio"`
	SellRatio   string `json:"sellRatio"`
	BuyVolUsd   string `json:"buyVolUsd"`
	SellVolUsd  string `json:"sellVolUsd"`
	CreatedTime string `json:"createdTime"`
	UpdatedTime string `json:"updatedTime"`
}

type TransferIn struct {
	Id                           string `json:"id"`
	UserId                       string `json:"userId"`
	AccountId                    string `json:"accountId"`
	CoinId                       string `json:"coinId"`
	Amount                       string `json:"amount"`
	SenderAccountId              string `json:"senderAccountId"`
	SenderL2Key                  string `json:"senderL2Key"`
	SenderTransferOutId          string `json:"senderTransferOutId"`
	ClientTransferId             string `json:"clientTransferId"`
	IsConditionTransfer          bool   `json:"isConditionTransfer"`
	ConditionFactRegistryAddress string `json:"conditionFactRegistryAddress"`
	ConditionFactErc20Address    string `json:"conditionFactErc20Address"`
	ConditionFactAmount          string `json:"conditionFactAmount"`
	ConditionFact                string `json:"conditionFact"`
	TransferReason               string `json:"transferReason"`
	ExtraType                    string `json:"extraType"`
	ExtraDataJson                string `json:"extraDataJson"`
	Status                       string `json:"status"`
	CollateralTransactionId      string `json:"collateralTransactionId"`
	CensorTxId                   string `json:"censorTxId"`
	CensorTime                   string `json:"censorTime"`
	CensorFailCode               string `json:"censorFailCode"`
	CensorFailReason             string `json:"censorFailReason"`
	L2TxId                       string `json:"l2TxId"`
	L2RejectTime                 string `json:"l2RejectTime"`
	L2RejectCode                 string `json:"l2RejectCode"`
	L2RejectReason               string `json:"l2RejectReason"`
	L2ApprovedTime               string `json:"l2ApprovedTime"`
	CreatedTime                  string `json:"createdTime"`
	UpdatedTime                  string `json:"updatedTime"`
}

type TransferOut struct {
	Id                           string      `json:"id"`
	UserId                       string      `json:"userId"`
	AccountId                    string      `json:"accountId"`
	CoinId                       string      `json:"coinId"`
	Amount                       string      `json:"amount"`
	ReceiverAccountId            string      `json:"receiverAccountId"`
	ReceiverL2Key                string      `json:"receiverL2Key"`
	ClientTransferId             string      `json:"clientTransferId"`
	IsConditionTransfer          bool        `json:"isConditionTransfer"`
	ConditionFactRegistryAddress string      `json:"conditionFactRegistryAddress"`
	ConditionFactErc20Address    string      `json:"conditionFactErc20Address"`
	ConditionFactAmount          string      `json:"conditionFactAmount"`
	ConditionFact                string      `json:"conditionFact"`
	TransferReason               string      `json:"transferReason"`
	L2Nonce                      string      `json:"l2Nonce"`
	L2ExpireTime                 string      `json:"l2ExpireTime"`
	L2Signature                  L2Signature `json:"l2Signature"`
	ExtraType                    string      `json:"extraType"`
	ExtraDataJson                string      `json:"extraDataJson"`
	Status                       string      `json:"status"`
	ReceiverTransferInId         string      `json:"receiverTransferInId"`
	CollateralTransactionId      string      `json:"collateralTransactionId"`
	CensorTxId                   string      `json:"censorTxId"`
	CensorTime                   string      `json:"censorTime"`
	CensorFailCode               string      `json:"censorFailCode"`
	CensorFailReason             string      `json:"censorFailReason"`
	L2TxId                       string      `json:"l2TxId"`
	L2RejectTime                 string      `json:"l2RejectTime"`
	L2RejectCode                 string      `json:"l2RejectCode"`
	L2RejectReason               string      `json:"l2RejectReason"`
	L2ApprovedTime               string      `json:"l2ApprovedTime"`
	CreatedTime                  string      `json:"createdTime"`
	UpdatedTime                  string      `json:"updatedTime"`
}

type Withdraw struct {
	Id                      string      `json:"id"`
	UserId                  string      `json:"userId"`
	AccountId               string      `json:"accountId"`
	CoinId                  string      `json:"coinId"`
	Amount                  string      `json:"amount"`
	EthAddress              string      `json:"ethAddress"`
	Erc20Address            string      `json:"erc20Address"`
	ClientWithdrawId        string      `json:"clientWithdrawId"`
	RiskSignature           L2Signature `json:"riskSignature"`
	L2Nonce                 string      `json:"l2Nonce"`
	L2ExpireTime            string      `json:"l2ExpireTime"`
	L2Signature             L2Signature `json:"l2Signature"`
	ExtraType               string      `json:"extraType"`
	ExtraDataJson           string      `json:"extraDataJson"`
	Status                  string      `json:"status"`
	CollateralTransactionId string      `json:"collateralTransactionId"`
	CensorTxId              string      `json:"censorTxId"`
	CensorTime              string      `json:"censorTime"`
	CensorFailCode          string      `json:"censorFailCode"`
	CensorFailReason        string      `json:"censorFailReason"`
	L2TxId                  string      `json:"l2TxId"`
	L2RejectTime            string      `json:"l2RejectTime"`
	L2RejectCode            string      `json:"l2RejectCode"`
	L2RejectReason          string      `json:"l2RejectReason"`
	L2ApprovedTime          string      `json:"l2ApprovedTime"`
	CreatedTime             string      `json:"createdTime"`
	UpdatedTime             string      `json:"updatedTime"`
}

type OrderStatus string

func (os OrderStatus) String() string {
	return string(os)
}

const (
	OrderStatusUnknown      OrderStatus = "UNKNOWN_ORDER_STATUS"
	OrderStatusPending      OrderStatus = "PENDING"
	OrderStatusOpen         OrderStatus = "OPEN"
	OrderStatusFilled       OrderStatus = "FILLED"
	OrderStatusCanceling    OrderStatus = "CANCELING"
	OrderStatusCanceled     OrderStatus = "CANCELED"
	OrderStatusUntriggered  OrderStatus = "UNTRIGGERED"
	OrderStatusUnrecognized OrderStatus = "UNRECOGNIZED"
)

// Funding Rate types
type FundingRateData struct {
	ContractId               string `json:"contractId"`               // Contract ID
	FundingTime              string `json:"fundingTime"`              // Funding rate settlement time
	FundingTimestamp         string `json:"fundingTimestamp"`         // Funding rate calculation time in milliseconds
	OraclePrice              string `json:"oraclePrice"`              // Oracle price
	MarkPrice                string `json:"markPrice"`                // Mark price
	IndexPrice               string `json:"indexPrice"`               // Index price
	FundingRate              string `json:"fundingRate"`              // Rate for the settlement interval returned by the venue
	IsSettlement             bool   `json:"isSettlement"`             // Funding rate settlement flag
	ForecastFundingRate      string `json:"forecastFundingRate"`      // Forecast funding rate
	PreviousFundingRate      string `json:"previousFundingRate"`      // Previous funding rate
	PreviousFundingTimestamp string `json:"previousFundingTimestamp"` // Previous funding rate calculation time
	PremiumIndex             string `json:"premiumIndex"`             // Premium index
	AvgPremiumIndex          string `json:"avgPremiumIndex"`          // Average premium index
	PremiumIndexTimestamp    string `json:"premiumIndexTimestamp"`    // Premium index calculation time
	ImpactMarginNotional     string `json:"impactMarginNotional"`     // Impact margin notional
	ImpactAskPrice           string `json:"impactAskPrice"`           // Impact ask price
	ImpactBidPrice           string `json:"impactBidPrice"`           // Impact bid price
	InterestRate             string `json:"interestRate"`             // Interest rate
	PredictedFundingRate     string `json:"predictedFundingRate"`     // Predicted funding rate
	FundingRateIntervalMin   string `json:"fundingRateIntervalMin"`   // Funding rate interval in minutes
	StarkExFundingIndex      string `json:"starkExFundingIndex"`      // StarkEx funding index
}

type FundingRatePage struct {
	DataList           []FundingRateData `json:"dataList"`
	NextPageOffsetData string            `json:"nextPageOffsetData"`
}

type OrderType string

const (
	OrderTypeLimit            OrderType = "LIMIT"
	OrderTypeMarket           OrderType = "MARKET"
	OrderTypeStopLimit        OrderType = "STOP_LIMIT"
	OrderTypeStopMarket       OrderType = "STOP_MARKET"
	OrderTypeTakeProfitLimit  OrderType = "TAKE_PROFIT_LIMIT"
	OrderTypeTakeProfitMarket OrderType = "TAKE_PROFIT_MARKET"
	OrderTypeUnknownOrderType OrderType = "UNKNOWN_ORDER_TYPE"
	OrderTypeUnrecognized     OrderType = "UNRECOGNIZED"
)

type TimeInForce string

const (
	TimeInForceUnknown           TimeInForce = "UNKNOWN_TIME_IN_FORCE"
	TimeInForceGoodTilCancel     TimeInForce = "GOOD_TIL_CANCEL"
	TimeInForceFillOrKill        TimeInForce = "FILL_OR_KILL"
	TimeInForceImmediateOrCancel TimeInForce = "IMMEDIATE_OR_CANCEL"
	TimeInForcePostOnly          TimeInForce = "POST_ONLY"
	TimeInForceUnrecognized      TimeInForce = "UNRECOGNIZED"
)

type TriggerPriceType string

const (
	TriggerPriceTypeUnknown      TriggerPriceType = "UNKNOWN_PRICE_TYPE"
	TriggerPriceTypeOraclePrice  TriggerPriceType = "ORACLE_PRICE"
	TriggerPriceTypeIndexPrice   TriggerPriceType = "INDEX_PRICE"
	TriggerPriceTypeLastPrice    TriggerPriceType = "LAST_PRICE"
	TriggerPriceTypeAsk1Price    TriggerPriceType = "ASK1_PRICE"
	TriggerPriceTypeBid1Price    TriggerPriceType = "BID1_PRICE"
	TriggerPriceTypeOpenInterest TriggerPriceType = "OPEN_INTEREST"
	TriggerPriceTypeUnrecognized TriggerPriceType = "UNRECOGNIZED"
)

type CancelReason string

const (
	CancelReasonUnknownOrderCancelReason CancelReason = "UNKNOWN_ORDER_CANCEL_REASON"
	CancelReasonUserCanceled             CancelReason = "USER_CANCELED"
	CancelReasonExpireCanceled           CancelReason = "EXPIRE_CANCELED"
	CancelReasonCouldNotFill             CancelReason = "COULD_NOT_FILL"
	CancelReasonReduceOnlyCanceled       CancelReason = "REDUCE_ONLY_CANCELED"
	CancelReasonLiquidateCanceled        CancelReason = "LIQUIDATE_CANCELED"
	CancelReasonMarginNotEnough          CancelReason = "MARGIN_NOT_ENOUGH"
	CancelReasonSystemLimitEvicted       CancelReason = "SYSTEM_LIMIT_EVICTED"
	CancelReasonAdminCanceled            CancelReason = "ADMIN_CANCELED"
	CancelReasonInternalFailed           CancelReason = "INTERNAL_FAILED"
	CancelReasonUnrecognized             CancelReason = "UNRECOGNIZED"
)

type Side string

const (
	SideUnknownSide  Side = "UNKNOWN_ORDER_SIDE"
	SideBuy          Side = "BUY"
	SideSell         Side = "SELL"
	SideUnrecognized Side = "UNRECOGNIZED"
)
