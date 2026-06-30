package cache

import "github.com/QuantProcessing/boltertrader/core/model"

// This file extends Cache with the latest market-data snapshot per instrument,
// written by the DataEngine on the bus goroutine and read by strategies.

// marketState is the latest per-instrument market snapshot.
type marketState struct {
	quote *model.QuoteTick
	book  *model.OrderBook
	trade *model.TradeTick
}

func (c *Cache) marketFor(key string) *marketState {
	ms := c.market[key]
	if ms == nil {
		ms = &marketState{}
		c.market[key] = ms
	}
	return ms
}

// UpsertQuote stores the latest top-of-book for an instrument.
func (c *Cache) UpsertQuote(q model.QuoteTick) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marketFor(q.InstrumentID.String()).quote = &q
}

// Quote returns the latest top-of-book for an instrument.
func (c *Cache) Quote(id model.InstrumentID) (model.QuoteTick, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ms := c.market[id.String()]; ms != nil && ms.quote != nil {
		return *ms.quote, true
	}
	return model.QuoteTick{}, false
}

// UpsertBook stores the latest order book for an instrument.
func (c *Cache) UpsertBook(b model.OrderBook) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marketFor(b.InstrumentID.String()).book = &b
}

// Book returns the latest order book for an instrument.
func (c *Cache) Book(id model.InstrumentID) (model.OrderBook, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ms := c.market[id.String()]; ms != nil && ms.book != nil {
		return *ms.book, true
	}
	return model.OrderBook{}, false
}

// UpsertTrade stores the latest public trade for an instrument.
func (c *Cache) UpsertTrade(t model.TradeTick) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.marketFor(t.InstrumentID.String()).trade = &t
}

// LastTrade returns the latest public trade for an instrument.
func (c *Cache) LastTrade(id model.InstrumentID) (model.TradeTick, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ms := c.market[id.String()]; ms != nil && ms.trade != nil {
		return *ms.trade, true
	}
	return model.TradeTick{}, false
}
