package marketdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

var ErrRateLimited = errors.New("shared venue rate limit exhausted")

const (
	MaxDepthLevels    = 20
	MaxFundingHistory = 20
)

type Query struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
}

func (query Query) validate() error {
	if strings.TrimSpace(query.Venue) == "" || strings.TrimSpace(query.Environment) == "" || strings.TrimSpace(query.InstrumentID) == "" ||
		(query.Product != "spot" && query.Product != "perp") {
		return fmt.Errorf("venue, environment, product, and instrumentId are required")
	}
	return nil
}

type Fact struct {
	Venue             string         `json:"venue"`
	Environment       string         `json:"environment"`
	Product           string         `json:"product"`
	InstrumentID      string         `json:"instrumentId"`
	BestBid           string         `json:"bestBid"`
	BestBidSize       string         `json:"bestBidSize"`
	BestAsk           string         `json:"bestAsk"`
	BestAskSize       string         `json:"bestAskSize"`
	Bids              []Level        `json:"bids"`
	Asks              []Level        `json:"asks"`
	MarkPrice         string         `json:"markPrice,omitempty"`
	FundingRate       string         `json:"fundingRate,omitempty"`
	FundingObservedAt time.Time      `json:"fundingObservedAt,omitzero"`
	FundingTime       time.Time      `json:"fundingTime,omitzero"`
	NextFundingTime   time.Time      `json:"nextFundingTime,omitzero"`
	FundingHistory    []FundingPoint `json:"fundingHistory,omitempty"`
	OpenInterest      string         `json:"openInterest,omitempty"`
	ObservedAt        time.Time      `json:"observedAt"`
}

type Level struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}

type FundingPoint struct {
	Rate        string    `json:"rate"`
	ObservedAt  time.Time `json:"observedAt,omitzero"`
	FundingTime time.Time `json:"fundingTime"`
}

type Source interface {
	Fetch(context.Context, Query) (Fact, error)
}

type BudgetStatus struct {
	Limit      int       `json:"limit"`
	Used       int       `json:"used"`
	Remaining  int       `json:"remaining"`
	WindowEnds time.Time `json:"windowEnds"`
}

type window struct {
	started time.Time
	used    int
}

type Coordinator struct {
	limit  int
	period time.Duration
	now    func() time.Time
	mu     sync.Mutex
	venues map[string]window
}

func NewCoordinator(limit int, period time.Duration, now func() time.Time) (*Coordinator, error) {
	if limit <= 0 || period <= 0 || now == nil {
		return nil, fmt.Errorf("rate-limit coordinator requires positive limit, period, and clock")
	}
	return &Coordinator{limit: limit, period: period, now: now, venues: make(map[string]window)}, nil
}

func (coordinator *Coordinator) Acquire(ctx context.Context, venue string) error {
	if ctx == nil {
		return fmt.Errorf("rate-limit context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	venue = strings.TrimSpace(venue)
	if venue == "" {
		return fmt.Errorf("rate-limit venue is required")
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now().UTC()
	current := coordinator.venues[venue]
	if current.started.IsZero() || now.Sub(current.started) >= coordinator.period {
		current = window{started: now}
	}
	if current.used >= coordinator.limit {
		coordinator.venues[venue] = current
		return ErrRateLimited
	}
	current.used++
	coordinator.venues[venue] = current
	return nil
}

func (coordinator *Coordinator) Status(venue string) BudgetStatus {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	now := coordinator.now().UTC()
	current := coordinator.venues[strings.TrimSpace(venue)]
	if current.started.IsZero() || now.Sub(current.started) >= coordinator.period {
		return BudgetStatus{Limit: coordinator.limit, Remaining: coordinator.limit, WindowEnds: now.Add(coordinator.period)}
	}
	return BudgetStatus{Limit: coordinator.limit, Used: current.used, Remaining: coordinator.limit - current.used, WindowEnds: current.started.Add(coordinator.period)}
}

type entry struct {
	fact     Fact
	loadedAt time.Time
	wait     chan struct{}
}

type Service struct {
	source Source
	budget *Coordinator
	ttl    time.Duration
	now    func() time.Time
	mu     sync.Mutex
	cache  map[Query]*entry
}

func NewService(source Source, budget *Coordinator, ttl time.Duration, now func() time.Time) (*Service, error) {
	if source == nil || budget == nil || ttl <= 0 || now == nil {
		return nil, fmt.Errorf("market-data source, budget, positive ttl, and clock are required")
	}
	return &Service{source: source, budget: budget, ttl: ttl, now: now, cache: make(map[Query]*entry)}, nil
}

func (service *Service) Get(ctx context.Context, query Query) (Fact, error) {
	if ctx == nil {
		return Fact{}, fmt.Errorf("market-data context is required")
	}
	if err := query.validate(); err != nil {
		return Fact{}, err
	}
	for {
		service.mu.Lock()
		cached := service.cache[query]
		if cached != nil && cached.wait == nil && service.now().UTC().Sub(cached.loadedAt) <= service.ttl {
			fact := cached.fact
			service.mu.Unlock()
			return fact, nil
		}
		if cached != nil && cached.wait != nil {
			wait := cached.wait
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return Fact{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		cached = &entry{wait: make(chan struct{})}
		service.cache[query] = cached
		service.mu.Unlock()

		err := service.budget.Acquire(ctx, query.Venue)
		var fact Fact
		if err == nil {
			fact, err = service.source.Fetch(ctx, query)
		}
		if err == nil {
			err = validateFact(query, fact)
			age := service.now().UTC().Sub(fact.ObservedAt)
			if err == nil && (age > service.ttl || age < 0) {
				err = fmt.Errorf("Gateway market-data fact is stale or future-dated")
			}
		}
		service.mu.Lock()
		if err == nil {
			fact.ObservedAt = fact.ObservedAt.UTC()
			cached.fact = fact
			cached.loadedAt = service.now().UTC()
		} else {
			delete(service.cache, query)
		}
		close(cached.wait)
		cached.wait = nil
		service.mu.Unlock()
		return fact, err
	}
}

func (service *Service) BudgetStatus(venue string) BudgetStatus { return service.budget.Status(venue) }

func validateFact(query Query, fact Fact) error {
	if fact.Venue != query.Venue || fact.Environment != query.Environment || fact.Product != query.Product || fact.InstrumentID != query.InstrumentID {
		return fmt.Errorf("Gateway market-data identity mismatch")
	}
	if fact.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway market-data observation time is missing")
	}
	if len(fact.Bids) == 0 || len(fact.Asks) == 0 || len(fact.Bids) > MaxDepthLevels || len(fact.Asks) > MaxDepthLevels || len(fact.FundingHistory) > MaxFundingHistory {
		return fmt.Errorf("Gateway market-data bounds are invalid")
	}
	if fact.BestBid != fact.Bids[0].Price || fact.BestBidSize != fact.Bids[0].Quantity || fact.BestAsk != fact.Asks[0].Price || fact.BestAskSize != fact.Asks[0].Quantity {
		return fmt.Errorf("Gateway top-of-book fact is inconsistent")
	}
	for _, level := range append(append([]Level(nil), fact.Bids...), fact.Asks...) {
		if !positiveDecimal(level.Price) || !positiveDecimal(level.Quantity) {
			return fmt.Errorf("Gateway market-data level is invalid")
		}
	}
	// A rate needs the instant it was read at, and nothing more. The funding
	// schedule is optional in the Gateway's own contract because most venues do
	// not publish a last-funding time on their current-rate endpoint — of the
	// nine perp venues only OKX does, and two publish no schedule at all.
	// Demanding all three here mirrored the same over-assertion the Gateway used
	// to make, and rejected every perp fact it produced.
	if fact.FundingRate != "" {
		if _, err := decimal.NewFromString(fact.FundingRate); err != nil || fact.FundingObservedAt.IsZero() {
			return fmt.Errorf("Gateway current funding fact is invalid")
		}
	} else if !fact.FundingObservedAt.IsZero() || !fact.FundingTime.IsZero() || !fact.NextFundingTime.IsZero() || len(fact.FundingHistory) != 0 {
		return fmt.Errorf("Gateway current funding fact is incomplete")
	}
	for _, point := range fact.FundingHistory {
		if _, err := decimal.NewFromString(point.Rate); err != nil || point.FundingTime.IsZero() {
			return fmt.Errorf("Gateway funding history fact is invalid")
		}
	}
	return nil
}

func positiveDecimal(value string) bool {
	parsed, err := decimal.NewFromString(value)
	return err == nil && parsed.IsPositive()
}
