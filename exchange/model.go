package exchange

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Venue string

const (
	VenueBinance     Venue = "binance"
	VenueOKX         Venue = "okx"
	VenueLighter     Venue = "lighter"
	VenueHyperliquid Venue = "hyperliquid"
	VenueBybit       Venue = "bybit"
	VenueBitget      Venue = "bitget"
	VenueGate        Venue = "gate"
	VenueAster       Venue = "aster"
	VenueNado        Venue = "nado"
)

type Product string

const (
	ProductSpot Product = "spot"
	ProductPerp Product = "perp"
)

type Side string

const (
	SideBuy  Side = "buy"
	SideSell Side = "sell"
)

type OrderType string

const (
	OrderTypeMarket OrderType = "market"
	OrderTypeLimit  OrderType = "limit"
)

type LimitPolicy string

const (
	LimitPolicyResting  LimitPolicy = "resting"
	LimitPolicyIOC      LimitPolicy = "ioc"
	LimitPolicyPostOnly LimitPolicy = "post_only"
)

type Liquidity string

const (
	LiquidityMaker Liquidity = "maker"
	LiquidityTaker Liquidity = "taker"
)

type OptionalDecimal struct {
	Value decimal.Decimal `json:"value"`
	Valid bool            `json:"valid"`
}

type OptionalInt struct {
	Value int64 `json:"value"`
	Valid bool  `json:"valid"`
}

// Environment names the endpoint family a venue client talks to. There is one:
// every venue's demo and testnet endpoints were removed because no two of them
// agreed on what a demo was, and maintaining nine dialects of "not real" cost
// more than it proved. The type is kept rather than the field being deleted
// because it is what a stored account's environment is validated against — a
// row still asking for demo has to be rejected, and something has to reject it.
type Environment string

const EnvironmentLive Environment = "live"

// AssetClass is the venue-published economic class of an instrument's
// underlying. Unknown is a first-class value: venues that do not publish a
// classification must not be silently treated as crypto.
type AssetClass string

const (
	AssetClassUnknown   AssetClass = "unknown"
	AssetClassCrypto    AssetClass = "crypto"
	AssetClassEquity    AssetClass = "equity"
	AssetClassCommodity AssetClass = "commodity"
	AssetClassIndex     AssetClass = "index"
	AssetClassFX        AssetClass = "fx"
	AssetClassRates     AssetClass = "rates"
	AssetClassPreIPO    AssetClass = "pre_ipo"
	AssetClassRWAOther  AssetClass = "rwa_other"
)

// MarketFamily describes how a market is hosted. It is deliberately separate
// from AssetClass: a builder-deployed perpetual can reference either crypto or
// a traditional asset, and a venue-native perpetual can do the same.
type MarketFamily string

const (
	MarketFamilySpot             MarketFamily = "spot"
	MarketFamilyVenuePerpetual   MarketFamily = "venue_perpetual"
	MarketFamilyBuilderPerpetual MarketFamily = "builder_perpetual"
)

// InstrumentMetadata carries only venue-originated classification facts.
// RawAssetType and RawCategories preserve the source vocabulary so a future
// taxonomy migration can be replayed without re-querying historical catalogs.
type InstrumentMetadata struct {
	MarketFamily         MarketFamily         `json:"market_family"`
	AssetClass           AssetClass           `json:"asset_class"`
	ClassificationSource string               `json:"classification_source,omitempty"`
	RawAssetType         string               `json:"raw_asset_type,omitempty"`
	RawCategories        InstrumentCategories `json:"raw_categories,omitempty"`
}

// InstrumentCategories stores a venue's category list in canonical JSON while
// keeping Instrument comparable. Comparability is part of the existing model's
// utility in tests and exact-value caches; a raw []string here would silently
// remove it from the whole Instrument type. MarshalJSON preserves the public
// array shape, and Values is the only accessor adapters and projections need.
type InstrumentCategories string

func NewInstrumentCategories(values []string) InstrumentCategories {
	if len(values) == 0 {
		return ""
	}
	payload, _ := json.Marshal(values)
	return InstrumentCategories(payload)
}

func (categories InstrumentCategories) Values() []string {
	values, _ := categories.values()
	return values
}

func (categories InstrumentCategories) values() ([]string, error) {
	if categories == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(categories), &values); err != nil || values == nil {
		return nil, invalidRequest("instrument categories are malformed")
	}
	return values, nil
}

func (categories InstrumentCategories) MarshalJSON() ([]byte, error) {
	if categories == "" {
		return []byte("[]"), nil
	}
	if _, err := categories.values(); err != nil {
		return nil, err
	}
	return []byte(categories), nil
}

func (categories *InstrumentCategories) UnmarshalJSON(payload []byte) error {
	var values []string
	if err := json.Unmarshal(payload, &values); err != nil {
		return err
	}
	*categories = NewInstrumentCategories(values)
	return nil
}

type Instrument struct {
	Identity InstrumentIdentity `json:"identity"`
	Rules    InstrumentRules    `json:"rules"`
	Encoding InstrumentEncoding `json:"encoding"`
	Metadata InstrumentMetadata `json:"metadata"`
	Revision string             `json:"revision,omitempty"`
}

type InstrumentIdentity struct {
	Venue        Venue   `json:"venue"`
	Product      Product `json:"product"`
	Symbol       string  `json:"symbol"`
	BaseAsset    string  `json:"base_asset"`
	QuoteAsset   string  `json:"quote_asset"`
	SettleAsset  string  `json:"settle_asset,omitempty"`
	NativeSymbol string  `json:"native_symbol"`
	NativeID     string  `json:"native_id,omitempty"`
}

type InstrumentRules struct {
	PriceIncrement    decimal.Decimal `json:"price_increment"`
	QuantityIncrement decimal.Decimal `json:"quantity_increment"`
	MinQuantity       decimal.Decimal `json:"min_quantity"`
	MinNotional       OptionalDecimal `json:"min_notional"`
}

type InstrumentEncoding struct {
	ContractMultiplier OptionalDecimal `json:"contract_multiplier"`
	PriceScale         OptionalInt     `json:"price_scale"`
	QuantityScale      OptionalInt     `json:"quantity_scale"`
	QuoteScale         OptionalInt     `json:"quote_scale"`
}

func (instrument Instrument) Validate() error {
	identity := instrument.Identity
	if identity.Venue == "" || identity.Product == "" || strings.TrimSpace(identity.Symbol) == "" ||
		strings.TrimSpace(identity.NativeSymbol) == "" || strings.TrimSpace(identity.BaseAsset) == "" ||
		strings.TrimSpace(identity.QuoteAsset) == "" {
		return invalidRequest("instrument identity is incomplete")
	}
	if !knownVenue(identity.Venue) {
		return invalidRequest("instrument venue is unknown")
	}
	if strings.TrimSpace(identity.Symbol) != identity.Symbol ||
		strings.TrimSpace(identity.NativeSymbol) != identity.NativeSymbol ||
		strings.TrimSpace(identity.NativeID) != identity.NativeID ||
		strings.TrimSpace(identity.BaseAsset) != identity.BaseAsset ||
		strings.TrimSpace(identity.QuoteAsset) != identity.QuoteAsset ||
		strings.TrimSpace(identity.SettleAsset) != identity.SettleAsset {
		return invalidRequest("instrument identity must be trimmed")
	}
	if identity.Product != ProductSpot && identity.Product != ProductPerp {
		return invalidRequest("instrument product is unknown")
	}
	rules := instrument.Rules
	if !rules.PriceIncrement.IsPositive() {
		return invalidRequest("instrument price increment must be positive")
	}
	if !rules.QuantityIncrement.IsPositive() {
		return invalidRequest("instrument quantity increment must be positive")
	}
	if rules.MinQuantity.IsNegative() {
		return invalidRequest("instrument min quantity must not be negative")
	}
	if rules.MinNotional.Valid && rules.MinNotional.Value.IsNegative() {
		return invalidRequest("instrument min notional must not be negative")
	}
	if instrument.Encoding.ContractMultiplier.Valid && !instrument.Encoding.ContractMultiplier.Value.IsPositive() {
		return invalidRequest("instrument contract multiplier must be positive")
	}
	if instrument.Encoding.PriceScale.Valid && instrument.Encoding.PriceScale.Value <= 0 {
		return invalidRequest("instrument price scale must be positive")
	}
	if instrument.Encoding.QuantityScale.Valid && instrument.Encoding.QuantityScale.Value <= 0 {
		return invalidRequest("instrument quantity scale must be positive")
	}
	if instrument.Encoding.QuoteScale.Valid && instrument.Encoding.QuoteScale.Value <= 0 {
		return invalidRequest("instrument quote scale must be positive")
	}
	metadata := instrument.Metadata
	if metadata.MarketFamily != "" && !knownMarketFamily(metadata.MarketFamily) {
		return invalidRequest("instrument market family is unknown")
	}
	if metadata.MarketFamily != "" {
		if identity.Product == ProductSpot && metadata.MarketFamily != MarketFamilySpot {
			return invalidRequest("spot instrument market family is inconsistent")
		}
		if identity.Product == ProductPerp && metadata.MarketFamily == MarketFamilySpot {
			return invalidRequest("perp instrument market family is inconsistent")
		}
	}
	if metadata.AssetClass != "" && !knownAssetClass(metadata.AssetClass) {
		return invalidRequest("instrument asset class is unknown")
	}
	if strings.TrimSpace(metadata.ClassificationSource) != metadata.ClassificationSource ||
		strings.TrimSpace(metadata.RawAssetType) != metadata.RawAssetType {
		return invalidRequest("instrument metadata must be trimmed")
	}
	categories, err := metadata.RawCategories.values()
	if err != nil {
		return err
	}
	seenCategories := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		if category == "" || strings.TrimSpace(category) != category {
			return invalidRequest("instrument category must be non-empty and trimmed")
		}
		if _, exists := seenCategories[category]; exists {
			return invalidRequest("instrument categories must be unique")
		}
		seenCategories[category] = struct{}{}
	}
	return nil
}

// EffectiveMetadata makes legacy and venue-agnostic instruments explicit at
// the public boundary without mutating the canonical instrument. Product is a
// structural fact, so it can safely supply the market family; asset class has
// no equivalent fallback and therefore remains unknown.
func (instrument Instrument) EffectiveMetadata() InstrumentMetadata {
	metadata := instrument.Metadata
	if metadata.MarketFamily == "" {
		switch instrument.Identity.Product {
		case ProductSpot:
			metadata.MarketFamily = MarketFamilySpot
		case ProductPerp:
			metadata.MarketFamily = MarketFamilyVenuePerpetual
		}
	}
	if metadata.AssetClass == "" {
		metadata.AssetClass = AssetClassUnknown
	}
	return metadata
}

func knownMarketFamily(family MarketFamily) bool {
	switch family {
	case MarketFamilySpot, MarketFamilyVenuePerpetual, MarketFamilyBuilderPerpetual:
		return true
	default:
		return false
	}
}

func knownAssetClass(class AssetClass) bool {
	switch class {
	case AssetClassUnknown, AssetClassCrypto, AssetClassEquity, AssetClassCommodity,
		AssetClassIndex, AssetClassFX, AssetClassRates, AssetClassPreIPO, AssetClassRWAOther:
		return true
	default:
		return false
	}
}

func knownVenue(venue Venue) bool {
	switch venue {
	case VenueBinance, VenueOKX, VenueLighter, VenueHyperliquid, VenueBybit, VenueBitget, VenueGate, VenueAster, VenueNado:
		return true
	default:
		return false
	}
}

func (instrument Instrument) NativeSymbol() string {
	return instrument.Identity.NativeSymbol
}

func (instrument Instrument) Symbol() string {
	return instrument.Identity.Symbol
}

func (instrument Instrument) Product() Product {
	return instrument.Identity.Product
}

type OrderBookRequest struct {
	Instrument Instrument `json:"instrument"`
	Limit      int        `json:"limit,omitempty"`
}

type OrderBook struct {
	Instrument Instrument  `json:"instrument"`
	Bids       []BookLevel `json:"bids,omitempty"`
	Asks       []BookLevel `json:"asks,omitempty"`
	Time       time.Time   `json:"time,omitempty"`
	Sequence   string      `json:"sequence,omitempty"`
	Page       PageInfo    `json:"page,omitempty"`
}

type BookLevel struct {
	Price    decimal.Decimal `json:"price"`
	Quantity decimal.Decimal `json:"quantity"`
}

type CandlesRequest struct {
	Instrument Instrument `json:"instrument"`
	Interval   string     `json:"interval,omitempty"`
	Start      time.Time  `json:"start,omitempty"`
	End        time.Time  `json:"end,omitempty"`
	Limit      int        `json:"limit,omitempty"`
	Cursor     string     `json:"cursor,omitempty"`
}

type CandlePage struct {
	Candles []Candle `json:"candles,omitempty"`
	Page    PageInfo `json:"page"`
}

type Candle struct {
	OpenTime  time.Time       `json:"open_time,omitempty"`
	CloseTime time.Time       `json:"close_time,omitempty"`
	Open      decimal.Decimal `json:"open"`
	High      decimal.Decimal `json:"high"`
	Low       decimal.Decimal `json:"low"`
	Close     decimal.Decimal `json:"close"`
	Volume    decimal.Decimal `json:"volume"`
	Complete  bool            `json:"complete"`
}

type PublicTradesRequest struct {
	Instrument Instrument `json:"instrument"`
	Limit      int        `json:"limit,omitempty"`
}

type PublicTradePage struct {
	Trades []PublicTrade `json:"trades,omitempty"`
	Page   PageInfo      `json:"page"`
}

type PublicTrade struct {
	Instrument Instrument      `json:"instrument"`
	TradeID    string          `json:"trade_id,omitempty"`
	Side       Side            `json:"side,omitempty"`
	Price      decimal.Decimal `json:"price"`
	Quantity   decimal.Decimal `json:"quantity"`
	Time       time.Time       `json:"time,omitempty"`
}

type PageInfo struct {
	Cursor       string    `json:"cursor,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	WindowStart  time.Time `json:"window_start,omitempty"`
	WindowEnd    time.Time `json:"window_end,omitempty"`
	HasMoreKnown bool      `json:"has_more_known"`
	HasMore      bool      `json:"has_more"`
}

type PlaceOrderRequest struct {
	Instrument    Instrument      `json:"instrument"`
	ClientOrderID string          `json:"client_order_id,omitempty"`
	Side          Side            `json:"side,omitempty"`
	Type          OrderType       `json:"type,omitempty"`
	Quantity      decimal.Decimal `json:"quantity"`
	LimitPrice    decimal.Decimal `json:"limit_price"`
	LimitPolicy   LimitPolicy     `json:"limit_policy,omitempty"`
	ReduceOnly    bool            `json:"reduce_only,omitempty"`
}

func (request PlaceOrderRequest) Validate(product Product) error {
	if product != ProductSpot && product != ProductPerp {
		return invalidRequest("unknown product")
	}
	if err := request.Instrument.Validate(); err != nil {
		return err
	}
	if request.Instrument.Identity.Product != product {
		return invalidRequest("instrument product does not match request product")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return invalidRequest("side must be buy or sell")
	}
	if !request.Quantity.IsPositive() {
		return invalidRequest("quantity must be positive")
	}
	if !decimalAligned(request.Quantity, request.Instrument.Rules.QuantityIncrement) {
		return invalidRequest("quantity is not aligned to instrument quantity increment")
	}
	if request.Quantity.LessThan(request.Instrument.Rules.MinQuantity) {
		return invalidRequest("quantity is below instrument minimum quantity")
	}
	if request.Instrument.Rules.MinNotional.Valid && request.LimitPrice.IsPositive() {
		notional := request.Quantity.Mul(request.LimitPrice)
		if notional.LessThan(request.Instrument.Rules.MinNotional.Value) {
			return invalidRequest("order notional is below instrument minimum notional")
		}
	}
	if !isPortableClientOrderID(request.ClientOrderID) {
		return invalidRequest("client order id must be a positive decimal uint48")
	}
	if product == ProductSpot && request.ReduceOnly {
		return invalidRequest("reduce-only is available only for perpetual products")
	}
	switch request.Type {
	case OrderTypeMarket:
		if !request.LimitPrice.IsZero() {
			return invalidRequest("market order must not include a limit price")
		}
		if request.LimitPolicy != "" {
			return invalidRequest("market order must not include a limit policy")
		}
	case OrderTypeLimit:
		if !request.LimitPrice.IsPositive() {
			return invalidRequest("limit order price must be positive")
		}
		if !decimalAligned(request.LimitPrice, request.Instrument.Rules.PriceIncrement) {
			return invalidRequest("limit order price is not aligned to instrument price increment")
		}
		switch request.LimitPolicy {
		case LimitPolicyResting, LimitPolicyIOC, LimitPolicyPostOnly:
		default:
			return invalidRequest("limit order policy must be resting, ioc, or post_only")
		}
	default:
		return invalidRequest("order type must be market or limit")
	}
	return nil
}

type CancelOrderRequest struct {
	Instrument Instrument `json:"instrument"`
	OrderID    string     `json:"order_id,omitempty"`
}

type OpenOrdersRequest struct {
	Instrument *Instrument `json:"instrument,omitempty"`
	Cursor     string      `json:"cursor,omitempty"`
	Limit      int         `json:"limit,omitempty"`
}

type OrderHistoryRequest struct {
	Instrument *Instrument `json:"instrument,omitempty"`
	Start      time.Time   `json:"start,omitempty"`
	End        time.Time   `json:"end,omitempty"`
	Cursor     string      `json:"cursor,omitempty"`
	Limit      int         `json:"limit,omitempty"`
}

type FillsRequest struct {
	Instrument *Instrument `json:"instrument,omitempty"`
	OrderID    string      `json:"order_id,omitempty"`
	Start      time.Time   `json:"start,omitempty"`
	End        time.Time   `json:"end,omitempty"`
	Cursor     string      `json:"cursor,omitempty"`
	Limit      int         `json:"limit,omitempty"`
}

type PositionsRequest struct {
	Instrument *Instrument `json:"instrument,omitempty"`
}

type OrderPage struct {
	Orders []Order  `json:"orders,omitempty"`
	Page   PageInfo `json:"page"`
}

type Order struct {
	NativeSymbol     string          `json:"native_symbol,omitempty"`
	NativeID         string          `json:"native_id,omitempty"`
	OrderID          string          `json:"order_id,omitempty"`
	ClientOrderID    string          `json:"client_order_id,omitempty"`
	Side             Side            `json:"side,omitempty"`
	Type             OrderType       `json:"type,omitempty"`
	Quantity         decimal.Decimal `json:"quantity"`
	LimitPrice       decimal.Decimal `json:"limit_price"`
	LimitPolicy      LimitPolicy     `json:"limit_policy,omitempty"`
	ReduceOnly       bool            `json:"reduce_only,omitempty"`
	Filled           decimal.Decimal `json:"filled"`
	AverageFillPrice OptionalDecimal `json:"average_fill_price"`
	Status           string          `json:"status,omitempty"`
	CreatedAt        time.Time       `json:"created_at,omitempty"`
	UpdatedAt        time.Time       `json:"updated_at,omitempty"`
}

type FillPage struct {
	Fills []Fill   `json:"fills,omitempty"`
	Page  PageInfo `json:"page"`
}

type Fill struct {
	NativeSymbol  string          `json:"native_symbol,omitempty"`
	NativeID      string          `json:"native_id,omitempty"`
	OrderID       string          `json:"order_id,omitempty"`
	ClientOrderID string          `json:"client_order_id,omitempty"`
	FillID        string          `json:"fill_id,omitempty"`
	Side          Side            `json:"side,omitempty"`
	Price         decimal.Decimal `json:"price"`
	Quantity      decimal.Decimal `json:"quantity"`
	Fee           decimal.Decimal `json:"fee"`
	FeeAsset      string          `json:"fee_asset,omitempty"`
	Liquidity     Liquidity       `json:"liquidity,omitempty"`
	Time          time.Time       `json:"time,omitempty"`
}

type SpotAccount struct {
	Balances []Balance `json:"balances,omitempty"`
}

type PerpAccount struct {
	Balances      []Balance       `json:"balances,omitempty"`
	Equity        OptionalDecimal `json:"equity"`
	Available     OptionalDecimal `json:"available"`
	MarginUsed    OptionalDecimal `json:"margin_used"`
	UnrealizedPnL OptionalDecimal `json:"unrealized_pnl"`
}

type Balance struct {
	Asset     string          `json:"asset,omitempty"`
	Available decimal.Decimal `json:"available"`
	Locked    decimal.Decimal `json:"locked"`
	Total     decimal.Decimal `json:"total"`
}

type Position struct {
	NativeSymbol     string          `json:"native_symbol,omitempty"`
	NativeID         string          `json:"native_id,omitempty"`
	Side             Side            `json:"side,omitempty"`
	Quantity         decimal.Decimal `json:"quantity"`
	EntryPrice       decimal.Decimal `json:"entry_price"`
	MarkPrice        decimal.Decimal `json:"mark_price"`
	UnrealizedPnL    decimal.Decimal `json:"unrealized_pnl"`
	LiquidationPrice OptionalDecimal `json:"liquidation_price"`
	Leverage         OptionalDecimal `json:"leverage"`
	MarginUsed       OptionalDecimal `json:"margin_used"`
}

type FundingRateRequest struct {
	Instrument Instrument `json:"instrument"`
}

type FundingRateHistoryRequest struct {
	Instrument Instrument `json:"instrument"`
	Start      time.Time  `json:"start,omitempty"`
	End        time.Time  `json:"end,omitempty"`
	Cursor     string     `json:"cursor,omitempty"`
	Limit      int        `json:"limit,omitempty"`
}

type FundingRatePage struct {
	Rates []FundingRate `json:"rates,omitempty"`
	Page  PageInfo      `json:"page"`
}

type FundingRate struct {
	NativeSymbol    string          `json:"native_symbol,omitempty"`
	NativeID        string          `json:"native_id,omitempty"`
	Rate            decimal.Decimal `json:"rate"`
	MarkPrice       OptionalDecimal `json:"mark_price"`
	ObservedAt      time.Time       `json:"observed_at,omitempty"`
	FundingTime     time.Time       `json:"funding_time,omitempty"`
	NextFundingTime time.Time       `json:"next_funding_time,omitempty"`
}

type FundingPaymentStatus string

const (
	FundingPaymentsUnsupported            FundingPaymentStatus = "unsupported"
	FundingPaymentsTemporarilyUnavailable FundingPaymentStatus = "temporarily_unavailable"
	FundingPaymentsIncomplete             FundingPaymentStatus = "incomplete"
	FundingPaymentsCompleteEmpty          FundingPaymentStatus = "complete_empty"
	FundingPaymentsComplete               FundingPaymentStatus = "complete"
)

type FundingPaymentHistoryRequest struct {
	Instrument      *Instrument `json:"instrument,omitempty"`
	Start           time.Time   `json:"start,omitempty"`
	End             time.Time   `json:"end,omitempty"`
	Cursor          string      `json:"cursor,omitempty"`
	Limit           int         `json:"limit,omitempty"`
	SettlementAsset string      `json:"settlement_asset,omitempty"`
}

type FundingPaymentPage struct {
	Items           []FundingPayment        `json:"items,omitempty"`
	Page            PageInfo                `json:"page"`
	Status          FundingPaymentStatus    `json:"status"`
	Watermark       FundingPaymentWatermark `json:"watermark"`
	CompleteThrough time.Time               `json:"complete_through,omitempty"`
	Revision        int64                   `json:"revision,omitempty"`
}

type FundingPaymentWatermark struct {
	Cursor           string    `json:"cursor,omitempty"`
	Revision         int64     `json:"revision,omitempty"`
	ObservedAt       time.Time `json:"observed_at,omitempty"`
	EffectiveThrough time.Time `json:"effective_through,omitempty"`
	CompleteThrough  time.Time `json:"complete_through,omitempty"`
	Verified         bool      `json:"verified"`
}

type FundingPayment struct {
	PaymentID        string          `json:"payment_id,omitempty"`
	FactID           string          `json:"fact_id,omitempty"`
	SupersedesFactID string          `json:"supersedes_fact_id,omitempty"`
	NativeSymbol     string          `json:"native_symbol,omitempty"`
	NativeID         string          `json:"native_id,omitempty"`
	SettlementAsset  string          `json:"settlement_asset,omitempty"`
	Amount           decimal.Decimal `json:"amount"`
	EffectiveAt      time.Time       `json:"effective_at,omitempty"`
	ObservedAt       time.Time       `json:"observed_at,omitempty"`
	Revision         int64           `json:"revision,omitempty"`
	Version          string          `json:"version,omitempty"`
	SourceRef        string          `json:"source_ref,omitempty"`
	FormulaVersion   string          `json:"formula_version,omitempty"`
}

type SetLeverageRequest struct {
	Instrument Instrument `json:"instrument"`
	Leverage   int        `json:"leverage"`
}

type Leverage struct {
	NativeSymbol string `json:"native_symbol,omitempty"`
	NativeID     string `json:"native_id,omitempty"`
	// Effective is the leverage confirmed by the venue. Zero means the venue
	// accepted the call but does not expose an instrument leverage setting.
	Effective int `json:"effective"`
}

func decimalAligned(value, increment decimal.Decimal) bool {
	if !increment.IsPositive() {
		return false
	}
	return value.Mod(increment).IsZero()
}

func isPortableClientOrderID(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 48)
	return err == nil && parsed > 0
}

func invalidRequest(message string) error {
	return NewError(KindInvalidRequest, ErrorDetails{
		Operation:   "PlaceOrderRequest.Validate",
		SafeMessage: message,
	})
}
