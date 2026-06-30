package clock

import (
	"testing"
	"time"
)

// staticInterfaceCheck ensures both impls satisfy Clock.
var (
	_ Clock = (*RealClock)(nil)
	_ Clock = (*SimulatedClock)(nil)
)

func TestRealClock_Sleep(t *testing.T) {
	c := NewRealClock()
	start := c.Now()
	c.Sleep(5 * time.Millisecond)
	if c.Now().Sub(start) < 5*time.Millisecond {
		t.Fatalf("RealClock.Sleep returned too early")
	}
}

func TestSimulatedClock_NowAndAdvance(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewSimulatedClock(start)
	if !c.Now().Equal(start) {
		t.Fatalf("Now=%v, want %v", c.Now(), start)
	}
	c.Advance(time.Hour)
	if got := c.Now(); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("after Advance Now=%v, want %v", got, start.Add(time.Hour))
	}
}

// TestSimulatedClock_TimerOrder is the core parity test: timers fire in
// deadline order, each stamped with its own deadline, when the clock advances
// past them in a single step.
func TestSimulatedClock_TimerOrder(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewSimulatedClock(start)

	t3 := c.NewTimer(3 * time.Second)
	t1 := c.NewTimer(1 * time.Second)
	t2 := c.NewTimer(2 * time.Second)

	// Nothing should have fired yet.
	for _, tm := range []Timer{t1, t2, t3} {
		select {
		case <-tm.C():
			t.Fatal("timer fired before advance")
		default:
		}
	}

	c.Advance(5 * time.Second)

	want := []struct {
		tm   Timer
		when time.Time
	}{
		{t1, start.Add(1 * time.Second)},
		{t2, start.Add(2 * time.Second)},
		{t3, start.Add(3 * time.Second)},
	}
	for i, w := range want {
		select {
		case got := <-w.tm.C():
			if !got.Equal(w.when) {
				t.Errorf("timer %d fired with %v, want deadline %v", i, got, w.when)
			}
		default:
			t.Errorf("timer %d did not fire", i)
		}
	}
}

func TestSimulatedClock_StopBeforeFire(t *testing.T) {
	c := NewSimulatedClock(time.Unix(0, 0))
	tm := c.NewTimer(time.Second)
	if !tm.Stop() {
		t.Fatal("Stop on a pending timer should return true")
	}
	c.Advance(2 * time.Second)
	select {
	case <-tm.C():
		t.Fatal("stopped timer fired")
	default:
	}
	// Second stop returns false.
	if tm.Stop() {
		t.Fatal("Stop on an already-stopped timer should return false")
	}
}

func TestSimulatedClock_AdvanceToPastOnly(t *testing.T) {
	start := time.Unix(100, 0)
	c := NewSimulatedClock(start)
	c.AdvanceTo(start.Add(-time.Second)) // backwards: no-op
	if !c.Now().Equal(start) {
		t.Fatalf("AdvanceTo backwards moved clock to %v", c.Now())
	}
}

func TestSimulatedClock_ZeroTimerFiresImmediately(t *testing.T) {
	c := NewSimulatedClock(time.Unix(0, 0))
	tm := c.NewTimer(0)
	select {
	case <-tm.C():
	default:
		t.Fatal("zero-duration timer should fire immediately")
	}
}

func TestSimulatedClock_SleepWakesOnAdvance(t *testing.T) {
	c := NewSimulatedClock(time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		c.Sleep(time.Second)
		close(done)
	}()
	// Give the goroutine a moment to register its timer, then advance.
	time.Sleep(10 * time.Millisecond)
	c.Advance(time.Second)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Sleep did not wake after Advance")
	}
}
