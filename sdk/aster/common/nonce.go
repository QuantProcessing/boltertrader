package common

import (
	"sync"
	"time"
)

type Clock interface {
	Now() time.Time
}

type ClockFunc func() time.Time

func (f ClockFunc) Now() time.Time { return f() }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type NonceCoordinator struct {
	mu    sync.Mutex
	clock Clock
	last  int64
}

func NewNonceCoordinator(clock Clock) *NonceCoordinator {
	if clock == nil {
		clock = systemClock{}
	}
	return &NonceCoordinator{clock: clock}
}

func (n *NonceCoordinator) Next() int64 {
	return n.nextAt(n.clock.Now())
}

func (n *NonceCoordinator) nextAt(now time.Time) int64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	next := now.UnixMicro()
	if next <= n.last {
		next = n.last + 1
	}
	n.last = next
	return next
}
