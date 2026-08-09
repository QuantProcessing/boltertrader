package publicconn

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type countingFactory struct {
	next   Factory
	mu     sync.Mutex
	builds map[routeprofile.Key]int
}

func (factory *countingFactory) Build(ctx context.Context, key routeprofile.Key) (Connector, error) {
	factory.mu.Lock()
	if factory.builds == nil {
		factory.builds = make(map[routeprofile.Key]int)
	}
	factory.builds[key]++
	factory.mu.Unlock()
	return factory.next.Build(ctx, key)
}

func (factory *countingFactory) BuildCalls(key routeprofile.Key) int {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.builds[key]
}
