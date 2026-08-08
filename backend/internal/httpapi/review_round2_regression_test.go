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
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
)

func TestFundingReadFailsClosedWhenAccountingRefresherIsNil(t *testing.T) {
	mux := http.NewServeMux()
	err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now),
		Portfolio:    portfolio.NewProjection(),
		Ledger:       ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Principal: func(*http.Request) (string, string, error) {
			return "principal-a", "test", nil
		},
		AuthorizeMutation: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/positions?productId=funding", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"code":"AUTHORITATIVE_ACCOUNTING_UNAVAILABLE"`) {
		t.Fatalf("nil accounting refresher response=%d %s", response.Code, response.Body.String())
	}
}

func TestRefreshAccountingIgnoresForeignPrincipalPositions(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	execution := work.FundingExecution{
		ExecutionID: "execution-owned", PrincipalID: "principal-a", Mode: orders.ModeTest, LineageID: "lineage-owned",
		ParentCommandID: "parent", PlanKey: "plan", PreviewHash: "hash", IntentKind: "funding.open", State: work.FundingExecutionHedged,
		Legs:      []work.FundingExecutionLeg{{Draft: work.FundingOrderDraft{AccountID: "account-owned", InstrumentID: "BTC-USDT"}}},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	}
	executions := &principalScopedExecutionFixture{owned: execution}
	ingestor := new(accountingIngestionRecorder)
	projection := &sharedFundingProjection{options: SharedFundingProjectionOptions{
		Executions: executions,
		Positions: fundingPositionFixture{
			{Mode: portfolio.ModeTest, ProductID: "funding", LineageID: "lineage-owned", AccountID: "account-owned", InstrumentID: "BTC-USDT", SettlementCurrency: "USDT", Quantity: "1"},
			{Mode: portfolio.ModeTest, ProductID: "funding", LineageID: "lineage-foreign", AccountID: "account-foreign", InstrumentID: "ETH-USDT", SettlementCurrency: "USDT", Quantity: "1"},
		},
		Accounting: ingestor,
		Now:        func() time.Time { return now },
	}}
	if _, err := projection.RefreshAccounting(context.Background(), "principal-a", "test"); err != nil {
		t.Fatalf("foreign principal position poisoned refresh: %v", err)
	}
	if executions.listPrincipal != "principal-a" || executions.listMode != orders.ModeTest {
		t.Fatalf("execution list scope=%q/%q, want principal-a/test", executions.listPrincipal, executions.listMode)
	}
	if len(executions.requestedLineages) != 0 {
		t.Fatalf("refresh performed per-lineage execution lookups=%v", executions.requestedLineages)
	}
	if len(ingestor.requests) != 1 || ingestor.requests[0].LineageID != "lineage-owned" {
		t.Fatalf("accounting ingestions=%+v, want owned lineage only", ingestor.requests)
	}
}

type principalScopedExecutionFixture struct {
	work.FundingExecutionStore
	owned             work.FundingExecution
	requestedLineages []string
	listPrincipal     string
	listMode          orders.Mode
}

func (fixture *principalScopedExecutionFixture) GetFundingExecution(_ context.Context, principal string, mode orders.Mode, lineage string) (work.FundingExecution, error) {
	fixture.requestedLineages = append(fixture.requestedLineages, lineage)
	if principal == fixture.owned.PrincipalID && mode == fixture.owned.Mode && lineage == fixture.owned.LineageID {
		return fixture.owned, nil
	}
	return work.FundingExecution{}, errors.New("execution is not owned by principal")
}

func (fixture *principalScopedExecutionFixture) ListFundingExecutions(_ context.Context, principal string, mode orders.Mode) ([]work.FundingExecution, error) {
	fixture.listPrincipal, fixture.listMode = principal, mode
	if principal == fixture.owned.PrincipalID && mode == fixture.owned.Mode {
		return []work.FundingExecution{fixture.owned}, nil
	}
	return nil, nil
}

type accountingIngestionRecorder struct{ requests []accounting.IngestionRequest }

func (recorder *accountingIngestionRecorder) Ingest(_ context.Context, request accounting.IngestionRequest) error {
	recorder.requests = append(recorder.requests, request)
	return nil
}
