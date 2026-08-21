package factoryclient

import (
	"math"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const fundingHistoryOperation = "FundingHistory.Load"

// Per-venue ceilings on a single history call.
//
// These are the venues' numbers, not ours, and they are named rather than
// inlined because they decide which loaders may issue one call and which must
// walk. At the eight-hourly settlement almost everything uses, the widest
// window the product offers is 90 rows and fits under every ceiling here — so
// the walking loaders below exist for the hourly instruments, where 30d is 720
// points and only Binance, Gate and Lighter answer that in a single call.
//
// Both walks were written after watching the venues truncate, not before. The
// direction each truncates in is the whole point: Bitget pages backwards from
// the newest settlement, so a bounded walk reaches the requested start;
// Hyperliquid answers forwards from the requested start, so an unwalked 30d
// request ends nine days short of today. A series that stops before the present
// is the one truncation a funding chart cannot absorb, since the recent end is
// what the reader came for.
const (
	binanceFundingHistoryMax = 1000
	asterFundingHistoryMax   = 1000
	okxFundingHistoryMax     = 100
	bybitFundingHistoryMax   = 200
	gateFundingHistoryMax    = 1000
	lighterFundingHistoryMax = 750
	bitgetFundingHistoryPage = 100
	// Bitget is the one venue with no time window at all, so the walk is the
	// only way to address a window. The ceiling bounds it in case a page cursor
	// stops advancing, which would otherwise spin until the context expires.
	bitgetFundingHistoryMaxPages  = 10
	hyperliquidFundingHistoryPage = 500
	// Two pages cover 30d hourly; the third and fourth are headroom for a
	// venue that settles more often than it does today.
	hyperliquidFundingHistoryMaxPages = 4
)

// fundingHistoryWindow checks the preconditions every venue shares and converts
// the window to milliseconds.
//
// The window is required rather than optional. Several venues treat a missing
// start as "since listing" and answer with a truncated tail of that, so an
// unbounded request would return a series whose extent is decided by the venue
// and differs between the two legs of the same arbitrage pair — two lines on
// one chart covering different periods, with nothing on screen to say so.
func fundingHistoryWindow(request exchange.FundingHistoryRequest) (int64, int64, error) {
	if fundingHistoryNative(request) == "" {
		return 0, 0, fundingHistoryInvalid("funding history requires a symbol")
	}
	if request.Since.IsZero() || request.Until.IsZero() {
		return 0, 0, fundingHistoryInvalid("funding history requires a bounded window")
	}
	if !request.Until.After(request.Since) {
		return 0, 0, fundingHistoryInvalid("funding history window ends before it starts")
	}
	return request.Since.UnixMilli(), request.Until.UnixMilli(), nil
}

// fundingHistoryNative is the identifier to put on the wire.
//
// The gateway resolves the canonical symbol against the board row it already
// holds, so NativeSymbol is normally set. The fallback covers the venues where
// the two spellings coincide (OKX swaps, Hyperliquid coins) and keeps a direct
// caller from having to know which those are.
func fundingHistoryNative(request exchange.FundingHistoryRequest) string {
	if request.NativeSymbol != "" {
		return request.NativeSymbol
	}
	return request.Symbol
}

// fundingHistoryLimit caps what the caller asked for at what the venue allows.
func fundingHistoryLimit(request exchange.FundingHistoryRequest, venueMax int) int {
	if request.Limit > 0 && request.Limit < venueMax {
		return request.Limit
	}
	return venueMax
}

// fundingHistorySnapshot turns a venue's raw reply into a plottable series.
//
// Four steps, and the order between the last two is the load-bearing part:
//
//  1. Sort ascending. The venues disagree on direction — Binance and Gate
//     answer oldest-first, Bybit and Bitget newest-first — so normalising here
//     rather than in each loader removes seven chances to get it wrong.
//  2. Drop repeated stamps. Two rates at one instant leave a reader no way to
//     know which was paid, and a chart no way to draw them.
//  3. Measure each interval from the gap to the point before it. No venue
//     publishes the interval alongside a historical rate, and the gap is exact
//     where a hardcoded eight hours would be wrong for everything Hyperliquid
//     and OKX settle hourly.
//  4. Clip to the window — after measuring, so that the first in-window point
//     can take its interval from a pre-window row when the venue returned one.
//     Clipping first would strip the leading point of every series of its
//     interval for no reason other than call order.
func fundingHistorySnapshot(request exchange.FundingHistoryRequest, points []exchange.FundingHistoryPoint) exchange.FundingHistorySnapshot {
	ordered := make([]exchange.FundingHistoryPoint, 0, len(points))
	for _, point := range points {
		if point.FundingTime.IsZero() {
			continue
		}
		ordered = append(ordered, point)
	}
	sortFundingHistoryPoints(ordered)

	deduped := ordered[:0]
	for _, point := range ordered {
		if last := len(deduped) - 1; last >= 0 && !point.FundingTime.After(deduped[last].FundingTime) {
			continue
		}
		deduped = append(deduped, point)
	}

	kept := make([]exchange.FundingHistoryPoint, 0, len(deduped))
	for index, point := range deduped {
		// A loader that learned the interval from the venue keeps it; none does
		// today, and the measurement is the rule rather than the fallback.
		if index > 0 && !point.IntervalHours.Valid {
			point.IntervalHours = fundingHistoryInterval(deduped[index-1].FundingTime, point.FundingTime)
		}
		if !request.Since.IsZero() && point.FundingTime.Before(request.Since) {
			continue
		}
		if !request.Until.IsZero() && point.FundingTime.After(request.Until) {
			continue
		}
		kept = append(kept, point)
	}

	return exchange.FundingHistorySnapshot{
		SchemaVersion: 1,
		Scope:         request.Scope,
		Symbol:        request.Symbol,
		ObservedAt:    time.Now().UTC(),
		Points:        kept,
	}
}

// sortFundingHistoryPoints orders by settlement time, ascending.
//
// Insertion sort rather than sort.Slice because the input is already ordered
// one way or the other on every venue: this is a single pass over a sorted
// slice and a single reversing pass over a reverse-sorted one, on inputs of a
// few hundred points.
func sortFundingHistoryPoints(points []exchange.FundingHistoryPoint) {
	for index := 1; index < len(points); index++ {
		current := points[index]
		position := index - 1
		for position >= 0 && points[position].FundingTime.After(current.FundingTime) {
			points[position+1] = points[position]
			position--
		}
		points[position+1] = current
	}
}

// fundingHistoryInterval reads the settlement interval off the gap between two
// consecutive settlements.
//
// The tolerance is about thirty-six seconds, which absorbs the jitter venues
// put on their own settlement stamps without absorbing a genuinely irregular
// gap. A venue that skipped a settlement shows up as a doubled interval and is
// reported as such rather than smoothed away, because the consumer divides
// through this field to reach a daily rate and a wrong interval there is a
// wrong number on screen rather than a missing one.
func fundingHistoryInterval(previous, current time.Time) exchange.OptionalInt {
	hours := current.Sub(previous).Hours()
	rounded := math.Round(hours)
	if rounded < 1 || math.Abs(hours-rounded) > 0.01 {
		return exchange.OptionalInt{}
	}
	return exchange.OptionalInt{Value: int64(rounded), Valid: true}
}

func fundingHistoryMillisTime(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

func fundingHistorySecondsTime(seconds int64) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func fundingHistoryInvalid(message string) error {
	return exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{
		Operation:   fundingHistoryOperation,
		SafeMessage: message,
	})
}
