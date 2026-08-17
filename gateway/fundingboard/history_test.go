package fundingboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

// TestFundingHistoryResolvesNativeIdentifierFromBoard pins the reason this read
// lives on the board source at all.
//
// Every venue addresses its own history by its own identifier — BTCUSDT, an
// integer market id, a bare coin — and a caller comparing two legs of a spread
// knows only BTC-USDT. The board already refreshed a row carrying both
// spellings, so the translation borrows that row. A second resolver would be a
// second chance to disagree with the table the reader is looking at, and the
// failure would be silent: a chart for the wrong instrument still draws.
//
// The window is asserted alongside it because it is the other half of what the
// caller cannot supply. Passing it through unchanged is what lets the two legs
// of one pair cover the same period.
func TestFundingHistoryResolvesNativeIdentifierFromBoard(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	histories := &fakeHistoryFactory{}
	source, err := newSource(histories, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err := source.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	since := time.Unix(1_719_964_800, 0).UTC()
	until := time.Unix(1_720_051_200, 0).UTC()
	snapshot, err := source.History(context.Background(), HistoryQuery{
		Venue: "binance", Environment: "live", Symbol: "BTC-USDT", Since: since, Until: until,
	})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(snapshot.Points) != 1 {
		t.Fatalf("got %d points, want 1: %+v", len(snapshot.Points), snapshot.Points)
	}

	request := histories.lastRequest()
	if request.Symbol != "BTC-USDT" {
		t.Errorf("symbol = %q, want the canonical BTC-USDT", request.Symbol)
	}
	// The two native spellings are what the venue's endpoint actually accepts,
	// and neither is derivable from the canonical symbol.
	if request.NativeSymbol != "BTCUSDT-native" {
		t.Errorf("native symbol = %q, want the board row's", request.NativeSymbol)
	}
	if request.NativeID != "binance-1" {
		t.Errorf("native id = %q, want the board row's", request.NativeID)
	}
	if !request.Since.Equal(since) || !request.Until.Equal(until) {
		t.Errorf("window = %s..%s, want %s..%s", request.Since, request.Until, since, until)
	}
}

// TestFundingHistorySearchesEveryColumnOfAVenue pins the fact that a venue does
// not name a column.
//
// Bybit and bitget each stand two, one per settlement asset, and the registry
// orders USDC ahead of USDT. A resolver that stopped at the first column
// matching the venue answered "this instrument is not listed" for every USDT
// pair on both venues while the board was carrying it one column over — and the
// arbitrage page turned that into a leg that would not draw for half the pairs
// an operator could open.
//
// Both settlements are asserted, and the native spelling with them: resolving to
// *a* column is not the same as resolving to the right one, and a chart drawn
// from the wrong column's row is a chart of a different instrument.
func TestFundingHistorySearchesEveryColumnOfAVenue(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	histories := &fakeHistoryFactory{}
	source, err := newSource(histories, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	if _, err := source.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	since := time.Unix(1_719_964_800, 0).UTC()
	until := time.Unix(1_720_051_200, 0).UTC()
	for _, settlement := range []string{"USDC", "USDT"} {
		t.Run(settlement, func(t *testing.T) {
			symbol := "BTC-" + settlement
			// The board is warm, so a column that has to be refreshed to answer
			// is a column that was not searched.
			before := histories.refreshes()
			if _, err := source.History(context.Background(), HistoryQuery{
				Venue: "bybit", Environment: "live", Symbol: symbol, Since: since, Until: until,
			}); err != nil {
				t.Fatalf("History for %s: %v", symbol, err)
			}
			if after := histories.refreshes(); after != before {
				t.Errorf("%s cost %d venue refreshes against a warm board", symbol, after-before)
			}
			if native := histories.lastRequest().NativeSymbol; native != "BTC"+settlement+"-native" {
				t.Errorf("native symbol = %q, want the %s column's row", native, settlement)
			}
		})
	}

	// A venue whose every column was searched and came up empty still owes the
	// narrower answer: the venue charts, this symbol is not on it.
	if _, err := source.History(context.Background(), HistoryQuery{
		Venue: "bybit", Environment: "live", Symbol: "NOTLISTED-USDT", Since: since, Until: until,
	}); !errors.Is(err, ErrHistoryUnknownInstrument) {
		t.Fatalf("History for an unlisted symbol = %v, want ErrHistoryUnknownInstrument", err)
	}
}

// TestFundingHistoryWarmsColdColumnOnce covers the chart opened before the
// board has ever been read.
//
// The mapping only exists once a column has refreshed, so a cold column and an
// unlisted instrument arrive at the same place with the same evidence. Refusing
// on the cold one would make the first chart after a restart fail for a reason
// that stops being true a second later. Refreshing on the warm one would let a
// mistyped symbol fan out to ten venues, so the retry is spent exactly once and
// only where it can change the answer.
func TestFundingHistoryWarmsColdColumnOnce(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	histories := &fakeHistoryFactory{}
	source, err := newSource(histories, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	// No Snapshot first: every column is cold.
	if _, err := source.History(context.Background(), HistoryQuery{
		Venue: "binance", Environment: "live", Symbol: "BTC-USDT",
		Since: time.Unix(1_719_964_800, 0).UTC(), Until: time.Unix(1_720_051_200, 0).UTC(),
	}); err != nil {
		t.Fatalf("History against a cold board: %v", err)
	}
	if refreshes := histories.refreshes(); refreshes == 0 {
		t.Fatal("a cold column was never refreshed")
	}

	// The board is warm now, so an instrument it does not carry is answered
	// from cache rather than by refreshing ten venues again.
	before := histories.refreshes()
	_, err = source.History(context.Background(), HistoryQuery{
		Venue: "binance", Environment: "live", Symbol: "NOTLISTED-USDT",
		Since: time.Unix(1_719_964_800, 0).UTC(), Until: time.Unix(1_720_051_200, 0).UTC(),
	})
	if !errors.Is(err, ErrHistoryUnknownInstrument) {
		t.Fatalf("unlisted instrument: %v, want ErrHistoryUnknownInstrument", err)
	}
	if after := histories.refreshes(); after != before {
		t.Errorf("an unlisted symbol triggered %d venue refreshes", after-before)
	}
}

// TestFundingHistoryRefusesUnchartableRead pins the four refusals apart.
//
// They are separate sentinels because each becomes a different sentence beside
// a leg that did not draw, and the arbitrage chart puts two legs side by side.
// Collapsing them would leave the reader with a blank line where "this venue
// keeps no history", "this venue does not list this pair" and "the board is
// down" all look identical — and identical to a genuine stretch of zero
// funding, which is the one reading that would cost money.
func TestFundingHistoryRefusesUnchartableRead(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	histories := &fakeHistoryFactory{}
	source, err := newSource(histories, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	if _, err := source.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	since := time.Unix(1_719_964_800, 0).UTC()
	until := time.Unix(1_720_051_200, 0).UTC()
	cases := []struct {
		name  string
		query HistoryQuery
		want  error
	}{
		{
			// Nado is a real board column with real rates and no series behind
			// them. It has to say so rather than draw an empty chart.
			name:  "charted venue publishes no history",
			query: HistoryQuery{Venue: "nado", Environment: "live", Symbol: "BTC-USDT0", Since: since, Until: until},
			want:  ErrHistoryUnsupported,
		},
		{
			name:  "venue is not on the board",
			query: HistoryQuery{Venue: "aster", Environment: "live", Symbol: "BTC-USDT", Since: since, Until: until},
			want:  ErrHistoryUnsupported,
		},
		{
			// Only live endpoints exist now, but the query still carries an
			// environment because it arrives from the browser. Anything else
			// names endpoints this board cannot reach and must be refused
			// rather than quietly answered from the live column.
			name:  "environment is not the one the board tracks",
			query: HistoryQuery{Venue: "binance", Environment: "demo", Symbol: "BTC-USDT", Since: since, Until: until},
			want:  ErrHistoryUnsupported,
		},
		{
			name:  "no symbol",
			query: HistoryQuery{Venue: "binance", Environment: "live", Since: since, Until: until},
			want:  ErrHistoryUnsupported,
		},
		{
			name:  "unbounded window",
			query: HistoryQuery{Venue: "binance", Environment: "live", Symbol: "BTC-USDT", Until: until},
			want:  ErrHistoryWindow,
		},
		{
			name:  "window ends before it starts",
			query: HistoryQuery{Venue: "binance", Environment: "live", Symbol: "BTC-USDT", Since: until, Until: since},
			want:  ErrHistoryWindow,
		},
		{
			// Wider than the loaders were sized for. Serving it would mean a
			// series that stops early on the venues with row caps, which reads
			// as funding that was never paid.
			name:  "window wider than one call",
			query: HistoryQuery{Venue: "binance", Environment: "live", Symbol: "BTC-USDT", Since: until.Add(-90 * 24 * time.Hour), Until: until},
			want:  ErrHistoryWindow,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := source.History(context.Background(), testCase.query); !errors.Is(err, testCase.want) {
				t.Fatalf("History = %v, want %v", err, testCase.want)
			}
		})
	}

	valid := HistoryQuery{Venue: "binance", Environment: "live", Symbol: "BTC-USDT", Since: since, Until: until}
	if err := source.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := source.History(context.Background(), valid); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("History after Close: %v, want ErrBoardUnavailable", err)
	}

	// A source reachable through the same field a built one lives in answers
	// rather than panics.
	var unbuilt *Source
	if _, err := unbuilt.History(context.Background(), valid); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("History on an unbuilt source: %v, want ErrBoardUnavailable", err)
	}

	// A factory with no history capability at all is still a Factory: every
	// column stays on the board and every chart says so.
	plain, err := newSource(&fakeFactory{}, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource without a history factory: %v", err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if _, err := plain.History(context.Background(), valid); !errors.Is(err, ErrHistoryUnsupported) {
		t.Fatalf("History without a history factory: %v, want ErrHistoryUnsupported", err)
	}
}

// fakeHistoryFactory is a fakeFactory that can also chart.
//
// It builds a history connector for every column the real one can, and none for
// Nado, so the split between "on the board" and "chartable" under test is the
// production split.
type fakeHistoryFactory struct {
	fakeFactory

	mu      sync.Mutex
	request exchange.FundingHistoryRequest
}

func (factory *fakeHistoryFactory) BuildHistory(profile routeprofile.Profile) (HistoryConnector, error) {
	if profile.Venue == "nado" {
		return nil, ErrHistoryUnsupported
	}
	return &fakeHistoryConnector{factory: factory, profile: profile}, nil
}

func (factory *fakeHistoryFactory) lastRequest() exchange.FundingHistoryRequest {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.request
}

type fakeHistoryConnector struct {
	factory *fakeHistoryFactory
	profile routeprofile.Profile
}

func (connector *fakeHistoryConnector) Load(_ context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	connector.factory.mu.Lock()
	connector.factory.request = request
	connector.factory.mu.Unlock()
	return exchange.FundingHistorySnapshot{
		SchemaVersion: 1,
		Symbol:        request.Symbol,
		ObservedAt:    time.Unix(1720000000, 0).UTC(),
		Points: []exchange.FundingHistoryPoint{{
			FundingTime: request.Since,
			Rate:        decimal.RequireFromString("0.0001"),
		}},
	}, nil
}

func (connector *fakeHistoryConnector) Close() error { return nil }
