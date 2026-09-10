package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

type feeReviewPositions struct {
	portfolio.Store
	positions []portfolio.Position
	err       error
}

func (store feeReviewPositions) List(context.Context, string, string, string) ([]portfolio.Position, error) {
	return store.positions, store.err
}

func TestPnLDoesNotPresentUnreviewedHistoryAsVerifiedEarnings(t *testing.T) {
	for _, test := range []struct {
		name      string
		positions []portfolio.Position
		err       error
		status    int
	}{
		{name: "review", positions: []portfolio.Position{{LineageID: "l", InstrumentID: "BTC-USDT", AccountingStatus: "legacy_fee_review"}}, status: 409},
		{name: "other lineage", positions: []portfolio.Position{{LineageID: "other", InstrumentID: "BTC-USDT", AccountingStatus: "legacy_fee_review"}}, status: 200},
		{name: "store unavailable", err: errors.New("private database details"), status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := tradingHandler{options: TradingOptions{Principal: func(*http.Request) (string, error) { return "p", nil }, Portfolio: portfolio.NewPersistentProjection(feeReviewPositions{positions: test.positions, err: test.err}), Ledger: ledger.NewService(ledger.NewMemoryStore(), time.Now)}}
			response := httptest.NewRecorder()
			handler.pnl(response, httptest.NewRequest(http.MethodGet, "/api/v1/pnl?productId=manual&lineageId=l&accountId=a&instrumentId=BTC-USDT", nil))
			if response.Code != test.status || strings.Contains(response.Body.String(), "private database details") {
				t.Fatalf("response=%d %s", response.Code, response.Body.String())
			}
			if test.status == 409 && !strings.Contains(response.Body.String(), "ACCOUNTING_REVIEW_REQUIRED") {
				t.Fatalf("missing review reason=%s", response.Body.String())
			}
		})
	}
}

func TestLedgerRetainsHistoricalAmountAndExposesReviewStatusByExactScope(t *testing.T) {
	ctx := context.Background()
	book := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	for _, lineage := range []string{"review", "ready"} {
		if err := book.Record(ctx, ledger.Entry{PrincipalID: "p", ProductID: "manual", LineageID: lineage, AccountID: "a", InstrumentID: "BTC-USDT", EntryID: lineage, Kind: ledger.KindFee, Amount: "-0.1", Currency: "USDT", OccurredAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	handler := tradingHandler{options: TradingOptions{Principal: func(*http.Request) (string, error) { return "p", nil }, Portfolio: portfolio.NewPersistentProjection(feeReviewPositions{positions: []portfolio.Position{{PrincipalID: "p", ProductID: "manual", LineageID: "review", AccountID: "a", InstrumentID: "BTC-USDT", AccountingStatus: "legacy_fee_review"}}}), Ledger: book}}
	response := httptest.NewRecorder()
	handler.ledgerEntries(response, httptest.NewRequest(http.MethodGet, "/api/v1/ledger", nil))
	if response.Code != 200 || strings.Count(response.Body.String(), `"accountingStatus":"legacy_fee_review"`) != 1 || strings.Count(response.Body.String(), `"amount":"-0.1"`) != 2 {
		t.Fatalf("raw history/review scope=%d %s", response.Code, response.Body.String())
	}
}
