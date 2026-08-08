package accounting

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/shopspring/decimal"
)

func TestJournalDuplicateReplayConflictAndSupersession(t *testing.T) {
	journal := NewJournal()
	fact := fundingFact("funding-1", 1, "2")
	if err := journal.Append(fact); err != nil {
		t.Fatal(err)
	}
	if err := journal.Append(fact); err != nil {
		t.Fatalf("replay: %v", err)
	}
	conflict := fact
	conflict.Amount = decimal.NewFromInt(3)
	if err := journal.Append(conflict); !errors.Is(err, ErrFactConflict) {
		t.Fatalf("conflict=%v", err)
	}
	revision := fundingFact("funding-2", 2, "2.5")
	revision.SupersedesFactID = fact.FactID
	if err := journal.Append(revision); err != nil {
		t.Fatal(err)
	}
	fork := fundingFact("funding-3", 3, "3")
	fork.SupersedesFactID = fact.FactID
	if err := journal.Append(fork); !errors.Is(err, ErrSupersessionFork) {
		t.Fatalf("fork=%v", err)
	}
	projection, err := Rebuild([]Fact{fact, revision, watermarkFact("watermark", 4, true), executionFact("execution", 5, ExecutionHedgedOpen)})
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, projection.Funding, "2.5")
}

func TestRebuildCalculatesFillFeesFundingAndSignedPnL(t *testing.T) {
	facts := []Fact{
		fillFact("fill-open", 1, SideBuy, "2", "100", "1"),
		fillFact("fill-close-half", 2, SideSell, "1", "110", "0.5"),
		fundingFact("funding-1", 3, "2"),
		markFact("mark-1", 4, "105"),
		watermarkFact("watermark-1", 5, true),
		executionFact("execution-1", 6, ExecutionHedgedOpen),
	}
	projection, err := Rebuild(facts)
	if err != nil {
		t.Fatal(err)
	}
	assertDecimal(t, projection.RealizedTradingPnL, "10")
	assertDecimal(t, projection.UnrealizedTradingPnL, "5")
	assertDecimal(t, projection.Funding, "2")
	assertDecimal(t, projection.Fees, "1.5")
	assertDecimal(t, projection.NetPnL, "15.5")
	if projection.Status != StatusOpen || !projection.Verified || projection.VerificationStatus != "verified" {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestRebuildRejectsMixedCurrencyPnLScalar(t *testing.T) {
	fill := fillFact("fill-open", 1, SideBuy, "1", "100", "1")
	funding := fundingFact("funding-1", 2, "2")
	fill.FeeAsset = "BNB"
	fill.SettlementAsset = "USDT"
	funding.Asset = "USDT"
	if _, err := Rebuild([]Fact{fill, funding}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("Rebuild error=%v, want ErrCurrencyMismatch", err)
	}
}

func TestRebuildAllowsTwoAccountsWithinOnePositionScope(t *testing.T) {
	long := fillFact("fill-long", 1, SideBuy, "1", "100", "1")
	short := fillFact("fill-short", 2, SideSell, "1", "120", "1")
	short.AccountID, short.InstrumentID, short.CommandID = "account-b", "BTC-USDT-B", "command-b"
	projection, err := Rebuild([]Fact{
		long, short, watermarkFact("watermark", 3, true), executionFact("execution", 4, ExecutionHedgedOpen),
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusOpen || !projection.Verified {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestRebuildRejectsCrossLineageAndPositionContamination(t *testing.T) {
	first := fillFact("fill-a", 1, SideBuy, "1", "100", "1")
	second := fillFact("fill-b", 2, SideBuy, "1", "100", "1")
	second.LineageID, second.PositionID = "lineage-b", "position-b"
	if _, err := Rebuild([]Fact{first, second}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error=%v", err)
	}
}

func TestRebuildRejectsZeroMark(t *testing.T) {
	fact := markFact("mark", 1, "0")
	if _, err := Rebuild([]Fact{fact}); !errors.Is(err, ErrInvalidFact) {
		t.Fatalf("error=%v", err)
	}
}

func TestRebuildBlocksVerifiedCloseOnResidualExposure(t *testing.T) {
	facts := []Fact{
		fillFact("fill-open", 1, SideBuy, "1", "100", "1"),
		fillFact("fill-close", 2, SideSell, "1", "105", "1"),
		watermarkFact("watermark-1", 3, true),
		executionFact("execution-1", 4, ExecutionClosed),
		residualFact("residual-1", 5, "0.01", 1, 0),
	}
	projection, err := Rebuild(facts)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusAttention || projection.Verified || projection.VerificationStatus != "residual_exposure" {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestRebuildDerivesResidualWhenClosedExecutionHasOpenQuantity(t *testing.T) {
	projection, err := Rebuild([]Fact{
		fillFact("fill-open", 1, SideBuy, "1", "100", "1"),
		watermarkFact("watermark", 2, true),
		executionFact("execution", 3, ExecutionClosed),
	})
	if err != nil {
		t.Fatal(err)
	}
	if projection.Status != StatusAttention || projection.Verified || !projection.Residual.LegQuantity.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("projection=%+v", projection)
	}
}

func TestRebuildPositionStatesAndRestartOrderAreDeterministic(t *testing.T) {
	tests := []struct {
		state ExecutionState
		want  PositionStatus
	}{
		{ExecutionHedgedOpen, StatusOpen},
		{ExecutionClosed, StatusClosed},
		{ExecutionCompensatedFlat, StatusClosed},
		{ExecutionManualAttention, StatusAttention},
	}
	for _, test := range tests {
		facts := []Fact{watermarkFact("watermark", 1, true), executionFact("execution", 2, test.state)}
		forward, err := Rebuild(facts)
		if err != nil {
			t.Fatal(err)
		}
		reverse := []Fact{facts[1], facts[0]}
		restarted, err := Rebuild(reverse)
		if err != nil {
			t.Fatal(err)
		}
		if forward.Status != test.want || !reflect.DeepEqual(forward, restarted) {
			t.Fatalf("state=%s forward=%+v restarted=%+v", test.state, forward, restarted)
		}
	}
}

func TestPlatformAdapterEmitsAttributedLedgerAndPortfolioEffects(t *testing.T) {
	ledgerSink := &recordingLedger{}
	portfolioSink := &recordingPortfolio{}
	adapter := NewPlatformAdapter(ledgerSink, portfolioSink)
	fact := fillFact("fill-1", 1, SideBuy, "2", "100", "1")
	fact.FeeAsset = "BNB"
	if err := adapter.Apply(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	if len(portfolioSink.fills) != 1 || portfolioSink.fills[0].ProductID != "funding" || portfolioSink.fills[0].AccountID != "account-a" || portfolioSink.fills[0].CommandID != "command-a" {
		t.Fatalf("fills=%+v", portfolioSink.fills)
	}
	if len(ledgerSink.entries) != 2 || ledgerSink.entries[0].Kind != ledger.KindRealizedPnL || ledgerSink.entries[0].Currency != "USDT" || ledgerSink.entries[1].Kind != ledger.KindFee || ledgerSink.entries[1].Amount != "1" || ledgerSink.entries[1].Currency != "BNB" {
		t.Fatalf("entries=%+v", ledgerSink.entries)
	}
	funding := fundingFact("funding-1", 2, "2.5")
	if err := adapter.Apply(context.Background(), funding); err != nil {
		t.Fatal(err)
	}
	if len(ledgerSink.entries) != 3 || ledgerSink.entries[2].Kind != ledger.KindFunding || ledgerSink.entries[2].Amount != "2.5" || ledgerSink.entries[2].Currency != "USDT" {
		t.Fatalf("entries=%+v", ledgerSink.entries)
	}
}

type recordingLedger struct {
	entries    []ledger.Entry
	unrealized []ledger.Unrealized
}

func (sink *recordingLedger) Record(_ context.Context, entry ledger.Entry) error {
	sink.entries = append(sink.entries, entry)
	return nil
}

func (sink *recordingLedger) SetUnrealized(_ context.Context, value ledger.Unrealized) error {
	sink.unrealized = append(sink.unrealized, value)
	return nil
}

type recordingPortfolio struct{ fills []portfolio.Fill }

func (sink *recordingPortfolio) ApplyFill(_ context.Context, fill portfolio.Fill) (portfolio.Position, portfolio.FillResult, error) {
	sink.fills = append(sink.fills, fill)
	return portfolio.Position{}, portfolio.FillResult{RealizedPnL: "10", SettlementCurrency: fill.SettlementCurrency}, nil
}

func baseFact(id string, revision int64, kind Kind) Fact {
	return Fact{
		FactID: id, Revision: revision, Kind: kind, ProductID: "funding", Mode: "test",
		LineageID: "lineage-a", PositionID: "position-a", AccountID: "account-a", InstrumentID: "BTC-USDT",
		EffectiveAt: time.Date(2026, 8, 5, 0, 0, int(revision), 0, time.UTC),
	}
}

func fillFact(id string, revision int64, side Side, quantity, price, fee string) Fact {
	fact := baseFact(id, revision, KindFill)
	fact.CommandID = "command-a"
	fact.Side, fact.Quantity, fact.Price, fact.Fee = side, decimal.RequireFromString(quantity), decimal.RequireFromString(price), decimal.RequireFromString(fee)
	fact.FeeAsset, fact.SettlementAsset = "USDT", "USDT"
	return fact
}

func fundingFact(id string, revision int64, amount string) Fact {
	fact := baseFact(id, revision, KindFunding)
	fact.Amount, fact.Asset = decimal.RequireFromString(amount), "USDT"
	return fact
}

func markFact(id string, revision int64, price string) Fact {
	fact := baseFact(id, revision, KindMark)
	fact.Price = decimal.RequireFromString(price)
	return fact
}

func watermarkFact(id string, revision int64, verified bool) Fact {
	fact := baseFact(id, revision, KindWatermark)
	fact.Completeness, fact.Verified = CompletenessComplete, verified
	return fact
}

func executionFact(id string, revision int64, state ExecutionState) Fact {
	fact := baseFact(id, revision, KindExecution)
	fact.ExecutionState = state
	return fact
}

func residualFact(id string, revision int64, quantity string, orders, compensations int) Fact {
	fact := baseFact(id, revision, KindResidual)
	fact.Residual = Residual{LegQuantity: decimal.RequireFromString(quantity), OpenOrderCount: orders, PendingCompensation: compensations}
	return fact
}

func assertDecimal(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Fatalf("got %s want %s", got, want)
	}
}
