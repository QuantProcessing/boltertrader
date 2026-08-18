package marketdata

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type boardSourceFunc func(context.Context) (BoardSnapshot, error)

func (fn boardSourceFunc) FetchFundingBoard(ctx context.Context) (BoardSnapshot, error) {
	return fn(ctx)
}

func healthyBoard(observedAt time.Time) BoardSnapshot {
	return BoardSnapshot{
		ObservedAt: observedAt,
		Venues: []BoardVenue{
			{
				Venue: "binance", Environment: "live", Product: "perp",
				RouteProfileID: "binance.perp.usdt.v1", Settlement: "USDT",
				ObservedAt: observedAt,
				Rows: []BoardRow{{
					Symbol: "BTC-USDT", Rate: "0.0001", IntervalHours: 8,
					NextFundingTime: observedAt.Add(2 * time.Hour),
				}},
			},
			{
				Venue: "okx", Environment: "live", Product: "perp",
				RouteProfileID: "okx.perp.usdt.v1", Settlement: "USDT",
				Unavailable: true, Rows: []BoardRow{},
			},
		},
	}
}

// The board is the one read on this surface with no parameters, so every
// visitor asks the identical question at once. That makes the cache and its
// single-flight the whole point of the type: without them the page's poll
// interval would set the Gateway's read rate directly, multiplied by the number
// of people looking at it.
func TestBoardServiceServesConcurrentReadersFromOneFetch(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	release := make(chan struct{})
	var fetches int
	var mu sync.Mutex
	source := boardSourceFunc(func(context.Context) (BoardSnapshot, error) {
		mu.Lock()
		fetches++
		mu.Unlock()
		<-release
		return healthyBoard(now.Add(-time.Second)), nil
	})
	budget, err := NewCoordinator(10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBoardService(source, budget, 15*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	const readers = 8
	results := make(chan error, readers)
	for range readers {
		go func() {
			snapshot, err := service.Get(context.Background())
			if err == nil && len(snapshot.Venues) != 2 {
				err = errors.New("board lost a column")
			}
			results <- err
		}()
	}
	// Let every reader reach the service before the fetch returns, so the ones
	// that did not win the slot are provably waiting rather than merely late.
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range readers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if fetches != 1 {
		t.Fatalf("fetches = %d, want 1", fetches)
	}
	if status := service.BudgetStatus(); status.Used != 1 {
		t.Fatalf("budget used = %d, want 1", status.Used)
	}
}

func TestBoardServiceRefetchesOnlyAfterTheTTL(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	fetches := 0
	source := boardSourceFunc(func(context.Context) (BoardSnapshot, error) {
		fetches++
		return healthyBoard(clock), nil
	})
	budget, err := NewCoordinator(10, time.Minute, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBoardService(source, budget, 15*time.Second, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15 * time.Second)
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fetches != 1 {
		t.Fatalf("fetches at the TTL boundary = %d, want 1", fetches)
	}
	clock = clock.Add(time.Millisecond)
	if _, err := service.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fetches != 2 {
		t.Fatalf("fetches past the TTL = %d, want 2", fetches)
	}
}

// A failed fetch must not be remembered as an answer, and must not leave the
// single-flight slot held: the next reader has to be able to try again rather
// than inherit the failure or block on a fetch that will never come.
func TestBoardServiceDoesNotCacheOrStrandOnFailure(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	fail := true
	source := boardSourceFunc(func(context.Context) (BoardSnapshot, error) {
		if fail {
			return BoardSnapshot{}, errors.New("gateway is down")
		}
		return healthyBoard(now), nil
	})
	budget, err := NewCoordinator(10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBoardService(source, budget, 15*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background()); err == nil {
		t.Fatal("failed fetch was reported as success")
	}
	fail = false
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatalf("second read inherited the failure: %v", err)
	}
	if len(snapshot.Venues) != 2 {
		t.Fatalf("venues = %d, want 2", len(snapshot.Venues))
	}
}

// The Gateway stamps the board with its own clock, so a projection that
// re-judged the whole table's freshness against its TTL would hide nine healthy
// columns whenever two containers disagreed by a few seconds. Per-column
// staleness is the truth that travels; only claims the projection cannot
// reconcile at all are refused.
func TestValidateBoardKeepsStaleColumnsAndRefusesIncoherentOnes(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	stale := healthyBoard(now.Add(-90 * time.Second))
	stale.Venues[0].Stale = true
	if err := validateBoard(stale, now); err != nil {
		t.Fatalf("a stale but coherent board was refused: %v", err)
	}

	for name, mutate := range map[string]func(*BoardSnapshot){
		"no observation":       func(board *BoardSnapshot) { board.ObservedAt = time.Time{} },
		"future dated":         func(board *BoardSnapshot) { board.ObservedAt = now.Add(time.Minute) },
		"no columns":           func(board *BoardSnapshot) { board.Venues = nil },
		"spot column":          func(board *BoardSnapshot) { board.Venues[0].Product = "spot" },
		"unnamed column":       func(board *BoardSnapshot) { board.Venues[0].Venue = " " },
		"unstamped column":     func(board *BoardSnapshot) { board.Venues[0].ObservedAt = time.Time{} },
		"unsymboled row":       func(board *BoardSnapshot) { board.Venues[0].Rows[0].Symbol = "" },
		"unparseable rate":     func(board *BoardSnapshot) { board.Venues[0].Rows[0].Rate = "eight" },
		"negative interval":    func(board *BoardSnapshot) { board.Venues[0].Rows[0].IntervalHours = -8 },
		"repeated profile":     func(board *BoardSnapshot) { board.Venues[1].RouteProfileID = board.Venues[0].RouteProfileID },
		"unavailable but full": func(board *BoardSnapshot) { board.Venues[1].Rows = board.Venues[0].Rows },
	} {
		t.Run(name, func(t *testing.T) {
			board := healthyBoard(now.Add(-time.Second))
			board.Venues = append([]BoardVenue(nil), board.Venues...)
			board.Venues[0].Rows = append([]BoardRow(nil), board.Venues[0].Rows...)
			mutate(&board)
			if err := validateBoard(board, now); err == nil {
				t.Fatal("incoherent board accepted")
			}
		})
	}
}

// Every cell on the board is read against its neighbour, so one column arriving
// in a venue's local offset would render as a settlement-clock spread that does
// not exist.
func TestBoardServiceNormalisesEveryTimeToUTC(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	source := boardSourceFunc(func(context.Context) (BoardSnapshot, error) {
		board := healthyBoard(now.Add(-time.Second))
		board.ObservedAt = board.ObservedAt.In(zone)
		board.Venues[0].ObservedAt = board.Venues[0].ObservedAt.In(zone)
		board.Venues[0].Rows[0].NextFundingTime = board.Venues[0].Rows[0].NextFundingTime.In(zone)
		return board, nil
	})
	budget, err := NewCoordinator(10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewBoardService(source, budget, 15*time.Second, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []time.Time{
		snapshot.ObservedAt,
		snapshot.Venues[0].ObservedAt,
		snapshot.Venues[0].Rows[0].NextFundingTime,
	} {
		if stamp.Location() != time.UTC {
			t.Fatalf("stamp %s is in %s, want UTC", stamp, stamp.Location())
		}
	}
	// The unavailable column carries no times at all; normalising must leave its
	// zero a zero, because omitzero is what keeps it off the wire.
	if !snapshot.Venues[1].ObservedAt.IsZero() {
		t.Fatal("normalisation invented an observation time for an unavailable column")
	}
}
