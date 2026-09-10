package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	consumer "github.com/QuantProcessing/boltertrader/clients/gateway"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type balanceSnapshotSpot struct {
	exchange.SpotClient
	rows []exchange.Balance
}

func (s balanceSnapshotSpot) Balances(context.Context) ([]exchange.Balance, error) {
	return s.rows, nil
}

func TestBalanceSnapshotNeverSilentlyTruncates(t *testing.T) {
	for _, count := range []int{0, 500, 601, wire.MaxBalanceItems, wire.MaxBalanceItems + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			fixture := newHTTPFixture(t)
			rows := make([]exchange.Balance, count)
			for i := range rows {
				rows[i] = exchange.Balance{Asset: fmt.Sprintf("ASSET%d", i), Available: decimal.NewFromInt(1), Total: decimal.NewFromInt(1)}
			}
			// Exact zeroes may be omitted without hiding money.
			rows = append(rows, exchange.Balance{Asset: "ZERO"})
			fixture.accounts.spot = balanceSnapshotSpot{rows: rows}
			server := httptest.NewServer(fixture.handler)
			defer server.Close()
			client, err := consumer.New(consumer.Config{URL: server.URL, Token: "test-token", HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Balances(t.Context(), "acct-a")
			if count > wire.MaxBalanceItems {
				if err == nil || got != nil {
					t.Fatalf("oversized snapshot accepted: %d %v", len(got), err)
				}
			} else if err != nil || len(got) != count || got == nil {
				t.Fatalf("count=%d result=%d err=%v", count, len(got), err)
			}
		})
	}
}

func TestBalanceSnapshotRejectsPagination(t *testing.T) {
	fixture := newHTTPFixture(t)
	for _, query := range []string{"pagination.limit=1", "pagination.cursor=next"} {
		req := httptest.NewRequest(http.MethodGet, "/v1/balances?account_id=acct-a&"+query, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		result := httptest.NewRecorder()
		fixture.handler.ServeHTTP(result, req)
		if result.Code != http.StatusBadRequest || !strings.Contains(result.Body.String(), "invalid_request") {
			t.Fatalf("pagination accepted: %d %s", result.Code, result.Body.String())
		}
	}
}
