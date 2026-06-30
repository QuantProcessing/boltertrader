package backtest

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/model"
)

// Stepper is the synchronous drain surface a TradingNode exposes
// (ProcessAvailable). The Runner drives it in lock-step with replayed data so
// the whole backtest runs deterministically on one goroutine.
type Stepper interface {
	ProcessAvailable()
}

// Runner replays a time-ordered slice of trade ticks through a Venue, draining
// the node to quiescence after each tick. Single-threaded and deterministic.
type Runner struct {
	venue *Venue
}

// NewRunner builds a Runner for a venue.
func NewRunner(v *Venue) *Runner { return &Runner{venue: v} }

// Run feeds every tick (assumed sorted by Timestamp ascending) into the venue,
// calling step.ProcessAvailable after each so the node fully reacts — including
// placing and matching orders — before the next tick. Returns when ctx is
// cancelled or all ticks are fed.
func (r *Runner) Run(ctx context.Context, step Stepper, ticks []model.TradeTick) {
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
