package sdk

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	SettleUSDT = "usdt"
	SettleUSDC = "usdc"
)

const (
	ChannelSpotOrder        = "spot.orders"
	ChannelSpotUserTrade    = "spot.usertrades"
	ChannelSpotBalance      = "spot.balances"
	ChannelSpotOrderBook    = "spot.order_book"
	ChannelSpotTrade        = "spot.trades"
	ChannelFuturesOrder     = "futures.orders"
	ChannelFuturesUserTrade = "futures.usertrades"
	ChannelFuturesBalance   = "futures.balances"
	ChannelFuturesPosition  = "futures.positions"
	ChannelFuturesOrderBook = "futures.order_book"
	ChannelFuturesTrade     = "futures.trades"
)

type NumberString string

func (n *NumberString) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	trimmed = strings.Trim(trimmed, `"`)
	*n = NumberString(trimmed)
	return nil
}

type CurrencyPair struct {
	ID              string `json:"id"`
	Base            string `json:"base"`
	Quote           string `json:"quote"`
	Fee             string `json:"fee"`
	MinBaseAmount   string `json:"min_base_amount"`
	MinQuoteAmount  string `json:"min_quote_amount"`
	MaxBaseAmount   string `json:"max_base_amount"`
	MaxQuoteAmount  string `json:"max_quote_amount"`
	AmountPrecision int    `json:"amount_precision"`
	Precision       int    `json:"precision"`
	TradeStatus     string `json:"trade_status"`
	SellStart       int64  `json:"sell_start"`
	BuyStart        int64  `json:"buy_start"`
	Type            string `json:"type"`
}

type Ticker struct {
	CurrencyPair  string `json:"currency_pair"`
	Last          string `json:"last"`
	LowestAsk     string `json:"lowest_ask"`
	HighestBid    string `json:"highest_bid"`
	ChangePercent string `json:"change_percentage"`
	BaseVolume    string `json:"base_volume"`
	QuoteVolume   string `json:"quote_volume"`
	High24h       string `json:"high_24h"`
	Low24h        string `json:"low_24h"`
}

type OrderBook struct {
	ID      int64            `json:"id"`
	Current int64            `json:"current"`
	Update  int64            `json:"update"`
	Asks    [][]NumberString `json:"asks"`
	Bids    [][]NumberString `json:"bids"`
}

type Trade struct {
	ID           string `json:"id"`
	CreateTime   string `json:"create_time"`
	CreateTimeMS string `json:"create_time_ms"`
	CurrencyPair string `json:"currency_pair"`
	Side         string `json:"side"`
	Amount       string `json:"amount"`
	Price        string `json:"price"`
}

func (t *Trade) UnmarshalJSON(data []byte) error {
	type wireTrade Trade
	base := wireTrade(*t)
	decoded := struct {
		ID           NumberString `json:"id"`
		CreateTime   NumberString `json:"create_time"`
		CreateTimeMS NumberString `json:"create_time_ms"`
		*wireTrade
	}{wireTrade: &base}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	base.ID = string(decoded.ID)
	base.CreateTime = string(decoded.CreateTime)
	base.CreateTimeMS = string(decoded.CreateTimeMS)
	*t = Trade(base)
	return nil
}

type Candlestick []NumberString

type FuturesCandlestick struct {
	Time   NumberString `json:"t"`
	Volume NumberString `json:"v"`
	Close  NumberString `json:"c"`
	High   NumberString `json:"h"`
	Low    NumberString `json:"l"`
	Open   NumberString `json:"o"`
	Sum    NumberString `json:"sum"`
}

type SpotAccount struct {
	Currency  string `json:"currency"`
	Available string `json:"available"`
	Locked    string `json:"locked"`
	UpdateID  int64  `json:"update_id"`
}

type UnifiedMode struct {
	Mode string `json:"mode"`
}

type UnifiedBalance struct {
	Available string `json:"available"`
	Freeze    string `json:"freeze"`
	Borrowed  string `json:"borrowed"`
	Equity    string `json:"equity"`
}

type UnifiedAccount struct {
	Balances map[string]UnifiedBalance `json:"balances"`
}

type Order struct {
	ID           string       `json:"id,omitempty"`
	Text         string       `json:"text,omitempty"`
	CreateTime   NumberString `json:"create_time,omitempty"`
	CreateTimeMS NumberString `json:"create_time_ms,omitempty"`
	UpdateTimeMS NumberString `json:"update_time_ms,omitempty"`
	Event        string       `json:"event,omitempty"`
	Status       string       `json:"status,omitempty"`
	CurrencyPair string       `json:"currency_pair"`
	Type         string       `json:"type"`
	Account      string       `json:"account,omitempty"`
	Side         string       `json:"side"`
	Amount       string       `json:"amount"`
	Price        string       `json:"price,omitempty"`
	TimeInForce  string       `json:"time_in_force,omitempty"`
	Iceberg      string       `json:"iceberg,omitempty"`
	AutoBorrow   bool         `json:"auto_borrow,omitempty"`
	AutoRepay    bool         `json:"auto_repay,omitempty"`
	Left         string       `json:"left,omitempty"`
	FilledAmount string       `json:"filled_amount,omitempty"`
	FilledTotal  string       `json:"filled_total,omitempty"`
	AvgDealPrice string       `json:"avg_deal_price,omitempty"`
	Fee          string       `json:"fee,omitempty"`
	FeeCurrency  string       `json:"fee_currency,omitempty"`
	FinishAs     string       `json:"finish_as,omitempty"`
	ActionMode   string       `json:"action_mode,omitempty"`
	STPAct       string       `json:"stp_act,omitempty"`
	STPID        int64        `json:"stp_id,omitempty"`
	AmendText    string       `json:"amend_text,omitempty"`
}

type OpenOrders struct {
	CurrencyPair string  `json:"currency_pair"`
	Total        int64   `json:"total"`
	Orders       []Order `json:"orders"`
}

type Contract struct {
	Name              string  `json:"name"`
	Type              string  `json:"type"`
	QuantoMultiplier  string  `json:"quanto_multiplier"`
	RefDiscountRate   string  `json:"ref_discount_rate"`
	OrderPriceDeviate string  `json:"order_price_deviate"`
	MaintenanceRate   string  `json:"maintenance_rate"`
	MarkType          string  `json:"mark_type"`
	LastPrice         string  `json:"last_price"`
	MarkPrice         string  `json:"mark_price"`
	IndexPrice        string  `json:"index_price"`
	FundingRateInd    string  `json:"funding_rate_indicative"`
	MarkPriceRound    string  `json:"mark_price_round"`
	FundingOffset     int64   `json:"funding_offset"`
	FundingNextApply  float64 `json:"funding_next_apply"`
	InDelisting       bool    `json:"in_delisting"`
	RiskLimitBase     string  `json:"risk_limit_base"`
	InterestRate      string  `json:"interest_rate"`
	OrderPriceRound   string  `json:"order_price_round"`
	OrderSizeMin      int64   `json:"order_size_min"`
	OrderSizeMax      int64   `json:"order_size_max"`
	FundingInterval   int64   `json:"funding_interval"`
	LeverageMin       string  `json:"leverage_min"`
	LeverageMax       string  `json:"leverage_max"`
	RiskLimitStep     string  `json:"risk_limit_step"`
	RiskLimitMax      string  `json:"risk_limit_max"`
	MakerFeeRate      string  `json:"maker_fee_rate"`
	TakerFeeRate      string  `json:"taker_fee_rate"`
	FundingRate       string  `json:"funding_rate"`
	Status            string  `json:"status"`
}

type FuturesTicker struct {
	Contract              string `json:"contract"`
	Last                  string `json:"last"`
	ChangePercentage      string `json:"change_percentage"`
	TotalSize             string `json:"total_size"`
	Volume24h             string `json:"volume_24h"`
	Volume24hBase         string `json:"volume_24h_base"`
	Volume24hQuote        string `json:"volume_24h_quote"`
	Volume24hSettle       string `json:"volume_24h_settle"`
	MarkPrice             string `json:"mark_price"`
	FundingRate           string `json:"funding_rate"`
	FundingRateIndicative string `json:"funding_rate_indicative"`
	IndexPrice            string `json:"index_price"`
	HighestBid            string `json:"highest_bid"`
	LowestAsk             string `json:"lowest_ask"`
	High24h               string `json:"high_24h"`
	Low24h                string `json:"low_24h"`
}

type FuturesOrderBook struct {
	ID      int64                  `json:"id"`
	Current NumberString           `json:"current"`
	Update  NumberString           `json:"update"`
	Asks    []FuturesOrderBookItem `json:"asks"`
	Bids    []FuturesOrderBookItem `json:"bids"`
}

type FuturesOrderBookItem struct {
	Price string `json:"p"`
	Size  int64  `json:"s"`
}

type FuturesTrade struct {
	ID         int64        `json:"id"`
	CreateTime NumberString `json:"create_time"`
	Contract   string       `json:"contract"`
	Size       NumberString `json:"size"`
	Price      string       `json:"price"`
}

type FuturesAccount struct {
	User                   int64                 `json:"user"`
	Total                  string                `json:"total"`
	UnrealisedPNL          string                `json:"unrealised_pnl"`
	PositionMargin         string                `json:"position_margin"`
	OrderMargin            string                `json:"order_margin"`
	Available              string                `json:"available"`
	Currency               string                `json:"currency"`
	InDualMode             bool                  `json:"in_dual_mode"`
	PositionMode           string                `json:"position_mode"`
	PositionInitialMargin  string                `json:"position_initial_margin"`
	MaintenanceMargin      string                `json:"maintenance_margin"`
	CrossOrderMargin       string                `json:"cross_order_margin"`
	CrossInitialMargin     string                `json:"cross_initial_margin"`
	CrossMaintenanceMargin string                `json:"cross_maintenance_margin"`
	CrossUnrealisedPNL     string                `json:"cross_unrealised_pnl"`
	CrossAvailable         string                `json:"cross_available"`
	CrossMarginBalance     string                `json:"cross_margin_balance"`
	History                FuturesAccountHistory `json:"history"`
	MarginMode             NumberString          `json:"margin_mode"`
}

type FuturesAccountHistory struct {
	Point string `json:"point"`
	Time  int64  `json:"time"`
}

type Position struct {
	User               int64      `json:"user"`
	Contract           string     `json:"contract"`
	Size               int64      `json:"size"`
	Leverage           string     `json:"leverage"`
	RiskLimit          string     `json:"risk_limit"`
	LeverageMax        string     `json:"leverage_max"`
	MaintenanceRate    string     `json:"maintenance_rate"`
	Value              string     `json:"value"`
	Margin             string     `json:"margin"`
	EntryPrice         string     `json:"entry_price"`
	LiqPrice           string     `json:"liq_price"`
	MarkPrice          string     `json:"mark_price"`
	InitialMargin      string     `json:"initial_margin"`
	MaintenanceMargin  string     `json:"maintenance_margin"`
	UnrealisedPNL      string     `json:"unrealised_pnl"`
	RealisedPNL        string     `json:"realised_pnl"`
	HistoryPNL         string     `json:"history_pnl"`
	LastClosePNL       string     `json:"last_close_pnl"`
	RealisedPoint      string     `json:"realised_point"`
	HistoryPoint       string     `json:"history_point"`
	ADLRanking         int64      `json:"adl_ranking"`
	PendingOrders      int64      `json:"pending_orders"`
	CloseOrder         OrderClose `json:"close_order"`
	Mode               string     `json:"mode"`
	CrossLeverageLimit string     `json:"cross_leverage_limit"`
	UpdateTime         int64      `json:"update_time"`
	UpdateID           int64      `json:"update_id"`
}

func (p *Position) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeGateWireFields(data,
		[]string{
			"leverage", "risk_limit", "leverage_max", "maintenance_rate", "value", "margin",
			"entry_price", "liq_price", "mark_price", "initial_margin", "maintenance_margin",
			"unrealised_pnl", "realised_pnl", "history_pnl", "last_close_pnl", "realised_point",
			"history_point", "cross_leverage_limit",
		},
		[]string{"user", "size", "adl_ranking", "pending_orders", "update_time", "update_id"},
	)
	if err != nil {
		return err
	}
	type wirePosition Position
	return json.Unmarshal(normalized, (*wirePosition)(p))
}

type OrderClose struct {
	ID    int64  `json:"id"`
	Price string `json:"price"`
	IsLiq bool   `json:"is_liq"`
}

func (o *OrderClose) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeGateWireFields(data, []string{"price"}, []string{"id"})
	if err != nil {
		return err
	}
	type wireOrderClose OrderClose
	return json.Unmarshal(normalized, (*wireOrderClose)(o))
}

type FuturesOrder struct {
	ID           int64        `json:"id,omitempty"`
	User         int64        `json:"user,omitempty"`
	Contract     string       `json:"contract"`
	Size         int64        `json:"size"`
	Iceberg      int64        `json:"iceberg,omitempty"`
	Price        string       `json:"price,omitempty"`
	Close        bool         `json:"close,omitempty"`
	ReduceOnly   bool         `json:"reduce_only,omitempty"`
	TIF          string       `json:"tif,omitempty"`
	Text         string       `json:"text,omitempty"`
	Left         int64        `json:"left,omitempty"`
	FillPrice    string       `json:"fill_price,omitempty"`
	MKFR         string       `json:"mkfr,omitempty"`
	TKFR         string       `json:"tkfr,omitempty"`
	Refu         int64        `json:"refu,omitempty"`
	AutoSize     string       `json:"auto_size,omitempty"`
	STPAct       string       `json:"stp_act,omitempty"`
	STPID        int64        `json:"stp_id,omitempty"`
	FinishAs     string       `json:"finish_as,omitempty"`
	Status       string       `json:"status,omitempty"`
	IsClose      bool         `json:"is_close,omitempty"`
	IsReduceOnly bool         `json:"is_reduce_only,omitempty"`
	CreateTime   NumberString `json:"create_time,omitempty"`
	CreateTimeMS NumberString `json:"create_time_ms,omitempty"`
	UpdateTime   NumberString `json:"update_time,omitempty"`
}

func (o *FuturesOrder) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeGateWireFields(
		data,
		[]string{"price", "fill_price", "mkfr", "tkfr"},
		[]string{"id", "user", "size", "iceberg", "left", "refu", "stp_id"},
	)
	if err != nil {
		return err
	}
	type wireFuturesOrder FuturesOrder
	return json.Unmarshal(normalized, (*wireFuturesOrder)(o))
}

type MyFuturesTrade struct {
	ID         int64        `json:"id"`
	CreateTime NumberString `json:"create_time"`
	Contract   string       `json:"contract"`
	OrderID    int64        `json:"order_id"`
	Size       int64        `json:"size"`
	CloseSize  int64        `json:"close_size"`
	Price      string       `json:"price"`
	Role       string       `json:"role"`
	Text       string       `json:"text"`
	Fee        string       `json:"fee"`
}

func (t *MyFuturesTrade) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeGateWireFields(
		data,
		[]string{"price", "fee"},
		[]string{"id", "order_id", "size", "close_size"},
	)
	if err != nil {
		return err
	}
	type wireMyFuturesTrade MyFuturesTrade
	return json.Unmarshal(normalized, (*wireMyFuturesTrade)(t))
}

type WSError struct {
	Code    int64  `json:"code"`
	Message string `json:"message"`
}

type WSEnvelope struct {
	ID      uint64          `json:"id"`
	Time    int64           `json:"time"`
	TimeMS  int64           `json:"time_ms"`
	Channel string          `json:"channel"`
	Event   string          `json:"event"`
	Result  json.RawMessage `json:"result"`
	Error   *WSError        `json:"error,omitempty"`
}

type SpotOrderMessage struct {
	WSEnvelope
	Orders []Order
}

type SpotBalanceMessage struct {
	WSEnvelope
	Balances []SpotBalance
}

type SpotUserTradeMessage struct {
	WSEnvelope
	Trades []SpotUserTrade
}

type FuturesOrderMessage struct {
	WSEnvelope
	Orders []FuturesOrder
}

type FuturesUserTradeMessage struct {
	WSEnvelope
	Trades []MyFuturesTrade
}

type FuturesBalanceMessage struct {
	WSEnvelope
	Balances []FuturesBalance
}

type FuturesPositionMessage struct {
	WSEnvelope
	Positions []Position
}

type SpotBalance struct {
	Timestamp    int64  `json:"timestamp"`
	TimestampMS  string `json:"timestamp_ms"`
	User         int64  `json:"user"`
	Currency     string `json:"currency"`
	Change       string `json:"change"`
	Total        string `json:"total"`
	Available    string `json:"available"`
	Freeze       string `json:"freeze"`
	FreezeChange string `json:"freeze_change"`
	ChangeType   string `json:"change_type"`
}

func (b *SpotBalance) UnmarshalJSON(data []byte) error {
	type wireSpotBalance SpotBalance
	base := wireSpotBalance(*b)
	decoded := struct {
		Timestamp   NumberString `json:"timestamp"`
		TimestampMS NumberString `json:"timestamp_ms"`
		User        NumberString `json:"user"`
		*wireSpotBalance
	}{wireSpotBalance: &base}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	timestamp, err := parseGateInt64(decoded.Timestamp)
	if err != nil {
		return err
	}
	user, err := parseGateInt64(decoded.User)
	if err != nil {
		return err
	}
	base.Timestamp = timestamp
	base.TimestampMS = string(decoded.TimestampMS)
	base.User = user
	*b = SpotBalance(base)
	return nil
}

type SpotUserTrade struct {
	ID           string `json:"id"`
	UserID       int64  `json:"user_id"`
	CurrencyPair string `json:"currency_pair"`
	OrderID      string `json:"order_id"`
	Side         string `json:"side"`
	Role         string `json:"role"`
	Amount       string `json:"amount"`
	Price        string `json:"price"`
	Fee          string `json:"fee"`
	FeeCurrency  string `json:"fee_currency"`
	PointFee     string `json:"point_fee"`
	CreateTime   string `json:"create_time"`
	CreateTimeMS string `json:"create_time_ms"`
	Text         string `json:"text"`
}

func (t *SpotUserTrade) UnmarshalJSON(data []byte) error {
	type wireSpotUserTrade SpotUserTrade
	base := wireSpotUserTrade(*t)
	decoded := struct {
		ID           NumberString `json:"id"`
		CreateTime   NumberString `json:"create_time"`
		CreateTimeMS NumberString `json:"create_time_ms"`
		*wireSpotUserTrade
	}{wireSpotUserTrade: &base}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	base.ID = string(decoded.ID)
	base.CreateTime = string(decoded.CreateTime)
	base.CreateTimeMS = string(decoded.CreateTimeMS)
	*t = SpotUserTrade(base)
	return nil
}

func parseGateInt64(value NumberString) (int64, error) {
	text := strings.TrimSpace(string(value))
	if text == "" || text == "null" {
		return 0, nil
	}
	return strconv.ParseInt(text, 10, 64)
}

func normalizeGateWireFields(data []byte, stringFields, integerFields []string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range stringFields {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if !gateJSONNumberToken(text) {
			continue
		}
		fields[name] = json.RawMessage(strconv.Quote(text))
	}
	for _, name := range integerFields {
		raw, ok := fields[name]
		if !ok {
			continue
		}
		text := strings.TrimSpace(string(raw))
		if len(text) < 2 || text[0] != '"' {
			continue
		}
		var encoded string
		if err := json.Unmarshal(raw, &encoded); err != nil {
			return nil, err
		}
		value, err := strconv.ParseInt(strings.TrimSpace(encoded), 10, 64)
		if err != nil {
			return nil, err
		}
		fields[name] = json.RawMessage(strconv.FormatInt(value, 10))
	}
	return json.Marshal(fields)
}

func gateJSONNumberToken(value string) bool {
	if value == "" {
		return false
	}
	return value[0] == '-' || (value[0] >= '0' && value[0] <= '9')
}

type FuturesBalance struct {
	Time     int64  `json:"time"`
	TimeMS   int64  `json:"time_ms"`
	User     int64  `json:"user"`
	Currency string `json:"currency"`
	Change   string `json:"change"`
	Total    string `json:"total"`
	Text     string `json:"text"`
}

func (b *FuturesBalance) UnmarshalJSON(data []byte) error {
	normalized, err := normalizeGateWireFields(
		data,
		[]string{"change", "total"},
		[]string{"time", "time_ms", "user"},
	)
	if err != nil {
		return err
	}
	type wireFuturesBalance FuturesBalance
	return json.Unmarshal(normalized, (*wireFuturesBalance)(b))
}
