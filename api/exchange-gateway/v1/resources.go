package gatewayv1

import "time"

// Decimal values on this boundary are strings. Consumers may parse them as
// arbitrary precision decimals; decoding through float64 loses order precision.
type Envelope[T any] struct {
	Data T `json:"data"`
}
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}
type ErrorBody struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	CorrelationID string `json:"correlation_id,omitempty"`
	RequestID     string `json:"requestId,omitempty"`
	VenueCode     string `json:"venueCode,omitempty"`
	Retryable     bool   `json:"retryable"`
}
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type Account struct {
	ID                string         `json:"accountId"`
	Venue             string         `json:"venue"`
	NativeEnvironment string         `json:"nativeEnvironment"`
	DisplayName       string         `json:"displayName"`
	Routes            []AccountRoute `json:"routes"`
	Status            string         `json:"status"`
	LastValidatedAt   *time.Time     `json:"lastValidatedAt"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}
type AccountRoute struct {
	RouteID         string `json:"routeId"`
	Product         string `json:"product"`
	SettlementAsset string `json:"settlementAsset,omitempty"`
	RouteProfileID  string `json:"routeProfileId"`
}
type AccountConnection struct {
	AccountID  string    `json:"accountId"`
	RuntimeID  string    `json:"runtimeId"`
	Connected  bool      `json:"connected"`
	ObservedAt time.Time `json:"observedAt"`
}
type Balance struct {
	Asset     string `json:"asset"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Total     string `json:"total"`
}
type BalanceResource struct {
	AccountID       string  `json:"account_id"`
	Product         string  `json:"product"`
	SettlementAsset string  `json:"settlement_asset,omitempty"`
	AvailableMargin string  `json:"available_margin,omitempty"`
	Balance         Balance `json:"balance"`
}

// BalanceSnapshot is complete across the selected account routes. It is not
// paginated: pages fetched at different times cannot form an account snapshot.
const MaxBalanceItems = 10000

type BalanceSnapshot struct {
	Items    []BalanceResource `json:"items"`
	Complete bool              `json:"complete"`
}

type OptionalDecimal struct {
	Value string `json:"value"`
	Valid bool   `json:"valid"`
}
type Position struct {
	NativeSymbol     string          `json:"native_symbol,omitempty"`
	NativeID         string          `json:"native_id,omitempty"`
	PositionSlot     string          `json:"position_slot"`
	Side             string          `json:"side,omitempty"`
	Quantity         string          `json:"quantity"`
	EntryPrice       string          `json:"entry_price"`
	MarkPrice        string          `json:"mark_price"`
	UnrealizedPnL    string          `json:"unrealized_pnl"`
	LiquidationPrice OptionalDecimal `json:"liquidation_price"`
	Leverage         OptionalDecimal `json:"leverage"`
	MarginUsed       OptionalDecimal `json:"margin_used"`
}
type PositionResource struct {
	AccountID       string    `json:"account_id"`
	RouteID         string    `json:"route_id"`
	InstrumentID    string    `json:"instrument_id"`
	PositionSlot    string    `json:"position_slot"`
	Position        Position  `json:"position"`
	ResourceVersion uint64    `json:"resource_version"`
	ObservedAt      time.Time `json:"observed_at"`
	VerifiedAt      time.Time `json:"verified_at"`
}
type PositionSnapshot struct {
	AccountID  string             `json:"account_id"`
	Freshness  string             `json:"freshness"`
	Reason     string             `json:"reason,omitempty"`
	VerifiedAt time.Time          `json:"verified_at"`
	Items      []PositionResource `json:"items"`
}
type PlaceOrderRequest struct {
	AccountID      string `json:"account_id"`
	RouteID        string `json:"route_id"`
	InstrumentID   string `json:"instrument_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Side           string `json:"side"`
	Type           string `json:"type"`
	Quantity       string `json:"quantity"`
	LimitPrice     string `json:"limit_price,omitempty"`
	LimitPolicy    string `json:"limit_policy,omitempty"`
	ReduceOnly     bool   `json:"reduce_only,omitempty"`
	Leverage       int    `json:"leverage,omitempty"`
	RuntimeID      string `json:"runtime_id"`
}
type Order struct {
	CommandID       string `json:"command_id"`
	GatewayOrderID  string `json:"gateway_order_id"`
	ClientOrderID   string `json:"client_order_id"`
	AccountID       string `json:"account_id"`
	InstrumentID    string `json:"instrument_id"`
	State           string `json:"state"`
	ResourceVersion int64  `json:"resource_version"`
	VenueCode       string `json:"venue_code,omitempty"`
	VenueMessage    string `json:"venue_message,omitempty"`
}
type OrderLookup struct {
	IdempotencyKey string `json:"idempotency_key"`
	AccountID      string `json:"account_id"`
	InstrumentID   string `json:"instrument_id"`
	Order          Order  `json:"order"`
}
type Fill struct {
	NativeSymbol  string    `json:"native_symbol,omitempty"`
	NativeID      string    `json:"native_id,omitempty"`
	OrderID       string    `json:"order_id,omitempty"`
	ClientOrderID string    `json:"client_order_id,omitempty"`
	FillID        string    `json:"fill_id"`
	Side          string    `json:"side"`
	Price         string    `json:"price"`
	Quantity      string    `json:"quantity"`
	Fee           string    `json:"fee"`
	FeeAsset      string    `json:"fee_asset,omitempty"`
	Liquidity     string    `json:"liquidity,omitempty"`
	Time          time.Time `json:"time"`
}
type FillResource struct {
	AccountID       string `json:"account_id"`
	InstrumentID    string `json:"instrument_id"`
	GatewayOrderID  string `json:"gateway_order_id,omitempty"`
	SettlementAsset string `json:"settlement_asset"`
	Product         string `json:"product"`
	BaseAsset       string `json:"base_asset"`
	Fill            Fill   `json:"fill"`
}
type MarketInstrument struct {
	InstrumentID   string `json:"instrument_id"`
	RouteProfileID string `json:"route_profile_id"`
	Venue          string `json:"venue"`
	Environment    string `json:"environment"`
	Product        string `json:"product"`
	Settlement     string `json:"settlement"`
	Symbol         string `json:"symbol"`
	BaseAsset      string `json:"base_asset"`
	QuoteAsset     string `json:"quote_asset"`
	Ready          bool   `json:"ready"`
}
type MarketCatalog struct {
	Revision            string             `json:"revision"`
	ObservedAt          time.Time          `json:"observed_at"`
	Complete            bool               `json:"complete"`
	UnavailableProfiles []string           `json:"unavailable_profiles"`
	Items               []MarketInstrument `json:"items"`
}
type MarketSnapshot struct {
	InstrumentID string  `json:"instrument_id"`
	Snapshot     Message `json:"snapshot"`
}
type Candle struct {
	OpenTime  time.Time `json:"open_time"`
	CloseTime time.Time `json:"close_time"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}
type PageInfo struct {
	Cursor       string    `json:"cursor,omitempty"`
	Limit        int       `json:"limit,omitempty"`
	WindowStart  time.Time `json:"window_start,omitempty"`
	WindowEnd    time.Time `json:"window_end,omitempty"`
	HasMoreKnown bool      `json:"has_more_known"`
	HasMore      bool      `json:"has_more"`
}
type MarketCandles struct {
	InstrumentID string   `json:"instrument_id"`
	Interval     string   `json:"interval"`
	Candles      []Candle `json:"candles"`
	Page         PageInfo `json:"page"`
}
type BookLevel struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}
type BBO struct {
	Bid BookLevel `json:"bid"`
	Ask BookLevel `json:"ask"`
}
