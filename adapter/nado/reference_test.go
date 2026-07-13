package nado

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoPerpReferenceCapabilitiesAndUnsupportedSpot(t *testing.T) {
	provider := nadoTestProvider()
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC))
	perp := newMarketDataClient(nil, provider, clk, enums.KindPerp)
	perp.referenceBackend = nadoReferenceFixture()
	perp.streamBackend = &recordingMarketStreamBackend{}

	var _ contract.DerivativeReferenceDataClient = perp
	var _ contract.OpenInterestClient = perp
	var _ contract.OpenInterestHistoryClient = perp

	ref := perp.Capabilities().ReferenceData
	if !ref.CurrentFunding || !ref.CurrentMarkPrice || !ref.CurrentIndexPrice || !ref.CurrentOraclePrice || !ref.CurrentOpenInterest {
		t.Fatalf("perp reference capabilities missing: %+v", ref)
	}
	if !ref.ReferenceStream || !ref.ReferencePolling || ref.FundingHistory || ref.OpenInterestCached || ref.OpenInterestHistory {
		t.Fatalf("perp reference capability truth mismatch: %+v", ref)
	}

	spot := newMarketDataClient(nil, provider, clk, enums.KindSpot)
	spot.referenceBackend = nadoReferenceFixture()
	if got := spot.Capabilities().ReferenceData; got != (contract.ReferenceDataCapabilities{}) {
		t.Fatalf("spot reference capabilities must remain zero: %+v", got)
	}
	spotID := model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot}
	if _, err := spot.ReferenceSnapshot(context.Background(), spotID); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot ReferenceSnapshot err=%v, want ErrNotSupported", err)
	}
	if err := spot.SubscribeReference(context.Background(), spotID); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot SubscribeReference err=%v, want ErrNotSupported", err)
	}
	if _, err := spot.OpenInterest(context.Background(), spotID); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot OpenInterest err=%v, want ErrNotSupported", err)
	}
	if _, err := spot.OpenInterestHistory(context.Background(), spotID, model.OpenInterestHistoryQuery{}); !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot OpenInterestHistory err=%v, want ErrNotSupported", err)
	}
}

func TestNadoReferenceSnapshotCombinesRESTSourcesWithIndependentFreshness(t *testing.T) {
	received := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(received), enums.KindPerp)
	market.referenceBackend = nadoReferenceFixture()
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	snapshot, err := market.ReferenceSnapshot(context.Background(), id)
	if err != nil {
		t.Fatalf("ReferenceSnapshot: %v", err)
	}
	wantFields := model.ReferenceHasFundingRate.
		With(model.ReferenceHasFundingInterval).
		With(model.ReferenceHasMarkPrice).
		With(model.ReferenceHasIndexPrice).
		With(model.ReferenceHasOraclePrice)
	if snapshot.InstrumentID != id || snapshot.Fields != wantFields {
		t.Fatalf("snapshot identity/fields mismatch: %+v", snapshot)
	}
	if !snapshot.FundingRate.Equal(decimal.RequireFromString("0.000123456789012345")) {
		t.Fatalf("funding rate lost exact x18 precision: %s", snapshot.FundingRate)
	}
	if snapshot.FundingInterval != 24*time.Hour {
		t.Fatalf("funding interval=%s, want 24h native rate basis", snapshot.FundingInterval)
	}
	if !snapshot.MarkPrice.Equal(decimal.RequireFromString("2501.25")) || !snapshot.IndexPrice.Equal(decimal.RequireFromString("2499.75")) || !snapshot.OraclePrice.Equal(decimal.RequireFromString("2500")) {
		t.Fatalf("reference prices mismatch: %+v", snapshot)
	}
	fundingTS := time.Unix(1700000000, 0)
	priceTS := time.Unix(1700003600, 0)
	oracleTS := time.Unix(1700001800, 0)
	if fresh := snapshot.FieldTimes.For(model.ReferenceFieldFundingRate); !fresh.Venue.Equal(fundingTS) || !fresh.Received.Equal(received) {
		t.Fatalf("funding freshness=%+v", fresh)
	}
	if fresh := snapshot.FieldTimes.For(model.ReferenceFieldMarkPrice); !fresh.Venue.Equal(priceTS) || !fresh.Received.Equal(received) {
		t.Fatalf("mark freshness=%+v", fresh)
	}
	if fresh := snapshot.FieldTimes.For(model.ReferenceFieldOraclePrice); !fresh.Venue.Equal(oracleTS) || !fresh.Received.Equal(received) {
		t.Fatalf("oracle freshness=%+v", fresh)
	}
	if !snapshot.Timestamp.Equal(priceTS) || !snapshot.ReceivedAt.Equal(received) {
		t.Fatalf("snapshot timestamps mismatch: %+v", snapshot)
	}
}

func TestNadoOpenInterestUsesExactPerpStateQuantity(t *testing.T) {
	received := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(received), enums.KindPerp)
	market.referenceBackend = nadoReferenceFixture()
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	oi, err := market.OpenInterest(context.Background(), id)
	if err != nil {
		t.Fatalf("OpenInterest: %v", err)
	}
	if oi.InstrumentID != id || !oi.OpenInterest.Equal(decimal.RequireFromString("12.5")) || oi.Unit != "BTC" {
		t.Fatalf("open interest quantity mismatch: %+v", oi)
	}
	if !oi.OpenInterestNotional.IsZero() || !oi.Fields.Has(model.OpenInterestHasQuantity) || oi.Fields.Has(model.OpenInterestHasNotional) || !oi.Fields.Has(model.OpenInterestHasUnit) {
		t.Fatalf("open interest fields mismatch: %+v", oi)
	}
	if !oi.Timestamp.Equal(received) || !oi.ReceivedAt.Equal(received) {
		t.Fatalf("open interest freshness mismatch: %+v", oi)
	}
}

func TestNadoOpenInterestRejectsMissingDuplicateOrInvalidQuantity(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	for name, mutate := range map[string]func(*nadoRecordingReferenceBackend){
		"missing product": func(b *nadoRecordingReferenceBackend) {
			b.products.PerpProducts = nil
		},
		"duplicate product": func(b *nadoRecordingReferenceBackend) {
			b.products.PerpProducts = append(b.products.PerpProducts, b.products.PerpProducts[0])
		},
		"malformed quantity": func(b *nadoRecordingReferenceBackend) {
			b.products.PerpProducts[0].State.OpenInterest = "bad"
		},
		"negative quantity": func(b *nadoRecordingReferenceBackend) {
			b.products.PerpProducts[0].State.OpenInterest = "-1"
		},
	} {
		t.Run(name, func(t *testing.T) {
			market := newMarketDataClient(nil, nadoTestProvider(), clock.NewRealClock(), enums.KindPerp)
			backend := nadoReferenceFixture()
			mutate(backend)
			market.referenceBackend = backend
			if _, err := market.OpenInterest(context.Background(), id); err == nil {
				t.Fatal("OpenInterest accepted malformed source")
			}
		})
	}
}

func TestNadoFundingHistoryIsNotAdvertisedWithoutOfficialStableQuery(t *testing.T) {
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewRealClock(), enums.KindPerp)
	if market.Capabilities().ReferenceData.FundingHistory {
		t.Fatal("Nado funding history capability must remain false")
	}
	if _, ok := any(market).(contract.FundingHistoryClient); ok {
		t.Fatal("Nado market must not implement FundingHistoryClient without an official stable query")
	}
}

func TestNadoReferenceRejectsMismatchedOrMalformedSources(t *testing.T) {
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	for name, mutate := range map[string]func(*nadoRecordingReferenceBackend){
		"missing oracle": func(b *nadoRecordingReferenceBackend) {
			b.oraclePrices = nil
		},
		"funding product mismatch": func(b *nadoRecordingReferenceBackend) {
			b.funding.ProductID = 99
		},
		"funding malformed": func(b *nadoRecordingReferenceBackend) {
			b.funding.FundingRateX18 = "not-x18"
		},
		"price product mismatch": func(b *nadoRecordingReferenceBackend) {
			b.perpPrice.ProductID = 99
		},
		"price zero mark": func(b *nadoRecordingReferenceBackend) {
			b.perpPrice.MarkPriceX18 = "0"
		},
		"oracle product mismatch": func(b *nadoRecordingReferenceBackend) {
			b.oraclePrices[0].ProductID = 99
		},
	} {
		t.Run(name, func(t *testing.T) {
			market := newMarketDataClient(nil, nadoTestProvider(), clock.NewRealClock(), enums.KindPerp)
			backend := nadoReferenceFixture()
			mutate(backend)
			market.referenceBackend = backend
			if _, err := market.ReferenceSnapshot(context.Background(), id); err == nil {
				t.Fatalf("ReferenceSnapshot accepted malformed source")
			}
		})
	}
}

func TestNadoSubscribeReferenceEmitsInitialSnapshotAndPartialFundingUpdates(t *testing.T) {
	received := time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)
	market := newMarketDataClient(nil, nadoTestProvider(), clock.NewSimulatedClock(received), enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	market.streamBackend = backend
	market.referenceBackend = nadoReferenceFixture()
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	if err := market.SubscribeReference(context.Background(), id); err != nil {
		t.Fatalf("SubscribeReference: %v", err)
	}
	initial := (<-market.Events()).Payload.(contract.ReferenceDataEvent).Snapshot
	if !initial.Fields.Has(model.ReferenceHasMarkPrice) || !initial.Fields.Has(model.ReferenceHasOraclePrice) || !initial.Fields.Has(model.ReferenceHasFundingRate) {
		t.Fatalf("initial reference snapshot incomplete: %+v", initial)
	}
	if backend.fundingRateProductID == nil || *backend.fundingRateProductID != 2 || backend.connectCalls != 1 {
		t.Fatalf("funding stream not subscribed/connected: backend=%+v", backend)
	}

	update := time.Unix(1700007200, 0)
	backend.fundingRate(&sdk.FundingRate{Type: "funding_rate", ProductId: 2, FundingRateX18: "222000000000000", UpdateTime: fmt.Sprint(update.Unix()), Timestamp: "1700007200000000000"})
	partial := (<-market.Events()).Payload.(contract.ReferenceDataEvent).Snapshot
	if partial.Fields != model.ReferenceHasFundingRate || !partial.FundingRate.Equal(decimal.RequireFromString("0.000222")) {
		t.Fatalf("partial funding snapshot mismatch: %+v", partial)
	}
	if fresh := partial.FieldTimes.For(model.ReferenceFieldFundingRate); !fresh.Venue.Equal(update) || !fresh.Received.Equal(received) {
		t.Fatalf("partial funding freshness mismatch: %+v", fresh)
	}

	backend.fundingRate(&sdk.FundingRate{Type: "funding_rate", ProductId: 99, FundingRateX18: "333000000000000", UpdateTime: fmt.Sprint(update.Unix())})
	select {
	case ev := <-market.Events():
		t.Fatalf("mismatched funding event emitted: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestNadoRuntimeReferenceStreamMergesAndOpenInterestStaysQueryOnly(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC))
	market := newMarketDataClient(nil, nadoTestProvider(), clk, enums.KindPerp)
	backend := &recordingMarketStreamBackend{}
	market.streamBackend = backend
	market.referenceBackend = nadoReferenceFixture()
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}

	node := runtime.NewNode(runtime.Clients{Market: market}, clk, "nado-reference-runtime")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitNadoNodeRunning(t, node)
	if err := market.SubscribeReference(context.Background(), id); err != nil {
		t.Fatalf("SubscribeReference: %v", err)
	}
	waitForNadoReference(t, node, id, func(s model.DerivativeReferenceSnapshot) bool {
		return s.Fields.Has(model.ReferenceHasMarkPrice) && s.Fields.Has(model.ReferenceHasOraclePrice) && s.Fields.Has(model.ReferenceHasFundingRate)
	})

	backend.fundingRate(&sdk.FundingRate{Type: "funding_rate", ProductId: 2, FundingRateX18: "333000000000000", UpdateTime: "1700007200"})
	merged := waitForNadoReference(t, node, id, func(s model.DerivativeReferenceSnapshot) bool {
		return s.FundingRate.Equal(decimal.RequireFromString("0.000333")) && s.Fields.Has(model.ReferenceHasMarkPrice) && s.Fields.Has(model.ReferenceHasOraclePrice)
	})
	if !merged.MarkPrice.Equal(decimal.RequireFromString("2501.25")) || !merged.OraclePrice.Equal(decimal.RequireFromString("2500")) {
		t.Fatalf("partial funding update did not retain REST fields: %+v", merged)
	}
	if _, err := node.OpenInterest(context.Background(), id); err != nil {
		t.Fatalf("node OpenInterest: %v", err)
	}
	if _, ok := node.Cache.DerivativeReference(id); !ok {
		t.Fatal("reference cache missing")
	}
	// There is intentionally no runtime cache accessor for open interest; the
	// direct query above must not create any OI cache surface.

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
}

func waitForNadoReference(t *testing.T, node *runtime.TradingNode, id model.InstrumentID, match func(model.DerivativeReferenceSnapshot) bool) model.DerivativeReferenceSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state := node.State(); state.Node != lifecycle.NodeRunning {
			t.Fatalf("node stopped while waiting for reference: %+v", state)
		}
		if got, ok := node.Cache.DerivativeReference(id); ok && match(got) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	if got, ok := node.Cache.DerivativeReference(id); ok {
		t.Fatalf("reference cache did not match before deadline: %+v", got)
	}
	t.Fatal("reference cache never populated")
	return model.DerivativeReferenceSnapshot{}
}

type nadoRecordingReferenceBackend struct {
	products     sdk.AllProductsResponse
	perpPrice    sdk.PerpPriceResponse
	oraclePrices []sdk.OraclePriceResponse
	funding      sdk.FundingRateResponse
}

func nadoReferenceFixture() *nadoRecordingReferenceBackend {
	return &nadoRecordingReferenceBackend{
		products: sdk.AllProductsResponse{
			PerpProducts: []sdk.PerpProduct{{
				ProductID: 2,
				State:     sdk.PerpProductState{OpenInterest: "12500000000000000000"},
			}},
		},
		perpPrice: sdk.PerpPriceResponse{
			ProductID: 2, MarkPriceX18: "2501250000000000000000", IndexPriceX18: "2499750000000000000000", UpdateTime: "1700003600",
		},
		oraclePrices: []sdk.OraclePriceResponse{{
			ProductID: 2, OraclePriceX18: "2500000000000000000000", UpdateTime: "1700001800",
		}},
		funding: sdk.FundingRateResponse{ProductID: 2, FundingRateX18: "123456789012345", UpdateTime: "1700000000"},
	}
}

func (b *nadoRecordingReferenceBackend) GetAllProducts(context.Context) (*sdk.AllProductsResponse, error) {
	out := b.products
	return &out, nil
}

func (b *nadoRecordingReferenceBackend) GetPerpPrice(context.Context, int64) (*sdk.PerpPriceResponse, error) {
	out := b.perpPrice
	return &out, nil
}

func (b *nadoRecordingReferenceBackend) GetOraclePrices(context.Context, []int64) ([]sdk.OraclePriceResponse, error) {
	return append([]sdk.OraclePriceResponse(nil), b.oraclePrices...), nil
}

func (b *nadoRecordingReferenceBackend) GetFundingRate(context.Context, int64) (*sdk.FundingRateResponse, error) {
	out := b.funding
	return &out, nil
}
