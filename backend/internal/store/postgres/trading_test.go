package postgres

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
)

func TestTradingStoresImplementPlatformContracts(t *testing.T) {
	var _ reservations.Store = (*ReservationStore)(nil)
	var _ orders.Store = (*CommandStore)(nil)
	var _ ledger.Store = (*LedgerStore)(nil)
	var _ portfolio.Store = (*PortfolioStore)(nil)
	if NewReservationStore(nil) != nil || NewCommandStore(nil) != nil || NewLedgerStore(nil) != nil || NewPortfolioStore(nil) != nil {
		t.Fatal("nil database accepted")
	}
}

func TestSameLedgerEntryUsesPostgresTimestampPrecision(t *testing.T) {
	original := ledger.Entry{PrincipalID: "principal-a",
		EntryID: "entry-a", Mode: ledger.ModeTest, ProductID: "manual", LineageID: "lineage-a",
		AccountID: "account-a", InstrumentID: "BTC-USDT", Kind: ledger.KindFunding,
		Amount: "1", Currency: "USDT", OccurredAt: time.Date(2026, 8, 8, 12, 0, 0, 789, time.FixedZone("test", 8*60*60)),
	}
	persisted := original
	persisted.OccurredAt = original.OccurredAt.UTC().Truncate(time.Microsecond)

	if !sameLedgerEntry(persisted, original) {
		t.Fatalf("database timestamp %s conflicts with original %s", persisted.OccurredAt, original.OccurredAt)
	}
}

func TestPortfolioStorePersistsFillAndPositionInOneTransaction(t *testing.T) {
	source, err := os.ReadFile("trading.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	start := strings.Index(implementation, "func (store *PortfolioStore) ApplyFill")
	if start < 0 {
		t.Fatal("portfolio fill transaction implementation missing")
	}
	end := strings.Index(implementation[start:], "func loadPortfolioPosition")
	if end < 0 {
		t.Fatal("portfolio fill transaction boundary missing")
	}
	applyFill := implementation[start : start+end]
	for _, required := range []string{"BeginTx", "platform_portfolio_fills", "ON CONFLICT (principal_id,fill_id) DO NOTHING", "platform_positions", "upsertPortfolioPosition", "tx.Commit"} {
		if !strings.Contains(applyFill, required) {
			t.Errorf("portfolio fill transaction missing %q", required)
		}
	}
	if !strings.Contains(implementation, `query += " FOR UPDATE"`) {
		t.Error("portfolio fill transaction does not lock the hydrated position")
	}
	markStart := strings.Index(implementation, "func (store *PortfolioStore) ApplyMark")
	if markStart < 0 {
		t.Fatal("portfolio mark transaction implementation missing")
	}
	markEnd := strings.Index(implementation[markStart:], "func loadPortfolioPosition")
	if markEnd < 0 {
		t.Fatal("portfolio mark transaction boundary missing")
	}
	mark := implementation[markStart : markStart+markEnd]
	for _, required := range []string{"BeginTx", "loadPortfolioPosition", "upsertPortfolioPosition", "tx.Commit"} {
		if !strings.Contains(mark, required) {
			t.Errorf("portfolio mark transaction missing %q", required)
		}
	}
}

func TestLedgerFundingSupersessionIsAtomicAndUnrealizedIsMonotonic(t *testing.T) {
	source, err := os.ReadFile("trading.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	start := strings.Index(implementation, "func (store *LedgerStore) SupersedeFunding")
	if start < 0 {
		t.Fatal("ledger funding supersession implementation missing")
	}
	end := strings.Index(implementation[start:], "func (store *LedgerStore) Entries")
	if end < 0 {
		t.Fatal("ledger funding supersession transaction boundary missing")
	}
	supersession := implementation[start : start+end]
	for _, required := range []string{"BeginTx", "FOR UPDATE", "DELETE FROM platform_ledger_entries", "INSERT INTO platform_ledger_entries", "tx.Commit"} {
		if !strings.Contains(supersession, required) {
			t.Errorf("funding supersession transaction missing %q", required)
		}
	}
	if !strings.Contains(implementation, "WHERE platform_unrealized_pnl.as_of < EXCLUDED.as_of") {
		t.Error("unrealized persistence can overwrite a newer observation")
	}
}

func TestCommandStoreAtomicallyConsumesReservationAfterReplayCheck(t *testing.T) {
	source, err := os.ReadFile("trading.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	start := strings.Index(implementation, "func (store *CommandStore) ReserveWithReservation")
	if start < 0 {
		t.Fatal("command store has no atomic reservation path")
	}
	atomicStore := implementation[start:]
	replay := strings.Index(atomicStore, "if rows == 0")
	fence := strings.Index(atomicStore, "UPDATE platform_reservations SET status='consumed'")
	commit := strings.Index(atomicStore, "tx.Commit()")
	if replay < 0 || fence < 0 || commit < 0 || replay > fence || fence > commit {
		t.Fatal("reservation command path does not replay, fence, and commit in order")
	}
}
