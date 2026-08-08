package httpapi

import (
	"context"
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
)

func TestRefreshAccountingCoversPersistedExposureAcrossExecutionStates(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		state      work.FundingExecutionState
		quantity   string
		position   bool
		wantIngest int
		wantErr    bool
	}{
		{name: "partially hedged nonzero", state: work.FundingExecutionPartiallyHedged, quantity: "0.5", position: true, wantIngest: 1},
		{name: "compensating nonzero", state: work.FundingExecutionCompensating, quantity: "0.5", position: true, wantIngest: 1},
		{name: "uncertain nonzero", state: work.FundingExecutionUncertain, quantity: "-0.5", position: true, wantIngest: 1},
		{name: "manual intervention nonzero", state: work.FundingExecutionManual, quantity: "-0.5", position: true, wantIngest: 1},
		{name: "closed nonzero", state: work.FundingExecutionClosed, quantity: "0.5", position: true, wantIngest: 1},
		{name: "closed zero", state: work.FundingExecutionClosed, quantity: "0", position: true, wantIngest: 0},
		{name: "nonclosed without exposure", state: work.FundingExecutionPreviewed, position: false, wantIngest: 0},
		{name: "closed without zero proof", state: work.FundingExecutionClosed, position: false, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executions := work.NewMemoryFundingExecutionStore()
			_, _, err := executions.ReserveFundingExecution(context.Background(), work.FundingExecution{
				ExecutionID: "execution-a", PrincipalID: "principal-a", Mode: orders.ModeTest, LineageID: "lineage-a",
				ParentCommandID: "parent-a", PlanKey: "plan-a", PreviewHash: "hash-a", IntentKind: "funding.open", State: test.state,
				Legs:      []work.FundingExecutionLeg{{Draft: work.FundingOrderDraft{AccountID: "account-a", InstrumentID: "BTC-USDT"}}},
				CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			ingestor := new(accountingIngestionRecorder)
			var positions fundingPositionFixture
			if test.position {
				positions = fundingPositionFixture{{Mode: portfolio.ModeTest, ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT", SettlementCurrency: "USDT", Quantity: test.quantity}}
			}
			projection := &sharedFundingProjection{options: SharedFundingProjectionOptions{
				Executions: executions,
				Positions:  positions,
				Accounting: ingestor,
				Now:        func() time.Time { return now },
			}}
			scopes, err := projection.RefreshAccounting(context.Background(), "principal-a", "test")
			if test.wantErr {
				if err == nil {
					t.Fatal("RefreshAccounting() succeeded without closed-zero proof")
				}
				return
			}
			if err != nil {
				t.Fatalf("RefreshAccounting() error=%v", err)
			}
			if len(scopes) != 1 || !scopes.owns("lineage-a", "account-a", "BTC-USDT") {
				t.Fatalf("Funding read scopes=%+v", scopes)
			}
			if len(ingestor.requests) != test.wantIngest {
				t.Fatalf("accounting ingestions=%d, want %d", len(ingestor.requests), test.wantIngest)
			}
		})
	}
}

func TestFundingBrowserReadsExcludeForeignPrincipalProjections(t *testing.T) {
	now := time.Date(2026, 8, 8, 13, 30, 0, 0, time.UTC)
	executions := work.NewMemoryFundingExecutionStore()
	_, _, err := executions.ReserveFundingExecution(context.Background(), work.FundingExecution{
		ExecutionID: "execution-owned", PrincipalID: "principal-a", Mode: orders.ModeTest, LineageID: "lineage-owned",
		ParentCommandID: "parent-owned", PlanKey: "plan-owned", PreviewHash: "hash-owned", IntentKind: "funding.open", State: work.FundingExecutionClosed,
		Legs:      []work.FundingExecutionLeg{{Draft: work.FundingOrderDraft{AccountID: "account-owned", InstrumentID: "BTC-USDT"}}},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = executions.ReserveFundingExecution(context.Background(), work.FundingExecution{
		ExecutionID: "execution-foreign", PrincipalID: "principal-b", Mode: orders.ModeTest, LineageID: "lineage-foreign",
		ParentCommandID: "parent-foreign", PlanKey: "plan-foreign", PreviewHash: "hash-foreign", IntentKind: "funding.open", State: work.FundingExecutionHedged,
		Legs:      []work.FundingExecutionLeg{{Draft: work.FundingOrderDraft{AccountID: "account-foreign", InstrumentID: "ETH-USDT"}}},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	positions := portfolio.NewProjection()
	for _, fill := range []portfolio.Fill{
		{PrincipalID: "principal-a", FillID: "fill-owned-open", CommandID: "command-owned-open", LineageID: "lineage-owned", ProductID: "funding", AccountID: "account-owned", InstrumentID: "BTC-USDT", Mode: portfolio.ModeTest, Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now.Add(-time.Second)},
		{PrincipalID: "principal-a", FillID: "fill-owned-close", CommandID: "command-owned-close", LineageID: "lineage-owned", ProductID: "funding", AccountID: "account-owned", InstrumentID: "BTC-USDT", Mode: portfolio.ModeTest, Side: portfolio.SideSell, Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now},
		{PrincipalID: "principal-a", FillID: "fill-foreign", CommandID: "command-foreign", LineageID: "lineage-foreign", ProductID: "funding", AccountID: "account-foreign", InstrumentID: "ETH-USDT", Mode: portfolio.ModeTest, Side: portfolio.SideBuy, Quantity: "1", Price: "200", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now},
	} {
		if _, _, err := positions.ApplyFill(context.Background(), fill); err != nil {
			t.Fatal(err)
		}
	}
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	for _, entry := range []ledger.Entry{
		{PrincipalID: "principal-a", EntryID: "entry-owned", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-owned", AccountID: "account-owned", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding, Amount: "1", Currency: "USDT", OccurredAt: now},
		{PrincipalID: "principal-a", EntryID: "entry-foreign", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-foreign", AccountID: "account-foreign", InstrumentID: "ETH-USDT", Kind: ledger.KindFunding, Amount: "9", Currency: "USDT", OccurredAt: now},
	} {
		if err := ledgerService.Record(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
	}
	projection := &sharedFundingProjection{options: SharedFundingProjectionOptions{
		Executions: executions, Positions: positions, Accounting: new(accountingIngestionRecorder), Now: func() time.Time { return now },
	}}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservations.NewService(reservations.NewMemoryStore(), time.Now),
		Commands:     orders.NewService(orders.NewMemoryStore(), nil, time.Now), Portfolio: positions, Ledger: ledgerService,
		FundingAccounting: projection,
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path      string
		owned     string
		forbidden string
	}{
		{path: "/api/v1/positions?productId=funding", owned: "lineage-owned", forbidden: "lineage-foreign"},
		{path: "/api/v1/ledger?productId=funding", owned: "entry-owned", forbidden: "entry-foreign"},
	} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.owned) || strings.Contains(response.Body.String(), test.forbidden) {
			t.Fatalf("GET %s response=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}

	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/pnl?productId=funding&lineageId=lineage-foreign&accountId=account-foreign&instrumentId=ETH-USDT", nil))
	if response.Code == http.StatusOK || strings.Contains(response.Body.String(), `"funding":"9"`) {
		t.Fatalf("foreign Funding PnL returned status=%d body=%s", response.Code, response.Body.String())
	}
}
