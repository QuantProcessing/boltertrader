package backtest

import (
	"context"
	"sort"

	"github.com/QuantProcessing/boltertrader/core/model"
)

// Stepper is the synchronous drain surface a TradingNode exposes
// (ProcessAvailable). The Runner drives it in lock-step with replayed data so
// the whole backtest runs deterministically on one goroutine.
type Stepper interface {
	ProcessAvailable()
}

// Runner replays a time-ordered stream of SimEvents through a Venue, draining
// the node to quiescence after each one. Single-threaded and deterministic.
type Runner struct {
	venue *Venue
}

// NewRunner builds a Runner for a venue.
func NewRunner(v *Venue) *Runner { return &Runner{venue: v} }

// Run feeds every event into the venue in time order, calling
// step.ProcessAvailable after each so the node fully reacts — including placing
// and matching orders — before the next event. The events are stably sorted by
// timestamp first (on a copy; the caller's slice is untouched). Returns when ctx
// is cancelled or all events are fed.
func (r *Runner) Run(ctx context.Context, step Stepper, events []SimEvent) {
	sorted := make([]SimEvent, len(events))
	copy(sorted, events)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Timestamp().Before(sorted[j].Timestamp())
	})
	for _, ev := range sorted {
		select {
		case <-ctx.Done():
			return
		default:
		}
		ev.feed(r.venue)
		step.ProcessAvailable()
	}
}

// RunTrades is the trade-only convenience: it replays a slice of trade ticks
// (assumed already sorted by Timestamp ascending) with the same per-event
// draining as Run.
func (r *Runner) RunTrades(ctx context.Context, step Stepper, ticks []model.TradeTick) {
	for _, t := range ticks {
		select {
		case <-ctx.Done():
			return
		default:
		}
		r.venue.Feed(t)
		step.ProcessAvailable()
	}
}
