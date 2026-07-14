package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

func TestCursorOverlapDeduplicationSurvivesFillRetentionEviction(t *testing.T) {
	const (
		accountID      = "acct-overlap"
		fillCount      = 3
		retentionLimit = 2
	)
	generatedAt := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", accountID, generatedAt)
	c := cache.New()
	for i := 0; i < fillCount; i++ {
		clientID := string(rune('a' + i))
		known := order("overlap-"+clientID, btc, "1", enums.StatusNew)
		known.Request.AccountID = accountID
		known.Request.Side = enums.SideBuy
		c.UpsertOrder(known)
		if err := mass.AddOrderReport(model.OrderStatusReport{
			Venue:     "T",
			AccountID: accountID,
			Order:     known,
		}); err != nil {
			t.Fatalf("add order report %d: %v", i, err)
		}
		fill := model.Fill{
			AccountID:    accountID,
			InstrumentID: btc,
			ClientID:     known.Request.ClientID,
			VenueOrderID: known.VenueOrderID,
			TradeID:      "overlap-trade-" + clientID,
			Side:         enums.SideBuy,
			Price:        d("100"),
			Quantity:     d("1"),
			Timestamp:    generatedAt,
		}
		if err := mass.AddFillReport(model.FillReport{
			Venue:      "T",
			AccountID:  accountID,
			Fill:       fill,
			ReportedAt: generatedAt,
		}); err != nil {
			t.Fatalf("add fill report %d: %v", i, err)
		}
	}

	store := NewJournalStateStore(journal.NewMemory())
	businessApplications := 0
	r := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID(accountID).
		WithStateStore(store).
		WithFillRetentionLimit(retentionLimit).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			businessApplications++
			return FillApplyApplied
		})

	first, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.FillsApplied != fillCount || businessApplications != fillCount {
		t.Fatalf("first report=%+v applications=%d, want %d newly applied fills", first, businessApplications, fillCount)
	}
	if len(r.fills) != retentionLimit {
		t.Fatalf("long-lived fill history=%d, want bounded limit %d", len(r.fills), retentionLimit)
	}

	second, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("second overlap run: %v", err)
	}
	if second.FillsApplied != 0 || second.FillsDuplicate != fillCount {
		t.Fatalf("second report=%+v, want zero applications and %d duplicates", second, fillCount)
	}
	if businessApplications != fillCount {
		t.Fatalf("business applications=%d after identical overlap, want unchanged %d", businessApplications, fillCount)
	}
	if len(r.fills) != retentionLimit {
		t.Fatalf("long-lived fill history=%d after overlap, want bounded limit %d", len(r.fills), retentionLimit)
	}
}

func TestDefaultStateOverlapDeduplicationSurvivesFillRetentionEviction(t *testing.T) {
	const (
		accountID      = "acct-overlap-default-state"
		fillCount      = 3
		retentionLimit = 2
	)
	generatedAt := time.Date(2026, 7, 14, 2, 0, 0, 0, time.UTC)
	mass := model.NewExecutionMassStatus("T", accountID, generatedAt)
	c := cache.New()
	for i := 0; i < fillCount; i++ {
		clientID := string(rune('a' + i))
		known := order("default-overlap-"+clientID, btc, "1", enums.StatusNew)
		known.Request.AccountID = accountID
		known.Request.Side = enums.SideBuy
		c.UpsertOrder(known)
		if err := mass.AddOrderReport(model.OrderStatusReport{
			Venue:     "T",
			AccountID: accountID,
			Order:     known,
		}); err != nil {
			t.Fatalf("add order report %d: %v", i, err)
		}
		fill := model.Fill{
			AccountID:    accountID,
			InstrumentID: btc,
			ClientID:     known.Request.ClientID,
			VenueOrderID: known.VenueOrderID,
			TradeID:      "default-overlap-trade-" + clientID,
			Side:         enums.SideBuy,
			Price:        d("100"),
			Quantity:     d("1"),
			Timestamp:    generatedAt,
		}
		if err := mass.AddFillReport(model.FillReport{
			Venue:      "T",
			AccountID:  accountID,
			Fill:       fill,
			ReportedAt: generatedAt,
		}); err != nil {
			t.Fatalf("add fill report %d: %v", i, err)
		}
	}

	businessApplications := 0
	r := New(nil, &snapshotExec{mass: mass, fillHistory: true}, c).
		WithAccountID(accountID).
		WithFillRetentionLimit(retentionLimit).
		WithFillApplier(func(model.Fill, contract.EventMeta) FillApplyResult {
			businessApplications++
			return FillApplyApplied
		})

	for passNumber := 1; passNumber <= 3; passNumber++ {
		report, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("pass %d: %v", passNumber, err)
		}
		if passNumber == 1 {
			if report.FillsApplied != fillCount || report.FillsDuplicate != 0 {
				t.Fatalf("first report=%+v, want %d newly applied fills", report, fillCount)
			}
			continue
		}
		if report.FillsApplied != 0 || report.FillsDuplicate != fillCount {
			t.Fatalf("overlap pass %d report=%+v, want zero applications and %d duplicates", passNumber, report, fillCount)
		}
	}

	if businessApplications != fillCount {
		t.Fatalf("business applications=%d after repeated overlap, want %d", businessApplications, fillCount)
	}
	if len(r.fills) != retentionLimit {
		t.Fatalf("long-lived fill history=%d, want bounded limit %d", len(r.fills), retentionLimit)
	}
}
