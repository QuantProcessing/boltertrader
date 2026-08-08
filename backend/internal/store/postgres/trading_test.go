package postgres

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

type failingScanner struct{ err error }

func (scanner failingScanner) Scan(...any) error { return scanner.err }

func TestScanWorkRegistrationPreservesDatabaseCause(t *testing.T) {
	cause := errors.New("registration scan cause")
	if _, err := scanWorkRegistration(failingScanner{err: cause}); !errors.Is(err, cause) {
		t.Fatalf("scan error=%v does not preserve %v", err, cause)
	}
}

func TestTradingStoresImplementPlatformContracts(t *testing.T) {
	var _ reservations.Store = (*ReservationStore)(nil)
	var _ orders.Store = (*CommandStore)(nil)
	var _ ledger.Store = (*LedgerStore)(nil)
	var _ portfolio.Store = (*PortfolioStore)(nil)
	var _ work.Store = (*WorkStore)(nil)
	if NewReservationStore(nil) != nil || NewCommandStore(nil) != nil || NewLedgerStore(nil) != nil || NewPortfolioStore(nil) != nil || NewWorkStore(nil) != nil {
		t.Fatal("nil database accepted")
	}
}

func TestSameLedgerEntryUsesPostgresTimestampPrecision(t *testing.T) {
	original := ledger.Entry{PrincipalID: "principal-a",
		EntryID: "entry-a", Mode: ledger.ModeTest, ProductID: "funding", LineageID: "lineage-a",
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

func TestWorkStoreClaimsOnlyDueItemsAndRevalidatesIntentReplayAuthority(t *testing.T) {
	source, err := os.ReadFile("work.go")
	if err != nil {
		t.Fatal(err)
	}
	implementation := string(source)
	if !strings.Contains(implementation, "i.not_before<=$1") {
		t.Fatal("work claim does not fence future not_before items")
	}
	intentStart := strings.Index(implementation, "func (store *WorkStore) RecordIntent")
	if intentStart < 0 {
		t.Fatal("work store has no intent implementation")
	}
	intentStore := implementation[intentStart:]
	if !strings.Contains(intentStore, "hash := work.IntentRequestHash(req)") {
		t.Fatal("work intent replay does not use stable economic intent hashing")
	}
	replay := strings.Index(intentStore, `loadWorkOperationValue(ctx, tx, operationKind`)
	activeLease := strings.Index(intentStore, "JOIN platform_work_items i ON i.work_item_id=l.work_item_id")
	if replay < 0 || activeLease < 0 || replay < activeLease {
		t.Fatal("principal-scoped intent idempotency is not loaded after current lease ownership")
	}
	fence := strings.Index(intentStore, "validateCurrentAccountFences(ctx, tx, accountFences, mode)")
	returnReplay := strings.Index(intentStore, "if replayed {")
	if fence < 0 || replay > fence || returnReplay < 0 || returnReplay < fence || !strings.Contains(intentStore[returnReplay:], "replay.Lease = lease") {
		t.Fatal("intent replay does not revalidate current lease and account authority before return")
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
