package cache

import "github.com/QuantProcessing/boltertrader/core/model"

// This file extends Cache with the latest market-data snapshot per instrument,
// written by the DataEngine on the bus goroutine and read by strategies.

// marketState is the latest per-instrument market snapshot.
type marketState struct {
	quote     *model.QuoteTick
	book      *model.OrderBook
	trade     *model.TradeTick
	reference *model.DerivativeReferenceSnapshot
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

// UpsertDerivativeReference merges a derivative funding/reference snapshot into
// the latest per-instrument market state. Venue payloads can be partial, so
// present fields replace cached fields and absent fields never clear them.
func (c *Cache) UpsertDerivativeReference(s model.DerivativeReferenceSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := c.marketFor(s.InstrumentID.String())
	merged := normalizeDerivativeReferenceSnapshot(s)
	if ms.reference != nil {
		merged = mergeDerivativeReference(*ms.reference, merged)
	}
	ms.reference = &merged
}

// DerivativeReference returns the latest merged derivative reference snapshot
// for an instrument.
func (c *Cache) DerivativeReference(id model.InstrumentID) (model.DerivativeReferenceSnapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ms := c.market[id.String()]; ms != nil && ms.reference != nil {
		return *ms.reference, true
	}
	return model.DerivativeReferenceSnapshot{}, false
}

func normalizeDerivativeReferenceSnapshot(s model.DerivativeReferenceSnapshot) model.DerivativeReferenceSnapshot {
	setReferenceFreshnessDefaults(&s, model.ReferenceHasFundingRate, model.ReferenceFieldFundingRate)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasNextFundingTime, model.ReferenceFieldNextFundingTime)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasFundingInterval, model.ReferenceFieldFundingInterval)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasMarkPrice, model.ReferenceFieldMarkPrice)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasIndexPrice, model.ReferenceFieldIndexPrice)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasOraclePrice, model.ReferenceFieldOraclePrice)
	setReferenceFreshnessDefaults(&s, model.ReferenceHasPremium, model.ReferenceFieldPremium)
	return s
}

func setReferenceFreshnessDefaults(s *model.DerivativeReferenceSnapshot, mask model.ReferenceFieldMask, field model.ReferenceField) {
	if !s.Fields.Has(mask) {
		return
	}
	freshness := s.FieldTimes.For(field)
	if freshness.Venue.IsZero() {
		freshness.Venue = s.Timestamp
	}
	if freshness.Received.IsZero() {
		freshness.Received = s.ReceivedAt
	}
	s.FieldTimes.Set(field, freshness)
}

func mergeDerivativeReference(existing, incoming model.DerivativeReferenceSnapshot) model.DerivativeReferenceSnapshot {
	out := existing
	out.InstrumentID = incoming.InstrumentID
	accepted := false
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasFundingRate, model.ReferenceFieldFundingRate) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasNextFundingTime, model.ReferenceFieldNextFundingTime) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasFundingInterval, model.ReferenceFieldFundingInterval) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasMarkPrice, model.ReferenceFieldMarkPrice) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasIndexPrice, model.ReferenceFieldIndexPrice) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasOraclePrice, model.ReferenceFieldOraclePrice) || accepted
	accepted = mergeReferenceField(&out, incoming, model.ReferenceHasPremium, model.ReferenceFieldPremium) || accepted
	if accepted {
		if out.Timestamp.IsZero() || incoming.Timestamp.After(out.Timestamp) {
			out.Timestamp = incoming.Timestamp
		}
		if out.ReceivedAt.IsZero() || incoming.ReceivedAt.After(out.ReceivedAt) {
			out.ReceivedAt = incoming.ReceivedAt
		}
		if incoming.StaleAfter > 0 {
			out.StaleAfter = incoming.StaleAfter
		}
	}
	return out
}

func mergeReferenceField(out *model.DerivativeReferenceSnapshot, incoming model.DerivativeReferenceSnapshot, mask model.ReferenceFieldMask, field model.ReferenceField) bool {
	if !incoming.Fields.Has(mask) {
		return false
	}
	if out.Fields.Has(mask) && referenceFieldOlder(out.FieldTimes.For(field), incoming.FieldTimes.For(field)) {
		return false
	}
	out.Fields = out.Fields.With(mask)
	switch field {
	case model.ReferenceFieldFundingRate:
		out.FundingRate = incoming.FundingRate
	case model.ReferenceFieldNextFundingTime:
		out.NextFundingTime = incoming.NextFundingTime
	case model.ReferenceFieldFundingInterval:
		out.FundingInterval = incoming.FundingInterval
	case model.ReferenceFieldMarkPrice:
		out.MarkPrice = incoming.MarkPrice
	case model.ReferenceFieldIndexPrice:
		out.IndexPrice = incoming.IndexPrice
	case model.ReferenceFieldOraclePrice:
		out.OraclePrice = incoming.OraclePrice
	case model.ReferenceFieldPremium:
		out.Premium = incoming.Premium
	}
	out.FieldTimes.Set(field, incoming.FieldTimes.For(field))
	return true
}

func referenceFieldOlder(existing, incoming model.FieldFreshness) bool {
	if existing.Venue.IsZero() && existing.Received.IsZero() {
		return false
	}
	if !existing.Venue.IsZero() && !incoming.Venue.IsZero() {
		if incoming.Venue.Before(existing.Venue) {
			return true
		}
		if incoming.Venue.After(existing.Venue) {
			return false
		}
	}
	if !existing.Received.IsZero() && !incoming.Received.IsZero() {
		return incoming.Received.Before(existing.Received)
	}
	return false
}
