package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// A fixed clock, so every fixture stamp is a literal and every expectation is
// computed. The two derivations are deliberately independent: a fixture row
// says 1719993600000 and the matching expectation says "eight hours after the
// epoch", so a helper that drifted would have to drift in both directions at
// once to stay green.
const (
	fundingHistoryHourMillis = int64(3_600_000)
	fundingHistoryEpoch      = int64(1_719_964_800_000) // 2024-07-03T00:00:00Z
)

func fundingHistoryStamp(hours int64) int64 {
	return fundingHistoryEpoch + hours*fundingHistoryHourMillis
}

func fundingHistoryTime(hours int64) time.Time {
	return time.UnixMilli(fundingHistoryStamp(hours)).UTC()
}

// fundingHistoryRate gives each generated row a distinct rate, so a walk that
// dropped or repeated a page shows up as a wrong value rather than as a right
// value in the wrong place.
func fundingHistoryRate(index int64) string { return fmt.Sprintf("0.%06d", index+1) }

type wantHistoryPoint struct {
	millis int64
	rate   string
	// interval is the settlement gap in hours, and zero means the point is
	// expected to carry none — which is the honest answer for the first point of
	// a series that has nothing before it to measure against.
	interval int64
}

type fundingHistoryCall struct {
	path  string
	query url.Values
	body  string
}

// fundingHistoryFixture answers a scripted sequence of bodies per path and
// records every request.
//
// The sequence is what the board fixture does not need: two of these seven
// loaders page, and a page walk can only be checked by giving the second call a
// different answer from the first and then reading back the cursor it sent. The
// last body repeats, so a single-call venue is scripted with a one-element list.
type fundingHistoryFixture struct {
	server *httptest.Server
	mutex  sync.Mutex
	pages  map[string][]string
	calls  []fundingHistoryCall
}

func newFundingHistoryFixture(t *testing.T, pages map[string][]string) *fundingHistoryFixture {
	t.Helper()
	fixture := &fundingHistoryFixture{pages: pages}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)

		fixture.mutex.Lock()
		fixture.calls = append(fixture.calls, fundingHistoryCall{
			path:  request.URL.Path,
			query: request.URL.Query(),
			body:  string(body),
		})
		seen := 0
		for _, call := range fixture.calls {
			if call.path == request.URL.Path {
				seen++
			}
		}
		bodies, known := fixture.pages[request.URL.Path]
		fixture.mutex.Unlock()

		if !known || len(bodies) == 0 {
			t.Errorf("unexpected funding history request: %s", request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		index := seen - 1
		if index >= len(bodies) {
			index = len(bodies) - 1
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, bodies[index])
	}))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (fixture *fundingHistoryFixture) settings(environment string) Settings {
	return Settings{Endpoint: fixture.server.URL, Environment: environment, HTTPClient: fixture.server.Client()}
}

func (fixture *fundingHistoryFixture) callsFor(path string) []fundingHistoryCall {
	fixture.mutex.Lock()
	defer fixture.mutex.Unlock()
	matched := make([]fundingHistoryCall, 0, len(fixture.calls))
	for _, call := range fixture.calls {
		if call.path == path {
			matched = append(matched, call)
		}
	}
	return matched
}

func assertFundingHistoryPoints(t *testing.T, got []exchange.FundingHistoryPoint, want []wantHistoryPoint) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("point count = %d, want %d", len(got), len(want))
	}
	for index, expected := range want {
		point := got[index]
		if stamp := point.FundingTime.UnixMilli(); stamp != expected.millis {
			t.Errorf("point %d settled at %s, want %s",
				index,
				point.FundingTime.UTC().Format(time.RFC3339),
				time.UnixMilli(expected.millis).UTC().Format(time.RFC3339))
		}
		wantRate := decimal.RequireFromString(expected.rate)
		if !point.Rate.Equal(wantRate) {
			t.Errorf("point %d rate = %s, want %s", index, point.Rate, wantRate)
		}
		switch {
		case expected.interval == 0 && point.IntervalHours.Valid:
			t.Errorf("point %d reported a %dh interval, want none", index, point.IntervalHours.Value)
		case expected.interval != 0 && !point.IntervalHours.Valid:
			t.Errorf("point %d reported no interval, want %dh", index, expected.interval)
		case expected.interval != 0 && point.IntervalHours.Value != expected.interval:
			t.Errorf("point %d interval = %dh, want %dh", index, point.IntervalHours.Value, expected.interval)
		}
	}
}

func assertFundingHistoryQuery(t *testing.T, got url.Values, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if actual := got.Get(key); actual != value {
			t.Errorf("query %s = %q, want %q", key, actual, value)
		}
	}
}

// hyperliquidFundingHistoryPageBody renders one page of an hourly series.
func hyperliquidFundingHistoryPageBody(firstHour, lastHour int64) string {
	var builder strings.Builder
	builder.WriteString("[")
	for hour := firstHour; hour <= lastHour; hour++ {
		if hour > firstHour {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `{"coin":"BTC","fundingRate":%q,"premium":"0.0001","time":%d}`,
			fundingHistoryRate(hour), fundingHistoryStamp(hour))
	}
	builder.WriteString("]")
	return builder.String()
}

// bitgetFundingHistoryPageBody renders one page of Bitget's newest-first
// history, where row i settled eight hours times (i+1) before the window end.
func bitgetFundingHistoryPageBody(firstIndex, lastIndex int64) string {
	var builder strings.Builder
	builder.WriteString(`{"code":"00000","msg":"success","requestTime":1720051200000,"data":[`)
	for index := firstIndex; index <= lastIndex; index++ {
		if index > firstIndex {
			builder.WriteString(",")
		}
		fmt.Fprintf(&builder, `{"symbol":"BTCUSDT","fundingRate":%q,"fundingTime":"%d"}`,
			fundingHistoryRate(index), fundingHistoryStamp(-8*(index+1)))
	}
	builder.WriteString("]}")
	return builder.String()
}

// TestFundingHistoryLoaderPerVenueShape pins each venue's reply to the series a
// chart is drawn from.
//
// The seven cases are not seven copies of one assertion. Every venue here
// disagrees with the others about something load-bearing — the direction rows
// arrive in, whether stamps are seconds or milliseconds, which of two rate
// fields was actually charged, whether the sign lives in the rate or beside it,
// whether a window can be expressed at all — and each case exists to hold one of
// those disagreements still. What they share is the output: an ascending series
// of settled rates whose intervals were measured rather than assumed.
func TestFundingHistoryLoaderPerVenueShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		build  func(*testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture)
		want   []wantHistoryPoint
		assert func(*testing.T, *fundingHistoryFixture)
	}{
		{
			// The unparsable row sits between two settlements rather than at an
			// end, so keeping it would halve every measured interval instead of
			// merely adding a point. Four-hour gaps on an eight-hourly contract
			// would double the daily rate the arbitrage engine derives.
			name: "binance drops the row it cannot read and keeps the clock intact",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/fapi/v1/fundingRate": {`[
						{"symbol":"BTCUSDT","fundingRate":"0.0002","fundingTime":1720022400000,"markPrice":"64000"},
						{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":1719964800000,"markPrice":"63000"},
						{"symbol":"BTCUSDT","fundingRate":"","fundingTime":1720008000000,"markPrice":"63500"},
						{"symbol":"BTCUSDT","fundingRate":"-0.00005","fundingTime":1719993600000,"markPrice":"63200"}
					]`},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDT",
					NativeSymbol: "BTCUSDT",
					Scope:        fundingBoardScope(exchange.VenueBinance, exchange.EnvironmentLive, "USDT"),
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(24),
				}
				return NewBinanceFundingHistoryLoader(fixture.settings("live")), request, fixture
			},
			want: []wantHistoryPoint{
				{millis: fundingHistoryStamp(0), rate: "0.0001"},
				{millis: fundingHistoryStamp(8), rate: "-0.00005", interval: 8},
				{millis: fundingHistoryStamp(16), rate: "0.0002", interval: 8},
			},
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/fapi/v1/fundingRate")
				if len(calls) != 1 {
					t.Fatalf("binance issued %d calls, want 1", len(calls))
				}
				// The native spelling is what goes on the wire; the canonical
				// one never leaves the request.
				assertFundingHistoryQuery(t, calls[0].query, map[string]string{
					"symbol":    "BTCUSDT",
					"startTime": fmt.Sprint(fundingHistoryStamp(0)),
					"endTime":   fmt.Sprint(fundingHistoryStamp(24)),
					"limit":     "1000",
				})
			},
		},
		{
			// No NativeSymbol here, which is the direct-caller shape: OKX names
			// its swaps the same way the request does, so the fallback in
			// fundingHistoryNative is the whole reason this case can be written
			// without a board lookup in front of it.
			name: "okx charts what it charged rather than what it predicted",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/api/v5/public/funding-rate-history": {`{"code":"0","msg":"","data":[
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.00012","realizedRate":"0.00011","fundingTime":"1719964800000","method":"current_period"},
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.00013","realizedRate":"","fundingTime":"1719993600000","method":"current_period"},
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"","realizedRate":"","fundingTime":"1720022400000","method":"current_period"}
					]}`},
				})
				request := exchange.FundingHistoryRequest{
					Symbol: "BTC-USDT-SWAP",
					Scope:  fundingBoardScope(exchange.VenueOKX, exchange.EnvironmentLive, "USDT"),
					Since:  fundingHistoryTime(0),
					Until:  fundingHistoryTime(24),
				}
				return NewOKXFundingHistoryLoader(fixture.settings("live")), request, fixture
			},
			want: []wantHistoryPoint{
				{millis: fundingHistoryStamp(0), rate: "0.00011"},
				{millis: fundingHistoryStamp(8), rate: "0.00013", interval: 8},
			},
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/api/v5/public/funding-rate-history")
				if len(calls) != 1 {
					t.Fatalf("okx issued %d calls, want 1", len(calls))
				}
				// OKX names its cursors for the row, not for the clock: `before`
				// takes the older bound and `after` the newer one. Swapped, this
				// endpoint answers with an empty list rather than an error, which
				// reads on screen as "this instrument has no history".
				assertFundingHistoryQuery(t, calls[0].query, map[string]string{
					"instId": "BTC-USDT-SWAP",
					"before": fmt.Sprint(fundingHistoryStamp(0)),
					"after":  fmt.Sprint(fundingHistoryStamp(24)),
					"limit":  "100",
				})
			},
		},
		{
			name: "bybit arrives newest-first and comes back ascending",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/v5/market/funding/history": {`{"retCode":0,"retMsg":"OK","time":1720051200000,"result":{"category":"linear","list":[
						{"symbol":"BTCUSDT","fundingRate":"0.0002","fundingRateTimestamp":"1720022400000"},
						{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateTimestamp":"1719993600000"},
						{"symbol":"BTCUSDT","fundingRate":"0.00005","fundingRateTimestamp":"1719964800000"}
					]}}`},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDT",
					NativeSymbol: "BTCUSDT",
					Scope:        fundingBoardScope(exchange.VenueBybit, exchange.EnvironmentLive, "USDT"),
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(24),
				}
				return NewBybitFundingHistoryLoader(fixture.settings("live")), request, fixture
			},
			want: []wantHistoryPoint{
				{millis: fundingHistoryStamp(0), rate: "0.00005"},
				{millis: fundingHistoryStamp(8), rate: "0.0001", interval: 8},
				{millis: fundingHistoryStamp(16), rate: "0.0002", interval: 8},
			},
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/v5/market/funding/history")
				if len(calls) != 1 {
					t.Fatalf("bybit issued %d calls, want 1", len(calls))
				}
				assertFundingHistoryQuery(t, calls[0].query, map[string]string{
					"category":  "linear",
					"symbol":    "BTCUSDT",
					"startTime": fmt.Sprint(fundingHistoryStamp(0)),
					"endTime":   fmt.Sprint(fundingHistoryStamp(24)),
					"limit":     "200",
				})
			},
		},
		{
			// Bitget takes no window at all, so the walk is the only way to
			// address one. The second page deliberately runs one settlement past
			// the requested start: that pre-window row is dropped from the
			// series but is what gives the first in-window point an interval,
			// which is the whole reason the finaliser measures before it clips.
			name: "bitget pages back to the requested start and dates the first point",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/api/v2/mix/market/history-fund-rate": {
						bitgetFundingHistoryPageBody(0, 99),
						bitgetFundingHistoryPageBody(100, 120),
					},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDT",
					NativeSymbol: "BTCUSDT",
					Scope:        fundingBoardScope(exchange.VenueBitget, exchange.EnvironmentLive, "USDT"),
					Since:        fundingHistoryTime(-960),
					Until:        fundingHistoryTime(0),
				}
				return NewBitgetFundingHistoryLoader("USDT-FUTURES", fixture.settings("live")), request, fixture
			},
			want: func() []wantHistoryPoint {
				points := make([]wantHistoryPoint, 0, 120)
				for index := int64(119); index >= 0; index-- {
					points = append(points, wantHistoryPoint{
						millis:   fundingHistoryStamp(-8 * (index + 1)),
						rate:     fundingHistoryRate(index),
						interval: 8,
					})
				}
				return points
			}(),
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/api/v2/mix/market/history-fund-rate")
				if len(calls) != 2 {
					t.Fatalf("bitget issued %d calls, want 2", len(calls))
				}
				for index, call := range calls {
					assertFundingHistoryQuery(t, call.query, map[string]string{
						"symbol":      "BTCUSDT",
						"productType": "USDT-FUTURES",
						"pageSize":    "100",
						"pageNo":      fmt.Sprint(index + 1),
					})
				}
			},
		},
		{
			// Gate is the one venue that stamps in seconds. The zero-stamped row
			// stands for the placeholder Gate occasionally emits: read as
			// seconds it would date to 1970 and stretch the chart's x-axis by
			// fifty years.
			name: "gate stamps in seconds and drops the placeholder row",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/futures/usdt/funding_rate": {`[
						{"t":1720022400,"r":"0.0002"},
						{"t":1719993600,"r":"0.0001"},
						{"t":0,"r":"0.00009"},
						{"t":1719964800,"r":"0.00005"}
					]`},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDT",
					NativeSymbol: "BTC_USDT",
					Scope:        fundingBoardScope(exchange.VenueGate, exchange.EnvironmentLive, "USDT"),
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(24),
				}
				return NewGateFundingHistoryLoader(fixture.settings("live")), request, fixture
			},
			want: []wantHistoryPoint{
				{millis: fundingHistoryStamp(0), rate: "0.00005"},
				{millis: fundingHistoryStamp(8), rate: "0.0001", interval: 8},
				{millis: fundingHistoryStamp(16), rate: "0.0002", interval: 8},
			},
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/futures/usdt/funding_rate")
				if len(calls) != 1 {
					t.Fatalf("gate issued %d calls, want 1", len(calls))
				}
				assertFundingHistoryQuery(t, calls[0].query, map[string]string{
					"contract": "BTC_USDT",
					"from":     fmt.Sprint(fundingHistoryStamp(0) / 1000),
					"to":       fmt.Sprint(fundingHistoryStamp(24) / 1000),
					"limit":    "1000",
				})
			},
		},
		{
			// Hyperliquid counts five hundred settlements forward from the start
			// of the window and stops, so an unwalked twenty-five-day hourly
			// request ends four days before the window does — at the recent end,
			// which is the end a funding chart is read for. The second page
			// repeats its own cursor row, which the finaliser must dedup rather
			// than plot twice.
			name: "hyperliquid walks forward to the end of the window",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/info": {
						hyperliquidFundingHistoryPageBody(0, 499),
						hyperliquidFundingHistoryPageBody(499, 600),
					},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDC",
					NativeSymbol: "BTC",
					Scope:        fundingBoardScope(exchange.VenueHyperliquid, exchange.EnvironmentLive, "USDC"),
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(600),
				}
				return NewHyperliquidFundingHistoryLoader(fixture.settings("mainnet")), request, fixture
			},
			want: func() []wantHistoryPoint {
				points := make([]wantHistoryPoint, 0, 601)
				for hour := int64(0); hour <= 600; hour++ {
					interval := int64(1)
					if hour == 0 {
						interval = 0
					}
					points = append(points, wantHistoryPoint{
						millis:   fundingHistoryStamp(hour),
						rate:     fundingHistoryRate(hour),
						interval: interval,
					})
				}
				return points
			}(),
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/info")
				if len(calls) != 2 {
					t.Fatalf("hyperliquid issued %d calls, want 2", len(calls))
				}
				wantStart := []int64{fundingHistoryStamp(0), fundingHistoryStamp(499) + 1}
				for index, call := range calls {
					var body struct {
						Type      string `json:"type"`
						Coin      string `json:"coin"`
						StartTime int64  `json:"startTime"`
						EndTime   int64  `json:"endTime"`
					}
					if err := json.Unmarshal([]byte(call.body), &body); err != nil {
						t.Fatalf("call %d body: %v", index, err)
					}
					if body.Type != "fundingHistory" || body.Coin != "BTC" {
						t.Errorf("call %d requested %s/%s, want fundingHistory/BTC", index, body.Type, body.Coin)
					}
					if body.StartTime != wantStart[index] {
						t.Errorf("call %d startTime = %d, want %d", index, body.StartTime, wantStart[index])
					}
					if body.EndTime != fundingHistoryStamp(600) {
						t.Errorf("call %d endTime = %d, want %d", index, body.EndTime, fundingHistoryStamp(600))
					}
				}
			},
		},
		{
			// Three Lighter-only conversions in one table. The rate is a
			// percentage, so "0.0012" means 1.2e-5 and reading it as a fraction
			// would report a thousand percent a year. The sign lives in
			// direction, not in the rate, so a series read without it never goes
			// negative. And an unrecognised direction is dropped rather than
			// guessed, which shows up honestly as the doubled interval on the
			// point after the gap.
			name: "lighter divides by a hundred and takes its sign from direction",
			build: func(t *testing.T) (exchange.FundingHistoryLoader, exchange.FundingHistoryRequest, *fundingHistoryFixture) {
				fixture := newFundingHistoryFixture(t, map[string][]string{
					"/api/v1/fundings": {`{"code":200,"message":"","resolution":"1h","fundings":[
						{"timestamp":1719964800,"value":"12.5","rate":"0.0012","direction":"long"},
						{"timestamp":1719968400,"value":"12.5","rate":"0.0012","direction":"short"},
						{"timestamp":1719972000,"value":"12.5","rate":"0.0012","direction":""},
						{"timestamp":1719975600,"value":"25.0","rate":"0.0024","direction":"long"}
					]}`},
				})
				request := exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDC",
					NativeSymbol: "BTC",
					NativeID:     "1",
					Scope:        fundingBoardScope(exchange.VenueLighter, exchange.EnvironmentLive, "USDC"),
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(24),
				}
				return NewLighterFundingHistoryLoader(fixture.settings("mainnet")), request, fixture
			},
			want: []wantHistoryPoint{
				{millis: fundingHistoryStamp(0), rate: "0.000012"},
				{millis: fundingHistoryStamp(1), rate: "-0.000012", interval: 1},
				{millis: fundingHistoryStamp(3), rate: "0.000024", interval: 2},
			},
			assert: func(t *testing.T, fixture *fundingHistoryFixture) {
				calls := fixture.callsFor("/api/v1/fundings")
				if len(calls) != 1 {
					t.Fatalf("lighter issued %d calls, want 1", len(calls))
				}
				// Lighter is addressed by market id, takes its window in
				// seconds, and counts rows rather than bounding them: one per
				// hour of the window, plus the settlement that opens it.
				assertFundingHistoryQuery(t, calls[0].query, map[string]string{
					"market_id":       "1",
					"resolution":      "1h",
					"start_timestamp": fmt.Sprint(fundingHistoryStamp(0) / 1000),
					"end_timestamp":   fmt.Sprint(fundingHistoryStamp(24) / 1000),
					"count_back":      "25",
				})
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			loader, request, fixture := testCase.build(t)

			snapshot, err := loader.LoadFundingHistory(context.Background(), request)
			if err != nil {
				t.Fatalf("LoadFundingHistory: %v", err)
			}
			// Validate is what the connector runs on the way back, so running it
			// here keeps the fixtures honest about the contract the gateway
			// relies on rather than only about the fields this test reads.
			if err := snapshot.Validate(request); err != nil {
				t.Fatalf("snapshot.Validate: %v", err)
			}
			if snapshot.SchemaVersion != 1 {
				t.Errorf("schema version = %d, want 1", snapshot.SchemaVersion)
			}
			if snapshot.Scope != request.Scope {
				t.Errorf("scope = %+v, want %+v", snapshot.Scope, request.Scope)
			}
			if snapshot.Symbol != request.Symbol {
				t.Errorf("symbol = %q, want %q", snapshot.Symbol, request.Symbol)
			}
			if snapshot.ObservedAt.IsZero() {
				t.Error("snapshot carries no observation time")
			}
			assertFundingHistoryPoints(t, snapshot.Points, testCase.want)
			if testCase.assert != nil {
				testCase.assert(t, fixture)
			}
		})
	}
}

// fundingHistoryDeniedTransport fails every request and counts the attempts, so
// a loader that reached the network can be told apart from one that refused.
type fundingHistoryDeniedTransport struct{ calls atomic.Int64 }

func (transport *fundingHistoryDeniedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	return nil, errors.New("funding history window test issued a request")
}

func fundingHistoryLoaderBuilders() []struct {
	name        string
	environment string
	build       func(Settings) exchange.FundingHistoryLoader
} {
	return []struct {
		name        string
		environment string
		build       func(Settings) exchange.FundingHistoryLoader
	}{
		{name: "binance", environment: "live", build: NewBinanceFundingHistoryLoader},
		{name: "okx", environment: "live", build: NewOKXFundingHistoryLoader},
		{name: "bybit", environment: "live", build: NewBybitFundingHistoryLoader},
		{name: "bitget", environment: "live", build: func(settings Settings) exchange.FundingHistoryLoader {
			return NewBitgetFundingHistoryLoader("USDT-FUTURES", settings)
		}},
		{name: "gate", environment: "live", build: NewGateFundingHistoryLoader},
		{name: "hyperliquid", environment: "mainnet", build: NewHyperliquidFundingHistoryLoader},
		{name: "lighter", environment: "mainnet", build: NewLighterFundingHistoryLoader},
	}
}

// TestFundingHistoryWindowIsBounded holds every loader to the same refusal.
//
// An unbounded request is not an empty chart — it is a chart. Several of these
// venues read a missing start as "since listing" and answer with whatever tail
// of that they feel like returning, so the two legs of one arbitrage pair would
// come back covering different periods with nothing on screen to say so. The
// refusal has to happen before the call for that reason, which is what the
// request counter checks: a loader that asks first and validates after would
// still return the right error and the wrong series.
func TestFundingHistoryWindowIsBounded(t *testing.T) {
	t.Parallel()

	mutations := []struct {
		name    string
		mutate  func(exchange.FundingHistoryRequest) exchange.FundingHistoryRequest
		message string
	}{
		{
			name: "no symbol",
			mutate: func(request exchange.FundingHistoryRequest) exchange.FundingHistoryRequest {
				request.Symbol = ""
				request.NativeSymbol = ""
				return request
			},
			message: "funding history requires a symbol",
		},
		{
			name: "open start",
			mutate: func(request exchange.FundingHistoryRequest) exchange.FundingHistoryRequest {
				request.Since = time.Time{}
				return request
			},
			message: "funding history requires a bounded window",
		},
		{
			name: "open end",
			mutate: func(request exchange.FundingHistoryRequest) exchange.FundingHistoryRequest {
				request.Until = time.Time{}
				return request
			},
			message: "funding history requires a bounded window",
		},
		{
			name: "empty window",
			mutate: func(request exchange.FundingHistoryRequest) exchange.FundingHistoryRequest {
				request.Until = request.Since
				return request
			},
			message: "funding history window ends before it starts",
		},
	}

	for _, venue := range fundingHistoryLoaderBuilders() {
		for _, mutation := range mutations {
			t.Run(venue.name+"/"+mutation.name, func(t *testing.T) {
				t.Parallel()
				transport := &fundingHistoryDeniedTransport{}
				loader := venue.build(Settings{
					Endpoint:    "https://funding-history.test",
					Environment: venue.environment,
					HTTPClient:  &http.Client{Transport: transport},
				})
				request := mutation.mutate(exchange.FundingHistoryRequest{
					Symbol:       "BTC-USDT",
					NativeSymbol: "BTCUSDT",
					NativeID:     "1",
					Since:        fundingHistoryTime(0),
					Until:        fundingHistoryTime(24),
				})

				_, err := loader.LoadFundingHistory(context.Background(), request)
				assertFundingHistoryRejection(t, err, mutation.message)
				if calls := transport.calls.Load(); calls != 0 {
					t.Errorf("loader issued %d requests before rejecting the window", calls)
				}
			})
		}
	}
}

// TestLighterFundingHistoryRequiresNumericMarketID keeps the one venue that is
// addressed by an integer from silently sending a symbol.
//
// Lighter answers a symbol in the market_id slot with a parameter error, which
// surfaces as "the venue is down" rather than "the instrument was never
// resolved" — the same series looks unavailable instead of unrequested.
func TestLighterFundingHistoryRequiresNumericMarketID(t *testing.T) {
	t.Parallel()

	for _, nativeID := range []string{"", "BTC", "1.5"} {
		t.Run("id="+nativeID, func(t *testing.T) {
			t.Parallel()
			transport := &fundingHistoryDeniedTransport{}
			loader := NewLighterFundingHistoryLoader(Settings{
				Endpoint:    "https://funding-history.test",
				Environment: "mainnet",
				HTTPClient:  &http.Client{Transport: transport},
			})

			_, err := loader.LoadFundingHistory(context.Background(), exchange.FundingHistoryRequest{
				Symbol:       "BTC-USDC",
				NativeSymbol: "BTC",
				NativeID:     nativeID,
				Since:        fundingHistoryTime(0),
				Until:        fundingHistoryTime(24),
			})
			assertFundingHistoryRejection(t, err, "lighter funding history requires a numeric market id")
			if calls := transport.calls.Load(); calls != 0 {
				t.Errorf("loader issued %d requests with an unresolved market id", calls)
			}
		})
	}
}

func assertFundingHistoryRejection(t *testing.T, err error, message string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a rejection, got none")
	}
	var exchangeErr *exchange.Error
	if !errors.As(err, &exchangeErr) {
		t.Fatalf("error %v is not an exchange error", err)
	}
	if exchangeErr.Kind() != exchange.KindInvalidConfig {
		t.Errorf("kind = %s, want %s", exchangeErr.Kind(), exchange.KindInvalidConfig)
	}
	if operation := exchangeErr.Details().Operation; operation != "FundingHistory.Load" {
		t.Errorf("operation = %q, want %q", operation, "FundingHistory.Load")
	}
	if safe := exchangeErr.Details().SafeMessage; safe != message {
		t.Errorf("message = %q, want %q", safe, message)
	}
}
