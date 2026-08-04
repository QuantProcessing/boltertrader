package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/auth"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/health"
	"github.com/QuantProcessing/boltertrader/gateway/reference"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/shopspring/decimal"
)

func TestHandlerPerpPrivateResourcesUseResolvedInstrumentAndCapabilityGates(t *testing.T) {
	instrumentID := domain.InstrumentID("binance:demo:perp:USDT:BTC-USDT")
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			SettleAsset: "USDT", NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
	}
	public := catalog.Instrument{
		ID: instrumentID, Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo,
		Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT",
	}
	perp := &fakeHTTPPerpClient{positions: []exchange.Position{{
		NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT", Quantity: decimal.NewFromInt(1),
	}}}
	accounts := &fakeHTTPAccounts{
		resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentDemo, Settlement: "USDT", ReadReady: true, WriteReady: true,
		},
		status: account.Status{Resource: account.Resource{
			AccountID: "acct-p", Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Environment: exchange.EnvironmentDemo, Settlement: "USDT", ReadReady: true, WriteReady: true,
		}},
		instrument: instrument, public: public, capability: capability.StaticMatrix().Rows[1], perp: perp,
	}
	references := &fakeHTTPReferences{accounts: accounts}
	monitor := health.NewMonitor()
	monitor.SetPostgresHealthy(true)
	handler, err := NewHandler(Options{
		Monitor: monitor, Accounts: accounts, References: references, Orders: &fakeHTTPOrders{},
		Events:  newTestEventHub(t),
		Auditor: &fakeHTTPAuditor{}, Identity: domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := secret.ParseRef("env://TOKEN")
	middleware, _ := auth.NewBearerMiddleware(context.Background(), secret.StaticProvider{ref.RawForProvider(): "test-token"}, ref)
	authenticated := middleware.Wrap(handler)

	tests := []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodGet, "/v1/fills?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodGet, "/v1/balances?account_id=acct-p", "", http.StatusOK},
		{http.MethodGet, "/v1/positions?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodGet, "/v1/funding-rates?account_id=acct-p&instrument_id=" + string(instrumentID), "", http.StatusOK},
		{http.MethodPut, "/v1/accounts/acct-p/instruments/" + string(instrumentID) + "/leverage", `{"leverage":5}`, http.StatusNotImplemented},
	}
	for _, test := range tests {
		request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString(test.body))
		request.Header.Set("Authorization", "Bearer test-token")
		if test.body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		authenticated.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Errorf("%s %s status=%d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
	if perp.fillsCalls != 1 || perp.balanceCalls != 1 || perp.positionCalls != 1 ||
		perp.fundingCalls != 1 || perp.leverageCalls != 0 {
		t.Fatalf("perp method calls=%+v", perp)
	}
	if perp.lastInstrument.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("resolved instrument=%+v", perp.lastInstrument.Identity)
	}

	request := httptest.NewRequest(http.MethodGet, "/v1/positions?account_id=acct-p", nil)
	request.Header.Set("Authorization", "Bearer test-token")
	recorder := httptest.NewRecorder()
	authenticated.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unscoped positions status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var positions collection[positionResource]
	if err := json.NewDecoder(recorder.Body).Decode(&positions); err != nil {
		t.Fatalf("decode unscoped positions: %v", err)
	}
	if len(positions.Items) != 1 || positions.Items[0].InstrumentID != instrumentID {
		t.Fatalf("unscoped positions=%+v, want mapped instrument %s", positions.Items, instrumentID)
	}
}

func TestHandlerRejectsPerpOnlyResourcesForSpotBeforeClientIO(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, path := range []string{
		"/v1/positions?account_id=acct-a",
		"/v1/funding-rates?account_id=acct-a&instrument_id=binance:demo:spot:BTC-USDT",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-token")
		recorder := httptest.NewRecorder()
		fixture.handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

type fakeHTTPPerpClient struct {
	exchange.PerpClient
	fillsCalls     int
	balanceCalls   int
	positionCalls  int
	fundingCalls   int
	leverageCalls  int
	leverageErr    error
	lastInstrument exchange.Instrument
	positions      []exchange.Position
}

func (client *fakeHTTPPerpClient) Fills(_ context.Context, request exchange.FillsRequest) (exchange.FillPage, error) {
	client.fillsCalls++
	client.lastInstrument = *request.Instrument
	return exchange.FillPage{Fills: []exchange.Fill{{FillID: "fill-1"}}}, nil
}

func (client *fakeHTTPPerpClient) Balances(context.Context) ([]exchange.Balance, error) {
	client.balanceCalls++
	return []exchange.Balance{{Asset: "USDT", Total: decimal.NewFromInt(100)}}, nil
}

func (client *fakeHTTPPerpClient) Positions(_ context.Context, request exchange.PositionsRequest) ([]exchange.Position, error) {
	client.positionCalls++
	if request.Instrument == nil {
		return append([]exchange.Position(nil), client.positions...), nil
	}
	client.lastInstrument = *request.Instrument
	return []exchange.Position{{NativeSymbol: request.Instrument.Identity.NativeSymbol}}, nil
}

func (client *fakeHTTPPerpClient) FundingRate(_ context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	client.fundingCalls++
	client.lastInstrument = request.Instrument
	return exchange.FundingRate{NativeSymbol: request.Instrument.Identity.NativeSymbol}, nil
}

func (client *fakeHTTPPerpClient) SetLeverage(_ context.Context, request exchange.SetLeverageRequest) (exchange.Leverage, error) {
	client.leverageCalls++
	client.lastInstrument = request.Instrument
	if client.leverageErr != nil {
		return exchange.Leverage{}, client.leverageErr
	}
	return exchange.Leverage{NativeSymbol: request.Instrument.Identity.NativeSymbol, Effective: request.Leverage}, nil
}

var _ reference.Registry = (*fakeHTTPAccounts)(nil)
