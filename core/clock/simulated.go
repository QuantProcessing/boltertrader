package clock

import (
	"sort"
	"sync"
	"time"
)

// SimulatedClock is a virtual Clock for backtesting. Time does not pass on its
// own: the backtest engine moves it forward with Advance / AdvanceTo as it
// replays historical data, and any timers due in that interval fire in deadline
// order, each stamped with its exact deadline (not the post-advance now). This
// gives deterministic, reproducible event ordering.
//
// SimulatedClock is safe for concurrent use, but note that timers fire
// synchronously inside Advance/AdvanceTo on the calling goroutine.
type SimulatedClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*simTimer
}

// NewSimulatedClock returns a SimulatedClock positioned at start.
func NewSimulatedClock(start time.Time) *SimulatedClock {
	return &SimulatedClock{now: start}
}

func (c *SimulatedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *SimulatedClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &simTimer{
		deadline: c.now.Add(d),
		ch:       make(chan time.Time, 1),
		clock:    c,
	}
	if d <= 0 {
		// Already due: fire immediately with current virtual time.
		t.fire(c.now)
		return t
	}
	c.timers = append(c.timers, t)
	return t
}

func (c *SimulatedClock) After(d time.Duration) <-chan time.Time {
	return c.NewTimer(d).C()
}

// Sleep blocks until the simulated clock is advanced past now+d by another
// goroutine. With d <= 0 it returns immediately.
func (c *SimulatedClock) Sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	<-c.After(d)
}

// Advance moves the clock forward by d, firing all timers that come due, in
// deadline order.
func (c *SimulatedClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	c.AdvanceTo(target)
}

// AdvanceTo moves the clock to t (no-op if t is not after now), firing all
// timers with deadline <= t in deadline order. Each fired timer receives its
// own deadline on its channel.
func (c *SimulatedClock) AdvanceTo(t time.Time) {
	c.mu.Lock()
	if !t.After(c.now) {
		c.mu.Unlock()
		return
	}

	// Collect due timers and retain the rest.
	var due []*simTimer
	remaining := c.timers[:0:0]
	for _, tm := range c.timers {
		if !tm.deadline.After(t) {
			due = append(due, tm)
		} else {
			remaining = append(remaining, tm)
		}
	}
	c.timers = remaining
	c.now = t
	c.mu.Unlock()

	sort.SliceStable(due, func(i, j int) bool {
		return due[i].deadline.Before(due[j].deadline)
	})
	for _, tm := range due {
		tm.fire(tm.deadline)
	}
}

type simTimer struct {
	deadline time.Time
	ch       chan time.Time
	clock    *SimulatedClock
	mu       sync.Mutex
	stopped  bool
	fired    bool
}

func (t *simTimer) C() <-chan time.Time { return t.ch }

func (t *simTimer) fire(at time.Time) {
	t.mu.Lock()
	if t.stopped || t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	t.mu.Unlock()
	// Channel is buffered (cap 1); non-blocking send.
	select {
	case t.ch <- at:
	default:
	}
}

// Stop removes the timer from its clock. It returns true if the timer had not
// yet fired.
func (t *simTimer) Stop() bool {
	t.mu.Lock()
	already := t.fired || t.stopped
	t.stopped = true
	t.mu.Unlock()
	if already {
		return false
	}
	// Detach from the clock's pending set.
	c := t.clock
	c.mu.Lock()
	for i, tm := range c.timers {
		if tm == t {
			c.timers = append(c.timers[:i], c.timers[i+1:]...)
			break
		}
	}
	c.mu.Unlock()
	return true
}
