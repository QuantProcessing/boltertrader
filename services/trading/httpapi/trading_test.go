package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func TestPositionsReturnsInternalErrorWhenPortfolioRepositoryFails(t *testing.T) {
	store := &portfolioListFailureStore{err: errors.New("portfolio read failed")}
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Commands:  newCommandService(orders.NewMemoryStore(), time.Now),
		Portfolio: portfolio.NewPersistentProjection(store), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, error) { return "principal-a", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	type contextKey string
	request := httptest.NewRequest(http.MethodGet, "/api/v1/positions", nil)
	request = request.WithContext(context.WithValue(request.Context(), contextKey("request"), "request-42"))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"INTERNAL_ERROR"`) {
		t.Fatalf("positions response=%d %s", response.Code, response.Body.String())
	}
	if got := store.context.Value(contextKey("request")); got != "request-42" {
		t.Fatalf("repository context value=%v, want request-42", got)
	}
}

func TestTradingReadsSeparateExactAccountingTupleAcrossPrincipals(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	positions := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	for _, principal := range []string{"principal-a", "principal-b"} {
		fill := portfolio.Fill{
			PrincipalID: principal, FillID: "shared-fill", CommandID: "command-" + principal, LineageID: "shared-lineage", ProductID: "manual",
			AccountID: "shared-account", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy,
			Quantity: map[string]string{"principal-a": "1", "principal-b": "2"}[principal], Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
		}
		if _, _, err := positions.ApplyFill(context.Background(), fill); err != nil {
			t.Fatal(err)
		}
		if err := ledgerService.Record(context.Background(), ledger.Entry{
			PrincipalID: principal, EntryID: "shared-entry", ProductID: "manual", LineageID: "shared-lineage",
			AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: ledger.KindRealizedPnL,
			Amount: map[string]string{"principal-a": "1", "principal-b": "2"}[principal], Currency: "USDT", OccurredAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	currentPrincipal := "principal-a"
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Commands:  newCommandService(orders.NewMemoryStore(), time.Now),
		Portfolio: positions, Ledger: ledgerService,
		Principal:         func(*http.Request) (string, error) { return currentPrincipal, nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	assertRead := func(principal, wantQuantity, wantAmount string) {
		t.Helper()
		currentPrincipal = principal
		for path, expected := range map[string]string{
			"/api/v1/positions?productId=manual&accountId=shared-account":                                          `"quantity":"` + wantQuantity + `"`,
			"/api/v1/ledger?productId=manual&lineageId=shared-lineage":                                             `"amount":"` + wantAmount + `"`,
			"/api/v1/pnl?productId=manual&lineageId=shared-lineage&accountId=shared-account&instrumentId=BTC-USDT": `"bookedNetPnl":"` + wantAmount + `"`,
		} {
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), expected) {
				t.Fatalf("principal=%s path=%s response=%d %s, want %s", principal, path, response.Code, response.Body.String(), expected)
			}
			if strings.Contains(response.Body.String(), `"principalId"`) {
				t.Fatalf("principal identity leaked in browser JSON: %s", response.Body.String())
			}
		}
	}
	assertRead("principal-a", "1", "1")
	assertRead("principal-b", "2", "2")
}

type portfolioListFailureStore struct {
	err     error
	context context.Context
}

func (*portfolioListFailureStore) ApplyFill(context.Context, portfolio.Fill, func(portfolio.Position) (portfolio.Position, portfolio.FillResult, error)) (portfolio.Position, portfolio.FillResult, error) {
	return portfolio.Position{}, portfolio.FillResult{}, nil
}
func (*portfolioListFailureStore) ApplyMark(context.Context, string, string, string, string, string, func(portfolio.Position) (portfolio.Position, error)) (portfolio.Position, error) {
	return portfolio.Position{}, nil
}
func (*portfolioListFailureStore) Upsert(context.Context, portfolio.Position) error { return nil }
func (store *portfolioListFailureStore) List(ctx context.Context, _ string, _, _ string) ([]portfolio.Position, error) {
	store.context = ctx
	return nil, store.err
}

func TestCommandDetailUsesRedactedBrowserProjection(t *testing.T) {
	commandService := newCommandService(orders.NewMemoryStore(), time.Now)
	command, _, err := commandService.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a",
		IdempotencyKey: "command-detail", Method: http.MethodPost, Path: "/api/v1/commands", CommandKind: "order.place", Payload: []byte(`{"private":"payload"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commandService.Transition(context.Background(), orders.TransitionRequest{
		CommandID: command.CommandID, State: orders.StateSubmitted,
		Evidence: orders.ExecutionEvidence{CommandID: command.CommandID, GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{Commands: commandService,
		Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal: func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/commands/"+command.CommandID, nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"commandId":"`+command.CommandID+`"`) {
		t.Fatalf("command detail=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{`"payload"`, `"executionEvidence"`, `"gatewayOrderId"`, `"clientOrderId"`, `"principalId"`} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("command detail leaked %s: %s", forbidden, response.Body.String())
		}
	}
}

func TestRemovedGenericCommandRouteCannotPersistOrSend(t *testing.T) {
	commandService := newCommandService(orders.NewMemoryStore(), time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	gateway := &manualGatewayFixture{}
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Commands:          commandService,
		Portfolio:         portfolioProjection,
		Ledger:            ledgerService,
		Manual:            manual.NewService(manual.Options{Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService, Gateway: gateway, Now: time.Now}),
		Principal:         func(*http.Request) (string, error) { return "principal-a", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	commandBody := `{"accountId":"account-a","productId":"manual","lineageId":"lineage-a","reservationId":"","idempotencyKey":"manual-fill-1","commandKind":"manual.place","payload":{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}}`
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(commandBody))
	commandRequest.Header.Set("Idempotency-Key", "manual-fill-1")
	commandResponse := httptest.NewRecorder()
	mux.ServeHTTP(commandResponse, commandRequest)
	if commandResponse.Code != http.StatusNotFound {
		t.Fatalf("manual command response=%d body=%s", commandResponse.Code, commandResponse.Body.String())
	}
	if gateway.calls != 0 {
		t.Fatalf("generic Manual route made %d Gateway calls", gateway.calls)
	}
	if _, err := commandService.Get(context.Background(), "command_0000000000000001"); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("generic Manual route persisted command: %v", err)
	}
}

type manualGatewayFixture struct {
	calls       int
	cancelCalls int
	failFirst   bool
	lastRequest manual.GatewayRequest
}

func (fixture *manualGatewayFixture) Execute(_ context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	fixture.calls++
	fixture.lastRequest = request
	if fixture.failFirst && fixture.calls == 1 {
		return manual.GatewayResult{}, errors.New("ambiguous Gateway handoff")
	}
	return manual.GatewayResult{
		CommandID: request.CommandID,
		AccountID: request.AccountID,
		State:     manual.GatewayStateFilled,
		Fills: []manual.FillFact{{
			FillID: "gateway-fill-1", CommandID: request.CommandID, AccountID: request.AccountID,
			InstrumentID: request.InstrumentID, Side: portfolio.Side(request.Side), Quantity: request.Quantity,
			Price: "100", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC),
		}},
	}, nil
}

func (fixture *manualGatewayFixture) Cancel(_ context.Context, request manual.GatewayCancelRequest) (manual.GatewayResult, error) {
	fixture.cancelCalls++
	evidence := orders.ExecutionEvidence{
		CommandID: request.CommandID, GatewayOrderID: request.GatewayOrderID,
		ClientOrderID: "client-order-1", State: "canceled", ResourceVersion: 2,
	}
	return manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID,
		State: manual.GatewayStateCanceled, Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence},
	}, nil
}

// errMissingCSRF stands in for whatever the caller's authorizer rejects with.
// These routes deliberately do not inspect the reason: any refusal from the
// injected authorizer is a refusal, which is what keeps this surface free of
// the auth edge's session vocabulary.
var errMissingCSRF = errors.New("missing csrf token")

// newCommandService builds the command service these routes write through, with
// live writes armed.
//
// Admission used to be a disjunction: a command was accepted if the service ran
// in test mode *or* the operator had armed live writes, so a test that never
// touched the switch still got to place orders. One mode is left, the switch is
// the only gate, and it starts closed — every construction here has to arm it
// or the first write comes back LIVE_PRIVATE_WRITE_DISABLED. The switch itself
// is covered where it lives, in orders, manual, and the gateway client; nothing
// on this surface asserts against it, so there is no reason to leave one of
// these deliberately unarmed.
func newCommandService(store orders.Store, now func() time.Time) *orders.Service {
	service := orders.NewService(store, now)
	service.SetLiveWritesEnabled(true)
	return service
}

// newManualService builds the manual product service with execution admitted.
//
// The command service above is not the only gate an order passes: manual keeps
// its own switch, and app.go arms both from the same config flag. Arming one and
// not the other stops the write one layer further down with the identical code,
// which reads like the first switch never took.
func newManualService(commands *orders.Service, positions *portfolio.Projection, ledgerService *ledger.Service, gateway manual.Gateway, now func() time.Time, readers ...manual.CatalogReader) *manual.Service {
	projection, _ := catalog.NewProjection(manualCatalogSource{}, time.Minute, now)
	var reader manual.CatalogReader = projection
	if len(readers) > 0 {
		reader = readers[0]
	}
	service := manual.NewService(manual.Options{Commands: commands, Portfolio: positions, Ledger: ledgerService, Gateway: gateway, Now: now, Accounts: &staticManualAccounts{account: readyManualAccount()}, Catalog: reader})
	// Only a nil receiver can fail this, and it was just constructed.
	_ = service.SetExecutionAdmission(true)
	return service
}
