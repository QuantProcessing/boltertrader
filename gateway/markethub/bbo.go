package markethub

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"golang.org/x/sync/singleflight"
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

type BBOBootstrap interface {
	SnapshotBBO(context.Context, string) (BBOQuote, error)
}

type RESTObserver interface {
	RecordMarketREST(venue, operation, outcome string)
}

// BBOCache is kept separate from Hub's JSON fanout because execution previews
// need typed, validated decimals and a singleflight REST fallback.
type BBOCache struct {
	bootstrap BBOBootstrap
	now       func() time.Time
	mu        sync.RWMutex
	quotes    map[string]BBOQuote
	group     singleflight.Group
	observer  RESTObserver
	requests  *ratelimit.MarketCoordinator
}

func (cache *BBOCache) SetRequestCoordinator(coordinator *ratelimit.MarketCoordinator) {
	if cache != nil {
		cache.requests = coordinator
	}
}

func (cache *BBOCache) SetObserver(observer RESTObserver) {
	if cache != nil {
		cache.observer = observer
	}
}

func NewBBOCache(bootstrap BBOBootstrap, now func() time.Time) (*BBOCache, error) {
	if bootstrap == nil {
		return nil, fmt.Errorf("BBO bootstrap is required")
	}
	if now == nil {
		now = time.Now
	}
	return &BBOCache{bootstrap: bootstrap, now: now, quotes: make(map[string]BBOQuote)}, nil
}

func (cache *BBOCache) Publish(quote BBOQuote) error {
	if cache == nil || !quote.valid() {
		return ErrQuoteUnavailable
	}
	quote.ObservedAt = quote.ObservedAt.UTC()
	cache.mu.Lock()
	current := cache.quotes[quote.InstrumentID]
	if current.ObservedAt.IsZero() || !quote.ObservedAt.Before(current.ObservedAt) {
		cache.quotes[quote.InstrumentID] = quote
	}
	cache.mu.Unlock()
	return nil
}

func (cache *BBOCache) GetFreshBBO(ctx context.Context, instrumentID string, maxAge time.Duration) (BBOQuote, error) {
	if cache == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(instrumentID) == "" || maxAge <= 0 {
		return BBOQuote{}, ErrQuoteUnavailable
	}
	if quote, ok := cache.fresh(instrumentID, maxAge); ok {
		return quote, nil
	}
	value, err, _ := cache.group.Do(instrumentID, func() (any, error) {
		if quote, ok := cache.fresh(instrumentID, maxAge); ok {
			return quote, nil
		}
		venueName, _, _ := strings.Cut(instrumentID, ":")
		load := func(callCtx context.Context) (any, error) { return cache.bootstrap.SnapshotBBO(callCtx, instrumentID) }
		var raw any
		var loadErr error
		if cache.requests != nil {
			raw, loadErr = cache.requests.Do(ctx, exchange.Venue(venueName), "bbo_snapshot", instrumentID, load)
		} else {
			raw, loadErr = load(ctx)
		}
		quote, _ := raw.(BBOQuote)
		if loadErr != nil {
			outcome := "error"
			if errors.Is(loadErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				outcome = "timeout"
			}
			if cache.requests == nil {
				cache.observeREST(instrumentID, outcome)
			}
			return BBOQuote{}, errors.Join(ErrQuoteUnavailable, loadErr)
		}
		if quote.InstrumentID != instrumentID || !quote.valid() {
			return BBOQuote{}, ErrQuoteUnavailable
		}
		age := cache.now().UTC().Sub(quote.ObservedAt.UTC())
		if age < 0 || age > maxAge {
			if cache.requests == nil {
				cache.observeREST(instrumentID, "error")
			}
			return BBOQuote{}, ErrQuoteStale
		}
		if publishErr := cache.Publish(quote); publishErr != nil {
			if cache.requests == nil {
				cache.observeREST(instrumentID, "error")
			}
			return BBOQuote{}, publishErr
		}
		if cache.requests == nil {
			cache.observeREST(instrumentID, "success")
		}
		return quote, nil
	})
	if err != nil {
		return BBOQuote{}, err
	}
	return value.(BBOQuote), nil
}

func (cache *BBOCache) observeREST(instrumentID, outcome string) {
	if cache == nil || cache.observer == nil {
		return
	}
	venue, _, _ := strings.Cut(instrumentID, ":")
	cache.observer.RecordMarketREST(venue, "bbo_snapshot", outcome)
}

func (cache *BBOCache) fresh(instrumentID string, maxAge time.Duration) (BBOQuote, bool) {
	cache.mu.RLock()
	quote, ok := cache.quotes[instrumentID]
	cache.mu.RUnlock()
	if !ok || !quote.valid() {
		return BBOQuote{}, false
	}
	age := cache.now().UTC().Sub(quote.ObservedAt.UTC())
	return quote, age >= 0 && age <= maxAge
}
