package marketdata

import (
	"context"
	"errors"
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
	Status         string     `json:"status"`
	LastSuccessAt  time.Time  `json:"lastSuccessAt,omitzero"`
	Reconnects     uint64     `json:"reconnects,omitempty"`
	Rows           []BoardRow `json:"rows"`
}

// BoardRow is one instrument's rate on one venue.
//
// Every optional cell carries omitempty, and the two times carry omitzero,
// because an absent cell has to reach the browser absent rather than as a zero
// the venue never quoted: several of the nine funding venues leave open interest
// out of their bulk call, and hyperliquid and lighter publish a bare rate with
// no settlement clock. omitempty alone would not do it for the times —
// it does not consult time.Time.IsZero, so a zero would ship as
// "0001-01-01T00:00:00Z" and render as a countdown that expired two thousand
// years ago.
type BoardRow struct {
	InstrumentID         string    `json:"instrumentId,omitempty"`
	Symbol               string    `json:"symbol"`
	BaseAsset            string    `json:"baseAsset,omitempty"`
	QuoteAsset           string    `json:"quoteAsset,omitempty"`
	MarketFamily         string    `json:"marketFamily"`
	AssetClass           string    `json:"assetClass"`
	ClassificationSource string    `json:"classificationSource,omitempty"`
	RawAssetType         string    `json:"rawAssetType,omitempty"`
	RawCategories        []string  `json:"rawCategories,omitempty"`
	Rate                 string    `json:"rate,omitempty"`
	IntervalHours        int64     `json:"intervalHours,omitempty"`
	NextFundingTime      time.Time `json:"nextFundingTime,omitzero"`
	MarkPrice            string    `json:"markPrice,omitempty"`
	IndexPrice           string    `json:"indexPrice,omitempty"`
	OpenInterest         string    `json:"openInterest,omitempty"`
	// OpenInterestNotional is quote/USD notional. The actual denomination is
	// explicit in OpenInterestNotionalAsset and is the comparable OI surface.
	OpenInterestNotional      string    `json:"openInterestNotional,omitempty"`
	OpenInterestNotionalAsset string    `json:"openInterestNotionalAsset,omitempty"`
	FundingObservedAt         time.Time `json:"fundingObservedAt,omitzero"`
	PriceObservedAt           time.Time `json:"priceObservedAt,omitzero"`
	OpenInterestObservedAt    time.Time `json:"openInterestObservedAt,omitzero"`
}

// BoardStream is the Gateway's resident funding projection. Run receives a
// complete first snapshot and complete replacement snapshots after each
// upsert/resync; it never performs a request on behalf of Get.
type BoardStream interface {
	RunFundingBoard(context.Context, func(BoardSnapshot) error) error
}

var ErrFundingBoardNotReady = errors.New("funding-board projection not ready")

// BoardService is Trading's local projection. Get is a pure memory read.
type BoardService struct {
	now    func() time.Time
	mu     sync.RWMutex
	cached BoardSnapshot
	ready  bool
}

func NewBoardService(now func() time.Time) (*BoardService, error) {
	if now == nil {
		return nil, fmt.Errorf("funding-board clock is required")
	}
	return &BoardService{now: now}, nil
}

func (service *BoardService) Get(ctx context.Context) (BoardSnapshot, error) {
	if service == nil || ctx == nil || ctx.Err() != nil {
		return BoardSnapshot{}, fmt.Errorf("funding-board context is required")
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if !service.ready {
		return BoardSnapshot{}, ErrFundingBoardNotReady
	}
	return cloneBoard(service.cached), nil
}

func (service *BoardService) Replace(snapshot BoardSnapshot) error {
	if service == nil {
		return ErrFundingBoardNotReady
	}
	if err := validateBoard(snapshot, service.now().UTC()); err != nil {
		return err
	}
	snapshot = normalizeBoard(snapshot)
	service.mu.Lock()
	service.cached = cloneBoard(snapshot)
	service.ready = hasFreshBoardVenue(snapshot)
	service.mu.Unlock()
	if !hasFreshBoardVenue(snapshot) {
		return ErrFundingBoardNotReady
	}
	return nil
}

func hasFreshBoardVenue(snapshot BoardSnapshot) bool {
	for _, venue := range snapshot.Venues {
		if !venue.Unavailable && len(venue.Rows) > 0 {
			return true
		}
	}
	return false
}

func cloneBoard(snapshot BoardSnapshot) BoardSnapshot {
	result := snapshot
	result.Venues = append([]BoardVenue(nil), snapshot.Venues...)
	for index := range result.Venues {
		// Keep an empty collection non-nil. An unavailable venue legitimately has
		// no rows, but the JSON contract is an array and browsers iterate it.
		result.Venues[index].Rows = append([]BoardRow{}, snapshot.Venues[index].Rows...)
		for row := range result.Venues[index].Rows {
			result.Venues[index].Rows[row].RawCategories = append([]string(nil), snapshot.Venues[index].Rows[row].RawCategories...)
		}
	}
	return result
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
			if strings.TrimSpace(row.Rate) != "" {
				if _, err := decimal.NewFromString(row.Rate); err != nil {
					return fmt.Errorf("Gateway funding-board rate for %q is invalid", row.Symbol)
				}
			}
			if row.IntervalHours < 0 {
				return fmt.Errorf("Gateway funding-board interval for %q is invalid", row.Symbol)
			}
			if row.MarketFamily != "" && !knownMarketFamily(row.MarketFamily) {
				return fmt.Errorf("Gateway funding-board market family for %q is invalid", row.Symbol)
			}
			if row.AssetClass != "" && !knownAssetClass(row.AssetClass) {
				return fmt.Errorf("Gateway funding-board asset class for %q is invalid", row.Symbol)
			}
			for label, value := range map[string]string{
				"open interest":          row.OpenInterest,
				"open-interest notional": row.OpenInterestNotional,
			} {
				if value == "" {
					continue
				}
				parsed, err := decimal.NewFromString(value)
				if err != nil || parsed.IsNegative() {
					return fmt.Errorf("Gateway funding-board %s for %q is invalid", label, row.Symbol)
				}
			}
		}
	}
	return nil
}

func knownMarketFamily(value string) bool {
	switch value {
	case "spot", "venue_perpetual", "builder_perpetual":
		return true
	default:
		return false
	}
}

func knownAssetClass(value string) bool {
	switch value {
	case "unknown", "crypto", "equity", "commodity", "index", "fx", "rates", "pre_ipo", "rwa_other":
		return true
	default:
		return false
	}
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
