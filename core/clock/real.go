package clock

import "time"

// RealClock is the production Clock backed by the wall clock. It is used by all
// live adapters and the live runtime.
type RealClock struct{}

// NewRealClock returns a Clock backed by package time.
func NewRealClock() *RealClock { return &RealClock{} }

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (RealClock) Sleep(d time.Duration) { time.Sleep(d) }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }
