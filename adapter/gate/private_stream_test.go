package gate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
)

type recordingGatePrivateStream struct {
	subscriptions []gatePrivateSubscription
}

type gatePrivateSubscription struct {
	channel string
	payload []string
}

func (s *recordingGatePrivateStream) Subscribe(_ context.Context, channel string, payload []string, _ func(json.RawMessage)) error {
	s.subscriptions = append(s.subscriptions, gatePrivateSubscription{
		channel: channel,
		payload: append([]string(nil), payload...),
	})
	return nil
}

func (s *recordingGatePrivateStream) SetReconnectHooks(func(error), func()) {}

func (s *recordingGatePrivateStream) Close() error { return nil }

func TestStartSpotStreamsUsesOneAllSymbolsSubscriptionPerPrivateTopic(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "AAA-USDT", Kind: enums.KindSpot},
			VenueSymbol: "AAA_USDT",
		},
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "BBB-USDT", Kind: enums.KindSpot},
			VenueSymbol: "BBB_USDT",
		},
	})
	stream := &recordingGatePrivateStream{}
	clk := clock.NewRealClock()
	adapter := &Adapter{
		provider:    provider,
		privateSpot: stream,
		exec:        newExecutionClient(nil, provider, clk, AccountIDUnified),
		acct:        newAccountClient(nil, provider, clk, []enums.InstrumentKind{enums.KindSpot}, AccountIDUnified),
		clk:         clk,
	}

	if err := adapter.startSpotStreams(context.Background()); err != nil {
		t.Fatalf("startSpotStreams: %v", err)
	}

	want := []gatePrivateSubscription{
		{channel: "spot.orders", payload: []string{"!all"}},
		{channel: "spot.usertrades", payload: []string{"!all"}},
		{channel: "spot.balances"},
	}
	if len(stream.subscriptions) != len(want) {
		t.Fatalf("subscriptions=%v, want exactly %v", stream.subscriptions, want)
	}
	for i := range want {
		got := stream.subscriptions[i]
		if got.channel != want[i].channel || !equalGateStringSlice(got.payload, want[i].payload) {
			t.Fatalf("subscription[%d]=%+v, want %+v", i, got, want[i])
		}
	}
}

func TestSpotAllSymbolsEventsDropUnknownInstruments(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "AAA-USDT", Kind: enums.KindSpot},
		VenueSymbol: "AAA_USDT",
	}})
	resolveSpot := func(symbol string) model.InstrumentID {
		id, ok := provider.ResolveVenueInstrument(symbol, enums.KindSpot, "")
		if !ok {
			return model.InstrumentID{}
		}
		return id
	}

	orderEvents := execEventsFromSpotOrderMessage(&gatesdk.SpotOrderMessage{
		Orders: []gatesdk.Order{
			{ID: "known", CurrencyPair: "AAA_USDT", Side: "buy", Amount: "1"},
			{ID: "unknown", CurrencyPair: "DELISTED_USDT", Side: "buy", Amount: "1"},
		},
	}, resolveSpot, AccountIDUnified)
	if len(orderEvents) != 1 {
		t.Fatalf("order events=%+v, want only the loaded Spot instrument", orderEvents)
	}

	fillEvents := execEventsFromSpotUserTradeMessage(&gatesdk.SpotUserTradeMessage{
		Trades: []gatesdk.SpotUserTrade{
			{ID: "known", CurrencyPair: "AAA_USDT", OrderID: "known-order", Side: "buy", Amount: "1", Price: "1"},
			{ID: "unknown", CurrencyPair: "DELISTED_USDT", OrderID: "unknown-order", Side: "buy", Amount: "1", Price: "1"},
		},
	}, resolveSpot, AccountIDUnified)
	if len(fillEvents) != 1 {
		t.Fatalf("fill events=%+v, want only the loaded Spot instrument", fillEvents)
	}
}

func TestStartFuturesStreamsUsesUserScopedAllSymbolsSubscriptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/futures/usdt/accounts" {
			t.Fatalf("path=%q, want futures account", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"user":42,"total":"100","available":"100","currency":"USDT"}`))
	}))
	defer server.Close()

	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "AAA-USDT", Kind: enums.KindPerp},
			VenueSymbol: "AAA_USDT",
			Settle:      "USDT",
		},
		{
			ID:          model.InstrumentID{Venue: VenueName, Symbol: "BBB-USDT", Kind: enums.KindPerp},
			VenueSymbol: "BBB_USDT",
			Settle:      "USDT",
		},
	})
	stream := &recordingGatePrivateStream{}
	clk := clock.NewRealClock()
	rest := gatesdk.NewClient().WithBaseURL(server.URL).WithCredentials("key", "secret")
	adapter := &Adapter{
		provider:       provider,
		rest:           rest,
		privateFutures: stream,
		exec:           newExecutionClient(rest, provider, clk, AccountIDUnified),
		acct:           newAccountClient(rest, provider, clk, []enums.InstrumentKind{enums.KindPerp}, AccountIDUnified),
		clk:            clk,
	}

	if err := adapter.startFuturesStreams(context.Background()); err != nil {
		t.Fatalf("startFuturesStreams: %v", err)
	}
	if got := adapter.exec.futuresOrderPositionSide(gatesdk.FuturesOrder{Size: 1}); got != enums.PosNet {
		t.Fatalf("single-mode positive futures size side=%s, want NET", got)
	}

	want := []gatePrivateSubscription{
		{channel: "futures.orders", payload: []string{"42", "!all"}},
		{channel: "futures.usertrades", payload: []string{"42", "!all"}},
		{channel: "futures.positions", payload: []string{"42", "!all"}},
		{channel: "futures.balances", payload: []string{"42"}},
	}
	if len(stream.subscriptions) != len(want) {
		t.Fatalf("subscriptions=%v, want exactly %v", stream.subscriptions, want)
	}
	for i := range want {
		got := stream.subscriptions[i]
		if got.channel != want[i].channel || !equalGateStringSlice(got.payload, want[i].payload) {
			t.Fatalf("subscription[%d]=%+v, want %+v", i, got, want[i])
		}
	}
}

func equalGateStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
