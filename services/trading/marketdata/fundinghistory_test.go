package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"
)

type historySourceFunc func(context.Context, FundingHistoryRead) (FundingHistorySeries, error)

func (fn historySourceFunc) FetchFundingHistory(ctx context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
	return fn(ctx, read)
}

func newHistoryService(t *testing.T, now time.Time, source FundingHistorySource) *FundingHistoryService {
	t.Helper()
	budget, err := NewCoordinator(10, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFundingHistoryService(source, budget, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// The window is the whole premise of this read: every venue's history endpoint
// is paginated, and these two spans are the two that fit in one page everywhere
// on the board. A caller who could name any span could name one that silently
// arrives truncated, and a funding curve missing its middle looks exactly like
// a complete one.
func TestFundingHistoryWindowIsBounded(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	var seen FundingHistoryRead
	service := newHistoryService(t, now, historySourceFunc(func(_ context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
		seen = read
		return FundingHistorySeries{
			Venue: read.Venue, Environment: read.Environment, Symbol: read.Symbol,
			Since: read.Since, Until: read.Until, ObservedAt: now,
		}, nil
	}))

	for _, test := range []struct {
		window FundingWindow
		span   time.Duration
	}{{FundingWindow7d, 7 * 24 * time.Hour}, {FundingWindow30d, 30 * 24 * time.Hour}} {
		if _, err := service.Get(context.Background(), FundingHistoryQuery{
			Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Window: test.window,
		}); err != nil {
			t.Fatalf("%s: %v", test.window, err)
		}
		if !seen.Until.Equal(now) || seen.Until.Sub(seen.Since) != test.span {
			t.Errorf("%s resolved to [%s, %s], want a %s span ending at %s", test.window, seen.Since, seen.Until, test.span, now)
		}
	}

	for _, raw := range []string{"", "1d", "90d", "7D", "7 d", "168h"} {
		if _, err := ParseFundingWindow(raw); err == nil {
			t.Errorf("ParseFundingWindow(%q) was accepted", raw)
		}
	}
}

// The bounds round-trip through RFC 3339 and come back echoed, so a clock that
// kept nanoseconds would fail its own equality check against the value that
// produced it — at every hour of the day except the one a test happens to pin.
func TestFundingHistoryResolvesWindowOnASecondBoundary(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 123456789, time.UTC)
	var seen FundingHistoryRead
	service := newHistoryService(t, now, historySourceFunc(func(_ context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
		seen = read
		// Echoing the bounds through the wire format is what the Gateway does.
		since, _ := time.Parse(time.RFC3339, read.Since.Format(time.RFC3339))
		until, _ := time.Parse(time.RFC3339, read.Until.Format(time.RFC3339))
		return FundingHistorySeries{
			Venue: read.Venue, Environment: read.Environment, Symbol: read.Symbol,
			Since: since, Until: until, ObservedAt: now,
		}, nil
	}))
	if _, err := service.Get(context.Background(), FundingHistoryQuery{
		Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Window: FundingWindow7d,
	}); err != nil {
		t.Fatal(err)
	}
	if seen.Until.Nanosecond() != 0 || seen.Since.Nanosecond() != 0 {
		t.Fatalf("window [%s, %s] carries sub-second precision the wire cannot", seen.Since, seen.Until)
	}
}

// Two of these series are drawn together as the legs of one spread, so a
// response that arrived under the wrong identity or over the wrong span renders
// as a plausible curve rather than as a visible gap.
func TestFundingHistoryRefusesAMisansweredSeries(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	good := func(read FundingHistoryRead) FundingHistorySeries {
		return FundingHistorySeries{
			Venue: read.Venue, Environment: read.Environment, Symbol: read.Symbol,
			Since: read.Since, Until: read.Until, ObservedAt: now,
			Points: []FundingHistoryPoint{
				{FundingTime: now.Add(-16 * time.Hour), Rate: "0.0001", IntervalHours: 8},
				{FundingTime: now.Add(-8 * time.Hour), Rate: "-0.0002", IntervalHours: 8},
			},
		}
	}
	for name, damage := range map[string]func(FundingHistorySeries) FundingHistorySeries{
		"wrong venue":       func(s FundingHistorySeries) FundingHistorySeries { s.Venue = "okx"; return s },
		"wrong environment": func(s FundingHistorySeries) FundingHistorySeries { s.Environment = "live"; return s },
		"wrong symbol":      func(s FundingHistorySeries) FundingHistorySeries { s.Symbol = "ETH-USDT"; return s },
		"narrowed window": func(s FundingHistorySeries) FundingHistorySeries {
			s.Since = s.Since.Add(time.Hour)
			return s
		},
		"no observation time": func(s FundingHistorySeries) FundingHistorySeries {
			s.ObservedAt = time.Time{}
			return s
		},
		"undated settlement": func(s FundingHistorySeries) FundingHistorySeries {
			s.Points[0].FundingTime = time.Time{}
			return s
		},
		"missing rate": func(s FundingHistorySeries) FundingHistorySeries {
			s.Points[1].Rate = "  "
			return s
		},
		"descending settlements": func(s FundingHistorySeries) FundingHistorySeries {
			s.Points[0], s.Points[1] = s.Points[1], s.Points[0]
			return s
		},
	} {
		service := newHistoryService(t, now, historySourceFunc(func(_ context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
			return damage(good(read)), nil
		}))
		if _, err := service.Get(context.Background(), FundingHistoryQuery{
			Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Window: FundingWindow7d,
		}); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// Unsupported has to survive the trip out unwrapped: the HTTP surface turns it
// into a permanent 404 so the browser stops asking, and every other failure
// into a retryable 503. Flattening the two would have a pair page spin forever
// on the one board column that publishes no history.
func TestFundingHistoryPropagatesUnsupportedVenue(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	service := newHistoryService(t, now, historySourceFunc(func(context.Context, FundingHistoryRead) (FundingHistorySeries, error) {
		return FundingHistorySeries{}, ErrFundingHistoryUnsupported
	}))
	_, err := service.Get(context.Background(), FundingHistoryQuery{
		Venue: "nado", Environment: "testnet", Symbol: "BTC-USDT", Window: FundingWindow7d,
	})
	if !errors.Is(err, ErrFundingHistoryUnsupported) {
		t.Fatalf("err = %v, want ErrFundingHistoryUnsupported", err)
	}
}

// Two legs of one spread are quoted by venues on different clocks and drawn on
// one axis, so the service — not the chart — is where they are put on UTC.
func TestFundingHistoryNormalisesEveryTimeToUTC(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	zone := time.FixedZone("UTC+8", 8*60*60)
	service := newHistoryService(t, now, historySourceFunc(func(_ context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
		return FundingHistorySeries{
			Venue: read.Venue, Environment: read.Environment, Symbol: read.Symbol,
			Since: read.Since.In(zone), Until: read.Until.In(zone), ObservedAt: now.In(zone),
			Points: []FundingHistoryPoint{{FundingTime: now.Add(-8 * time.Hour).In(zone), Rate: "0.0001"}},
		}, nil
	}))
	series, err := service.Get(context.Background(), FundingHistoryQuery{
		Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Window: FundingWindow7d,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, moment := range map[string]time.Time{
		"since": series.Since, "until": series.Until,
		"observedAt": series.ObservedAt, "fundingTime": series.Points[0].FundingTime,
	} {
		if moment.Location() != time.UTC {
			t.Errorf("%s is in %s, want UTC", name, moment.Location())
		}
	}
}

// History spends the same per-venue allowance the quote projection does. It has
// to: a reload storm on a pair page and the matrix poller reach the same venue,
// and a budget that only one of them respected would not be a budget.
func TestFundingHistorySpendsTheSharedVenueBudget(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	budget, err := NewCoordinator(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewFundingHistoryService(historySourceFunc(func(_ context.Context, read FundingHistoryRead) (FundingHistorySeries, error) {
		return FundingHistorySeries{
			Venue: read.Venue, Environment: read.Environment, Symbol: read.Symbol,
			Since: read.Since, Until: read.Until, ObservedAt: now,
		}, nil
	}), budget, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	query := FundingHistoryQuery{Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Window: FundingWindow7d}
	if _, err := service.Get(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(context.Background(), query); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("second read err = %v, want ErrRateLimited", err)
	}
	// A different venue draws on its own allowance, so one busy pair cannot
	// starve the other leg of the very spread it belongs to.
	if _, err := service.Get(context.Background(), FundingHistoryQuery{
		Venue: "bybit", Environment: "demo", Symbol: "BTC-USDT", Window: FundingWindow7d,
	}); err != nil {
		t.Fatalf("second venue err = %v, want the read to succeed", err)
	}
	if status := service.BudgetStatus("binance"); status.Remaining != 0 {
		t.Errorf("binance budget remaining = %d, want 0", status.Remaining)
	}
}
