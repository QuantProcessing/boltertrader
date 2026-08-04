package marketdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrRateLimited = errors.New("shared venue rate limit exhausted")

type Query struct {
	Venue        string
	InstrumentID string
}

func (query Query) validate() error {
	if strings.TrimSpace(query.Venue) == "" || strings.TrimSpace(query.InstrumentID) == "" {
		return fmt.Errorf("venue and instrumentId are required")
	}
	return nil
}

type Fact struct {
	Venue        string    `json:"venue"`
	InstrumentID string    `json:"instrumentId"`
	BestBid      string    `json:"bestBid,omitempty"`
	BestAsk      string    `json:"bestAsk,omitempty"`
	MarkPrice    string    `json:"markPrice,omitempty"`
	FundingRate  string    `json:"fundingRate,omitempty"`
	OpenInterest string    `json:"openInterest,omitempty"`
	ObservedAt   time.Time `json:"observedAt"`
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
		}
		service.mu.Lock()
		if err == nil {
			if fact.ObservedAt.IsZero() {
				fact.ObservedAt = service.now().UTC()
			} else {
				fact.ObservedAt = fact.ObservedAt.UTC()
			}
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
	if fact.Venue != query.Venue || fact.InstrumentID != query.InstrumentID {
		return fmt.Errorf("Gateway market-data identity mismatch")
	}
	if fact.BestBid == "" && fact.BestAsk == "" && fact.MarkPrice == "" && fact.FundingRate == "" && fact.OpenInterest == "" {
		return fmt.Errorf("Gateway market-data fact is empty")
	}
	return nil
}
