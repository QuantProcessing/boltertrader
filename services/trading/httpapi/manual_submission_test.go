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

type manualApplicationFunc func(context.Context, string, manual.CommandRequest) (manual.Submission, error)

func (f manualApplicationFunc) Submit(ctx context.Context, p string, r manual.CommandRequest) (manual.Submission, error) {
	return f(ctx, p, r)
}

func TestManualTransportRejectsUnauthorizedAndMalformedRequestsBeforeSubmission(t *testing.T) {
	const valid = `{"action":"place","lineageId":"lineage-a","idempotencyKey":"key","order":{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market"}}`
	tests := []struct {
		name, body, key       string
		principalErr, csrfErr error
		want                  int
	}{
		{name: "unauthenticated", body: valid, key: "key", principalErr: errors.New("missing session"), want: 401},
		{name: "csrf", body: valid, key: "key", csrfErr: errors.New("invalid csrf"), want: 403},
		{name: "unknown field", body: strings.Replace(valid, `"action":`, `"reservationId":"r","action":`, 1), key: "key", want: 400},
		{name: "trailing garbage", body: valid + " garbage", key: "key", want: 400},
		{name: "second document", body: valid + " {}", key: "key", want: 400},
		{name: "key mismatch", body: valid, key: "other", want: 400},
		{name: "missing key", body: valid, want: 400},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			err = RegisterTradingRoutes(mux, TradingOptions{Commands: orders.NewService(orders.NewMemoryStore(), time.Now), Portfolio: portfolio.NewProjection(), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now), Catalog: projection,
				Manual: manualApplicationFunc(func(context.Context, string, manual.CommandRequest) (manual.Submission, error) {
					t.Fatal("invalid transport request reached application")
					return manual.Submission{}, nil
				}),
				Principal: func(*http.Request) (string, error) { return "principal-a", test.principalErr }, AuthorizeMutation: func(*http.Request) error { return test.csrfErr }})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, manual.CommandPath, strings.NewReader(test.body))
			req.Header.Set("Idempotency-Key", test.key)
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, req)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
