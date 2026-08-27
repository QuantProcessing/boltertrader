package fundingboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

func TestProjectionFundingHistoryPassesNativeIdentityAndWindow(t *testing.T) {
	histories := &fakeHistoryFactory{}
	capabilities, capabilityErr := marketcapability.DefaultRegistry()
	if capabilityErr != nil {
		t.Fatal(capabilityErr)
	}
	projection, err := NewProjection(markethub.NewFactStore(nil, time.Now), histories, routeprofile.DefaultRegistry(), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = projection.Close() })
	since, until := time.Unix(1_719_964_800, 0).UTC(), time.Unix(1_720_051_200, 0).UTC()
	snapshot, err := projection.History(context.Background(), HistoryQuery{
		RouteProfileID: "binance.perp.usdt.v1", NativeSymbol: "BTCUSDT", NativeID: "binance-1", Since: since, Until: until,
	})
	if err != nil || len(snapshot.Points) != 1 {
		t.Fatalf("History snapshot=%+v err=%v", snapshot, err)
	}
	request := histories.lastRequest()
	if request.NativeSymbol != "BTCUSDT" || request.NativeID != "binance-1" || !request.Since.Equal(since) || !request.Until.Equal(until) {
		t.Fatalf("history request=%+v", request)
	}
}

func TestProjectionFundingHistoryRefusalsAndClose(t *testing.T) {
	capabilities, capabilityErr := marketcapability.DefaultRegistry()
	if capabilityErr != nil {
		t.Fatal(capabilityErr)
	}
	projection, err := NewProjection(markethub.NewFactStore(nil, time.Now), &fakeHistoryFactory{}, routeprofile.DefaultRegistry(), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	since, until := time.Now().Add(-24*time.Hour), time.Now()
	for _, test := range []struct {
		query HistoryQuery
		want  error
	}{
		{HistoryQuery{RouteProfileID: "nado.perp.usdt0.v1", NativeSymbol: "BTCUSDT0", Since: since, Until: until}, ErrHistoryUnsupported},
		{HistoryQuery{RouteProfileID: "unknown", NativeSymbol: "BTC", Since: since, Until: until}, ErrHistoryUnsupported},
		{HistoryQuery{RouteProfileID: "binance.perp.usdt.v1", Since: since, Until: until}, ErrHistoryUnsupported},
		{HistoryQuery{RouteProfileID: "binance.perp.usdt.v1", NativeSymbol: "BTCUSDT", Until: until}, ErrHistoryWindow},
		{HistoryQuery{RouteProfileID: "binance.perp.usdt.v1", NativeSymbol: "BTCUSDT", Since: until.Add(-90 * 24 * time.Hour), Until: until}, ErrHistoryWindow},
	} {
		if _, err := projection.History(context.Background(), test.query); !errors.Is(err, test.want) {
			t.Fatalf("History(%+v)=%v, want %v", test.query, err, test.want)
		}
	}
	_ = projection.Close()
	if _, err := projection.History(context.Background(), HistoryQuery{RouteProfileID: "binance.perp.usdt.v1", NativeSymbol: "BTCUSDT", Since: since, Until: until}); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("History after close=%v", err)
	}
}

type fakeHistoryFactory struct {
	mu      sync.Mutex
	request exchange.FundingHistoryRequest
}

func (factory *fakeHistoryFactory) BuildHistory(profile routeprofile.Profile) (HistoryConnector, error) {
	if profile.Venue == "nado" || profile.Venue == "aster" {
		return nil, ErrHistoryUnsupported
	}
	return &fakeHistoryConnector{factory: factory}, nil
}

func (factory *fakeHistoryFactory) lastRequest() exchange.FundingHistoryRequest {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.request
}

type fakeHistoryConnector struct{ factory *fakeHistoryFactory }

func (connector *fakeHistoryConnector) Load(_ context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	connector.factory.mu.Lock()
	connector.factory.request = request
	connector.factory.mu.Unlock()
	return exchange.FundingHistorySnapshot{SchemaVersion: 1, ObservedAt: time.Now().UTC(), Points: []exchange.FundingHistoryPoint{{FundingTime: request.Since, Rate: decimal.RequireFromString("0.0001")}}}, nil
}

func (*fakeHistoryConnector) Close() error { return nil }
