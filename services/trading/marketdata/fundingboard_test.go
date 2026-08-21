package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

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

func TestBoardServiceSerializesUnavailableVenueRowsAsArray(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewBoardService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	board := healthyBoard(now)
	board.Venues[1].Rows = nil
	if err := service.Replace(board); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) == "" || !strings.Contains(string(wire), `"rows":[]`) {
		t.Fatalf("unavailable venue rows are not an array: %s", wire)
	}
}

func TestBoardServiceServesConcurrentReadersFromLocalProjection(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewBoardService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background()); !errors.Is(err, ErrFundingBoardNotReady) {
		t.Fatalf("cold projection = %v, want ErrFundingBoardNotReady", err)
	}
	if err := service.Replace(healthyBoard(now.Add(-time.Second))); err != nil {
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
	for range readers {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}

}

func TestBoardServiceReflectsTheNextStreamReplacement(t *testing.T) {
	clock := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewBoardService(func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	first := healthyBoard(clock)
	first.Venues[0].Rows[0].Rate = "0.0001"
	if err := service.Replace(first); err != nil {
		t.Fatal(err)
	}
	next := healthyBoard(clock.Add(time.Second))
	next.Venues[0].Rows[0].Rate = "0.0002"
	clock = clock.Add(time.Second)
	if err := service.Replace(next); err != nil {
		t.Fatal(err)
	}
	got, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Venues[0].Rows[0].Rate != "0.0002" {
		t.Fatalf("rate = %s, want stream replacement", got.Venues[0].Rows[0].Rate)
	}
}

func TestBoardServiceRejectsInvalidReplacementWithoutLosingLastSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewBoardService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Replace(healthyBoard(now)); err != nil {
		t.Fatal(err)
	}
	invalid := healthyBoard(now)
	invalid.Venues[0].Rows[0].Rate = "invalid"
	if err := service.Replace(invalid); err == nil {
		t.Fatal("invalid stream snapshot was accepted")
	}
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatalf("last valid snapshot was lost: %v", err)
	}
	if snapshot.Venues[0].Rows[0].Rate != "0.0001" {
		t.Fatalf("last valid rate = %s", snapshot.Venues[0].Rows[0].Rate)
	}
}

func TestBoardServiceAcceptsPriceFactWithoutFundingRate(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service, err := NewBoardService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	board := healthyBoard(now)
	board.Venues[0].Rows[0].Rate = ""
	board.Venues[0].Rows[0].IntervalHours = 0
	board.Venues[0].Rows[0].MarkPrice = "100.25"
	board.Venues[0].Rows[0].PriceObservedAt = now
	if err := service.Replace(board); err != nil {
		t.Fatalf("price-only fact was refused: %v", err)
	}
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Venues[0].Rows[0].Rate != "" || snapshot.Venues[0].Rows[0].MarkPrice != "100.25" {
		t.Fatalf("price-only fact changed in projection: %+v", snapshot.Venues[0].Rows[0])
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
	service, err := NewBoardService(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	board := healthyBoard(now.Add(-time.Second))
	board.ObservedAt = board.ObservedAt.In(zone)
	board.Venues[0].ObservedAt = board.Venues[0].ObservedAt.In(zone)
	board.Venues[0].Rows[0].NextFundingTime = board.Venues[0].Rows[0].NextFundingTime.In(zone)
	if err := service.Replace(board); err != nil {
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
