package marketdata

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPublicInterfacesDoNotExposePrivateOrMutatingMethods(t *testing.T) {
	for _, test := range []struct {
		name      string
		surface   any
		forbidden []string
	}{
		{
			name:    "rest",
			surface: (*PublicREST)(nil),
			forbidden: []string{
				"PlaceOrder", "CancelOrder", "OpenOrders", "OrderHistory", "Fills",
				"Balances", "PerpAccount", "Positions", "SetLeverage", "WebSocket",
			},
		},
		{
			name:    "stream",
			surface: (*PublicStream)(nil),
			forbidden: []string{
				"PlaceOrder", "CancelOrder", "WatchOrders", "WatchFills",
				"WatchBalances", "WatchPositions",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			typ := reflect.TypeOf(test.surface).Elem()
			for _, method := range test.forbidden {
				if _, ok := typ.MethodByName(method); ok {
					t.Fatalf("%s exposes forbidden method %s", typ, method)
				}
			}
		})
	}
}

func TestProviderForwardsOnlyPublicOperations(t *testing.T) {
	source := &spySource{}
	provider, err := NewProvider(source, source, source, nil)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	instrument := Instrument{AccountID: "acct", InstrumentID: "inst"}
	ctx := context.Background()
	if _, err := provider.Instruments(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.FundingRate(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.FundingRateHistory(ctx, FundingRateHistoryRequest{Instrument: instrument}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.OrderBook(ctx, OrderBookRequest{Instrument: instrument}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.BBO(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.MarkPrice(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.WatchOrderBook(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.WatchBBO(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.WatchMarkPrice(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.WatchFundingRate(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	if source.publicCalls != 11 {
		t.Fatalf("public calls = %d, want 11", source.publicCalls)
	}
}

func TestProviderRequiresRESTAndReadinessAndFailsClosedWithoutStreams(t *testing.T) {
	source := &spySource{}
	if _, err := NewProvider(nil, source, source, nil); err == nil {
		t.Fatal("expected missing rest rejection")
	}
	if _, err := NewProvider(source, source, nil, nil); err == nil {
		t.Fatal("expected missing readiness rejection")
	}
	provider, err := NewProvider(source, nil, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.WatchBBO(context.Background(), Instrument{}); !errors.Is(err, ErrStreamUnavailable) {
		t.Fatalf("WatchBBO error = %v", err)
	}
}

func TestInstrumentKeyIncludesAccountScope(t *testing.T) {
	first := Instrument{AccountID: "account-a", InstrumentID: "shared-id"}
	second := Instrument{AccountID: "account-b", InstrumentID: "shared-id"}
	if first.Key() == second.Key() {
		t.Fatal("instrument key lost account scope")
	}
}

type spySource struct {
	publicCalls int
}

func (source *spySource) Instruments(context.Context) ([]Instrument, error) {
	source.publicCalls++
	return nil, nil
}

func (source *spySource) FundingRate(context.Context, Instrument) (FundingRate, error) {
	source.publicCalls++
	return FundingRate{}, nil
}

func (source *spySource) FundingRateHistory(context.Context, FundingRateHistoryRequest) (FundingRatePage, error) {
	source.publicCalls++
	return FundingRatePage{}, nil
}

func (source *spySource) OrderBook(context.Context, OrderBookRequest) (OrderBook, error) {
	source.publicCalls++
	return OrderBook{}, nil
}

func (source *spySource) BBO(context.Context, Instrument) (BBO, error) {
	source.publicCalls++
	return BBO{}, nil
}

func (source *spySource) MarkPrice(context.Context, Instrument) (MarkPrice, error) {
	source.publicCalls++
	return MarkPrice{}, nil
}

func (source *spySource) WatchOrderBook(context.Context, Instrument) (Subscription[OrderBook], error) {
	source.publicCalls++
	return stubSubscription[OrderBook]{}, nil
}

func (source *spySource) WatchBBO(context.Context, Instrument) (Subscription[BBO], error) {
	source.publicCalls++
	return stubSubscription[BBO]{}, nil
}

func (source *spySource) WatchMarkPrice(context.Context, Instrument) (Subscription[MarkPrice], error) {
	source.publicCalls++
	return stubSubscription[MarkPrice]{}, nil
}

func (source *spySource) WatchFundingRate(context.Context, Instrument) (Subscription[FundingRate], error) {
	source.publicCalls++
	return stubSubscription[FundingRate]{}, nil
}

func (source *spySource) Probe(context.Context) (Readiness, error) {
	source.publicCalls++
	return Readiness{}, nil
}

type stubSubscription[T any] struct{}

func (stubSubscription[T]) Next(context.Context) (StreamEvent[T], error) {
	return StreamEvent[T]{}, nil
}

func (stubSubscription[T]) Close() error { return nil }
