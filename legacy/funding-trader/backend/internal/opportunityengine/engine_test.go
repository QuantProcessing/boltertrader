package opportunityengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/domain/risk"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/shopspring/decimal"
)

func TestRefreshPairsDemoAndTestnetInTestModeAndPreservesNativeIdentity(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	source.instruments[0].VenueEnvironment = "demo"
	source.instruments[1].VenueEnvironment = "testnet"
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-mixed", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("mixed Test opportunities=%+v", store.snapshot.Opportunities)
	}
	var payload map[string]any
	if err := json.Unmarshal(store.snapshot.Opportunities[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	calculation := payload["calculationInputs"].(map[string]any)
	longLeg := calculation["longLeg"].(map[string]any)
	shortLeg := calculation["shortLeg"].(map[string]any)
	if longLeg["venueEnvironment"] != "demo" || shortLeg["venueEnvironment"] != "testnet" {
		t.Fatalf("native environments were not preserved: %s", store.snapshot.Opportunities[0].Payload)
	}
}

func TestRefreshBuildsPersistedAccountScopedOpportunityAlertsAndFundingHistory(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	store := &recordingProjectionStore{}
	publisher := &recordingPublisher{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: source,
		Store: store, Publisher: publisher, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("opportunities=%+v", store.snapshot.Opportunities)
	}
	if len(store.snapshot.Alerts) != 1 {
		t.Fatalf("alerts=%+v", store.snapshot.Alerts)
	}
	if len(store.snapshot.Funding) != 10 {
		t.Fatalf("funding observations=%d", len(store.snapshot.Funding))
	}
	var opportunity map[string]any
	if err := json.Unmarshal(store.snapshot.Opportunities[0].Payload, &opportunity); err != nil {
		t.Fatal(err)
	}
	for key, expected := range map[string]string{
		"symbol":            "BTC-USDT",
		"longAccountId":     "account-low",
		"longInstrumentId":  "instrument-low",
		"shortAccountId":    "account-high",
		"shortInstrumentId": "instrument-high",
		"formulaVersion":    "funding-arb-v1",
	} {
		if opportunity[key] != expected {
			t.Fatalf("%s=%v want=%s payload=%s", key, opportunity[key], expected, store.snapshot.Opportunities[0].Payload)
		}
	}
	if opportunity["opportunityId"] == "" || opportunity["status"] != "executable" {
		t.Fatalf("payload=%s", store.snapshot.Opportunities[0].Payload)
	}
	calculation, ok := opportunity["calculationInputs"].(map[string]any)
	if !ok {
		t.Fatalf("calculation inputs missing: %s", store.snapshot.Opportunities[0].Payload)
	}
	for _, key := range []string{
		"longTakerFeeRate",
		"shortTakerFeeRate",
		"liquidationBufferPct",
		"longLeg",
		"shortLeg",
	} {
		if _, exists := calculation[key]; !exists {
			t.Fatalf("calculation input %q missing: %s", key, store.snapshot.Opportunities[0].Payload)
		}
	}
	longLeg, ok := calculation["longLeg"].(map[string]any)
	if !ok {
		t.Fatalf("long leg inputs missing: %s", store.snapshot.Opportunities[0].Payload)
	}
	for _, key := range []string{
		"accountId",
		"instrumentId",
		"marketObservedAt",
		"markPrice",
		"bbo",
		"depth",
		"currentFunding",
		"fundingHistory",
	} {
		if _, exists := longLeg[key]; !exists {
			t.Fatalf("long leg input %q missing: %s", key, store.snapshot.Opportunities[0].Payload)
		}
	}
	if _, leaked := opportunity["credentials"]; leaked {
		t.Fatalf("payload leaked credentials: %s", store.snapshot.Opportunities[0].Payload)
	}
	if len(publisher.broadcast) != len(store.events) {
		t.Fatalf("broadcast=%d stored=%d", len(publisher.broadcast), len(store.events))
	}
}

func TestRefreshRequestsThirtyDayHistoryInVenueSafePages(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: source,
		Store: &recordingProjectionStore{}, Publisher: &recordingPublisher{},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(source.historyRequests) != len(source.instruments) {
		t.Fatalf("history requests=%d instruments=%d", len(source.historyRequests), len(source.instruments))
	}
	for _, request := range source.historyRequests {
		if request.Limit != 100 {
			t.Fatalf("history page limit=%d, want venue-safe 100", request.Limit)
		}
		if !request.Start.Equal(now.Add(-30*24*time.Hour)) || !request.End.Equal(now) {
			t.Fatalf("history window=%s..%s, want 30 days ending at %s",
				request.Start, request.End, now)
		}
	}
}

func TestRefreshNeverFabricatesOpportunityWithoutACompatibleSecondVenue(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	source.instruments = source.instruments[:1]
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 0 {
		t.Fatalf("fabricated opportunities=%+v", store.snapshot.Opportunities)
	}
}

func TestRefreshHonorsConfiguredNormalizedMarketUniverse(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: completeMarketSource(now),
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
		MarketSymbols: []string{"ETH-USDT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 0 || len(store.snapshot.Alerts) != 0 ||
		len(store.snapshot.Funding) != 0 {
		t.Fatalf("out-of-universe data was loaded: %+v", store.snapshot)
	}
}

func TestRefreshSkipsFirstLoadInstrumentFailureAndKeepsCompleteCompatibleRoutes(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	unsupported := marketdata.Instrument{
		AccountID: "account-unsupported", InstrumentID: "instrument-unsupported",
		Venue: "aster", VenueEnvironment: "testnet", Product: "perp",
		Settlement: "usdt", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	source.instruments = append(source.instruments, unsupported)
	source.historyErr = map[string]error{unsupported.Key(): errors.New("unsupported_capability")}
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-partial", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("complete compatible route was dropped after unrelated load failure: %+v", store.snapshot.Opportunities)
	}
	if len(store.snapshot.Funding) != 10 {
		t.Fatalf("incomplete instrument leaked into funding observations: %d", len(store.snapshot.Funding))
	}
	status := engine.Status()
	if status.Ready || status.LastError != "market-data-partial" {
		t.Fatalf("status=%+v, want degraded market-data-partial", status)
	}
}

func TestRefreshSkipsFundingFactsWithoutPeriodButKeepsOpportunitySnapshot(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	key := source.instruments[0].Key()
	current := source.funding[key]
	current.ObservedAt = now.Add(-time.Second)
	current.FundingTime = time.Time{}
	current.NextFundingTime = time.Time{}
	source.funding[key] = current
	source.history[key] = marketdata.FundingRatePage{Rates: []marketdata.FundingRate{
		{
			Instrument:  source.instruments[0],
			Rate:        decimal.RequireFromString("0.00010"),
			ObservedAt:  now.Add(-8 * time.Hour),
			FundingTime: time.Time{},
		},
		{
			Instrument:      source.instruments[0],
			Rate:            decimal.RequireFromString("0.00010"),
			ObservedAt:      now.Add(-16 * time.Hour),
			FundingTime:     time.Time{},
			NextFundingTime: now.Add(-8 * time.Hour),
		},
	}}
	store := &recordingProjectionStore{validateFunding: true}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-missing-period", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("opportunity disappeared when funding facts were filtered: %+v", store.snapshot.Opportunities)
	}
	if got := len(store.snapshot.Funding); got != 6 {
		t.Fatalf("funding observations=%d want 6 valid facts only", got)
	}
	for _, observation := range store.snapshot.Funding {
		if observation.AccountID == source.instruments[0].AccountID &&
			observation.InstrumentID == source.instruments[0].InstrumentID &&
			observation.FundingTime.Equal(now) {
			t.Fatalf("current rate without funding period was persisted: %+v", observation)
		}
	}
}

func TestPairedHistoryMatchesSettlementTimesAtSecondPrecision(t *testing.T) {
	settlement := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	longRates := []marketdata.FundingRate{{FundingTime: settlement.Add(time.Millisecond)}}
	shortRates := []marketdata.FundingRate{{FundingTime: settlement}}

	pairedLong, pairedShort := pairedHistory(longRates, shortRates)

	if len(pairedLong) != 1 || len(pairedShort) != 1 {
		t.Fatalf("paired history lengths = %d/%d, want 1/1", len(pairedLong), len(pairedShort))
	}
}

func TestPairedHistoryRejectsDifferentSettlementSeconds(t *testing.T) {
	settlement := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	longRates := []marketdata.FundingRate{{FundingTime: settlement.Add(time.Second)}}
	shortRates := []marketdata.FundingRate{{FundingTime: settlement}}

	pairedLong, pairedShort := pairedHistory(longRates, shortRates)

	if len(pairedLong) != 0 || len(pairedShort) != 0 {
		t.Fatalf("paired history lengths = %d/%d, want 0/0", len(pairedLong), len(pairedShort))
	}
}

func TestRefreshFailsWhenEveryEligibleInstrumentIsIncompleteOnFirstLoad(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	source.historyErr = map[string]error{}
	for _, instrument := range source.instruments {
		source.historyErr[instrument.Key()] = errors.New("unsupported_capability")
	}
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-all-fail", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded with no complete market-data observations")
	}
	if store.applies != 0 {
		t.Fatalf("incomplete market data was persisted applies=%d", store.applies)
	}
}

func TestRefreshKeepsLastCompleteRouteVisibleAndBlockedWhenOneVenueTurnsStale(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(source.bbo, source.instruments[1].Key())
	now = now.Add(10 * time.Second)

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("stale route disappeared: %+v", store.snapshot.Opportunities)
	}
	var payload struct {
		Status         string  `json:"status"`
		DataAgeSeconds float64 `json:"dataAgeSeconds"`
		Blockers       []struct {
			Code string `json:"code"`
		} `json:"blockers"`
	}
	if err := json.Unmarshal(store.snapshot.Opportunities[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "blocked" || payload.DataAgeSeconds < 10 {
		t.Fatalf("stale payload=%s", store.snapshot.Opportunities[0].Payload)
	}
	foundStale := false
	for _, blocker := range payload.Blockers {
		foundStale = foundStale || blocker.Code == "stale_data"
	}
	if !foundStale {
		t.Fatalf("stale blocker missing: %s", store.snapshot.Opportunities[0].Payload)
	}
	foundCachedAlert := false
	for _, alert := range store.snapshot.Alerts {
		if alert.AccountID != source.instruments[1].AccountID {
			continue
		}
		foundCachedAlert = true
		var alertPayload struct {
			ObservedAt string `json:"observedAt"`
		}
		if err := json.Unmarshal(alert.Payload, &alertPayload); err != nil {
			t.Fatal(err)
		}
		expected := now.Add(-11 * time.Second).Format(time.RFC3339Nano)
		if alertPayload.ObservedAt != expected {
			t.Fatalf("cached alert observedAt=%s want=%s payload=%s", alertPayload.ObservedAt, expected, alert.Payload)
		}
	}
	if !foundCachedAlert {
		t.Fatal("cached venue alert disappeared")
	}
}

func TestRefreshEvaluatesAfterAllLoadsAndDoesNotAgeFreshMarketDataBehindSlowHistory(t *testing.T) {
	start := time.Date(2026, 7, 30, 3, 33, 19, 0, time.UTC)
	clock := &advancingClock{current: start, step: 100 * time.Millisecond}
	source := advancingMarketSource(clock, start)
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-advancing", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(store.snapshot.Opportunities) == 0 {
		t.Fatalf("opportunities=%+v", store.snapshot.Opportunities)
	}
	var payload struct {
		Status         string  `json:"status"`
		DataAgeSeconds float64 `json:"dataAgeSeconds"`
		Blockers       []struct {
			Code string `json:"code"`
		} `json:"blockers"`
	}
	for _, projection := range store.snapshot.Opportunities {
		if err := json.Unmarshal(projection.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Status != "executable" {
			t.Fatalf("payload status=%s blockers=%+v raw=%s", payload.Status, payload.Blockers, projection.Payload)
		}
		for _, blocker := range payload.Blockers {
			if blocker.Code == "future_data" || blocker.Code == "stale_data" {
				t.Fatalf("fresh refresh produced %s blocker: %s", blocker.Code, projection.Payload)
			}
		}
	}
	if !store.snapshot.ObservedAt.After(start) {
		t.Fatalf("snapshot observedAt=%s was not captured after loading started at %s", store.snapshot.ObservedAt, start)
	}
}

func TestRefreshLoadsEligibleInstrumentsConcurrentlyWithDeterministicProjection(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 19, 0, time.UTC)
	source := concurrentHistoryMarketSource(now)
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-concurrent", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := engine.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}

	if source.maxActiveHistory < 2 {
		t.Fatalf("history loads did not overlap; max active=%d", source.maxActiveHistory)
	}
	if source.maxActiveHistory > 4 {
		t.Fatalf("history concurrency exceeded bound; max active=%d", source.maxActiveHistory)
	}
	if len(store.snapshot.Opportunities) != 1 {
		t.Fatalf("opportunities=%+v", store.snapshot.Opportunities)
	}
	if got := store.snapshot.Opportunities[0].ID; got != "opp_Ar2wLHnKfQ81E8C628ZWSfb5" {
		t.Fatalf("opportunity id=%s", got)
	}
}

func TestFreshOpportunityLoadsOnlyExactRouteAndDoesNotPersist(t *testing.T) {
	now := time.Date(2026, 7, 30, 4, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	unrelated := marketdata.Instrument{
		AccountID: "account-unrelated", InstrumentID: "instrument-unrelated",
		Venue: "gate", VenueEnvironment: "demo", Product: "perp",
		Settlement: "usdt", Symbol: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
	}
	source.instruments = append(source.instruments, unrelated)
	store := &recordingProjectionStore{}
	publisher := &recordingPublisher{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-fresh", Source: source,
		Store: store, Publisher: publisher, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := engine.FreshOpportunity(
		context.Background(),
		"account-low", "instrument-low",
		"account-high", "instrument-high",
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.applies != 0 || len(publisher.broadcast) != 0 || len(publisher.published) != 0 {
		t.Fatalf("fresh route persisted or published: applies=%d broadcast=%d published=%d",
			store.applies, len(publisher.broadcast), len(publisher.published))
	}
	if len(source.historyRequests) != 2 {
		t.Fatalf("history requests=%d want exact two route legs", len(source.historyRequests))
	}
	loaded := map[string]bool{}
	for _, request := range source.historyRequests {
		loaded[request.Instrument.Key()] = true
	}
	if !loaded[source.instruments[0].Key()] || !loaded[source.instruments[1].Key()] || loaded[unrelated.Key()] {
		t.Fatalf("loaded route keys=%v unrelated=%s", loaded, unrelated.Key())
	}
	var opportunity struct {
		OpportunityID     string `json:"opportunityId"`
		LongAccountID     string `json:"longAccountId"`
		ShortAccountID    string `json:"shortAccountId"`
		LongInstrumentID  string `json:"longInstrumentId"`
		ShortInstrumentID string `json:"shortInstrumentId"`
		CalculationInputs struct {
			LongLeg struct {
				AccountID    string `json:"accountId"`
				InstrumentID string `json:"instrumentId"`
			} `json:"longLeg"`
			ShortLeg struct {
				AccountID    string `json:"accountId"`
				InstrumentID string `json:"instrumentId"`
			} `json:"shortLeg"`
		} `json:"calculationInputs"`
	}
	if err := json.Unmarshal(payload, &opportunity); err != nil {
		t.Fatal(err)
	}
	if opportunity.OpportunityID == "" ||
		opportunity.LongAccountID != "account-low" ||
		opportunity.ShortAccountID != "account-high" ||
		opportunity.LongInstrumentID != "instrument-low" ||
		opportunity.ShortInstrumentID != "instrument-high" {
		t.Fatalf("fresh route identity mismatch: %s", payload)
	}
}

func TestFreshOpportunityRefreshesFastFieldsAgedBySlowMarketEndpoint(t *testing.T) {
	start := time.Date(2026, 7, 30, 4, 15, 0, 0, time.UTC)
	clock := &advancingClock{current: start}
	source := slowMarkCoherenceSource(clock, start)
	store := &recordingProjectionStore{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-fresh-coherent", Source: source,
		Store: store, Publisher: &recordingPublisher{}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	payload, err := engine.FreshOpportunity(
		context.Background(),
		"account-low", "instrument-low",
		"account-high", "instrument-high",
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.applies != 0 {
		t.Fatalf("fresh route persisted applies=%d", store.applies)
	}
	var opportunity struct {
		Status         string  `json:"status"`
		DataAgeSeconds float64 `json:"dataAgeSeconds"`
		Blockers       []struct {
			Code string `json:"code"`
		} `json:"blockers"`
	}
	if err := json.Unmarshal(payload, &opportunity); err != nil {
		t.Fatal(err)
	}
	if opportunity.Status != "executable" || opportunity.DataAgeSeconds > risk.MaxDataAge.Seconds() {
		t.Fatalf("fresh coherent payload status=%s age=%f blockers=%+v raw=%s",
			opportunity.Status, opportunity.DataAgeSeconds, opportunity.Blockers, payload)
	}
	for _, blocker := range opportunity.Blockers {
		if blocker.Code == "stale_data" || blocker.Code == "future_data" {
			t.Fatalf("coherent refresh still produced %s blocker: %s", blocker.Code, payload)
		}
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	for _, instrument := range source.instruments {
		key := instrument.Key()
		if source.fundingCalls[key] < 2 ||
			source.bookCalls[key] < 2 ||
			source.bboCalls[key] < 2 {
			t.Fatalf("stale fast fields were not refreshed for %s: funding=%d book=%d bbo=%d",
				key, source.fundingCalls[key], source.bookCalls[key], source.bboCalls[key])
		}
	}
}

func TestFreshOpportunityFailsClosedForMissingDuplicateAndIncompatibleRoute(t *testing.T) {
	now := time.Date(2026, 7, 30, 4, 30, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		mutate func(*fakeMarketSource)
	}{
		{
			name: "missing",
			mutate: func(source *fakeMarketSource) {
				source.instruments = source.instruments[:1]
			},
		},
		{
			name: "duplicate",
			mutate: func(source *fakeMarketSource) {
				source.instruments = append(source.instruments, source.instruments[0])
			},
		},
		{
			name: "incompatible",
			mutate: func(source *fakeMarketSource) {
				source.instruments[1].Venue = source.instruments[0].Venue
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := completeMarketSource(now)
			test.mutate(source)
			store := &recordingProjectionStore{}
			engine, err := New(Config{
				Mode: config.Test, BootID: "boot-fresh-fail", Source: source,
				Store: store, Publisher: &recordingPublisher{}, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}

			_, err = engine.FreshOpportunity(
				context.Background(),
				"account-low", "instrument-low",
				"account-high", "instrument-high",
			)
			if err == nil {
				t.Fatal("FreshOpportunity succeeded for invalid route")
			}
			if store.applies != 0 {
				t.Fatalf("invalid fresh route persisted applies=%d", store.applies)
			}
		})
	}
}

func TestUpstreamResyncRefreshesThenPublishesBrowserResnapshotRequired(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	store := &recordingProjectionStore{}
	publisher := &recordingPublisher{}
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: completeMarketSource(now),
		Store: store, Publisher: publisher, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = engine.HandleStreamError(context.Background(),
		errors.Join(errors.New("upstream detail"), marketdata.ErrResnapshotRequired))
	if err != nil {
		t.Fatal(err)
	}
	if store.applies != 1 || len(publisher.published) != 1 {
		t.Fatalf("applies=%d published=%+v", store.applies, publisher.published)
	}
	event := publisher.published[0]
	if event.Kind != uistream.EventResnapshotRequired ||
		string(event.Data) != `{"reason":"upstream-resync","scope":["opportunities","alerts"]}` ||
		string(event.Data) == `resync_required` {
		t.Fatalf("event=%+v", event)
	}
}

func TestRunConsumesFundingStreamAndMapsResyncToBrowserResnapshot(t *testing.T) {
	now := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	source := completeMarketSource(now)
	source.subscription = &scriptedFundingSubscription{firstErr: marketdata.ErrResnapshotRequired}
	store := &recordingProjectionStore{}
	publisher := newRecordingPublisher()
	engine, err := New(Config{
		Mode: config.Test, BootID: "boot-a", Source: source,
		Store: store, Publisher: publisher, Now: func() time.Time { return now },
		RefreshInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- engine.Run(ctx) }()
	select {
	case event := <-publisher.notify:
		if event.Kind != uistream.EventResnapshotRequired {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mapped stream resnapshot")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}

type fakeMarketSource struct {
	mu              sync.Mutex
	instruments     []marketdata.Instrument
	funding         map[string]marketdata.FundingRate
	fundingErr      map[string]error
	history         map[string]marketdata.FundingRatePage
	historyErr      map[string]error
	historyRequests []marketdata.FundingRateHistoryRequest
	books           map[string]marketdata.OrderBook
	bookErr         map[string]error
	bbo             map[string]marketdata.BBO
	bboErr          map[string]error
	marks           map[string]marketdata.MarkPrice
	markErr         map[string]error
	subscription    marketdata.Subscription[marketdata.FundingRate]
}

func (source *fakeMarketSource) Instruments(context.Context) ([]marketdata.Instrument, error) {
	return append([]marketdata.Instrument(nil), source.instruments...), nil
}

func (source *fakeMarketSource) FundingRate(_ context.Context, instrument marketdata.Instrument) (marketdata.FundingRate, error) {
	if err := source.fundingErr[instrument.Key()]; err != nil {
		return marketdata.FundingRate{}, err
	}
	return source.funding[instrument.Key()], nil
}

func (source *fakeMarketSource) FundingRateHistory(_ context.Context, request marketdata.FundingRateHistoryRequest) (marketdata.FundingRatePage, error) {
	source.mu.Lock()
	source.historyRequests = append(source.historyRequests, request)
	source.mu.Unlock()
	if err := source.historyErr[request.Instrument.Key()]; err != nil {
		return marketdata.FundingRatePage{}, err
	}
	return source.history[request.Instrument.Key()], nil
}

func (source *fakeMarketSource) OrderBook(_ context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	if err := source.bookErr[request.Instrument.Key()]; err != nil {
		return marketdata.OrderBook{}, err
	}
	return source.books[request.Instrument.Key()], nil
}

func (source *fakeMarketSource) BBO(_ context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	if err := source.bboErr[instrument.Key()]; err != nil {
		return marketdata.BBO{}, err
	}
	return source.bbo[instrument.Key()], nil
}

func (source *fakeMarketSource) MarkPrice(_ context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	if err := source.markErr[instrument.Key()]; err != nil {
		return marketdata.MarkPrice{}, err
	}
	return source.marks[instrument.Key()], nil
}

func (source *fakeMarketSource) WatchFundingRate(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.FundingRate], error) {
	if source.subscription == nil {
		return &scriptedFundingSubscription{}, nil
	}
	return source.subscription, nil
}

type recordingProjectionStore struct {
	applies         int
	validateFunding bool
	snapshot        readmodel.MarketSnapshot
	events          []readmodel.Event
}

func (store *recordingProjectionStore) ApplyMarketSnapshot(_ context.Context, bootID string, snapshot readmodel.MarketSnapshot) ([]readmodel.Event, error) {
	if store.validateFunding {
		for _, observation := range snapshot.Funding {
			if observation.AccountID == "" ||
				observation.InstrumentID == "" ||
				observation.VenueEnvironment == "" ||
				observation.FundingTime.IsZero() ||
				observation.ObservedAt.IsZero() {
				return nil, errors.New("invalid funding observation")
			}
		}
	}
	store.applies++
	store.snapshot = snapshot
	store.events = nil
	sequence := int64(1)
	for _, opportunity := range snapshot.Opportunities {
		store.events = append(store.events, readmodel.Event{
			ID: sequence, BootID: bootID, Mode: snapshot.Mode,
			Kind: readmodel.OpportunityUpsert, Scope: []string{"opportunities"},
			EntityID: opportunity.ID, Payload: opportunity.Payload, OccurredAt: snapshot.ObservedAt,
		})
		sequence++
	}
	for _, alert := range snapshot.Alerts {
		store.events = append(store.events, readmodel.Event{
			ID: sequence, BootID: bootID, Mode: snapshot.Mode,
			Kind: readmodel.AlertUpsert, Scope: []string{"alerts"},
			EntityID: alert.ID, Payload: alert.Payload, OccurredAt: snapshot.ObservedAt,
		})
		sequence++
	}
	return store.events, nil
}

type recordingPublisher struct {
	mu        sync.Mutex
	broadcast []uistream.StoredEvent
	published []uistream.EventInput
	notify    chan uistream.EventInput
}

func (publisher *recordingPublisher) BroadcastStored(event uistream.StoredEvent) error {
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.broadcast = append(publisher.broadcast, event)
	return nil
}

func (publisher *recordingPublisher) Publish(_ context.Context, input uistream.EventInput) (uistream.StoredEvent, error) {
	publisher.mu.Lock()
	publisher.published = append(publisher.published, input)
	publisher.mu.Unlock()
	if publisher.notify != nil {
		select {
		case publisher.notify <- input:
		default:
		}
	}
	return uistream.StoredEvent{}, nil
}

func newRecordingPublisher() *recordingPublisher {
	return &recordingPublisher{notify: make(chan uistream.EventInput, 8)}
}

type scriptedFundingSubscription struct {
	mu       sync.Mutex
	firstErr error
	sent     bool
}

func (subscription *scriptedFundingSubscription) Next(ctx context.Context) (marketdata.StreamEvent[marketdata.FundingRate], error) {
	subscription.mu.Lock()
	if !subscription.sent && subscription.firstErr != nil {
		subscription.sent = true
		err := subscription.firstErr
		subscription.mu.Unlock()
		return marketdata.StreamEvent[marketdata.FundingRate]{}, err
	}
	subscription.mu.Unlock()
	<-ctx.Done()
	return marketdata.StreamEvent[marketdata.FundingRate]{}, ctx.Err()
}

func (*scriptedFundingSubscription) Close() error { return nil }

func completeMarketSource(now time.Time) *fakeMarketSource {
	low := marketdata.Instrument{
		AccountID: "account-low", InstrumentID: "instrument-low",
		Venue: "binance", VenueEnvironment: "demo", Product: "perp",
		Settlement: "usdt", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	high := marketdata.Instrument{
		AccountID: "account-high", InstrumentID: "instrument-high",
		Venue: "bybit", VenueEnvironment: "demo", Product: "perp",
		Settlement: "usdt", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	source := &fakeMarketSource{
		instruments: []marketdata.Instrument{low, high},
		funding:     map[string]marketdata.FundingRate{},
		history:     map[string]marketdata.FundingRatePage{},
		books:       map[string]marketdata.OrderBook{},
		bbo:         map[string]marketdata.BBO{},
		marks:       map[string]marketdata.MarkPrice{},
	}
	for index, instrument := range source.instruments {
		rate := decimal.RequireFromString("0.00010")
		if index == 1 {
			rate = decimal.RequireFromString("0.00050")
		}
		current := marketdata.FundingRate{
			Instrument: instrument, Rate: rate, ObservedAt: now.Add(-time.Second),
			FundingTime: now, NextFundingTime: now.Add(8 * time.Hour),
		}
		source.funding[instrument.Key()] = current
		history := make([]marketdata.FundingRate, 0, 4)
		for point := 4; point >= 1; point-- {
			history = append(history, marketdata.FundingRate{
				Instrument: instrument, Rate: rate,
				FundingTime: now.Add(-time.Duration(point) * 8 * time.Hour),
				ObservedAt:  now.Add(-time.Duration(point) * 8 * time.Hour),
			})
		}
		source.history[instrument.Key()] = marketdata.FundingRatePage{Rates: history}
		source.books[instrument.Key()] = marketdata.OrderBook{
			Instrument: instrument,
			Bids:       []marketdata.Level{{Price: decimal.RequireFromString("9999"), Quantity: decimal.RequireFromString("10")}},
			Asks:       []marketdata.Level{{Price: decimal.RequireFromString("10001"), Quantity: decimal.RequireFromString("10")}},
			ObservedAt: now.Add(-time.Second),
		}
		source.bbo[instrument.Key()] = marketdata.BBO{
			Instrument: instrument,
			Bid:        marketdata.Level{Price: decimal.RequireFromString("9999"), Quantity: decimal.RequireFromString("10")},
			Ask:        marketdata.Level{Price: decimal.RequireFromString("10001"), Quantity: decimal.RequireFromString("10")},
			ObservedAt: now.Add(-time.Second),
		}
		mark := decimal.RequireFromString("10000")
		if index == 1 {
			mark = decimal.RequireFromString("10010")
		}
		source.marks[instrument.Key()] = marketdata.MarkPrice{
			Instrument: instrument, Price: mark, ObservedAt: now.Add(-time.Second),
		}
	}
	return source
}

type advancingClock struct {
	mu      sync.Mutex
	current time.Time
	step    time.Duration
}

func (clock *advancingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.current
}

func (clock *advancingClock) Advance() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.current = clock.current.Add(clock.step)
	return clock.current
}

func (clock *advancingClock) AdvanceBy(delta time.Duration) time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.current = clock.current.Add(delta)
	return clock.current
}

func advancingMarketSource(clock *advancingClock, start time.Time) MarketSource {
	source := completeMarketSource(start)
	for index, symbol := range []string{"ETHUSDT", "SOLUSDT", "XRPUSDT", "ADAUSDT"} {
		instrument := source.instruments[index%2]
		instrument.AccountID = fmt.Sprintf("account-extra-%d", index)
		instrument.InstrumentID = fmt.Sprintf("instrument-extra-%d", index)
		instrument.Symbol = symbol
		instrument.BaseAsset = strings.TrimSuffix(symbol, "USDT")
		source.instruments = append(source.instruments, instrument)
	}
	for index, instrument := range source.instruments {
		rate := decimal.RequireFromString("0.00010")
		if index == 1 {
			rate = decimal.RequireFromString("0.00050")
		}
		key := instrument.Key()
		current := source.funding[key]
		current.Instrument = instrument
		current.Rate = rate
		current.FundingTime = time.Time{}
		current.NextFundingTime = time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
		source.funding[key] = current
		history := []marketdata.FundingRate{
			{Instrument: instrument, Rate: rate, FundingTime: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC), ObservedAt: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)},
			{Instrument: instrument, Rate: rate, FundingTime: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC), ObservedAt: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)},
			{Instrument: instrument, Rate: rate, FundingTime: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC), ObservedAt: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)},
		}
		source.history[key] = marketdata.FundingRatePage{Rates: history}
		source.books[key] = marketdata.OrderBook{
			Instrument: instrument,
			Bids:       []marketdata.Level{{Price: decimal.RequireFromString("9999"), Quantity: decimal.RequireFromString("10")}},
			Asks:       []marketdata.Level{{Price: decimal.RequireFromString("10001"), Quantity: decimal.RequireFromString("10")}},
			ObservedAt: start,
		}
		source.bbo[key] = marketdata.BBO{
			Instrument: instrument,
			Bid:        marketdata.Level{Price: decimal.RequireFromString("9999"), Quantity: decimal.RequireFromString("10")},
			Ask:        marketdata.Level{Price: decimal.RequireFromString("10001"), Quantity: decimal.RequireFromString("10")},
			ObservedAt: start,
		}
		mark := decimal.RequireFromString("10000")
		if index == 1 {
			mark = decimal.RequireFromString("10010")
		}
		source.marks[key] = marketdata.MarkPrice{Instrument: instrument, Price: mark, ObservedAt: start}
	}
	return &observedAtAdvancingSource{fakeMarketSource: source, clock: clock}
}

func slowMarkCoherenceSource(clock *advancingClock, start time.Time) *slowMarkMarketSource {
	return &slowMarkMarketSource{
		fakeMarketSource: completeMarketSource(start),
		clock:            clock,
		fundingCalls:     map[string]int{},
		bookCalls:        map[string]int{},
		bboCalls:         map[string]int{},
		markCalls:        map[string]int{},
	}
}

type slowMarkMarketSource struct {
	*fakeMarketSource
	clock        *advancingClock
	mu           sync.Mutex
	fundingCalls map[string]int
	bookCalls    map[string]int
	bboCalls     map[string]int
	markCalls    map[string]int
}

func (source *slowMarkMarketSource) nextCall(calls map[string]int, key string) int {
	source.mu.Lock()
	defer source.mu.Unlock()
	calls[key]++
	return calls[key]
}

func (source *slowMarkMarketSource) FundingRate(ctx context.Context, instrument marketdata.Instrument) (marketdata.FundingRate, error) {
	call := source.nextCall(source.fundingCalls, instrument.Key())
	rate, err := source.fakeMarketSource.FundingRate(ctx, instrument)
	if err != nil {
		return marketdata.FundingRate{}, err
	}
	if call > 1 {
		rate.ObservedAt = source.clock.Now()
	}
	return rate, nil
}

func (source *slowMarkMarketSource) OrderBook(ctx context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	call := source.nextCall(source.bookCalls, request.Instrument.Key())
	book, err := source.fakeMarketSource.OrderBook(ctx, request)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	if call > 1 {
		book.ObservedAt = source.clock.Now()
	}
	return book, nil
}

func (source *slowMarkMarketSource) BBO(ctx context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	call := source.nextCall(source.bboCalls, instrument.Key())
	bbo, err := source.fakeMarketSource.BBO(ctx, instrument)
	if err != nil {
		return marketdata.BBO{}, err
	}
	if call > 1 {
		bbo.ObservedAt = source.clock.Now()
	}
	return bbo, nil
}

func (source *slowMarkMarketSource) MarkPrice(ctx context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	call := source.nextCall(source.markCalls, instrument.Key())
	mark, err := source.fakeMarketSource.MarkPrice(ctx, instrument)
	if err != nil {
		return marketdata.MarkPrice{}, err
	}
	if call == 1 {
		source.clock.AdvanceBy(risk.MaxDataAge + 500*time.Millisecond)
	}
	mark.ObservedAt = source.clock.Now()
	return mark, nil
}

type observedAtAdvancingSource struct {
	*fakeMarketSource
	clock *advancingClock
}

func (source *observedAtAdvancingSource) FundingRateHistory(ctx context.Context, request marketdata.FundingRateHistoryRequest) (marketdata.FundingRatePage, error) {
	page, err := source.fakeMarketSource.FundingRateHistory(ctx, request)
	if err != nil {
		return marketdata.FundingRatePage{}, err
	}
	source.clock.AdvanceBy(2 * time.Second)
	return page, nil
}

func (source *observedAtAdvancingSource) FundingRate(ctx context.Context, instrument marketdata.Instrument) (marketdata.FundingRate, error) {
	rate, err := source.fakeMarketSource.FundingRate(ctx, instrument)
	if err != nil {
		return marketdata.FundingRate{}, err
	}
	rate.ObservedAt = source.clock.Advance()
	return rate, nil
}

func (source *observedAtAdvancingSource) OrderBook(ctx context.Context, request marketdata.OrderBookRequest) (marketdata.OrderBook, error) {
	book, err := source.fakeMarketSource.OrderBook(ctx, request)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	book.ObservedAt = source.clock.Advance()
	return book, nil
}

func (source *observedAtAdvancingSource) BBO(ctx context.Context, instrument marketdata.Instrument) (marketdata.BBO, error) {
	bbo, err := source.fakeMarketSource.BBO(ctx, instrument)
	if err != nil {
		return marketdata.BBO{}, err
	}
	bbo.ObservedAt = source.clock.Advance()
	return bbo, nil
}

func (source *observedAtAdvancingSource) MarkPrice(ctx context.Context, instrument marketdata.Instrument) (marketdata.MarkPrice, error) {
	mark, err := source.fakeMarketSource.MarkPrice(ctx, instrument)
	if err != nil {
		return marketdata.MarkPrice{}, err
	}
	mark.ObservedAt = source.clock.Advance()
	return mark, nil
}

type concurrentHistorySource struct {
	*fakeMarketSource
	mu               sync.Mutex
	activeHistory    int
	maxActiveHistory int
}

func concurrentHistoryMarketSource(now time.Time) *concurrentHistorySource {
	source := completeMarketSource(now)
	return &concurrentHistorySource{fakeMarketSource: source}
}

func (source *concurrentHistorySource) FundingRateHistory(ctx context.Context, request marketdata.FundingRateHistoryRequest) (marketdata.FundingRatePage, error) {
	source.mu.Lock()
	source.activeHistory++
	if source.activeHistory > source.maxActiveHistory {
		source.maxActiveHistory = source.activeHistory
	}
	source.mu.Unlock()

	select {
	case <-time.After(20 * time.Millisecond):
	case <-ctx.Done():
		source.mu.Lock()
		source.activeHistory--
		source.mu.Unlock()
		return marketdata.FundingRatePage{}, ctx.Err()
	}

	source.mu.Lock()
	source.activeHistory--
	source.mu.Unlock()
	return source.fakeMarketSource.FundingRateHistory(ctx, request)
}
