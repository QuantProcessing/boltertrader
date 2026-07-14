package reconcile

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
)

func TestBlockingFindingLogicalIdentityBoundsProtectedJournalRetention(t *testing.T) {
	ctx := context.Background()
	j := journal.NewMemoryWithRetention(1)
	store := NewJournalStateStore(j)
	r := New(nil, nil, cache.New())
	scope := ScopeKey{Venue: "T", AccountID: "acct-poison"}
	logicalIDs := make(map[string]struct{})
	var latestPass PassHeader
	var latestCreatedAt time.Time

	for i := 0; i < 5; i++ {
		at := time.Date(2026, 7, 14, 2, i, 0, 0, time.UTC)
		pass := PassHeader{
			PassID:        PassID(scope, at),
			Scope:         scope,
			StartedAt:     at,
			StableEventAt: at,
		}
		finding := r.finding(
			pass,
			StreamFills,
			FindingBlocking,
			"FILL_WITHOUT_ORDER",
			"fill report could not be matched or materialized",
			true,
		)
		finding.CreatedAt = at
		if err := store.RecordFinding(ctx, finding); err != nil {
			t.Fatalf("record finding %d: %v", i, err)
		}
		logicalIDs[finding.ID] = struct{}{}
		latestPass = pass
		latestCreatedAt = at

		if err := j.AppendReport(ctx, journal.ReportRecord{
			RecordID:   fmt.Sprintf("diagnostic-%d", i),
			ReportedAt: at,
		}); err != nil {
			t.Fatalf("append diagnostic %d: %v", i, err)
		}
	}

	if len(logicalIDs) != 1 {
		t.Fatalf("logical blocking finding ids=%d, want one stable id across passes", len(logicalIDs))
	}
	var logicalID string
	for id := range logicalIDs {
		logicalID = id
	}
	latest := store.findings[logicalID]
	if latest.PassID != latestPass.PassID || !latest.CreatedAt.Equal(latestCreatedAt) {
		t.Fatalf("in-memory finding metadata=%+v, want latest pass=%s created_at=%s", latest, latestPass.PassID, latestCreatedAt)
	}
	open, err := store.LoadOpenFindings(ctx, scope)
	if err != nil {
		t.Fatalf("load open findings: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open findings=%d, want one logical blocking condition", len(open))
	}

	protectedReports := 0
	for _, record := range j.Records() {
		finding, isFinding := decodeFindingRecord(record.Payload)
		if _, tracked := logicalIDs[finding.ID]; isFinding && tracked {
			protectedReports++
		}
	}
	if protectedReports != 1 {
		t.Fatalf("protected finding reports=%d, want one", protectedReports)
	}
	if got := len(j.Records()); got > 2 {
		t.Fatalf("retained journal records=%d, want one protected finding plus one diagnostic", got)
	}
}

func TestFindingIdentitySeparatesLogicalConditionsAndKeepsDiagnosticsPerPass(t *testing.T) {
	at := time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)
	scope := ScopeKey{Venue: "T", AccountID: "acct-a"}
	pass := PassHeader{PassID: PassID(scope, at), Scope: scope}
	r := New(nil, nil, cache.New())
	base := r.finding(pass, StreamFills, FindingBlocking, "POISON", "same message", true)

	variants := []Finding{
		r.finding(PassHeader{PassID: PassID(ScopeKey{Venue: "T", AccountID: "acct-b"}, at), Scope: ScopeKey{Venue: "T", AccountID: "acct-b"}}, StreamFills, FindingBlocking, "POISON", "same message", true),
		r.finding(pass, StreamOrders, FindingBlocking, "POISON", "same message", true),
		r.finding(pass, StreamFills, FindingBlocking, "OTHER", "same message", true),
		r.finding(pass, StreamFills, FindingBlocking, "POISON", "different message", true),
	}
	for i, variant := range variants {
		if variant.ID == base.ID {
			t.Fatalf("variant %d shares id %q with a different logical blocking condition", i, base.ID)
		}
	}

	nextPass := PassHeader{PassID: PassID(scope, at.Add(time.Minute)), Scope: scope}
	nextBlocking := r.finding(nextPass, StreamFills, FindingBlocking, "POISON", "same message", true)
	if nextBlocking.ID != base.ID {
		t.Fatalf("same blocking condition ids differ across passes: %q != %q", nextBlocking.ID, base.ID)
	}
	firstDiagnostic := r.finding(pass, StreamOrders, FindingWarning, "PARTIAL", "diagnostic", false)
	nextDiagnostic := r.finding(nextPass, StreamOrders, FindingWarning, "PARTIAL", "diagnostic", false)
	if firstDiagnostic.ID == nextDiagnostic.ID {
		t.Fatalf("nonblocking per-pass diagnostics unexpectedly share id %q", firstDiagnostic.ID)
	}
}
