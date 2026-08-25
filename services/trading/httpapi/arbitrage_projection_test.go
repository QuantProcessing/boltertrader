package httpapi

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/realtimewire"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
)

type projectionCatalog struct{ snapshot catalog.Snapshot }

func (source projectionCatalog) Snapshot(context.Context) (catalog.Snapshot, error) {
	return source.snapshot, nil
}
func (projectionCatalog) Status() catalog.Status { return catalog.Status{Ready: true} }

type projectionBoard struct{ snapshot marketdata.BoardSnapshot }

func (source projectionBoard) Get(context.Context) (marketdata.BoardSnapshot, error) {
	return source.snapshot, nil
}

type projectionTopics struct {
	mu       sync.Mutex
	requests []realtimewire.TopicRequest
}

func (source *projectionTopics) Subscribe(ctx context.Context, request realtimewire.TopicRequest, _ int) (realtimewire.Subscription, error) {
	source.mu.Lock()
	source.requests = append(source.requests, request)
	source.mu.Unlock()
	updates := make(chan realtimewire.Message)
	return &projectionTopicSubscription{updates: updates}, nil
}

func (source *projectionTopics) Requests() []realtimewire.TopicRequest {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]realtimewire.TopicRequest(nil), source.requests...)
}

type projectionTopicSubscription struct {
	updates chan realtimewire.Message
	once    sync.Once
}

func (subscription *projectionTopicSubscription) Events() <-chan realtimewire.Message {
	return subscription.updates
}
func (subscription *projectionTopicSubscription) Close() {
	subscription.once.Do(func() { close(subscription.updates) })
}

func TestArbitrageProjectionRunsAndOwnsTopHotsetWithoutBrowser(t *testing.T) {
	now := time.Now().UTC()
	instruments := []catalog.Instrument{
		{InstrumentID: "binance:live:perp:USDT:BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", MarketFamily: "venue_perpetual", AssetClass: "crypto", Ready: true},
		{InstrumentID: "okx:live:perp:USDT:BTC-USDT", Venue: "okx", Environment: "live", Product: "perp", Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", MarketFamily: "venue_perpetual", AssetClass: "crypto", Ready: true},
	}
	row := func(instrumentID, rate string) marketdata.BoardRow {
		return marketdata.BoardRow{InstrumentID: instrumentID, Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rate: rate, IntervalHours: 8, MarkPrice: "100", FundingObservedAt: now, MarkObservedAt: now, PriceObservedAt: now}
	}
	board := marketdata.BoardSnapshot{ObservedAt: now, Venues: []marketdata.BoardVenue{
		{Venue: "binance", Environment: "live", Product: "perp", Settlement: "USDT", RouteProfileID: "binance.perp.usdt.v1", Rows: []marketdata.BoardRow{row(instruments[0].InstrumentID, "-0.0001")}},
		{Venue: "okx", Environment: "live", Product: "perp", Settlement: "USDT", RouteProfileID: "okx.perp.usdt.v1", Rows: []marketdata.BoardRow{row(instruments[1].InstrumentID, "0.0003")}},
	}}
	topics := &projectionTopics{}
	projection, err := NewArbitrageProjection(projectionCatalog{snapshot: catalog.Snapshot{Revision: "r1", ObservedAt: now, Instruments: instruments}}, projectionBoard{snapshot: board}, topics, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go projection.Run(ctx)
	deadline := time.Now().Add(time.Second)
	for len(topics.Requests()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	requests := topics.Requests()
	if len(requests) != 2 {
		t.Fatalf("resident projection hot subscriptions=%d want=2", len(requests))
	}
	for _, request := range requests {
		if request.InternalPurpose != "arbitrage_top" || request.Topic != realtimewire.TopicMarkPrice {
			t.Fatalf("hot request=%+v", request)
		}
	}
	current, ready, _, stop, err := projection.Subscribe(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	if !ready || len(current.Items) == 0 {
		t.Fatalf("projection not ready without browser: %+v", current)
	}
}
