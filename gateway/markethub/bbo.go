package markethub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type BBOQuote struct {
	InstrumentID   string             `json:"instrumentId"`
	Bid            exchange.BookLevel `json:"bid"`
	Ask            exchange.BookLevel `json:"ask"`
	ObservedAt     time.Time          `json:"observedAt"`
	SourceSequence string             `json:"sourceSequence,omitempty"`
}

func (quote BBOQuote) valid() bool {
	return strings.TrimSpace(quote.InstrumentID) != "" && quote.Bid.Price.IsPositive() && quote.Bid.Quantity.IsPositive() &&
		quote.Ask.Price.IsPositive() && quote.Ask.Quantity.IsPositive() && quote.Ask.Price.GreaterThanOrEqual(quote.Bid.Price) && !quote.ObservedAt.IsZero()
}

// BBOKeyResolver maps a canonical instrument onto the same native owner key
// used by browser market subscriptions. Execution previews must join that
// owner instead of opening a second venue connection or falling back to REST.
type BBOKeyResolver interface {
	SubscriptionKey(context.Context, string, exchange.MarketDataset, string, int) (SubscriptionKey, error)
}

// BBOCache is the typed execution view over Hub's retained latest BBO value.
// Hub remains the only owner of the native WebSocket and its idle grace; this
// type only validates decimals and freshness. Concurrent readers subscribe to
// one Hub key and therefore still create exactly one native venue stream.
type BBOCache struct {
	hub  *Hub
	keys BBOKeyResolver
	now  func() time.Time
}

func NewBBOCache(hub *Hub, keys BBOKeyResolver, now func() time.Time) (*BBOCache, error) {
	if hub == nil || keys == nil {
		return nil, fmt.Errorf("BBO Hub and key resolver are required")
	}
	if now == nil {
		now = time.Now
	}
	return &BBOCache{hub: hub, keys: keys, now: now}, nil
}

func (cache *BBOCache) GetFreshBBO(ctx context.Context, instrumentID string, maxAge time.Duration) (BBOQuote, error) {
	if cache == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(instrumentID) == "" || maxAge <= 0 {
		return BBOQuote{}, ErrQuoteUnavailable
	}
	key, err := cache.keys.SubscriptionKey(ctx, instrumentID, exchange.DatasetBBO, "", 0)
	if err != nil {
		return BBOQuote{}, errors.Join(ErrQuoteUnavailable, err)
	}
	if quote, status := cache.latest(key, instrumentID, maxAge); status == quoteFresh {
		return quote, nil
	}
	return cache.awaitFresh(ctx, key, instrumentID, maxAge)
}

type quoteStatus uint8

const (
	quoteUnavailable quoteStatus = iota
	quoteStale
	quoteFresh
)

func (cache *BBOCache) latest(key SubscriptionKey, instrumentID string, maxAge time.Duration) (BBOQuote, quoteStatus) {
	message, ok := cache.hub.Latest(key)
	if !ok {
		return BBOQuote{}, quoteUnavailable
	}
	return cache.quoteFromMessage(instrumentID, message, maxAge)
}

func (cache *BBOCache) awaitFresh(ctx context.Context, key SubscriptionKey, instrumentID string, maxAge time.Duration) (BBOQuote, error) {
	subscription, err := cache.hub.Subscribe(ctx, key, 4)
	if err != nil {
		return BBOQuote{}, errors.Join(ErrQuoteUnavailable, err)
	}
	defer subscription.Close()
	// Hub deliberately bounds BBO fanout to protect browser consumers while
	// retaining every native update as latest. Inspect that in-memory latest on
	// the same cadence so an execution read cannot miss a fresh quote merely
	// because its wire notification was coalesced.
	latestTicker := time.NewTicker(bboFanoutMinInterval)
	defer latestTicker.Stop()

	sawStale := false
	for {
		select {
		case <-ctx.Done():
			if sawStale {
				return BBOQuote{}, errors.Join(ErrQuoteStale, ctx.Err())
			}
			return BBOQuote{}, errors.Join(ErrQuoteUnavailable, ctx.Err())
		case <-latestTicker.C:
			quote, status := cache.latest(key, instrumentID, maxAge)
			switch status {
			case quoteFresh:
				return quote, nil
			case quoteStale:
				sawStale = true
			}
		case message, open := <-subscription.Events():
			if !open {
				if sawStale {
					return BBOQuote{}, ErrQuoteStale
				}
				return BBOQuote{}, ErrQuoteUnavailable
			}
			if message.Kind == MessageResyncRequired {
				continue
			}
			quote, status := cache.quoteFromMessage(instrumentID, message, maxAge)
			switch status {
			case quoteFresh:
				return quote, nil
			case quoteStale:
				sawStale = true
			}
		}
	}
}

func (cache *BBOCache) quoteFromMessage(instrumentID string, message Message, maxAge time.Duration) (BBOQuote, quoteStatus) {
	if message.Kind != MessageSnapshot && message.Kind != MessageDelta && message.Kind != MessageFull {
		return BBOQuote{}, quoteUnavailable
	}
	var value struct {
		Bid exchange.BookLevel `json:"bid"`
		Ask exchange.BookLevel `json:"ask"`
	}
	if json.Unmarshal(message.Data, &value) != nil {
		return BBOQuote{}, quoteUnavailable
	}
	quote := BBOQuote{
		InstrumentID: instrumentID, Bid: value.Bid, Ask: value.Ask,
		ObservedAt: message.ObservedAt.UTC(), SourceSequence: message.SourceSequence,
	}
	if !quote.valid() {
		return BBOQuote{}, quoteUnavailable
	}
	age := cache.now().UTC().Sub(quote.ObservedAt)
	if age < 0 || age > maxAge {
		return quote, quoteStale
	}
	return quote, quoteFresh
}
