// Package clock provides the time seam that makes backtest and live execution
// identical. Every adapter and runtime component takes time ONLY through a
// Clock — never time.Now() directly — so a backtest can drive a SimulatedClock
// while live code uses a RealClock, with no other change to the call sites.
package clock

import "time"

// Timer is the subset of *time.Timer the runtime relies on. A SimulatedClock
// returns a virtual timer that fires when the simulated clock is advanced past
// its deadline.
type Timer interface {
	// C is the channel on which the time is delivered when the timer fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing. It returns true if it stopped the
	// timer before it fired.
	Stop() bool
}

// Clock is the injectable source of time. Order timestamps, TIF expiry,
// rate-limit windows, and request deadlines all flow through it.
type Clock interface {
	// Now returns the current time (real or simulated).
	Now() time.Time
	// NewTimer creates a Timer that fires once after d.
	NewTimer(d time.Duration) Timer
	// After is shorthand for NewTimer(d).C().
	After(d time.Duration) <-chan time.Time
	// Sleep blocks for d as measured by this clock.
	Sleep(d time.Duration)
}
