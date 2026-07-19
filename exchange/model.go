package exchange

import (
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

type Instrument struct {
	Symbol            string          `json:"symbol,omitempty"`
	BaseAsset         string          `json:"base_asset,omitempty"`
	QuoteAsset        string          `json:"quote_asset,omitempty"`
	SettleAsset       string          `json:"settle_asset,omitempty"`
	Product           Product         `json:"product,omitempty"`
	PriceIncrement    decimal.Decimal `json:"price_increment"`
	QuantityIncrement decimal.Decimal `json:"quantity_increment"`
	MinQuantity       decimal.Decimal `json:"min_quantity"`
	MinNotional       OptionalDecimal `json:"min_notional"`
}

type OrderBookRequest struct {
	Instrument string `json:"instrument,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type OrderBook struct {
	Instrument string      `json:"instrument,omitempty"`
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
	Instrument string    `json:"instrument,omitempty"`
	Interval   string    `json:"interval,omitempty"`
	Start      time.Time `json:"start,omitempty"`
	End        time.Time `json:"end,omitempty"`
	Limit      int       `json:"limit,omitempty"`
	Cursor     string    `json:"cursor,omitempty"`
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
	Instrument string `json:"instrument,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type PublicTradePage struct {
	Trades []PublicTrade `json:"trades,omitempty"`
	Page   PageInfo      `json:"page"`
}

type PublicTrade struct {
	Instrument string          `json:"instrument,omitempty"`
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
	Instrument    string          `json:"instrument,omitempty"`
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
	if strings.TrimSpace(request.Instrument) == "" {
		return invalidRequest("instrument is required")
	}
	if request.Side != SideBuy && request.Side != SideSell {
		return invalidRequest("side must be buy or sell")
	}
	if !request.Quantity.IsPositive() {
		return invalidRequest("quantity must be positive")
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
	Instrument string `json:"instrument,omitempty"`
	OrderID    string `json:"order_id,omitempty"`
}

type OpenOrdersRequest struct {
	Instrument string `json:"instrument,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type OrderHistoryRequest struct {
	Instrument string    `json:"instrument,omitempty"`
	Start      time.Time `json:"start,omitempty"`
	End        time.Time `json:"end,omitempty"`
	Cursor     string    `json:"cursor,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

type FillsRequest struct {
	Instrument string    `json:"instrument,omitempty"`
	OrderID    string    `json:"order_id,omitempty"`
	Start      time.Time `json:"start,omitempty"`
	End        time.Time `json:"end,omitempty"`
	Cursor     string    `json:"cursor,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

type PositionsRequest struct {
	Instrument string `json:"instrument,omitempty"`
}

type OrderPage struct {
	Orders []Order  `json:"orders,omitempty"`
	Page   PageInfo `json:"page"`
}

type Order struct {
	Instrument       string          `json:"instrument,omitempty"`
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
	Instrument    string          `json:"instrument,omitempty"`
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
	Instrument       string          `json:"instrument,omitempty"`
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
	Instrument string `json:"instrument,omitempty"`
}

type FundingRateHistoryRequest struct {
	Instrument string    `json:"instrument,omitempty"`
	Start      time.Time `json:"start,omitempty"`
	End        time.Time `json:"end,omitempty"`
	Cursor     string    `json:"cursor,omitempty"`
	Limit      int       `json:"limit,omitempty"`
}

type FundingRatePage struct {
	Rates []FundingRate `json:"rates,omitempty"`
	Page  PageInfo      `json:"page"`
}

type FundingRate struct {
	Instrument      string          `json:"instrument,omitempty"`
	Rate            decimal.Decimal `json:"rate"`
	MarkPrice       OptionalDecimal `json:"mark_price"`
	ObservedAt      time.Time       `json:"observed_at,omitempty"`
	FundingTime     time.Time       `json:"funding_time,omitempty"`
	NextFundingTime time.Time       `json:"next_funding_time,omitempty"`
}

type SetLeverageRequest struct {
	Instrument string `json:"instrument,omitempty"`
	Leverage   int    `json:"leverage"`
}

type Leverage struct {
	Instrument string `json:"instrument,omitempty"`
	// Effective is the leverage confirmed by the venue. Zero means the venue
	// accepted the call but does not expose an instrument leverage setting.
	Effective int `json:"effective"`
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
