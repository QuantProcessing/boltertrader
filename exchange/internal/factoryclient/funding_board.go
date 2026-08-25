package factoryclient

import (
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// Shared plumbing for credential-free derivative snapshot loaders.
//
// The loaders in this family differ only in which endpoint they call and how
// that venue spells a symbol. Everything downstream of the parse — the snapshot
// envelope and the "absent means absent" rule for optional fields — is identical,
// and lives here so that nine loaders cannot drift into nine slightly different
// notions of what a missing mark price means.

const derivativeSnapshotOperation = "DerivativeSnapshot.Load"

func derivativeSnapshot(scope exchange.CatalogScope, entries []DerivativeSnapshotEntry) DerivativeSnapshot {
	return DerivativeSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		ObservedAt:    time.Now().UTC(),
		Entries:       entries,
	}
}

// fundingOptionalDecimal converts a venue's price string into a cell that knows
// whether it was answered.
//
// Unparseable and non-positive both degrade to invalid rather than to zero. A
// zero mark price is not a price, and rendering one as "0.00" in a product an
// operator sizes a trade against is worse than rendering a dash.
func fundingOptionalDecimal(value string) exchange.OptionalDecimal {
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: parsed, Valid: true}
}

// fundingOptionalNonNegative is the same, for quantities where zero is a real
// answer — an instrument genuinely can have no open interest.
func fundingOptionalNonNegative(value string) exchange.OptionalDecimal {
	parsed, err := decimal.NewFromString(value)
	if err != nil || parsed.IsNegative() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: parsed, Valid: true}
}

// fundingOpenInterestNotional converts venue-native open interest into quote
// notional. Most linear venues report base units and use a multiplier of one;
// contract-count venues such as Gate pass their catalog multiplier instead.
// Missing or malformed conversion inputs stay absent rather than putting a
// quantity with the wrong unit into a cross-venue ranking.
func fundingOpenInterestNotional(openInterest, markPrice string, multiplier decimal.Decimal) exchange.OptionalDecimal {
	quantity, err := decimal.NewFromString(openInterest)
	if err != nil || quantity.IsNegative() || !multiplier.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	price, err := decimal.NewFromString(markPrice)
	if err != nil || !price.IsPositive() {
		return exchange.OptionalDecimal{}
	}
	return exchange.OptionalDecimal{Value: quantity.Mul(multiplier).Mul(price), Valid: true}
}

// baseFromCanonical recovers the base asset from an already-canonical symbol.
//
// The bulk ticker endpoints send no base/quote metadata — only the native
// symbol — so the base is whatever survives canonicalisation ahead of the
// separator. A symbol that never got one is returned whole rather than blanked,
// because a derivative row labelled with the venue's own spelling is still usable.
func baseFromCanonical(canonical string) string {
	if index := strings.Index(canonical, "-"); index > 0 {
		return canonical[:index]
	}
	return canonical
}

// decimalOrSkip parses a signed rate, and is the caller's cue to drop the row.
//
// Funding rates are legitimately negative, so the sign carries meaning and the
// positive-only parsers cannot be reused. An unparseable rate is the one field
// a derivative row cannot degrade around — there is nothing left to render — so the
// loaders skip such a row rather than failing the whole venue.
func decimalOrSkip(value string) (decimal.Decimal, error) {
	return positiveOrNegativeDecimal(strings.TrimSpace(value))
}

// fundingIntervalHours reads an interval a venue publishes as a bare number of
// hours, rejecting the zero that several of them use to mean "unset".
func fundingIntervalHours(value string) exchange.OptionalInt {
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return exchange.OptionalInt{}
	}
	return exchange.OptionalInt{Value: parsed.IntPart(), Valid: true}
}

// fundingUnixMillis reads a settlement stamp, degrading a blank or non-positive
// value to the zero time.
//
// Several venues send "" or "0" for instruments between settlement windows.
// The strict parsers used on the account-bound path reject those outright,
// which is right for an order and wrong for a board: one instrument without a
// clock must not cost the operator the other four hundred rows.
func fundingUnixMillis(value string) time.Time {
	parsed, err := decimal.NewFromString(value)
	if err != nil || !parsed.IsPositive() {
		return time.Time{}
	}
	return time.UnixMilli(parsed.IntPart()).UTC()
}
