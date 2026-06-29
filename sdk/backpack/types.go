package sdk

import "encoding/json"

type StringValue string

func (v *StringValue) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*v = ""
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		*v = StringValue(raw)
		return nil
	}
	*v = StringValue(string(data))
	return nil
}

func (v StringValue) String() string {
	return string(v)
}

type Market struct {
	Symbol      string        `json:"symbol"`
	BaseSymbol  string        `json:"baseSymbol"`
	QuoteSymbol string        `json:"quoteSymbol"`
	MarketType  string        `json:"marketType"`
	Filters     MarketFilters `json:"filters"`
	Visible     bool          `json:"visible"`
}

type MarketFilters struct {
	Price    PriceFilter    `json:"price"`
	Quantity QuantityFilter `json:"quantity"`
}

type PriceFilter struct {
	MinPrice string `json:"minPrice"`
	TickSize string `json:"tickSize"`
}

type QuantityFilter struct {
	MinQuantity string `json:"minQuantity"`
	StepSize    string `json:"stepSize"`
}

type Ticker struct {
	Symbol             string `json:"symbol"`
	FirstPrice         string `json:"firstPrice"`
	LastPrice          string `json:"lastPrice"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	High               string `json:"high"`
	Low                string `json:"low"`
	Volume             string `json:"volume"`
	QuoteVolume        string `json:"quoteVolume"`
	Trades             string `json:"trades"`
}

type Depth struct {
	Asks         [][]string `json:"asks"`
	Bids         [][]string `json:"bids"`
	LastUpdateID string     `json:"lastUpdateId"`
	Timestamp    int64      `json:"timestamp"`
}

type Trade struct {
	ID            int64  `json:"id"`
	Price         string `json:"price"`
	Quantity      string `json:"quantity"`
	QuoteQuantity string `json:"quoteQuantity"`
	Timestamp     int64  `json:"timestamp"`
	IsBuyerMaker  bool   `json:"isBuyerMaker"`
}

type Kline struct {
	Start       string `json:"start"`
	End         string `json:"end"`
	Open        string `json:"open"`
	High        string `json:"high"`
	Low         string `json:"low"`
	Close       string `json:"close"`
	Volume      string `json:"volume"`
	QuoteVolume string `json:"quoteVolume"`
	Trades      string `json:"trades"`
}

type MarkPrice struct {
	Symbol               string `json:"symbol"`
	FundingRate          string `json:"fundingRate"`
	MarkPrice            string `json:"markPrice"`
	IndexPrice           string `json:"indexPrice"`
	NextFundingTimestamp int64  `json:"nextFundingTimestamp"`
}

type FundingRate struct {
	Symbol               string      `json:"symbol"`
	IntervalEndTimestamp StringValue `json:"intervalEndTimestamp"`
	FundingRate          string      `json:"fundingRate"`
}

type CapitalBalance struct {
	Available string `json:"available"`
	Locked    string `json:"locked"`
	Staked    string `json:"staked"`
}

type Order struct {
	OrderType             string `json:"orderType"`
	ID                    string `json:"id"`
	ClientID              uint32 `json:"clientId"`
	CreatedAt             int64  `json:"createdAt"`
	ExecutedQuantity      string `json:"executedQuantity"`
	ExecutedQuoteQuantity string `json:"executedQuoteQuantity"`
	Quantity              string `json:"quantity"`
	QuoteQuantity         string `json:"quoteQuantity"`
	Price                 string `json:"price"`
	ReduceOnly            bool   `json:"reduceOnly"`
	TimeInForce           string `json:"timeInForce"`
	Side                  string `json:"side"`
	Status                string `json:"status"`
	Symbol                string `json:"symbol"`
}

type FillHistoryRequest struct {
	MarketType    string
	OrderID       string
	Symbol        string
	Limit         int
	Offset        int
	FillType      string
	SortDirection string
}

type Fill struct {
	ClientID  StringValue `json:"clientId"`
	Fee       string      `json:"fee"`
	FeeSymbol string      `json:"feeSymbol"`
	IsMaker   bool        `json:"isMaker"`
	OrderID   string      `json:"orderId"`
	Price     string      `json:"price"`
	Quantity  string      `json:"quantity"`
	Side      string      `json:"side"`
	Symbol    string      `json:"symbol"`
	Timestamp string      `json:"timestamp"`
	TradeID   StringValue `json:"tradeId"`
}

type Position struct {
	BreakEvenPrice      string `json:"breakEvenPrice"`
	EntryPrice          string `json:"entryPrice"`
	EstLiquidationPrice string `json:"estLiquidationPrice"`
	NetCost             string `json:"netCost"`
	NetQuantity         string `json:"netQuantity"`
	NetExposureQuantity string `json:"netExposureQuantity"`
	NetExposureNotional string `json:"netExposureNotional"`
	PnlRealized         string `json:"pnlRealized"`
	PnlUnrealized       string `json:"pnlUnrealized"`
	Symbol              string `json:"symbol"`
	PositionID          string `json:"positionId"`
}

type AccountSettings struct {
	FuturesMakerFee string `json:"futuresMakerFee"`
	FuturesTakerFee string `json:"futuresTakerFee"`
	SpotMakerFee    string `json:"spotMakerFee"`
	SpotTakerFee    string `json:"spotTakerFee"`
}

type CreateOrderRequest struct {
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Quantity    string `json:"quantity"`
	Price       string `json:"price,omitempty"`
	TimeInForce string `json:"timeInForce,omitempty"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	ClientID    uint32 `json:"clientId,omitempty"`
}

type CancelOrderRequest struct {
	OrderID string `json:"orderId"`
	Symbol  string `json:"symbol"`
}

type StreamEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type DepthEvent struct {
	EventType       string     `json:"e"`
	EventTime       int64      `json:"E"`
	Symbol          string     `json:"s"`
	Asks            [][]string `json:"a"`
	Bids            [][]string `json:"b"`
	FirstUpdateID   int64      `json:"U"`
	FinalUpdateID   int64      `json:"u"`
	EngineTimestamp int64      `json:"T"`
}

type OrderUpdateEvent struct {
	EventType             string      `json:"e"`
	EventTime             int64       `json:"E"`
	Symbol                string      `json:"s"`
	ClientID              StringValue `json:"c"`
	Side                  string      `json:"S"`
	OrderType             string      `json:"o"`
	TimeInForce           string      `json:"f"`
	Quantity              string      `json:"q"`
	QuoteQuantity         string      `json:"Q"`
	Price                 string      `json:"p"`
	OrderState            string      `json:"X"`
	OrderID               string      `json:"i"`
	TradeID               StringValue `json:"t"`
	FillQuantity          string      `json:"l"`
	ExecutedQuantity      string      `json:"z"`
	ExecutedQuoteQuantity string      `json:"Z"`
	FillPrice             string      `json:"L"`
	IsMaker               bool        `json:"m"`
	Fee                   string      `json:"n"`
	FeeSymbol             string      `json:"N"`
	EngineTimestamp       int64       `json:"T"`
	PostOnly              bool        `json:"y"`
}

type PositionUpdateEvent struct {
	EventType        string      `json:"e"`
	EventTime        int64       `json:"E"`
	Symbol           string      `json:"s"`
	BreakEvenPrice   StringValue `json:"b"`
	EntryPrice       StringValue `json:"B"`
	MarkPrice        StringValue `json:"M"`
	NetQuantity      StringValue `json:"q"`
	ExposureQuantity StringValue `json:"Q"`
	ExposureNotional StringValue `json:"n"`
	PositionID       string      `json:"i"`
	PnlRealized      StringValue `json:"p"`
	PnlUnrealized    StringValue `json:"P"`
	EngineTimestamp  int64       `json:"T"`
}
