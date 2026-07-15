package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

func TestNadoSpotMassStatusRetainsSuccessfulRowsWhenAnotherInstrumentFails(t *testing.T) {
	digest := "0x" + strings.Repeat("a", 64)
	products := nadoTestProducts()
	products.SpotProducts = append(products.SpotProducts, sdk.SpotProduct{
		ProductID: 3,
		BookInfo:  sdk.ProductBookInfo{PriceIncrementX18: "10000000000000000", SizeIncrement: "100000000000000", MinSize: "100000000000000"},
	})
	symbols := nadoTestSymbols()
	symbols.Symbols["SOL_USDT0"] = sdk.Symbol{
		Type: string(sdk.MarketTypeSpot), ProductID: 3, Symbol: "SOL_USDT0",
		PriceIncrementX18: "10000000000000000", SizeIncrement: "100000000000000", MinSize: "100000000000000",
		LongWeightInitialX18: "800000000000000000", LongWeightMaintenanceX18: "900000000000000000",
		MakerFeeRateX18: "-100000000000000", TakerFeeRateX18: "2000000000000000",
		TradingStatus: sdk.TradingStatusLive,
	}
	provider, err := newInstrumentProviderFromDiscovery(products, symbols, []enums.InstrumentKind{enums.KindSpot})
	if err != nil {
		t.Fatalf("build two-spot provider: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		productID, ok := request["product_id"].(float64)
		if !ok {
			http.Error(w, "missing product id", http.StatusBadRequest)
			return
		}
		if int64(productID) == 3 {
			http.Error(w, "forced product failure", http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"sender":%q,"product_id":1,"orders":[{"product_id":1,"sender":%q,"price_x18":"1000000000000000000","amount":"2","expiration":"4000000000","nonce":"1","unfilled_amount":"2","digest":%q,"placed_at":1700000000000,"appendix":"1","order_type":"limit"}]},"request_type":"subaccount_orders"}`, request["sender"], request["sender"], digest)
	}))
	t.Cleanup(server.Close)

	rest := nadoTestRESTClient(t, server)
	rest, err = rest.WithCredentials(strings.Repeat("1", 64), "arb")
	if err != nil {
		t.Fatal(err)
	}
	exec := newExecutionClient(rest, provider, clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	query := model.MassStatusQuery{AccountID: AccountIDUnified}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 1 {
		t.Fatalf("retained reports=%+v, want one successful row", mass.OrderReports)
	}
	for _, report := range mass.OrderReports {
		if report.Order.VenueOrderID != digest || report.Order.Request.InstrumentID.Symbol != "ETH-USDT0" {
			t.Fatalf("retained report=%+v", report)
		}
	}
	expectedIDs := make([]model.InstrumentID, 0)
	for _, inst := range provider.All() {
		if inst != nil && inst.ID.Kind == enums.KindSpot {
			expectedIDs = append(expectedIDs, inst.ID)
		}
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDUnified || coverage.Scope.Through.IsZero() || len(coverage.Scope.InstrumentIDs) != len(expectedIDs) {
		t.Fatalf("coverage=%+v, want fully scoped Partial", coverage)
	}
	for _, id := range expectedIDs {
		if !coverage.Scope.ContainsInstrument(id) {
			t.Fatalf("coverage selector %v omitted %s", coverage.Scope.InstrumentIDs, id)
		}
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

func TestNadoMassStatusDistinguishesEmptyFromPreIOUnavailable(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	requested := model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true, IncludePositions: true}
	unavailable, err := exec.GenerateExecutionMassStatus(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if unavailable.OpenOrdersCoverage.State != model.CoverageUnavailable || unavailable.FillsCoverage.State != model.CoverageUnavailable || unavailable.PositionsCoverage.State != model.CoverageUnavailable ||
		!unavailable.OpenOrdersCoverage.Scope.IsZero() || !unavailable.FillsCoverage.Scope.IsZero() || !unavailable.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("pre-I/O coverage=%+v/%+v/%+v", unavailable.OpenOrdersCoverage, unavailable.FillsCoverage, unavailable.PositionsCoverage)
	}
	if err := unavailable.ValidateFor(requested); err != nil {
		t.Fatalf("pre-I/O validation: %v", err)
	}
	emptyQuery := requested
	emptyQuery.InstrumentIDs = []model.InstrumentID{}
	empty, err := exec.GenerateExecutionMassStatus(context.Background(), emptyQuery)
	if err != nil {
		t.Fatal(err)
	}
	if empty.OpenOrdersCoverage.State != model.CoverageComplete || empty.FillsCoverage.State != model.CoverageComplete || empty.PositionsCoverage.State != model.CoverageComplete || empty.OpenOrdersCoverage.Scope.InstrumentIDs == nil {
		t.Fatalf("empty coverage=%+v/%+v/%+v", empty.OpenOrdersCoverage, empty.FillsCoverage, empty.PositionsCoverage)
	}
	if err := empty.ValidateFor(emptyQuery); err != nil {
		t.Fatalf("empty validation: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary", http.StatusInternalServerError)
	}))
	defer server.Close()
	rest := nadoTestRESTClient(t, server)
	rest, err = rest.WithCredentials("1111111111111111111111111111111111111111111111111111111111111111", "arb")
	if err != nil {
		t.Fatal(err)
	}
	attemptedExec := newExecutionClient(rest, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	attempted, err := attemptedExec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatal(err)
	}
	if attempted.OpenOrdersCoverage.State != model.CoverageUnavailable || attempted.OpenOrdersCoverage.Scope.IsZero() {
		t.Fatalf("attempted coverage=%+v, want scoped Unavailable", attempted.OpenOrdersCoverage)
	}
}

func TestNadoMassStatusRejectsMismatchedScopeBeforeIO(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	rest := nadoTestRESTClient(t, server)
	var err error
	rest, err = rest.WithCredentials("1111111111111111111111111111111111111111111111111111111111111111", "arb")
	if err != nil {
		t.Fatal(err)
	}
	exec := newExecutionClient(rest, nadoTestProvider(), clock.NewRealClock(), enums.KindPerp, AccountIDUnified)
	id := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	wrongKind := id
	wrongKind.Kind = enums.KindSpot
	unknown := id
	unknown.Symbol = "UNKNOWN-USDT0"

	for name, query := range map[string]model.MassStatusQuery{
		"account":            {AccountID: "NADO-OTHER"},
		"venue":              {Venue: "OTHER"},
		"instrument venue":   {InstrumentIDs: []model.InstrumentID{{Venue: "OTHER", Symbol: id.Symbol, Kind: id.Kind}}},
		"instrument kind":    {InstrumentIDs: []model.InstrumentID{wrongKind}},
		"unknown instrument": {InstrumentIDs: []model.InstrumentID{unknown}},
	} {
		t.Run(name, func(t *testing.T) {
			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err == nil || mass != nil {
				t.Fatalf("mass=%+v err=%v, want nil fail-closed error", mass, err)
			}
		})
	}
	if called {
		t.Fatal("invalid mass-status scope crossed the venue I/O boundary")
	}
}
