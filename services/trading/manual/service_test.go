package manual

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

var fixtureTime = time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)

type gatewayFixture struct {
	requests       []GatewayRequest
	results        []GatewayResult
	errors         []error
	cancelRequests []GatewayCancelRequest
	cancelResults  []GatewayResult
	cancelErrors   []error
}

type gatewayFunc func(context.Context, GatewayRequest) (GatewayResult, error)

func (function gatewayFunc) Execute(ctx context.Context, request GatewayRequest) (GatewayResult, error) {
	return function(ctx, request)
}

type failingTransitionStore struct {
	orders.Store
	err error
}

func (store failingTransitionStore) Transition(context.Context, orders.TransitionRequest, time.Time) (orders.Command, error) {
	return orders.Command{}, store.err
}
func (fixture *gatewayFixture) Execute(_ context.Context, request GatewayRequest) (GatewayResult, error) {
	fixture.requests = append(fixture.requests, request)
	index := len(fixture.requests) - 1
	if index < len(fixture.errors) && fixture.errors[index] != nil {
		return GatewayResult{}, fixture.errors[index]
	}
	if index >= len(fixture.results) {
		return GatewayResult{}, errors.New("unexpected Gateway call")
	}
	result := fixture.results[index]
	if result.CommandID == "command_0000000000000001" {
		result.CommandID = request.CommandID
	}
	for index := range result.Fills {
		if result.Fills[index].CommandID == "command_0000000000000001" {
			result.Fills[index].CommandID = request.CommandID
		}
	}
	return result, nil
}

func (fixture *gatewayFixture) Cancel(_ context.Context, request GatewayCancelRequest) (GatewayResult, error) {
	fixture.cancelRequests = append(fixture.cancelRequests, request)
	index := len(fixture.cancelRequests) - 1
	if index < len(fixture.cancelErrors) && fixture.cancelErrors[index] != nil {
		result := GatewayResult{}
		if index < len(fixture.cancelResults) {
			result = fixture.cancelResults[index]
		}
		return result, fixture.cancelErrors[index]
	}
	if index >= len(fixture.cancelResults) {
		return GatewayResult{}, errors.New("unexpected Gateway cancel call")
	}
	return fixture.cancelResults[index], nil
}

func TestCancelRestingOrderTransitionsOriginalCommand(t *testing.T) {
	fixture := &gatewayFixture{}
	service, commands, _, _ := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"limit","limitPrice":"100","timeInForce":"gtc"}`)
	command = submittedCommand(t, commands, command)
	fixture.cancelResults = []GatewayResult{{
		CommandID: command.CommandID, AccountID: command.AccountID,
		State: GatewayStateCanceled, Handoff: authoritativeHandoff("canceled", 2, nil),
	}}

	result, err := service.Cancel(context.Background(), command)
	if err != nil || result.State != orders.StateCanceled {
		t.Fatalf("Cancel() result=%+v err=%v", result, err)
	}
	if len(fixture.cancelRequests) != 1 || fixture.cancelRequests[0].GatewayOrderID != "gateway-order-1" || fixture.cancelRequests[0].CommandID != command.CommandID {
		t.Fatalf("cancel requests=%+v", fixture.cancelRequests)
	}
	replayed, err := service.Cancel(context.Background(), result)
	if err != nil || replayed.State != orders.StateCanceled || len(fixture.cancelRequests) != 1 {
		t.Fatalf("replayed Cancel()=%+v err=%v calls=%d", replayed, err, len(fixture.cancelRequests))
	}
}

func TestCancelAppliesPartialFillsAndFeesBeforeCanceledTransition(t *testing.T) {
	fixture := &gatewayFixture{}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"limit","limitPrice":"100","timeInForce":"gtc"}`)
	command = submittedCommand(t, commands, command)
	fill := filledResult().Fills[0]
	fill.CommandID, fill.Quantity, fill.Price, fill.Fee = command.CommandID, "1", "100", "0.5"
	fixture.cancelResults = []GatewayResult{{
		CommandID: command.CommandID, AccountID: command.AccountID,
		State: GatewayStateCanceled, Fills: []FillFact{fill}, Handoff: authoritativeHandoff("canceled", 2, []FillFact{fill}),
	}}

	result, err := service.Cancel(context.Background(), command)
	positions := projection.List("principal-a", "account-a", "manual")
	entries, ledgerErr := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a",
		ProductID: "manual", LineageID: command.LineageID, AccountID: command.AccountID, InstrumentID: "BTC-USDT",
	})
	if err != nil || ledgerErr != nil || result.State != orders.StateCanceled || len(positions) != 1 || positions[0].Quantity != "1" || len(entries) != 1 || entries[0].Kind != ledger.KindFee || entries[0].Amount != "0.5" {
		t.Fatalf("Cancel()=%+v positions=%+v entries=%+v err=%v ledgerErr=%v", result, positions, entries, err, ledgerErr)
	}
}

func TestCancelFillRaceAppliesAuthoritativeFillAndClosesPosition(t *testing.T) {
	fixture := &gatewayFixture{results: []GatewayResult{filledResult()}}
	service, commands, projection, _ := newFixture(fixture)
	open := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
	if _, err := service.Execute(context.Background(), open); err != nil {
		t.Fatal(err)
	}
	resting := createCommandWithKey(t, commands, "manual-command-2", `{"instrumentId":"BTC-USDT","side":"sell","quantity":"2","orderType":"limit","limitPrice":"102","timeInForce":"gtc","reduceOnly":true}`)
	resting = submittedCommand(t, commands, resting)
	fill := filledResult().Fills[0]
	fill.FillID, fill.CommandID, fill.Side, fill.Price = "gateway-fill-cancel-race", resting.CommandID, portfolio.SideSell, "102"
	fixture.cancelResults = []GatewayResult{{
		CommandID: resting.CommandID, AccountID: resting.AccountID,
		State: GatewayStateFilled, Fills: []FillFact{fill}, Handoff: authoritativeHandoff("filled", 2, []FillFact{fill}),
	}}

	result, err := service.Cancel(context.Background(), resting)
	positions := projection.List("principal-a", "account-a", "manual")
	if err != nil || result.State != orders.StateFilled || len(positions) != 1 || positions[0].Quantity != "0" {
		t.Fatalf("Cancel()=%+v positions=%+v err=%v", result, positions, err)
	}
}

func TestCancelAmbiguousHandoffPreservesOrderForReconciliation(t *testing.T) {
	fixture := &gatewayFixture{}
	service, commands, _, _ := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"limit","limitPrice":"100","timeInForce":"gtc"}`)
	command = submittedCommand(t, commands, command)
	fixture.cancelResults = []GatewayResult{{
		CommandID: command.CommandID, AccountID: command.AccountID,
		State: GatewayStateSubmitted, Handoff: orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: command.ExecutionEvidence},
	}}
	fixture.cancelErrors = []error{context.DeadlineExceeded}

	result, err := service.Cancel(context.Background(), command)
	if !errors.Is(err, context.DeadlineExceeded) || result.State != orders.StateUncertain || result.ExecutionEvidence.GatewayOrderID != "gateway-order-1" {
		t.Fatalf("Cancel() result=%+v err=%v", result, err)
	}
}

func TestExecuteAppliesOnlyGatewayAuthoritativeFillFacts(t *testing.T) {
	fixture := &gatewayFixture{results: []GatewayResult{filledResult()}}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market","timeInForce":"ioc","reduceOnly":false}`)

	result, err := service.Execute(context.Background(), command)
	if err != nil || result.State != orders.StateFilled {
		t.Fatalf("Execute() result=%+v err=%v", result, err)
	}
	if len(fixture.requests) != 1 {
		t.Fatalf("Gateway calls=%d, want 1", len(fixture.requests))
	}
	request := fixture.requests[0]
	if request.CommandID != command.CommandID || request.IdempotencyKey != command.IdempotencyKey || request.ReservationID != "reservation-a" {
		t.Fatalf("Gateway request lost durable authority fields: %+v", request)
	}
	positions := projection.List("principal-a", "account-a", "manual")
	if len(positions) != 1 || positions[0].Quantity != "2" || positions[0].AverageEntryPrice != "101.25" {
		t.Fatalf("positions=%+v", positions)
	}
	entries, err := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil || len(entries) != 1 || entries[0].EntryID != "gateway-fill-1:fee" || entries[0].Amount != "1.5" {
		t.Fatalf("ledger entries=%+v err=%v", entries, err)
	}
}

func TestExecuteDenialHasNoEconomicSideEffects(t *testing.T) {
	result := filledResult()
	result.State, result.Fills, result.DenialCode = GatewayStateDenied, nil, "RISK_DENIED"
	fixture := &gatewayFixture{results: []GatewayResult{result}}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

	got, err := service.Execute(context.Background(), command)
	if err != nil || got.State != orders.StateFailed {
		t.Fatalf("Execute() result=%+v err=%v", got, err)
	}
	assertNoEconomicSideEffects(t, projection, ledgerService)
}

func TestApplyFillsKeepsFeeAndSettlementCurrenciesIndependent(t *testing.T) {
	service, _, _, ledgerService := newFixture(nil)
	command := orders.Command{CommandID: "command-1", PrincipalID: "principal-a", LineageID: "lineage-a", ProductID: "manual"}
	base := FillFact{
		FillID: "fill-open", CommandID: command.CommandID, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0.01", FeeCurrency: "BNB", SettlementCurrency: "USDT", OccurredAt: fixtureTime,
	}
	if err := service.applyFills(context.Background(), command, []FillFact{base}); err != nil {
		t.Fatal(err)
	}
	closeFill := base
	closeFill.FillID, closeFill.Side, closeFill.Price, closeFill.OccurredAt = "fill-close", portfolio.SideSell, "110", fixtureTime.Add(time.Second)
	if err := service.applyFills(context.Background(), command, []FillFact{closeFill}); err != nil {
		t.Fatal(err)
	}
	query := ledger.Query{PrincipalID: "principal-a", ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"}
	entries, err := ledgerService.List(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var feeCurrency, realizedCurrency string
	for _, entry := range entries {
		switch entry.Kind {
		case ledger.KindFee:
			feeCurrency = entry.Currency
		case ledger.KindRealizedPnL:
			realizedCurrency = entry.Currency
		}
	}
	if feeCurrency != "BNB" || realizedCurrency != "USDT" {
		t.Fatalf("entries=%+v", entries)
	}
	if _, err := ledgerService.PnL(context.Background(), query); !errors.Is(err, ledger.ErrCurrencyMismatch) {
		t.Fatalf("PnL error=%v, want ErrCurrencyMismatch", err)
	}
}

func TestExecuteRejectsMalformedOrMismatchedGatewayFactsBeforeSideEffects(t *testing.T) {
	tests := map[string]func(*GatewayResult){
		"wrong command": func(result *GatewayResult) { result.CommandID = "other-command" },
		"wrong account": func(result *GatewayResult) { result.AccountID = "other-account" },
		"wrong fill command": func(result *GatewayResult) {
			result.Fills[0].CommandID = "other-command"
		},
		"wrong instrument": func(result *GatewayResult) { result.Fills[0].InstrumentID = "ETH-USDT" },
		"wrong side":       func(result *GatewayResult) { result.Fills[0].Side = portfolio.SideSell },
		"missing fill id":  func(result *GatewayResult) { result.Fills[0].FillID = "" },
		"invalid decimal":  func(result *GatewayResult) { result.Fills[0].Price = "not-a-price" },
		"filled without facts": func(result *GatewayResult) {
			result.Fills = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			result := filledResult()
			mutate(&result)
			fixture := &gatewayFixture{results: []GatewayResult{result}}
			service, commands, projection, ledgerService := newFixture(fixture)
			command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

			got, err := service.Execute(context.Background(), command)
			if err == nil || got.State != orders.StateUncertain {
				t.Fatalf("Execute() result=%+v err=%v", got, err)
			}
			tasks, claimErr := commands.ClaimDueReconciliation(context.Background(), fixtureTime.Add(2*time.Minute), time.Minute, 1)
			if claimErr != nil || len(tasks) != 1 || tasks[0].Reason != orders.ReconciliationEvidenceInvalid {
				t.Fatalf("reconciliation tasks=%+v err=%v", tasks, claimErr)
			}
			assertNoEconomicSideEffects(t, projection, ledgerService)
		})
	}
}

func TestExecuteReplayIsDeterministicAndDoesNotDuplicateEffects(t *testing.T) {
	fixture := &gatewayFixture{results: []GatewayResult{filledResult()}}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

	first, err := service.Execute(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Execute(context.Background(), command)
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("replay result=%+v first=%+v err=%v", second, first, err)
	}
	if len(fixture.requests) != 1 {
		t.Fatalf("terminal replay called Gateway %d times", len(fixture.requests))
	}
	positions := projection.List("principal-a", "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual"})
	if len(positions) != 1 || positions[0].Quantity != "2" || len(entries) != 1 {
		t.Fatalf("replay duplicated effects: positions=%+v entries=%+v", positions, entries)
	}
}

func TestReconciliationAppliesPartialThenTerminalFillsIdempotently(t *testing.T) {
	partial := filledResult()
	partial.State = GatewayStatePartiallyFilled
	partial.Handoff = orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "partially_filled", ResourceVersion: 1}}
	partial.Handoff.Evidence.Fills = []orders.FillEvidence{{FillID: "gateway-fill-1", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "2", Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: fixtureTime}}
	fixture := &gatewayFixture{results: []GatewayResult{partial}}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
	current, err := service.Execute(context.Background(), command)
	if err != nil || current.State != orders.StateSubmitted {
		t.Fatalf("partial result=%+v err=%v", current, err)
	}
	terminal := partial.Handoff
	terminal.Evidence.State, terminal.Evidence.ResourceVersion = "filled", 2
	current, err = service.RecordReconciliation(context.Background(), current, terminal)
	if err != nil || current.State != orders.StateFilled {
		t.Fatalf("terminal result=%+v err=%v", current, err)
	}
	positions := projection.List("principal-a", "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual"})
	if len(positions) != 1 || positions[0].Quantity != "2" || len(entries) != 1 {
		t.Fatalf("replay duplicated effects positions=%+v entries=%+v", positions, entries)
	}
}

func TestExecuteResumesUncertainGatewayHandoffWithSameIdentity(t *testing.T) {
	fixture := &gatewayFixture{
		results: []GatewayResult{{}, filledResult()},
		errors:  []error{errors.New("ambiguous transport handoff")},
	}
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

	uncertain, err := service.Execute(context.Background(), command)
	if err == nil || uncertain.State != orders.StateUncertain {
		t.Fatalf("first Execute() result=%+v err=%v", uncertain, err)
	}
	tasks, claimErr := commands.ClaimDueReconciliation(context.Background(), fixtureTime.Add(2*time.Minute), time.Minute, 1)
	if claimErr != nil || len(tasks) != 1 || tasks[0].Reason != orders.ReconciliationAmbiguousHandoff {
		t.Fatalf("reconciliation tasks=%+v err=%v", tasks, claimErr)
	}
	filled, err := service.Execute(context.Background(), uncertain)
	if err != nil || filled.State != orders.StateFilled {
		t.Fatalf("resume Execute() result=%+v err=%v", filled, err)
	}
	if len(fixture.requests) != 2 || fixture.requests[0] != fixture.requests[1] {
		t.Fatalf("Gateway replay identity changed: %+v", fixture.requests)
	}
	positions := projection.List("principal-a", "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual"})
	if len(positions) != 1 || len(entries) != 1 {
		t.Fatalf("resume effects positions=%+v entries=%+v", positions, entries)
	}
}

func TestAmbiguousHandoffPersistsKnownEvidenceAndSurfacesTransitionFailure(t *testing.T) {
	evidence := orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}
	ambiguous := gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		return GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: evidence}}, errors.New("ambiguous transport handoff")
	})

	t.Run("known evidence is durable", func(t *testing.T) {
		service, commands, _, _ := newFixture(ambiguous)
		command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
		uncertain, err := service.Execute(context.Background(), command)
		if err == nil || uncertain.State != orders.StateUncertain || !reflect.DeepEqual(uncertain.ExecutionEvidence, evidence) {
			t.Fatalf("Execute() result=%+v err=%v", uncertain, err)
		}
		stored, getErr := commands.Get(context.Background(), command.CommandID)
		if getErr != nil || !reflect.DeepEqual(stored.ExecutionEvidence, evidence) {
			t.Fatalf("stored evidence=%+v err=%v", stored.ExecutionEvidence, getErr)
		}
	})

	t.Run("transition failure is surfaced", func(t *testing.T) {
		transitionErr := errors.New("persist uncertain transition")
		commandStore := failingTransitionStore{Store: orders.NewMemoryStore(), err: transitionErr}
		commands := orders.NewService(commandStore, nil, func() time.Time { return fixtureTime })
		commands.SetLiveWritesEnabled(true)
		service := NewGatewayService(commands, portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return fixtureTime }), ambiguous, func() time.Time { return fixtureTime })
		// This one builds its own service to reach the failing store, so it has to
		// arm both switches the way newFixture does.
		_ = service.SetExecutionAdmission(true)
		command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
		returned, err := service.Execute(context.Background(), command)
		if !errors.Is(err, transitionErr) {
			t.Fatalf("Execute() error=%v, want transition error", err)
		}
		if !reflect.DeepEqual(returned.ExecutionEvidence, evidence) {
			t.Fatalf("returned evidence=%+v, want %+v", returned.ExecutionEvidence, evidence)
		}
	})
}

func TestAmbiguousHandoffReconcilesExactEvidenceWithoutSecondPlacement(t *testing.T) {
	placements := 0
	initialEvidence := orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}
	gateway := gatewayFunc(func(context.Context, GatewayRequest) (GatewayResult, error) {
		placements++
		return GatewayResult{Handoff: orders.HandoffResult{Outcome: orders.HandoffOutcomeUnknown, Evidence: initialEvidence}}, errors.New("ambiguous transport handoff")
	})
	service, commands, projection, ledgerService := newFixture(gateway)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)
	uncertain, err := service.Execute(context.Background(), command)
	if err == nil || uncertain.State != orders.StateUncertain || !reflect.DeepEqual(uncertain.ExecutionEvidence, initialEvidence) {
		t.Fatalf("uncertain result=%+v err=%v", uncertain, err)
	}
	terminalEvidence := initialEvidence
	terminalEvidence.State = "filled"
	terminalEvidence.ResourceVersion = 2
	terminalEvidence.Fills = []orders.FillEvidence{{FillID: "gateway-fill-1", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "2", Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: fixtureTime}}
	filled, err := service.RecordReconciliation(context.Background(), uncertain, orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: terminalEvidence})
	if err != nil || filled.State != orders.StateFilled || placements != 1 {
		t.Fatalf("reconciled result=%+v placements=%d err=%v", filled, placements, err)
	}
	positions := projection.List("principal-a", "account-a", "manual")
	entries, listErr := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual"})
	if listErr != nil || len(positions) != 1 || positions[0].Quantity != "2" || len(entries) != 1 {
		t.Fatalf("reconciled effects positions=%+v entries=%+v err=%v", positions, entries, listErr)
	}
}

// TestExecuteRejectsLiveAndBrowserSuppliedFillFactsBeforeGateway had a "live"
// case first: a live-mode command handed to a test-mode service, refused
// because the two modes disagreed. With one mode left there is nothing to
// disagree about, and what that case actually proved about an unadmitted
// service is now TestExecuteAdmissionIsExplicit's job.
func TestExecuteRejectsBrowserSuppliedFillFactsBeforeGateway(t *testing.T) {
	fixture := new(gatewayFixture)
	service, commands, projection, ledgerService := newFixture(fixture)
	command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market","price":"1","fee":"0"}`)
	failed, err := service.Execute(context.Background(), command)
	if err == nil || failed.State != orders.StateFailed {
		t.Fatal("browser-supplied fill facts were accepted")
	}
	if len(fixture.requests) != 0 {
		t.Fatal("invalid intent reached Gateway")
	}
	assertNoEconomicSideEffects(t, projection, ledgerService)
}

// TestExecuteLiveAdmissionIsExplicitAndModeScoped also swept a cross-mode axis:
// an admitted service still refused a command whose mode was test or was not a
// mode at all. That half described the mode agreement, which is gone. What
// survives is the half the name led with — admission is never implied, so a
// service nobody armed reaches no venue.
func TestExecuteAdmissionIsExplicit(t *testing.T) {
	t.Run("admitted reaches the Gateway", func(t *testing.T) {
		fixture := &gatewayFixture{results: []GatewayResult{filledResult()}}
		service, commands, _, _ := newFixture(fixture)
		command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

		got, err := service.Execute(context.Background(), command)
		if err != nil || got.State != orders.StateFilled {
			t.Fatalf("Execute() result=%+v err=%v", got, err)
		}
		if len(fixture.requests) != 1 {
			t.Fatalf("Gateway requests=%+v", fixture.requests)
		}
	})

	t.Run("unadmitted reaches nothing", func(t *testing.T) {
		fixture := new(gatewayFixture)
		service, commands, projection, ledgerService := newUnadmittedFixture(fixture)
		command := createCommand(t, commands, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

		if _, err := service.Execute(context.Background(), command); !errors.Is(err, orders.ErrLiveWriteDisabled) {
			t.Fatalf("Execute() err=%v", err)
		}
		if len(fixture.requests) != 0 {
			t.Fatalf("unadmitted command reached Gateway: %+v", fixture.requests)
		}
		assertNoEconomicSideEffects(t, projection, ledgerService)
	})
}

func TestPreviewTokenReconstructsAcrossServiceInstancesAndRejectsTampering(t *testing.T) {
	key := []byte("shared-preview-signing-key-with-32-bytes")
	first := NewGatewayServiceWithPreviewKey(nil, nil, nil, nil, time.Now, key)
	second := NewGatewayServiceWithPreviewKey(nil, nil, nil, nil, time.Now, key)
	payload := []byte(`{"accountId":"account-a","instrumentId":"BTC-USDT","quantity":"2"}`)

	token, err := first.SealPreview("principal-a", payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := second.OpenPreview("principal-a", token)
	if err != nil || string(decoded) != string(payload) {
		t.Fatalf("decoded=%s err=%v", decoded, err)
	}
	if _, err := second.OpenPreview("principal-b", token); err == nil {
		t.Fatal("preview token crossed principal ownership")
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := second.OpenPreview("principal-a", tampered); err == nil {
		t.Fatal("tampered preview token was accepted")
	}
}

// newFixture arms both write switches: the command service's and the manual
// service's execution admission. Neither used to matter here, because a
// test-mode command was admitted by both with nothing armed. With one mode left
// they are the only gates and both start closed, so every test that creates or
// executes a command needs them open. TestExecuteAdmissionIsExplicit builds an
// unarmed manual service to prove that gate still holds.
func newFixture(gateway Gateway) (*Service, *orders.Service, *portfolio.Projection, *ledger.Service) {
	service, commands, projection, ledgerService := newUnadmittedFixture(gateway)
	// Only a nil receiver can fail this, and it was just constructed.
	_ = service.SetExecutionAdmission(true)
	return service, commands, projection, ledgerService
}

func newUnadmittedFixture(gateway Gateway) (*Service, *orders.Service, *portfolio.Projection, *ledger.Service) {
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return fixtureTime })
	commands.SetLiveWritesEnabled(true)
	projection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return fixtureTime })
	return NewGatewayService(commands, projection, ledgerService, gateway, func() time.Time { return fixtureTime }), commands, projection, ledgerService
}

func createCommand(t *testing.T, commands *orders.Service, payload string) orders.Command {
	return createCommandWithKey(t, commands, "manual-command-1", payload)
}

func createCommandWithKey(t *testing.T, commands *orders.Service, key, payload string) orders.Command {
	t.Helper()
	command, _, err := commands.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a", ReservationID: "reservation-a",
		IdempotencyKey: key, Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Payload: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func submittedCommand(t *testing.T, commands *orders.Service, command orders.Command) orders.Command {
	t.Helper()
	result, err := commands.Transition(context.Background(), orders.TransitionRequest{
		CommandID: command.CommandID, State: orders.StateSubmitted,
		Evidence: authoritativeHandoff("submitted", 1, nil).Evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func authoritativeHandoff(state string, version int64, fills []FillFact) orders.HandoffResult {
	evidence := orders.ExecutionEvidence{
		// The Gateway mints its own command id and never echoes back the
		// trading command id it was handed. Stamping the trading id here would
		// let a cancel that is impossible in production pass in tests.
		CommandID: "gateway-command-1", GatewayOrderID: "gateway-order-1", ClientOrderID: "client-order-1",
		State: state, ResourceVersion: version,
		OrderEvents: []orders.OrderEventEvidence{{EventID: fmt.Sprintf("gateway-order-1:%d", version), State: state, ResourceVersion: version, OccurredAt: fixtureTime}},
	}
	for _, fill := range fills {
		evidence.Fills = append(evidence.Fills, orders.FillEvidence{
			FillID: fill.FillID, AccountID: fill.AccountID, InstrumentID: fill.InstrumentID,
			Side: string(fill.Side), Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee,
			FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt,
		})
	}
	return orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence}
}

func filledResult() GatewayResult {
	return GatewayResult{
		CommandID: "command_0000000000000001", AccountID: "account-a", State: GatewayStateFilled,
		Fills: []FillFact{{FillID: "gateway-fill-1", CommandID: "command_0000000000000001", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "2", Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: fixtureTime}},
	}
}

func assertNoEconomicSideEffects(t *testing.T, projection *portfolio.Projection, ledgerService *ledger.Service) {
	t.Helper()
	if positions := projection.List("principal-a", "", "manual"); len(positions) != 0 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
	entries, err := ledgerService.List(context.Background(), ledger.Query{PrincipalID: "principal-a", ProductID: "manual"})
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected ledger entries=%+v err=%v", entries, err)
	}
}
