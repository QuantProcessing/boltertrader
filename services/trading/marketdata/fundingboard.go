package marketdata

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
)

// The board's bounds. They are ceilings on what a healthy Gateway could
// possibly say, not on what it usually says: eleven perp route profiles exist
// per mode and the largest venue lists a few hundred perpetuals, so a response
// past either bound came from something other than the Gateway we think we are
// talking to.
const (
	MaxBoardVenues   = 32
	MaxBoardRowsEach = 2000
)

// BoardSnapshot is the cross-venue funding matrix.
//
// It is a projection of its own rather than a slice of Fact because the two
// answer different questions. Fact is one instrument on one venue, read through
// a configured account's authenticated client; the board is every venue at
// once, read from public endpoints, and is the one market view an operator has
// before configuring any account at all. Reusing Fact would mean either an
// empty board on a fresh install or a board whose cells silently require
// credentials.
// The board used to lead with the mode it was assembled under, so a browser
// could not draw a test board against a live account. One mode is left, and the
// Gateway no longer sends the field.
type BoardSnapshot struct {
	ObservedAt time.Time    `json:"observedAt"`
	Venues     []BoardVenue `json:"venues"`
}

// BoardVenue is one column.
//
// Stale and Unavailable both travel because they are different answers to "may
// I compare this number against the next column": stale means these are the
// venue's last known rates and older than the rest of the table, unavailable
// means there is nothing recent enough to put beside another venue's number.
// An unavailable column carries no rows, which validateBoard enforces — a
// three-minute-old rate rendered next to fresh ones is an invitation to trade a
// spread that has already closed.
type BoardVenue struct {
	Venue          string     `json:"venue"`
	Environment    string     `json:"environment"`
	Product        string     `json:"product"`
	Settlement     string     `json:"settlement,omitempty"`
	RouteProfileID string     `json:"routeProfileId"`
	ObservedAt     time.Time  `json:"observedAt,omitzero"`
	Stale          bool       `json:"stale"`
	Unavailable    bool       `json:"unavailable"`
	Rows           []BoardRow `json:"rows"`
}

// BoardRow is one instrument's rate on one venue.
//
// Every optional cell carries omitempty, and the two times carry omitzero,
// because an absent cell has to reach the browser absent rather than as a zero
// the venue never quoted: seven of the ten funding venues leave open interest
// out of their bulk call, and hyperliquid and lighter publish a bare rate with
// no settlement clock. omitempty alone would not do it for the times —
// it does not consult time.Time.IsZero, so a zero would ship as
// "0001-01-01T00:00:00Z" and render as a countdown that expired two thousand
// years ago.
type BoardRow struct {
	Symbol          string    `json:"symbol"`
	NativeSymbol    string    `json:"nativeSymbol,omitempty"`
	NativeID        string    `json:"nativeId,omitempty"`
	BaseAsset       string    `json:"baseAsset,omitempty"`
	QuoteAsset      string    `json:"quoteAsset,omitempty"`
	Rate            string    `json:"rate"`
	IntervalHours   int64     `json:"intervalHours,omitempty"`
	NextFundingTime time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice       string    `json:"markPrice,omitempty"`
	IndexPrice      string    `json:"indexPrice,omitempty"`
	OpenInterest    string    `json:"openInterest,omitempty"`
}

type BoardSource interface {
	FetchFundingBoard(context.Context) (BoardSnapshot, error)
}

// BoardBudgetKey is the name the board's reads are counted under.
//
// It is not a venue. A board read costs the Gateway one call, and the Gateway
// serves it from columns it refreshes on its own half-minute clock no matter
// how often trading asks — so charging the read to binance or okx would deduct
// tokens that were never spent there and would let a board poller starve the
// order ticket's quotes. The board gets its own line in the same ledger
// instead, which is what makes the rateLimit block on its responses mean
// something.
const BoardBudgetKey = "funding-board"

// BoardService fronts the Gateway's funding board.
//
// It caches, like Service and unlike CandleService, and for the same reason
// Service does: every visitor to the board page asks the identical question,
// there are no parameters to spread the load across, and the answer is a
// document the Gateway itself only rebuilds twice a minute. Without a cache the
// page's own poll interval would set the Gateway's read rate directly.
//
// The TTL is longer than the top-of-book projection's because the two decay at
// different speeds: a quote is worthless a second later, while funding settles
// hourly and the board exists to compare venues rather than to price a fill.
type BoardService struct {
	source   BoardSource
	budget   *Coordinator
	ttl      time.Duration
	now      func() time.Time
	mu       sync.Mutex
	cached   BoardSnapshot
	loadedAt time.Time
	wait     chan struct{}
}

func NewBoardService(source BoardSource, budget *Coordinator, ttl time.Duration, now func() time.Time) (*BoardService, error) {
	if source == nil || budget == nil || ttl <= 0 || now == nil {
		return nil, fmt.Errorf("funding-board source, budget, positive ttl, and clock are required")
	}
	return &BoardService{source: source, budget: budget, ttl: ttl, now: now}, nil
}

// Get returns the board, fetching it when the cached one has aged out.
//
// The single-flight channel is the same device Service uses, kept here rather
// than shared because there is only ever one entry to guard: the board takes no
// query, so a map keyed by nothing would be a map with one key.
func (service *BoardService) Get(ctx context.Context) (BoardSnapshot, error) {
	if ctx == nil {
		return BoardSnapshot{}, fmt.Errorf("funding-board context is required")
	}
	for {
		service.mu.Lock()
		if service.wait == nil && !service.loadedAt.IsZero() && service.now().UTC().Sub(service.loadedAt) <= service.ttl {
			snapshot := service.cached
			service.mu.Unlock()
			return snapshot, nil
		}
		if service.wait != nil {
			wait := service.wait
			service.mu.Unlock()
			select {
			case <-ctx.Done():
				return BoardSnapshot{}, ctx.Err()
			case <-wait:
				continue
			}
		}
		wait := make(chan struct{})
		service.wait = wait
		service.mu.Unlock()

		err := service.budget.Acquire(ctx, BoardBudgetKey)
		var snapshot BoardSnapshot
		if err == nil {
			snapshot, err = service.source.FetchFundingBoard(ctx)
		}
		if err == nil {
			err = validateBoard(snapshot, service.now().UTC())
		}
		service.mu.Lock()
		if err == nil {
			snapshot = normalizeBoard(snapshot)
			service.cached = snapshot
			service.loadedAt = service.now().UTC()
		}
		service.wait = nil
		close(wait)
		service.mu.Unlock()
		if err != nil {
			return BoardSnapshot{}, err
		}
		return snapshot, nil
	}
}

// BudgetStatus takes no venue, unlike the other two projections' method of the
// same name, because the board has one budget rather than one per venue. A
// signature that accepted a venue would have to ignore it, and a caller reading
// that signature would reasonably expect otherwise.
func (service *BoardService) BudgetStatus() BudgetStatus {
	return service.budget.Status(BoardBudgetKey)
}

// validateBoard judges what the Gateway said against what the browser can draw.
//
// It deliberately does not re-judge the board's freshness against the TTL the
// way Service does for a quote. The Gateway has already decided, column by
// column, which venues are too old to compare and marked them unavailable, and
// that per-column verdict is strictly better information than one verdict for
// the whole table — rejecting the board because a single timestamp is a few
// seconds off two containers' clocks would hide nine healthy columns to
// complain about a tenth. A future-dated board is still refused: that is not
// staleness but a claim we have no way to reconcile.
func validateBoard(snapshot BoardSnapshot, now time.Time) error {
	if snapshot.ObservedAt.IsZero() {
		return fmt.Errorf("Gateway funding-board observation time is missing")
	}
	if snapshot.ObservedAt.UTC().After(now) {
		return fmt.Errorf("Gateway funding-board is future-dated")
	}
	if len(snapshot.Venues) == 0 || len(snapshot.Venues) > MaxBoardVenues {
		return fmt.Errorf("Gateway funding-board venue count is invalid")
	}
	seen := make(map[string]struct{}, len(snapshot.Venues))
	for _, venue := range snapshot.Venues {
		if strings.TrimSpace(venue.Venue) == "" || strings.TrimSpace(venue.Environment) == "" ||
			venue.Product != "perp" || strings.TrimSpace(venue.RouteProfileID) == "" {
			return fmt.Errorf("Gateway funding-board column identity is invalid")
		}
		if _, exists := seen[venue.RouteProfileID]; exists {
			return fmt.Errorf("Gateway funding-board repeats route profile %q", venue.RouteProfileID)
		}
		seen[venue.RouteProfileID] = struct{}{}
		if venue.Unavailable && (len(venue.Rows) != 0 || !venue.ObservedAt.IsZero()) {
			return fmt.Errorf("Gateway funding-board column %q is unavailable but carries rates", venue.RouteProfileID)
		}
		if !venue.Unavailable && venue.ObservedAt.IsZero() {
			return fmt.Errorf("Gateway funding-board column %q has rates but no observation time", venue.RouteProfileID)
		}
		if len(venue.Rows) > MaxBoardRowsEach {
			return fmt.Errorf("Gateway funding-board column %q exceeds the row bound", venue.RouteProfileID)
		}
		for _, row := range venue.Rows {
			if strings.TrimSpace(row.Symbol) == "" {
				return fmt.Errorf("Gateway funding-board row has no symbol")
			}
			if _, err := decimal.NewFromString(row.Rate); err != nil {
				return fmt.Errorf("Gateway funding-board rate for %q is invalid", row.Symbol)
			}
			if row.IntervalHours < 0 {
				return fmt.Errorf("Gateway funding-board interval for %q is invalid", row.Symbol)
			}
		}
	}
	return nil
}

// normalizeBoard puts every time on the board in UTC.
//
// The cells are compared against each other in the browser — a countdown to the
// next settlement is read beside the neighbouring venue's — so one column
// carrying a venue's local offset while the rest carry UTC would render as a
// spread in the settlement clock that does not exist.
func normalizeBoard(snapshot BoardSnapshot) BoardSnapshot {
	snapshot.ObservedAt = snapshot.ObservedAt.UTC()
	for venueIndex := range snapshot.Venues {
		venue := &snapshot.Venues[venueIndex]
		if !venue.ObservedAt.IsZero() {
			venue.ObservedAt = venue.ObservedAt.UTC()
		}
		for rowIndex := range venue.Rows {
			if !venue.Rows[rowIndex].NextFundingTime.IsZero() {
				venue.Rows[rowIndex].NextFundingTime = venue.Rows[rowIndex].NextFundingTime.UTC()
			}
		}
	}
	return snapshot
}
