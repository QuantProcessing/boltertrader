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
)

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
