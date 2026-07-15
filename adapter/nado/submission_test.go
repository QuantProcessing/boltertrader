package nado

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoSubmitUsesDiscoveredIsolatedOnlyMode(t *testing.T) {
	symbols := nadoTestSymbols()
	perp := symbols.Symbols["BTC_USDT0-PERP"]
	perp.IsolatedOnly = true
	symbols.Symbols["BTC_USDT0-PERP"] = perp
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), symbols, []enums.InstrumentKind{enums.KindPerp})
	if err != nil {
		t.Fatalf("newInstrumentProviderFromDiscovery: %v", err)
	}
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindPerp, AccountIDUnified)
	deps := newRecordingSubmissionDeps()
	exec.submitter = deps
	req := nadoTestOrderRequest(enums.KindPerp, enums.SideBuy)
	if _, err := exec.Submit(context.Background(), req); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if !deps.input.Isolated {
		t.Fatal("ordinary submit did not use discovered isolated-only mode")
	}
	if deps.input.IsolatedMargin != 2 {
		t.Fatalf("isolated margin=%v, want 1x notional 2", deps.input.IsolatedMargin)
	}
	if deps.input.IsolatedMarginX6 == nil || deps.input.IsolatedMarginX6.String() != "2000000" {
		t.Fatalf("isolated margin x6=%v, want 2000000", deps.input.IsolatedMarginX6)
	}
	reduceOnly := req
	reduceOnly.ReduceOnly = true
	input, err := exec.orderInput(reduceOnly, mustNadoInstrument(t, exec, req.InstrumentID), 2)
	if err != nil {
		t.Fatalf("reduce-only orderInput: %v", err)
	}
	if !input.Isolated || input.IsolatedMargin != 0 {
		t.Fatalf("reduce-only isolated input=%+v, want isolated with zero added margin", input)
	}
}

func TestNadoExecutionReportsUseArchiveMatchesAndAccountSnapshot(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	reports := &recordingReportDeps{
		sender: "sender-1",
		matches: &sdk.ArchiveMatchesResponse{Matches: []sdk.Match{{
			Digest:        "digest-fill",
			BaseFilled:    "2000000000000000000",
			Fee:           "1000000000000000",
			SubmissionIdx: "42",
			Timestamp:     "2026-07-10T05:00:00Z",
			Order: sdk.MatchOrder{
				PriceX18: "3000000000000000000",
				Amount:   "2000000000000000000",
			},
		}}, Txs: []sdk.Tx{{SubmissionIdx: "42", TxInfo: sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 2}}}}},
		snapshot: &sdk.AccountSnapshot{Account: sdk.AccountInfo{
			Exists: true,
			PerpBalances: []sdk.Balance{{
				ProductID: 2,
				Balance: struct {
					Amount                string  `json:"amount"`
					VQuoteBalance         *string `json:"v_quote_balance,omitempty"`
					LastCumulativeFunding *string `json:"last_cumulative_funding_x18,omitempty"`
				}{Amount: "1500000000000000000"},
			}},
		}, ReceivedAt: clk.Now()},
	}
	exec.reports = reports

	fillReports, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp},
		AccountID:    AccountIDUnified,
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fillReports) != 1 || fillReports[0].Fill.VenueOrderID != "digest-fill" || fillReports[0].Fill.Side != enums.SideBuy || !fillReports[0].Fill.Quantity.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("fill reports mismatch: %+v", fillReports)
	}

	positionReports, err := exec.GeneratePositionReports(context.Background(), model.PositionReportQuery{
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp},
		AccountID:    AccountIDUnified,
	})
	if err != nil {
		t.Fatalf("GeneratePositionReports: %v", err)
	}
	if len(positionReports) != 1 || !positionReports[0].Position.Quantity.Equal(decimal.RequireFromString("1.5")) {
		t.Fatalf("position reports mismatch: %+v", positionReports)
	}
	if !exec.Capabilities().Reports.FillHistory || !exec.Capabilities().Reports.PositionReports {
		t.Fatalf("report capabilities must be true when report backend is configured: %+v", exec.Capabilities().Reports)
	}

	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{
		AccountID:        AccountIDUnified,
		IncludeFills:     true,
		IncludePositions: true,
	})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.FillReports) == 0 || len(mass.PositionReports) == 0 {
		t.Fatalf("mass status did not include fills/positions: %+v", mass)
	}
	for _, warning := range mass.Warnings {
		if warning.Code == "NADO_STORY5_FOUNDATION" {
			t.Fatalf("mass status still reports unsupported fills/positions: %+v", mass.Warnings)
		}
	}
}

func TestNadoReportsPreserveRebatesAndScopePositionReports(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC))
	reports := &recordingReportDeps{
		sender: "sender-1",
		matches: &sdk.ArchiveMatchesResponse{Matches: []sdk.Match{{
			Digest:        "digest-rebate",
			BaseFilled:    "1000000000000000000",
			Fee:           "-200000000000000",
			SubmissionIdx: "77",
			Timestamp:     "2026-07-10T05:00:00Z",
			Order:         sdk.MatchOrder{PriceX18: "3000000000000000000", Amount: "1000000000000000000"},
		}}, Txs: []sdk.Tx{{SubmissionIdx: "77", TxInfo: sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 2}}}}},
		snapshot: &sdk.AccountSnapshot{Account: sdk.AccountInfo{Exists: true, PerpBalances: []sdk.Balance{{
			ProductID: 2,
			Balance: struct {
				Amount                string  `json:"amount"`
				VQuoteBalance         *string `json:"v_quote_balance,omitempty"`
				LastCumulativeFunding *string `json:"last_cumulative_funding_x18,omitempty"`
			}{Amount: "1000000000000000000"},
		}}}, ReceivedAt: clk.Now()},
	}
	perpExec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	perpExec.reports = reports
	fills, err := perpExec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, Limit: 10})
	if err != nil {
		t.Fatalf("GenerateFillReports: %v", err)
	}
	if len(fills) != 1 || !fills[0].Fill.Fee.Equal(decimal.RequireFromString("-0.0002")) {
		t.Fatalf("rebate fee was not preserved: %+v", fills)
	}
	if !perpExec.Capabilities().Reports.PositionReports {
		t.Fatalf("perp execution must advertise position reports when backend configured: %+v", perpExec.Capabilities().Reports)
	}

	spotExec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	spotExec.reports = reports
	if spotExec.Capabilities().Reports.PositionReports {
		t.Fatalf("spot execution must not advertise perp position reports: %+v", spotExec.Capabilities().Reports)
	}
	positions, err := spotExec.GeneratePositionReports(context.Background(), model.PositionReportQuery{AccountID: AccountIDUnified})
	if !errors.Is(err, contract.ErrNotSupported) {
		t.Fatalf("spot GeneratePositionReports err=%v, want ErrNotSupported", err)
	}
	if positions != nil {
		t.Fatalf("spot execution returned positions: %+v", positions)
	}
}

func TestNadoMassStatusCompleteOpenEnumerationRepairsMissedCancellation(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC))
	var sender string
	calls := make(map[int64]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["type"] != "subaccount_orders" {
			t.Fatalf("unexpected query: %#v", req)
		}
		if req["sender"] != sender {
			t.Fatalf("sender mismatch: %#v", req)
		}
		productID := int64(req["product_id"].(float64))
		calls[productID]++
		_, _ = w.Write([]byte(`{"status":"success","data":{"sender":"` + sender + `","product_id":` + decimal.NewFromInt(productID).String() + `,"orders":[]}}`))
	}))
	defer server.Close()

	rest := nadoTestRESTClient(t, server)
	rest, err := rest.WithCredentials("1111111111111111111111111111111111111111111111111111111111111111", "arb")
	if err != nil {
		t.Fatal(err)
	}
	sender, err = rest.Sender()
	if err != nil {
		t.Fatal(err)
	}
	provider := nadoTestProvider()
	exec := newExecutionClient(rest, provider, clk, enums.KindPerp, AccountIDUnified)
	query := model.MassStatusQuery{AccountID: AccountIDUnified}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if len(mass.OrderReports) != 0 {
		t.Fatalf("missed cancellation repair expects missing local open, got venue reports: %+v", mass.OrderReports)
	}
	if calls[2] != 1 {
		t.Fatalf("scoped perp product was not enumerated exactly once: %+v", calls)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("typed coverage: %v", err)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageComplete || mass.FillsCoverage.State != model.CoverageNotRequested || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("coverage=%+v/%+v/%+v", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage)
	}
	if !mass.OpenOrdersCoverage.Scope.Through.Equal(clk.Now()) || len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 1 {
		t.Fatalf("watermark/selector=%s/%v", mass.OpenOrdersCoverage.Scope.Through, mass.OpenOrdersCoverage.Scope.InstrumentIDs)
	}
	frozenID := mass.OpenOrdersCoverage.Scope.InstrumentIDs[0]
	provider.mu.Lock()
	provider.byID = map[string]*model.Instrument{}
	provider.byProductID = map[int64]model.InstrumentID{}
	provider.productIDByInstrument = map[string]int64{}
	provider.all = nil
	provider.mu.Unlock()
	if len(mass.OpenOrdersCoverage.Scope.InstrumentIDs) != 1 || mass.OpenOrdersCoverage.Scope.InstrumentIDs[0] != frozenID {
		t.Fatalf("provider mutation changed response selector: %v", mass.OpenOrdersCoverage.Scope.InstrumentIDs)
	}
	foundOpenOnly := false
	for _, warning := range mass.Warnings {
		if warning.Code == "OPEN_ORDERS_ONLY" {
			foundOpenOnly = true
		}
		if warning.Code == "OPEN_ORDERS_UNAVAILABLE" || warning.Code == "OPEN_ORDERS_PARTIAL" {
			t.Fatalf("complete enumeration emitted incomplete-scope warning: %+v", mass.Warnings)
		}
	}
	if !foundOpenOnly {
		t.Fatalf("open-only ambiguity warning missing: %+v", mass.Warnings)
	}
}

func TestNadoFillReportsResolveTxProductAndRejectAmbiguousMatches(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 5, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	reports := &recordingReportDeps{
		sender: "sender-1",
		matches: &sdk.ArchiveMatchesResponse{
			Matches: []sdk.Match{{
				Digest: "digest-tx", BaseFilled: "-1000000000000000000", Fee: "0", SubmissionIdx: "99",
				Order: sdk.MatchOrder{PriceX18: "3000000000000000000", Amount: "-1000000000000000000"},
			}},
			Txs: []sdk.Tx{{SubmissionIdx: "99", Timestamp: "1783659600", TxInfo: sdk.TxInfo{MatchOrders: sdk.MatchOrders{ProductId: 2}}}},
		},
	}
	exec.reports = reports
	got, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, Limit: 10})
	if err != nil {
		t.Fatalf("GenerateFillReports tx metadata: %v", err)
	}
	if len(got) != 1 || got[0].Fill.InstrumentID.Symbol != "BTC-USDT0" || got[0].Fill.Timestamp.Unix() != 1783659600 || got[0].Fill.Side != enums.SideSell || !got[0].Fill.Quantity.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("tx metadata product mapping mismatch: %+v", got)
	}

	reports.matches = &sdk.ArchiveMatchesResponse{Matches: []sdk.Match{{
		Digest: "ambiguous", BaseFilled: "1000000000000000000", Fee: "0", SubmissionIdx: "100", Timestamp: "2026-07-10T05:00:00Z",
		Order: sdk.MatchOrder{PriceX18: "3000000000000000000", Amount: "1000000000000000000"},
	}}}
	if _, err := exec.GenerateFillReports(context.Background(), model.FillReportQuery{AccountID: AccountIDUnified, Limit: 10}); err == nil {
		t.Fatal("ambiguous match was silently skipped")
	}
	query := model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true}
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), query)
	if err != nil {
		t.Fatalf("mass status should mark partial instead of failing ambiguous fills: %v", err)
	}
	found := false
	for _, warning := range mass.Warnings {
		if warning.Code == "FILL_REPORTS_PARTIAL" {
			found = true
		}
	}
	if !found {
		t.Fatalf("mass status did not diagnose unavailable ambiguous fills: %+v", mass)
	}
	if mass.OpenOrdersCoverage.State != model.CoverageUnavailable || mass.FillsCoverage.State != model.CoverageUnavailable || mass.PositionsCoverage.State != model.CoverageNotRequested {
		t.Fatalf("coverage=%+v/%+v/%+v, want Unavailable/Unavailable/NotRequested", mass.OpenOrdersCoverage, mass.FillsCoverage, mass.PositionsCoverage)
	}
	if !mass.OpenOrdersCoverage.Scope.IsZero() || !mass.PositionsCoverage.Scope.IsZero() {
		t.Fatalf("unattempted/not-requested scopes open=%+v positions=%+v, want zero", mass.OpenOrdersCoverage.Scope, mass.PositionsCoverage.Scope)
	}
	wantID := model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp}
	if fills := mass.FillsCoverage.Scope; fills.AccountID != AccountIDUnified || fills.ClientID != "" || len(fills.InstrumentIDs) != 1 || fills.InstrumentIDs[0] != wantID || !fills.From.IsZero() || !fills.Through.Equal(clk.Now()) {
		t.Fatalf("fill coverage scope=%+v, want exact attempted history scope", fills)
	}
	if err := mass.ValidateFor(query); err != nil {
		t.Fatalf("ValidateFor: %v", err)
	}
}

type recordingSubmissionDeps struct {
	mu           sync.Mutex
	calls        []string
	input        sdk.ClientOrderInput
	prepared     *sdk.PreparedOrder
	prepareErr   error
	prepareFn    func(int, sdk.ClientOrderInput) (*sdk.PreparedOrder, error)
	executed     *sdk.PlaceOrderResponse
	executeErr   error
	executeFn    func(*sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error)
	prepareCalls int
	executeCalls int
	onPrepare    func()
	onExecute    func(*sdk.PreparedOrder)
}

type recordingReportDeps struct {
	sender   string
	matches  *sdk.ArchiveMatchesResponse
	snapshot *sdk.AccountSnapshot
}

func nadoTestRESTClient(t *testing.T, server *httptest.Server) *sdk.Client {
	t.Helper()
	profile, err := sdk.NewProfile(sdk.EnvironmentTestnet)
	if err != nil {
		t.Fatal(err)
	}
	client, err := sdk.NewClient(profile)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		clone := request.Clone(request.Context())
		clone.URL.Scheme = target.Scheme
		clone.URL.Host = target.Host
		clone.Host = target.Host
		return transport.RoundTrip(clone)
	})})
	return client
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (d *recordingReportDeps) Sender() (string, error) {
	return d.sender, nil
}

func (d *recordingReportDeps) GetMatches(ctx context.Context, subaccount string, productIDs []int64, limit int) (*sdk.ArchiveMatchesResponse, error) {
	return d.matches, nil
}

func (d *recordingReportDeps) GetAccountSnapshot(ctx context.Context) (*sdk.AccountSnapshot, error) {
	return d.snapshot, nil
}

func (d *recordingSubmissionDeps) PrepareOrder(ctx context.Context, input sdk.ClientOrderInput) (*sdk.PreparedOrder, error) {
	d.mu.Lock()
	d.calls = append(d.calls, "prepare")
	d.input = input
	d.prepareCalls++
	call := d.prepareCalls
	onPrepare := d.onPrepare
	prepared := d.prepared
	prepareErr := d.prepareErr
	prepareFn := d.prepareFn
	d.mu.Unlock()
	if onPrepare != nil {
		onPrepare()
	}
	if prepareFn != nil {
		return prepareFn(call, input)
	}
	return prepared, prepareErr
}

func (d *recordingSubmissionDeps) ExecutePreparedOrder(ctx context.Context, order *sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error) {
	d.mu.Lock()
	d.calls = append(d.calls, "execute")
	d.executeCalls++
	onExecute := d.onExecute
	executed := d.executed
	executeErr := d.executeErr
	executeFn := d.executeFn
	d.mu.Unlock()
	if onExecute != nil {
		onExecute(order)
	}
	if executeFn != nil {
		return executeFn(order)
	}
	return executed, executeErr
}

func (d *recordingSubmissionDeps) snapshot() (calls []string, prepareCalls, executeCalls int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...), d.prepareCalls, d.executeCalls
}

func newRecordingSubmissionDeps() *recordingSubmissionDeps {
	return &recordingSubmissionDeps{
		prepared: preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-safe"),
		executed: &sdk.PlaceOrderResponse{Digest: "digest-safe"},
	}
}

func nadoTestOrderRequest(kind enums.InstrumentKind, side enums.OrderSide) model.OrderRequest {
	symbol := "ETH-USDT0"
	if kind == enums.KindPerp {
		symbol = "BTC-USDT0"
	}
	return model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: symbol, Kind: kind},
		ClientID:     "client-submit-1",
		Side:         side,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("2"),
		Price:        decimal.RequireFromString("1"),
		PositionSide: enums.PosNet,
	}
}

func mustNadoInstrument(t *testing.T, exec *executionClient, id model.InstrumentID) *model.Instrument {
	t.Helper()
	inst, _, err := exec.instrument(id)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func preparedOrderForTest(productID int64, priceX18, amountX18, digest string) *sdk.PreparedOrder {
	return &sdk.PreparedOrder{
		Tx: sdk.TxOrder{
			ProductId:  uint32(productID),
			PriceX18:   priceX18,
			Amount:     amountX18,
			Nonce:      "1",
			Expiration: "4000000000",
			Appendix:   "1",
		},
		Signature:    "sig-" + digest,
		Digest:       digest,
		EncodedOrder: "encoded-" + digest,
		Request:      map[string]interface{}{"place_order": map[string]interface{}{"product_id": productID}},
	}
}
