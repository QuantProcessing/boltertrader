package marketdata

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MaxCandles bounds one page. It matches the Gateway's own bound, so a page
// larger than this did not come from a Gateway that agrees with us about what
// the limit means.
const MaxCandles = 500

// CandleQuery is the chart's read. It is Query plus an interval and a bound,
// and deliberately not an extension of Query: Query is the cache key of the
// top-of-book projection, and putting an interval in it would make the same
// instrument occupy eight cache entries whose freshness rules disagree.
type CandleQuery struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
	Interval     string
	Limit        int
}

func (query CandleQuery) validate() error {
	if strings.TrimSpace(query.Venue) == "" || strings.TrimSpace(query.Environment) == "" ||
		strings.TrimSpace(query.InstrumentID) == "" || strings.TrimSpace(query.Interval) == "" ||
		(query.Product != "spot" && query.Product != "perp") {
		return fmt.Errorf("venue, environment, product, instrumentId, and interval are required")
	}
	if query.Limit < 0 || query.Limit > MaxCandles {
		return fmt.Errorf("candles limit is out of range")
	}
	return nil
}

// CandlePage is what the browser receives. Decimals stay strings the whole way
// through, as everywhere else on this surface.
type CandlePage struct {
	Venue        string    `json:"venue"`
	Environment  string    `json:"environment"`
	Product      string    `json:"product"`
	InstrumentID string    `json:"instrumentId"`
	Interval     string    `json:"interval"`
	Candles      []Candle  `json:"candles"`
	ObservedAt   time.Time `json:"observedAt"`
}

type Candle struct {
	OpenTime  time.Time `json:"openTime"`
	CloseTime time.Time `json:"closeTime"`
	Open      string    `json:"open"`
	High      string    `json:"high"`
	Low       string    `json:"low"`
	Close     string    `json:"close"`
	Volume    string    `json:"volume"`
	Complete  bool      `json:"complete"`
}

type CandleSource interface {
	FetchCandles(context.Context, CandleQuery) (CandlePage, error)
}

// CandleService fronts the Gateway's candle read.
//
// It takes the venue budget but keeps no cache, which is the one way it differs
// from Service. Service caches because a top-of-book fact is asked for by every
// panel on the page many times a second and is worthless a second later; a
// candle page is asked for once per instrument-and-interval and the newest bar
// is the only part that moves. Caching it would mean either a TTL short enough
// to be pointless or one long enough to freeze the live bar. The browser's own
// query cache is the right place for this, and the budget below is what stops a
// reload storm from spending the poller's rate limit.
type CandleService struct {
	source CandleSource
	budget *Coordinator
}

func NewCandleService(source CandleSource, budget *Coordinator) (*CandleService, error) {
	if source == nil || budget == nil {
		return nil, fmt.Errorf("candle source and budget are required")
	}
	return &CandleService{source: source, budget: budget}, nil
}

func (service *CandleService) Get(ctx context.Context, query CandleQuery) (CandlePage, error) {
	if ctx == nil {
		return CandlePage{}, fmt.Errorf("candles context is required")
	}
	if err := query.validate(); err != nil {
		return CandlePage{}, err
	}
	if err := service.budget.Acquire(ctx, query.Venue); err != nil {
		return CandlePage{}, err
	}
	page, err := service.source.FetchCandles(ctx, query)
	if err != nil {
		return CandlePage{}, err
	}
	if err := validateCandlePage(query, page); err != nil {
		return CandlePage{}, err
	}
	page.ObservedAt = page.ObservedAt.UTC()
	return page, nil
}

func (service *CandleService) BudgetStatus(venue string) BudgetStatus {
	return service.budget.Status(venue)
}

// validateCandlePage refuses a page a chart cannot draw.
//
// The strictly-ascending check is the one that is not merely defensive: a chart
// library rejects out-of-order or duplicated timestamps by throwing, so an
// unsorted page from a venue would surface as a blank panel and a console
// stack trace rather than as an error anyone can act on. There is no staleness
// check here on purpose — the newest bar of a 1d window is hours old by
// definition, and the age that would mean "stale" is the interval, not a
// constant.
func validateCandlePage(query CandleQuery, page CandlePage) error {
	if page.Venue != query.Venue || page.Environment != query.Environment ||
		page.Product != query.Product || page.InstrumentID != query.InstrumentID || page.Interval != query.Interval {
		return fmt.Errorf("Gateway candles identity mismatch")
	}
	if page.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway candles observation time is missing")
	}
	if len(page.Candles) > MaxCandles || query.Limit > 0 && len(page.Candles) > query.Limit {
		return fmt.Errorf("Gateway candles exceed the requested bound")
	}
	for index, candle := range page.Candles {
		if candle.OpenTime.IsZero() {
			return fmt.Errorf("Gateway candle open time is missing")
		}
		if index > 0 && !candle.OpenTime.After(page.Candles[index-1].OpenTime) {
			return fmt.Errorf("Gateway candles are not in ascending open-time order")
		}
	}
	return nil
}
