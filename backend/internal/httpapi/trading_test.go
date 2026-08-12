package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
)

func TestPositionsReturnsInternalErrorWhenPortfolioRepositoryFails(t *testing.T) {
	store := &portfolioListFailureStore{err: errors.New("portfolio read failed")}
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewPersistentProjection(store), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
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
			Mode: portfolio.ModeTest, AccountID: "shared-account", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy,
			Quantity: map[string]string{"principal-a": "1", "principal-b": "2"}[principal], Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
		}
		if _, _, err := positions.ApplyFill(context.Background(), fill); err != nil {
			t.Fatal(err)
		}
		if err := ledgerService.Record(context.Background(), ledger.Entry{
			PrincipalID: principal, EntryID: "shared-entry", Mode: ledger.ModeTest, ProductID: "manual", LineageID: "shared-lineage",
			AccountID: "shared-account", InstrumentID: "BTC-USDT", Kind: ledger.KindRealizedPnL,
			Amount: map[string]string{"principal-a": "1", "principal-b": "2"}[principal], Currency: "USDT", OccurredAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	currentPrincipal := "principal-a"
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    positions, Ledger: ledgerService,
		Principal:         func(*http.Request) (string, string, error) { return currentPrincipal, "test", nil },
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
			"/api/v1/pnl?productId=manual&lineageId=shared-lineage&accountId=shared-account&instrumentId=BTC-USDT": `"netPnl":"` + wantAmount + `"`,
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
func (*portfolioListFailureStore) ApplyMark(context.Context, string, portfolio.Mode, string, string, string, string, func(portfolio.Position) (portfolio.Position, error)) (portfolio.Position, error) {
	return portfolio.Position{}, nil
}
func (*portfolioListFailureStore) Upsert(context.Context, portfolio.Position) error { return nil }
func (store *portfolioListFailureStore) List(ctx context.Context, _ string, _ portfolio.Mode, _, _ string) ([]portfolio.Position, error) {
	store.context = ctx
	return nil, store.err
}

func TestTradingRoutesRejectUnknownFieldsAndCreateReservation(t *testing.T) {
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"accountId":"account-a","productId":"manual","reservationKind":"open","nettingScope":{"accountId":"account-a","venue":"binance","instrumentId":"BTC-USDT","positionMode":null,"settlementScope":null,"venueEnvironment":"demo"},"quantity":"1","notional":null,"currency":null,"mode":"test","unknown":true}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "reserve-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("unknown field response=%d %s", response.Code, response.Body.String())
	}
	body = `{"accountId":"account-a","productId":"manual","reservationKind":"open","nettingScope":{"accountId":"account-a","venue":"binance","instrumentId":"BTC-USDT","positionMode":null,"settlementScope":null,"venueEnvironment":"demo"},"quantity":"1","notional":null,"currency":null,"mode":"test"}`
	request = httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "reserve-a")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"productId":"manual"`) {
		t.Fatalf("create response=%d %s", response.Code, response.Body.String())
	}
}

func TestTradingRoutesRequireExactlyOneReservationSizing(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservationService,
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	scope := `"nettingScope":{"accountId":"account-long","venue":"venue-a","instrumentId":"btc-a","positionMode":null,"settlementScope":null,"venueEnvironment":"testnet"}`
	both := `{"accountId":"account-long","productId":"manual","reservationKind":"close",` + scope + `,"quantity":"0.5","notional":"50","currency":"USDT","mode":"test"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(both))
	request.Header.Set("Idempotency-Key", "close-long-both")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("quantity and notional together=%d body=%s", response.Code, response.Body.String())
	}

	neither := `{"accountId":"account-long","productId":"manual","reservationKind":"close",` + scope + `,"quantity":null,"notional":null,"currency":"USDT","mode":"test"}`
	request = httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(neither))
	request.Header.Set("Idempotency-Key", "close-long-neither")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("neither quantity nor notional=%d body=%s", response.Code, response.Body.String())
	}

	notionalOnly := `{"accountId":"account-long","productId":"manual","reservationKind":"close",` + scope + `,"quantity":null,"notional":"50","currency":"USDT","mode":"test"}`
	request = httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(notionalOnly))
	request.Header.Set("Idempotency-Key", "close-long-notional")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("notional-only reservation=%d body=%s", response.Code, response.Body.String())
	}
	items, err := reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
	if err != nil || len(items) != 1 || items[0].Quantity != nil || items[0].Notional == nil || *items[0].Notional != "50" || items[0].Currency == nil || *items[0].Currency != "USDT" {
		t.Fatalf("persisted reservations=%+v err=%v", items, err)
	}
}

func TestTradingMutationRejectsMissingCSRFAheadOfSideEffects(t *testing.T) {
	reservationsService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	allow := false
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservationsService,
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error {
			if !allow {
				return session.ErrInvalidCSRF
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"accountId":"account-a","productId":"manual","reservationKind":"open","nettingScope":{"accountId":"account-a","venue":"binance","instrumentId":"BTC-USDT","positionMode":null,"settlementScope":null,"venueEnvironment":"demo"},"quantity":"1","notional":null,"currency":null,"mode":"test"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "reserve-csrf")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cookie-only mutation=%d body=%s", response.Code, response.Body.String())
	}
	allow = true
	request = httptest.NewRequest(http.MethodPost, "/api/v1/reservations", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", "reserve-csrf")
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("authorized mutation=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTradingCSRFProtectsCommandsButNotReservationReads(t *testing.T) {
	commandStore := orders.NewMemoryStore()
	commandService := orders.NewService(commandStore, nil, time.Now)
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     commandService,
		Portfolio:    portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return session.ErrInvalidCSRF },
	})
	if err != nil {
		t.Fatal(err)
	}

	listResponse := httptest.NewRecorder()
	mux.ServeHTTP(listResponse, httptest.NewRequest(http.MethodGet, "/api/v1/reservations", nil))
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list without csrf=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	commandBody := `{"accountId":"account-a","productId":"manual","lineageId":"lineage-a","reservationId":"","idempotencyKey":"command-csrf","commandKind":"place","mode":"test","payload":{}}`
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(commandBody))
	commandRequest.Header.Set("Idempotency-Key", "command-csrf")
	commandResponse := httptest.NewRecorder()
	mux.ServeHTTP(commandResponse, commandRequest)
	if commandResponse.Code != http.StatusForbidden {
		t.Fatalf("command without csrf=%d body=%s", commandResponse.Code, commandResponse.Body.String())
	}
	if _, err := commandService.Get(context.Background(), "command_0000000000000001"); !errors.Is(err, orders.ErrNotFound) {
		t.Fatalf("command side effect occurred: %v", err)
	}
}

func TestCommandDetailUsesRedactedBrowserProjection(t *testing.T) {
	commandService := orders.NewService(orders.NewMemoryStore(), nil, time.Now)
	command, _, err := commandService.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", Mode: orders.ModeTest, AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a",
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
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now), Commands: commandService,
		Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
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

func TestGenericCommandRouteRejectsManualBeforePersistenceAndGatewayHandoff(t *testing.T) {
	commandService := orders.NewService(orders.NewMemoryStore(), nil, time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	gateway := &manualGatewayFixture{}
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations:      reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:          commandService,
		Portfolio:         portfolioProjection,
		Ledger:            ledgerService,
		Manual:            manual.NewGatewayService(commandService, portfolioProjection, ledgerService, gateway, time.Now),
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	commandBody := `{"accountId":"account-a","productId":"manual","lineageId":"lineage-a","reservationId":"","idempotencyKey":"manual-fill-1","commandKind":"manual.place","mode":"test","payload":{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}}`
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(commandBody))
	commandRequest.Header.Set("Idempotency-Key", "manual-fill-1")
	commandResponse := httptest.NewRecorder()
	mux.ServeHTTP(commandResponse, commandRequest)
	if commandResponse.Code != http.StatusBadRequest || !strings.Contains(commandResponse.Body.String(), `"code":"VALIDATION_ERROR"`) {
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
		Mode:      request.Mode,
		State:     manual.GatewayStateFilled,
		Fills: []manual.FillFact{{
			FillID: "gateway-fill-1", CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
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
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
		State: manual.GatewayStateCanceled, Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence},
	}, nil
}
