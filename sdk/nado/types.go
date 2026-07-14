package nado

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Common Types

type ApiV1Response struct {
	Status      string          `json:"status"`
	Data        json.RawMessage `json:"data,omitempty"`
	Error       string          `json:"error,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	RequestType string          `json:"request_type,omitempty"`
}

// Request Types

type QueryRequest struct {
	Type      string `json:"type"`
	ProductId *int64 `json:"product_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Depth     int    `json:"depth,omitempty"`
	OrderId   string `json:"order_id,omitempty"`
	Address   string `json:"address,omitempty"`
}

// Order Types

type OrderType string

const (
	OrderTypeLimit           OrderType = "limit"
	OrderTypeMarket          OrderType = "market"
	OrderTypeStopLoss        OrderType = "stop_loss"
	OrderTypeTakeProfit      OrderType = "take_profit"
	OrderTypeStopLossLimit   OrderType = "stop_loss_limit"
	OrderTypeTakeProfitLimit OrderType = "take_profit_limit"
	OrderTypeIOC             OrderType = "ioc"
	OrderTypeFOK             OrderType = "fok"
)

type OrderSide string

const (
	OrderSideBuy  OrderSide = "buy"
	OrderSideSell OrderSide = "sell"
)

type OrderDirection string

const (
	OrderDirectionLong  OrderDirection = "long"
	OrderDirectionShort OrderDirection = "short"
)

type TradingStatus string

const (
	TradingStatusLive           TradingStatus = "live"
	TradingStatusPostOnly       TradingStatus = "post_only"
	TradingStatusReduceOnly     TradingStatus = "reduce_only"
	TradingStatusSoftReduceOnly TradingStatus = "soft_reduce_only"
	TradingStatusNotTradable    TradingStatus = "not_tradable"
)

type SequencerStatus string

const (
	SequencerStatusActive SequencerStatus = "active"
	SequencerStatusFailed SequencerStatus = "failed"
)

// OrderUpdateReason represents the WS order update reason string.
// Nado uses event-based status via the "reason" field in order update messages.
type OrderUpdateReason string

const (
	OrderReasonPlaced    OrderUpdateReason = "placed"
	OrderReasonFilled    OrderUpdateReason = "filled"
	OrderReasonCancelled OrderUpdateReason = "cancelled"
)

type PositionChangeReason string

const (
	PositionReasonDepositCollateral    PositionChangeReason = "deposit_collateral"
	PositionReasonMatchOrders          PositionChangeReason = "match_orders"
	PositionReasonWithdrawCollateral   PositionChangeReason = "withdraw_collateral"
	PositionReasonWithdrawCollateralV2 PositionChangeReason = "withdraw_collateral_v2"
	PositionReasonTransferQuote        PositionChangeReason = "transfer_quote"
	PositionReasonSettlePnL            PositionChangeReason = "settle_pnl"
	PositionReasonMintNLP              PositionChangeReason = "mint_nlp"
	PositionReasonBurnNLP              PositionChangeReason = "burn_nlp"
	PositionReasonLiquidation          PositionChangeReason = "liquidate_subaccount"
)

// Data Structures

type Ticker struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	ProductId int64  `json:"product_id"`
	BidPrice  string `json:"bid_price"`
	AskPrice  string `json:"ask_price"`
	BidQty    string `json:"bid_qty"`
	AskQty    string `json:"ask_qty"`
}

type OrderBook struct {
	Type             string      `json:"type"`
	MinTimestamp     string      `json:"min_timestamp"`
	MaxTimestamp     string      `json:"max_timestamp"`
	LastMaxTimestamp string      `json:"last_max_timestamp"`
	ProductId        int64       `json:"product_id"`
	Bids             [][2]string `json:"bids"` // [price, size]
	Asks             [][2]string `json:"asks"` // [price, size]
}

type Trade struct {
	Type         string `json:"type"`
	Timestamp    string `json:"timestamp"`
	ProductId    int64  `json:"product_id"`
	Price        string `json:"price"`
	TakerQty     string `json:"taker_qty"`
	MakerQty     string `json:"maker_qty"`
	IsTakerBuyer bool   `json:"is_taker_buyer"`
}

type Liquidation struct {
	Type       string  `json:"type"`
	Timestamp  string  `json:"timestamp"`
	ProductIds []int64 `json:"product_ids"`
	Liquidator string  `json:"liquidator"`
	Liquidatee string  `json:"liquidatee"`
	Amount     string  `json:"amount"`
	Price      string  `json:"price"`
}

type AccountInfo struct {
	Subaccount          string               `json:"subaccount"`
	Exists              bool                 `json:"exists"`
	Healths             []Health             `json:"healths"`
	HealthContributions []HealthContribution `json:"health_contributions"`
	SpotCount           int                  `json:"spot_count"`
	PerpCount           int                  `json:"perp_count"`
	SpotBalances        []Balance            `json:"spot_balances"`
	PerpBalances        []Balance            `json:"perp_balances"`
	TakerFeeRatesX18    []string             `json:"taker_fee_rates_x18"`
	MakerFeeRatesX18    []string             `json:"maker_fee_rates_x18"`
	FeeTier             int                  `json:"fee_tier"`
	PreState            *SubaccountPreState  `json:"pre_state,omitempty"`
}

type AccountSnapshot struct {
	Account    AccountInfo `json:"account"`
	ReceivedAt time.Time   `json:"received_at"`
}

func (s AccountSnapshot) FreshAt(now time.Time, maxAge time.Duration) bool {
	if s.ReceivedAt.IsZero() || maxAge <= 0 || now.Before(s.ReceivedAt) {
		return false
	}
	return now.Sub(s.ReceivedAt) <= maxAge
}

type SubaccountPreState struct {
	Healths             []Health             `json:"healths"`
	HealthContributions []HealthContribution `json:"health_contributions"`
	SpotBalances        []Balance            `json:"spot_balances"`
	PerpBalances        []Balance            `json:"perp_balances"`
}

type Health struct {
	Assets      string `json:"assets"`
	Liabilities string `json:"liabilities"`
	Health      string `json:"health"`
}

type HealthContribution [3]string

type Balance struct {
	ProductID int64 `json:"product_id"`
	Balance   struct {
		Amount                string  `json:"amount"`
		VQuoteBalance         *string `json:"v_quote_balance,omitempty"`
		LastCumulativeFunding *string `json:"last_cumulative_funding_x18,omitempty"`
	} `json:"balance"`
}

type AccountProductOrders struct {
	Sender    string  `json:"sender,omitempty"`
	ProductID int64   `json:"product_id"`
	Orders    []Order `json:"orders"`
}

type GetAccountMultiProductsOrders struct {
	Sender        string                 `json:"sender"`
	ProductOrders []AccountProductOrders `json:"product_orders"`
}

type Order struct {
	ProductID      int64  `json:"product_id"`
	Sender         string `json:"sender"`
	PriceX18       string `json:"price_x18"`
	Amount         string `json:"amount"`
	Expiration     string `json:"expiration"`
	Nonce          string `json:"nonce"`
	UnfilledAmount string `json:"unfilled_amount"`
	Digest         string `json:"digest"`
	PlacedAt       int64  `json:"placed_at"`
	Appendix       string `json:"appendix"`
	OrderType      string `json:"order_type"`
}

type Product struct {
	ProductId  int64  `json:"product_id"`
	Symbol     string `json:"symbol"`
	BaseAsset  string `json:"base_asset"`
	QuoteAsset string `json:"quote_asset"`
	Decimals   int    `json:"decimals"`
	MinSize    string `json:"min_size"`
	TickSize   string `json:"tick_size"`
}

type FeeRate struct {
	MakerFeeRate string `json:"maker_fee_rate"`
	TakerFeeRate string `json:"taker_fee_rate"`
}

// EIP712 Interaction Types

type Sender struct {
	Address    string // hex string
	SubAccount string // hex string
}

type PlaceOrderRequest struct {
	PlaceOrder PlaceOrder `json:"place_order"`
}

type PlaceOrder struct {
	ProductID int              `json:"product_id"`
	Order     PlaceOrderParams `json:"order"`
	Signature string           `json:"signature"`
	ID        int64            `json:"id"`
}

type PlaceOrderResponse struct {
	Digest string `json:"digest"`
}

type PlaceOrderParams struct {
	Sender     string  `json:"sender"`
	PriceX18   float64 `json:"price_x18"`
	Amount     float64 `json:"amount"`
	Expiration string  `json:"expiration"`
	Nonce      string  `json:"nonce"`
	Appendix   string  `json:"appendix"`
}

type TxOrder struct {
	Sender     string `json:"sender"`     // bytes32
	ProductId  uint32 `json:"productId"`  // Not signed directly in EIP-712 Order struct but used for contract address derivation
	Amount     string `json:"amount"`     // int128 -> string
	PriceX18   string `json:"priceX18"`   // int128 -> string
	Nonce      string `json:"nonce"`      // uint64 -> string
	Expiration string `json:"expiration"` // uint64 -> string
	Appendix   string `json:"appendix"`   // uint128 -> string
}

// Trigger Types for Appendix
const (
	AppendixTriggerTypeNone              = 0
	AppendixTriggerTypePrice             = 1
	AppendixTriggerTypeTWAP              = 2
	AppendixTriggerTypeTWAPCustomAmounts = 3
)

type TxCancelOrder struct {
	Sender    string `json:"sender"` // bytes32
	ProductId uint32 `json:"productId"`
	Nonce     string `json:"nonce"` // uint64 -> string
}

type TxCancelProductOrders struct {
	Sender     string  `json:"sender"`     // bytes32
	ProductIds []int64 `json:"productIds"` // uint32[]
	Nonce      string  `json:"nonce"`      // uint64 -> string
}

type TxCancelOrders struct {
	Sender     string   `json:"sender"`     // bytes32
	ProductIds []int64  `json:"productIds"` // int64
	Digests    []string `json:"digests"`    // bytes32[]
	Nonce      string   `json:"nonce"`      // uint64 -> string
}

type TxStreamAuth struct {
	Sender     string `json:"sender"`     // bytes32
	Expiration string `json:"expiration"` // uint64 -> string
}

// Internal wrapper for JSON marshaling of execute payloads
type ExecTransaction[T any] struct {
	Tx        T       `json:"tx"`
	Signature string  `json:"signature"`
	Digest    *string `json:"digest"` // Nullable
	ID        int64   `json:"id,omitempty"`
}

// V2 Types

type AssetV2 struct {
	ProductId   int64    `json:"product_id"`
	TickerId    string   `json:"ticker_id"`
	MarketType  string   `json:"market_type"` // spot, perp
	Name        string   `json:"name"`
	Symbol      string   `json:"symbol"`
	TakerFee    *float64 `json:"taker_fee,omitempty"`
	MakerFee    *float64 `json:"maker_fee,omitempty"`
	CanWithdraw bool     `json:"can_withdraw"`
	CanDeposit  bool     `json:"can_deposit"`
}

type PairV2 struct {
	ProductId int64  `json:"product_id"`
	TickerId  string `json:"ticker_id"`
	Base      string `json:"base"`
	Quote     string `json:"quote"`
}

type AprV2 struct {
	Name       string  `json:"name"`
	Symbol     string  `json:"symbol"`
	ProductID  string  `json:"product_id"`
	DepositApr float64 `json:"deposit_apr"`
	BorrowApr  float64 `json:"borrow_apr"`
	Tvl        float64 `json:"tvl"`
}

type OrderBookV2 struct {
	ProductId int64        `json:"product_id"`
	TickerId  string       `json:"ticker_id"`
	Bids      [][2]float64 `json:"bids"`
	Asks      [][2]float64 `json:"asks"`
	Timestamp int64        `json:"timestamp"`
}

type TickerV2Map map[string]TickerV2
type TickerV2 struct {
	ProductID             int     `json:"product_id"`
	TickerID              string  `json:"ticker_id"`
	BaseCurrency          string  `json:"base_currency"`
	QuoteCurrency         string  `json:"quote_currency"`
	LastPrice             float64 `json:"last_price"`
	BaseVolume            float64 `json:"base_volume"`
	QuoteVolume           float64 `json:"quote_volume"`
	PriceChangePercent24H float64 `json:"price_change_percent_24h"`
}

type ContractV2Map map[string]ContractV2 // ticker_id -> Contract info object
type ContractV2 struct {
	ProductID                int      `json:"product_id"`
	TickerID                 string   `json:"ticker_id"`
	BaseCurrency             string   `json:"base_currency"`
	QuoteCurrency            string   `json:"quote_currency"`
	LastPrice                float64  `json:"last_price"`
	BaseVolume               float64  `json:"base_volume"`
	QuoteVolume              float64  `json:"quote_volume"`
	ProductType              string   `json:"product_type"`
	ContractPrice            float64  `json:"contract_price"`
	ContractPriceCurrency    string   `json:"contract_price_currency"`
	OpenInterest             *float64 `json:"open_interest"`
	OpenInterestUsd          *float64 `json:"open_interest_usd"`
	IndexPrice               *float64 `json:"index_price"`
	MarkPrice                *float64 `json:"mark_price"`
	FundingRate              *float64 `json:"funding_rate"`
	NextFundingRateTimestamp *int64   `json:"next_funding_rate_timestamp"`
	PriceChangePercent24H    float64  `json:"price_change_percent_24h"`
}

type ContractV1 struct {
	ChainID         string `json:"chain_id"`
	EndpointAddress string `json:"endpoint_addr"`
}

var (
	ErrNadoContractProfileMismatch   = errors.New("nado: contract profile mismatch")
	ErrNadoContractEndpointMalformed = errors.New("nado: contract endpoint malformed")
	ErrNadoDiscoveryMissingProduct0  = errors.New("nado: discovery missing product 0")
	ErrNadoDiscoveryInactiveProduct  = errors.New("nado: discovery inactive product")
	ErrNadoDiscoveryUnknownProduct   = errors.New("nado: discovery unknown product")
	ErrNadoDiscoveryProductMismatch  = errors.New("nado: discovery product mismatch")
	ErrNadoSequencerInactive         = errors.New("nado: sequencer is not active")
)

func (c *Client) ValidateContractV1(contract ContractV1) error {
	chainID, err := strconv.ParseInt(contract.ChainID, 10, 64)
	if err != nil || chainID != c.profile.ChainID() {
		return fmt.Errorf("%w: chain_id %q does not match profile chain %d", ErrNadoContractProfileMismatch, contract.ChainID, c.profile.ChainID())
	}
	if !isNadoHexAddress(contract.EndpointAddress) {
		return fmt.Errorf("%w: endpoint_addr %q", ErrNadoContractEndpointMalformed, contract.EndpointAddress)
	}
	return nil
}

func isNadoHexAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	if strings.Trim(value[2:], "0") == "" {
		return false
	}
	for _, r := range value[2:] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

type AllProductsResponse struct {
	SpotProducts []SpotProduct `json:"spot_products"`
	PerpProducts []PerpProduct `json:"perp_products"`
}

type ProductRisk struct {
	LongWeightInitialX18      string `json:"long_weight_initial_x18"`
	ShortWeightInitialX18     string `json:"short_weight_initial_x18"`
	LongWeightMaintenanceX18  string `json:"long_weight_maintenance_x18"`
	ShortWeightMaintenanceX18 string `json:"short_weight_maintenance_x18"`
	PriceX18                  string `json:"price_x18"`
	LargePositionPenaltyX18   string `json:"large_position_penalty_x18,omitempty"`
}

type ProductBookInfo struct {
	SizeIncrement     string `json:"size_increment"`
	PriceIncrementX18 string `json:"price_increment_x18"`
	MinSize           string `json:"min_size"`
	CollectedFees     string `json:"collected_fees"`
}

type SpotProduct struct {
	ProductID      int64             `json:"product_id"`
	OraclePriceX18 string            `json:"oracle_price_x18,omitempty"`
	Risk           ProductRisk       `json:"risk"`
	Config         SpotProductConfig `json:"config"`
	State          SpotProductState  `json:"state"`
	BookInfo       ProductBookInfo   `json:"book_info"`
}

type SpotProductConfig struct {
	Token                     string `json:"token"`
	InterestInflectionUtilX18 string `json:"interest_inflection_util_x18"`
	InterestFloorX18          string `json:"interest_floor_x18"`
	InterestSmallCapX18       string `json:"interest_small_cap_x18"`
	InterestLargeCapX18       string `json:"interest_large_cap_x18"`
	WithdrawFeeX18            string `json:"withdraw_fee_x18"`
	MinDepositRateX18         string `json:"min_deposit_rate_x18"`
}

type SpotProductState struct {
	CumulativeDepositsMultiplierX18 string `json:"cumulative_deposits_multiplier_x18"`
	CumulativeBorrowsMultiplierX18  string `json:"cumulative_borrows_multiplier_x18"`
	TotalDepositsNormalized         string `json:"total_deposits_normalized"`
	TotalBorrowsNormalized          string `json:"total_borrows_normalized"`
}

type PerpProduct struct {
	ProductID      int64            `json:"product_id"`
	OraclePriceX18 string           `json:"oracle_price_x18,omitempty"`
	IndexPriceX18  string           `json:"index_price_x18,omitempty"`
	Risk           ProductRisk      `json:"risk"`
	State          PerpProductState `json:"state"`
	BookInfo       ProductBookInfo  `json:"book_info"`
}

type PerpProductState struct {
	CumulativeFundingLongX18  string `json:"cumulative_funding_long_x18"`
	CumulativeFundingShortX18 string `json:"cumulative_funding_short_x18"`
	AvailableSettle           string `json:"available_settle"`
	OpenInterest              string `json:"open_interest"`
}

type SymbolsRequest struct {
	ProductType MarketType `json:"product_type,omitempty"`
	ProductIDs  []int64    `json:"product_ids,omitempty"`
}

type MaxOrderSizeRequest struct {
	ProductID    int64          `json:"product_id"`
	Sender       string         `json:"sender"`
	PriceX18     string         `json:"price_x18"`
	AvgPriceX18  string         `json:"avg_price_x18,omitempty"`
	Direction    OrderDirection `json:"direction"`
	SpotLeverage *bool          `json:"spot_leverage,omitempty"`
	ReduceOnly   *bool          `json:"reduce_only,omitempty"`
	Isolated     *bool          `json:"isolated,omitempty"`
	BorrowMargin *bool          `json:"borrow_margin,omitempty"`
}

type MaxOrderSizeResponse struct {
	MaxOrderSize string `json:"max_order_size"`
}

type SubaccountInfoRequest struct {
	Subaccount string                    `json:"subaccount"`
	Txns       []SubaccountSimulationTxn `json:"txns,omitempty"`
	PreState   *bool                     `json:"pre_state,omitempty"`
}

type SubaccountSimulationTxn struct {
	ApplyDelta SubaccountSimulationDelta `json:"apply_delta"`
}

type SubaccountSimulationDelta struct {
	ProductID   int64  `json:"product_id"`
	Subaccount  string `json:"subaccount"`
	AmountDelta string `json:"amount_delta"`
	VQuoteDelta string `json:"v_quote_delta"`
}

type TradeV2 struct {
	ProductID   int     `json:"product_id"`
	TickerID    string  `json:"ticker_id"`
	TradeID     int64   `json:"trade_id"`
	Price       float64 `json:"price"`
	BaseFilled  float64 `json:"base_filled"`
	QuoteFilled float64 `json:"quote_filled"`
	Timestamp   int64   `json:"timestamp"`
	TradeType   string  `json:"trade_type"`
}

type MarketPrice struct {
	ProductID int    `json:"product_id"`
	BidX18    string `json:"bid_x18"`
	AskX18    string `json:"ask_x18"`
}

type MarketPricesResponse struct {
	MarketPrices []MarketPrice `json:"market_prices"`
}

type MarketPricesReq struct {
	Type       string `json:"type"`
	ProductIds []int  `json:"product_ids"`
}

type Nonce struct {
	TxNonce    string `json:"tx_nonce"`
	OrderNonce string `json:"order_nonce"`
}

type MarketLiquidity struct {
	ProductID int64       `json:"product_id"`
	Timestamp string      `json:"timestamp"`
	Bids      [][2]string `json:"bids"`
	Asks      [][2]string `json:"asks"`
}

type SymbolsInfo struct {
	Symbols map[string]Symbol `json:"symbols"`
}
type Symbol struct {
	Type                     string        `json:"type"`
	ProductID                int           `json:"product_id"`
	Symbol                   string        `json:"symbol"`
	PriceIncrementX18        string        `json:"price_increment_x18"`
	SizeIncrement            string        `json:"size_increment"`
	MinSize                  string        `json:"min_size"`
	MakerFeeRateX18          string        `json:"maker_fee_rate_x18"`
	TakerFeeRateX18          string        `json:"taker_fee_rate_x18"`
	LongWeightInitialX18     string        `json:"long_weight_initial_x18"`
	LongWeightMaintenanceX18 string        `json:"long_weight_maintenance_x18"`
	MaxOpenInterestX18       *string       `json:"max_open_interest_x18,omitempty"`
	IsolatedOnly             bool          `json:"isolated_only,omitempty"`
	TradingStatus            TradingStatus `json:"trading_status,omitempty"`
}

func ValidateNadoProductDiscovery(products AllProductsResponse, symbols SymbolsInfo) error {
	productsByType := make(map[string]ProductBookInfo, len(products.SpotProducts)+len(products.PerpProducts))
	foundProduct0 := 0
	for _, product := range products.SpotProducts {
		if product.ProductID == 0 {
			foundProduct0++
		}
		key := productDiscoveryKey(product.ProductID, string(MarketTypeSpot))
		if _, exists := productsByType[key]; exists {
			return fmt.Errorf("%w: duplicate product_id %d type %s", ErrNadoDiscoveryProductMismatch, product.ProductID, MarketTypeSpot)
		}
		productsByType[key] = product.BookInfo
	}
	for _, product := range products.PerpProducts {
		key := productDiscoveryKey(product.ProductID, string(MarketTypePerp))
		if _, exists := productsByType[key]; exists {
			return fmt.Errorf("%w: duplicate product_id %d type %s", ErrNadoDiscoveryProductMismatch, product.ProductID, MarketTypePerp)
		}
		productsByType[key] = product.BookInfo
	}
	if foundProduct0 != 1 {
		return fmt.Errorf("%w: all_products spot_products does not include product_id 0", ErrNadoDiscoveryMissingProduct0)
	}

	seen := make(map[string]struct{}, len(symbols.Symbols))
	for _, symbol := range symbols.Symbols {
		if !isKnownNadoTradingStatus(symbol.TradingStatus) {
			return fmt.Errorf("%w: product_id %d status %q", ErrNadoDiscoveryInactiveProduct, symbol.ProductID, symbol.TradingStatus)
		}
		if symbol.ProductID == 0 {
			if symbol.Type != string(MarketTypeSpot) || symbol.Symbol != "USDT0" {
				return fmt.Errorf("%w: product 0 symbol is inconsistent", ErrNadoDiscoveryProductMismatch)
			}
			continue
		}
		key := productDiscoveryKey(int64(symbol.ProductID), symbol.Type)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("%w: duplicate symbol for product_id %d type %s", ErrNadoDiscoveryProductMismatch, symbol.ProductID, symbol.Type)
		}
		seen[key] = struct{}{}
		book, ok := productsByType[key]
		if !ok {
			return fmt.Errorf("%w: product_id %d type %s", ErrNadoDiscoveryUnknownProduct, symbol.ProductID, symbol.Type)
		}
		if err := validateDiscoveredProduct(int64(symbol.ProductID), symbol.Type, book, symbol); err != nil {
			return err
		}
	}
	return nil
}

func validateDiscoveredProduct(productID int64, productType string, book ProductBookInfo, symbol Symbol) error {
	if int64(symbol.ProductID) != productID ||
		symbol.Type != productType ||
		symbol.PriceIncrementX18 != book.PriceIncrementX18 ||
		symbol.SizeIncrement != book.SizeIncrement ||
		symbol.MinSize != book.MinSize {
		return fmt.Errorf("%w: product_id %d type %s", ErrNadoDiscoveryProductMismatch, productID, productType)
	}
	return nil
}

type DiscoveredProduct struct {
	ProductID   int64
	ProductType MarketType
	Symbol      Symbol
	BookInfo    ProductBookInfo
}

func productDiscoveryKey(productID int64, productType string) string {
	return fmt.Sprintf("%d:%s", productID, productType)
}

func isKnownNadoTradingStatus(status TradingStatus) bool {
	switch status {
	case TradingStatusLive, TradingStatusPostOnly, TradingStatusReduceOnly, TradingStatusSoftReduceOnly, TradingStatusNotTradable:
		return true
	default:
		return false
	}
}

type FeeRates struct {
	MakerFeeRateX18         []string `json:"maker_fee_rates_x18"`
	TakerFeeRateX18         []string `json:"taker_fee_rates_x18"`
	LiquidationSequencerFee string   `json:"liquidation_sequencer_fee"`
	HealthCheckSequencerFee string   `json:"health_check_sequencer_fee"`
	TakerSequencerFee       string   `json:"taker_sequencer_fee"`
	WithdrawSequencerFees   []string `json:"withdraw_sequencer_fees"`
	FeeTier                 int      `json:"fee_tier"`
}

type CancelOrdersResponse struct {
	CancelledOrders []Order `json:"cancelled_orders"`
}

type CancelProductOrdersResponse struct {
	CancelledOrders []Order `json:"cancelled_orders"`
}

type CandlestickRequest struct {
	Candlesticks Candlesticks `json:"candlesticks"`
}

type Candlesticks struct {
	ProductID   int64 `json:"product_id"`
	Granularity int64 `json:"granularity"`
	MaxTime     int64 `json:"max_time,omitempty"`
	Limit       int   `json:"limit,omitempty"`
}

type CandlestickResponse struct {
	Candlesticks []ArchiveCandlestick `json:"candlesticks"`
}

// ArchiveCandlestick represents a single candle data point from Archive Indexer
type ArchiveCandlestick struct {
	ProductID     int64  `json:"product_id"`
	Granularity   int64  `json:"granularity"`
	SubmissionIdx string `json:"submission_idx"`
	Timestamp     string `json:"timestamp"`
	OpenX18       string `json:"open_x18"`
	HighX18       string `json:"high_x18"`
	LowX18        string `json:"low_x18"`
	CloseX18      string `json:"close_x18"`
	Volume        string `json:"volume"` // x18
}

type MarketType string

const (
	MarketTypeSpot MarketType = "spot"
	MarketTypePerp MarketType = "perp"
)

// FundingRateResponse represents the response from Archive funding rate query
type FundingRateResponse struct {
	ProductID      int64  `json:"product_id"`
	FundingRateX18 string `json:"funding_rate_x18"` // 24hr funding rate * 10^18
	UpdateTime     string `json:"update_time"`      // Epoch seconds
}

// PerpPriceResponse is the archive price query response for one perpetual.
// Prices retain the exchange's x18 integer encoding and source update time.
type PerpPriceResponse struct {
	ProductID     int64  `json:"product_id"`
	IndexPriceX18 string `json:"index_price_x18"`
	MarkPriceX18  string `json:"mark_price_x18"`
	UpdateTime    string `json:"update_time"` // Epoch seconds
}

// OraclePriceResponse is one product entry from the archive oracle_price query.
type OraclePriceResponse struct {
	ProductID      int64  `json:"product_id"`
	OraclePriceX18 string `json:"oracle_price_x18"`
	UpdateTime     string `json:"update_time"` // Epoch seconds
}

type oraclePricesResponse struct {
	Prices []OraclePriceResponse `json:"prices"`
}

// Archive Types

type ArchiveSnapshotRequest struct {
	AccountSnapshots AccountSnapshotsQuery `json:"account_snapshots"`
}

type AccountSnapshotsQuery struct {
	Subaccounts []string `json:"subaccounts"`
	Timestamps  []int64  `json:"timestamps"`
}

type ArchiveSnapshotResponse struct {
	Snapshots []Snapshot `json:"snapshots"`
}

type Snapshot struct {
	Subaccount string            `json:"subaccount"`
	Timestamp  int64             `json:"timestamp"`
	Balances   []SnapshotBalance `json:"products"` // Use SnapshotProduct
}

// Reuse SnapshotProduct as Balances item
type SnapshotBalance struct { // Alias to SnapshotProduct or explicit
	ProductID            int64   `json:"product_id"`
	Balance              Balance `json:"balance"`
	NetFundingCumulative string  `json:"net_funding_cumulative_x18"`
}

type ArchiveMatchesRequest struct {
	Matches MatchesQuery `json:"matches"`
}

type MatchesQuery struct {
	Subaccounts []string `json:"subaccounts"`
	ProductIds  []int64  `json:"product_ids,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	MaxTime     int64    `json:"max_time,omitempty"`
}

type ArchiveMatchesResponse struct {
	Matches []Match `json:"matches"`
	Txs     []Tx    `json:"txs"`
}

type ArchiveOrdersRequest struct {
	Orders OrdersByDigestsQuery `json:"orders"`
}

type OrdersByDigestsQuery struct {
	Digests []string `json:"digests"`
	Limit   int      `json:"limit"`
}

type ArchiveOrdersResponse struct {
	Orders []ArchiveOrder `json:"orders"`
}

type ArchiveOrder struct {
	Digest                string `json:"digest"`
	Subaccount            string `json:"subaccount"`
	ProductID             int64  `json:"product_id"`
	SubmissionIdx         string `json:"submission_idx"`
	LastFillSubmissionIdx string `json:"last_fill_submission_idx"`
	Amount                string `json:"amount"`
	PriceX18              string `json:"price_x18"`
	BaseFilled            string `json:"base_filled"`
	QuoteFilled           string `json:"quote_filled"`
	Fee                   string `json:"fee"`
	BuilderFee            string `json:"builder_fee"`
	ClosedAmount          string `json:"closed_amount"`
	RealizedPnL           string `json:"realized_pnl"`
	ClosedNetEntry        string `json:"closed_net_entry"`
	ClosedMargin          string `json:"closed_margin"`
	FirstFillTimestamp    string `json:"first_fill_timestamp"`
	LastFillTimestamp     string `json:"last_fill_timestamp"`
	Expiration            string `json:"expiration"`
	Nonce                 string `json:"nonce"`
	Appendix              string `json:"appendix"`
}

type Match struct {
	Digest             string            `json:"digest"`
	Order              MatchOrder        `json:"order"`
	BaseFilled         string            `json:"base_filled"` // User sample: "base_filled" (no x18 suffix in key, but value is x18)
	QuoteFilled        string            `json:"quote_filled"`
	Fee                string            `json:"fee"`
	SequencerFee       string            `json:"sequencer_fee"`
	SubmissionIdx      string            `json:"submission_idx"`
	Timestamp          string            `json:"timestamp"`
	PreBalance         MatchBalanceOuter `json:"pre_balance"`
	PostBalance        MatchBalanceOuter `json:"post_balance"`
	NetEntryUnrealized string            `json:"net_entry_unrealized"` // Cost basis for current position
	NetEntryCumulative string            `json:"net_entry_cumulative"` // Cumulative cost basis
	ClosedAmount       string            `json:"closed_amount"`        // Amount closed in this match
	RealizedPnL        string            `json:"realized_pnl"`         // Realized PnL from closing
}

type MatchBalanceOuter struct {
	Base MatchBalanceBase `json:"base"`
}

type MatchBalanceBase struct {
	Perp *MatchBalancePerp `json:"perp,omitempty"`
	Spot *MatchBalanceSpot `json:"spot,omitempty"`
}

type MatchBalancePerp struct {
	ProductID int64 `json:"product_id"`
	Balance   struct {
		Amount                string `json:"amount"`
		VQuoteBalance         string `json:"v_quote_balance"`
		LastCumulativeFunding string `json:"last_cumulative_funding_x18"`
	} `json:"balance"`
}

type MatchBalanceSpot struct {
	ProductID int64 `json:"product_id"`
	Balance   struct {
		Amount string `json:"amount"`
	} `json:"balance"`
}

type MatchOrder struct {
	Sender     string `json:"sender"`
	PriceX18   string `json:"priceX18"`
	Amount     string `json:"amount"`
	Expiration string `json:"expiration"`
	Nonce      string `json:"nonce"`
	Appendix   string `json:"appendix"`
}

type Tx struct {
	SubmissionIdx string `json:"submission_idx"`
	Timestamp     string `json:"timestamp"`
	TxInfo        TxInfo `json:"tx"`
}

type TxInfo struct {
	MatchOrders MatchOrders `json:"match_orders"`
}

type MatchOrders struct {
	ProductId int     `json:"product_id"`
	Taker     TxTaker `json:"taker"`
	Maker     TxMaker `json:"maker"`
}

type TxTaker struct {
	Order     TxMatchOrder `json:"order"`
	Signature string       `json:"signature"`
}
type TxMaker struct {
	Order     TxMatchOrder `json:"order"`
	Signature string       `json:"signature"`
}
type TxMatchOrder struct {
	Sender     string `json:"sender"`
	PriceX18   string `json:"price_x18"`
	Amount     string `json:"amount"`
	Expiration uint64 `json:"expiration"` // uint64: can be max uint64 (18446744073709551615)
	Nonce      uint64 `json:"nonce"`      // uint64: same range as expiration
	Appendix   string `json:"appendix"`
}
