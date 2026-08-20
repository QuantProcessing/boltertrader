package marketdata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxFundingHistoryPoints bounds one series. Thirty-one days is the widest span
// the Gateway will resolve and the shortest settlement on the board is hourly,
// so 744 points is the arithmetic ceiling; the round number above it leaves room
// for a venue that back-fills a correction without making the bound read as if
// it were fitted to one exchange.
const MaxFundingHistoryPoints = 1000

// ErrFundingHistoryUnsupported says the venue publishes no settled series and
// never will, as distinct from the projection being unready.
//
// The distinction is the whole reason this sentinel exists. One of the ten board
// columns has no history endpoint at all, and without a permanent answer the
// browser would retry that leg forever behind a spinner that never resolves.
// The interface has to be able to say "this venue does not publish it" instead.
var ErrFundingHistoryUnsupported = errors.New("venue publishes no settled funding history")

// ErrFundingHistoryUnknownInstrument says this venue does publish settled
// series, but not for this symbol — it is not on the venue's column of the
// board.
//
// Separate from ErrFundingHistoryUnsupported because the two are separate facts
// and only one of them is about the venue. Flattened together, a pair page would
// tell an operator that bybit publishes no funding history, which is false and
// checkable, on the strength of one symbol bybit happens not to list.
var ErrFundingHistoryUnknownInstrument = errors.New("venue does not list this instrument")

// FundingWindow is the only way to ask for a span.
//
// The browser names a window and the service resolves the name against its own
// clock, rather than the browser sending two instants. That is the constraint
// the whole read rests on: every venue's history endpoint is paginated, and
// these two windows are the two that fit in a single page at every venue on the
// board. Accepting an arbitrary range would mean either silently truncating the
// answer or teaching seven adapters to walk pages for a chart nobody asked to be
// that wide.
type FundingWindow string

const (
	FundingWindow7d  FundingWindow = "7d"
	FundingWindow30d FundingWindow = "30d"
)

// ParseFundingWindow refuses anything that is not one of the two literals,
// including the ones that look reasonable — "1d", "90d". A window this surface
// does not stand behind has to fail at the edge rather than reach a venue.
func ParseFundingWindow(raw string) (FundingWindow, error) {
	switch window := FundingWindow(strings.TrimSpace(raw)); window {
	case FundingWindow7d, FundingWindow30d:
		return window, nil
	default:
		return "", fmt.Errorf("funding history window must be %q or %q", FundingWindow7d, FundingWindow30d)
	}
}

func (window FundingWindow) span() (time.Duration, error) {
	switch window {
	case FundingWindow7d:
		return 7 * 24 * time.Hour, nil
	case FundingWindow30d:
		return 30 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("funding history window %q is unsupported", string(window))
	}
}

// FundingHistoryQuery is what the browser asks for: one leg of a spread, over a
// named window.
type FundingHistoryQuery struct {
	InstrumentID string
	Venue        string
	Window       FundingWindow
}

func (query FundingHistoryQuery) validate() error {
	if strings.TrimSpace(query.InstrumentID) == "" || strings.TrimSpace(query.Venue) == "" {
		return fmt.Errorf("instrumentId and resolved venue are required")
	}
	_, err := query.Window.span()
	return err
}

// FundingHistoryRead is what the Gateway is asked for: the same leg, with the
// window already resolved into instants.
//
// It is a separate type from FundingHistoryQuery rather than two more fields on
// it because the resolution happens here and nowhere else. A source handed the
// window would have to resolve it against some other clock, and two clocks
// answering the same question is how a test that passes at noon fails at a
// settlement boundary.
type FundingHistoryRead struct {
	InstrumentID string
	Venue        string
	Since        time.Time
	Until        time.Time
}

// FundingHistorySeries is what the browser receives, and it is also exactly what
// the Gateway returns: the trading client decodes the internal plane with
// unknown fields disallowed, so these tags are pinned to that projection rather
// than chosen here.
type FundingHistorySeries struct {
	InstrumentID string                `json:"instrumentId"`
	Venue        string                `json:"venue"`
	Since        time.Time             `json:"since"`
	Until        time.Time             `json:"until"`
	ObservedAt   time.Time             `json:"observedAt"`
	Points       []FundingHistoryPoint `json:"points"`
}

// FundingHistoryPoint is one settled funding payment.
//
// IntervalHours is optional because it is measured from the gaps between
// settlements rather than declared by anyone: a venue that published a single
// point inside the window has nothing to measure it from, and a series that
// guessed would mis-annualise every rate drawn from it.
type FundingHistoryPoint struct {
	FundingTime   time.Time `json:"fundingTime"`
	Rate          string    `json:"rate"`
	IntervalHours int64     `json:"intervalHours,omitempty"`
}

type FundingHistorySource interface {
	FetchFundingHistory(context.Context, FundingHistoryRead) (FundingHistorySeries, error)
}

// FundingHistoryService fronts the Gateway's funding-history read.
//
// Like CandleService and unlike BoardService it keeps no cache. The board is one
// parameterless document that every visitor to the matrix asks for at once; a
// history series is one canonical instrument over one window, fetched when
// someone opens a pair. Caching it would mean a map keyed by instrument that grows
// with the instrument universe, to serve a curve that only changes when the
// venue settles. The browser's query cache already holds it for as long as the
// page is open, and the shared venue budget is what stops a reload storm from
// spending the poller's allowance.
type FundingHistoryService struct {
	source FundingHistorySource
	budget *Coordinator
	now    func() time.Time
}

func NewFundingHistoryService(source FundingHistorySource, budget *Coordinator, now func() time.Time) (*FundingHistoryService, error) {
	if source == nil || budget == nil || now == nil {
		return nil, fmt.Errorf("funding history source, budget, and clock are required")
	}
	return &FundingHistoryService{source: source, budget: budget, now: now}, nil
}

func (service *FundingHistoryService) Get(ctx context.Context, query FundingHistoryQuery) (FundingHistorySeries, error) {
	if ctx == nil {
		return FundingHistorySeries{}, fmt.Errorf("funding history context is required")
	}
	if err := query.validate(); err != nil {
		return FundingHistorySeries{}, err
	}
	span, err := query.Window.span()
	if err != nil {
		return FundingHistorySeries{}, err
	}
	// Truncated to the second because the bounds travel as RFC 3339 and come
	// back echoed: a nanosecond the wire format cannot carry would fail the
	// window check below against the very value that produced it.
	until := service.now().UTC().Truncate(time.Second)
	read := FundingHistoryRead{
		InstrumentID: query.InstrumentID, Venue: query.Venue,
		Since: until.Add(-span), Until: until,
	}
	if err := service.budget.Acquire(ctx, query.Venue); err != nil {
		return FundingHistorySeries{}, err
	}
	series, err := service.source.FetchFundingHistory(ctx, read)
	if err != nil {
		return FundingHistorySeries{}, err
	}
	if err := validateFundingHistory(read, series); err != nil {
		return FundingHistorySeries{}, err
	}
	return normalizeFundingHistory(series), nil
}

func (service *FundingHistoryService) BudgetStatus(venue string) BudgetStatus {
	return service.budget.Status(venue)
}

// validateFundingHistory refuses a series a chart cannot draw, or one that would
// draw the wrong thing.
//
// The identity check earns its place even though the Gateway makes the same one:
// two of these series are drawn together as the legs of a spread, so a response
// that arrived under the wrong venue renders as a plausible curve in the wrong
// colour rather than as a visible gap. The window check is the same argument one
// step out — a series answered over a span other than the one requested puts
// both legs on one axis at different scales.
//
// The ascending check is the requirement the candle page has for the same
// reason: the chart library throws on unordered timestamps, which surfaces as a
// blank panel and a stack trace instead of an error anyone can act on.
func validateFundingHistory(read FundingHistoryRead, series FundingHistorySeries) error {
	if series.InstrumentID != read.InstrumentID || series.Venue != read.Venue {
		return fmt.Errorf("Gateway funding history identity mismatch")
	}
	if !series.Since.Equal(read.Since) || !series.Until.Equal(read.Until) {
		return fmt.Errorf("Gateway funding history window mismatch")
	}
	if series.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway funding history observation time is missing")
	}
	if len(series.Points) > MaxFundingHistoryPoints {
		return fmt.Errorf("Gateway funding history exceeds the window bound")
	}
	for index, point := range series.Points {
		if point.FundingTime.IsZero() {
			return fmt.Errorf("Gateway funding history settlement time is missing")
		}
		if strings.TrimSpace(point.Rate) == "" {
			return fmt.Errorf("Gateway funding history rate is missing")
		}
		if point.IntervalHours < 0 {
			return fmt.Errorf("Gateway funding history interval is negative")
		}
		if index > 0 && !point.FundingTime.After(series.Points[index-1].FundingTime) {
			return fmt.Errorf("Gateway funding history is not in ascending settlement order")
		}
	}
	return nil
}

// normalizeFundingHistory puts every instant on UTC, so two legs quoted by
// venues on different clocks share one axis.
func normalizeFundingHistory(series FundingHistorySeries) FundingHistorySeries {
	series.Since = series.Since.UTC()
	series.Until = series.Until.UTC()
	series.ObservedAt = series.ObservedAt.UTC()
	for index := range series.Points {
		series.Points[index].FundingTime = series.Points[index].FundingTime.UTC()
	}
	return series
}
