package manual

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
)

var fixtureTime = time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)

type gatewayFixture struct {
	requests []GatewayRequest
	results  []GatewayResult
	errors   []error
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
	if request.CommandID != command.CommandID || request.IdempotencyKey != command.IdempotencyKey || request.ReservationID != "reservation-a" || request.Mode != orders.ModeTest {
		t.Fatalf("Gateway request lost durable authority fields: %+v", request)
	}
	positions := projection.List(portfolio.ModeTest, "account-a", "manual")
	if len(positions) != 1 || positions[0].Quantity != "2" || positions[0].AverageEntryPrice != "101.25" {
		t.Fatalf("positions=%+v", positions)
	}
	entries, err := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
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
	command := orders.Command{CommandID: "command-1", LineageID: "lineage-a", ProductID: "manual", Mode: orders.ModeTest}
	base := FillFact{
		FillID: "fill-open", CommandID: command.CommandID, AccountID: "account-a", InstrumentID: "BTC-USDT", Mode: orders.ModeTest,
		Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0.01", FeeCurrency: "BNB", SettlementCurrency: "USDT", OccurredAt: fixtureTime,
	}
	if err := service.applyFills(context.Background(), command, []FillFact{base}); err != nil {
		t.Fatal(err)
	}
	closeFill := base
	closeFill.FillID, closeFill.Side, closeFill.Price, closeFill.OccurredAt = "fill-close", portfolio.SideSell, "110", fixtureTime.Add(time.Second)
	if err := service.applyFills(context.Background(), command, []FillFact{closeFill}); err != nil {
		t.Fatal(err)
	}
	query := ledger.Query{Mode: ledger.ModeTest, ProductID: "manual", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"}
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
		"wrong mode":    func(result *GatewayResult) { result.Mode = orders.ModeLive },
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
			tasks, claimErr := commands.ClaimDueReconciliation(context.Background(), orders.ModeTest, fixtureTime.Add(2*time.Minute), time.Minute, 1)
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
	positions := projection.List(portfolio.ModeTest, "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual"})
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
	positions := projection.List(portfolio.ModeTest, "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual"})
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
	tasks, claimErr := commands.ClaimDueReconciliation(context.Background(), orders.ModeTest, fixtureTime.Add(2*time.Minute), time.Minute, 1)
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
	positions := projection.List(portfolio.ModeTest, "account-a", "manual")
	entries, _ := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual"})
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
		service := NewGatewayService(commands, portfolio.NewProjection(), ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return fixtureTime }), ambiguous, func() time.Time { return fixtureTime })
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
	positions := projection.List(portfolio.ModeTest, "account-a", "manual")
	entries, listErr := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual"})
	if listErr != nil || len(positions) != 1 || positions[0].Quantity != "2" || len(entries) != 1 {
		t.Fatalf("reconciled effects positions=%+v entries=%+v err=%v", positions, entries, listErr)
	}
}
func TestExecuteRejectsLiveAndBrowserSuppliedFillFactsBeforeGateway(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		fixture := new(gatewayFixture)
		service, _, projection, ledgerService := newFixture(fixture)
		command := orders.Command{CommandID: "command-live", AccountID: "account-a", LineageID: "lineage-a", ProductID: "manual", Mode: orders.ModeLive, IdempotencyKey: "live", CommandKind: "manual.place", State: orders.StateAccepted, Payload: []byte(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"market"}`)}
		if _, err := service.Execute(context.Background(), command); !errors.Is(err, orders.ErrLiveWriteDisabled) {
			t.Fatalf("Execute() err=%v", err)
		}
		if len(fixture.requests) != 0 {
			t.Fatal("Live command reached Gateway")
		}
		assertNoEconomicSideEffects(t, projection, ledgerService)
	})

	t.Run("browser fill facts", func(t *testing.T) {
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
	})
}

func TestExecuteLiveAdmissionIsExplicitAndModeScoped(t *testing.T) {
	t.Run("enabled Live reaches fake Gateway", func(t *testing.T) {
		result := filledResult()
		result.Mode = orders.ModeLive
		for index := range result.Fills {
			result.Fills[index].Mode = orders.ModeLive
		}
		fixture := &gatewayFixture{results: []GatewayResult{result}}
		service, commands, _, _ := newFixture(fixture)
		commands.SetLiveWritesEnabled(true)
		if err := service.SetExecutionAdmission(orders.ModeLive, true); err != nil {
			t.Fatal(err)
		}
		command := createCommandForMode(t, commands, orders.ModeLive, `{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}`)

		got, err := service.Execute(context.Background(), command)
		if err != nil || got.State != orders.StateFilled {
			t.Fatalf("Execute() result=%+v err=%v", got, err)
		}
		if len(fixture.requests) != 1 || fixture.requests[0].Mode != orders.ModeLive {
			t.Fatalf("Gateway requests=%+v", fixture.requests)
		}
	})

	for _, mode := range []orders.Mode{orders.ModeTest, "unsupported"} {
		t.Run("reject "+string(mode), func(t *testing.T) {
			fixture := new(gatewayFixture)
			service, _, _, _ := newFixture(fixture)
			if err := service.SetExecutionAdmission(orders.ModeLive, true); err != nil {
				t.Fatal(err)
			}
			command := orders.Command{CommandID: "cross-mode", ProductID: "manual", CommandKind: "manual.place", Mode: mode}
			if _, err := service.Execute(context.Background(), command); !errors.Is(err, orders.ErrLiveWriteDisabled) {
				t.Fatalf("Execute() err=%v", err)
			}
			if len(fixture.requests) != 0 {
				t.Fatalf("cross-mode command reached Gateway: %+v", fixture.requests)
			}
		})
	}
}

func TestPreviewTokenReconstructsAcrossServiceInstancesAndRejectsTampering(t *testing.T) {
	key := []byte("shared-preview-signing-key-with-32-bytes")
	first := NewGatewayServiceWithPreviewKey(nil, nil, nil, nil, time.Now, key)
	second := NewGatewayServiceWithPreviewKey(nil, nil, nil, nil, time.Now, key)
	payload := []byte(`{"accountId":"account-a","instrumentId":"BTC-USDT","quantity":"2"}`)

	token, err := first.SealPreview("principal-a", orders.ModeTest, payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := second.OpenPreview("principal-a", orders.ModeTest, token)
	if err != nil || string(decoded) != string(payload) {
		t.Fatalf("decoded=%s err=%v", decoded, err)
	}
	if _, err := second.OpenPreview("principal-b", orders.ModeTest, token); err == nil {
		t.Fatal("preview token crossed principal ownership")
	}
	tampered := token[:len(token)-1] + "A"
	if _, err := second.OpenPreview("principal-a", orders.ModeTest, tampered); err == nil {
		t.Fatal("tampered preview token was accepted")
	}
}

func newFixture(gateway Gateway) (*Service, *orders.Service, *portfolio.Projection, *ledger.Service) {
	commands := orders.NewService(orders.NewMemoryStore(), nil, func() time.Time { return fixtureTime })
	projection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return fixtureTime })
	return NewGatewayService(commands, projection, ledgerService, gateway, func() time.Time { return fixtureTime }), commands, projection, ledgerService
}

func createCommand(t *testing.T, commands *orders.Service, payload string) orders.Command {
	return createCommandForMode(t, commands, orders.ModeTest, payload)
}

func createCommandForMode(t *testing.T, commands *orders.Service, mode orders.Mode, payload string) orders.Command {
	t.Helper()
	command, _, err := commands.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a", ReservationID: "reservation-a",
		IdempotencyKey: "manual-command-1", Method: "POST", Path: "/api/v1/commands", CommandKind: "manual.place", Mode: mode, Payload: []byte(payload),
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func filledResult() GatewayResult {
	return GatewayResult{
		CommandID: "command_0000000000000001", AccountID: "account-a", Mode: orders.ModeTest, State: GatewayStateFilled,
		Fills: []FillFact{{FillID: "gateway-fill-1", CommandID: "command_0000000000000001", AccountID: "account-a", Mode: orders.ModeTest, InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "2", Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: fixtureTime}},
	}
}

func assertNoEconomicSideEffects(t *testing.T, projection *portfolio.Projection, ledgerService *ledger.Service) {
	t.Helper()
	if positions := projection.List(portfolio.ModeTest, "", "manual"); len(positions) != 0 {
		t.Fatalf("unexpected positions: %+v", positions)
	}
	entries, err := ledgerService.List(context.Background(), ledger.Query{Mode: ledger.ModeTest, ProductID: "manual"})
	if err != nil || len(entries) != 0 {
		t.Fatalf("unexpected ledger entries=%+v err=%v", entries, err)
	}
}
