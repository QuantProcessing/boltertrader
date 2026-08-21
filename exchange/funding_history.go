package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

/*
A funding history is one perpetual's settled funding rates over a window, read
credential-free.

It is to `FundingBoardSnapshot` what a chart is to a table: the board answers
"what is every instrument on this venue paying right now" in one call, and this
answers "what has this one instrument been paying" in one call per instrument.
That asymmetry belongs to the venues, not to us — none of the ten publishes a
bulk history endpoint — and it is why a history is read for a chosen pair rather
than for a whole board. A board of several hundred symbols times nine venues is
not backfillable; an arbitrage pair is two calls.

It is deliberately not `FundingRatePage`. That type is the account-bound reply to
`PerpClient.FundingRateHistory`, and it carries `MarkPrice`, `ObservedAt` and
`NextFundingTime` — none of which a settled past event has, because the venues
return a stamp and a rate and nothing else. Reusing it would mint three fields
that are structurally always empty, which is precisely the shape a consumer
cannot tell apart from "the venue declined to answer".

Not every venue can be asked. Nado publishes current funding only, so a history
loader for it does not exist rather than returning an empty series; a caller must
be able to distinguish "this venue has no history endpoint" from "this venue paid
nothing", and an empty slice says the second.
*/

// FundingHistoryPoint is one settled funding event.
//
// IntervalHours is optional for a different reason than on the board. There the
// venue either publishes the interval or does not; here no venue publishes it
// alongside a historical rate at all, so it is measured from the gap to the
// preceding settlement rather than looked up. The first point of a series has no
// preceding gap and therefore no measurable interval, and a venue that skipped a
// settlement will show the skip as a longer interval. Both cases must render as
// missing rather than as eight hours, because a cross-venue comparison
// normalises to a daily rate by dividing through this field.
type FundingHistoryPoint struct {
	FundingTime   time.Time       `json:"funding_time"`
	Rate          decimal.Decimal `json:"rate"`
	IntervalHours OptionalInt     `json:"interval_hours"`
}

// FundingHistoryRequest names one instrument by every identifier a venue might
// want, plus the window.
//
// Symbol is the canonical spelling and is carried for labelling and validation;
// every loader addresses its venue with NativeSymbol or NativeID, which the
// caller resolves from the board that already holds the mapping. A loader that
// re-derived the native spelling from the canonical one would be a second copy
// of the catalog's naming rules, free to drift from the first.
//
// Limit is a ceiling the caller accepts, not a page size to iterate: the windows
// this serves are chosen so that one call suffices on every venue. A loader that
// cannot honour the window in a single call is expected to say so, not to
// silently return a prefix.
type FundingHistoryRequest struct {
	Scope        CatalogScope `json:"scope"`
	Symbol       string       `json:"symbol"`
	NativeSymbol string       `json:"native_symbol,omitempty"`
	NativeID     string       `json:"native_id,omitempty"`
	Since        time.Time    `json:"since"`
	Until        time.Time    `json:"until"`
	Limit        int          `json:"limit,omitempty"`
}

type FundingHistorySnapshot struct {
	SchemaVersion int                   `json:"schema_version"`
	Scope         CatalogScope          `json:"scope"`
	Symbol        string                `json:"symbol"`
	ObservedAt    time.Time             `json:"observed_at"`
	Points        []FundingHistoryPoint `json:"points,omitempty"`
}

type FundingHistoryLoader interface {
	LoadFundingHistory(context.Context, FundingHistoryRequest) (FundingHistorySnapshot, error)
}

// Validate rejects a snapshot that cannot be plotted as a line.
//
// The ordering rule is the load-bearing one. The venues disagree on direction —
// Binance and Gate answer oldest-first, Bybit and Bitget newest-first — so every
// loader has to normalise, and a loader that forgot would hand the chart a
// series that folds back on itself. Requiring strictly increasing stamps catches
// both the unsorted case and the duplicate one, where two rates at the same
// instant leave the reader no way to know which was paid.
func (snapshot FundingHistorySnapshot) Validate(request FundingHistoryRequest) error {
	if snapshot.Scope != request.Scope {
		return invalidFundingHistory("funding history scope does not match requested scope")
	}
	if snapshot.Symbol != request.Symbol {
		return invalidFundingHistory("funding history symbol does not match requested symbol")
	}
	previous := time.Time{}
	for _, point := range snapshot.Points {
		if point.FundingTime.IsZero() {
			return invalidFundingHistory("funding history point is missing a settlement time")
		}
		if !previous.IsZero() && !point.FundingTime.After(previous) {
			return invalidFundingHistory("funding history points are not in ascending settlement order")
		}
		previous = point.FundingTime
	}
	return nil
}

func invalidFundingHistory(message string) error {
	return NewError(KindInvalidConfig, ErrorDetails{
		Operation:   "FundingHistory.Load",
		SafeMessage: message,
	})
}
