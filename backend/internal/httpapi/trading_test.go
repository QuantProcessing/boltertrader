package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
)

func TestTradingRoutesRejectUnknownFieldsAndCreateReservation(t *testing.T) {
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
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
