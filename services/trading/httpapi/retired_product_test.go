package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func TestRetiredProductHasNoRoutesOrGenericWriteBypass(t *testing.T) {
	commands := newCommandService(orders.NewMemoryStore(), time.Now)
	mux := http.NewServeMux()
	_, err := Register(mux, Options{Trading: &TradingOptions{Commands: commands, Portfolio: portfolio.NewProjection(),
		Ledger:            ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal:         func(*http.Request) (string, error) { return "p", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/api/v1/products/arbitrage/opportunities", "", 404},
		{"GET", "/api/v1/products/arbitrage/positions", "", 404},
		{"POST", "/api/v1/products/arbitrage/execution-previews", `{}`, 404},
		{"POST", "/api/v1/products/arbitrage/commands", `{}`, 404},
		{"GET", "/api/v1/realtime/ws", "", 404},
		{"POST", "/api/v1/commands", `{"accountId":"a","productId":"arbitrage","lineageId":"l","idempotencyKey":"retired","commandKind":"arbitrage.open","payload":{}}`, 404},
		{"POST", "/api/v1/reservations", `{"accountId":"a","productId":"arbitrage","reservationKind":"open","quantity":"1","nettingScope":{"accountId":"a","venue":"binance","instrumentId":"BTC-USDT","venueEnvironment":"live"}}`, 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Idempotency-Key", "retired")
		res := httptest.NewRecorder()
		mux.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, res.Code, res.Body.String())
		}
	}
	for _, route := range Routes() {
		if strings.Contains(route, "arbitrage") || strings.HasSuffix(route, " /api/v1/realtime/ws") {
			t.Fatalf("retired route advertised: %s", route)
		}
	}
}
