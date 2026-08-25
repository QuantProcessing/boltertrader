package factoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// wantEntry is a funding row written the way a venue's documentation writes it,
// so a fixture and its expectation can be read side by side.
//
// Every optional cell is expressed as its absent value ("" or 0) rather than as
// a flag, because absence is the thing under test: most of the nine venues leave
// at least one of these empty, and a board that renders an absent cell as zero
// invents a number the venue never published.
type wantEntry struct {
	symbol               string
	nativeSymbol         string
	nativeID             string
	base                 string
	quote                string
	rate                 string
	intervalHours        int64  // 0 = venue published no interval
	nextFunding          int64  // unix seconds; 0 = venue published no settlement clock
	markPrice            string // "" = absent
	indexPrice           string // "" = absent
	openInterest         string // "" = absent
	openInterestNotional string // "" = absent; quote-stablecoin notional
}

// TestFundingBoardLoaderPerVenueShape pins each venue's field mapping and,
// more importantly, each venue's degradation.
//
// The board is one table built from nine differently-shaped venue replies, and the
// mapping is where a venue's quirk turns into a wrong number rather than an
// error: Gate publishes its interval in seconds, Nado its rate scaled by 10^18
// over a 24-hour period, Hyperliquid an hourly rate with no interval field at
// all. Each of those is a silent factor-of-N bug in a spread the operator is
// about to trade against, so each is asserted against a fixed reply rather than
// left to the loader to be trusted on.
//
// The fixtures deliberately include rows that must NOT survive — a Binance
// quarterly future, an OKX blank rate, a Bybit row settling in the wrong asset,
// a Lighter row Lighter is merely relaying from another venue, a delisted Gate
// contract — because a board silently gaining a phantom instrument is worse
// than one visibly missing a real one.
func TestFundingBoardLoaderPerVenueShape(t *testing.T) {
	cases := []struct {
		name string
		// publishesOpenInterest states the venue's bulk-call contract. Only
		// four of the nine answer with open interest, and the board's sort
		// falls back to rate spread precisely because of that; asserting it per
		// venue is what keeps the fallback honest.
		publishesOpenInterest bool
		build                 func(*testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope)
		want                  []wantEntry
	}{
		{
			name: "aster uses its v3 all-market premium index and filters settlement",
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/fapi/v3/premiumIndex": `[
						{"symbol":"BTCUSDT","markPrice":"64010.25","indexPrice":"64008.75","lastFundingRate":"0.00012","nextFundingTime":1720000000000},
						{"symbol":"ETHUSDC","markPrice":"3400.00","indexPrice":"3399.00","lastFundingRate":"-0.00005","nextFundingTime":1720000000000}
					]`,
					"/fapi/v3/fundingInfo": `[
						{"symbol":"BTCUSDT","fundingIntervalHours":4},
						{"symbol":"ETHUSDC","fundingIntervalHours":8}
					]`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewAsterFundingBoardLoader("USDT", settings), fundingBoardScope(exchange.VenueAster, "USDT")
			},
			want: []wantEntry{{
				symbol: "BTC-USDT", nativeSymbol: "BTCUSDT", base: "BTC", quote: "USDT",
				rate: "0.00012", intervalHours: 4, nextFunding: 1720000000,
				markPrice: "64010.25", indexPrice: "64008.75",
			}},
		},
		{
			name: "binance splits a concatenated symbol and takes its interval from fundingInfo",
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/fapi/v1/premiumIndex": `[
						{"symbol":"BTCUSDT","markPrice":"64000.10","indexPrice":"63999.50","lastFundingRate":"0.00010000","nextFundingTime":1720000000000},
						{"symbol":"ETHUSDC","markPrice":"3400.00","indexPrice":"3399.00","lastFundingRate":"-0.00005000","nextFundingTime":1720000000000},
						{"symbol":"BTCUSDT_240628","markPrice":"64500.00","indexPrice":"64400.00","lastFundingRate":"0.00020000","nextFundingTime":1720000000000}
					]`,
					"/fapi/v1/fundingInfo": `[{"symbol":"BTCUSDT","fundingIntervalHours":4}]`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewBinanceFundingBoardLoader("USDT", settings), fundingBoardScope(exchange.VenueBinance, "USDT")
			},
			// ETHUSDC settles in the wrong asset for this loader and the
			// quarterly has no quote suffix to split on; both are dropped.
			want: []wantEntry{{
				symbol: "BTC-USDT", nativeSymbol: "BTCUSDT", base: "BTC", quote: "USDT",
				rate: "0.0001", intervalHours: 4, nextFunding: 1720000000,
				markPrice: "64000.10", indexPrice: "63999.50",
			}},
		},
		{
			name:                  "okx keeps the native instId and derives its interval from two settlement stamps",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/api/v5/public/funding-rate": `{"code":"0","msg":"","data":[
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.00012","fundingTime":"1720000000000","nextFundingTime":"1720028800000"},
						{"instType":"SWAP","instId":"ETH-USDT-SWAP","fundingRate":"","fundingTime":"1720000000000","nextFundingTime":"1720028800000"},
						{"instType":"FUTURES","instId":"BTC-USDT-240628","fundingRate":"0.00001","fundingTime":"1720000000000","nextFundingTime":"1720028800000"}
					]}`,
					"/api/v5/public/mark-price": `{"code":"0","msg":"","data":[
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","markPx":"64000.1","ts":"1719999999000"}
					]}`,
					"/api/v5/public/open-interest": `{"code":"0","msg":"","data":[
						{"instType":"SWAP","instId":"BTC-USDT-SWAP","oi":"20","oiCcy":"12.5","oiUsd":"800001.25","ts":"1719999998000"}
					]}`,
					"/api/v5/market/index-tickers": `{"code":"0","msg":"","data":[
						{"instId":"BTC-USDT","idxPx":"63999.9","ts":"1719999997000"}
					]}`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewOKXFundingBoardLoader("USDT", settings), fundingBoardScope(exchange.VenueOKX, "USDT")
			},
			// The blank-rate row is dropped rather than read as zero: OKX leaves
			// the field empty when it has no rate to quote, and a 0.0000% cell
			// on the board is a spread the operator can act on.
			want: []wantEntry{{
				symbol: "BTC-USDT-SWAP", nativeSymbol: "BTC-USDT-SWAP", base: "BTC", quote: "USDT",
				rate: "0.00012", intervalHours: 8, nextFunding: 1720000000,
				markPrice: "64000.1", indexPrice: "63999.9", openInterest: "12.5",
				openInterestNotional: "800001.25",
			}},
		},
		{
			name:                  "bybit answers with open interest and filters by settlement asset",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/v5/market/tickers": `{"retCode":0,"retMsg":"OK","time":1719999999000,"result":{"category":"linear","list":[
						{"symbol":"BTCUSDT","markPrice":"64000.5","indexPrice":"63999.1","openInterest":"12345.67","fundingRate":"0.0001","nextFundingTime":"1720000000000","fundingIntervalHour":"8"},
						{"symbol":"ETHPERP","markPrice":"3400","indexPrice":"3399","openInterest":"500","fundingRate":"0.0002","nextFundingTime":"1720000000000","fundingIntervalHour":"8"}
					]}}`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewBybitFundingBoardLoader("USDT", settings), fundingBoardScope(exchange.VenueBybit, "USDT")
			},
			// Bybit serves every linear contract from one category, so the
			// settlement filter here is the only thing keeping a USDC column's
			// rates out of the USDT column.
			want: []wantEntry{{
				symbol: "BTC-USDT", nativeSymbol: "BTCUSDT", base: "BTC", quote: "USDT",
				rate: "0.0001", intervalHours: 8, nextFunding: 1720000000,
				markPrice: "64000.5", indexPrice: "63999.1", openInterest: "12345.67",
				openInterestNotional: "790129052.835",
			}},
		},
		{
			name: "bitget falls back to the instrument interval and publishes no open interest",
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/api/v3/market/tickers": `{"code":"00000","msg":"success","requestTime":1719999999000,"data":[
						{"category":"USDT-FUTURES","symbol":"BTCUSDT","markPrice":"64000","indexPrice":"63999","fundingRate":"0.00015","fundingRateInterval":"","nextFundingTime":"1720000000000"}
					]}`,
					"/api/v3/market/instruments": `{"code":"00000","msg":"success","requestTime":1719999999000,"data":[
						{"symbol":"BTCUSDT","baseCoin":"BTC","quoteCoin":"USDT","fundInterval":"8","status":"online"}
					]}`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewBitgetFundingBoardLoader("USDT-FUTURES", settings), fundingBoardScope(exchange.VenueBitget, "USDT")
			},
			// The ticker's own interval is blank here, which is what forces the
			// instruments call to be the source. That second call is best-effort
			// in the loader, so this row is the only proof it is wired at all.
			want: []wantEntry{{
				symbol: "BTC-USDT", nativeSymbol: "BTCUSDT", base: "BTC", quote: "USDT",
				rate: "0.00015", intervalHours: 8, nextFunding: 1720000000,
				markPrice: "64000", indexPrice: "63999",
			}},
		},
		{
			name:                  "gate converts a seconds interval to hours and skips a delisted contract's clock",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/futures/usdt/tickers": `[
						{"contract":"BTC_USDT","mark_price":"64000","index_price":"63999","funding_rate":"0.0001","total_size":"98765"},
						{"contract":"OLD_USDT","mark_price":"1.5","index_price":"1.4","funding_rate":"0.0003","total_size":"0"}
					]`,
					"/futures/usdt/contracts": `[
						{"name":"BTC_USDT","quanto_multiplier":"0.001","funding_interval":28800,"funding_next_apply":1720000000,"status":"trading"},
						{"name":"OLD_USDT","quanto_multiplier":"1","funding_interval":28800,"funding_next_apply":1720000000,"status":"delisting"}
					]`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewGateFundingBoardLoader(settings), fundingBoardScope(exchange.VenueGate, "USDT")
			},
			want: []wantEntry{
				{
					// 28800 seconds is eight hours. Passing it through as-is
					// would put the next settlement three years out.
					symbol: "BTC-USDT", nativeSymbol: "BTC_USDT", base: "BTC", quote: "USDT",
					rate: "0.0001", intervalHours: 8, nextFunding: 1720000000,
					markPrice: "64000", indexPrice: "63999", openInterest: "98765",
					openInterestNotional: "6320960",
				},
				{
					// Still quoted by the ticker feed, but delisting, so its
					// clock is withheld: an interval on a contract that will not
					// settle again is a countdown to nothing. A zero open size
					// is a real answer and stays present.
					symbol: "OLD-USDT", nativeSymbol: "OLD_USDT", base: "OLD", quote: "USDT",
					rate: "0.0003", markPrice: "1.5", indexPrice: "1.4", openInterest: "0",
					openInterestNotional: "0",
				},
			},
		},
		{
			name:                  "hyperliquid states the hourly interval the venue never sends",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					var payload map[string]any
					if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					var response string
					switch payload["type"] {
					case "allPerpMetas":
						response = `[{"universe":[{"name":"BTC","szDecimals":3},{"name":"#1","szDecimals":3},{"name":"K:PEPE","szDecimals":3}],"collateralToken":0}]`
					case "perpDexs":
						response = `[null]`
					case "spotMeta":
						response = `{"tokens":[{"name":"USDC","index":0,"szDecimals":6,"weiDecimals":6}],"universe":[]}`
					case "metaAndAssetCtxs":
						response = `[
						{"universe":[{"name":"BTC"},{"name":"#1"},{"name":"K:PEPE"}]},
						[
							{"funding":"0.0000125","markPx":"64000.0","oraclePx":"63999.0","openInterest":"1234.5"},
							{"funding":"0.00001","markPx":"1.0","oraclePx":"1.0","openInterest":"10"},
							{"funding":"0.00002","markPx":"2.0","oraclePx":"2.0","openInterest":"20"}
						]
					]`
					default:
						t.Fatalf("unexpected Hyperliquid request type %v", payload["type"])
					}
					_, _ = io.WriteString(writer, response)
				}))
				t.Cleanup(server.Close)
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewHyperliquidFundingBoardLoader(settings), fundingBoardScope(exchange.VenueHyperliquid, "USDC")
			},
			// Hyperliquid quotes an hourly rate and publishes no interval field,
			// so the 1 is asserted here rather than inferred. Comparing this
			// rate against an eight-hourly venue without it overstates the
			// spread eightfold — the exact error the board exists to avoid.
			// The index-position rows are spot pairs and non-tradable indices,
			// which the universe interleaves with perps.
			want: []wantEntry{{
				symbol: "BTC-USDC", nativeSymbol: "BTC", nativeID: "0", base: "BTC", quote: "USDC",
				rate: "0.0000125", intervalHours: 1,
				markPrice: "64000.0", indexPrice: "63999.0", openInterest: "1234.5",
				openInterestNotional: "79008000",
			}},
		},
		{
			name:                  "lighter drops relayed rates and enriches its own market facts",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				server := fundingBoardFixtureServer(t, map[string]string{
					"/api/v1/funding-rates": `{"code":200,"message":"","funding_rates":[
						{"market_id":1,"exchange":"lighter","symbol":"BTC","rate":0.0001},
						{"market_id":2,"exchange":"binance","symbol":"ETH","rate":0.0002}
					]}`,
					"/api/v1/orderBookDetails": `{"code":200,"message":"","order_book_details":[
						{"market_id":1,"market_type":"perp","symbol":"BTC","mark_price":"64000","index_price":"63990","open_interest":12.5}
					]}`,
				})
				settings := Settings{Endpoint: server.URL, HTTPClient: server.Client()}
				return NewLighterFundingBoardLoader(settings), fundingBoardScope(exchange.VenueLighter, "USDC")
			},
			// Lighter's endpoint is a comparison feed: it returns other venues'
			// rates alongside its own. Filing a Binance rate in the Lighter
			// column would show a zero spread against Binance and a real one
			// against everyone else.
			want: []wantEntry{{
				symbol: "BTC-USDC", nativeSymbol: "BTC", nativeID: "1", base: "BTC", quote: "USDC",
				rate: "0.0001", intervalHours: 8, markPrice: "64000", indexPrice: "63990",
				openInterest: "12.5", openInterestNotional: "800000",
			}},
		},
		{
			name:                  "nado unscales an x18 rate quoted over twenty-four hours",
			publishesOpenInterest: true,
			build: func(t *testing.T) (exchange.FundingBoardLoader, exchange.CatalogScope) {
				settings := nadoCatalogTestSettings(nadoFundingBoardRouter(t), exchange.ProductPerp)
				return NewNadoFundingBoardLoader(settings), settings.CatalogScope
			},
			// 10^14 unscaled by 10^18 is 0.0001, over a 24-hour period rather
			// than the eight hours most venues quote. Both conversions are
			// invisible in the reply and both are pure multiplication error if
			// missed. update_time is the funding observation clock and must not
			// be mistaken for the next settlement timestamp.
			want: []wantEntry{{
				symbol: "ETH-PERP-USDT0", nativeSymbol: "ETH-PERP_USDT0", nativeID: "2",
				base: "ETH", quote: "USDT0",
				rate: "0.0001", intervalHours: 24, nextFunding: 1720003600,
				markPrice: "100", indexPrice: "100", openInterest: "1", openInterestNotional: "100",
			}},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			loader, scope := testCase.build(t)
			snapshot, err := loader.LoadFundingBoard(context.Background(), scope)
			if err != nil {
				t.Fatalf("LoadFundingBoard: %v", err)
			}

			// The connector validates every snapshot before it reaches a board,
			// so a loader that produced a duplicate or unnamed symbol would fail
			// there rather than here. Running it now attributes the failure to
			// the venue that caused it.
			if err := snapshot.Validate(scope); err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if snapshot.SchemaVersion != 1 {
				t.Fatalf("SchemaVersion = %d, want 1", snapshot.SchemaVersion)
			}
			if snapshot.Scope != scope {
				t.Fatalf("Scope = %+v, want %+v", snapshot.Scope, scope)
			}
			if snapshot.ObservedAt.IsZero() {
				t.Fatal("ObservedAt is zero; a board cannot mark a column stale without it")
			}

			if len(snapshot.Entries) != len(testCase.want) {
				t.Fatalf("got %d entries, want %d: %+v", len(snapshot.Entries), len(testCase.want), snapshot.Entries)
			}
			for index, want := range testCase.want {
				assertFundingEntry(t, snapshot.Entries[index], want)
			}

			// Stated as its own assertion because the board's default sort
			// depends on it: open interest is not a column every venue can
			// fill, and a sort that assumed otherwise would drop valid venues.
			for _, entry := range snapshot.Entries {
				if entry.OpenInterest.Valid != testCase.publishesOpenInterest {
					t.Fatalf("%s: OpenInterest present = %t, want %t", entry.Symbol, entry.OpenInterest.Valid, testCase.publishesOpenInterest)
				}
			}
		})
	}
}

func TestBybitLinearFundingBoardSharesOneBulkTickerAcrossSettlements(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v5/market/tickers" || request.URL.Query().Get("category") != "linear" {
			t.Errorf("unexpected request %s?%s", request.URL.Path, request.URL.RawQuery)
			http.Error(writer, "unexpected request", http.StatusNotFound)
			return
		}
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"retCode":0,"retMsg":"OK","time":1719999999000,"result":{"category":"linear","list":[
			{"symbol":"BTCUSDT","markPrice":"64000","indexPrice":"63999","openInterest":"10","fundingRate":"0.0001","nextFundingTime":"1720000000000","fundingIntervalHour":"8"},
			{"symbol":"ETHUSDC","markPrice":"3400","indexPrice":"3399","openInterest":"20","fundingRate":"-0.0002","nextFundingTime":"1720000000000","fundingIntervalHour":"8"}
		]}}`)
	}))
	t.Cleanup(server.Close)

	client := server.Client()
	settings := Settings{Endpoint: server.URL, HTTPClient: client}
	cacheKey := fmt.Sprintf("%p|%s", client, server.URL)
	t.Cleanup(func() {
		bybitLinearTickerCache.Lock()
		delete(bybitLinearTickerCache.values, cacheKey)
		bybitLinearTickerCache.Unlock()
	})

	settlements := []string{"USDT", "USDC"}
	snapshots := make([]exchange.FundingBoardSnapshot, len(settlements))
	errors := make([]error, len(settlements))
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index, settlement := range settlements {
		workers.Add(1)
		go func(index int, settlement string) {
			defer workers.Done()
			<-start
			snapshots[index], errors[index] = NewBybitFundingBoardLoader(settlement, settings).LoadFundingBoard(
				t.Context(), fundingBoardScope(exchange.VenueBybit, settlement),
			)
		}(index, settlement)
	}
	close(start)
	workers.Wait()

	for index, err := range errors {
		if err != nil {
			t.Fatalf("%s board: %v", settlements[index], err)
		}
		if got := len(snapshots[index].Entries); got != 1 {
			t.Fatalf("%s entries = %d, want 1", settlements[index], got)
		}
		if got := snapshots[index].Entries[0].QuoteAsset; got != settlements[index] {
			t.Fatalf("%s quote = %q", settlements[index], got)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("Bybit linear ticker requests = %d, want 1 shared request", got)
	}
}

func assertFundingEntry(t *testing.T, got exchange.FundingBoardEntry, want wantEntry) {
	t.Helper()

	if got.Symbol != want.symbol {
		t.Fatalf("Symbol = %q, want %q", got.Symbol, want.symbol)
	}
	if got.NativeSymbol != want.nativeSymbol {
		t.Fatalf("%s: NativeSymbol = %q, want %q", want.symbol, got.NativeSymbol, want.nativeSymbol)
	}
	if got.NativeID != want.nativeID {
		t.Fatalf("%s: NativeID = %q, want %q", want.symbol, got.NativeID, want.nativeID)
	}
	if got.BaseAsset != want.base {
		t.Fatalf("%s: BaseAsset = %q, want %q", want.symbol, got.BaseAsset, want.base)
	}
	if got.QuoteAsset != want.quote {
		t.Fatalf("%s: QuoteAsset = %q, want %q", want.symbol, got.QuoteAsset, want.quote)
	}
	if wantRate := decimal.RequireFromString(want.rate); !got.Rate.Equal(wantRate) {
		t.Fatalf("%s: Rate = %s, want %s", want.symbol, got.Rate, wantRate)
	}

	if want.intervalHours == 0 {
		if got.IntervalHours.Valid {
			t.Fatalf("%s: IntervalHours = %d, want absent", want.symbol, got.IntervalHours.Value)
		}
	} else if !got.IntervalHours.Valid || got.IntervalHours.Value != want.intervalHours {
		t.Fatalf("%s: IntervalHours = %+v, want %d", want.symbol, got.IntervalHours, want.intervalHours)
	}

	if want.nextFunding == 0 {
		if !got.NextFundingTime.IsZero() {
			t.Fatalf("%s: NextFundingTime = %s, want absent", want.symbol, got.NextFundingTime)
		}
	} else if wantNext := time.Unix(want.nextFunding, 0).UTC(); !got.NextFundingTime.Equal(wantNext) {
		t.Fatalf("%s: NextFundingTime = %s, want %s", want.symbol, got.NextFundingTime, wantNext)
	}

	assertFundingOptional(t, want.symbol, "MarkPrice", got.MarkPrice, want.markPrice)
	assertFundingOptional(t, want.symbol, "IndexPrice", got.IndexPrice, want.indexPrice)
	assertFundingOptional(t, want.symbol, "OpenInterest", got.OpenInterest, want.openInterest)
	assertFundingOptional(t, want.symbol, "OpenInterestNotional", got.OpenInterestNotional, want.openInterestNotional)
}

func assertFundingOptional(t *testing.T, symbol, field string, got exchange.OptionalDecimal, want string) {
	t.Helper()

	if want == "" {
		if got.Valid {
			t.Fatalf("%s: %s = %s, want absent", symbol, field, got.Value)
		}
		return
	}
	if !got.Valid {
		t.Fatalf("%s: %s is absent, want %s", symbol, field, want)
	}
	if wantValue := decimal.RequireFromString(want); !got.Value.Equal(wantValue) {
		t.Fatalf("%s: %s = %s, want %s", symbol, field, got.Value, wantValue)
	}
}

func fundingBoardScope(venue exchange.Venue, settlement string) exchange.CatalogScope {
	return exchange.CatalogScope{
		Venue:       venue,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductPerp,
		Settlement:  settlement,
	}
}

// fundingBoardFixtureServer answers a fixed body per path and fails the test on
// any path it was not given, so a loader that silently changed endpoints is
// caught here rather than by an empty board in production.
func fundingBoardFixtureServer(t *testing.T, responses map[string]string) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, known := responses[request.URL.Path]
		if !known {
			t.Errorf("unexpected request path %s", request.URL.Path)
			http.Error(writer, "unexpected path", http.StatusNotFound)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)
	return server
}

// nadoFundingBoardRouter serves the shared Nado OpenAPI fixture for discovery
// and intercepts only the bulk funding POST.
//
// The shared router answers anything on the archive path containing
// "funding_rate" with a single-product object, which the per-symbol call
// expects. The bulk call sends {"funding_rates":{...}} — a superstring of that
// marker — and decodes into a map keyed by product id, so it must be answered
// before the shared router sees it.
func nadoFundingBoardRouter(t *testing.T) http.RoundTripper {
	t.Helper()

	return nadoRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1" || request.Body == nil {
			return newNadoOpenAPIRouter(t).RoundTrip(request)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read nado archive request: %v", err)
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		if !bytes.Contains(body, []byte(`"funding_rates"`)) {
			return newNadoOpenAPIRouter(t).RoundTrip(request)
		}
		return asterOpenAPIJSONResponse(`{"2":{"product_id":2,"funding_rate_x18":"100000000000000","update_time":"1720000000"}}`), nil
	})
}
