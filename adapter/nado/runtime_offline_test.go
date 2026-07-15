package nado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	runtimeexec "github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/shopspring/decimal"
)

type nadoRuntimeAccount struct {
	state model.AccountState
}

type nadoIntentFailStore struct {
	journal.Store
	err error
}

type nadoRuntimeRiskProbe struct {
	checks   int
	releases int
}

func (p *nadoRuntimeRiskProbe) CheckSubmission(context.Context, model.OrderRequest, *model.Instrument) (func(), error) {
	p.checks++
	return func() { p.releases++ }, nil
}

func (s *nadoIntentFailStore) AppendCommandIntent(context.Context, journal.CommandIntent) error {
	return s.err
}

func (a *nadoRuntimeAccount) AccountID() string { return a.state.AccountID }
func (a *nadoRuntimeAccount) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{{
			Kind:    enums.KindSpot,
			Account: true,
		}},
		Reports: contract.ReportCapabilities{},
	}
}
func (a *nadoRuntimeAccount) AccountState(context.Context) (model.AccountState, error) {
	return model.CloneAccountState(a.state), nil
}
func (a *nadoRuntimeAccount) Balances(context.Context) ([]model.AccountBalance, error) {
	return append([]model.AccountBalance(nil), a.state.Balances...), nil
}
func (a *nadoRuntimeAccount) Positions(context.Context) ([]model.Position, error) {
	return nil, nil
}
func (a *nadoRuntimeAccount) SetLeverage(context.Context, model.InstrumentID, decimal.Decimal) error {
	return contract.ErrNotSupported
}
func (a *nadoRuntimeAccount) SetMarginMode(context.Context, model.InstrumentID, string) error {
	return contract.ErrNotSupported
}
func (a *nadoRuntimeAccount) Events() <-chan contract.AccountEnvelope { return nil }
func (a *nadoRuntimeAccount) Close() error                            { return nil }

func TestNadoWhitespaceClientIDFailsDuringMandatoryValidation(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC))
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	submitter := newRecordingSubmissionDeps()
	execution.submitter = submitter
	risk := &nadoRuntimeRiskProbe{}
	cached := cache.New()
	store := journal.NewMemory()
	engine := runtimeexec.New(execution, cached, clk, "nado-whitespace").
		WithAccountID(AccountIDUnified).
		WithJournal(store).
		WithRisk(risk, provider)
	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	req.ClientID = "   "

	if _, err := engine.Submit(context.Background(), req); err == nil || !strings.Contains(err.Error(), "client id is required") {
		t.Fatalf("Submit err=%v, want mandatory validation rejection", err)
	}
	if risk.checks != 0 || risk.releases != 0 {
		t.Fatalf("risk checks/releases=%d/%d, want 0/0", risk.checks, risk.releases)
	}
	if len(store.Records()) != 0 || engine.InFlightCount() != 0 {
		t.Fatalf("validation rejection durable state records=%d inflight=%d", len(store.Records()), engine.InFlightCount())
	}
	if _, ok := cached.OrderByClientIDForAccount(AccountIDUnified, req.ClientID); ok {
		t.Fatal("validation rejection produced cached PendingNew")
	}
	calls, prepareCalls, executeCalls := submitter.snapshot()
	if len(calls) != 0 || prepareCalls != 0 || executeCalls != 0 {
		t.Fatalf("submission backend calls=%v prepare=%d execute=%d, want none", calls, prepareCalls, executeCalls)
	}
}

func TestNadoRuntimeUsesOrdinarySubmitWithoutFabricatedFreeBalance(t *testing.T) {
	now := time.Now().UTC()
	clk := clock.NewSimulatedClock(now)
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	submitter := newRecordingSubmissionDeps()
	execution.submitter = submitter
	account := &nadoRuntimeAccount{state: model.AccountState{
		AccountID:    AccountIDUnified,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USDT0",
		Balances: []model.AccountBalance{{
			AccountID: AccountIDUnified,
			Currency:  "USDT0",
			Total:     decimal.NewFromInt(1000),
			UpdatedAt: now,
		}},
		Summary: &model.AccountSummary{
			SettlementCurrency:  "USDT0",
			Equity:              decimal.NewFromInt(1000),
			AvailableCollateral: decimal.NewFromInt(900),
			UpdatedAt:           now,
		},
		Reported: true,
		EventID:  model.AccountStateEventID(VenueName, AccountIDUnified, now),
		TsEvent:  now,
		TsInit:   now,
	}}
	node := runtime.NewNode(
		runtime.Clients{Execution: execution, Account: account},
		clk,
		"nado-runtime",
		runtime.WithAccountID(AccountIDUnified),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, provider, decimal.NewFromInt(1_000_000))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitNadoNodeRunning(t, node)

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	order, err := node.Exec.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("runtime submit: %v", err)
	}
	if order == nil || order.Request.AccountID != AccountIDUnified || order.VenueOrderID == "" {
		t.Fatalf("runtime order=%+v", order)
	}
	submitter.mu.Lock()
	calls := append([]string(nil), submitter.calls...)
	submitter.mu.Unlock()
	wantCalls := []string{"prepare", "execute"}
	if len(calls) != len(wantCalls) {
		t.Fatalf("submission calls=%v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Fatalf("submission calls=%v, want %v", calls, wantCalls)
		}
	}
	if bal, ok := node.Cache.BalanceForAccount(AccountIDUnified, "USDT0"); !ok || !bal.Free.IsZero() {
		t.Fatalf("runtime fabricated currency free balance: %+v ok=%v", bal, ok)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
}

func TestNadoRuntimeJournalFailureStopsBeforeAdapterPreparationOrVenueWrite(t *testing.T) {
	now := time.Now().UTC()
	clk := clock.NewSimulatedClock(now)
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	submitter := newRecordingSubmissionDeps()
	execution.submitter = submitter
	account := &nadoRuntimeAccount{state: model.AccountState{
		AccountID:    AccountIDUnified,
		Venue:        VenueName,
		Type:         model.AccountMargin,
		BaseCurrency: "USDT0",
		Balances: []model.AccountBalance{{
			AccountID: AccountIDUnified,
			Currency:  "USDT0",
			Total:     decimal.NewFromInt(1000),
			UpdatedAt: now,
		}},
		Reported: true,
		EventID:  model.AccountStateEventID(VenueName, AccountIDUnified, now),
		TsEvent:  now,
		TsInit:   now,
	}}
	fail := errors.New("intent write failed")
	store := &nadoIntentFailStore{Store: journal.NewMemory(), err: fail}
	node := runtime.NewNode(
		runtime.Clients{Execution: execution, Account: account},
		clk,
		"nado-runtime-fail",
		runtime.WithAccountID(AccountIDUnified),
		runtime.WithJournal(store),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitNadoNodeRunning(t, node)

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	req.ClientID = "journal-failure"
	if _, err := node.Exec.Submit(context.Background(), req); !errors.Is(err, fail) {
		t.Fatalf("submit err=%v, want %v", err, fail)
	}
	submitter.mu.Lock()
	calls := append([]string(nil), submitter.calls...)
	submitter.mu.Unlock()
	if len(calls) != 0 {
		t.Fatalf("adapter called before durable intent succeeded: %v", calls)
	}
	if _, ok := node.Cache.Order(req.ClientID); ok {
		t.Fatal("journal-rejected order entered cache")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
}

func TestNadoRuntimeWithoutOpenOrderBackendStaysRestricted(t *testing.T) {
	execution := newExecutionClient(nil, nadoTestProvider(), clock.NewRealClock(), enums.KindSpot, AccountIDUnified)
	execution.submitter = newRecordingSubmissionDeps()
	node := runtime.NewNode(
		runtime.Clients{Execution: execution},
		nil,
		"nado-runtime-incomplete",
		runtime.WithAccountID(AccountIDUnified),
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := node.State()
		if state.Node == lifecycle.NodeRunning && state.Trading == lifecycle.TradingReconciling {
			if state.Reason != "reconciliation open-order evidence is incomplete" {
				t.Fatalf("restricted reason=%q", state.Reason)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("node did not fail closed without open-order evidence: %+v", node.State())
}

func newNadoRuntimeExecution(t *testing.T, provider *instrumentProvider, clk clock.Clock) *executionClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request["type"] == "subaccount_orders" {
			sender, _ := request["sender"].(string)
			productID, _ := request["product_id"].(float64)
			_, _ = fmt.Fprintf(w, `{"status":"success","data":{"sender":%q,"product_id":%s,"orders":[]}}`, sender, decimal.NewFromFloat(productID).String())
			return
		}
		if _, ok := request["matches"]; ok {
			_, _ = w.Write([]byte(`{"matches":[],"txs":[]}`))
			return
		}
		http.Error(w, "unexpected Nado runtime fixture request", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)

	rest := nadoTestRESTClient(t, server)
	var err error
	rest, err = rest.WithCredentials("1111111111111111111111111111111111111111111111111111111111111111", "runtime")
	if err != nil {
		t.Fatalf("runtime REST credentials: %v", err)
	}
	execution := newExecutionClient(rest, provider, clk, enums.KindSpot, AccountIDUnified)
	if execution.reports == nil {
		t.Fatal("runtime execution fixture has no authoritative report backend")
	}
	return execution
}

func waitNadoNodeRunning(t *testing.T, node *runtime.TradingNode) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state := node.State(); state.Node == lifecycle.NodeRunning && state.Trading == lifecycle.TradingActive {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("node did not become ready: %+v", node.State())
}
