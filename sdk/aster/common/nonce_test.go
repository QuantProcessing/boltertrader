package common

import (
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type atomicClock struct {
	micros atomic.Int64
}

func (c *atomicClock) Now() time.Time {
	return time.UnixMicro(c.micros.Load())
}

func TestNonceCoordinatorIsMonotonicAcrossConcurrentCallers(t *testing.T) {
	clock := &atomicClock{}
	clock.micros.Store(1_748_310_859_508_867)
	nonces := NewNonceCoordinator(clock)

	const count = 1000
	values := make([]int64, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			values[index] = nonces.Next()
		}(i)
	}
	wg.Wait()

	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	for i, value := range values {
		want := int64(1_748_310_859_508_867 + i)
		if value != want {
			t.Fatalf("nonce[%d] = %d, want %d", i, value, want)
		}
	}
}

func TestNonceCoordinatorSurvivesClockRegression(t *testing.T) {
	clock := &atomicClock{}
	clock.micros.Store(200)
	nonces := NewNonceCoordinator(clock)
	if got := nonces.Next(); got != 200 {
		t.Fatalf("first nonce = %d", got)
	}
	clock.micros.Store(100)
	if got := nonces.Next(); got != 201 {
		t.Fatalf("regressed-clock nonce = %d, want 201", got)
	}
}
