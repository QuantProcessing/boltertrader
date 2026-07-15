package lighter

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterMassStatusOwnsFrozenTypedCoverage(t *testing.T) {
	perp, spot := lighterCoverageInstruments()
	provider := newRegistry([]*model.Instrument{spot, perp, spot})
	start := time.Date(2026, 7, 15, 5, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/accountActiveOrders" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		calls++
		if calls == 1 {
			provider.byID = map[string]*model.Instrument{perp.ID.String(): perp}
			provider.byMarketID = map[int]*model.Instrument{*perp.AssetIndex: perp}
		}
		clk.Advance(time.Minute)
		_, _ = w.Write([]byte(`{"code":200,"orders":[]}`))
	}))
	defer server.Close()
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = server.URL
	exec := newExecutionClient(rest, provider, clk, 66)
	query := model.MassStatusQuery{Venue: venueName}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if calls != 2 {
		t.Fatalf("open-order calls=%d, want one per frozen market", calls)
	}
	if mass.AccountID != AccountIDDefault || mass.OpenOrdersCoverage.State != model.CoverageComplete || !mass.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("mass account=%q open coverage=%+v", mass.AccountID, mass.OpenOrdersCoverage)
	}
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{perp.ID, spot.ID})
	if got := mass.OpenOrdersCoverage.Scope.InstrumentIDs; len(got) != 2 || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
		t.Fatalf("frozen ids=%v, want %v", got, wantIDs)
	}
	if mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("optional coverage fills=%+v positions=%+v", mass.FillsCoverage, mass.PositionsCoverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestLighterMassStatusRequestedPositionsAreCompleteAndFillsUnavailable(t *testing.T) {
	perp, spot := lighterCoverageInstruments()
	provider := newRegistry([]*model.Instrument{perp, spot})
	start := time.Date(2026, 7, 15, 6, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/accountActiveOrders":
			_, _ = w.Write([]byte(`{"code":200,"orders":[]}`))
		case "/api/v1/account":
			_, _ = fmt.Fprintf(w, `{"code":200,"accounts":[{"account_index":66,"positions":[{"market_id":%d,"sign":1,"position":"1","avg_entry_price":"100","unrealized_pnl":"2"}]}]}`, *perp.AssetIndex)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = server.URL
	exec := newExecutionClient(rest, provider, clk, 66)
	query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{spot.ID, perp.ID, spot.ID}, IncludeFills: true, IncludePositions: true}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.FillsCoverage.State != model.CoverageUnavailable || !mass.FillsCoverage.Scope.IsZero() {
		t.Fatalf("fills coverage=%+v, want unavailable before request", mass.FillsCoverage)
	}
	if mass.PositionsCoverage.State != model.CoverageComplete || !mass.PositionsCoverage.Scope.Through.Equal(start) {
		t.Fatalf("positions coverage=%+v, want complete at position request start", mass.PositionsCoverage)
	}
	if len(mass.PositionReports) != 1 {
		t.Fatalf("position reports=%d, want one", len(mass.PositionReports))
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestLighterMassStatusMarksAttemptedOpenOrdersUnavailable(t *testing.T) {
	perp, _ := lighterCoverageInstruments()
	start := time.Unix(200, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":500,"message":"temporary failure"}`))
	}))
	defer server.Close()
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = server.URL
	exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp}), clock.NewSimulatedClock(start), 66)
	query := model.MassStatusQuery{}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoverageUnavailable || !coverage.Scope.Through.Equal(start) ||
		len(coverage.Scope.InstrumentIDs) != 1 || coverage.Scope.InstrumentIDs[0] != perp.ID {
		t.Fatalf("attempted open-order coverage=%+v", coverage)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestLighterMassStatusMarksCursorTruncatedPagePartialAndRetainsRows(t *testing.T) {
	perp, _ := lighterCoverageInstruments()
	start := time.Unix(250, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/accountActiveOrders" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_, _ = fmt.Fprintf(w, `{"code":200,"next_cursor":"cursor-2","orders":[{"order_index":42,"client_order_index":77,"market_index":%d,"initial_base_amount":"1","remaining_base_amount":"1","price":"100","status":"open","side":"buy"}]}`, *perp.AssetIndex)
	}))
	defer server.Close()
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = server.URL
	exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp}), clock.NewSimulatedClock(start), 66)
	query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{perp.ID}}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	coverage := mass.OpenOrdersCoverage
	if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || coverage.Scope.ClientID != "" || !coverage.Scope.Through.Equal(start) {
		t.Fatalf("open coverage=%+v, want fully scoped cursor-truncated Partial", coverage)
	}
	if got := coverage.Scope.InstrumentIDs; len(got) != 1 || got[0] != perp.ID {
		t.Fatalf("open coverage ids=%v, want [%s]", got, perp.ID)
	}
	if report, ok := mass.OrderReports["42"]; !ok || report.Order.Request.InstrumentID != perp.ID {
		t.Fatalf("retained report=%+v ok=%t, want positive row for %s", report, ok, perp.ID)
	}
	if !hasLighterWarning(mass.Warnings, "OPEN_ORDERS_TRUNCATED") {
		t.Fatalf("warnings=%+v, want cursor truncation warning", mass.Warnings)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed mass status validation: %v", err)
	}
}

func TestLighterMassStatusRetainsSuccessfulMarketWhenAnotherFails(t *testing.T) {
	perp, spot := lighterCoverageInstruments()
	wantIDs := model.NormalizeInstrumentIDs([]model.InstrumentID{perp.ID, spot.ID})
	for _, success := range []*model.Instrument{perp, spot} {
		t.Run(success.ID.Symbol, func(t *testing.T) {
			start := time.Unix(300, 0)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/accountActiveOrders" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				if r.URL.Query().Get("market_id") != fmt.Sprint(*success.AssetIndex) {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"code":500,"message":"temporary failure"}`))
					return
				}
				_, _ = fmt.Fprintf(w, `{"code":200,"orders":[{"order_index":%d,"client_order_index":77,"market_index":%d,"initial_base_amount":"1","remaining_base_amount":"1","price":"100","status":"open","side":"buy"}]}`, 40+*success.AssetIndex, *success.AssetIndex)
			}))
			defer server.Close()
			rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
			rest.BaseURL = server.URL
			exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp, spot}), clock.NewSimulatedClock(start), 66)
			query := model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{spot.ID, perp.ID}}

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
			if err != nil {
				t.Fatalf("GenerateExecutionMassStatus: %v", err)
			}
			coverage := mass.OpenOrdersCoverage
			if coverage.State != model.CoveragePartial || !coverage.Scope.Through.Equal(start) || coverage.Scope.AccountID != AccountIDDefault {
				t.Fatalf("coverage=%+v, want fully scoped Partial", coverage)
			}
			if got := coverage.Scope.InstrumentIDs; len(got) != len(wantIDs) || got[0] != wantIDs[0] || got[1] != wantIDs[1] {
				t.Fatalf("coverage ids=%v, want %v", got, wantIDs)
			}
			if len(mass.OrderReports) != 1 {
				t.Fatalf("reports=%+v, want one retained successful-market row", mass.OrderReports)
			}
			for _, report := range mass.OrderReports {
				if report.Order.Request.InstrumentID != success.ID {
					t.Fatalf("report instrument=%s, want %s", report.Order.Request.InstrumentID, success.ID)
				}
			}
			if err := mass.ValidateFor(query); err != nil {
				t.Fatalf("typed mass status validation: %v", err)
			}
		})
	}
}

func TestLighterClientIDScopedMassStatusStaysPartialWithoutAuthoritativeIdentity(t *testing.T) {
	perp, _ := lighterCoverageInstruments()
	const (
		restartClient   = "runtime-client-after-restart"
		collisionClient = "client-ym78OtFs7nPp"
		collisionOther  = "client-xljjdgDP7Tnx"
	)
	if clientOrderIndex(collisionClient) != clientOrderIndex(collisionOther) {
		t.Fatal("hard-coded Lighter client-id collision no longer collides")
	}

	for _, tc := range []struct {
		name       string
		queryID    string
		rememberID string
	}{
		{name: "restart loses original client id", queryID: restartClient},
		{name: "hash collision aliases original client id", queryID: collisionClient, rememberID: collisionOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clientIndex := clientOrderIndex(tc.queryID)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/accountActiveOrders" {
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				_, _ = fmt.Fprintf(w, `{"code":200,"orders":[{"order_index":42,"client_order_index":%d,"market_index":%d,"initial_base_amount":"1","remaining_base_amount":"1","price":"100","status":"open","side":"buy"}]}`, clientIndex, *perp.AssetIndex)
			}))
			defer server.Close()
			rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
			rest.BaseURL = server.URL
			exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp}), clock.NewSimulatedClock(time.Unix(400, 0)), 66)
			if tc.rememberID != "" {
				exec.rememberClientIndex(clientIndex, tc.rememberID)
			}

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{ClientID: tc.queryID})
			if err != nil {
				t.Fatalf("GenerateExecutionMassStatus: %v", err)
			}
			coverage := mass.OpenOrdersCoverage
			if coverage.State != model.CoveragePartial || coverage.Scope.AccountID != AccountIDDefault || coverage.Scope.ClientID != tc.queryID || coverage.Scope.Through.IsZero() {
				t.Fatalf("client-scoped coverage=%+v, want fully scoped attempted Partial", coverage)
			}
			if got := coverage.Scope.InstrumentIDs; len(got) != 1 || got[0] != perp.ID {
				t.Fatalf("client-scoped coverage ids=%v, want [%s]", got, perp.ID)
			}
			if len(mass.OrderReports) != 0 {
				t.Fatalf("client-scoped reports=%+v, want no falsely attributed order", mass.OrderReports)
			}
			if !hasLighterWarning(mass.Warnings, "OPEN_ORDERS_CLIENT_ID_UNVERIFIED") {
				t.Fatalf("warnings=%+v, want client-id authority warning", mass.Warnings)
			}
		})
	}
}

func TestLighterMassStatusRejectsUnknownExplicitInstrumentBeforeIO(t *testing.T) {
	perp, _ := lighterCoverageInstruments()
	unknown := perp.ID
	unknown.Symbol += "-UNKNOWN"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":200,"orders":[]}`))
	}))
	defer server.Close()
	rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
	rest.BaseURL = server.URL
	exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp}), clock.NewRealClock(), 66)

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{InstrumentIDs: []model.InstrumentID{unknown}})
	if mass != nil || err == nil {
		t.Fatalf("mass=%+v err=%v, want unknown-selector error", mass, err)
	}
	if calls != 0 {
		t.Fatalf("network calls=%d, want zero before selector rejection", calls)
	}
}

func TestLighterMassStatusRejectsForeignScopeBeforeIO(t *testing.T) {
	perp, _ := lighterCoverageInstruments()
	for _, tc := range []struct {
		name  string
		query model.MassStatusQuery
	}{
		{name: "foreign account", query: model.MassStatusQuery{AccountID: "LIGHTER-OTHER"}},
		{name: "foreign venue", query: model.MassStatusQuery{Venue: "OTHER"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				_, _ = w.Write([]byte(`{"code":200,"orders":[]}`))
			}))
			defer server.Close()
			rest := sdk.NewClient().WithEnvironment(sdk.EnvironmentTestnet).WithCredentials(testLighterPrivateKey(), 66, 7)
			rest.BaseURL = server.URL
			exec := newExecutionClient(rest, newRegistry([]*model.Instrument{perp}), clock.NewRealClock(), 66)

			mass, err := exec.GenerateExecutionMassStatus(context.Background(), tc.query)
			if mass != nil || err == nil {
				t.Fatalf("mass=%+v err=%v, want foreign-scope error", mass, err)
			}
			if calls != 0 {
				t.Fatalf("network calls=%d, want zero before scope rejection", calls)
			}
		})
	}
}

func hasLighterWarning(warnings []model.ReportWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

func lighterCoverageInstruments() (*model.Instrument, *model.Instrument) {
	perpMarket, spotMarket := 1, 2
	perp := &model.Instrument{ID: model.InstrumentID{Venue: venueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}, VenueSymbol: "BTC", AssetIndex: &perpMarket, PriceTick: decimal.NewFromInt(1), SizeStep: decimal.NewFromInt(1)}
	spot := &model.Instrument{ID: model.InstrumentID{Venue: venueName, Symbol: "ETH-USDC", Kind: enums.KindSpot}, VenueSymbol: "ETH/USDC", AssetIndex: &spotMarket, PriceTick: decimal.NewFromInt(1), SizeStep: decimal.NewFromInt(1)}
	return perp, spot
}

func testLighterPrivateKey() string {
	return "01010101010101010101010101010101010101010101010101010101010101010101010101010101"
}
