package publicmarket

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

func TestSnapshotOwnsPublicToNativeFundingIdentity(t *testing.T) {
	instrumentID := domain.InstrumentID("okx:live:perp:USDT:BTC-USDT-SWAP")
	snapshot := Snapshot{Instruments: []Instrument{{
		RouteProfileID: "okx.perp.usdt.v1",
		Catalog: catalog.Instrument{
			ID: instrumentID, Venue: exchange.VenueOKX, Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT",
		},
		Canonical: exchange.Instrument{Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueOKX, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT-SWAP", NativeSymbol: "BTC-USDT-SWAP", NativeID: "swap-1",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
		}},
	}}}

	matched, ok := snapshot.MatchFundingEntry("okx.perp.usdt.v1", exchange.FundingBoardEntry{
		// A public spelling is deliberately not used as an identity key.
		Symbol: "BTC-USDT", NativeSymbol: "BTC-USDT-SWAP", NativeID: "swap-1",
	})
	if !ok || matched.Catalog.ID != instrumentID {
		t.Fatalf("matched=%+v ok=%v", matched, ok)
	}
	if _, ok := snapshot.MatchFundingEntry("okx.perp.usdc.v1", exchange.FundingBoardEntry{NativeID: "swap-1"}); ok {
		t.Fatal("native id from another route profile was accepted")
	}
	if found, ok := snapshot.Find(string(instrumentID)); !ok || found.Canonical.Identity.NativeID != "swap-1" {
		t.Fatalf("found=%+v ok=%v", found, ok)
	}
}

func TestSourceServesPublicMarketWithoutAnAccount(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	connector := &fakeConnector{
		instrument: exchange.Instrument{
			Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
			},
			Rules: exchange.InstrumentRules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
				MinQuantity: decimal.RequireFromString("0.001"),
			},
		},
		book: exchange.OrderBook{
			Time: observedAt,
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		},
		mark: exchange.MarkPriceEvent{Price: decimal.RequireFromString("100.5"), Time: observedAt},
		funding: exchange.FundingRate{
			Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt,
		},
		history: exchange.FundingRatePage{Rates: []exchange.FundingRate{{
			Rate: decimal.RequireFromString("0.0002"), ObservedAt: observedAt.Add(-time.Hour), FundingTime: observedAt.Add(-time.Hour),
		}}},
		candles: exchange.CandlePage{Candles: []exchange.Candle{{
			OpenTime: observedAt.Add(-time.Minute), CloseTime: observedAt,
			Open: decimal.NewFromInt(99), High: decimal.NewFromInt(102), Low: decimal.NewFromInt(98), Close: decimal.NewFromInt(100), Volume: decimal.NewFromInt(7),
		}}},
	}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Instruments) != 1 || snapshot.Revision == "" || snapshot.ObservedAt.IsZero() {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	instrumentID := snapshot.Instruments[0].Catalog.ID.String()
	if instrumentID != "binance:live:perp:USDT:BTC-USDT" {
		t.Fatalf("instrument id=%q", instrumentID)
	}

	query := Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: instrumentID}
	fact, err := source.MarketData(context.Background(), query, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if fact.Book.Bids[0].Price.String() != "100" || !fact.HasMark || !fact.HasFunding || len(fact.FundingHistory.Rates) != 1 {
		t.Fatalf("fact=%+v", fact)
	}
	page, err := source.Candles(context.Background(), query, "1m", 300)
	if err != nil || len(page.Page.Candles) != 1 {
		t.Fatalf("candles=%+v err=%v", page, err)
	}
	if connector.refreshes != 1 {
		t.Fatalf("catalog refreshes=%d, want one cached public refresh", connector.refreshes)
	}
}

func TestSourceKeepsDepthWhenOptionalMarketFacetsFail(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	connector := &fakeConnector{
		instrument: exchange.Instrument{
			Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
			},
			Rules: exchange.InstrumentRules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
				MinQuantity: decimal.RequireFromString("0.001"),
			},
		},
		book: exchange.OrderBook{
			Time: observedAt,
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		},
		markErr:    errors.New("mark unavailable"),
		fundingErr: errors.New("funding unavailable"),
	}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	query := Query{
		Venue: "binance", Environment: "live", Product: "perp",
		InstrumentID: string(snapshot.Instruments[0].Catalog.ID),
	}
	fact, err := source.MarketData(context.Background(), query, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fact.Book.Bids) != 1 || len(fact.Book.Asks) != 1 || fact.HasMark || fact.HasFunding {
		t.Fatalf("fact=%+v", fact)
	}
}

type fakeFactory struct {
	connector Connector
}

func (factory fakeFactory) Build(profile routeprofile.Profile) (Connector, error) {
	if profile.Venue == "binance" && profile.Product == routeprofile.ProductPerp && profile.SettlementAsset == "USDT" {
		return factory.connector, nil
	}
	return nil, errors.New("profile not exercised")
}

type fakeConnector struct {
	instrument exchange.Instrument
	book       exchange.OrderBook
	mark       exchange.MarkPriceEvent
	funding    exchange.FundingRate
	history    exchange.FundingRatePage
	candles    exchange.CandlePage
	markErr    error
	fundingErr error
	historyErr error
	refreshes  int
}

func (connector *fakeConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	connector.refreshes++
	return []exchange.Instrument{connector.instrument}, nil
}

func (connector *fakeConnector) OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return connector.book, nil
}

func (connector *fakeConnector) Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error) {
	return connector.candles, nil
}

func (connector *fakeConnector) FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, bool, error) {
	return connector.funding, true, connector.fundingErr
}

func (connector *fakeConnector) FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, bool, error) {
	return connector.history, true, connector.historyErr
}

func (connector *fakeConnector) MarkPrice(context.Context, exchange.Instrument) (exchange.MarkPriceEvent, bool, error) {
	return connector.mark, true, connector.markErr
}

func (*fakeConnector) WebSocket() exchange.SpotWebSocket { return nil }

func (*fakeConnector) Close() error { return nil }
