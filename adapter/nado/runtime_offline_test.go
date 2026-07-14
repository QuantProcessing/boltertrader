package nado

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	runtimeexec "github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/shopspring/decimal"
)

type nadoRuntimeAccount struct {
	state model.AccountState
}

type nadoIntentFailStore struct {
	journal.Store
	err error
}

type nadoAdvancingIntentStore struct {
	journal.Store
	clk     *clock.SimulatedClock
	advance time.Duration
}

func (s *nadoIntentFailStore) AppendCommandIntent(context.Context, journal.CommandIntent) error {
	return s.err
}

func (s *nadoAdvancingIntentStore) AppendCommandIntent(ctx context.Context, intent journal.CommandIntent) error {
	if err := s.Store.AppendCommandIntent(ctx, intent); err != nil {
		return err
	}
	s.clk.Advance(s.advance)
	return nil
}

func (a *nadoRuntimeAccount) AccountID() string { return a.state.AccountID }
func (a *nadoRuntimeAccount) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue: VenueName,
		Products: []contract.ProductCapability{{
			Kind:    enums.KindSpot,
			Account: true,
		}},
		Reports: contract.ReportCapabilities{AccountStateSnapshots: true},
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

func TestNadoRuntimeUsesVenuePreTradeWithoutFabricatedFreeBalance(t *testing.T) {
	now := time.Now().UTC()
	clk := clock.NewSimulatedClock(now)
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	pretrade := newRecordingPreTradeDeps()
	execution.pretrade = pretrade
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
	riskEngine := risk.New(risk.Limits{}, node.Cache).
		WithClock(func() time.Time { return clk.Now() }).
		RequireAccountState()
	runtime.WithRisk(riskEngine, provider)(node)

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
	if execution.preparedLen() != 0 {
		t.Fatalf("prepared entries=%d, want 0 after submit", execution.preparedLen())
	}
	pretrade.mu.Lock()
	calls := append([]string(nil), pretrade.calls...)
	pretrade.mu.Unlock()
	wantCalls := []string{"sender", "max", "prepare", "execute"}
	if len(calls) != len(wantCalls) {
		t.Fatalf("pretrade calls=%v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Fatalf("pretrade calls=%v, want %v", calls, wantCalls)
		}
	}
	if bal, ok := node.Cache.BalanceForAccount(AccountIDUnified, "USDT0"); !ok || bal.Free.IsPositive() || bal.Available.IsPositive() {
		t.Fatalf("runtime fabricated currency availability: %+v ok=%v", bal, ok)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
}

func TestNadoRuntimeJournalFailureReleasesPreparedPayloadBeforeVenueWrite(t *testing.T) {
	now := time.Now().UTC()
	clk := clock.NewSimulatedClock(now)
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	pretrade := newRecordingPreTradeDeps()
	execution.pretrade = pretrade
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
	riskEngine := risk.New(risk.Limits{}, node.Cache).
		WithClock(func() time.Time { return clk.Now() }).
		RequireAccountState()
	runtime.WithRisk(riskEngine, provider)(node)

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
	if execution.preparedLen() != 0 {
		t.Fatalf("prepared entries=%d, want 0 after journal failure", execution.preparedLen())
	}
	pretrade.mu.Lock()
	calls := append([]string(nil), pretrade.calls...)
	pretrade.mu.Unlock()
	for _, call := range calls {
		if call == "execute" {
			t.Fatalf("venue execute called after journal failure: %v", calls)
		}
	}
	if _, ok := node.Cache.Order(req.ClientID); ok {
		t.Fatal("journal-rejected order entered cache")
	}
	if pretrade.prepared.Signature != "" || pretrade.prepared.EncodedOrder != "" || pretrade.prepared.Request != nil {
		t.Fatalf("released prepared payload retained secret material: %+v", pretrade.prepared)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
}

func TestNadoRuntimeExpiredPreparedPayloadClosesLocalDeniedWithoutRevalidation(t *testing.T) {
	now := time.Now().UTC()
	clk := clock.NewSimulatedClock(now)
	provider := nadoTestProvider()
	execution := newNadoRuntimeExecution(t, provider, clk)
	execution.prepared = newPreparedOrderCache(8, time.Second)
	pretrade := newRecordingPreTradeDeps()
	execution.pretrade = pretrade
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
	memory := journal.NewMemory()
	store := &nadoAdvancingIntentStore{Store: memory, clk: clk, advance: 2 * time.Second}
	node := runtime.NewNode(
		runtime.Clients{Execution: execution, Account: account},
		clk,
		"nado-runtime-expired",
		runtime.WithAccountID(AccountIDUnified),
		runtime.WithJournal(store),
	)
	riskEngine := risk.New(risk.Limits{}, node.Cache).
		WithClock(func() time.Time { return clk.Now() }).
		RequireAccountState()
	runtime.WithRisk(riskEngine, provider)(node)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitNadoNodeRunning(t, node)

	req := nadoTestOrderRequest(enums.KindSpot, enums.SideBuy)
	req.ClientID = "runtime-expired"
	if _, err := node.Exec.Submit(context.Background(), req); !errors.Is(err, contract.ErrPreparedStateUnavailable) {
		t.Fatalf("submit err=%v, want ErrPreparedStateUnavailable", err)
	}
	pretrade.mu.Lock()
	calls := append([]string(nil), pretrade.calls...)
	pretrade.mu.Unlock()
	wantCalls := []string{"sender", "max", "prepare"}
	if len(calls) != len(wantCalls) {
		t.Fatalf("pretrade calls=%v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Fatalf("pretrade calls=%v, want %v", calls, wantCalls)
		}
	}
	if execution.preparedLen() != 0 {
		t.Fatalf("prepared entries=%d, want 0", execution.preparedLen())
	}
	order, ok := node.Cache.Order(req.ClientID)
	if !ok || order.Status != enums.StatusRejected {
		t.Fatalf("cache order=%+v ok=%v, want rejected", order, ok)
	}
	if got := node.Exec.InFlightCount(); got != 0 {
		t.Fatalf("in-flight count=%d, want 0", got)
	}
	records := memory.Records()
	var commandIntents int
	var commandResults int
	var resultPayload json.RawMessage
	for _, record := range records {
		switch record.Type {
		case journal.RecordCommandIntent:
			commandIntents++
		case journal.RecordCommandResult:
			commandResults++
			resultPayload = record.Payload
		}
	}
	if commandIntents != 1 || commandResults != 1 {
		t.Fatalf("command journal records intents=%d results=%d", commandIntents, commandResults)
	}
	var result journal.CommandResult
	if err := json.Unmarshal(resultPayload, &result); err != nil {
		t.Fatalf("decode command result: %v", err)
	}
	if result.Outcome != string(runtimeexec.OutcomeLocalDenied) {
		t.Fatalf("journal outcome=%q, want %q", result.Outcome, runtimeexec.OutcomeLocalDenied)
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
	execution.pretrade = newRecordingPreTradeDeps()
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
			if state.Reason != "open-order status requires a configured REST client" {
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
