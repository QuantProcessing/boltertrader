package lighter

import "strings"

import (
	"fmt"
)

// Constants
const (
	TxTypeChangePubKey          = 8
	TxTypeCreateSubAccount      = 9
	TxTypeCreatePublicPool      = 10
	TxTypeUpdatePublicPool      = 11
	TxTypeTransfer              = 12
	TxTypeWithdraw              = 13
	TxTypeCreateOrder           = 14
	TxTypeCancelOrder           = 15
	TxTypeCancelAllOrders       = 16
	TxTypeModifyOrder           = 17
	TxTypeMintShares            = 18
	TxTypeBurnShares            = 19
	TxTypeUpdateLeverage        = 20
	TxTypeL2CreateGroupedOrders = 28
	TxTypeL2UpdateMargin        = 29

	OrderTypeLimit           = 0
	OrderTypeMarket          = 1
	OrderTypeStopLoss        = 2
	OrderTypeStopLossLimit   = 3
	OrderTypeTakeProfit      = 4
	OrderTypeTakeProfitLimit = 5
	OrderTypeTwap            = 6

	OrderTimeInForceImmediateOrCancel = 0
	OrderTimeInForceGoodTillTime      = 1
	OrderTimeInForcePostOnly          = 2

	CancelAllTifImmediate = 0
	CancelAllTifScheduled = 1
	CancelAllTifAbort     = 2

	NilTriggerPrice         = 0
	Default28DayOrderExpiry = -1
	DefaultIocExpiry        = 0
	Default10MinAuthExpiry  = -1
	Minute                  = 60

	CrossMarginMode    = 0
	IsolatedMarginMode = 1

	MainnetChainID = 304
)

type OrderStatus string

const (
	// OrderStatus
	OrderStatusInProgress                 OrderStatus = "in-progress"
	OrderStatusPending                    OrderStatus = "pending"
	OrderStatusOpen                       OrderStatus = "open"
	OrderStatusFilled                     OrderStatus = "filled"
	OrderStatusCanceled                   OrderStatus = "canceled"
	OrderStatusCanceledPostOnly           OrderStatus = "canceled-post-only"
	OrderStatusCanceledReduceOnly         OrderStatus = "canceled-reduce-only"
	OrderStatusCanceledPositionNotAllowed OrderStatus = "canceled-position-not-allowed"
	OrderStatusCanceledMarginNotAllowed   OrderStatus = "canceled-margin-not-allowed"
	OrderStatusCanceledTooMuchSlippage    OrderStatus = "canceled-too-much-slippage"
	OrderStatusCanceledNotEnoughLiquidity OrderStatus = "canceled-not-enough-liquidity"
	OrderStatusCanceledSelfTrade          OrderStatus = "canceled-self-trade"
	OrderStatusCanceledExpired            OrderStatus = "canceled-expired"
	OrderStatusCanceledOco                OrderStatus = "canceled-oco"
	OrderStatusCanceledChild              OrderStatus = "canceled-child"
	OrderStatusCanceledLiquidation        OrderStatus = "canceled-liquidation"
	OrderStatusCanceledInvalidBalance     OrderStatus = "canceled-invalid-balance"

	// lighter not provided
	OrderStatusRejected        OrderStatus = "rejected"
	OrderStatusPartiallyFilled OrderStatus = "partially-filled"
)

type OrderTypeResp string

const (
	OrderTypeRespLimit           OrderTypeResp = "limit"
	OrderTypeRespMarket          OrderTypeResp = "market"
	OrderTypeRespStopLoss        OrderTypeResp = "stop-loss"
	OrderTypeRespStopLossLimit   OrderTypeResp = "stop-loss-limit"
	OrderTypeRespTakeProfit      OrderTypeResp = "take-profit"
	OrderTypeRespTakeProfitLimit OrderTypeResp = "take-profit-limit"
	OrderTypeRespTwap            OrderTypeResp = "twap"
	OrderTypeRespTwapSub         OrderTypeResp = "twap-sub"
	OrderTypeRespLiquidation     OrderTypeResp = "liquidation"
)

// APIError represents an error response from the API
type APIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("API Error %d: %s", e.Code, e.Message)
}

type UpdateLeveragePayload struct {
	*UpdateLeverageInfo
	Sig        []byte `json:"Sig"`
	SignedHash string `json:"-"`
}

type AssetDetailsResponse struct {
	Code   int      `json:"code"`
	Msg    string   `json:"message"`
	Assets []*Asset `json:"assets_details"`
}

type Asset struct {
	AssetIndex          int16  `json:"asset_index"`
	Symbol              string `json:"symbol"`
	L1Decimals          uint8  `json:"l1_decimals"`
	Decimals            uint8  `json:"decimals"`
	ExtensionMultiplier int64  `json:"extension_multiplier"`
	MinTransferAmount   int64  `json:"min_transfer_amount"`
	MinWithdrawalAmount int64  `json:"min_withdrawal_amount"`
	MarginMode          uint8  `json:"margin_mode"`
	IndexPrice          uint32 `json:"index_price"`
	L1Address           string `json:"l1_address"`
	TickSize            string `json:"tick_size"`
}

// Order represents an order in the system
type Order struct {
	OrderIndex                  int64         `json:"order_index"`
	ClientOrderIndex            int64         `json:"client_order_index"`
	OrderId                     string        `json:"order_id"`
	ClientOrderId               string        `json:"client_order_id"`
	MarketIndex                 int           `json:"market_index"`
	OwnerAccountIndex           int           `json:"owner_account_index"`
	InitialBaseAmount           string        `json:"initial_base_amount"`
	Price                       string        `json:"price"`
	Nonce                       int64         `json:"nonce"`
	RemainingBaseAmount         string        `json:"remaining_base_amount"`
	IsAsk                       bool          `json:"is_ask"`
	BaseSize                    int64         `json:"base_size"`
	BasePrice                   int64         `json:"base_price"`
	FilledBaseAmount            string        `json:"filled_base_amount"`
	FilledQuoteAmount           string        `json:"filled_quote_amount"`
	Side                        string        `json:"side"`
	OrderType                   OrderTypeResp `json:"type"`
	TimeInForce                 string        `json:"time_in_force"`
	ReduceOnly                  bool          `json:"reduce_only"`
	TriggerPrice                string        `json:"trigger_price"`
	OrderExpiry                 int64         `json:"order_expiry"`
	Status                      OrderStatus   `json:"status"`
	TriggerStatus               string        `json:"trigger_status"`
	TriggerTime                 int64         `json:"trigger_time"`
	ParentOrderIndex            int64         `json:"parent_order_index"`
	ParentOrderId               string        `json:"parent_order_id"`
	ToTriggerOrderId0           string        `json:"to_trigger_order_id_0"`
	ToTriggerOrderId1           string        `json:"to_trigger_order_id_1"`
	ToTriggerOrderId2           string        `json:"to_trigger_order_id_2"`
	ToCancelOrderId0            string        `json:"to_cancel_order_id_0"`
	IntegratorFeeCollectorIndex string        `json:"integrator_fee_collector_index"`
	IntegratorTakerFee          string        `json:"integrator_taker_fee"`
	IntegratorMakerFee          string        `json:"integrator_maker_fee"`
	BlockHeight                 int64         `json:"block_height"`
	Timestamp                   int64         `json:"timestamp"`
	CreatedAt                   int64         `json:"created_at"`
	UpdatedAt                   int64         `json:"updated_at"`
	TransactionTime             int64         `json:"transaction_time"`
}

// AccountInfo represents account information
type AccountInfo struct {
	AccountIndex int64  `json:"account_index"`
	L1Address    string `json:"l1_address"`
	// Add other fields as needed
}

// CreateOrderRequest represents the payload for creating an order
type CreateOrderRequest struct {
	MarketId      int    `json:"market_id"`
	Price         uint32 `json:"price"`
	BaseAmount    int64  `json:"base_amount"`
	IsAsk         uint32 `json:"is_ask"`
	OrderType     uint32 `json:"order_type"`
	ClientOrderId int64  `json:"client_order_id,omitempty"`
	TimeInForce   uint32 `json:"time_in_force,omitempty"`
	ReduceOnly    uint32 `json:"reduce_only"`
	TriggerPrice  uint32 `json:"trigger_price"`
	OrderExpiry   int64  `json:"order_expiry"`
}

// CreateOrderResponse represents the response from creating an order
type CreateOrderResponse struct {
	Code                     int32  `json:"code"`
	Message                  string `json:"message"`
	TxHash                   string `json:"tx_hash"`
	PredictedExecutionTimeMs int64  `json:"predicted_execution_time_ms"`
}

// CancelOrderRequest represents the payload for cancelling an order
type CancelOrderRequest struct {
	MarketId int   `json:"market_id"`
	OrderId  int64 `json:"order_id"`
}

// CancelOrderResponse represents the response from cancelling an order
type CancelOrderResponse struct {
	Code                     int32  `json:"code"`
	Message                  string `json:"message"`
	TxHash                   string `json:"tx_hash"`
	PredictedExecutionTimeMs int64  `json:"predicted_execution_time_ms"`
}

// CancelAllOrdersRequest represents the payload for cancelling all orders
type CancelAllOrdersRequest struct {
	MarketId     int    `json:"market_id"`
	CancelAllTif uint32 `json:"cancel_all_tif,omitempty"`
}

// UpdateLeverageRequest represents the payload for updating leverage
type UpdateLeverageRequest struct {
	MarketId              int    `json:"market_id"`
	InitialMarginFraction uint16 `json:"initial_margin_fraction"`
	MarginMode            uint8  `json:"margin_mode"` // 0: Cross, 1: Isolated
}

// UpdateLeverageResponse represents the response from updating leverage
type UpdateLeverageResponse struct {
	Code                     int32  `json:"code"`
	Msg                      string `json:"message"`
	TxHash                   string `json:"tx_hash"`
	PredictedExecutionTimeMs int64  `json:"predicted_execution_time_ms"`
}

type OrderBookDetailsResponse struct {
	Code                 int32              `json:"code"`
	Msg                  string             `json:"message"`
	OrderBookDetails     []*OrderBookDetail `json:"order_book_details"`
	SpotOrderBookDetails []*OrderBookDetail `json:"spot_order_book_details"`
}

type OrderBookDetail struct {
	Symbol                       string  `json:"symbol"`
	MarketId                     int     `json:"market_id"`
	MarketType                   string  `json:"market_type"`
	BaseAssetId                  int     `json:"base_asset_id"`
	QuoteAssetId                 int     `json:"quote_asset_id"`
	Status                       string  `json:"status"`
	TakerFee                     string  `json:"taker_fee"`
	MakerFee                     string  `json:"maker_fee"`
	LiquidationFee               string  `json:"liquidation_fee"`
	MinBaseAmount                string  `json:"min_base_amount"`
	MinQuoteAmount               string  `json:"min_quote_amount"`
	SupportedSizeDecimals        uint8   `json:"supported_size_decimals"`
	SupportedPriceDecimals       uint8   `json:"supported_price_decimals"`
	SupportedQuoteDecimals       uint8   `json:"supported_quote_decimals"`
	SizeDecimals                 uint8   `json:"size_decimals"`
	PriceDecimals                uint8   `json:"price_decimals"`
	QuoteMultiplier              int64   `json:"quote_multiplier"`
	DefaultInitialMarginFraction int     `json:"default_initial_margin_fraction"`
	MinInitialMarginFraction     int     `json:"min_initial_margin_fraction"`
	MaintenanceMarginFraction    int     `json:"maintenance_margin_fraction"`
	CloseoutMarginFraction       int     `json:"closeout_margin_fraction"`
	LastTradePrice               float64 `json:"last_trade_price"`
	DailyTradesCount             int64   `json:"daily_trades_count"`
	DailyBaseTokenVolume         float64 `json:"daily_base_token_volume"`
	DailyQuoteTokenVolume        float64 `json:"daily_quote_token_volume"`
	DailyPriceLow                float64 `json:"daily_price_low"`
	DailyPriceHigh               float64 `json:"daily_price_high"`
	DailyPriceChange             float64 `json:"daily_price_change"`
	OpenInterest                 float64 `json:"open_interest"`
}

type AccountActiveOrdersResponse struct {
	Code       int32    `json:"code"`
	Msg        string   `json:"message"`
	NextCursor string   `json:"next_cursor"`
	Orders     []*Order `json:"orders"`
}

type OrderBooksResponse struct {
	Code       int32       `json:"code"`
	Msg        string      `json:"message"`
	OrderBooks []OrderBook `json:"order_books"`
}

type OrderBook struct {
	Symbol                 string     `json:"symbol"`
	MarketId               int        `json:"market_id"`
	Status                 string     `json:"status"`
	TakerFee               string     `json:"taker_fee"`
	MakerFee               string     `json:"maker_fee"`
	LiquidationFee         string     `json:"liquidation_fee"`
	MinBaseAmount          string     `json:"min_base_amount"`
	MinQuoteAmount         string     `json:"min_quote_amount"`
	SupportedSizeDecimals  uint8      `json:"supported_size_decimals"`
	SupportedPriceDecimals uint8      `json:"supported_price_decimals"`
	SupportedQuoteDecimals uint8      `json:"supported_quote_decimals"`
	MarketType             MarketType `json:"market_type"`
	BaseAssetId            int16      `json:"base_asset_id"`
	QuoteAssetId           int16      `json:"quote_asset_id"`
	OrderQuoteLimit        string     `json:"order_quote_limit"`
}

type OrderBookLevel struct {
	Price string `json:"price"`
	Size  string `json:"size"`
}

type RecentTradesResponse struct {
	Code   int32   `json:"code"`
	Msg    string  `json:"message"`
	Trades []Trade `json:"trades"`
}

type TradesRequest struct {
	MarketID       *int
	MarketType     string
	AccountIndex   *int64
	OrderIndex     *int64
	SortBy         string
	SortDir        string
	Cursor         string
	From           int64
	AskFilter      *int8
	Role           string
	Type           string
	Limit          int64
	Aggregate      bool
	SkipAskOrderID string
	SkipBidOrderID string
}

type Trade struct {
	TradeId                          int64  `json:"trade_id"`
	TradeIdStr                       string `json:"trade_id_str"`
	TxHash                           string `json:"tx_hash"`
	TradeType                        string `json:"type"`
	MarketId                         int    `json:"market_id"`
	Size                             string `json:"size"`
	Price                            string `json:"price"`
	UsdAmount                        string `json:"usd_amount"`
	AskId                            int64  `json:"ask_id"`
	AskIdStr                         string `json:"ask_id_str"`
	BidId                            int64  `json:"bid_id"`
	BidIdStr                         string `json:"bid_id_str"`
	AskClientId                      int64  `json:"ask_client_id"`
	AskClientIdStr                   string `json:"ask_client_id_str"`
	BidClientId                      int64  `json:"bid_client_id"`
	BidClientIdStr                   string `json:"bid_client_id_str"`
	AskAccountId                     int64  `json:"ask_account_id"`
	BidAccountId                     int64  `json:"bid_account_id"`
	IsMakerAsk                       bool   `json:"is_maker_ask"`
	BlockHeight                      int64  `json:"block_height"`
	Timestamp                        int64  `json:"timestamp"`
	TakerFee                         int32  `json:"taker_fee"`
	TakerPositionSizeBefore          string `json:"taker_position_size_before"`
	TakerEntryQuoteBefore            string `json:"taker_entry_quote_before"`
	TakerInitialMarginFractionBefore int    `json:"taker_initial_margin_fraction_before"`
	TakerPositionSignChanged         bool   `json:"taker_position_sign_changed"`
	MakerFee                         int32  `json:"maker_fee"`
	MakerPositionSizeBefore          string `json:"maker_position_size_before"`
	MakerEntryQuoteBefore            string `json:"maker_entry_quote_before"`
	MakerInitialMarginFractionBefore int    `json:"maker_initial_margin_fraction_before"`
	MakerPositionSignChanged         bool   `json:"maker_position_sign_changed"`
	TransactionTime                  int64  `json:"transaction_time"`
	AskAccountPnl                    string `json:"ask_account_pnl"`
	BidAccountPnl                    string `json:"bid_account_pnl"`
}

type AccountResponse struct {
	Code     int32      `json:"code"`
	Msg      string     `json:"message"`
	Accounts []*Account `json:"accounts"`
}

type Account struct {
	Code                     int32        `json:"code"`
	Msg                      string       `json:"message"`
	AccountType              int8         `json:"account_type"`
	Index                    int64        `json:"index"`
	L1Address                string       `json:"l1_address"`
	CancelAllTime            int64        `json:"cancel_all_time"`
	TotalOrderCount          int64        `json:"total_order_count"`
	TotalIsolatedOrderCount  int64        `json:"total_isolated_order_count"`
	PendingOrderCount        int64        `json:"pending_order_count"`
	AvailableBalance         string       `json:"available_balance"`
	Status                   uint8        `json:"status"`
	Collateral               string       `json:"collateral"`
	AccountIndex             int64        `json:"account_index"`
	Name                     string       `json:"name"`
	Description              string       `json:"description"`
	CanInvite                bool         `json:"can_invite"`
	ReferralPointsPercentage string       `json:"referral_points_percentage"`
	Positions                []*Position  `json:"positions"`
	Assets                   []*SpotAsset `json:"assets"`
	TotalAssetValue          string       `json:"total_asset_value"`
	CrossAssetValue          string       `json:"cross_asset_value"`
	PoolInfo                 *PoolInfo    `json:"pool_info"`
	Shares                   []*Share     `json:"shares"`
}

type SpotAsset struct {
	Symbol        string `json:"symbol"`
	AssetId       int    `json:"asset_id"`
	Balance       string `json:"balance"`
	LockedBalance string `json:"locked_balance"`
}

type Position struct {
	MarketId               int    `json:"market_id"`
	Symbol                 string `json:"symbol"`
	InitialMarginFraction  string `json:"initial_margin_fraction"`
	OpenOrderCount         int64  `json:"open_order_count"`
	PendingOrderCount      int64  `json:"pending_order_count"`
	PositionTiedOrderCount int64  `json:"position_tied_order_count"`
	Sign                   int32  `json:"sign"`
	Position               string `json:"position"`
	AvgEntryPrice          string `json:"avg_entry_price"`
	PositionValue          string `json:"position_value"`
	UnrealizedPnl          string `json:"unrealized_pnl"`
	RealizedPnl            string `json:"realized_pnl"`
	LiquidationPrice       string `json:"liquidation_price"`
	TotalFundingPaidOut    string `json:"total_funding_paid_out"`
	MarginMode             int32  `json:"margin_mode"`
	AllocatedMargin        string `json:"allocated_margin"`
	TotalDiscount          string `json:"total_discount"`
}

type PoolInfo struct {
	Status                uint8         `json:"status"`
	OperatorFee           string        `json:"operator_fee"`
	MinOperatorShareRate  string        `json:"min_operator_share_rate"`
	TotalShares           int64         `json:"total_shares"`
	OperatorShares        int64         `json:"operator_shares"`
	AnnualPercentageYield float64       `json:"annual_percentage_yield"`
	DailyReturn           *DailyReturn  `json:"daily_return"`
	SharePrices           []*SharePrice `json:"share_prices"`
}

type DailyReturn struct {
	Timestamp   int64   `json:"timestamp"`
	DailyReturn float64 `json:"daily_return"`
}

type SharePrice struct {
	Timestamp  int64   `json:"timestamp"`
	SharePrice float64 `json:"share_price"`
}

type Share struct {
	PublicPoolIndex int64  `json:"public_pool_index"`
	SharesAmount    int64  `json:"shares_amount"`
	EntryUsdc       string `json:"entry_usdc"`
	PrincipalAmount string `json:"principal_amount"`
	EntryTimestamp  int64  `json:"entry_timestamp"`
}

type AccountInactiveOrdersResponse struct {
	Code       int32    `json:"code"`
	Msg        string   `json:"message"`
	NextCursor string   `json:"next_cursor"`
	Orders     []*Order `json:"orders"`
}

type AccountTxsResponse struct {
	Code int32  `json:"code"`
	Msg  string `json:"message"`
	Txs  []Tx   `json:"txs"`
}

type Tx struct {
	Type             uint8  `json:"type"`
	Hash             string `json:"hash"`
	TxType           uint8  `json:"tx_type"`
	Info             string `json:"info"`
	EventInfo        string `json:"event_info"`
	Status           int64  `json:"status"`
	TransactionIndex int64  `json:"transaction_index"`
	L1Address        string `json:"l1_address"`
	AccountIndex     int64  `json:"account_index"`
	Nonce            int64  `json:"nonce"`
	ExpireAt         int64  `json:"expire_at"`
	BlockHeight      int64  `json:"block_height"`
	QueuedAt         int64  `json:"queued_at"`
	ExecutedAt       int64  `json:"executed_at"`
	SequenceIndex    int64  `json:"sequence_index"`
	ParentHash       string `json:"parent_hash"`
	APIKeyIndex      int    `json:"api_key_index"`
	TransactionTime  int64  `json:"transaction_time"`
}

type PnlResponse struct {
	Code       int32  `json:"code"`
	Msg        string `json:"message"`
	Resolution string `json:"resolution"`
	Pnl        []Pnl  `json:"pnl"`
}

type Pnl struct {
	Timestamp       int64   `json:"timestamp"`
	TradePnl        float64 `json:"trade_pnl"`
	Inflow          float64 `json:"inflow"`
	Outflow         float64 `json:"outflow"`
	PoolPnl         float64 `json:"pool_pnl"`
	PoolInflow      float64 `json:"pool_inflow"`
	PoolOutflow     float64 `json:"pool_outflow"`
	PoolTotalShares float64 `json:"pool_total_shares"`
}

type AccountLimitsResponse struct {
	Code                int32  `json:"code"`
	Msg                 string `json:"message"`
	MaxLlpPercentage    int32  `json:"max_llp_percentage"`
	UserTier            string `json:"user_tier"`
	CanCreatePublicPool bool   `json:"can_create_public_pool"`
	CurrentMakerFeeTick int32  `json:"current_maker_fee_tick"`
	CurrentTakerFeeTick int32  `json:"current_taker_fee_tick"`
}

type AccountTier string

const (
	AccountTierUnknown  AccountTier = ""
	AccountTierStandard AccountTier = "standard"
	AccountTierPremium  AccountTier = "premium"
	AccountTierPlus     AccountTier = "plus"
)

func (r AccountLimitsResponse) AccountTier() AccountTier {
	switch strings.ToLower(strings.TrimSpace(r.UserTier)) {
	case string(AccountTierStandard):
		return AccountTierStandard
	case string(AccountTierPremium):
		return AccountTierPremium
	case string(AccountTierPlus):
		return AccountTierPlus
	default:
		return AccountTierUnknown
	}
}

type AccountMetadataResponse struct {
	Code             int32              `json:"code"`
	Msg              string             `json:"message"`
	AccountMetadatas []*AccountMetadata `json:"account_metadatas"`
}

type AccountMetadata struct {
	AccountIndex             int64  `json:"account_index"`
	Name                     string `json:"name"`
	Description              string `json:"description"`
	CanInvite                bool   `json:"can_invite"`
	ReferralPointsPercentage string `json:"referral_points_percentage"`
}

type ChangeAccountTierResponse struct {
	Code int32  `json:"code"`
	Msg  string `json:"message"`
}

type PositionFundingResponse struct {
	Code             int32              `json:"code"`
	Msg              string             `json:"message"`
	PositionFundings []*PositionFunding `json:"position_fundings"`
}

type PositionFunding struct {
	Timestamp    int64  `json:"timestamp"`
	MarketId     int    `json:"market_id"`
	FundingId    int64  `json:"funding_id"`
	Change       string `json:"change"`
	Rate         string `json:"rate"`
	PositionSize string `json:"position_size"`
	PositionSide string `json:"position_side"`
	Discount     string `json:"discount"`
}

type ModifyOrderResponse struct {
	Code                     int32  `json:"code"`
	Message                  string `json:"message"`
	TxHash                   string `json:"tx_hash"`
	PredictedExecutionTimeMs int64  `json:"predicted_execution_time_ms"`
}

// Market Data types
type OrderBookOrdersResponse struct {
	Code      int32  `json:"code"`
	Msg       string `json:"message"`
	TotalAsks int64  `json:"total_asks"`
	Asks      []Ask  `json:"asks"`
	TotalBids int64  `json:"total_bids"`
	Bids      []Bid  `json:"bids"`
}

type Ask struct {
	OrderIndex          int64  `json:"order_index"`
	OrderId             string `json:"order_id"`
	OwnerAccountIndex   int64  `json:"owner_account_index"`
	InitialBaseAmount   string `json:"initial_base_amount"`
	RemainingBaseAmount string `json:"remaining_base_amount"`
	Price               string `json:"price"`
	OrderExpiry         int64  `json:"order_expiry"`
}

type Bid struct {
	OrderIndex          int64  `json:"order_index"`
	OrderId             string `json:"order_id"`
	OwnerAccountIndex   int64  `json:"owner_account_index"`
	InitialBaseAmount   string `json:"initial_base_amount"`
	RemainingBaseAmount string `json:"remaining_base_amount"`
	Price               string `json:"price"`
	OrderExpiry         int64  `json:"order_expiry"`
}

type TradesResponse struct {
	Code   int32   `json:"code"`
	Msg    string  `json:"message"`
	Trades []Trade `json:"trades"`
}

type FundingRatesResponse struct {
	Code        int32          `json:"code"`
	Msg         string         `json:"message"`
	FundingRate []*FundingRate `json:"funding_rates"`
}

type FundingRate struct {
	MarketId int     `json:"market_id"`
	Exchange string  `json:"exchange"`
	Symbol   string  `json:"symbol"`
	Rate     float64 `json:"rate"`
}

type ExchangeStatsResponse struct {
	Code             int32            `json:"code"`
	Msg              string           `json:"message"`
	Total            int64            `json:"total"`
	OrderBookStats   []*OrderBookStat `json:"order_book_stats"`
	DailyUsdVolume   float64          `json:"daily_usd_volume"`
	DailyTradesCount int64            `json:"daily_trades_count"`
}

type OrderBookStat struct {
	Symbol                string  `json:"symbol"`
	LastTradePrice        float64 `json:"last_trade_price"`
	DailyTradesCount      int64   `json:"daily_trades_count"`
	DailyBaseTokenVolume  float64 `json:"daily_base_token_volume"`
	DailyQuoteTokenVolume float64 `json:"daily_quote_token_volume"`
	DailyPriceChange      float64 `json:"daily_price_change"`
}

type LiquidationsResponse struct {
	Code         int32         `json:"code"`
	Msg          string        `json:"message"`
	Liquidations []Liquidation `json:"liquidations"`
}

type Liquidation struct {
	Id       int64    `json:"id"`
	MarketId int      `json:"market_id"`
	Type     string   `json:"type"`
	Trade    LiqTrade `json:"trade"`
	Info     LiqInfo  `json:"info"`
}

type LiqTrade struct {
	Price    string `json:"price"`
	Size     string `json:"size"`
	TakerFee string `json:"taker_fee"`
	MakerFee string `json:"maker_fee"`
}

type LiqInfo struct {
	Position       Position `json:"position"`
	RiskInfoBefore RiskInfo `json:"risk_info_before"`
	RiskInfoAfter  RiskInfo `json:"risk_info_after"`
}

type RiskInfo struct {
	CrossRiskParameters    CrossRiskParameters    `json:"cross_risk_parameters"`
	IsolatedRiskParameters IsolatedRiskParameters `json:"isolated_risk_parameters"`
}

type CrossRiskParameters struct {
	MarketId             int    `json:"market_id"`
	Collateral           string `json:"collateral"`
	TotalAccountValue    string `json:"total_account_value"`
	InitialMarginReq     string `json:"initial_margin_req"`
	MaintenanceMarginReq string `json:"maintenance_margin_req"`
	CloseOutMarginReq    string `json:"close_out_margin_req"`
}

type IsolatedRiskParameters struct {
	MarketId             int    `json:"market_id"`
	Collateral           string `json:"collateral"`
	TotalAccountValue    string `json:"total_account_value"`
	InitialMarginReq     string `json:"initial_margin_req"`
	MaintenanceMarginReq string `json:"maintenance_margin_req"`
	CloseOutMarginReq    string `json:"close_out_margin_req"`
}

type CandlesticksResponse struct {
	Code         int32         `json:"code"`
	Msg          string        `json:"message"`
	Resolution   string        `json:"r"`
	Candlesticks []Candlestick `json:"c"`
}

type Candlestick struct {
	Timestamp   int64   `json:"t"`
	Open        float64 `json:"o"`
	High        float64 `json:"h"`
	Low         float64 `json:"l"`
	Close       float64 `json:"c"`
	Volume      float64 `json:"v"`
	QuoteVolume float64 `json:"V"`
	LastTradeID int64   `json:"i"`
	CloseRaw    float64 `json:"C,omitempty"`
	HighRaw     float64 `json:"H,omitempty"`
	LowRaw      float64 `json:"L,omitempty"`
	OpenRaw     float64 `json:"O,omitempty"`
}

type FundingHistoryResponse struct {
	Code       int32                `json:"code"`
	Msg        string               `json:"message"`
	Resolution string               `json:"resolution"`
	Fundings   []FundingRateHistory `json:"fundings"`
}

type FundingRateHistory struct {
	Timestamp int64  `json:"timestamp"`
	Value     string `json:"value"`
	Rate      string `json:"rate"`
	Direction string `json:"direction"`
}

type TransferFeeInfoResponse struct {
	Code            int32  `json:"code"`
	Msg             string `json:"message"`
	TransferFeeUSDC int64  `json:"transfer_fee_usdc"`
}

type WithdrawalDelayResponse struct {
	Seconds int64 `json:"seconds"`
}

type ApiKeysResponse struct {
	Code    int32    `json:"code"`
	Msg     string   `json:"message"`
	ApiKeys []ApiKey `json:"api_keys"`
}

type ApiKey struct {
	Index     uint32 `json:"index"`
	PublicKey string `json:"public_key"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"created_at"`
}

type L1MetadataResponse struct {
	L1Address                string `json:"l1_address"`
	CanInvite                bool   `json:"can_invite"`
	ReferralPointsPercentage string `json:"referral_points_percentage"`
}

type L1Metadata struct {
	ChainId   uint32 `json:"chain_id"`
	BlockNum  uint64 `json:"block_num"`
	Timestamp uint64 `json:"timestamp"`
}

type PublicPoolsMetadataResponse struct {
	Code        int32                `json:"code"`
	Msg         string               `json:"message"`
	PublicPools []PublicPoolMetadata `json:"public_pools"`
}

type PublicPoolMetadata struct {
	AccountIndex          int64             `json:"account_index"`
	AccountType           uint8             `json:"account_type"`
	Name                  string            `json:"name"`
	L1Address             string            `json:"l1_address"`
	AnnualPercentageYield float64           `json:"annual_percentage_yield"`
	Status                uint8             `json:"status"`
	OperatorFee           string            `json:"operator_fee"`
	TotalAssetValue       string            `json:"total_asset_value"`
	TotalShares           int64             `json:"total_shares"`
	Assets                []PublicPoolAsset `json:"assets"`
	CreatedAt             int64             `json:"created_at"`
	MasterAccountIndex    int64             `json:"master_account_index"`
	SharpeRatio           float64           `json:"sharpe_ratio"`
	TotalPerpsValue       string            `json:"total_perps_value"`
	TotalSpotValue        string            `json:"total_spot_value"`
}

type PublicPoolAsset struct {
	Symbol        string `json:"symbol"`
	AssetID       int16  `json:"asset_id"`
	Balance       string `json:"balance"`
	LockedBalance string `json:"locked_balance"`
	MarginBalance string `json:"margin_balance"`
	MarginMode    string `json:"margin_mode"`
}

type ReferralPointsResponse struct {
	Code           int32          `json:"code"`
	Msg            string         `json:"message"`
	ReferralPoints ReferralPoints `json:"referral_points"`
}

type ReferralPoints struct {
	Points      string `json:"points"`
	TotalPoints string `json:"total_points"`
	Rank        int64  `json:"rank"`
}

type AccountsByL1AddressResponse struct {
	Code     int32      `json:"code"`
	Msg      string     `json:"message"`
	Accounts []*Account `json:"accounts"`
}

type DepositHistoryResponse struct {
	Code     int32     `json:"code"`
	Msg      string    `json:"message"`
	Deposits []Deposit `json:"deposits"`
}

type Deposit struct {
	TxHash    string `json:"tx_hash"`
	Amount    string `json:"amount"`
	Timestamp int64  `json:"timestamp"`
	Status    string `json:"status"`
}

type WithdrawHistoryResponse struct {
	Code      int32      `json:"code"`
	Msg       string     `json:"message"`
	Withdraws []Withdraw `json:"withdraws"`
}

type Withdraw struct {
	TxHash    string `json:"tx_hash"`
	Amount    string `json:"amount"`
	Timestamp int64  `json:"timestamp"`
	Status    string `json:"status"`
}

type TransferHistoryResponse struct {
	Code      int32      `json:"code"`
	Msg       string     `json:"message"`
	Transfers []Transfer `json:"transfers"`
}

type Transfer struct {
	TxHash    string `json:"tx_hash"`
	Amount    string `json:"amount"`
	Timestamp int64  `json:"timestamp"`
	Status    string `json:"status"`
	ToAccount int64  `json:"to_account"`
}

type SendTxBatchResponse struct {
	Code   int32    `json:"code"`
	Msg    string   `json:"message"`
	Result []string `json:"result"`
}

type AnnouncementResponse struct {
	Code          int32          `json:"code"`
	Msg           string         `json:"message"`
	Announcements []Announcement `json:"announcements"`
}

type Announcement struct {
	Id        int64  `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

type MarketType string

const (
	MarketTypePerp MarketType = "perp"
	MarketTypeSpot MarketType = "spot"
)

// Token structs for API Management
type Token struct {
	TokenID          int64  `json:"token_id"`
	ApiToken         string `json:"api_token"`
	Name             string `json:"name"`
	Expiry           int64  `json:"expiry"`
	AccountIndex     int64  `json:"account_index"`
	SubAccountAccess bool   `json:"sub_account_access"`
	Scopes           string `json:"scopes"`
	Revoked          bool   `json:"revoked"`
}

type TokenListResponse struct {
	Code      int     `json:"code"`
	Msg       string  `json:"message"`
	ApiTokens []Token `json:"api_tokens"`
}

type CreateTokenRequest struct {
	Name             string `json:"name"`
	AccountIndex     int64  `json:"account_index"`
	Expiry           int64  `json:"expiry"` // Milliseconds, 0 for max?
	SubAccountAccess bool   `json:"sub_account_access"`
	Scopes           string `json:"scopes,omitempty"`
}

type CreateTokenResponse struct {
	Code             int    `json:"code"`
	Msg              string `json:"message"`
	TokenId          int64  `json:"token_id"`
	ApiToken         string `json:"api_token"`
	Name             string `json:"name"`
	AccountIndex     int64  `json:"account_index"`
	Expiry           int64  `json:"expiry"`
	SubAccountAccess bool   `json:"sub_account_access"`
	Scopes           string `json:"scopes"`
}

type RevokeTokenRequest struct {
	TokenId      int64 `json:"token_id"`
	AccountIndex int64 `json:"account_index"`
}

type RevokeTokenResponse struct {
	Code    int    `json:"code"`
	Msg     string `json:"message"`
	TokenId int64  `json:"token_id"`
	Revoked bool   `json:"revoked"`
}
