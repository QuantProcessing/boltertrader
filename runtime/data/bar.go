// Package data provides market-data processing helpers for the runtime's
// DataEngine, notably bar aggregation from trade prints. It is venue-neutral
// and clock-agnostic: callers pass the bucket boundary, so the same code builds
// bars from any live-style trade stream.
package data

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
)

// BarAggregator builds fixed-interval OHLCV bars from trade ticks for a single
// instrument. It emits a completed bar when a trade crosses into a new time
// bucket. It is used from the bus goroutine and holds no lock.
type BarAggregator struct {
	id       model.InstrumentID
	interval time.Duration
	intLabel string

	cur    *model.Bar
	bucket time.Time // start of the current bar's time bucket
}

// NewBarAggregator builds an aggregator for an instrument and interval. label is
// the human interval string carried on emitted bars (e.g. "1m").
func NewBarAggregator(id model.InstrumentID, interval time.Duration, label string) *BarAggregator {
	return &BarAggregator{id: id, interval: interval, intLabel: label}
}

// bucketStart truncates t to the aggregator's interval boundary (UTC-based).
func (a *BarAggregator) bucketStart(t time.Time) time.Time {
	return t.UTC().Truncate(a.interval)
}

// OnTrade folds a trade into the current bar. If the trade opens a new bucket,
// the previous (now complete) bar is returned with ok=true; otherwise ok=false.
func (a *BarAggregator) OnTrade(t model.TradeTick) (completed model.Bar, ok bool) {
	bs := a.bucketStart(t.Timestamp)

	if a.cur == nil {
		a.startBar(bs, t)
		return model.Bar{}, false
	}

	if bs.After(a.bucket) {
		done := *a.cur
		a.startBar(bs, t)
		return done, true
	}

	// Same bucket: update high/low/close/volume.
	if t.Price.GreaterThan(a.cur.High) {
		a.cur.High = t.Price
	}
	if t.Price.LessThan(a.cur.Low) {
		a.cur.Low = t.Price
	}
	a.cur.Close = t.Price
	a.cur.Volume = a.cur.Volume.Add(t.Quantity)
	a.cur.CloseTime = t.Timestamp
	return model.Bar{}, false
}

func (a *BarAggregator) startBar(bucket time.Time, t model.TradeTick) {
	a.bucket = bucket
	a.cur = &model.Bar{
		InstrumentID: a.id,
		Interval:     a.intLabel,
		Open:         t.Price,
		High:         t.Price,
		Low:          t.Price,
		Close:        t.Price,
		Volume:       t.Quantity,
		OpenTime:     bucket,
		CloseTime:    t.Timestamp,
	}
}

// Flush returns the in-progress bar as completed (e.g. at shutdown). ok is false
// if no bar is in progress.
func (a *BarAggregator) Flush() (model.Bar, bool) {
	if a.cur == nil {
		return model.Bar{}, false
	}
	done := *a.cur
	a.cur = nil
	return done, true
}
