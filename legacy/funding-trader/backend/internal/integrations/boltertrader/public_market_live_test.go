package boltertrader

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
)

func TestGatewayPublicMarketLive(t *testing.T) {
	if os.Getenv("RUN_GATEWAY_PUBLIC_READ") != "1" {
		t.Skip("set RUN_GATEWAY_PUBLIC_READ=1 for the explicit read-only public-market Gateway check")
	}
	mode := configuredProductMode()
	options := PublicMarketClientOptions{
		BaseURL: os.Getenv("FT_GATEWAY_BASE_URL"),
		Token:   liveGatewayToken(t),
		Timeout: 15 * time.Second,
		Mode:    mode,
	}
	publicClient, err := NewPublicMarketClient(options)
	if err != nil {
		t.Fatalf("construct public market client: %v", err)
	}
	gatewayClient, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: options.BaseURL, Token: options.Token, Mode: mode,
		Timeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("construct Gateway catalog client: %v", err)
	}
	provider, err := NewMarketDataProvider(gatewayClient, publicClient, mode)
	if err != nil {
		t.Fatalf("construct production marketdata provider: %v", err)
	}
	defer provider.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	readiness, err := provider.Probe(ctx)
	if err != nil {
		t.Fatalf("public-data readiness probe: %v", err)
	}
	if readiness.Mode != string(mode) || readiness.Accounts == 0 || readiness.Instruments == 0 {
		t.Fatalf("invalid public-data readiness: %+v", readiness)
	}
	instruments, err := provider.Instruments(ctx)
	if err != nil {
		t.Fatalf("discover public instruments: %v", err)
	}
	selected := configuredLiveInstruments(t, instruments)
	for _, instrument := range selected {
		if !eligibleVenueEnvironment(mode, VenueEnvironment(instrument.VenueEnvironment)) {
			t.Fatalf("invalid venue environment for %s in mode %s: %s",
				normalizedLiveSymbol(instrument), mode, instrument.VenueEnvironment)
		}
		assertLiveRESTMarketSnapshot(t, ctx, provider, instrument)
	}

	instrument := selected[0]
	assertLiveSnapshot(t, ctx, "order_book", func(streamContext context.Context) error {
		subscription, err := provider.WatchOrderBook(streamContext, instrument)
		if err != nil {
			return err
		}
		defer subscription.Close()
		event, err := subscription.Next(streamContext)
		if err != nil {
			return err
		}
		if event.Type != marketdata.StreamSnapshot || event.Sequence != 1 {
			return marketdata.ErrInvalidMarketData
		}
		return nil
	})
	assertLiveSnapshot(t, ctx, "bbo", func(streamContext context.Context) error {
		subscription, err := provider.WatchBBO(streamContext, instrument)
		if err != nil {
			return err
		}
		defer subscription.Close()
		event, err := subscription.Next(streamContext)
		if err != nil {
			return err
		}
		if event.Type != marketdata.StreamSnapshot || event.Sequence != 1 {
			return marketdata.ErrInvalidMarketData
		}
		return nil
	})
	assertLiveSnapshot(t, ctx, "mark_price", func(streamContext context.Context) error {
		subscription, err := provider.WatchMarkPrice(streamContext, instrument)
		if err != nil {
			return err
		}
		defer subscription.Close()
		event, err := subscription.Next(streamContext)
		if err != nil {
			return err
		}
		if event.Type != marketdata.StreamSnapshot || event.Sequence != 1 {
			return marketdata.ErrInvalidMarketData
		}
		return nil
	})
	assertLiveSnapshot(t, ctx, "funding_rate", func(streamContext context.Context) error {
		subscription, err := provider.WatchFundingRate(streamContext, instrument)
		if err != nil {
			return err
		}
		defer subscription.Close()
		event, err := subscription.Next(streamContext)
		if err != nil {
			return err
		}
		if event.Type != marketdata.StreamSnapshot || event.Sequence != 1 {
			return marketdata.ErrInvalidMarketData
		}
		return nil
	})
}

func assertLiveRESTMarketSnapshot(
	t *testing.T,
	ctx context.Context,
	provider *marketdata.Provider,
	instrument marketdata.Instrument,
) {
	t.Helper()
	if instrument.AccountID == "" || instrument.InstrumentID == "" {
		t.Fatalf("invalid public instrument binding for %s", normalizedLiveSymbol(instrument))
	}
	if _, err := provider.FundingRate(ctx, instrument); err != nil {
		t.Fatalf("%s current funding account=%s venue=%s environment=%s instrument=%s: %v",
			normalizedLiveSymbol(instrument), instrument.AccountID, instrument.Venue,
			instrument.VenueEnvironment, instrument.InstrumentID, err)
	}
	history, err := provider.FundingRateHistory(ctx, marketdata.FundingRateHistoryRequest{
		Instrument: instrument, Start: time.Now().UTC().Add(-30 * 24 * time.Hour),
		End: time.Now().UTC(), Limit: 100,
	})
	if err != nil {
		t.Fatalf(
			"%s funding history account=%s venue=%s environment=%s instrument=%s: %v; compatibility probes: 7d/10=%v 30d/10=%v 7d/100=%v unbounded/100=%v",
			normalizedLiveSymbol(instrument),
			instrument.AccountID,
			instrument.Venue,
			instrument.VenueEnvironment,
			instrument.InstrumentID,
			err,
			liveHistoryProbe(ctx, provider, instrument, 7*24*time.Hour, 10),
			liveHistoryProbe(ctx, provider, instrument, 30*24*time.Hour, 10),
			liveHistoryProbe(ctx, provider, instrument, 7*24*time.Hour, 100),
			liveHistoryProbe(ctx, provider, instrument, 0, 100),
		)
	}
	if len(history.Rates) > 100 {
		t.Fatalf("funding history exceeded requested bound: %d", len(history.Rates))
	}
	if _, err := provider.OrderBook(ctx, marketdata.OrderBookRequest{
		Instrument: instrument, Limit: 100,
	}); err != nil {
		t.Fatalf("%s order book account=%s venue=%s environment=%s instrument=%s: %v",
			normalizedLiveSymbol(instrument), instrument.AccountID, instrument.Venue,
			instrument.VenueEnvironment, instrument.InstrumentID, err)
	}
	if _, err := provider.BBO(ctx, instrument); err != nil {
		t.Fatalf("%s bbo account=%s venue=%s environment=%s instrument=%s: %v",
			normalizedLiveSymbol(instrument), instrument.AccountID, instrument.Venue,
			instrument.VenueEnvironment, instrument.InstrumentID, err)
	}
	if _, err := provider.MarkPrice(ctx, instrument); err != nil {
		t.Fatalf("%s mark price account=%s venue=%s environment=%s instrument=%s: %v",
			normalizedLiveSymbol(instrument), instrument.AccountID, instrument.Venue,
			instrument.VenueEnvironment, instrument.InstrumentID, err)
	}
}

func liveHistoryProbe(
	ctx context.Context,
	provider *marketdata.Provider,
	instrument marketdata.Instrument,
	window time.Duration,
	limit int,
) error {
	request := marketdata.FundingRateHistoryRequest{
		Instrument: instrument,
		End:        time.Now().UTC(),
		Limit:      limit,
	}
	if window > 0 {
		request.Start = request.End.Add(-window)
	} else {
		request.End = time.Time{}
	}
	_, err := provider.FundingRateHistory(ctx, request)
	return err
}

func configuredLiveInstruments(t *testing.T, instruments []marketdata.Instrument) []marketdata.Instrument {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("FT_MARKET_SYMBOLS"))
	if raw == "" || raw == "*" {
		return []marketdata.Instrument{instruments[0]}
	}
	requested := make(map[string]struct{})
	for _, symbol := range strings.Split(raw, ",") {
		if symbol = strings.ToUpper(strings.TrimSpace(symbol)); symbol != "" {
			requested[symbol] = struct{}{}
		}
	}
	selected := make([]marketdata.Instrument, 0, len(requested))
	found := make(map[string]struct{})
	for _, instrument := range instruments {
		symbol := normalizedLiveSymbol(instrument)
		if _, ok := requested[symbol]; !ok {
			continue
		}
		selected = append(selected, instrument)
		found[symbol] = struct{}{}
	}
	for symbol := range requested {
		if _, ok := found[symbol]; !ok {
			t.Fatalf("configured public market %s was not discovered", symbol)
		}
	}
	if len(selected) == 0 {
		t.Fatal("no configured public markets were discovered")
	}
	return selected
}

func normalizedLiveSymbol(instrument marketdata.Instrument) string {
	return strings.ToUpper(strings.TrimSpace(instrument.BaseAsset)) + "-" +
		strings.ToUpper(strings.TrimSpace(instrument.QuoteAsset))
}

func assertLiveSnapshot(
	t *testing.T,
	parent context.Context,
	kind string,
	read func(context.Context) error,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	if err := read(ctx); err != nil {
		t.Fatalf("%s public stream snapshot: %v", kind, err)
	}
}
