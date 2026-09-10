package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
)

type MarketCatalog = wire.MarketCatalog
type MarketSnapshot = wire.MarketSnapshot
type MarketCandles = wire.MarketCandles
type BookLevel = wire.BookLevel
type BBO = wire.BBO

// MarketTopic omits all service-only priorities and private topics.
type MarketTopic struct {
	Topic        wire.Topic `json:"topic"`
	InstrumentID string     `json:"instrumentId"`
	Depth        int        `json:"depth,omitempty"`
	Interval     string     `json:"interval,omitempty"`
}

const (
	OrderBook    = wire.TopicOrderBook
	BestBidOffer = wire.TopicBBO
	Candles      = wire.TopicCandles
	MarkPrice    = wire.TopicMarkPrice
)

func (t MarketTopic) validate() error {
	if !validID(t.InstrumentID) {
		return invalid("market instrument")
	}
	switch t.Topic {
	case OrderBook:
		if t.Depth < 1 || t.Depth > 100 || t.Interval != "" {
			return invalid("book topic")
		}
	case BestBidOffer, MarkPrice:
		if t.Depth != 0 || t.Interval != "" {
			return invalid("price topic")
		}
	case Candles:
		if t.Depth != 0 || !validInterval(t.Interval) {
			return invalid("candle topic")
		}
	default:
		return invalid("market topic")
	}
	return nil
}
func (t MarketTopic) request() wire.TopicRequest {
	return wire.TopicRequest{Topic: t.Topic, InstrumentID: t.InstrumentID, Depth: t.Depth, Interval: t.Interval}
}
func validInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d":
		return true
	default:
		return false
	}
}
func (c *Client) MarketCatalog(ctx context.Context) (MarketCatalog, error) {
	var result MarketCatalog
	if err := c.request(ctx, http.MethodGet, "/v1/market/instruments", nil, nil, "", &result, 200); err != nil {
		return result, err
	}
	if !validID(result.Revision) || result.ObservedAt.IsZero() || result.Items == nil || result.UnavailableProfiles == nil {
		return MarketCatalog{}, protocol("market catalog")
	}
	if result.Complete && len(result.UnavailableProfiles) > 0 {
		return MarketCatalog{}, protocol("catalog completeness")
	}
	seen := map[string]bool{}
	for _, item := range result.Items {
		if !validID(item.InstrumentID) || !validID(item.RouteProfileID) || !validID(item.Venue) || !product(item.Product) || item.Environment != "live" || !validID(item.BaseAsset) || !validID(item.QuoteAsset) || seen[item.InstrumentID] || (result.Complete && !item.Ready) {
			return MarketCatalog{}, protocol("market instrument")
		}
		seen[item.InstrumentID] = true
	}
	return result, nil
}
func (c *Client) MarketSnapshot(ctx context.Context, topic MarketTopic) (MarketSnapshot, error) {
	if err := topic.validate(); err != nil {
		return MarketSnapshot{}, err
	}
	query := url.Values{"instrument_id": {topic.InstrumentID}, "topic": {string(topic.Topic)}}
	if topic.Depth > 0 {
		query.Set("depth", strconv.Itoa(topic.Depth))
	}
	if topic.Interval != "" {
		query.Set("interval", topic.Interval)
	}
	var result MarketSnapshot
	if err := c.request(ctx, http.MethodGet, "/v1/market/snapshot", query, nil, "", &result, 200); err != nil {
		return MarketSnapshot{}, err
	}
	if result.InstrumentID != topic.InstrumentID || result.Snapshot.Type != wire.MessageSnapshot || !validMarketMessage(topic, result.Snapshot) {
		return MarketSnapshot{}, protocol("market snapshot")
	}
	return result, nil
}
func (c *Client) MarketCandles(ctx context.Context, instrumentID, interval string, limit int) (MarketCandles, error) {
	if !validID(instrumentID) || !validInterval(interval) || limit < 1 || limit > 500 {
		return MarketCandles{}, invalid("candles query")
	}
	var result MarketCandles
	if err := c.request(ctx, http.MethodGet, "/v1/market/candles", url.Values{"instrument_id": {instrumentID}, "interval": {interval}, "limit": {strconv.Itoa(limit)}}, nil, "", &result, 200); err != nil {
		return MarketCandles{}, err
	}
	if result.InstrumentID != instrumentID || result.Interval != interval || result.Candles == nil || len(result.Candles) > limit {
		return MarketCandles{}, protocol("candles scope")
	}
	for _, candle := range result.Candles {
		if !validCandle(candle) {
			return MarketCandles{}, protocol("candle")
		}
	}
	return result, nil
}
func validCandle(c wire.Candle) bool {
	return !c.OpenTime.IsZero() && c.CloseTime.After(c.OpenTime) && positive(c.Open) && positive(c.High) && positive(c.Low) && positive(c.Close) && nonnegative(c.Volume)
}

// StreamCandle uses camelCase times, as specified by the live market protocol.
type StreamCandle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

func validMarketMessage(topic MarketTopic, message wire.Message) bool {
	if message.Topic != topic.Topic || message.Generation == 0 || message.Sequence == 0 || message.ObservedAt.IsZero() {
		return false
	}
	switch topic.Topic {
	case BestBidOffer:
		var bbo BBO
		return decode(message.Data, &bbo) == nil && positive(bbo.Bid.Price) && positive(bbo.Ask.Price) && nonnegative(bbo.Bid.Quantity) && nonnegative(bbo.Ask.Quantity)
	case OrderBook:
		var book struct {
			Bids []BookLevel `json:"bids"`
			Asks []BookLevel `json:"asks"`
		}
		if decode(message.Data, &book) != nil || book.Bids == nil || book.Asks == nil {
			return false
		}
		for _, levels := range [][]BookLevel{book.Bids, book.Asks} {
			for _, level := range levels {
				if !positive(level.Price) || !nonnegative(level.Quantity) {
					return false
				}
			}
		}
		return true
	case MarkPrice:
		var mark struct {
			Price        string    `json:"price"`
			ReceivedAt   time.Time `json:"receivedAt"`
			AgeMS        *int64    `json:"ageMs"`
			StaleAfterMS *int64    `json:"staleAfterMs"`
		}
		return decode(message.Data, &mark) == nil && positive(mark.Price) && !mark.ReceivedAt.IsZero() && mark.AgeMS != nil && *mark.AgeMS >= 0 && mark.StaleAfterMS != nil && *mark.StaleAfterMS >= 0
	case Candles:
		var data struct {
			Interval string         `json:"interval"`
			Candles  []StreamCandle `json:"candles"`
			Candle   *StreamCandle  `json:"candle"`
		}
		if decode(message.Data, &data) != nil || data.Interval != topic.Interval {
			return false
		}
		items := data.Candles
		if message.Type == wire.MessageSnapshot {
			if items == nil {
				return false
			}
		} else {
			if data.Candle == nil {
				return false
			}
			items = []StreamCandle{*data.Candle}
		}
		for _, c := range items {
			if !validCandle(wire.Candle{OpenTime: c.OpenTime, CloseTime: c.CloseTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume}) {
				return false
			}
		}
		return true
	}
	return false
}

// DecodeBBO keeps decimal prices as strings. Message scope and sequence have
// already been validated by MarketSnapshot or SubscribeMarkets.
func DecodeBBO(message wire.Message) (BBO, error) {
	var quote BBO
	if message.Topic != BestBidOffer || json.Unmarshal(message.Data, &quote) != nil || !positive(quote.Bid.Price) || !positive(quote.Ask.Price) || !nonnegative(quote.Bid.Quantity) || !nonnegative(quote.Ask.Quantity) {
		return BBO{}, protocol("BBO")
	}
	return quote, nil
}
