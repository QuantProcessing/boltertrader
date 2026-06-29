package standx

import "encoding/json"

// Base Response
type APIResponse struct {
	Code      int             `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data,omitempty"` // For flexible parsing
}

// ==========================================
// Authentication Types
// ==========================================

// PrepareSignInRequest - POST /v1/offchain/prepare-signin
type PrepareSignInRequest struct {
	Address   string `json:"address"`
	RequestID string `json:"requestId"` // Base58 encoded Ed25519 Public Key
}

type PrepareSignInResponse struct {
	Success    bool   `json:"success"`
	SignedData string `json:"signedData"` // JWT
}

// SignedDataJWT Payload (JWT Claims)
type SignedDataPayload struct {
	Domain    string `json:"domain"`
	URI       string `json:"uri"`
	Statement string `json:"statement"`
	Version   string `json:"version"`
	ChainID   int    `json:"chainId"`
	Nonce     string `json:"nonce"`
	Address   string `json:"address"`
	RequestID string `json:"requestId"`
	IssuedAt  string `json:"issuedAt"`
	Message   string `json:"message"` // Message to sign with EVM wallet
	Exp       int64  `json:"exp"`
	Iat       int64  `json:"iat"`
}

// LoginRequest - POST /v1/offchain/login
type LoginRequest struct {
	Signature      string `json:"signature"` // EVM Wallet Signature
	SignedData     string `json:"signedData"`
	ExpiresSeconds int    `json:"expiresSeconds"`
}

type LoginResponse struct {
	Token      string `json:"token"`
	Address    string `json:"address"`
	Alias      string `json:"alias"`
	Chain      string `json:"chain"`
	PerpsAlpha bool   `json:"perpsAlpha"`
}

// ==========================================
// Market Data Types (Public)
// ==========================================

type SymbolInfo struct {
	BaseAsset         string `json:"base_asset"`
	BaseDecimals      int    `json:"base_decimals"`
	CreatedAt         string `json:"created_at"`
	DefLeverage       string `json:"def_leverage"`
	DepthTicks        string `json:"depth_ticks"`
	Enabled           bool   `json:"enabled"`
	MakerFee          string `json:"maker_fee"`
	MaxLeverage       string `json:"max_leverage"`
	MaxOpenOrders     string `json:"max_open_orders"`
	MaxOrderQty       string `json:"max_order_qty"`
	MaxPositionSize   string `json:"max_position_size"`
	MinOrderQty       string `json:"min_order_qty"`
	PriceCapRatio     string `json:"price_cap_ratio"`
	PriceFloorRatio   string `json:"price_floor_ratio"`
	PriceTickDecimals int    `json:"price_tick_decimals"`
	QtyTickDecimals   int    `json:"qty_tick_decimals"`
	QuoteAsset        string `json:"quote_asset"`
	QuoteDecimals     int    `json:"quote_decimals"`
	Symbol            string `json:"symbol"`
	TakerFee          string `json:"taker_fee"`
	UpdatedAt         string `json:"updated_at"`
}

type SymbolMarket struct {
	Bid1                 string   `json:"bid1"`
	Base                 string   `json:"base"`
	Ask1                 string   `json:"ask1"`
	FundingRate          string   `json:"funding_rate"`
	HighPrice24h         float64  `json:"high_price_24h"`
	IndexPrice           string   `json:"index_price"`
	LastPrice            string   `json:"last_price"`
	LowPrice24h          float64  `json:"low_price_24h"`
	MarkPrice            string   `json:"mark_price"`
	MidPrice             string   `json:"mid_price"`
	NextFundingTime      string   `json:"next_funding_time"`
	OpenInterest         string   `json:"open_interest"`
	OpenInterestNotional string   `json:"open_interest_notional"`
	Quote                string   `json:"quote"`
	Spread               []string `json:"spread"` // [bid, ask]
	Symbol               string   `json:"symbol"`
	Time                 string   `json:"time"`
	Volume24h            float64  `json:"volume_24h"`
	VolumeQuote24h       float64  `json:"volume_quote_24h"`
}

type MarketOverview struct {
	Summary MarketOverviewSummary  `json:"summary"`
	Symbols []MarketOverviewSymbol `json:"symbols"`
}

type MarketOverviewSummary struct {
	OpenInterestNotional string `json:"open_interest_notional"`
	SymbolCount          int    `json:"symbol_count"`
	VolumeQuote24h       string `json:"volume_quote_24h"`
}

type MarketOverviewSymbol struct {
	Base                 string  `json:"base"`
	FundingRate          string  `json:"funding_rate"`
	LastPrice            string  `json:"last_price"`
	MarkPrice            string  `json:"mark_price"`
	OpenInterest         string  `json:"open_interest"`
	OpenInterestNotional string  `json:"open_interest_notional"`
	OpenPrice24h         float64 `json:"open_price_24h"`
	PriceChangePct       float64 `json:"price_change_pct"`
	Quote                string  `json:"quote"`
	Symbol               string  `json:"symbol"`
	Time                 string  `json:"time"`
	Volume24h            string  `json:"volume_24h"`
	VolumeQuote24h       string  `json:"volume_quote_24h"`
}

type DepthBook struct {
	Asks   [][]string `json:"asks"` // [price, qty]
	Bids   [][]string `json:"bids"` // [price, qty]
	Symbol string     `json:"symbol"`
}

type SymbolPrice struct {
	Base       string `json:"base"`
	IndexPrice string `json:"index_price"`
	LastPrice  string `json:"last_price"`
	MarkPrice  string `json:"mark_price"`
	MidPrice   string `json:"mid_price"`
	Quote      string `json:"quote"`
	SpreadAsk  string `json:"spread_ask"`
	SpreadBid  string `json:"spread_bid"`
	Symbol     string `json:"symbol"`
	Time       string `json:"time"`
}

type RecentTrade struct {
	IsBuyerTaker bool   `json:"is_buyer_taker"`
	Price        string `json:"price"`
	Qty          string `json:"qty"`
	QuoteQty     string `json:"quote_qty"`
	Symbol       string `json:"symbol"`
	Time         string `json:"time"`
}

type FundingRate struct {
	ID          int    `json:"id"`
	Symbol      string `json:"symbol"`
	FundingRate string `json:"funding_rate"`
	IndexPrice  string `json:"index_price"`
	MarkPrice   string `json:"mark_price"`
	Premium     string `json:"premium"`
	Time        string `json:"time"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Order Constants
type OrderSide string

const (
	SideBuy  OrderSide = "buy"
	SideSell OrderSide = "sell"
)

type OrderType string

const (
	OrderTypeLimit  OrderType = "limit"
	OrderTypeMarket OrderType = "market"
)

type TimeInForce string

const (
	TimeInForceGTC TimeInForce = "gtc"
	TimeInForceIOC TimeInForce = "ioc"
	TimeInForceFOK TimeInForce = "fok"
	TimeInForceALO TimeInForce = "alo"
)

// Order Status Constants
const (
	OrderStatusOpen        = "open"
	OrderStatusNew         = "new"
	OrderStatusFilled      = "filled"
	OrderStatusCancelled   = "cancelled"
	OrderStatusCanceled    = "canceled" // Alternative spelling
	OrderStatusRejected    = "rejected"
	OrderStatusUntriggered = "untriggered"
)

// ==========================================
// Account & Trade Types (Private)
// ==========================================

// CreateOrderRequest - POST /api/new_order
type CreateOrderRequest struct {
	Symbol      string      `json:"symbol"`
	Side        OrderSide   `json:"side"`                    // buy, sell
	OrderType   OrderType   `json:"order_type"`              // limit, market
	Qty         string      `json:"qty"`                     // string decimal
	Price       string      `json:"price,omitempty"`         // string decimal (optional for market)
	TimeInForce TimeInForce `json:"time_in_force,omitempty"` // gtc, ioc, alo
	ReduceOnly  bool        `json:"reduce_only"`
	ClientOrdID string      `json:"cl_ord_id,omitempty"` // Optional client ID
}

// CancelOrderRequest - POST /api/cancel_order
type CancelOrderRequest struct {
	OrderID interface{} `json:"order_id,omitempty"` // int64 or string, API doc says order_id in example is int
	ClOrdID string      `json:"cl_ord_id,omitempty"`
	Symbol  string      `json:"symbol,omitempty"` // Sometimes required
}

// CancelOrdersRequest - POST /api/cancel_orders
type CancelOrdersRequest struct {
	OrderIDs []interface{} `json:"order_id_list,omitempty"`
	ClOrdIDs []string      `json:"cl_ord_id_list,omitempty"`
	Symbol   string        `json:"symbol,omitempty"` // Usually not needed for bulk but check docs
}

// ChangeLeverageRequest - POST /api/change_leverage
type ChangeLeverageRequest struct {
	Symbol   string `json:"symbol"`
	Leverage int    `json:"leverage"`
}

// ChangeMarginModeRequest - POST /api/change_margin_mode
type ChangeMarginModeRequest struct {
	Symbol     string `json:"symbol"`
	MarginMode string `json:"margin_mode"` // "cross" or "isolated"
}

type Position struct {
	BankruptcyPrice string `json:"bankruptcy_price"`
	CreatedAt       string `json:"created_at"`
	EntryPrice      string `json:"entry_price"`
	EntryValue      string `json:"entry_value"`
	HoldingMargin   string `json:"holding_margin"`
	ID              int    `json:"id"`
	InitialMargin   string `json:"initial_margin"`
	Leverage        string `json:"leverage"`
	LiqPrice        string `json:"liq_price"`
	MaintMargin     string `json:"maint_margin"`
	MarginAsset     string `json:"margin_asset"`
	MarginMode      string `json:"margin_mode"`
	MarkPrice       string `json:"mark_price"`
	MMR             string `json:"mmr"`
	PositionValue   string `json:"position_value"`
	Qty             string `json:"qty"`
	RealizedPnL     string `json:"realized_pnl"`
	Status          string `json:"status"`
	Symbol          string `json:"symbol"`
	Time            string `json:"time"`
	UpdatedAt       string `json:"updated_at"`
	Upnl            string `json:"upnl"`
	User            string `json:"user"`
}

// Balance (Unified snapshot)
type Balance struct {
	IsolatedBalance string `json:"isolated_balance"`
	IsolatedUpnl    string `json:"isolated_upnl"`
	CrossBalance    string `json:"cross_balance"`
	CrossMargin     string `json:"cross_margin"`
	CrossUpnl       string `json:"cross_upnl"`
	Locked          string `json:"locked"`
	CrossAvailable  string `json:"cross_available"`
	Balance         string `json:"balance"` // Total assets
	Upnl            string `json:"upnl"`    // Total unrealized
	Equity          string `json:"equity"`
}

type Trade struct {
	CreatedAt string `json:"created_at"`
	FeeAsset  string `json:"fee_asset"`
	FeeQty    string `json:"fee_qty"`
	ID        int    `json:"id"`
	OrderID   int    `json:"order_id"`
	Pnl       string `json:"pnl"`
	Price     string `json:"price"`
	Qty       string `json:"qty"`
	Side      string `json:"side"`
	Symbol    string `json:"symbol"`
	UpdatedAt string `json:"updated_at"`
	User      string `json:"user"`
	Value     string `json:"value"`
}

type Order struct {
	AvailLocked  string `json:"avail_locked"`
	ClOrdID      string `json:"cl_ord_id"`
	ClosedBlock  int    `json:"closed_block"`
	CreatedAt    string `json:"created_at"`
	CreatedBlock int    `json:"created_block"`
	FillAvgPrice string `json:"fill_avg_price"`
	FillQty      string `json:"fill_qty"`
	ID           int    `json:"id"`
	Leverage     string `json:"leverage"`
	LiqID        int    `json:"liq_id"`
	Margin       string `json:"margin"`
	OrderType    string `json:"order_type"`
	Payload      string `json:"payload"`
	PositionID   int    `json:"position_id"`
	Price        string `json:"price"`
	Qty          string `json:"qty"`
	ReduceOnly   bool   `json:"reduce_only"`
	Remark       string `json:"remark"`
	Side         string `json:"side"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	Symbol       string `json:"symbol"`
	TimeInForce  string `json:"time_in_force"`
	UpdatedAt    string `json:"updated_at"`
	User         string `json:"user"`
}

type UserOrdersResponse struct {
	PageSize int             `json:"page_size"`
	Result   json.RawMessage `json:"result"` // []Order
	Total    int             `json:"total"`
}

type UserTradesResponse struct {
	PageSize int             `json:"page_size"`
	Result   json.RawMessage `json:"result"` // []Trade
	Total    int             `json:"total"`
}
