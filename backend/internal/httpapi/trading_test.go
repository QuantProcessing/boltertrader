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

type portfolioListFailureStore struct {
	err     error
	context context.Context
}

func (*portfolioListFailureStore) ApplyFill(context.Context, portfolio.Fill, func(portfolio.Position) (portfolio.Position, portfolio.FillResult, error)) (portfolio.Position, portfolio.FillResult, error) {
	return portfolio.Position{}, portfolio.FillResult{}, nil
}
func (*portfolioListFailureStore) ApplyMark(context.Context, portfolio.Mode, string, string, string, string, func(portfolio.Position) (portfolio.Position, error)) (portfolio.Position, error) {
	return portfolio.Position{}, nil
}
func (*portfolioListFailureStore) Upsert(context.Context, portfolio.Position) error { return nil }
func (store *portfolioListFailureStore) List(ctx context.Context, _ portfolio.Mode, _, _ string) ([]portfolio.Position, error) {
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
	calls     int
	failFirst bool
}

func (fixture *manualGatewayFixture) Execute(_ context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	fixture.calls++
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
