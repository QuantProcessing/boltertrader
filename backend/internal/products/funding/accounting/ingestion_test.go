package accounting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
)

func TestIngestorProjectsAuthoritativeMarkAndFundingWithReplay(t *testing.T) {
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(8 * time.Hour)
	source := &ingestionSource{
		mark: MarkPrice{AccountID: "account-a", InstrumentID: "BTC-USDT", Price: "110", ObservedAt: end.Add(-time.Second)},
		pages: []FundingPaymentPage{
			{AccountID: "account-a", InstrumentID: "BTC-USDT", Start: start, End: end, Revision: 7, ObservedAt: end, Status: CompletenessIncomplete, NextCursor: "page-2", Items: []FundingPayment{{FactID: "funding-1", AccountID: "account-a", InstrumentID: "BTC-USDT", SettlementAsset: "USDT", Amount: "2", EffectiveAt: start.Add(time.Hour)}}},
			{AccountID: "account-a", InstrumentID: "BTC-USDT", Start: start, End: end, Revision: 7, ObservedAt: end, CompleteThrough: end, Status: CompletenessComplete, Verified: true, Items: []FundingPayment{{FactID: "funding-2", AccountID: "account-a", InstrumentID: "BTC-USDT", SettlementAsset: "USDT", Amount: "-0.5", EffectiveAt: start.Add(2 * time.Hour)}}},
		},
	}
	portfolioProjection := portfolio.NewProjection()
	fill := portfolio.Fill{PrincipalID: "browser-a", FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "funding", Mode: portfolio.ModeTest, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "2", Price: "100", Fee: "1", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: start}
	if _, _, err := portfolioProjection.ApplyFill(context.Background(), fill); err != nil {
		t.Fatal(err)
	}
	ledgerStore := ledger.NewMemoryStore()
	ledgerService := ledger.NewService(ledgerStore, func() time.Time { return end })
	ingestor, err := NewIngestor(source, NewPlatformAdapter(ledgerService, portfolioProjection), nil)
	if err != nil {
		t.Fatal(err)
	}
	request := IngestionRequest{PrincipalID: "browser-a", RequestID: "request-a", ProductID: "funding", Mode: "test", LineageID: "lineage-a", PositionID: "position-a", AccountID: "account-a", InstrumentID: "BTC-USDT", SettlementAsset: "USDT", Start: start, End: end}
	if err := ingestor.Ingest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := ingestor.Ingest(context.Background(), request); err != nil {
		t.Fatalf("replay: %v", err)
	}
	positions := portfolioProjection.List("browser-a", portfolio.ModeTest, "account-a", "funding")
	if len(positions) != 1 || positions[0].MarkPrice != "110" || positions[0].UnrealizedPnL != "20" {
		t.Fatalf("positions=%+v", positions)
	}
	pnl, err := ledgerService.PnL(context.Background(), ledger.Query{PrincipalID: "browser-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-a", AccountID: "account-a", InstrumentID: "BTC-USDT"})
	if err != nil || pnl.UnrealizedPnL != "20" || pnl.Funding != "1.5" || pnl.NetPnL != "21.5" {
		t.Fatalf("pnl=%+v err=%v", pnl, err)
	}
	if len(source.queries) != 4 || source.queries[0].Cursor != "" || source.queries[1].Cursor != "page-2" {
		t.Fatalf("queries=%+v", source.queries)
	}
}

func TestIngestorRejectsUnverifiedAndUnsupportedFundingPayments(t *testing.T) {
	start := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	request := IngestionRequest{PrincipalID: "browser-a", RequestID: "request-a", ProductID: "funding", Mode: "test", LineageID: "lineage-a", PositionID: "position-a", AccountID: "account-a", InstrumentID: "BTC-USDT", SettlementAsset: "USDT", Start: start, End: end}
	for _, test := range []struct {
		name string
		page FundingPaymentPage
		want error
	}{
		{name: "unverified", page: FundingPaymentPage{AccountID: "account-a", InstrumentID: "BTC-USDT", Start: start, End: end, Revision: 1, ObservedAt: end, Status: CompletenessIncomplete}, want: ErrIngestionIncomplete},
		{name: "unsupported", page: FundingPaymentPage{AccountID: "account-a", InstrumentID: "BTC-USDT", Start: start, End: end, Revision: 1, ObservedAt: end, Status: Completeness("unsupported")}, want: ErrIngestionUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			projection := portfolio.NewProjection()
			_, _, _ = projection.ApplyFill(context.Background(), portfolio.Fill{PrincipalID: "principal-a", FillID: "fill-1", CommandID: "command-1", LineageID: "lineage-a", ProductID: "funding", Mode: portfolio.ModeTest, AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy, Quantity: "1", Price: "100", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: start})
			source := &ingestionSource{mark: MarkPrice{AccountID: "account-a", InstrumentID: "BTC-USDT", Price: "100", ObservedAt: start.Add(time.Minute)}, pages: []FundingPaymentPage{test.page}}
			ingestor, _ := NewIngestor(source, NewPlatformAdapter(ledger.NewService(ledger.NewMemoryStore(), nil), projection), nil)
			if err := ingestor.Ingest(context.Background(), request); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want %v", err, test.want)
			}
			positions := projection.List("principal-a", portfolio.ModeTest, "account-a", "funding")
			if len(positions) != 1 || positions[0].MarkPrice != "0" || positions[0].UnrealizedPnL != "0" {
				t.Fatalf("incomplete ingestion partially mutated positions=%+v", positions)
			}
		})
	}
}

type ingestionSource struct {
	mark    MarkPrice
	markErr error
	pages   []FundingPaymentPage
	queries []FundingPaymentQuery
}

func (source *ingestionSource) MarkPrice(context.Context, string, string) (MarkPrice, error) {
	return source.mark, source.markErr
}

func (source *ingestionSource) FundingPayments(_ context.Context, query FundingPaymentQuery) (FundingPaymentPage, error) {
	source.queries = append(source.queries, query)
	index := len(source.queries) - 1
	if len(source.pages) == 0 {
		return FundingPaymentPage{}, errors.New("funding source unavailable")
	}
	return source.pages[index%len(source.pages)], nil
}
