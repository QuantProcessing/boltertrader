package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

const (
	ReasonUnavailable  = "catalog_unavailable"
	ReasonInvalidFacts = "catalog_invalid_gateway_facts"
	ReasonStale        = "catalog_projection_stale"
)

var ErrUnavailable = errors.New("catalog projection unavailable")

type Venue struct {
	Venue       string `json:"venue"`
	Environment string `json:"environment"`
	Ready       bool   `json:"ready"`
}

type Instrument struct {
	InstrumentID string     `json:"instrumentId"`
	Venue        string     `json:"venue"`
	Environment  string     `json:"environment"`
	Product      string     `json:"product"`
	Settlement   string     `json:"settlement,omitempty"`
	Symbol       string     `json:"symbol"`
	BaseAsset    string     `json:"baseAsset"`
	QuoteAsset   string     `json:"quoteAsset"`
	Rules        TradeRules `json:"rules"`
	Ready        bool       `json:"ready"`
}

type TradeRules struct {
	PriceIncrement    string `json:"priceIncrement"`
	QuantityIncrement string `json:"quantityIncrement"`
	MinQuantity       string `json:"minQuantity"`
	MinNotional       string `json:"minNotional,omitempty"`
}

type Snapshot struct {
	Revision    string       `json:"revision"`
	ObservedAt  time.Time    `json:"observedAt"`
	Venues      []Venue      `json:"venues"`
	Instruments []Instrument `json:"instruments"`
}

type Status struct {
	Ready      bool          `json:"ready"`
	Reason     string        `json:"reason"`
	Revision   string        `json:"revision,omitempty"`
	ObservedAt time.Time     `json:"observedAt,omitempty"`
	Age        time.Duration `json:"-"`
}

type Source interface {
	Fetch(context.Context) (Snapshot, error)
}

type Projection struct {
	source Source
	ttl    time.Duration
	now    func() time.Time

	mu       sync.Mutex
	snapshot Snapshot
	loaded   bool
	reason   string
	inFlight chan struct{}
}

func NewProjection(source Source, ttl time.Duration, now func() time.Time) (*Projection, error) {
	if source == nil {
		return nil, fmt.Errorf("catalog Gateway source is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("catalog projection ttl must be positive")
	}
	if now == nil {
		return nil, fmt.Errorf("catalog clock is required")
	}
	return &Projection{source: source, ttl: ttl, now: now, reason: ReasonUnavailable}, nil
}

func (projection *Projection) Snapshot(ctx context.Context) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, fmt.Errorf("catalog context is required")
	}
	for {
		projection.mu.Lock()
		if projection.loaded && projection.now().UTC().Sub(projection.snapshot.ObservedAt) <= projection.ttl {
			snapshot := clone(projection.snapshot)
			projection.mu.Unlock()
			return snapshot, nil
		}
		if projection.inFlight != nil {
			wait := projection.inFlight
			projection.mu.Unlock()
			select {
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		projection.inFlight = make(chan struct{})
		wait := projection.inFlight
		projection.mu.Unlock()

		snapshot, err := projection.source.Fetch(ctx)
		if err == nil {
			err = validate(snapshot)
			age := projection.now().UTC().Sub(snapshot.ObservedAt)
			if err == nil && (age > projection.ttl || age < 0) {
				err = fmt.Errorf("Gateway catalog facts are stale or future-dated")
			}
		}
		projection.mu.Lock()
		if err == nil {
			snapshot.ObservedAt = snapshot.ObservedAt.UTC()
			projection.snapshot = clone(snapshot)
			projection.loaded = true
			projection.reason = "ready"
		} else if errors.Is(err, ErrUnavailable) {
			projection.reason = ReasonUnavailable
		} else {
			projection.reason = ReasonInvalidFacts
		}
		close(wait)
		projection.inFlight = nil
		projection.mu.Unlock()
		if err != nil {
			return Snapshot{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return clone(snapshot), nil
	}
}

func (projection *Projection) Status() Status {
	projection.mu.Lock()
	defer projection.mu.Unlock()
	if !projection.loaded {
		return Status{Reason: projection.reason}
	}
	age := projection.now().UTC().Sub(projection.snapshot.ObservedAt)
	if age < 0 {
		age = 0
	}
	ready := age <= projection.ttl
	reason := projection.reason
	if !ready {
		reason = ReasonStale
	}
	return Status{Ready: ready, Reason: reason, Revision: projection.snapshot.Revision, ObservedAt: projection.snapshot.ObservedAt, Age: age}
}

func validate(snapshot Snapshot) error {
	if strings.TrimSpace(snapshot.Revision) == "" {
		return fmt.Errorf("Gateway catalog revision is required")
	}
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway catalog observation time is required")
	}
	venues := make(map[string]struct{}, len(snapshot.Venues))
	for _, venue := range snapshot.Venues {
		if strings.TrimSpace(venue.Venue) == "" || strings.TrimSpace(venue.Environment) == "" {
			return fmt.Errorf("Gateway venue identity is incomplete")
		}
		key := venue.Venue + "\x00" + venue.Environment
		if _, exists := venues[key]; exists {
			return fmt.Errorf("duplicate Gateway venue %q environment %q", venue.Venue, venue.Environment)
		}
		venues[key] = struct{}{}
	}
	instruments := make(map[string]struct{}, len(snapshot.Instruments))
	for _, instrument := range snapshot.Instruments {
		if strings.TrimSpace(instrument.InstrumentID) == "" || strings.TrimSpace(instrument.Venue) == "" ||
			strings.TrimSpace(instrument.Environment) == "" || strings.TrimSpace(instrument.Product) == "" ||
			strings.TrimSpace(instrument.Symbol) == "" || strings.TrimSpace(instrument.BaseAsset) == "" || strings.TrimSpace(instrument.QuoteAsset) == "" {
			return fmt.Errorf("Gateway instrument identity is incomplete")
		}
		if _, ok := venues[instrument.Venue+"\x00"+instrument.Environment]; !ok {
			return fmt.Errorf("instrument references unknown Gateway venue")
		}
		if instrument.Product != "spot" && instrument.Product != "perp" {
			return fmt.Errorf("Gateway instrument product is unsupported")
		}
		if err := validateRules(instrument.Rules); err != nil {
			return err
		}
		key := strings.Join([]string{instrument.Venue, instrument.Environment, instrument.Product, instrument.InstrumentID}, "\x00")
		if _, exists := instruments[key]; exists {
			return fmt.Errorf("duplicate Gateway instrument %q", instrument.InstrumentID)
		}
		instruments[key] = struct{}{}
	}
	return nil
}

func validateRules(rules TradeRules) error {
	for name, value := range map[string]string{
		"price increment": rules.PriceIncrement, "quantity increment": rules.QuantityIncrement, "minimum quantity": rules.MinQuantity,
	} {
		parsed, err := decimal.NewFromString(value)
		if err != nil || !parsed.IsPositive() {
			return fmt.Errorf("Gateway instrument %s is invalid", name)
		}
	}
	if rules.MinNotional != "" {
		parsed, err := decimal.NewFromString(rules.MinNotional)
		if err != nil || !parsed.IsPositive() {
			return fmt.Errorf("Gateway instrument minimum notional is invalid")
		}
	}
	return nil
}

func clone(snapshot Snapshot) Snapshot {
	snapshot.Venues = append([]Venue(nil), snapshot.Venues...)
	snapshot.Instruments = append([]Instrument(nil), snapshot.Instruments...)
	return snapshot
}
