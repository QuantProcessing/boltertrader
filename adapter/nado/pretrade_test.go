package nado

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
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

func TestNadoPreTradeChecksCapacityBeforePreparedLeaseAndSubmitConsumesOnce(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	deps := &recordingPreTradeDeps{
		sender:     "sender-1",
		maxSizeX18: "5000000000000000000",
		prepared:   preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-1"),
		executed:   &sdk.PlaceOrderResponse{Digest: "digest-1"},
	}
	exec.pretrade = deps

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err != nil {
		t.Fatalf("ValidatePreTrade: %v", err)
	}
	if lease == nil {
		t.Fatal("ValidatePreTrade returned nil lease")
	}
	if got, want := deps.calls, []string{"sender", "max", "prepare"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("call order=%v, want %v", got, want)
	}
	if deps.maxReq == nil || deps.maxReq.SpotLeverage == nil || *deps.maxReq.SpotLeverage {
		t.Fatalf("spot max_order_size must set spot_leverage=false: %+v", deps.maxReq)
	}
	if !exec.Capabilities().Trading.Submit {
		t.Fatal("Submit capability must be true when pretrade executor is configured")
	}

	order, err := exec.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if order.VenueOrderID != "digest-1" || order.Status != enums.StatusNew || order.Request.ClientID != req.ClientID {
		t.Fatalf("submitted order mismatch: %+v", order)
	}
	if got, want := deps.calls, []string{"sender", "max", "prepare", "execute"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("call order after submit=%v, want %v", got, want)
	}
	if _, err := exec.Submit(context.Background(), req); err == nil || !strings.Contains(err.Error(), "prepared") {
		t.Fatalf("reused prepared state must fail closed, err=%v", err)
	}
	lease.Release()
	lease.Release()
}

func TestNadoPreTradeRejectsBeforePrepareAndExecute(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	deps := &recordingPreTradeDeps{
		sender:     "sender-1",
		maxSizeX18: "100000000000000000",
		prepared:   preparedOrderForTest(2, "1000000000000000000", "2000000000000000000", "digest-2"),
	}
	exec.pretrade = deps

	req := nadoTestOrderRequest(enums.KindPerp, enums.SideBuy)
	_, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err == nil || !strings.Contains(err.Error(), "max_order_size") {
		t.Fatalf("capacity rejection err=%v", err)
	}
	if got, want := deps.calls, []string{"sender", "max"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("call order=%v, want %v", got, want)
	}
	if _, err := exec.Submit(context.Background(), req); err == nil {
		t.Fatal("Submit without prepared state must fail closed")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	deps.calls = nil
	_, err = exec.ValidatePreTrade(cancelled, req, mustNadoInstrument(t, exec, req.InstrumentID))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ValidatePreTrade err=%v, want context.Canceled", err)
	}
	if len(deps.calls) != 0 {
		t.Fatalf("cancelled context must not call transport: %v", deps.calls)
	}
}

func TestNadoPreTradeUsesDiscoveredIsolatedOnlyMode(t *testing.T) {
	symbols := nadoTestSymbols()
	perp := symbols.Symbols["BTC_USDT0-PERP"]
	perp.IsolatedOnly = true
	symbols.Symbols["BTC_USDT0-PERP"] = perp
	provider, err := newInstrumentProviderFromDiscovery(nadoTestProducts(), symbols, []enums.InstrumentKind{enums.KindPerp})
	if err != nil {
		t.Fatalf("newInstrumentProviderFromDiscovery: %v", err)
	}
	exec := newExecutionClient(nil, provider, clock.NewRealClock(), enums.KindPerp, AccountIDUnified)
	deps := newRecordingPreTradeDeps()
	exec.pretrade = deps
	req := nadoTestOrderRequest(enums.KindPerp, enums.SideBuy)
	lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err != nil {
		t.Fatalf("ValidatePreTrade: %v", err)
	}
	defer lease.Release()
	if deps.maxReq.Isolated == nil || !*deps.maxReq.Isolated {
		t.Fatalf("max_order_size isolated=%v, want true", deps.maxReq.Isolated)
	}
	if !deps.input.Isolated {
		t.Fatal("prepared order did not use discovered isolated-only mode")
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

func TestNadoPreTradeCancellationStagesStopBeforeLaterVenueCalls(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC))
	req := nadoTestOrderRequest(enums.KindPerp, enums.SideBuy)

	t.Run("after max size", func(t *testing.T) {
		exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
		ctx, cancel := context.WithCancel(context.Background())
		deps := &recordingPreTradeDeps{
			sender:     "sender-1",
			maxSizeX18: "5000000000000000000",
			prepared:   preparedOrderForTest(2, "1000000000000000000", "2000000000000000000", "digest-cancel-max"),
			onMax:      cancel,
		}
		exec.pretrade = deps

		_, err := exec.ValidatePreTrade(ctx, req, mustNadoInstrument(t, exec, req.InstrumentID))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ValidatePreTrade err=%v, want context.Canceled", err)
		}
		if got, want := deps.calls, []string{"sender", "max"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("call order=%v, want %v", got, want)
		}
		if got := exec.preparedLen(); got != 0 {
			t.Fatalf("prepared entries after max cancel=%d, want 0", got)
		}
	})

	t.Run("after prepare", func(t *testing.T) {
		exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
		ctx, cancel := context.WithCancel(context.Background())
		deps := &recordingPreTradeDeps{
			sender:     "sender-1",
			maxSizeX18: "5000000000000000000",
			prepared:   preparedOrderForTest(2, "1000000000000000000", "2000000000000000000", "digest-cancel-prepare"),
			onPrepare:  cancel,
		}
		exec.pretrade = deps

		_, err := exec.ValidatePreTrade(ctx, req, mustNadoInstrument(t, exec, req.InstrumentID))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ValidatePreTrade err=%v, want context.Canceled", err)
		}
		if got, want := deps.calls, []string{"sender", "max", "prepare"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("call order=%v, want %v", got, want)
		}
		if got := exec.preparedLen(); got != 0 {
			t.Fatalf("prepared entries after prepare cancel=%d, want 0", got)
		}
		if deps.prepared.Signature != "" || deps.prepared.EncodedOrder != "" || deps.prepared.Request != nil {
			t.Fatalf("cancelled prepared payload was not redacted: %+v", deps.prepared)
		}
	})
}

func TestNadoPreparedLeaseReleaseIsConcurrentSafeAndRemovesPayload(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 10, 4, 0, 0, 0, time.UTC))
	exec := newExecutionClient(nil, nadoTestProvider(), clk, enums.KindSpot, AccountIDUnified)
	deps := &recordingPreTradeDeps{
		sender:     "sender-1",
		maxSizeX18: "5000000000000000000",
		prepared:   preparedOrderForTest(1, "1000000000000000000", "2000000000000000000", "digest-3"),
	}
	exec.pretrade = deps

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	lease, err := exec.ValidatePreTrade(context.Background(), req, mustNadoInstrument(t, exec, req.InstrumentID))
	if err != nil {
		t.Fatalf("ValidatePreTrade: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease.Release()
		}()
	}
	wg.Wait()

	if _, err := exec.Submit(context.Background(), req); err == nil || !strings.Contains(err.Error(), "prepared") {
		t.Fatalf("released prepared state must fail closed, err=%v", err)
	}
	if got := exec.preparedLen(); got != 0 {
		t.Fatalf("prepared cache len=%d, want 0", got)
	}
	if deps.prepared.Signature != "" || deps.prepared.EncodedOrder != "" || deps.prepared.Request != nil {
		t.Fatalf("removed prepared payload was not redacted: %+v", deps.prepared)
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
	exec := newExecutionClient(rest, nadoTestProvider(), clk, enums.KindPerp, AccountIDUnified)
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified})
	if err != nil {
		t.Fatalf("GenerateExecutionMassStatus: %v", err)
	}
	if mass.Partial {
		t.Fatalf("complete open-order enumeration must be non-partial: %+v", mass)
	}
	if len(mass.OrderReports) != 0 {
		t.Fatalf("missed cancellation repair expects missing local open, got venue reports: %+v", mass.OrderReports)
	}
	if calls[2] != 1 {
		t.Fatalf("scoped perp product was not enumerated exactly once: %+v", calls)
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
	mass, err := exec.GenerateExecutionMassStatus(context.Background(), model.MassStatusQuery{AccountID: AccountIDUnified, IncludeFills: true})
	if err != nil {
		t.Fatalf("mass status should mark partial instead of failing ambiguous fills: %v", err)
	}
	found := false
	for _, warning := range mass.Warnings {
		if warning.Code == "FILL_REPORTS_PARTIAL" {
			found = true
		}
	}
	if !found || !mass.Partial {
		t.Fatalf("mass status did not mark partial ambiguous fills: %+v", mass)
	}
}

var _ contract.VenuePreTradeValidator = (*executionClient)(nil)
var _ contract.PreparedExecutionClient = (*executionClient)(nil)

type recordingPreTradeDeps struct {
	mu           sync.Mutex
	calls        []string
	sender       string
	maxReq       *sdk.MaxOrderSizeRequest
	input        sdk.ClientOrderInput
	maxSizeX18   string
	prepared     *sdk.PreparedOrder
	executed     *sdk.PlaceOrderResponse
	executeErr   error
	prepareCalls int
	onPrepare    func()
	blockPrepare chan struct{}
	onMax        func()
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

func (d *recordingPreTradeDeps) Sender() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "sender")
	return d.sender, nil
}

func (d *recordingPreTradeDeps) GetMaxOrderSize(ctx context.Context, req sdk.MaxOrderSizeRequest) (*sdk.MaxOrderSizeResponse, error) {
	d.mu.Lock()
	d.calls = append(d.calls, "max")
	cp := req
	d.maxReq = &cp
	onMax := d.onMax
	d.mu.Unlock()
	if onMax != nil {
		onMax()
	}
	return &sdk.MaxOrderSizeResponse{MaxOrderSize: d.maxSizeX18}, nil
}

func (d *recordingPreTradeDeps) PrepareOrder(ctx context.Context, input sdk.ClientOrderInput) (*sdk.PreparedOrder, error) {
	d.mu.Lock()
	d.calls = append(d.calls, "prepare")
	d.input = input
	d.prepareCalls++
	onPrepare := d.onPrepare
	prepared := d.prepared
	d.mu.Unlock()
	if onPrepare != nil {
		onPrepare()
	}
	return prepared, nil
}

func (d *recordingPreTradeDeps) ExecutePreparedOrder(ctx context.Context, order *sdk.PreparedOrder) (*sdk.PlaceOrderResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, "execute")
	return d.executed, d.executeErr
}

func nadoTestOrderRequest(kind enums.InstrumentKind, side enums.OrderSide) model.OrderRequest {
	symbol := "ETH-USDT0"
	if kind == enums.KindPerp {
		symbol = "BTC-USDT0"
	}
	return model.OrderRequest{
		AccountID:    AccountIDUnified,
		InstrumentID: model.InstrumentID{Venue: VenueName, Symbol: symbol, Kind: kind},
		ClientID:     "client-pretrade-1",
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
