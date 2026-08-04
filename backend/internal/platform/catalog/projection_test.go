package catalog

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingSource struct {
	mu       sync.Mutex
	calls    int
	snapshot Snapshot
}

func (source *countingSource) Fetch(context.Context) (Snapshot, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.snapshot, nil
}

func TestProjectionCoalescesGatewayFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision:    "gateway-rev-1",
		Venues:      []Venue{{Venue: "binance", Environment: "testnet", Ready: true}},
		Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Ready: true}},
	}}
	projection, err := NewProjection(source, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	const consumers = 12
	var wait sync.WaitGroup
	wait.Add(consumers)
	for range consumers {
		go func() {
			defer wait.Done()
			snapshot, fetchErr := projection.Snapshot(context.Background())
			if fetchErr != nil || snapshot.Revision != "gateway-rev-1" {
				t.Errorf("Snapshot() = %#v, %v", snapshot, fetchErr)
			}
		}()
	}
	wait.Wait()
	if source.calls != 1 {
		t.Fatalf("Gateway fetch calls = %d, want 1", source.calls)
	}

	status := projection.Status()
	if !status.Ready || status.Revision != "gateway-rev-1" || status.Age != 0 {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestProjectionRejectsUntrustedCatalogFacts(t *testing.T) {
	source := &countingSource{snapshot: Snapshot{
		Venues:      []Venue{{Venue: "binance", Environment: "testnet", Ready: true}},
		Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "okx", Ready: true}},
	}}
	projection, err := NewProjection(source, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil, want invalid Gateway facts")
	}
	if status := projection.Status(); status.Ready || status.Reason != ReasonInvalidFacts {
		t.Fatalf("Status() = %#v", status)
	}
}
