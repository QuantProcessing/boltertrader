package market

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hub"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

// Public channel names.
const (
	ChannelBook   = "book"
	ChannelTrades = "trades"
	ChannelCandle = "candle"
	ChannelTicker = "ticker"
)

const (
	bookDepth    = 50
	bookInterval = 100 * time.Millisecond
)

// Level is one price level as [price, quantity] decimal strings.
type Level [2]string

// Book is a complete top-N book snapshot.
type Book struct {
	Bids []Level `json:"bids"`
	Asks []Level `json:"asks"`
	Time int64   `json:"ts"`
}

type Trade struct {
	Price    string `json:"p"`
	Quantity string `json:"q"`
	Side     string `json:"side"`
	Time     int64  `json:"ts"`
}

type Candle struct {
	Time   int64  `json:"t"`
	Open   string `json:"o"`
	High   string `json:"h"`
	Low    string `json:"l"`
	Close  string `json:"c"`
	Volume string `json:"v"`
}

// Ticker carries derivative reference values; any field may be empty when the
// venue does not publish it.
type Ticker struct {
	Mark         string `json:"mark,omitempty"`
	Index        string `json:"index,omitempty"`
	Funding      string `json:"funding,omitempty"`
	NextFunding  int64  `json:"nextFunding,omitempty"`
	OpenInterest string `json:"oi,omitempty"`
	Time         int64  `json:"ts"`
}

// Feeds implements hub.Source for public channels.
type Feeds struct {
	catalog *Catalog
}

func NewFeeds(catalog *Catalog) *Feeds { return &Feeds{catalog: catalog} }

// ValidKey reports whether key names a servable public topic.
func (feeds *Feeds) ValidKey(key hub.Key) error {
	instrument, err := feeds.catalog.Get(venue.InstrumentID(key.Instrument))
	if err != nil {
		return err
	}
	switch key.Channel {
	case ChannelBook, ChannelTrades:
		return nil
	case ChannelCandle:
		if !validInterval(key.Interval) {
			return fmt.Errorf("unsupported candle interval")
		}
		return nil
	case ChannelTicker:
		if instrument.Product != exchange.ProductPerp {
			return fmt.Errorf("ticker is available for perpetuals only")
		}
		return nil
	}
	return fmt.Errorf("unknown channel")
}

func validInterval(interval string) bool {
	switch interval {
	case "1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d":
		return true
	}
	return false
}

func (feeds *Feeds) Source(ctx context.Context, key hub.Key, emit hub.Emit) error {
	instrument, client, err := feeds.catalog.connector(venue.InstrumentID(key.Instrument))
	if err != nil {
		return err
	}
	socket := client.WebSocket()
	if socket == nil {
		return fmt.Errorf("market websocket unavailable")
	}
	native := instrument.Native()
	switch key.Channel {
	case ChannelBook:
		subscription, err := socket.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: native, Options: exchange.WatchOptions{Buffer: 512}})
		if err != nil {
			return err
		}
		return pumpBook(ctx, key, subscription, emit)
	case ChannelTrades:
		subscription, err := socket.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: native, Options: exchange.WatchOptions{Buffer: 256}})
		if err != nil {
			return err
		}
		return pump(ctx, subscription, func(event exchange.PublicTradeEvent) {
			emit(hub.Message{Channel: ChannelTrades, Instrument: key.Instrument, Data: []Trade{{
				Price: event.Price.String(), Quantity: event.Quantity.String(), Side: string(event.Side), Time: event.Time.UnixMilli(),
			}}}, false)
		})
	case ChannelCandle:
		subscription, err := socket.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: native, Interval: key.Interval, Options: exchange.WatchOptions{Buffer: 64}})
		if err != nil {
			return err
		}
		return pump(ctx, subscription, func(event exchange.CandleEvent) {
			emit(hub.Message{Channel: ChannelCandle, Instrument: key.Instrument, Interval: key.Interval, Data: projectCandle(event.Candle)}, true)
		})
	case ChannelTicker:
		reference, ok := socket.(exchange.DerivativeReferenceWebSocket)
		if !ok {
			return fmt.Errorf("ticker stream unavailable for %s", instrument.Venue)
		}
		subscription, err := reference.WatchDerivativeReference(ctx, exchange.WatchRequest{Instrument: native, Options: exchange.WatchOptions{Buffer: 64}})
		if err != nil {
			return err
		}
		var current Ticker
		return pump(ctx, subscription, func(event exchange.DerivativeReferenceEvent) {
			mergeTicker(&current, event)
			emit(hub.Message{Channel: ChannelTicker, Instrument: key.Instrument, Data: current}, true)
		})
	}
	return fmt.Errorf("unknown channel %q", key.Channel)
}

// pumpBook keeps a local book from snapshot/delta/full events and emits a
// complete top-N snapshot at most every bookInterval.
func pumpBook(ctx context.Context, key hub.Key, subscription exchange.Subscription[exchange.BookEvent], emit hub.Emit) error {
	defer subscription.Close()
	bids := map[string]exchange.BookLevel{}
	asks := map[string]exchange.BookLevel{}
	var observed time.Time
	started, dirty := false, false
	ticker := time.NewTicker(bookInterval)
	defer ticker.Stop()
	flush := func() {
		if !started || !dirty {
			return
		}
		dirty = false
		emit(hub.Message{Channel: ChannelBook, Instrument: key.Instrument, Data: Book{
			Bids: topLevels(bids, true), Asks: topLevels(asks, false), Time: observed.UnixMilli(),
		}}, true)
	}
	events, failures, statuses := subscription.Events(), subscription.Errors(), subscription.Status()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			flush()
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			if err != nil {
				return err
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if status.State == exchange.SubscriptionClosed {
				return fmt.Errorf("book subscription closed")
			}
		case event, open := <-events:
			if !open {
				return fmt.Errorf("book subscription ended")
			}
			switch event.Kind {
			case exchange.EventSnapshot, exchange.EventFull:
				bids, asks = replaceSide(event.Bids), replaceSide(event.Asks)
				started = true
			case exchange.EventDelta:
				if !started {
					continue
				}
				patchSide(bids, event.Bids)
				patchSide(asks, event.Asks)
			}
			if !event.Time.IsZero() {
				observed = event.Time
			} else {
				observed = time.Now()
			}
			dirty = true
			if event.Kind != exchange.EventDelta {
				flush()
			}
		}
	}
}

func replaceSide(levels []exchange.BookLevel) map[string]exchange.BookLevel {
	side := make(map[string]exchange.BookLevel, len(levels))
	patchSide(side, levels)
	return side
}

func patchSide(side map[string]exchange.BookLevel, levels []exchange.BookLevel) {
	for _, level := range levels {
		key := level.Price.String()
		if level.Quantity.IsZero() {
			delete(side, key)
			continue
		}
		side[key] = level
	}
}

func topLevels(side map[string]exchange.BookLevel, descending bool) []Level {
	levels := make([]exchange.BookLevel, 0, len(side))
	for _, level := range side {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool {
		if descending {
			return levels[i].Price.GreaterThan(levels[j].Price)
		}
		return levels[i].Price.LessThan(levels[j].Price)
	})
	if len(levels) > bookDepth {
		levels = levels[:bookDepth]
	}
	result := make([]Level, len(levels))
	for i, level := range levels {
		result[i] = Level{level.Price.String(), level.Quantity.String()}
	}
	return result
}

// pump drains a subscription into handle until ctx ends or it fails.
func pump[T any](ctx context.Context, subscription exchange.Subscription[T], handle func(T)) error {
	defer subscription.Close()
	events, failures, statuses := subscription.Events(), subscription.Errors(), subscription.Status()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			if err != nil {
				return err
			}
		case status, open := <-statuses:
			if !open {
				statuses = nil
				continue
			}
			if status.State == exchange.SubscriptionClosed {
				return errors.New("subscription closed")
			}
		case event, open := <-events:
			if !open {
				return errors.New("subscription ended")
			}
			handle(event)
		}
	}
}

func projectCandle(candle exchange.Candle) Candle {
	return Candle{
		Time: candle.OpenTime.UnixMilli(), Open: candle.Open.String(), High: candle.High.String(),
		Low: candle.Low.String(), Close: candle.Close.String(), Volume: candle.Volume.String(),
	}
}

func mergeTicker(current *Ticker, event exchange.DerivativeReferenceEvent) {
	set := func(target *string, value exchange.OptionalDecimal) {
		if value.Valid {
			*target = value.Value.String()
		}
	}
	set(&current.Mark, event.MarkPrice)
	set(&current.Index, event.IndexPrice)
	set(&current.Funding, event.FundingRate)
	set(&current.OpenInterest, event.OpenInterest)
	if !event.NextFundingTime.IsZero() {
		current.NextFunding = event.NextFundingTime.UnixMilli()
	}
	current.Time = time.Now().UnixMilli()
}

// ---- REST snapshots ----

func (catalog *Catalog) OrderBook(ctx context.Context, id venue.InstrumentID, depth int) (Book, error) {
	instrument, client, err := catalog.connector(id)
	if err != nil {
		return Book{}, err
	}
	if depth <= 0 || depth > bookDepth {
		depth = 20
	}
	book, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument.Native(), Limit: depth})
	if err != nil {
		return Book{}, err
	}
	result := Book{Bids: make([]Level, 0, len(book.Bids)), Asks: make([]Level, 0, len(book.Asks)), Time: book.Time.UnixMilli()}
	for _, level := range book.Bids {
		result.Bids = append(result.Bids, Level{level.Price.String(), level.Quantity.String()})
	}
	for _, level := range book.Asks {
		result.Asks = append(result.Asks, Level{level.Price.String(), level.Quantity.String()})
	}
	return result, nil
}

func (catalog *Catalog) Candles(ctx context.Context, id venue.InstrumentID, interval string, start, end time.Time, limit int) ([]Candle, error) {
	if !validInterval(interval) {
		return nil, fmt.Errorf("unsupported candle interval")
	}
	instrument, client, err := catalog.connector(id)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	page, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument.Native(), Interval: interval, Start: start, End: end, Limit: limit})
	if err != nil {
		return nil, err
	}
	result := make([]Candle, 0, len(page.Candles))
	for _, candle := range page.Candles {
		result = append(result, projectCandle(candle))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Time < result[j].Time })
	return result, nil
}

// Stats24h derives last price and 24h change/volume from hourly candles.
type Stats24h struct {
	Last        string `json:"last"`
	Open24h     string `json:"open24h"`
	High24h     string `json:"high24h"`
	Low24h      string `json:"low24h"`
	Volume24h   string `json:"volume24h"`
	Funding     string `json:"funding,omitempty"`
	NextFunding int64  `json:"nextFunding,omitempty"`
	Mark        string `json:"mark,omitempty"`
}

func (catalog *Catalog) Stats(ctx context.Context, id venue.InstrumentID) (Stats24h, error) {
	instrument, client, err := catalog.connector(id)
	if err != nil {
		return Stats24h{}, err
	}
	now := time.Now().UTC()
	page, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument.Native(), Interval: "1h", Start: now.Add(-24 * time.Hour), End: now, Limit: 25})
	if err != nil {
		return Stats24h{}, err
	}
	candles := page.Candles
	sort.Slice(candles, func(i, j int) bool { return candles[i].OpenTime.Before(candles[j].OpenTime) })
	if len(candles) > 24 {
		candles = candles[len(candles)-24:]
	}
	var stats Stats24h
	if len(candles) > 0 {
		high, low, volume := candles[0].High, candles[0].Low, decimal.Zero
		for _, candle := range candles {
			high = decimal.Max(high, candle.High)
			low = decimal.Min(low, candle.Low)
			volume = volume.Add(candle.Volume)
		}
		stats = Stats24h{Last: candles[len(candles)-1].Close.String(), Open24h: candles[0].Open.String(), High24h: high.String(), Low24h: low.String(), Volume24h: volume.String()}
	}
	if instrument.Product == exchange.ProductPerp {
		if rate, supported, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument.Native()}); supported && err == nil {
			stats.Funding = rate.Rate.String()
			if !rate.NextFundingTime.IsZero() {
				stats.NextFunding = rate.NextFundingTime.UnixMilli()
			}
			if rate.MarkPrice.Valid {
				stats.Mark = rate.MarkPrice.Value.String()
			}
		}
	}
	return stats, nil
}
