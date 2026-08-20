package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

/*
A funding board is one venue's whole perpetual funding surface read in a single
credential-free call.

This is deliberately not `FundingRate`: that type answers "what is this one
instrument paying, for this account's route", and every venue serves it from a
per-symbol endpoint behind an authenticated client. The board answers "what is
every instrument on this venue paying right now", which every venue also
publishes, but from a different endpoint that needs no key at all. Modelling it
as a loader — the same shape as `InstrumentLoader` — keeps it out of the
account-bound client surface entirely, so a board can be read before a single
account exists.

The optional fields are optional because the venues disagree, not because we
are being permissive: only three venues return open interest in their bulk
call, and some venues publish no settlement clock. A consumer must render a
missing cell as missing rather than as zero.
*/

type FundingBoardEntry struct {
	Symbol          string          `json:"symbol"`
	NativeSymbol    string          `json:"native_symbol,omitempty"`
	NativeID        string          `json:"native_id,omitempty"`
	BaseAsset       string          `json:"base_asset,omitempty"`
	QuoteAsset      string          `json:"quote_asset,omitempty"`
	Rate            decimal.Decimal `json:"rate"`
	IntervalHours   OptionalInt     `json:"interval_hours"`
	NextFundingTime time.Time       `json:"next_funding_time,omitempty"`
	MarkPrice       OptionalDecimal `json:"mark_price"`
	IndexPrice      OptionalDecimal `json:"index_price"`
	// OpenInterest is the venue-native quantity. Depending on the venue this is
	// base units or contracts and therefore must not be compared across venues.
	OpenInterest OptionalDecimal `json:"open_interest"`
	// OpenInterestNotional is OpenInterest converted at the row's mark price
	// into the quote/settlement stablecoin (USDT or USDC). Cross-venue products
	// use this field for display, filtering and sorting.
	OpenInterestNotional OptionalDecimal `json:"open_interest_notional"`
}

type FundingBoardSnapshot struct {
	SchemaVersion int                 `json:"schema_version"`
	Scope         CatalogScope        `json:"scope"`
	ObservedAt    time.Time           `json:"observed_at"`
	Entries       []FundingBoardEntry `json:"entries,omitempty"`
}

type FundingBoardLoader interface {
	LoadFundingBoard(context.Context, CatalogScope) (FundingBoardSnapshot, error)
}

// Validate rejects a snapshot that cannot be rendered as a row on a board.
//
// The scope check matters more than it looks: a board aggregates several
// venues into one table, and a loader that answered for the wrong scope would
// silently file its rates under another venue's column.
func (snapshot FundingBoardSnapshot) Validate(scope CatalogScope) error {
	if snapshot.Scope != scope {
		return invalidFundingBoard("funding board scope does not match requested scope")
	}
	seen := make(map[string]struct{}, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Symbol == "" {
			return invalidFundingBoard("funding board entry is missing a canonical symbol")
		}
		if _, duplicate := seen[entry.Symbol]; duplicate {
			return invalidFundingBoard("funding board contains a duplicate canonical symbol")
		}
		seen[entry.Symbol] = struct{}{}
	}
	return nil
}

func invalidFundingBoard(message string) error {
	return NewError(KindInvalidConfig, ErrorDetails{
		Operation:   "FundingBoard.Load",
		SafeMessage: message,
	})
}
