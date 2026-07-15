package perp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	runtimecache "github.com/QuantProcessing/boltertrader/runtime/cache"
	sdkperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
)

func TestPerpReferenceSnapshotStrictlyNormalizesFundingPricesAndFreshness(t *testing.T) {
	inst := mustPerpInstrument(t)
	now := time.UnixMilli(1700000012345)
	market := newMarketDataClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","nextFundingTime":1700007200000,"time":1700000000000}`), nil, testProvider(inst), clock.NewSimulatedClock(now))

	snapshot, err := market.ReferenceSnapshot(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("ReferenceSnapshot: %v", err)
	}
	if snapshot.InstrumentID != inst.ID || !snapshot.Timestamp.Equal(time.UnixMilli(1700000000000)) || !snapshot.ReceivedAt.Equal(now) {
		t.Fatalf("bad snapshot identity/timing: %#v", snapshot)
	}
	if !snapshot.Fields.Has(model.ReferenceHasFundingRate) || !snapshot.Fields.Has(model.ReferenceHasNextFundingTime) || !snapshot.Fields.Has(model.ReferenceHasMarkPrice) || !snapshot.Fields.Has(model.ReferenceHasIndexPrice) {
		t.Fatalf("missing reference fields: %#v", snapshot.Fields)
	}
	assertDec(t, snapshot.FundingRate, "0.00010000")
	assertDec(t, snapshot.MarkPrice, "1.2500")
	assertDec(t, snapshot.IndexPrice, "1.2480")
	if !snapshot.NextFundingTime.Equal(time.UnixMilli(1700007200000)) {
		t.Fatalf("next funding time=%s", snapshot.NextFundingTime)
	}
	if got := snapshot.FieldTimes.For(model.ReferenceFieldNextFundingTime); !got.Venue.Equal(snapshot.Timestamp) || !got.Received.Equal(now) {
		t.Fatalf("next funding freshness=%#v, want source update timestamp and received time", got)
	}
}

func TestPerpReferenceSnapshotRejectsMalformedPayloads(t *testing.T) {
	inst := mustPerpInstrument(t)
	cases := map[string]string{
		"nil timestamp":      `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000"}`,
		"wrong symbol":       `{"symbol":"OTHERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","time":1700000000000}`,
		"negative mark":      `{"symbol":"ASTERUSDT","markPrice":"-1","indexPrice":"1.2480","lastFundingRate":"0.00010000","time":1700000000000}`,
		"malformed index":    `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"bad","lastFundingRate":"0.00010000","time":1700000000000}`,
		"missing all fields": `{"symbol":"ASTERUSDT","time":1700000000000}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			market := newMarketDataClient(perpClientResponse(t, body), nil, testProvider(inst), nil)
			if _, err := market.ReferenceSnapshot(context.Background(), inst.ID); err == nil {
				t.Fatalf("ReferenceSnapshot accepted malformed payload")
			}
		})
	}
}

func TestPerpSubscribeReferencePublishesRESTSnapshotThenNativeMarkPriceStream(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	clk := clock.NewSimulatedClock(time.UnixMilli(1700000010000))
	market := newMarketDataClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","nextFundingTime":1700007200000,"time":1700000000000}`), ws, testProvider(inst), clk)

	if err := market.SubscribeReference(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeReference: %v", err)
	}
	restEnv := <-market.Events()
	if restEnv.Source != contract.SourceAdapterREST || !restEnv.Flags.Has(contract.EventFlagFromSnapshot) {
		t.Fatalf("REST reference envelope meta=%#v", restEnv.EventMeta)
	}
	if ws.markSymbol != inst.VenueSymbol || ws.markInterval != "1s" || ws.markSubs != 1 {
		t.Fatalf("mark subscription symbol=%q interval=%q subs=%d", ws.markSymbol, ws.markInterval, ws.markSubs)
	}

	clk.Advance(time.Second)
	if err := ws.markHandler(&sdkperp.WsMarkPriceEvent{
		EventTime:       1700000001000,
		Symbol:          inst.VenueSymbol,
		MarkPrice:       "1.2600",
		IndexPrice:      "1.2580",
		FundingRate:     "0.00020000",
		NextFundingTime: 1700007200000,
	}); err != nil {
		t.Fatalf("mark handler: %v", err)
	}
	streamEnv := <-market.Events()
	if streamEnv.Source != contract.SourceAdapterStream || !streamEnv.Flags.Has(contract.EventFlagFromStream) {
		t.Fatalf("stream reference envelope meta=%#v", streamEnv.EventMeta)
	}
	snapshot := streamEnv.Payload.(contract.ReferenceDataEvent).Snapshot
	if !snapshot.Timestamp.Equal(time.UnixMilli(1700000001000)) || !snapshot.ReceivedAt.Equal(clk.Now()) {
		t.Fatalf("bad stream reference timing: %#v", snapshot)
	}
	if !snapshot.Fields.Has(model.ReferenceHasFundingRate) || !snapshot.Fields.Has(model.ReferenceHasNextFundingTime) || !snapshot.Fields.Has(model.ReferenceHasMarkPrice) || !snapshot.Fields.Has(model.ReferenceHasIndexPrice) {
		t.Fatalf("missing stream fields: %#v", snapshot.Fields)
	}
	assertDec(t, snapshot.FundingRate, "0.00020000")
	assertDec(t, snapshot.MarkPrice, "1.2600")
	assertDec(t, snapshot.IndexPrice, "1.2580")
	if got := snapshot.FieldTimes.For(model.ReferenceFieldNextFundingTime); !got.Venue.Equal(snapshot.Timestamp) {
		t.Fatalf("next funding freshness=%#v, want event update timestamp", got)
	}
}

func TestPerpSubscribeReferenceWithoutWebsocketStillPublishesRESTSnapshot(t *testing.T) {
	inst := mustPerpInstrument(t)
	market := newMarketDataClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","nextFundingTime":1700007200000,"time":1700000000000}`), nil, testProvider(inst), nil)

	if err := market.SubscribeReference(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeReference REST-only: %v", err)
	}
	env := <-market.Events()
	if env.Source != contract.SourceAdapterREST || !env.Flags.Has(contract.EventFlagFromSnapshot) {
		t.Fatalf("REST-only reference envelope meta=%#v", env.EventMeta)
	}
}

func TestPerpSubscribeReferenceValidatesScopeAndFailsClosedOnMalformedStream(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	market := newMarketDataClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","nextFundingTime":1700007200000,"time":1700000000000}`), ws, testProvider(inst), nil)

	if err := market.SubscribeReference(context.Background(), model.InstrumentID{Venue: VenueName, Symbol: "ASTER-USDT", Kind: enums.KindSpot}); err == nil {
		t.Fatalf("SubscribeReference accepted wrong instrument scope")
	}
	if err := market.SubscribeReference(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeReference: %v", err)
	}
	<-market.Events()
	cases := map[string]*sdkperp.WsMarkPriceEvent{
		"nil":            nil,
		"wrong symbol":   {EventTime: 1700000001000, Symbol: "OTHERUSDT", MarkPrice: "1", IndexPrice: "1", FundingRate: "0"},
		"missing time":   {Symbol: inst.VenueSymbol, MarkPrice: "1", IndexPrice: "1", FundingRate: "0"},
		"negative mark":  {EventTime: 1700000001000, Symbol: inst.VenueSymbol, MarkPrice: "-1", IndexPrice: "1", FundingRate: "0"},
		"malformed rate": {EventTime: 1700000001000, Symbol: inst.VenueSymbol, MarkPrice: "1", IndexPrice: "1", FundingRate: "bad"},
	}
	for name, event := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ws.markHandler(event); err == nil {
				t.Fatalf("malformed mark price event accepted")
			}
			select {
			case env := <-market.Events():
				t.Fatalf("malformed stream emitted event: %#v", env)
			default:
			}
		})
	}
}

func TestPerpOpenInterestStrictlyValidatesRESTPayload(t *testing.T) {
	inst := mustPerpInstrument(t)
	now := time.UnixMilli(1700000012345)
	market := newMarketDataClient(perpClientResponse(t, `{"symbol":"ASTERUSDT","openInterest":"12345.678","time":1700000000000}`), nil, testProvider(inst), clock.NewSimulatedClock(now))

	oi, err := market.OpenInterest(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if oi.InstrumentID != inst.ID || oi.Unit != inst.Base || !oi.Timestamp.Equal(time.UnixMilli(1700000000000)) || !oi.ReceivedAt.Equal(now) {
		t.Fatalf("bad OI snapshot: %#v", oi)
	}
	if !oi.Fields.Has(model.OpenInterestHasQuantity) || !oi.Fields.Has(model.OpenInterestHasUnit) || oi.Fields.Has(model.OpenInterestHasNotional) {
		t.Fatalf("bad OI fields: %#v", oi.Fields)
	}
	assertDec(t, oi.OpenInterest, "12345.678")

	for name, body := range map[string]string{
		"null":      `null`,
		"negative":  `{"symbol":"ASTERUSDT","openInterest":"-1","time":1700000000000}`,
		"zero time": `{"symbol":"ASTERUSDT","openInterest":"1","time":0}`,
	} {
		t.Run(name, func(t *testing.T) {
			market := newMarketDataClient(perpClientResponse(t, body), nil, testProvider(inst), nil)
			if _, err := market.OpenInterest(context.Background(), inst.ID); err == nil {
				t.Fatalf("OpenInterest accepted malformed payload")
			}
		})
	}
}

func TestPerpFundingHistoryStrictlyNormalizesRowsAndBounds(t *testing.T) {
	inst := mustPerpInstrument(t)
	market := newMarketDataClient(perpClientResponse(t, `[{"symbol":"ASTERUSDT","fundingRate":"0.00010000","fundingTime":1700000000000},{"symbol":"ASTERUSDT","fundingRate":"-0.00020000","fundingTime":1700007200000}]`), nil, testProvider(inst), nil)
	rows, err := market.FundingHistory(context.Background(), inst.ID, model.FundingRateHistoryQuery{
		Start: time.UnixMilli(1700000000000),
		End:   time.UnixMilli(1700007200000),
		Limit: 2,
	})
	if err != nil {
		t.Fatalf("FundingHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len=%d", len(rows))
	}
	assertDec(t, rows[0].FundingRate, "0.00010000")
	assertDec(t, rows[1].FundingRate, "-0.00020000")
	if rows[0].InstrumentID != inst.ID || !rows[0].Timestamp.Equal(time.UnixMilli(1700000000000)) || !rows[0].Fields.Has(model.ReferenceHasFundingRate) {
		t.Fatalf("bad first funding row: %#v", rows[0])
	}

	for name, query := range map[string]model.FundingRateHistoryQuery{
		"negative limit": {Limit: -1},
		"inverted":       {Start: time.UnixMilli(2), End: time.UnixMilli(1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := market.FundingHistory(context.Background(), inst.ID, query); err == nil {
				t.Fatalf("FundingHistory accepted invalid query")
			}
		})
	}

	for name, body := range map[string]string{
		"wrong symbol":   `[{"symbol":"OTHERUSDT","fundingRate":"0.00010000","fundingTime":1700000000000}]`,
		"missing time":   `[{"symbol":"ASTERUSDT","fundingRate":"0.00010000"}]`,
		"malformed rate": `[{"symbol":"ASTERUSDT","fundingRate":"bad","fundingTime":1700000000000}]`,
		"outside bounds": `[{"symbol":"ASTERUSDT","fundingRate":"0.00010000","fundingTime":1699999999999}]`,
		"too many rows":  `[{"symbol":"ASTERUSDT","fundingRate":"0.00010000","fundingTime":1700000000000},{"symbol":"ASTERUSDT","fundingRate":"0.00010000","fundingTime":1700000000001}]`,
	} {
		t.Run(name, func(t *testing.T) {
			market := newMarketDataClient(perpClientResponse(t, body), nil, testProvider(inst), nil)
			_, err := market.FundingHistory(context.Background(), inst.ID, model.FundingRateHistoryQuery{Start: time.UnixMilli(1700000000000), End: time.UnixMilli(1700007200000), Limit: 1})
			if err == nil {
				t.Fatalf("FundingHistory accepted malformed row")
			}
		})
	}
}

func TestPerpReferenceCacheRuntimeProofAndOIDirectQueryOnly(t *testing.T) {
	inst := mustPerpInstrument(t)
	ws := &fakePerpMarketWS{}
	client := perpClientSequenceOrdered(t, map[string][]string{
		"/fapi/v3/premiumIndex": {
			`{"symbol":"ASTERUSDT","markPrice":"1.2500","indexPrice":"1.2480","lastFundingRate":"0.00010000","nextFundingTime":1700007200000,"time":1700000000000}`,
		},
		"/fapi/v3/openInterest": {
			`{"symbol":"ASTERUSDT","openInterest":"12345.678","time":1700000005000}`,
		},
	})
	clk := clock.NewSimulatedClock(time.UnixMilli(1700000010000))
	market := newMarketDataClient(client, ws, testProvider(inst), clk)
	cache := runtimecache.New()

	if err := market.SubscribeReference(context.Background(), inst.ID); err != nil {
		t.Fatalf("SubscribeReference: %v", err)
	}
	applyReferenceEventToCache(t, cache, <-market.Events())
	cached, ok := cache.DerivativeReference(inst.ID)
	if !ok {
		t.Fatalf("cache missing REST reference snapshot")
	}
	assertDec(t, cached.FundingRate, "0.00010000")
	assertDec(t, cached.MarkPrice, "1.2500")
	assertDec(t, cached.IndexPrice, "1.2480")

	oi, err := market.OpenInterest(context.Background(), inst.ID)
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	assertDec(t, oi.OpenInterest, "12345.678")
	if cachedAfterOI, ok := cache.DerivativeReference(inst.ID); !ok || !cachedAfterOI.MarkPrice.Equal(cached.MarkPrice) || cachedAfterOI.Fields.Has(model.ReferenceHasOraclePrice) {
		t.Fatalf("OI query changed derivative reference cache: %#v", cachedAfterOI)
	}

	clk.Advance(time.Second)
	if err := ws.markHandler(&sdkperp.WsMarkPriceEvent{EventTime: 1700000002000, Symbol: inst.VenueSymbol, MarkPrice: "1.2700", IndexPrice: "1.2680", FundingRate: "0.00030000", NextFundingTime: 1700007200000}); err != nil {
		t.Fatalf("new mark handler: %v", err)
	}
	applyReferenceEventToCache(t, cache, <-market.Events())
	cached, ok = cache.DerivativeReference(inst.ID)
	if !ok {
		t.Fatalf("cache missing stream reference snapshot")
	}
	assertDec(t, cached.FundingRate, "0.00030000")
	assertDec(t, cached.MarkPrice, "1.2700")
	assertDec(t, cached.IndexPrice, "1.2680")

	if err := ws.markHandler(&sdkperp.WsMarkPriceEvent{EventTime: 1700000001000, Symbol: inst.VenueSymbol, MarkPrice: "9.9900", IndexPrice: "9.9900", FundingRate: "0.99990000"}); err != nil {
		t.Fatalf("old mark handler should normalize but be rejected by cache freshness: %v", err)
	}
	applyReferenceEventToCache(t, cache, <-market.Events())
	cached, _ = cache.DerivativeReference(inst.ID)
	assertDec(t, cached.FundingRate, "0.00030000")
	assertDec(t, cached.MarkPrice, "1.2700")
	assertDec(t, cached.IndexPrice, "1.2680")

	if err := ws.markHandler(&sdkperp.WsMarkPriceEvent{EventTime: 1700000003000, Symbol: inst.VenueSymbol, MarkPrice: "bad", IndexPrice: "1.3000", FundingRate: "0.00040000"}); err == nil {
		t.Fatalf("malformed mark handler accepted payload")
	}
	select {
	case env := <-market.Events():
		t.Fatalf("malformed payload emitted event: %#v", env)
	default:
	}
}

func applyReferenceEventToCache(t *testing.T, cache *runtimecache.Cache, env contract.MarketEnvelope) {
	t.Helper()
	event, ok := env.Payload.(contract.ReferenceDataEvent)
	if !ok {
		t.Fatalf("event payload=%T, want ReferenceDataEvent", env.Payload)
	}
	cache.UpsertDerivativeReference(event.Snapshot)
}

func perpClientSequenceOrdered(t *testing.T, byPath map[string][]string) *sdkperp.Client {
	t.Helper()
	client, err := sdkperp.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		rows := byPath[r.URL.Path]
		if len(rows) == 0 {
			t.Fatalf("unexpected REST call: %s %s", r.Method, r.URL.String())
		}
		body := rows[0]
		byPath[r.URL.Path] = rows[1:]
		if r.URL.Path == "/fapi/v3/fundingRate" && !strings.Contains(r.URL.RawQuery, "symbol=ASTERUSDT") {
			t.Fatalf("funding history query missing symbol: %s", r.URL.RawQuery)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})})
	return client
}

func TestPerpFundingHistorySendsQueryBounds(t *testing.T) {
	inst := mustPerpInstrument(t)
	client, err := sdkperp.NewClient(mustProfile(t), testSecurity(t))
	if err != nil {
		t.Fatal(err)
	}
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		query := r.URL.Query()
		for key, want := range map[string]string{"symbol": "ASTERUSDT", "startTime": "1700000000000", "endTime": "1700007200000", "limit": "2"} {
			if got := query.Get(key); got != want {
				t.Fatalf("query %s=%q, want %q in %s", key, got, want, r.URL.RawQuery)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[{"symbol":"ASTERUSDT","fundingRate":"0.00010000","fundingTime":1700000000000}]`)), Header: make(http.Header), Request: r}, nil
	})})
	market := newMarketDataClient(client, nil, testProvider(inst), nil)
	if _, err := market.FundingHistory(context.Background(), inst.ID, model.FundingRateHistoryQuery{Start: time.UnixMilli(1700000000000), End: time.UnixMilli(1700007200000), Limit: 2}); err != nil {
		t.Fatalf("FundingHistory: %v", err)
	}
}
