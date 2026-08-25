package factoryclient

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

func TestOKXDerivativeLoaderCoordinatesFourNativeBatchOperations(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	requests := &operationRecordingMarketRequests{}
	loader := okxDerivativeSnapshotLoader{
		settlement: "USDT",
		settings:   Settings{MarketRequests: requests},
		client: okxDerivativeSnapshotClientStub{
			funding: []okx.FundingRate{{InstrumentType: "SWAP", InstrumentID: "BTC-USDT-SWAP", FundingRate: "0.0001", FundingTime: "1787659200000", NextFundingTime: "1787688000000", Ts: "1787659200000"}},
			marks:   []okx.MarkPrice{{InstType: "SWAP", InstId: "BTC-USDT-SWAP", MarkPx: "78000", Ts: "1787659200000"}},
			oi:      []okx.OpenInterest{{InstType: "SWAP", InstId: "BTC-USDT-SWAP", OICcy: "12", OIUsd: "936000", Ts: "1787659200000"}},
			indexes: []okx.IndexTicker{{InstId: "BTC-USDT", IdxPx: "77990", Ts: "1787659200000"}},
		},
	}
	scope := exchange.CatalogScope{Venue: exchange.VenueOKX, Product: exchange.ProductPerp, Environment: exchange.EnvironmentLive, Settlement: "USDT"}
	snapshot, err := loadDerivativeSnapshot(context.Background(), scope, loader, loader.settings)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || !snapshot.Entries[0].MarkPrice.Valid || !snapshot.Entries[0].IndexPrice.Valid || !snapshot.Entries[0].OpenInterest.Valid {
		t.Fatalf("snapshot=%+v at %s", snapshot, now)
	}
	want := []string{"funding_rates:swap", "mark_prices:swap", "open_interest:swap", "index_tickers:swap"}
	if !reflect.DeepEqual(requests.operations, want) {
		t.Fatalf("operations=%v want=%v", requests.operations, want)
	}
}

type operationRecordingMarketRequests struct{ operations []string }

func (requests *operationRecordingMarketRequests) Do(ctx context.Context, _ exchange.Venue, operation, collapseKey string, call func(context.Context) (any, error)) (any, error) {
	requests.operations = append(requests.operations, operation+":"+collapseKey)
	return call(ctx)
}

type okxDerivativeSnapshotClientStub struct {
	funding []okx.FundingRate
	marks   []okx.MarkPrice
	oi      []okx.OpenInterest
	indexes []okx.IndexTicker
}

func (client okxDerivativeSnapshotClientStub) GetAllFundingRates(context.Context) ([]okx.FundingRate, error) {
	return client.funding, nil
}

func (client okxDerivativeSnapshotClientStub) GetMarkPrices(context.Context, string) ([]okx.MarkPrice, error) {
	return client.marks, nil
}

func (client okxDerivativeSnapshotClientStub) GetOpenInterests(context.Context, string) ([]okx.OpenInterest, error) {
	return client.oi, nil
}

func (client okxDerivativeSnapshotClientStub) GetIndexTickers(context.Context, string) ([]okx.IndexTicker, error) {
	return client.indexes, nil
}
