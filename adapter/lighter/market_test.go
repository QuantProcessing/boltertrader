package lighter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterMarketReferenceSnapshotOpenInterestAndStatsPayload(t *testing.T) {
	marketID := 1
	id := model.InstrumentID{Venue: venueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}
	provider := newRegistry([]*model.Instrument{{
		ID:          id,
		Base:        "BTC",
		Quote:       "USDC",
		Settle:      "USDC",
		VenueSymbol: "BTC",
		AssetIndex:  &marketID,
	}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/funding-rates":
			_, _ = w.Write([]byte(`{"code":200,"message":"success","funding_rates":[{"market_id":1,"exchange":"lighter","symbol":"BTC","rate":0.0001}]}`))
		case "/api/v1/orderBookDetails":
			if got := r.URL.Query().Get("market_id"); got != "1" {
				t.Fatalf("market_id=%q, want 1", got)
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"success","order_book_details":[{"symbol":"BTC","market_id":1,"market_type":"perp","open_interest":55.5}]}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	rest := sdk.NewClient()
	rest.BaseURL = server.URL
	rest.HTTPClient = server.Client()
	market := newMarketDataClient(rest, nil, provider, clock.NewSimulatedClock(time.UnixMilli(1700000000000)))
	var _ contract.DerivativeReferenceDataClient = market
	var _ contract.OpenInterestClient = market
	if ref := market.Capabilities().ReferenceData; !ref.CurrentFunding || ref.CurrentMarkPrice || ref.CurrentIndexPrice || !ref.ReferencePolling || !ref.CurrentOpenInterest {
		t.Fatalf("REST-only reference capabilities unexpected: %+v", ref)
	}

	ref, err := market.ReferenceSnapshot(context.Background(), id)
	if err != nil {
		t.Fatalf("ReferenceSnapshot: %v", err)
	}
	if !ref.FundingRate.Equal(decimal.RequireFromString("0.0001")) || ref.FundingInterval != time.Hour || !ref.Fields.Has(model.ReferenceHasFundingRate) {
		t.Fatalf("unexpected reference snapshot: %+v", ref)
	}
	oi, err := market.OpenInterest(context.Background(), id)
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if !oi.OpenInterest.Equal(decimal.RequireFromString("55.5")) || oi.Unit != "contracts" {
		t.Fatalf("unexpected OI: %+v", oi)
	}

	streamRef, ok := referenceFromLighterMarketStats(id, []byte(`{"channel":"market_stats/1","type":"update","timestamp":1700000000100,"market_stats":{"market_id":1,"index_price":"64999.5","mark_price":"65000.5","open_interest":"123.45","current_funding_rate":"0.0002","funding_timestamp":1700000000100}}`), time.UnixMilli(1700000000200))
	if !ok || !streamRef.Fields.Has(model.ReferenceHasFundingRate) || !streamRef.Fields.Has(model.ReferenceHasMarkPrice) || !streamRef.Fields.Has(model.ReferenceHasIndexPrice) {
		t.Fatalf("unexpected stream reference=%+v ok=%v", streamRef, ok)
	}
	if !streamRef.FundingRate.Equal(decimal.RequireFromString("0.0002")) || !streamRef.MarkPrice.Equal(decimal.RequireFromString("65000.5")) || !streamRef.IndexPrice.Equal(decimal.RequireFromString("64999.5")) {
		t.Fatalf("unexpected stream values: %+v", streamRef)
	}
}

func TestAggregateLighterBookLevelsDropsMalformedAndNonPositivePrices(t *testing.T) {
	levels := aggregateLighterBookLevels([]sdk.Bid{
		{Price: "0", RemainingBaseAmount: "1"},
		{Price: "-1", RemainingBaseAmount: "1"},
		{Price: "bad", RemainingBaseAmount: "1"},
		{Price: "10", RemainingBaseAmount: "2"},
		{Price: "10", RemainingBaseAmount: "3"},
		{Price: "9", RemainingBaseAmount: "bad"},
	}, true, 5)

	if len(levels) != 1 || !levels[0].Price.Equal(decimal.NewFromInt(10)) || !levels[0].Quantity.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("levels=%+v, want one aggregated positive 10@5 level", levels)
	}
}
